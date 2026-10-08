#!/bin/bash
# The erasure code layout of a GPFS cluster (IBM Storage Scale Erasure Code Edition), run on an admin host after the
# cluster runs (shared-storage-design.md §7.9). Every action can run again: what exists is left alone.
#   configure:  {"cluster_uuid", "node_class", "servers": [ip], "disk_expr", "pagepool_bytes"}
#               the mmvdisk node class of the servers, their disk topology (only the claimed disks, disk_expr), the
#               server configuration; GPFS restarts on one server at a time
#   slots:      {"cluster_uuid", "node_class", "no_slot_map", "slot_mode", "slot_range": [min, max], "nodes": [ip]}
#               real servers need the slot map ecedrivemapping makes: made here with the slot mode and range when the
#               cluster names them, else it must be there; without one (virtual machines, emulated disks) the slot
#               check and the volatile write cache check are turned off and the daemons restarted one at a time.
#               nodes: only these servers (servers joining), else every server of the node class
#   create_rg:  {"cluster_uuid", "recovery_group", "node_class", "disk_expr"}   result: {"pdisks": [...]}
#               the recovery group on the claimed disks; slow, it formats every log home vdisk
#   create_vs:  {"cluster_uuid", "recovery_group", "sets": [{"vdisk_set", "code", "block_size", "set_size_pct",
#               "da_type", "nsd_usage", "storage_pool"}]}
#               a vdisk set per declustered array: one on a recovery group of one media, a metadata set on the solid
#               state disks and a data set on the HDDs when the group has both (da_type: the media of its array)
#   create_fs:  {"cluster_uuid", "fs_name", "vdisk_sets": [name], "data_pool", "mount_point"}
#               result: {"capacity_bytes", "free_bytes"}
#               the file system on the vdisk sets, mounted on every node; with a data pool (a metadata only system
#               pool) the placement rule sends the files there
#   add_servers: {"cluster_uuid", "node_class", "recovery_group", "servers": [ip], "disk_expr", "full_expr",
#               "total_servers", "no_slot_map", "slot_mode", "slot_range"}   result: {"pdisks": [...]}
#               servers joining a scale-out recovery group: configured like the class (and in it), their topology the
#               same as the others', added with their disks (mmvdisk recoverygroup add); mmvdisk rebalances the
#               stripes over them and then formats their log groups and extends the vdisk sets and file systems
#               (--complete-node-add, run by its callback; run here when the callback did not)
#   remove_server: {"cluster_uuid", "node_class", "recovery_group", "ip"}
#               a server leaving: the file systems give up its vdisk set members, the recovery group its pdisks (the
#               data moves to the other servers), the node class the node. mmvdisk refuses when the servers left can
#               not hold the data or the width of the code
#   replace:    {"cluster_uuid", "recovery_group", "pdisk", "ip", "device", "wwn", "slot_map"}
#               result: {"pdisks": [...]}
#               a failed pdisk replaced by a new disk of the same server: with a slot map mmvdisk finds the new disk
#               in the slot (pdisk replace); without one the new device is named to mmaddpdisk --replace. The new
#               disk takes the name of the old pdisk
#   resize:     {"cluster_uuid", "recovery_group", "disk_expr", "vdisk_set", "code", "block_size", "set_size_pct",
#               "da_type", "nsd_usage", "storage_pool", "fs_name"}   result: {"pdisks": [...]}
#               disks added to every server (the same number each): the recovery group takes them (mmvdisk
#               recoverygroup resize), a new vdisk set takes the share of the array the set size leaves for it, the
#               file system gets it (mmvdisk filesystem add)

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

valid_name() { [[ "$1" =~ ^[A-Za-z][A-Za-z0-9_]{0,31}$ ]]; }
valid_expr() { [[ "$1" =~ ^[0-9A-Za-z.:,\;_-]+$ ]]; }
valid_code() { [[ "$1" =~ ^(3WayReplication|4WayReplication|4\+2p|4\+3p|8\+2p|8\+3p)$ ]]; }

# node_map: the GPFS names of the nodes and their addresses, "<name>\t<ip>" (daemon and admin name, and the short one)
function node_map()
{
    $gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: '
        $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "daemonNodeName") d = i; if ($i == "adminNodeName") m = i; if ($i == "ipAddress") a = i }; next }
        $2 == "clusterNode" && d && a {
            print $d "\t" $a; print $m "\t" $a
            s = $d; sub(/\..*/, "", s); print s "\t" $a
        }'
}

# ip_of <node name or ip>: the address of a node
function ip_of()
{
    local ip
    [[ "$1" =~ ^[0-9.]+$ ]] && { echo $1; return; }
    ip=$(node_map | awk -F'\t' -v n="$1" '$1 == n { print $2; exit }')
    echo ${ip:-$1}
}

