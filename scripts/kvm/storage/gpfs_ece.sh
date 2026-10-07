#!/bin/bash
# The erasure code layout of a GPFS cluster (IBM Storage Scale Erasure Code Edition), run on an admin host after the
# cluster runs (shared-storage-design.md §7.9). Every action can run again: what exists is left alone.
#   configure: {"cluster_uuid", "node_class", "servers": [ip], "disk_expr", "pagepool_bytes"}
#              the mmvdisk node class of the servers, their disk topology (only the claimed disks, disk_expr), the
#              server configuration; GPFS restarts on one server at a time
#   slots:     {"cluster_uuid", "node_class", "no_slot_map"}
#              real servers need the slot map ecedrivemapping makes; without one (virtual machines, emulated disks)
#              the slot check and the volatile write cache check are turned off and the daemons restarted one at a
#              time
#   create_rg: {"cluster_uuid", "recovery_group", "node_class", "disk_expr"}   result: {"pdisks": [...]}
#              the recovery group on the claimed disks; slow, it formats every log home vdisk
#   create_vs: {"cluster_uuid", "recovery_group", "vdisk_set", "code", "block_size", "set_size_pct"}
#   create_fs: {"cluster_uuid", "fs_name", "vdisk_set", "mount_point"}   result: {"capacity_bytes", "free_bytes"}
#              the file system on the vdisk set, mounted on every node

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

valid_name() { [[ "$1" =~ ^[A-Za-z][A-Za-z0-9_]{0,31}$ ]]; }

# on_node <node> <command>: run a command on a member with the remote shell of the cluster (its key and known_hosts,
# written by the ssh_trust step), by the address of the node: known_hosts surely has that, not always its GPFS name.
# mmdsh is no use here, mmdiag prints nothing under it
function on_node()
{
    local rsh=$run_dir/storage/$cluster_uuid/gpfs_rsh addr
    [ -x $rsh ] || rsh="ssh -o BatchMode=yes"
    addr=$($gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: -v n="$1" '
        $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "daemonNodeName") d = i; if ($i == "adminNodeName") m = i; if ($i == "ipAddress") a = i }; next }
        $2 == "clusterNode" && a && ($d == n || $m == n || $a == n) { print $a; exit }')
    $rsh "${addr:-$1}" "$2"
}

# wait_active <node>...: every node active in GPFS, at most 10 minutes
function wait_active()
{
    local i n state ok
    for i in $(seq 1 120); do
        ok=true
        for n in "$@"; do
            state=$($gpfs_bin/mmgetstate -N $n -Y 2>/dev/null | gpfs_y "" state | head -1)
            [ "$state" = active ] || ok=false
        done
        $ok && return 0
        sleep 5
    done
    return 1
}

