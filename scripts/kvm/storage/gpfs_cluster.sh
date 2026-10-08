#!/bin/bash
# Cluster level GPFS commands, run on an admin host (shared-storage-design.md §7.2, §7.6). Input: {"action", ...}
#   create:   {"cluster_uuid", "cluster_name", "nodes": [{"ip", "quorum", "manager"}], "server_license": [ip],
#              "client_license": [ip], "pagepool_mib"}         result: {"cluster_name", "cluster_id"}
#   start:    {"cluster_uuid"}                                  every node up and active
#   teardown: {"cluster_uuid", "cluster_name", "filesystems": [name], "nsds": [name], "offline": [ip]}
#             offline: members that are offline now; mmdelnode -a may fail on them, they drop the cluster when they
#             leave (stc_leave.sh, run on them once they are back)
#   add:      {"cluster_uuid", "nodes": [{"ip", "quorum", "manager"}], "server_license": [ip], "client_license": [ip]}
#             hosts join a running cluster (§7.5): added, licensed, started and mounting every file system
#   remove:   {"cluster_uuid", "ip", "offline"}     a host leaves; offline: it is gone for good, nothing runs on it
#   roles:    {"cluster_uuid", "ip", "quorum", "server"}   a member becomes a quorum (and manager) node or stops being
#             one (§13.1), online; server: it needs a server license
#   fence:    {"cluster_uuid", "ip"}   a host that is down is expelled and can not join again (§11.2)
#   unfence:  {"cluster_uuid", "ip"}   it may join again, once it removed what was recovered elsewhere
#   finalize: {"cluster_uuid", "filesystems", "recovery_group"}   once every host runs the new release (upgrade, §7.7):
#             the cluster and the file systems take it (mmchconfig release=LATEST, mmchfs -V full), and the recovery
#             group of the erasure code layout its feature version (mmvdisk recoverygroup change --version LATEST);
#             hosts of an older release can not join afterwards, there is no way back
# Every action checks first what is there, so it can run again after a failure.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

function cluster_summary()
{
    local out
    out=$($gpfs_bin/mmlscluster -Y 2>/dev/null) || return 1
    cluster_name=$(gpfs_y clusterSummary clusterName <<<"$out" | head -1)
    cluster_id=$(gpfs_y clusterSummary clusterId <<<"$out" | head -1)
    [ -n "$cluster_name" ]
}

function do_create()
{
    local input=$1 uuid name dir tmp ip designation servers clients pagepool
    uuid=$(jq -r .cluster_uuid <<<"$input")
    name=$(jq -r .cluster_name <<<"$input")
    dir=$run_dir/storage/$uuid
    [ -x $dir/gpfs_rsh ] && [ -f $dir/id_ed25519 ] || stc_fail "this host has no key of the cluster: the ssh_trust step did not run here"
    if cluster_summary; then
        [ "$cluster_name" = "$name" ] || [ "${cluster_name%%.*}" = "$name" ] || stc_fail "the host belongs to GPFS cluster $cluster_name already"
        echo "cluster $cluster_name exists already"
    else
        tmp=$(mktemp)
        for row in $(jq -c '.nodes[]' <<<"$input"); do
            ip=$(jq -r .ip <<<"$row")
            designation=""
            [ "$(jq -r .quorum <<<"$row")" = "true" ] && designation="quorum"
            [ "$(jq -r .manager <<<"$row")" = "true" ] && designation="${designation:+$designation-}manager"
            echo "$ip${designation:+:$designation}" >>$tmp
        done
        echo "nodes:"; cat $tmp
        stc_progress 10 "creating the cluster"
        $gpfs_bin/mmcrcluster -N $tmp -C "$name" -r $dir/gpfs_rsh -R $dir/gpfs_rcp -A || { rm -f $tmp; stc_fail "mmcrcluster failed"; }
        rm -f $tmp
        cluster_summary || stc_fail "the cluster is not there after mmcrcluster"
    fi
    servers=$(jq -r '.server_license | join(",")' <<<"$input")
    clients=$(jq -r '.client_license | join(",")' <<<"$input")
    stc_progress 60 "accepting the licenses and setting the configuration"
    [ -n "$servers" ] && { $gpfs_bin/mmchlicense server --accept -N "$servers" || stc_fail "mmchlicense server failed"; }
    [ -n "$clients" ] && { $gpfs_bin/mmchlicense client --accept -N "$clients" || stc_fail "mmchlicense client failed"; }
    pagepool=$(jq -r '.pagepool_mib // 1024' <<<"$input")
    [[ "$pagepool" =~ ^[0-9]+$ ]] || stc_fail "invalid pagepool"
    $gpfs_bin/mmchconfig adminMode=central,autoBuildGPL=yes,restripeOnDiskFailure=yes,pagepool=${pagepool}M ||
        stc_fail "mmchconfig failed"
    stc_result "$(jq -cn --arg n "$cluster_name" --arg i "$cluster_id" '{cluster_name: $n, cluster_id: $i}')"
}