# on_node <node> <command>: run a command on a member with the remote shell of the cluster (its key and known_hosts,
# written by the ssh_trust step), by the address of the node: known_hosts surely has that, not always its GPFS name.
# mmdsh is no use here, mmdiag prints nothing under it
function on_node()
{
    local rsh=$run_dir/storage/$cluster_uuid/gpfs_rsh
    [ -x $rsh ] || rsh="ssh -o BatchMode=yes"
    $rsh "$(ip_of $1)" "$2"
}

# class_ips <node class>: the addresses of the members of a node class
function class_ips()
{
    local n
    for n in $($gpfs_bin/mmlsnodeclass $1 -Y 2>/dev/null | gpfs_y "" memberNodes | tr ',' ' '); do
        ip_of $n
    done
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

# check_topology <node class> <disk expression>: every server sees its claimed disks with the same topology and none
# needs attention; the topology in topo_kinds
function check_topology()
{
    local nc=$1 expr=$2 topo bad kinds
    topo=$($gpfs_bin/mmvdisk server list --node-class $nc --disk-list "$expr" --disk-topology -Y </dev/null 2>&1)
    echo "$topo"
    bad=$(gpfs_y_rows serverDiskTopology nodeName needsAttention <<<"$topo" | awk -F'\t' '$2 != "no" {print $1}')
    kinds=$(gpfs_y_rows serverDiskTopology diskTopology <<<"$topo" | sort -u)
    [ -n "$kinds" ] || stc_fail "mmvdisk reported no disk topology for node class $nc"
    [ -z "$bad" ] || stc_fail "the disk topology of $(echo $bad) needs attention"
    [ "$(grep -c . <<<"$kinds")" = 1 ] || stc_fail "the servers have different disk topologies: $(echo $kinds)"
    topo_kinds=$kinds
}

function do_configure()
{
    local input=$1 nc servers expr bytes kinds
    nc=$(jq -r .node_class <<<"$input")
    servers=$(jq -r '.servers | join(",")' <<<"$input")
    expr=$(jq -r .disk_expr <<<"$input")
    bytes=$(jq -r .pagepool_bytes <<<"$input")
    valid_name "$nc" || stc_fail "invalid node class name"
    [[ "$servers" =~ ^[0-9.,]+$ ]] || stc_fail "invalid server list"
    valid_expr "$expr" || stc_fail "invalid disk expression"
    [[ "$bytes" =~ ^[0-9]+$ ]] || stc_fail "invalid pagepool"
    if ! $gpfs_bin/mmlsnodeclass $nc >/dev/null 2>&1; then
        stc_progress 10 "creating node class $nc"
        $gpfs_bin/mmvdisk nodeclass create --node-class $nc -N $servers </dev/null || stc_fail "mmvdisk nodeclass create failed"
    fi
    # Every server must see its claimed disks with the same topology, and none may need attention
    check_topology $nc "$expr"
    kinds=$topo_kinds
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

# has_slot_map <node>: the slot map of ecedrivemapping is on a server (lmr: slotmap.yaml, nvme: <vendor>_<product>.edf).
# A server has one of the two: ls fails for the one that is missing, so what it lists counts, not its exit code
function has_slot_map()
{
    on_node $1 "ls /usr/lpp/mmfs/data/gems/slotmap.yaml /usr/lpp/mmfs/data/gems/*.edf 2>/dev/null | grep -q ." >/dev/null 2>&1
}

# slots_on <input> <node>...: what a server needs before its disks go to a recovery group, on the nodes given; what
# was done in slot_result
function slots_on()
{
    local input=$1 noslot mode min max n a missing=""
    shift
    noslot=$(jq -r '.no_slot_map // false' <<<"$input")
    mode=$(jq -r '.slot_mode // ""' <<<"$input")
    min=$(jq -r '.slot_range[0] // ""' <<<"$input")
    max=$(jq -r '.slot_range[1] // ""' <<<"$input")
    if [ "$noslot" != "true" ]; then
        # The slot map is made on each server by ecedrivemapping (SAS disks behind a LSI controller, or NVMe). Made
        # here when the cluster names the mode and the slot range (it prompts for them otherwise); a map that is
        # there is kept, it may have been made by hand
        if [ -n "$mode" ]; then
            [[ "$mode" =~ ^(lmr|nvme)$ ]] || stc_fail "invalid slot mode"
            [[ "$min" =~ ^[0-9]+$ ]] && [[ "$max" =~ ^[0-9]+$ ]] && [ $min -le $max ] || stc_fail "invalid slot range"
            for n in "$@"; do
                has_slot_map $n && continue
                stc_progress 10 "mapping the drive slots of $n ($mode, slots $min-$max)"
                on_node $n "$gpfs_bin/ecedrivemapping --mode $mode --slotrange $min $max --force </dev/null" ||
                    stc_fail "ecedrivemapping failed on $n"
            done
        fi
        for n in "$@"; do
            has_slot_map $n || missing="$missing $n"
        done
        [ -z "$missing" ] || stc_fail "no slot map on$missing: run ecedrivemapping there first, or give the cluster a slot mode and range"
        slot_result='{"slot_check": "on"}'
        return
    fi
    # mmchconfig wants 999 typed to write an attribute it does not know (the slot one; a known one does not ask). Set
    # for the node class every time (a value left from a class deleted before does not count), what the daemons run
    # with is checked below
    stc_progress 10 "turning the slot and write cache checks off"
    for a in $test_disk_attrs; do
        printf '999\n' | $gpfs_bin/mmchconfig $a -N $(jq -r .node_class <<<"$input") || stc_fail "mmchconfig ${a%%=*} failed"
    done
    # The daemons read them when they start: one server at a time, so the cluster keeps its quorum
    for n in "$@"; do
        checks_off $n && continue
        stc_progress 30 "restarting GPFS on $n"
        $gpfs_bin/mmshutdown -N $n || stc_fail "mmshutdown on $n failed"
        $gpfs_bin/mmstartup -N $n || stc_fail "mmstartup on $n failed"
        wait_active $n || stc_fail "GPFS did not come back on $n"
        checks_off $n || stc_fail "GPFS on $n still runs with the slot or write cache check"
    done
    slot_result='{"slot_check": "off", "write_cache_check": "off"}'
}

function do_slots()
{
    local input=$1 nc nodes
    nc=$(jq -r .node_class <<<"$input")
    valid_name "$nc" || stc_fail "invalid node class name"
    nodes=$(jq -r '.nodes // [] | join(" ")' <<<"$input")
    [ -n "$nodes" ] || nodes=$(class_ips $nc | xargs)
    [ -n "$nodes" ] || stc_fail "node class $nc has no members"
    [[ "$nodes" =~ ^[0-9.\ ]+$ ]] || stc_fail "invalid node list"
    slots_on "$input" $nodes
    stc_result "$slot_result"
}

# rg_pdisks <rg>: the pdisks of a recovery group as JSON: [{name, ip, device, wwn, state, da}]. mmvdisk pdisk list -Y
# has no device or WWN, mmlspdisk has both in its stanzas; its server is a node name, turned into the address
function rg_pdisks()
{
    local map
    map=$(node_map)
    $gpfs_bin/mmlspdisk $1 2>/dev/null | awk -v map="$map" '
        BEGIN {
            n = split(map, lines, "\n")
            for (i = 1; i <= n; i++) { split(lines[i], kv, "\t"); ip[kv[1]] = kv[2] }
        }
        function flush(   s) {
            if (name != "") {
                # The node of the device path: on scale-out servers the server line names the node serving the
                # recovery group, not the one the disk is in. The server line only for a device path without one
                if (node != "") srv = node
                s = srv; sub(/\..*/, "", s)
                printf "%s\t%s\t%s\t%s\t%s\t%s\n", name, (srv in ip) ? ip[srv] : ((s in ip) ? ip[s] : srv), dev, wwn, st, da
            }
            name = dev = wwn = srv = st = node = da = ""
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
            else if (key == "declusteredArray") da = val
        }
        END { flush() }' |
        jq -R -s -c 'split("\n") | map(select(. != "") | split("\t") | {name: .[0], ip: .[1], device: .[2], wwn: .[3], state: .[4], da: .[5]})'
}

# rg_has_server <rg> <ip>: whether the recovery group has pdisks on the server of that address
function rg_has_server()
{
    rg_pdisks $1 | jq -e --arg ip "$2" 'any(.[]; .ip == $ip)' >/dev/null
}

# expr_disks <expression>: how many disks a disk expression lists
function expr_disks()
{
    tr ';' '\n' <<<"$1" | cut -d: -f2 | tr ',' '\n' | grep -c .
}

# wait_pdisks <rg> <count> <minutes>: the recovery group lists that many pdisks (mmlspdisk fails while it is busy)
function wait_pdisks()
{
    local i pdisks have
    for i in $(seq 1 $(($3 * 4))); do
        pdisks=$(rg_pdisks $1)
        have=$(jq 'length' <<<"$pdisks" 2>/dev/null)
        [ "$have" = "$2" ] && { echo "$pdisks"; return 0; }
        sleep 15
    done
    echo "$pdisks"
    return 1
}

# log_groups <rg>: how many log groups the recovery group has formatted
function log_groups()
{
    $gpfs_bin/mmvdisk recoverygroup list --recovery-group $1 --log-group -Y </dev/null 2>/dev/null |
        awk -F: '$2 == "rgLogGroup" && $3 != "HEADER"' | grep -c .
}

# rg_summary <rg>: the summary of a recovery group, "<pending node add>\t<rebalance status>\t<suspended server>"
function rg_summary()
{
    $gpfs_bin/mmvdisk recoverygroup list -Y </dev/null 2>/dev/null | gpfs_y_rows rgSummary rgName AddNode rebalanceStatus suspendedServer |
        awk -F'\t' -v rg="$1" '$1 == rg { print $2 "\t" $3 "\t" $4; exit }'
}

# rg_das <rg>: the declustered arrays of a recovery group, "<name>\t<hardware type>\t<free percent>"
function rg_das()
{
    $gpfs_bin/mmvdisk vdiskset list --recovery-group $1 -Y </dev/null 2>/dev/null |
        gpfs_y_rows vdisksetDaSizing declusteredArray hardwareType freePercent recoveryGroup | awk -F'\t' -v rg="$1" '$4 == rg { print $1 "\t" $2 "\t" $3 }'
}

# da_of <rg> <media>: the declustered array of a media (hdd, ssd, nvme; empty: the only array). mmvdisk says HDD, SSD,
# NVMe; an ssd array may show as NVMe and the other way round on some controllers, so the two match each other
function da_of()
{
    local das want=${2,,}
    das=$(rg_das $1)
    if [ -z "$want" ]; then
        [ "$(grep -c . <<<"$das")" = 1 ] && cut -f1 <<<"$das"
        return
    fi
    awk -F'\t' -v w="$want" '{ t = tolower($2) } t == w { print $1; exit }' <<<"$das" | grep . ||
        awk -F'\t' -v w="$want" '{ t = tolower($2) } (w == "ssd" || w == "nvme") && (t == "ssd" || t == "nvme") { print $1; exit }' <<<"$das"
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
    local input=$1 rg nc expr servers members disks total watcher rc have pdisks
    rg=$(jq -r .recovery_group <<<"$input")
    nc=$(jq -r .node_class <<<"$input")
    expr=$(jq -r .disk_expr <<<"$input")
    valid_name "$rg" || stc_fail "invalid recovery group name"
    valid_name "$nc" || stc_fail "invalid node class name"
    valid_expr "$expr" || stc_fail "invalid disk expression"
    # The servers and disks the platform claimed: "<ip>:sdb,sdc;<ip>:sdb,sdc;..."
    servers=$(tr ';' '\n' <<<"$expr" | grep -c .)
    disks=$(expr_disks "$expr")
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
    pdisks=$(wait_pdisks $rg $disks 3)
    have=$(jq 'length' <<<"$pdisks" 2>/dev/null)
    [ "$have" = "$disks" ] || stc_fail "recovery group $rg lists ${have:-no} pdisks, $disks disks were claimed"
    stc_result "$(jq -cn --argjson p "$pdisks" '{pdisks: $p}')"
}

# define_set <rg> <set json> [set size percent]: define a vdisk set on the array of its media unless it is defined
function define_set()
{
    local rg=$1 set=$2 vs code bs pct da_type usage pool da args
    vs=$(jq -r .vdisk_set <<<"$set")
    code=$(jq -r .code <<<"$set")
    bs=$(jq -r .block_size <<<"$set")
    pct=${3:-$(jq -r .set_size_pct <<<"$set")}
    da_type=$(jq -r '.da_type // ""' <<<"$set")
    usage=$(jq -r '.nsd_usage // ""' <<<"$set")
    pool=$(jq -r '.storage_pool // ""' <<<"$set")
    valid_name "$vs" || stc_fail "invalid vdisk set name"
    valid_code "$code" || stc_fail "invalid code"
    [[ "$bs" =~ ^[0-9]+[KMkm]$ ]] || stc_fail "invalid block size"
    [[ "$pct" =~ ^[0-9]+$ ]] && [ $pct -ge 1 ] && [ $pct -le 100 ] || stc_fail "invalid set size"
    [[ "$da_type" =~ ^(|hdd|ssd|nvme)$ ]] || stc_fail "invalid media"
    [[ "$usage" =~ ^(|dataOnly|metadataOnly|dataAndMetadata)$ ]] || stc_fail "invalid NSD usage"
    [ -z "$pool" ] || valid_name "$pool" || stc_fail "invalid storage pool"
    [ -n "$(gpfs_vdisksets | awk -v v="$vs" '$1 == v')" ] && return 0
    args=(--vdisk-set $vs --recovery-group $rg --code $code --block-size ${bs,,} --set-size $pct%)
    # Block sizes above 4M take the checksum granularity of 32K (mmvdisk refuses them without it)
    case ${bs^^} in 8M|16M) args+=(--checksum-granularity 32k) ;; esac
    if [ -n "$da_type" ] || [ "$(rg_das $rg | grep -c .)" -gt 1 ]; then
        da=$(da_of $rg "$da_type")
        [ -n "$da" ] || stc_fail "recovery group $rg has no declustered array of ${da_type:-one} media"
        args+=(--declustered-array $da)
    fi
    [ -n "$usage" ] && args+=(--nsd-usage $usage)
    [ -n "$pool" ] && args+=(--storage-pool $pool)
    stc_progress 10 "defining vdisk set $vs (${da:-the array}, $pct%)"
    $gpfs_bin/mmvdisk vdiskset define "${args[@]}" </dev/null
}

# create_set <vdisk set>: create the vdisks of a defined set unless they are created
function create_set()
{
    if [ "$(gpfs_vdisksets | awk -v v="$1" '$1 == v {print $2}')" = yes ]; then
        echo "vdisk set $1 is created already"
        return 0
    fi
    stc_progress 40 "creating the vdisks of $1"
    $gpfs_bin/mmvdisk vdiskset create --vdisk-set $1 </dev/null || stc_fail "mmvdisk vdiskset create $1 failed"
}

function do_create_vs()
{
    local input=$1 rg set vs
    rg=$(jq -r .recovery_group <<<"$input")
    valid_name "$rg" || stc_fail "invalid recovery group name"
    [ "$(jq '.sets | length' <<<"$input")" -ge 1 ] || stc_fail "no vdisk set to make"
    while read -r set; do
        define_set $rg "$set" || stc_fail "mmvdisk vdiskset define $(jq -r .vdisk_set <<<"$set") failed"
    done < <(jq -c '.sets[]' <<<"$input")
    for vs in $(jq -r '.sets[].vdisk_set' <<<"$input"); do
        create_set $vs
        $gpfs_bin/mmvdisk vdiskset list --vdisk-set $vs </dev/null
    done
    stc_result '{}'
}

# placement_rule <fs> <data pool>: new files go to the data pool (the system pool holds metadata only)
function placement_rule()
{
    local tmp
    [ -n "$2" ] || return 0
    valid_name "$2" || stc_fail "invalid data pool"
    $gpfs_bin/mmlspolicy $1 -L 2>/dev/null | grep -q "SET POOL '$2'" && return 0
    tmp=$(mktemp)
    echo "RULE 'default' SET POOL '$2'" >$tmp
    $gpfs_bin/mmchpolicy $1 $tmp || { rm -f $tmp; stc_fail "installing the placement rule failed"; }
    rm -f $tmp
}

function do_create_fs()
{
    local input=$1 fs sets mount nodes mounted i df size free vs
    fs=$(jq -r .fs_name <<<"$input")
    sets=$(jq -r '.vdisk_sets | join(",")' <<<"$input")
    mount=$(jq -r .mount_point <<<"$input")
    valid_name "$fs" || stc_fail "invalid file system name"
    for vs in ${sets//,/ }; do
        valid_name "$vs" || stc_fail "invalid vdisk set name"
    done
    [ -n "$sets" ] || stc_fail "no vdisk set for the file system"
    [[ "$mount" =~ ^/gpfs/[A-Za-z0-9_]+$ ]] || stc_fail "invalid mount point"
    if $gpfs_bin/mmlsfs $fs >/dev/null 2>&1; then
        echo "file system $fs exists already"
    else
        stc_progress 10 "creating file system $fs"
        # Quotas on: the CloudLand pools are filesets with a quota (§7.4)
        $gpfs_bin/mmvdisk filesystem create --file-system $fs --vdisk-set $sets --mmcrfs -T $mount -A yes -Q yes </dev/null ||
            stc_fail "mmvdisk filesystem create failed"
    fi
    placement_rule $fs "$(jq -r '.data_pool // ""' <<<"$input")"
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

function do_add_servers()
{
    local input=$1 nc rg ips expr full total missing="" ip members pdisks sum add rebalance waited=0 lg i
    nc=$(jq -r .node_class <<<"$input")
    rg=$(jq -r .recovery_group <<<"$input")
    ips=$(jq -r '.servers | join(" ")' <<<"$input")
    expr=$(jq -r .disk_expr <<<"$input")
    full=$(jq -r .full_expr <<<"$input")
    total=$(jq -r .total_servers <<<"$input")
    valid_name "$nc" && valid_name "$rg" || stc_fail "invalid node class or recovery group name"
    [[ "$ips" =~ ^[0-9.\ ]+$ ]] || stc_fail "invalid server list"
    valid_expr "$expr" && valid_expr "$full" || stc_fail "invalid disk expression"
    [[ "$total" =~ ^[0-9]+$ ]] || stc_fail "invalid server count"
    # 1. configured like the class, and in it
    members=" $(class_ips $nc | xargs) "
    for ip in $ips; do
        [[ "$members" == *" $ip "* ]] || missing="$missing,$ip"
    done
    if [ -n "$missing" ]; then
        stc_progress 5 "configuring ${missing#,} like node class $nc"
        $gpfs_bin/mmvdisk server configure -N ${missing#,} --target-node-class $nc --recycle one </dev/null ||
            stc_fail "mmvdisk server configure -N ${missing#,} failed"
        members=" $(class_ips $nc | xargs) "
        for ip in ${missing//,/ }; do
            [[ "$members" == *" $ip "* ]] && continue
            $gpfs_bin/mmvdisk nodeclass add --node-class $nc -N $ip </dev/null || stc_fail "mmvdisk nodeclass add $ip failed"
        done
    fi
    wait_active $ips || stc_fail "GPFS is not active on the new servers"
    # 2. the slot map, or the checks off, on them
    slots_on "$input" $ips
    # 3. every server with the same topology, the new ones too
    check_topology $nc "$full"
    # 4. their disks into the recovery group
    missing=""
    for ip in $ips; do
        rg_has_server $rg $ip || missing="$missing,$ip"
    done
    if [ -n "$missing" ]; then
        stc_progress 15 "adding ${missing#,} to recovery group $rg"
        $gpfs_bin/mmvdisk recoverygroup add --recovery-group $rg -N ${missing#,} --disk-list "$expr" </dev/null ||
            stc_fail "mmvdisk recoverygroup add failed"
    fi
    # 5. mmvdisk rebalances the stripes over the new pdisks, then its callback formats the log groups of the new
    # servers and extends the vdisk sets and file systems (--complete-node-add). Done when no add is pending and every
    # server has its two log groups. The callback is given 10 minutes after the rebalance ended, then it is run here
    lg=$((total * 2 + 1))
    for i in $(seq 1 100000); do
        sum=$(rg_summary $rg)
        add=$(cut -f1 <<<"$sum")
        rebalance=$(cut -f2 <<<"$sum")
        [ -z "$add" ] && [ "$(log_groups $rg)" -ge $lg ] && break
        if [ -n "$add" ] && [ "$rebalance" = complete ]; then
            waited=$((waited + 1))
            if [ $waited -ge 20 ]; then
                stc_progress 90 "completing the add of $add (the callback did not)"
                $gpfs_bin/mmvdisk recoverygroup add --recovery-group $rg --complete-node-add </dev/null ||
                    stc_fail "mmvdisk recoverygroup add --complete-node-add failed"
                waited=0
            fi
        else
            stc_progress 30 "rebalancing recovery group $rg over the new servers (${rebalance:-starting})"
        fi
        sleep 30
    done
    pdisks=$(wait_pdisks $rg $(expr_disks "$full") 5) || stc_fail "recovery group $rg lists $(jq length <<<"$pdisks") pdisks, $(expr_disks "$full") disks were claimed"
    stc_result "$(jq -cn --argjson p "$pdisks" '{pdisks: $p}')"
}

function do_remove_server()
{
    local input=$1 nc rg ip node line vs fs rgs created sets i
    nc=$(jq -r .node_class <<<"$input")
    rg=$(jq -r .recovery_group <<<"$input")
    ip=$(jq -r .ip <<<"$input")
    valid_name "$nc" && valid_name "$rg" || stc_fail "invalid node class or recovery group name"
    [[ "$ip" =~ ^[0-9.]+$ ]] || stc_fail "invalid address"
    if rg_has_server $rg $ip; then
        # The file systems give up the members of their vdisk sets on the server first (mmvdisk checks the servers
        # left can hold them)
        declare -A sets_of=()
        while read -r vs created fs rgs; do
            [ -n "$vs" ] && [ "$fs" != "-" ] && [[ ",$rgs," == *",$rg,"* ]] || continue
            sets_of[$fs]="${sets_of[$fs]:+${sets_of[$fs]},}$vs"
        done < <(gpfs_vdisksets)
        for fs in "${!sets_of[@]}"; do
            stc_progress 10 "taking the members of ${sets_of[$fs]} on $ip out of $fs"
            $gpfs_bin/mmvdisk filesystem delete --file-system $fs --vdisk-set ${sets_of[$fs]} --recovery-group $rg -N $ip --confirm </dev/null ||
                stc_fail "mmvdisk filesystem delete -N $ip failed: the servers left may not hold the data of $fs"
        done
        stc_progress 40 "taking $ip out of recovery group $rg (its data moves to the other servers)"
        $gpfs_bin/mmvdisk recoverygroup delete --recovery-group $rg -N $ip --confirm </dev/null ||
            stc_fail "mmvdisk recoverygroup delete -N $ip failed"
        for i in $(seq 1 100000); do
            rg_has_server $rg $ip || break
            stc_progress 60 "draining the pdisks of $ip"
            sleep 30
        done
    else
        echo "$ip has no pdisks in $rg"
    fi
    if [[ " $(class_ips $nc | xargs) " == *" $ip "* ]]; then
        stc_progress 90 "taking $ip out of node class $nc"
        $gpfs_bin/mmvdisk nodeclass delete --node-class $nc -N $ip </dev/null || stc_fail "mmvdisk nodeclass delete -N $ip failed"
    fi
    stc_result '{}'
}

# pdisk_of <rg> <pdisk>: a pdisk of the recovery group as JSON, empty when it has none of that name
function pdisk_of()
{
    rg_pdisks $1 | jq -c --arg n "$2" '[.[] | select(.name == $n)][0] // empty'
}

function do_replace()
{
    local input=$1 rg pdisk ip device wwn slotmap cur state node da tmp i new
    rg=$(jq -r .recovery_group <<<"$input")
    pdisk=$(jq -r .pdisk <<<"$input")
    ip=$(jq -r .ip <<<"$input")
    device=$(jq -r .device <<<"$input")
    wwn=$(jq -r '.wwn // ""' <<<"$input")
    slotmap=$(jq -r '.slot_map // false' <<<"$input")
    valid_name "$rg" || stc_fail "invalid recovery group name"
    [[ "$pdisk" =~ ^[A-Za-z0-9_]+$ ]] || stc_fail "invalid pdisk name"
    [[ "$ip" =~ ^[0-9.]+$ ]] || stc_fail "invalid address"
    [[ "$device" =~ ^[a-z][a-z0-9]*$ ]] || stc_fail "invalid device"
    [[ "$wwn" =~ ^[A-Za-z0-9.x-]*$ ]] || stc_fail "invalid WWN"
    cur=$(pdisk_of $rg $pdisk)
    [ -n "$cur" ] || stc_fail "recovery group $rg has no pdisk $pdisk"
    # A retry after the new disk went in: the pdisk is on it
    if [ "$(jq -r .ip <<<"$cur")" = "$ip" ] && [ "$(jq -r .device <<<"$cur")" = "$device" ] && [[ "$(jq -r .state <<<"$cur")" == ok* ]]; then
        echo "pdisk $pdisk is on /dev/$device already"
    else
        state=$(jq -r .state <<<"$cur")
        # GNR replaces a pdisk that failed (dead, missing, failing, drained...); a working one is never replaced
        [[ "$state" == ok ]] && stc_fail "pdisk $pdisk is working (state ok): only a failed pdisk is replaced"
        stc_progress 10 "preparing pdisk $pdisk ($state) for replacement"
        # Prepared already on a retry: it says so and fails, the replacement below tells
        $gpfs_bin/mmvdisk pdisk replace --prepare --recovery-group $rg --pdisk $pdisk </dev/null || echo "prepare said no, going on"
        if [ "$slotmap" = true ]; then
            # The new disk sits in the slot of the old one: mmvdisk finds it there
            stc_progress 40 "replacing pdisk $pdisk with the disk in its slot"
            printf 'yes\n' | $gpfs_bin/mmvdisk pdisk replace --recovery-group $rg --pdisk $pdisk -v no ||
                stc_fail "mmvdisk pdisk replace failed: is the new disk in the slot of $pdisk?"
        else
            # No slot map: the new device is named, on the node of the old pdisk, in the declustered array of it
            node=$(node_map | awk -F'\t' -v ip="$ip" '$2 == ip { print $1; exit }')
            da=$(jq -r .da <<<"$cur")
            [ -n "$node" ] || stc_fail "no node of the cluster has address $ip"
            valid_name "$da" || stc_fail "the declustered array of $pdisk is not known"
            tmp=$(mktemp)
            echo "%pdisk: pdiskName=$pdisk device=//$node/dev/$device da=$da" >$tmp
            stc_progress 40 "replacing pdisk $pdisk with /dev/$device of $node"
            $gpfs_bin/mmaddpdisk $rg -F $tmp --replace -v no || { rm -f $tmp; stc_fail "mmaddpdisk --replace failed"; }
            rm -f $tmp
        fi
    fi
    # The new disk under the old name, working
    for i in $(seq 1 40); do
        new=$(pdisk_of $rg $pdisk)
        if [[ "$(jq -r .state <<<"$new")" == ok* ]] &&
            { [ "$(jq -r .device <<<"$new")" = "$device" ] || { [ -n "$wwn" ] && [ "$(gpfs_wwn "$(jq -r .wwn <<<"$new")")" = "$(gpfs_wwn "$wwn")" ]; }; }; then
            stc_result "$(jq -cn --argjson p "$(rg_pdisks $rg)" '{pdisks: $p}')"
            return
        fi
        stc_progress 70 "waiting for pdisk $pdisk to come up on the new disk ($(jq -r .state <<<"$new"))"
        sleep 15
    done
    stc_fail "pdisk $pdisk is not working on /dev/$device after 10 minutes: $(jq -c . <<<"$new")"
}

# gpfs_wwn <wwn>: a WWN written the one way (naa.5000C500..., 0x5000c500..., wwn-0x...)
function gpfs_wwn()
{
    local w=${1,,}
    w=${w#wwn-}; w=${w#naa.}; w=${w#0x}
    echo $w
}

function do_resize()
{
    local input=$1 rg expr fs set vs da_type da free pct want have pdisks
    rg=$(jq -r .recovery_group <<<"$input")
    expr=$(jq -r .disk_expr <<<"$input")
    fs=$(jq -r .fs_name <<<"$input")
    vs=$(jq -r .vdisk_set <<<"$input")
    da_type=$(jq -r '.da_type // ""' <<<"$input")
    valid_name "$rg" && valid_name "$fs" && valid_name "$vs" || stc_fail "invalid name"
    valid_expr "$expr" || stc_fail "invalid disk expression"
    want=$(expr_disks "$expr")
    have=$(rg_pdisks $rg | jq length)
    # 1. the recovery group takes the new disks of every server (the topology of each server changes the same way)
    if [ "${have:-0}" -lt "$want" ]; then
        stc_progress 10 "adding the new disks to recovery group $rg"
        $gpfs_bin/mmvdisk recoverygroup resize --recovery-group $rg --disk-list "$expr" </dev/null ||
            stc_fail "mmvdisk recoverygroup resize failed: every server needs the same new disks"
    fi
    pdisks=$(wait_pdisks $rg $want 30) || stc_fail "recovery group $rg lists $(jq length <<<"$pdisks") pdisks, $want disks were claimed"
    # 2. a new vdisk set on the array the disks went to: the set size of the cluster is the share of the array the
    # vdisk sets may take, the used part counts against it
    set=$(jq -c '{vdisk_set, code, block_size, set_size_pct, da_type, nsd_usage, storage_pool}' <<<"$input")
    if [ -z "$(gpfs_vdisksets | awk -v v="$vs" '$1 == v')" ]; then
        da=$(da_of $rg "$da_type")
        [ -n "$da" ] || stc_fail "recovery group $rg has no declustered array of ${da_type:-one} media"
        free=$(rg_das $rg | awk -F'\t' -v d="$da" '$1 == d { print int($3) }')
        [[ "$free" =~ ^[0-9]+$ ]] || stc_fail "the free space of $da is not known"
        pct=$(($(jq -r .set_size_pct <<<"$input") - (100 - free)))
        [ $pct -ge 5 ] || stc_fail "the new disks leave $pct% of $da for a vdisk set: nothing to add"
        # The share is of the raw space, the definition of the usable space: a little less when it does not fit
        while ! define_set $rg "$set" $pct; do
            pct=$((pct - 5))
            [ $pct -ge 5 ] || stc_fail "mmvdisk vdiskset define $vs failed"
        done
    fi
    create_set $vs
    # 3. the file system gets the new vdisks
    if [ "$(gpfs_vdisksets | awk -v v="$vs" '$1 == v {print $3}')" != "$fs" ]; then
        stc_progress 70 "adding $vs to $fs"
        $gpfs_bin/mmvdisk filesystem add --file-system $fs --vdisk-set $vs </dev/null || stc_fail "mmvdisk filesystem add failed"
    fi
    stc_result "$(jq -cn --argjson p "$pdisks" '{pdisks: $p}')"
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
        configure|slots|create_rg|create_vs|create_fs|add_servers|remove_server|replace|resize) do_$action "$input" ;;
        *) stc_fail "unknown action $action" ;;
    esac
}

stc_run "$@"