function do_configure()
{
    local input=$1 nc servers expr bytes topo bad kinds
    nc=$(jq -r .node_class <<<"$input")
    servers=$(jq -r '.servers | join(",")' <<<"$input")
    expr=$(jq -r .disk_expr <<<"$input")
    bytes=$(jq -r .pagepool_bytes <<<"$input")
    valid_name "$nc" || stc_fail "invalid node class name"
    [[ "$servers" =~ ^[0-9.,]+$ ]] || stc_fail "invalid server list"
    [[ "$expr" =~ ^[0-9A-Za-z.:,\;_-]+$ ]] || stc_fail "invalid disk expression"
    [[ "$bytes" =~ ^[0-9]+$ ]] || stc_fail "invalid pagepool"
    if ! $gpfs_bin/mmlsnodeclass $nc >/dev/null 2>&1; then
        stc_progress 10 "creating node class $nc"
        $gpfs_bin/mmvdisk nodeclass create --node-class $nc -N $servers </dev/null || stc_fail "mmvdisk nodeclass create failed"
    fi
    # Every server must see its claimed disks with the same topology, and none may need attention
    topo=$($gpfs_bin/mmvdisk server list --node-class $nc --disk-list "$expr" --disk-topology -Y </dev/null 2>&1)
    echo "$topo"
    bad=$(gpfs_y_rows serverDiskTopology nodeName needsAttention <<<"$topo" | awk -F'\t' '$2 != "no" {print $1}')
    kinds=$(gpfs_y_rows serverDiskTopology diskTopology <<<"$topo" | sort -u)
    [ -n "$kinds" ] || stc_fail "mmvdisk reported no disk topology for node class $nc"
    [ -z "$bad" ] || stc_fail "the disk topology of $(echo $bad) needs attention"
    [ "$(grep -c . <<<"$kinds")" = 1 ] || stc_fail "the servers have different disk topologies: $(echo $kinds)"
    # mmvdisk puts the configuration of the class in a section of its own; with --update it never lowers the
    # pagepool, so a configured class is left as it is
    if $gpfs_bin/mmlsconfig | grep -q "^\[$nc\]"; then
        echo "node class $nc is configured already"
    else
        stc_progress 30 "configuring the servers (GPFS restarts on one at a time)"
        # pagepool in bytes: mmvdisk takes "8G" for "dynamic"
        $gpfs_bin/mmvdisk server configure --node-class $nc --pagepool $bytes --recycle one </dev/null ||
            stc_fail "mmvdisk server configure failed"
    fi
    wait_active ${servers//,/ } || stc_fail "GPFS is not active on every server after configuring them"
    # The component database (mmlscomp) the health monitor compares with the cluster, else it reports
    # ess_config_mismatch; servers with no enclosure (emulated disks) give no components, so failing here is no reason
    # to stop
    $gpfs_bin/mmdiscovercomp -N $nc </dev/null || echo "mmdiscovercomp failed, going on"
    stc_result "$(jq -cn --arg t "$kinds" '{topology: $t}')"
}

# What a server without a slot map runs with: no strict slot check (a hidden attribute), and no volatile write cache
# check (yes / no). An emulated disk (LIO) takes the DPO bit the drive diagnosis reads with only when it reports a write
# cache, and GNR drains a pdisk that reports one (writes through LIO fileio are synchronous all the same)
test_disk_attrs="nsdRAIDStrictPdiskSlotLocation=0 nsdRAIDDiskCheckVWCE=no"

# checks_off <node>: whether the daemon of a node runs with every one of them off (mmdiag shows 0 for no)
function checks_off()
{
    local config a
    config=$(on_node $1 "$gpfs_bin/mmdiag --config" 2>/dev/null)
    for a in $test_disk_attrs; do
        awk -v a="${a%%=*}" '{for (i = 1; i < NF; i++) if ($i == a) print $(i + 1)}' <<<"$config" | grep -qx 0 ||
            return 1
    done
}

function do_slots()
{
    local input=$1 nc noslot nodes n a missing=""
    nc=$(jq -r .node_class <<<"$input")
    noslot=$(jq -r '.no_slot_map // false' <<<"$input")
    valid_name "$nc" || stc_fail "invalid node class name"
    nodes=$($gpfs_bin/mmlsnodeclass $nc -Y 2>/dev/null | gpfs_y "" memberNodes | tr ',' ' ')
    [ -n "$nodes" ] || stc_fail "node class $nc has no members"
    if [ "$noslot" != "true" ]; then
        # The slot map is made on each server by ecedrivemapping (SAS disks behind a LSI controller, or NVMe)
        for n in $nodes; do
            on_node $n "ls /usr/lpp/mmfs/data/gems/slotmap.yaml /usr/lpp/mmfs/data/gems/*.edf" >/dev/null 2>&1 ||
                missing="$missing $n"
        done
        [ -z "$missing" ] || stc_fail "no slot map on$missing: run ecedrivemapping there first"
        stc_result '{"slot_check": "on"}'
        return
    fi
    # mmchconfig wants 999 typed to write an attribute it does not know (the slot one; a known one does not ask). Set
    # for this node class every time (a value left from a class deleted before does not count), what the daemons run
    # with is checked below
    stc_progress 10 "turning the slot and write cache checks off"
    for a in $test_disk_attrs; do
        printf '999\n' | $gpfs_bin/mmchconfig $a -N $nc || stc_fail "mmchconfig ${a%%=*} failed"
    done
    # The daemons read them when they start: one server at a time, so the cluster keeps its quorum
    for n in $nodes; do
        checks_off $n && continue
        stc_progress 30 "restarting GPFS on $n"
        $gpfs_bin/mmshutdown -N $n || stc_fail "mmshutdown on $n failed"
        $gpfs_bin/mmstartup -N $n || stc_fail "mmstartup on $n failed"
        wait_active $n || stc_fail "GPFS did not come back on $n"
        checks_off $n || stc_fail "GPFS on $n still runs with the slot or write cache check"
    done
    stc_result '{"slot_check": "off", "write_cache_check": "off"}'
}

# rg_pdisks <rg>: the pdisks of a recovery group as JSON: [{name, ip, device, wwn, state}]. mmvdisk pdisk list -Y
# has no device or WWN, mmlspdisk has both in its stanzas; its server is a node name, turned into the address
function rg_pdisks()
{
    local map
    map=$($gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: '
        $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "daemonNodeName") d = i; if ($i == "adminNodeName") m = i; if ($i == "ipAddress") a = i }; next }
        $2 == "clusterNode" && d && a { print $d "\t" $a; print $m "\t" $a }')
    $gpfs_bin/mmlspdisk $1 2>/dev/null | awk -v map="$map" '
        BEGIN {
            n = split(map, lines, "\n")
            for (i = 1; i <= n; i++) {
                split(lines[i], kv, "\t"); ip[kv[1]] = kv[2]; short = kv[1]; sub(/\..*/, "", short); ip[short] = kv[2]
            }
        }
        function flush(   s) {
            if (name != "") {
                # The node of the device path: on scale-out servers the server line names the node serving the
                # recovery group, not the one the disk is in. The server line only for a device path without one
                if (node != "") srv = node
                s = srv; sub(/\..*/, "", s)
                printf "%s\t%s\t%s\t%s\t%s\n", name, (srv in ip) ? ip[srv] : ((s in ip) ? ip[s] : srv), dev, wwn, st
            }
            name = dev = wwn = srv = st = node = ""
        }
        /^pdisk:/ { flush(); next }
        {
            key = $1; val = $0; sub(/^[^=]*= */, "", val); gsub(/"/, "", val)
            if (key == "name") name = val
            else if (key == "device") {
                # The first path: /dev/sdc, or //<node>/dev/sdc for a disk of a scale-out server
                split(val, d, ","); dev = d[1]
                if (dev ~ /^\/\/[^\/]+\//) { node = dev; sub(/^\/\//, "", node); sub(/\/.*/, "", node) }
                sub(/^.*\/dev\//, "", dev)
            }
            else if (key == "WWN") wwn = val
            else if (key == "server") srv = val
            else if (key == "state") st = val
        }
        END { flush() }' |
        jq -R -s -c 'split("\n") | map(select(. != "") | split("\t") | {name: .[0], ip: .[1], device: .[2], wwn: .[3], state: .[4]})'
}

# log_groups <rg>: how many log groups the recovery group has formatted
function log_groups()
{
    $gpfs_bin/mmvdisk recoverygroup list --recovery-group $1 --log-group -Y </dev/null 2>/dev/null |
        awk -F: '$2 == "rgLogGroup" && $3 != "HEADER"' | grep -c .
}

# stop_watcher <pid>: a background watcher and the command it waits in (its sleep)
function stop_watcher()
{
    pkill -P $1 2>/dev/null
    kill $1 2>/dev/null
}

# primary_root <rg>: the root log group on the first server of the group, as the health monitor wants it (it reports
# gnr_rg_not_primary otherwise, a warning against upgrading); mmvdisk may leave it on another server. Moving it is a
# short failover of that log group; a failure is only reported
function primary_root()
{
    local line active first
    line=$($gpfs_bin/mmlsrecoverygroup $1 -Y 2>/dev/null | gpfs_y_rows server ActiveRecoveryGroupServer Servers | head -1)
    active=$(cut -f1 <<<"$line")
    first=$(cut -f2 <<<"$line" | cut -d, -f1)
    [ -n "$first" ] && [ "$active" != "$first" ] || return 0
    stc_progress 90 "moving the root log group of $1 to its first server $first"
    $gpfs_bin/mmvdisk recoverygroup change --recovery-group $1 --log-group root --active $first </dev/null ||
        echo "moving the root log group to $first failed, going on"
}

function do_create_rg()
{
    local input=$1 rg nc expr servers members disks total watcher rc have pdisks i
    rg=$(jq -r .recovery_group <<<"$input")
    nc=$(jq -r .node_class <<<"$input")
    expr=$(jq -r .disk_expr <<<"$input")
    valid_name "$rg" || stc_fail "invalid recovery group name"
    valid_name "$nc" || stc_fail "invalid node class name"
    [[ "$expr" =~ ^[0-9A-Za-z.:,\;_-]+$ ]] || stc_fail "invalid disk expression"
    # The servers and disks the platform claimed: "<ip>:sdb,sdc;<ip>:sdb,sdc;..."
    servers=$(tr ';' '\n' <<<"$expr" | grep -c .)
    disks=$(tr ';' '\n' <<<"$expr" | cut -d: -f2 | tr ',' '\n' | grep -c .)
    # The node class must hold them all: a failed read here would make the log group count below 1 and let a half
    # formatted recovery group through
    members=$($gpfs_bin/mmlsnodeclass $nc -Y 2>/dev/null | gpfs_y "" memberNodes | tr ',' '\n' | grep -c .)
    [ "$members" = "$servers" ] || stc_fail "node class $nc has $members members, the disk expression $servers servers"
    # Two user log groups per server and the root log group; each gets its log home vdisk formatted
    total=$((servers * 2 + 1))
    # The watcher must not hold the lock of the job (fd 5): a sleep of it outliving the job would keep the next step
    # of the cluster waiting
    (
        while sleep 30; do
            n=$(ps -eo args | grep -o 'tscrvdisk RG[0-9]*\(LG[0-9]*\|ROOT\)LOGHOME' | head -1)
            [ -n "$n" ] || continue
            n=${n#tscrvdisk }
            # The root one comes first, then LG001, LG002, ...
            i=1
            [[ "$n" =~ LG([0-9]+)LOGHOME ]] && i=$((10#${BASH_REMATCH[1]} + 1))
            stc_progress $((20 + 70 * (i - 1) / total)) "formatting log home vdisk $i of $total ($n)"
        done
    ) 5>&- &
    watcher=$!
    if $gpfs_bin/mmvdisk recoverygroup list --recovery-group $rg -Y </dev/null >/dev/null 2>&1; then
        echo "recovery group $rg exists already"
    else
        stc_progress 10 "creating recovery group $rg"
        $gpfs_bin/mmvdisk recoverygroup create --recovery-group $rg --node-class $nc --disk-list "$expr" </dev/null
        rc=$?
        [ $rc = 0 ] || { stop_watcher $watcher; stc_fail "mmvdisk recoverygroup create failed"; }
    fi
    # mmvdisk may defer the log vdisks (it did on the emulated disks of the validation): they are made here, and on
    # a retry of a run interrupted half way; with none deferred it only says so
    if [ "$(log_groups $rg)" -lt $total ]; then
        stc_progress 20 "formatting the log vdisks mmvdisk deferred"
        $gpfs_bin/mmvdisk recoverygroup create --complete-log-format </dev/null ||
            { stop_watcher $watcher; stc_fail "mmvdisk recoverygroup create --complete-log-format failed"; }
    fi
    stop_watcher $watcher
    primary_root $rg
    have=$(log_groups $rg)
    [ "$have" -ge $total ] || stc_fail "recovery group $rg has $have of $total log groups"
    $gpfs_bin/mmvdisk recoverygroup list --recovery-group $rg --declustered-array </dev/null
    # Every claimed disk must come back as a pdisk: the platform names the disks after them. mmlspdisk fails while
    # the group is busy, so it is read a few times before the step gives up (a retry of the step finds the group made)
    for i in $(seq 1 10); do
        pdisks=$(rg_pdisks $rg)
        [ "$(jq 'length' <<<"$pdisks" 2>/dev/null)" = "$disks" ] && break
        sleep 15
    done
    have=$(jq 'length' <<<"$pdisks" 2>/dev/null)
    [ "$have" = "$disks" ] || stc_fail "recovery group $rg lists ${have:-no} pdisks, $disks disks were claimed"
    stc_result "$(jq -cn --argjson p "$pdisks" '{pdisks: $p}')"
}

function do_create_vs()
{
    local input=$1 rg vs code bs pct summary
    rg=$(jq -r .recovery_group <<<"$input")
    vs=$(jq -r .vdisk_set <<<"$input")
    code=$(jq -r .code <<<"$input")
    bs=$(jq -r .block_size <<<"$input")
    pct=$(jq -r .set_size_pct <<<"$input")
    valid_name "$rg" && valid_name "$vs" || stc_fail "invalid recovery group or vdisk set name"
    [[ "$code" =~ ^(3WayReplication|4WayReplication|4\+2p|4\+3p|8\+2p|8\+3p)$ ]] || stc_fail "invalid code"
    [[ "$bs" =~ ^[0-9]+[KMkm]$ ]] || stc_fail "invalid block size"
    [[ "$pct" =~ ^[0-9]+$ ]] && [ $pct -ge 1 ] && [ $pct -le 100 ] || stc_fail "invalid set size"
    summary=$(gpfs_vdisksets | awk -v v="$vs" '$1 == v')
    if [ -z "$summary" ]; then
        stc_progress 10 "defining vdisk set $vs"
        $gpfs_bin/mmvdisk vdiskset define --vdisk-set $vs --recovery-group $rg --code $code --block-size ${bs,,} --set-size $pct% </dev/null ||
            stc_fail "mmvdisk vdiskset define failed"
        summary=$(gpfs_vdisksets | awk -v v="$vs" '$1 == v')
    fi
    if [ "$(awk '{print $2}' <<<"$summary")" = yes ]; then
        echo "vdisk set $vs is created already"
    else
        stc_progress 40 "creating the vdisks of $vs"
        $gpfs_bin/mmvdisk vdiskset create --vdisk-set $vs </dev/null || stc_fail "mmvdisk vdiskset create failed"
    fi
    $gpfs_bin/mmvdisk vdiskset list --vdisk-set $vs </dev/null
    stc_result '{}'
}

function do_create_fs()
{
    local input=$1 fs vs mount nodes mounted i df size free
    fs=$(jq -r .fs_name <<<"$input")
    vs=$(jq -r .vdisk_set <<<"$input")
    mount=$(jq -r .mount_point <<<"$input")
    valid_name "$fs" && valid_name "$vs" || stc_fail "invalid file system or vdisk set name"
    [[ "$mount" =~ ^/gpfs/[A-Za-z0-9_]+$ ]] || stc_fail "invalid mount point"
    if $gpfs_bin/mmlsfs $fs >/dev/null 2>&1; then
        echo "file system $fs exists already"
    else
        stc_progress 10 "creating file system $fs"
        # Quotas on: the CloudLand pools are filesets with a quota (§7.4)
        $gpfs_bin/mmvdisk filesystem create --file-system $fs --vdisk-set $vs --mmcrfs -T $mount -A yes -Q yes </dev/null ||
            stc_fail "mmvdisk filesystem create failed"
    fi
    stc_progress 60 "mounting $fs on every node"
    $gpfs_bin/mmmount $fs -a
    nodes=$($gpfs_bin/mmlscluster -Y | gpfs_y clusterNode daemonNodeName | grep -c .)
    for i in $(seq 1 60); do
        mounted=$($gpfs_bin/mmlsmount $fs -L -Y 2>/dev/null | gpfs_y "" nodeIP | sort -u | grep -c .)
        [ "$mounted" -ge "$nodes" ] && break
        stc_progress $((60 + i / 2)) "mounted on $mounted of $nodes nodes"
        sleep 5
    done
    $gpfs_bin/mmlsmount $fs -L
    [ "$mounted" -ge "$nodes" ] || stc_fail "$fs is mounted on $mounted of $nodes nodes after 5 minutes"
    df=$($gpfs_bin/mmdf $fs -Y)
    size=$(gpfs_y fsTotal fsSize <<<"$df" | head -1)
    free=$(gpfs_y fsTotal freeBlocks <<<"$df" | head -1)
    stc_result "$(jq -cn --argjson s "${size:-0}" --argjson f "${free:-0}" --argjson n "$mounted" \
        '{capacity_bytes: ($s * 1024), free_bytes: ($f * 1024), mounted_nodes: $n}')"
}

function stc_main()
{
    local input action
    input=$(cat)
    cluster_uuid=$(jq -r .cluster_uuid <<<"$input")
    action=$(jq -r .action <<<"$input")
    valid_uuid "$cluster_uuid" || stc_fail "invalid cluster uuid"
    [ -x $gpfs_bin/mmvdisk ] || stc_fail "mmvdisk is not installed (the gpfs.gnr packages)"
    stc_lock "gpfs-$cluster_uuid"
    case $action in
        configure|slots|create_rg|create_vs|create_fs) do_$action "$input" ;;
        *) stc_fail "unknown action $action" ;;
    esac
}

stc_run "$@"