function do_start()
{
    local i states down total
    cluster_summary || stc_fail "this host is in no GPFS cluster"
    $gpfs_bin/mmstartup -a || echo "mmstartup reported a failure, waiting for the nodes anyway"
    for i in $(seq 1 120); do
        states=$($gpfs_bin/mmgetstate -a -Y 2>/dev/null | gpfs_y "" state)
        total=$(grep -c . <<<"$states")
        down=$(grep -vc '^active$' <<<"$states")
        stc_progress $((i * 100 / 120)) "$((total - down)) of $total nodes active"
        [ "$total" -gt 0 ] && [ "$down" -eq 0 ] && break
        sleep 5
    done
    $gpfs_bin/mmgetstate -a
    [ "$total" -gt 0 ] && [ "$down" -eq 0 ] || stc_fail "$down of $total nodes did not become active in 10 minutes"
    stc_result "$(jq -cn --argjson n "$total" '{active: $n}')"
}

# teardown_ece <input>: the erasure code layer of a cluster goes first, in mmvdisk's order: the file systems on the
# vdisk sets, the vdisk sets, the recovery group, the server configuration, the node class (§7.9). Input "ece":
# {"recovery_group", "node_class"}, the vdisk sets are those on the recovery group; nothing when the cluster has no such
# layer. Each part that is gone already is skipped, so a retry goes on where the last run stopped
function teardown_ece()
{
    local input=$1 rg nc vs fs servers created rgs
    rg=$(jq -r '.ece.recovery_group // empty' <<<"$input")
    [ -n "$rg" ] || return 0
    nc=$(jq -r '.ece.node_class // empty' <<<"$input")
    [[ "$rg" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] && [[ "$nc" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || stc_fail "invalid recovery group or node class"
    for fs in $(jq -r '.filesystems[]?' <<<"$input"); do
        [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || continue
        $gpfs_bin/mmlsfs $fs >/dev/null 2>&1 || continue
        stc_progress 20 "deleting file system $fs"
        $gpfs_bin/mmvdisk filesystem delete --file-system $fs --confirm </dev/null || stc_fail "mmvdisk filesystem delete $fs failed"
    done
    # Every vdisk set on the recovery group, the file system on it first; one only defined (a deployment stopped half
    # way) is undefined
    while read -r vs created fs rgs; do
        [[ ",$rgs," == *",$rg,"* ]] && [[ "$vs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || continue
        if [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]]; then
            stc_progress 25 "deleting file system $fs"
            $gpfs_bin/mmvdisk filesystem delete --file-system $fs --confirm </dev/null || stc_fail "mmvdisk filesystem delete $fs failed"
        fi
        stc_progress 30 "deleting vdisk set $vs"
        if [ "$created" = yes ]; then
            # It asks before deleting vdisks; the cluster is being deleted, so yes
            printf 'yes\n' | $gpfs_bin/mmvdisk vdiskset delete --vdisk-set $vs || stc_fail "mmvdisk vdiskset delete $vs failed"
        fi
        $gpfs_bin/mmvdisk vdiskset undefine --vdisk-set $vs --confirm </dev/null || stc_fail "mmvdisk vdiskset undefine $vs failed"
    done < <(gpfs_vdisksets)
    if $gpfs_bin/mmvdisk recoverygroup list --recovery-group $rg -Y </dev/null >/dev/null 2>&1; then
        stc_progress 40 "deleting recovery group $rg"
        if ! printf 'yes\n' | $gpfs_bin/mmvdisk recoverygroup delete --recovery-group $rg --confirm; then
            # Still in use (a vdisk set the loop above could not see): a configuration problem, not one to force
            vs=$(gpfs_vdisksets | awk -v r="$rg" 'index("," $4 ",", "," r ",") {print $1}')
            [ -z "$vs" ] || stc_fail "recovery group $rg still has vdisk sets: $(echo $vs)"
            # A recovery group left half made (a log vdisk the daemon serves but the configuration does not know, after
            # an interrupted log format) does not delete. Stop its servers and remove it from the configuration only:
            # -p is refused while it is served. The cluster goes next anyway, and its disks are wiped when claimed
            servers=$($gpfs_bin/mmlsnodeclass $nc -Y 2>/dev/null | gpfs_y "" memberNodes)
            [ -n "$servers" ] || stc_fail "mmvdisk recoverygroup delete failed, and node class $nc has no members"
            stc_progress 45 "stopping the servers of $rg to remove it from the configuration"
            $gpfs_bin/mmshutdown -N $servers
            printf 'yes\n' | $gpfs_bin/mmvdisk recoverygroup delete --recovery-group $rg --confirm -p ||
                stc_fail "mmvdisk recoverygroup delete failed"
        fi
    fi
    if $gpfs_bin/mmlsnodeclass $nc >/dev/null 2>&1; then
        $gpfs_bin/mmvdisk server unconfigure --node-class $nc --recycle none </dev/null || stc_fail "mmvdisk server unconfigure failed"
        $gpfs_bin/mmvdisk nodeclass delete --node-class $nc --confirm </dev/null || stc_fail "mmvdisk nodeclass delete failed"
    fi
}

function do_teardown()
{
    local input=$1 uuid name fs nsd list offline
    uuid=$(jq -r .cluster_uuid <<<"$input")
    name=$(jq -r .cluster_name <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    # Only a cluster CloudLand made: the join step marks a host before anything is installed on it, and the name is
    # the one CloudLand gave. A deployment stopped at the precheck leaves no mark, and the host may well belong to the
    # GPFS cluster of somebody else, which a delete must never touch
    if [ ! -f $run_dir/storage/$uuid/member ]; then
        echo "this host never joined storage cluster $uuid: nothing of it to tear down"
        stc_result '{"removed": false}'
        return 0
    fi
    if ! cluster_summary; then
        echo "this host is in no GPFS cluster: nothing to tear down"
        stc_result '{"removed": false}'
        return 0
    fi
    if [ "$cluster_name" != "$name" ] && [ "${cluster_name%%.*}" != "$name" ]; then
        echo "this host is in GPFS cluster $cluster_name, not $name: leaving it alone"
        stc_result '{"removed": false}'
        return 0
    fi
    stc_progress 10 "unmounting"
    $gpfs_bin/mmumount all -a
    teardown_ece "$input"
    for fs in $(jq -r '.filesystems[]?' <<<"$input"); do
        $gpfs_bin/mmlsfs $fs >/dev/null 2>&1 || continue
        stc_progress 30 "deleting file system $fs"
        $gpfs_bin/mmdelfs $fs || stc_fail "mmdelfs $fs failed"
    done
    list=""
    for nsd in $(jq -r '.nsds[]?' <<<"$input"); do
        gpfs_nsd_exists $nsd && list="${list:+$list;}$nsd"
    done
    if [ -n "$list" ]; then
        stc_progress 50 "deleting the NSDs"
        $gpfs_bin/mmdelnsd "$list" || stc_fail "mmdelnsd failed"
    fi
    stc_progress 70 "stopping GPFS"
    $gpfs_bin/mmshutdown -a
    if ! $gpfs_bin/mmdelnode -a; then
        offline=$(jq -r '.offline[]?' <<<"$input" | xargs)
        [ -n "$offline" ] || stc_fail "mmdelnode failed"
        # Every host drops the configuration of the cluster when it leaves, the ones offline now once they are back
        echo "mmdelnode -a failed with members offline ($offline): the hosts drop the cluster when they leave"
    fi
    stc_result '{"removed": true}'
}

# gpfs_node_in <ip>: whether a host is a node of the cluster of this host
function gpfs_node_in()
{
    $gpfs_bin/mmlscluster -Y 2>/dev/null | gpfs_y clusterNode ipAddress | grep -qx "$1"
}

function do_add()
{
    local input=$1 tmp row ip designation added="" servers clients i states down
    cluster_summary || stc_fail "this host is in no GPFS cluster"
    tmp=$(mktemp)
    for row in $(jq -c '.nodes[]' <<<"$input"); do
        ip=$(jq -r .ip <<<"$row")
        [[ "$ip" =~ ^[0-9.]+$ ]] || stc_fail "invalid address $ip"
        added="${added:+$added,}$ip"
        if gpfs_node_in $ip; then
            echo "$ip is a node already"
            continue
        fi
        designation=""
        [ "$(jq -r .quorum <<<"$row")" = "true" ] && designation="quorum"
        [ "$(jq -r .manager <<<"$row")" = "true" ] && designation="${designation:+$designation-}manager"
        echo "$ip${designation:+:$designation}" >>$tmp
    done
    if [ -s $tmp ]; then
        echo "nodes:"; cat $tmp
        stc_progress 10 "adding the nodes"
        $gpfs_bin/mmaddnode -N $tmp || { rm -f $tmp; stc_fail "mmaddnode failed"; }
    fi
    rm -f $tmp
    servers=$(jq -r '.server_license | join(",")' <<<"$input")
    clients=$(jq -r '.client_license | join(",")' <<<"$input")
    stc_progress 30 "accepting the licenses"
    [ -n "$servers" ] && { $gpfs_bin/mmchlicense server --accept -N "$servers" || stc_fail "mmchlicense server failed"; }
    [ -n "$clients" ] && { $gpfs_bin/mmchlicense client --accept -N "$clients" || stc_fail "mmchlicense client failed"; }
    stc_progress 40 "starting GPFS on the new nodes"
    $gpfs_bin/mmstartup -N "$added" || echo "mmstartup reported a failure, waiting for the nodes anyway"
    for i in $(seq 1 120); do
        states=$($gpfs_bin/mmgetstate -N "$added" -Y 2>/dev/null | gpfs_y "" state)
        down=$(grep -vc '^active$' <<<"$states")
        [ -n "$states" ] && [ "$down" -eq 0 ] && break
        stc_progress $((40 + i / 4)) "waiting for the new nodes to be active"
        sleep 5
    done
    $gpfs_bin/mmgetstate -N "$added"
    [ -n "$states" ] && [ "$down" -eq 0 ] || stc_fail "the new nodes did not become active in 10 minutes"
    stc_progress 80 "mounting the file systems on the new nodes"
    $gpfs_bin/mmmount all -N "$added" || stc_fail "mmmount on the new nodes failed"
    stc_result "$(jq -cn --arg n "$added" '{added: ($n | split(","))}')"
}

function do_remove()
{
    local input=$1 ip offline
    ip=$(jq -r .ip <<<"$input")
    offline=$(jq -r '.offline // false' <<<"$input")
    [[ "$ip" =~ ^[0-9.]+$ ]] || stc_fail "invalid address $ip"
    cluster_summary || stc_fail "this host is in no GPFS cluster"
    if ! gpfs_node_in $ip; then
        echo "$ip is not a node of the cluster"
        stc_result '{"removed": false}'
        return 0
    fi
    if [ "$offline" != "true" ]; then
        stc_progress 20 "unmounting and stopping GPFS on $ip"
        $gpfs_bin/mmumount all -N $ip
        $gpfs_bin/mmshutdown -N $ip
    fi
    # A quorum node leaves the quorum first, so the cluster keeps it while the node goes
    if $gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: -v ip="$ip" '
            $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "ipAddress") a = i; if ($i == "designation") d = i }; next }
            $2 == "clusterNode" && $a == ip && $d ~ /quorum/ { found = 1 } END { exit !found }'; then
        stc_progress 50 "taking $ip out of the quorum"
        $gpfs_bin/mmchnode --nonquorum -N $ip || stc_fail "mmchnode --nonquorum failed"
    fi
    stc_progress 70 "deleting node $ip"
    $gpfs_bin/mmdelnode -N $ip || stc_fail "mmdelnode failed"
    stc_result '{"removed": true}'
}

# designation_of <ip>: the designation of a node (quorumManager, quorum, manager or empty)
function designation_of()
{
    $gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: -v ip="$1" '
        $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "ipAddress") a = i; if ($i == "designation") d = i }; next }
        $2 == "clusterNode" && a && $a == ip { print $d }'
}

