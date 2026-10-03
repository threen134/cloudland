#!/bin/bash
# Cluster level Ceph commands of a managed cluster, run on an admin host (shared-storage-design.md §8.2, §8.5, §8.6).
# Input: {"action", "cluster_uuid", "fsid", ...}
#   bootstrap:   {"mon_ip", "hostname", "labels", "image", "cluster_network", "single_host"}  the first mon and mgr,
#                with the key of the cluster as the key cephadm logs in with
#   add_hosts:   {"hosts": [{"hostname", "ip", "labels"}], "mons"}   every cephadm host with its labels, the daemons
#                placed by label; waits for the mons to form their quorum
#   configure:   {"replicas", "osd_memory_target", "cluster_network", "client_user"}   result: {"client_key", "mon_addrs"}
#   create_osds: {"osds": [{"key", "hostname", "device", "kname", "media"}]}           result: {"osds": [{"key", "osd_id"}]}
#   remove_osds: {"osd_ids", "offline"}    their data moves to the other OSDs first, unless their host is gone
#   remove_host: {"hostname", "orch_host", "osd_ids", "offline"}   drained and removed, or removed at once when gone
#   teardown:    the orchestrator stops, so it does not redeploy what the hosts remove next (stc_leave.sh)
# Every action checks first what is there, so it can run again after a failure.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/ceph.sh
STC_SCRIPT=$(readlink -f $0)

input=""
fsid=""

function valid_name()
{
    [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$ ]]
}

