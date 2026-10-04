#!/bin/bash
# Cluster level GPFS commands, run on an admin host (shared-storage-design.md §7.2, §7.6). Input: {"action", ...}
#   create:   {"cluster_uuid", "cluster_name", "nodes": [{"ip", "quorum", "manager"}], "server_license": [ip],
#              "client_license": [ip], "pagepool_mib"}         result: {"cluster_name", "cluster_id"}
#   start:    {"cluster_uuid"}                                  every node up and active
#   teardown: {"cluster_uuid", "cluster_name", "filesystems": [name], "nsds": [name]}
#   add:      {"cluster_uuid", "nodes": [{"ip", "quorum", "manager"}], "server_license": [ip], "client_license": [ip]}
#             hosts join a running cluster (§7.5): added, licensed, started and mounting every file system
#   remove:   {"cluster_uuid", "ip", "offline"}     a host leaves; offline: it is gone for good, nothing runs on it
#   roles:    {"cluster_uuid", "ip", "quorum", "server"}   a member becomes a quorum (and manager) node or stops being
#             one (§13.1), online; server: it needs a server license
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

function do_teardown()
{
    local input=$1 uuid name fs nsd list
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
    $gpfs_bin/mmdelnode -a || stc_fail "mmdelnode failed"
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

function stc_main()
{
    local input action
    input=$(cat)
    valid_uuid "$(jq -r .cluster_uuid <<<"$input")" || stc_fail "invalid cluster uuid"
    stc_lock "gpfs-$(jq -r .cluster_uuid <<<"$input")"
    action=$(jq -r .action <<<"$input")
    case "$action" in
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