function do_roles()
{
    local input=$1 ip quorum server designation other fs node
    ip=$(jq -r .ip <<<"$input")
    quorum=$(jq -r '.quorum // false' <<<"$input")
    server=$(jq -r '.server // false' <<<"$input")
    [[ "$ip" =~ ^[0-9.]+$ ]] || stc_fail "invalid address $ip"
    cluster_summary || stc_fail "this host is in no GPFS cluster"
    gpfs_node_in $ip || stc_fail "$ip is not a node of the cluster"
    designation=$(designation_of $ip)
    if [ "$server" = "true" ]; then
        $gpfs_bin/mmchlicense server --accept -N $ip || stc_fail "mmchlicense server failed"
    fi
    if [ "$quorum" = "true" ] && [[ "$designation" != *quorum* ]]; then
        stc_progress 30 "making $ip a quorum node"
        $gpfs_bin/mmchnode --quorum --manager -N $ip || stc_fail "mmchnode --quorum failed"
    elif [ "$quorum" != "true" ] && [[ "$designation" == *quorum* ]]; then
        # The cluster manager and the file system managers move to another quorum node first
        other=$($gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: -v ip="$ip" '
            $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "ipAddress") a = i; if ($i == "designation") d = i }; next }
            $2 == "clusterNode" && a && $a != ip && $d ~ /quorum/ { print $a; exit }')
        [ -n "$other" ] || stc_fail "no other quorum node to take over from $ip"
        if $gpfs_bin/mmlsmgr -c 2>/dev/null | grep -qw "$ip"; then
            stc_progress 20 "moving the cluster manager to $other"
            $gpfs_bin/mmchmgr -c $other || stc_fail "mmchmgr -c failed"
        fi
        while IFS=$'\t' read -r fs node; do
            [ -n "$fs" ] && [ "$node" = "$ip" ] || continue
            stc_progress 30 "moving the manager of $fs to $other"
            $gpfs_bin/mmchmgr $fs $other || stc_fail "mmchmgr $fs failed"
        done < <($gpfs_bin/mmlsmgr -Y 2>/dev/null | awk -F: '
            $2 == "filesystemManager" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "filesystem") f = i; if ($i == "managerIP") m = i }; next }
            $2 == "filesystemManager" && f { print $f "\t" $m }')
        stc_progress 60 "taking $ip out of the quorum"
        $gpfs_bin/mmchnode --nonquorum --client -N $ip || stc_fail "mmchnode --nonquorum failed"
    else
        echo "$ip is ${designation:-a client node} already"
    fi
    stc_result "$(jq -cn --arg d "$(designation_of $ip)" '{designation: $d}')"
}