function valid_ip()
{
    [[ "$1" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]
}

# wait_for <seconds> <message> <command...>: until the command succeeds
function wait_for()
{
    local limit=$1 message=$2 start=$(date +%s)
    shift 2
    until "$@"; do
        [ $(($(date +%s) - start)) -ge $limit ] && return 1
        echo "waiting: $message"
        sleep 5
    done
}

function do_bootstrap()
{
    local uuid dir ip name image net single labels l args pub keys
    uuid=$(jq -r .cluster_uuid <<<"$input")
    ip=$(jq -r .mon_ip <<<"$input")
    name=$(jq -r .hostname <<<"$input")
    image=$(jq -r '.image // ""' <<<"$input")
    net=$(jq -r '.cluster_network // ""' <<<"$input")
    single=$(jq -r '.single_host // false' <<<"$input")
    valid_ip "$ip" || stc_fail "invalid mon address $ip"
    valid_name "$name" || stc_fail "invalid host name $name"
    [ "$(hostname)" = "$name" ] || stc_fail "this host is $(hostname), the cluster knows it as $name"
    [[ "$image" =~ ^[a-z0-9][a-z0-9./:_@-]{0,199}$ ]] || stc_fail "invalid image $image"
    [ -z "$net" ] || [[ "$net" =~ ^[0-9./]+$ ]] || stc_fail "invalid cluster network $net"
    dir=$run_dir/storage/$uuid
    [ -f $dir/id_ed25519 ] || stc_fail "this host has no key of the cluster: the ssh_trust step did not run here"
    if ceph_admin_ready $fsid; then
        echo "cluster $fsid is bootstrapped already"
    else
        if [ -d /var/lib/ceph/$fsid ]; then
            # What a failed bootstrap left: it never served, there is nothing to keep
            echo "removing the remains of an earlier bootstrap"
            cephadm rm-cluster --force --fsid $fsid >/dev/null 2>&1
        fi
        pub=$dir/id_ed25519.pub
        ssh-keygen -y -f $dir/id_ed25519 >$pub || stc_fail "the key of the cluster is unreadable"
        args="--fsid $fsid --mon-ip $ip --ssh-user root --ssh-private-key $dir/id_ed25519 --ssh-public-key $pub"
        args="$args --skip-monitoring-stack --skip-dashboard --skip-firewalld --skip-pull --allow-fqdn-hostname --orphan-initial-daemons"
        [ -n "$net" ] && args="$args --cluster-network $net"
        [ "$single" = "true" ] && args="$args --single-host-defaults"
        stc_progress 10 "bootstrapping the first monitor on $name"
        # The configuration and the admin key go to the cluster's own directory, not /etc/ceph/ceph.conf
        cephadm --image "$image" bootstrap $args --output-dir /var/lib/ceph/$fsid/bootstrap --no-cleanup-on-failure ||
            stc_fail "cephadm bootstrap failed"
        ceph_admin_ready $fsid || stc_fail "the cluster does not answer after bootstrap"
        # cephadm authorized the key of the cluster for root without restriction; the line of the ssh_trust step
        # (only from the admin and mgr hosts) is the one that stays
        stc_keys_update line "$(cat $pub)" || stc_fail "rewriting authorized_keys failed"
    fi
    for l in $(jq -r '.labels[]' <<<"$input"); do
        ceph_admin $fsid orch host label add "$name" "$l" >/dev/null || stc_fail "labelling $name $l failed"
    done
    stc_result "$(jq -cn --arg f "$fsid" '{fsid: $f}')"
}

function mons_in_quorum()
{
    local want=$1 have
    have=$(ceph_admin $fsid quorum_status -f json 2>/dev/null | jq '.quorum_names | length')
    [ "${have:-0}" -ge "$want" ]
}

function do_add_hosts()
{
    local known row name ip labels l mons n=0 total
    mons=$(jq -r '.mons // 1' <<<"$input")
    [[ "$mons" =~ ^[1-9]$ ]] || stc_fail "invalid mon count $mons"
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    # Placed by label: a mon and a mgr on every host so labelled, crash collection everywhere. cephadm otherwise
    # wants 5 mons and 2 mgrs wherever it likes
    ceph_admin $fsid orch apply mon --placement=label:mon >/dev/null || stc_fail "placing the mons failed"
    ceph_admin $fsid orch apply mgr --placement=label:mgr >/dev/null || stc_fail "placing the mgrs failed"
    ceph_admin $fsid orch apply crash --placement='*' >/dev/null || stc_fail "placing the crash collectors failed"
    known=$(ceph_admin $fsid orch host ls --format json) || stc_fail "listing the hosts failed"
    total=$(jq '.hosts | length' <<<"$input")
    for row in $(jq -c '.hosts[]' <<<"$input"); do
        name=$(jq -r .hostname <<<"$row")
        ip=$(jq -r .ip <<<"$row")
        labels=$(jq -r '.labels | join(",")' <<<"$row")
        valid_name "$name" && valid_ip "$ip" || stc_fail "invalid host $name $ip"
        n=$((n + 1))
        stc_progress $((10 + 60 * n / total)) "adding $name"
        if jq -e --arg h "$name" '.[] | select(.hostname == $h)' >/dev/null <<<"$known"; then
            echo "$name is a host of the cluster already"
            for l in ${labels//,/ }; do
                ceph_admin $fsid orch host label add "$name" "$l" >/dev/null || stc_fail "labelling $name $l failed"
            done
        else
            ceph_admin $fsid orch host add "$name" "$ip" --labels "$labels" || stc_fail "adding $name ($ip) failed: can the mgr log in to it?"
        fi
    done
    stc_progress 75 "waiting for $mons monitors in quorum"
    wait_for 900 "monitors in quorum" mons_in_quorum $mons || stc_fail "the monitors did not form a quorum of $mons in 15 minutes"
    wait_for 300 "an active mgr" eval 'ceph_admin $fsid mgr stat -f json 2>/dev/null | jq -e .available >/dev/null' ||
        stc_fail "no mgr became active"
    stc_result "$(jq -cn --argjson m "$mons" '{mons: $m}')"
}

function do_configure()
{
    local replicas min target net user key mons
    replicas=$(jq -r .replicas <<<"$input")
    target=$(jq -r .osd_memory_target <<<"$input")
    net=$(jq -r '.cluster_network // ""' <<<"$input")
    user=$(jq -r .client_user <<<"$input")
    [[ "$replicas" =~ ^[1-3]$ ]] || stc_fail "invalid replicas $replicas"
    [[ "$target" =~ ^[0-9]+$ ]] || stc_fail "invalid memory target $target"
    [[ "$user" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] || stc_fail "invalid client user $user"
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    min=$((replicas - replicas / 2))
    stc_progress 20 "setting the configuration"
    {
        ceph_admin $fsid config set global osd_pool_default_size $replicas &&
        ceph_admin $fsid config set global osd_pool_default_min_size $min &&
        # The memory of the OSDs is reserved on the host (§6.7): a fixed target, not one autotuned to the host's RAM
        ceph_admin $fsid config set osd osd_memory_target_autotune false &&
        ceph_admin $fsid config set osd osd_memory_target $target &&
        ceph_admin $fsid config set mon mon_allow_pool_delete false &&
        # OSDs are made on the disks CloudLand claimed and on nothing else
        ceph_admin $fsid orch apply osd --all-available-devices --unmanaged=true
    } >/dev/null || stc_fail "setting the configuration failed"
    if [ "$replicas" = "1" ]; then
        ceph_admin $fsid config set global mon_allow_pool_size_one true >/dev/null &&
            ceph_admin $fsid config set global mon_warn_on_pool_no_redundancy false >/dev/null || stc_fail "allowing pools of one replica failed"
    fi
    [ -n "$net" ] && { ceph_admin $fsid config set global cluster_network $net >/dev/null || stc_fail "setting the cluster network failed"; }
    stc_progress 60 "making the client user client.$user"
    # The cluster belongs to CloudLand: its client may use every RBD pool, which are all CloudLand's (§8.4)
    ceph_admin $fsid auth get-or-create client.$user mon 'profile rbd' osd 'profile rbd' mgr 'profile rbd' >/dev/null ||
        stc_fail "making client.$user failed"
    key=$(ceph_admin $fsid auth get-key client.$user) || stc_fail "reading the key of client.$user failed"
    mons=$(ceph_admin $fsid mon dump -f json | jq -c '[.mons[].public_addrs.addrvec[] | select(.type == "v2") | .addr | split(":")[0]] | unique')
    [ "$(jq length <<<"$mons")" -gt 0 ] || stc_fail "the mon map has no address"
    echo "mon addresses: $mons"
    stc_result "$(jq -cn --arg k "$key" --argjson m "$mons" '{client_key: $k, mon_addrs: $m}')"
}

# osd_of <host> <kernel name>: the id of the OSD on a disk, from what the OSDs report about themselves
function osd_of()
{
    ceph_admin $fsid osd metadata -f json 2>/dev/null |
        jq -r --arg h "$1" --arg k "$2" '.[] | select(.hostname == $h and ((.devices // "") | split(",") | index($k))) | .id' | head -1
}

function osd_up()
{
    ceph_admin $fsid osd dump -f json 2>/dev/null | jq -e --argjson i "$1" '.osds[] | select(.osd == $i and .up == 1)' >/dev/null
}

function do_create_osds()
{
    local row key name dev kname media id out="[]" n=0 total res
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    total=$(jq '.osds | length' <<<"$input")
    # 20.2 checks the device against the inventory of the host, which a host added a moment ago does not have yet and
    # which never lists a logical volume (a loop device under LVM); 19.2 inventories with --filter-for-batch, so a host
    # whose disks are all in use has an empty one for good, and does not check. The inventory is refreshed, and the
    # check is skipped when it gets in the way
    ceph_admin $fsid orch device ls --refresh >/dev/null 2>&1
    for row in $(jq -c '.osds[]' <<<"$input"); do
        key=$(jq -r .key <<<"$row")
        name=$(jq -r .hostname <<<"$row")
        dev=$(jq -r .device <<<"$row")
        kname=$(jq -r .kname <<<"$row")
        media=$(jq -r '.media // ""' <<<"$row")
        valid_name "$name" || stc_fail "invalid host name $name"
        [[ "$dev" =~ ^/dev/[A-Za-z0-9/_.-]+$ ]] && [[ "$kname" =~ ^[A-Za-z0-9_.-]+$ ]] || stc_fail "invalid device $dev ($kname)"
        [ -z "$media" ] || [[ "$media" =~ ^(hdd|ssd|nvme)$ ]] || stc_fail "invalid media $media"
        n=$((n + 1))
        stc_progress $((5 + 85 * (n - 1) / total)) "making an OSD on $name:$dev"
        id=$(osd_of "$name" "$kname")
        if [ -n "$id" ]; then
            echo "osd.$id is on $name:$dev already"
        else
            res=$(CEPH_TIMEOUT=900 ceph_admin $fsid orch daemon add osd "$name:$dev" 2>&1)
            if grep -qE "is not found on host|No devices found for host" <<<"$res"; then
                res=$(CEPH_TIMEOUT=900 ceph_admin $fsid orch daemon add osd "$name:$dev" --skip-validation 2>&1)
            fi
            echo "$res"
            grep -qi "^Error" <<<"$res" && stc_fail "making an OSD on $name:$dev failed: $(grep -i '^Error' <<<"$res" | head -1)"
            wait_for 600 "the OSD on $name:$dev" eval '[ -n "$(osd_of "$name" "$kname")" ]' || stc_fail "no OSD showed up on $name:$dev"
            id=$(osd_of "$name" "$kname")
        fi
        wait_for 600 "osd.$id up" osd_up $id || stc_fail "osd.$id did not come up"
        if [ -n "$media" ]; then
            ceph_admin $fsid osd crush rm-device-class osd.$id >/dev/null 2>&1
            ceph_admin $fsid osd crush set-device-class $media osd.$id >/dev/null || stc_fail "setting the class of osd.$id to $media failed"
        fi
        out=$(jq -c --arg k "$key" --argjson i "$id" '. + [{key: $k, osd_id: $i}]' <<<"$out")
    done
    # 20.2 saves every "orch daemon add osd" as a managed spec osd.default for that host and disk, which would make a
    # new OSD on the disk again once it is removed and zapped. 19.2 has no such spec: the command fails, harmlessly
    ceph_admin $fsid orch set-unmanaged osd.default >/dev/null 2>&1
    stc_result "$(jq -cn --argjson o "$out" '{osds: $o}')"
}

function osds_gone()
{
    local ids=$1 have
    have=$(ceph_admin $fsid osd ls -f json 2>/dev/null) || return 1
    jq -e --argjson want "$ids" '. as $have | all($want[]; . as $i | ($have | index($i)) == null)' >/dev/null <<<"$have"
}

# purge_osds <ids json>: OSDs whose host is gone for good: out of the map at once, Ceph recovers their copies
function purge_osds()
{
    local id
    for id in $(jq -r '.[]' <<<"$1"); do
        ceph_admin $fsid osd out $id >/dev/null 2>&1
        ceph_admin $fsid osd purge $id --yes-i-really-mean-it >/dev/null 2>&1
    done
    osds_gone "$1"
}

# wait_removed <ids json>: until the OSDs being removed with their data moved are out of the map, with progress
function wait_removed()
{
    local ids=$1 start=$(date +%s) pgs
    until osds_gone "$ids"; do
        pgs=$(ceph_admin $fsid orch osd rm status -f json 2>/dev/null | jq -r '[.[]? | "osd.\(.osd_id): \(.drain_started // false | if . then "draining" else "waiting" end) \(.pg_count // "?") pgs"] | join(", ")')
        stc_progress 50 "moving the data off: ${pgs:-waiting}"
        sleep 15
    done
}

function do_remove_osds()
{
    local ids offline
    ids=$(jq -c '.osd_ids // []' <<<"$input")
    offline=$(jq -r '.offline // false' <<<"$input")
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    [ "$(jq length <<<"$ids")" -gt 0 ] || { stc_result '{"removed": 0}'; return 0; }
    if osds_gone "$ids"; then
        echo "the OSDs are gone already"
    elif [ "$offline" = "true" ]; then
        purge_osds "$ids" || stc_fail "purging the OSDs failed"
    else
        # orch osd rm marks them out, waits until their data is elsewhere, removes them and zaps the devices
        ceph_admin $fsid orch osd rm $(jq -r 'join(" ")' <<<"$ids") --zap || stc_fail "starting the removal of the OSDs failed"
        wait_removed "$ids"
    fi
    stc_result "$(jq -cn --argjson i "$ids" '{removed: ($i | length)}')"
}

function do_remove_host()
{
    local name orch ids offline daemons
    name=$(jq -r .hostname <<<"$input")
    orch=$(jq -r '.orch_host // true' <<<"$input")
    ids=$(jq -c '.osd_ids // []' <<<"$input")
    offline=$(jq -r '.offline // false' <<<"$input")
    valid_name "$name" || stc_fail "invalid host name $name"
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    if [ "$orch" != "true" ] || ! ceph_admin $fsid orch host ls --format json | jq -e --arg h "$name" '.[] | select(.hostname == $h)' >/dev/null; then
        echo "$name runs no daemons of the cluster"
    elif [ "$offline" = "true" ]; then
        stc_progress 20 "removing $name, gone for good"
        ceph_admin $fsid orch host rm "$name" --offline --force || stc_fail "removing $name failed"
    else
        stc_progress 10 "draining $name"
        ceph_admin $fsid orch host drain "$name" --zap-osd-devices || stc_fail "draining $name failed"
        [ "$(jq length <<<"$ids")" -gt 0 ] && wait_removed "$ids"
        stc_progress 80 "waiting for the daemons of $name to go"
        wait_for 900 "the daemons of $name" eval \
            '[ "$(ceph_admin $fsid orch ps $name --format json 2>/dev/null | jq length)" = "0" ]' || stc_fail "the daemons of $name did not go in 15 minutes"
        ceph_admin $fsid orch host rm "$name" || stc_fail "removing $name failed"
    fi
    if [ "$(jq length <<<"$ids")" -gt 0 ] && ! osds_gone "$ids"; then
        purge_osds "$ids" || stc_fail "the OSDs of $name are still in the map"
    fi
    stc_result "$(jq -cn --arg h "$name" '{removed: $h}')"
}

function do_teardown()
{
    if ceph_admin_ready $fsid; then
        ceph_admin $fsid mgr module disable cephadm >/dev/null 2>&1 || echo "the orchestrator could not be stopped, the hosts remove the daemons anyway"
    else
        echo "the cluster does not answer, the hosts remove what is left"
    fi
    stc_result '{"teardown": true}'
}

function stc_main()
{
    local action uuid
    input=$(cat)
    action=$(jq -r .action <<<"$input")
    uuid=$(jq -r .cluster_uuid <<<"$input")
    fsid=$(jq -r .fsid <<<"$input")
    valid_uuid "$uuid" && valid_uuid "$fsid" || stc_fail "invalid cluster uuid or fsid"
    stc_lock "ceph-$uuid"
    case "$action" in
        bootstrap) do_bootstrap ;;
        add_hosts) do_add_hosts ;;
        configure) do_configure ;;
        create_osds) do_create_osds ;;
        remove_osds) do_remove_osds ;;
        remove_host) do_remove_host ;;
        teardown) do_teardown ;;
        *) stc_fail "unknown action $action" ;;
    esac
}

stc_run "$@"