# gpfs_daemon_name <ip>: the GPFS node name of a member, from its address
function gpfs_daemon_name()
{
    $gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: -v ip="$1" '
        $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "daemonNodeName") n = i; if ($i == "ipAddress") a = i }; next }
        $2 == "clusterNode" && n && a && $a == ip { print $n }'
}

# gpfs_expelled <node name> <address>: whether a node is on the list of the nodes expelled by mmexpelnode, whose lines
# are "<address> (<node name>)" under a dashed line; 0 when it is, 1 when not, 2 when the list could not be read
function gpfs_expelled()
{
    local out
    out=$(timeout 60 $gpfs_bin/mmexpelnode -l 2>/dev/null) || return 2
    awk -v n="$1" -v ip="$2" '
        f && NF && $1 != "(Empty)" { name = $2; gsub(/[()]/, "", name); if ($1 == ip || name == n) found = 1 }
        /^---/ { f = 1 }
        END { exit !found }' <<<"$out"
}

# fence: a host taken for dead is expelled (§11.2). Unlike the expel GPFS does by itself when a lease runs out, this
# one stays when the host comes back and starts GPFS again: it can not mount the file systems, so the instances it
# still runs can not write to disks that are in use elsewhere, until unfence lets it back in
function do_fence()
{
    local ip name
    ip=$(jq -r .ip <<<"$1")
    [[ "$ip" =~ ^[0-9.]+$ ]] || stc_fail "invalid address $ip"
    name=$(gpfs_daemon_name $ip)
    [ -n "$name" ] || stc_fail "$ip is not a node of this cluster"
    gpfs_expelled "$name" $ip
    case $? in
        0) echo "$name is expelled already" ;;
        1)
            stc_progress 30 "expelling $name"
            timeout 600 $gpfs_bin/mmexpelnode -N $name || stc_fail "mmexpelnode -N $name failed"
            ;;
        *) stc_fail "mmexpelnode -l failed" ;;
    esac
    gpfs_expelled "$name" $ip || stc_fail "$name is not on the list of expelled nodes"
    # The file systems stay frozen until GPFS recovered the node: for one that may still be alive it first waits for its
    # lease to run out, up to a minute. The evacuation that waits for this fence would find its pools not reachable
    local m deadline=$((SECONDS + 300))
    stc_progress 60 "waiting for the file systems to recover from the expel of $name"
    for m in $(mount -t gpfs | awk '{print $3}'); do
        until timeout 10 stat -f -c %T "$m" >/dev/null 2>&1 && timeout 10 ls "$m" >/dev/null 2>&1; do
            [ $SECONDS -ge $deadline ] && stc_fail "$m did not recover in 5 minutes after the expel of $name"
            sleep 3
        done
        echo "$m answers"
    done
    stc_result "$(jq -cn --arg n "$name" '{node: $n}')"
}

function do_unfence()
{
    local ip name
    ip=$(jq -r .ip <<<"$1")
    [[ "$ip" =~ ^[0-9.]+$ ]] || stc_fail "invalid address $ip"
    name=$(gpfs_daemon_name $ip)
    if [ -z "$name" ]; then
        # Removed from the cluster: GPFS forgot its expel with it
        echo "$ip is not a node of this cluster (any more): nothing to let back in"
        stc_result "$(jq -cn --arg a "$ip" '{address: $a}')"
        return 0
    fi
    gpfs_expelled "$name" $ip
    case $? in
        0)
            stc_progress 30 "letting $name join again"
            timeout 600 $gpfs_bin/mmexpelnode -r -N $name || stc_fail "mmexpelnode -r -N $name failed"
            ;;
        1) echo "$name is not expelled" ;;
        *) stc_fail "mmexpelnode -l failed" ;;
    esac
    gpfs_expelled "$name" $ip
    case $? in
        0) stc_fail "$name is still on the list of expelled nodes" ;;
        1) ;;
        *) stc_fail "mmexpelnode -l failed" ;;
    esac
    stc_result "$(jq -cn --arg n "$name" '{node: $n}')"
}

# fs_format <file system> <field>: a format version of mmlsfs -V (filesystemVersion, filesystemHighestSupported), as
# "38.00 (6.0.0.0)"
function fs_format()
{
    timeout 60 $gpfs_bin/mmlsfs $1 -V -Y 2>/dev/null | awk -F: -v f="$2" '
        $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "fieldName") n = i; if ($i == "data") d = i }; next }
        n && $n == f { print $d; exit }'
}

function do_finalize()
{
    local fs release rg have top
    stc_progress 10 "raising the cluster to the release its hosts run"
    # GPFS 5.1 and later refuse without a cipher list unless told the clear daemon traffic is what the admin wants
    timeout 900 $gpfs_bin/mmchconfig release=LATEST --accept-empty-cipherlist-security ||
        stc_fail "mmchconfig release=LATEST failed: does every host run the new release, with GPFS up?"
    for fs in $(jq -r '.filesystems[]?' <<<"$1"); do
        [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || stc_fail "invalid file system $fs"
        stc_progress 50 "raising the format of $fs"
        # mmchfs -V asks for a confirmation on its standard input; without one it changes nothing and still exits 0
        printf 'yes\n' | timeout 900 $gpfs_bin/mmchfs $fs -V full || stc_fail "mmchfs $fs -V full failed"
        have=$(fs_format $fs filesystemVersion)
        top=$(fs_format $fs filesystemHighestSupported)
        [ -n "$have" ] && [ "$have" = "$top" ] || stc_fail "$fs is at format ${have:-unknown} after mmchfs -V full, not ${top:-unknown}"
        echo "$fs is at format $have"
    done
    rg=$(jq -r '.recovery_group // ""' <<<"$1")
    if [ -n "$rg" ]; then
        [[ "$rg" =~ ^[A-Za-z][A-Za-z0-9_]{0,31}$ ]] || stc_fail "invalid recovery group name"
        stc_progress 80 "raising recovery group $rg to the feature version of the release"
        timeout 900 $gpfs_bin/mmvdisk recoverygroup change --recovery-group $rg --version LATEST </dev/null ||
            stc_fail "mmvdisk recoverygroup change --version LATEST failed"
    fi
    release=$($gpfs_bin/mmlsconfig minReleaseLevel -Y 2>/dev/null | gpfs_y "" value | head -1)
    stc_result "$(jq -cn --arg r "$release" '{release: $r}')"
}

function stc_main()
{
    local input action uuid
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    action=$(jq -r .action <<<"$input")
    # A fence is urgent and does not wait for a structural job that may hang on the very host that is down
    case "$action" in
        fence | unfence) stc_lock "gpfs-fence-$uuid" ;;
        *) stc_lock "gpfs-$uuid" ;;
    esac
    case "$action" in
        fence) do_fence "$input" ;;
        unfence) do_unfence "$input" ;;
        finalize) do_finalize "$input" ;;
        create) do_create "$input" ;;
        start) do_start ;;
        teardown) do_teardown "$input" ;;
        add) do_add "$input" ;;
        remove) do_remove "$input" ;;
        roles) do_roles "$input" ;;
        *) stc_fail "unknown action $action" ;;
    esac
}

stc_run "$@"
