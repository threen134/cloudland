#!/bin/bash
# Cluster level Ceph commands of a managed cluster, run on an admin host (shared-storage-design.md §8.2, §8.5, §8.6).
# Input: {"action", "cluster_uuid", "fsid", ...}
#   bootstrap:   {"mon_ip", "hostname", "labels", "image", "cluster_network", "single_host", "registry"}  the first mon
#                and mgr, with the key of the cluster as the key cephadm logs in with; registry: {"url", "username",
#                "password"} of a private registry of the image, which cephadm keeps for its pulls
#   add_hosts:   {"hosts": [{"hostname", "ip", "labels"}], "mons"}   every cephadm host with its labels, the daemons
#                placed by label; waits for the mons to form their quorum
#   configure:   {"replicas", "osd_memory_target", "cluster_network", "client_user"}   result: {"client_key", "mon_addrs"}
#   mon_addrs:   the addresses of the mons as they are now (after mons came or went)   result: {"mon_addrs"}
#   create_osds: {"osds": [{"key", "hostname", "device", "kname", "media"}]}           result: {"osds": [{"key", "osd_id"}]}
#   remove_osds: {"osd_ids", "offline"}    their data moves to the other OSDs first, unless their host is gone
#   remove_host: {"hostname", "orch_host", "osd_ids", "offline"}   drained and removed, or removed at once when gone
#   replace_osd: {"old_id", "osds": [{"key", "hostname", "device", "kname", "media"}]}   the OSD of a failed disk is
#                destroyed keeping its id and the new disk of the same host takes it   result: {"osds": [{"key", "osd_id"}]}
#   set_labels:  {"hostname", "ip", "labels", "mons"}   the mon, mgr and _admin labels of a host as its roles say;
#                cephadm places the daemons by them. Waits for the mons and an active mgr
#   teardown:    the orchestrator stops, so it does not redeploy what the hosts remove next (stc_leave.sh)
#   fence:       {"address", "expire", "client_user"}   every client on the address of a host taken for dead is refused
#                by the OSDs (§11.2); client_user: an imported cluster, with no admin key here: the CloudLand client
#                may add to the blocklist, not remove from it
#   unfence:     {"address", "client_user"}   the address may connect again
#   cephadm_key: {"private_key", "public_key", "hosts": [{"hostname", "ip"}]}   the orchestrator logs in with a new key
#                of the cluster (rotation, every host takes it already) and checks it reaches every host with it
#   cephadm_check: {"hosts"}   the orchestrator reaches every host (after the old key is refused)
#   client_pending: {"client_user"}   a pending key for the client user; the cluster takes both keys until the
#                pending one is first used, which makes it the key   result: {"client_key_pending"}
#   client_commit: {"client_user", "client_key"}   the key given is the key of the user: made so when no host used it
#   upgrade:     {"version", "image"}   cephadm upgrades every daemon to the image of the release the hosts installed
#                (ceph orch upgrade), one after the other; waits until every daemon runs it   result: {"version"}
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
    local uuid dir ip name image net single labels l args pub keys regjson rc
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
        # The login of a private registry goes in a file, never on a command line
        regjson=""
        if [ "$(jq -r '.registry.url // empty' <<<"$input")" != "" ]; then
            regjson=$dir/registry.json
            (umask 077 && jq -c '.registry | {url, username, password}' <<<"$input" >$regjson) || stc_fail "writing the registry login failed"
            args="$args --registry-json $regjson"
        fi
        stc_progress 10 "bootstrapping the first monitor on $name"
        # The configuration and the admin key go to the cluster's own directory, not /etc/ceph/ceph.conf
        cephadm --image "$image" bootstrap $args --output-dir /var/lib/ceph/$fsid/bootstrap --no-cleanup-on-failure
        rc=$?
        [ -n "$regjson" ] && rm -f $regjson
        [ $rc -eq 0 ] || stc_fail "cephadm bootstrap failed"
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

# The addresses of the mons, without their ports: the clients try msgr v2, then v1
function mon_addrs_json()
{
    ceph_admin $fsid mon dump -f json | jq -c '[.mons[].public_addrs.addrvec[] | select(.type == "v2") | .addr | split(":")[0]] | unique'
}

function do_mon_addrs()
{
    local mons
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    mons=$(mon_addrs_json)
    [ "$(jq length <<<"$mons" 2>/dev/null)" -gt 0 ] 2>/dev/null || stc_fail "the mon map has no address"
    echo "mon addresses: $mons"
    stc_result "$(jq -cn --argjson m "$mons" '{mon_addrs: $m}')"
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
    # The metrics of the cluster come from the active mgr (shared-storage-design.md §14.3, port 9283); the health
    # watchdog switches the module on again when it is off
    ceph_admin $fsid mgr module enable prometheus >/dev/null 2>&1 || echo "the mgr prometheus module could not be enabled"
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
        # Clients reclaiming their global_id without proving it may be someone else (CVE-2021-20288); every client of
        # CloudLand is newer than the fix. cephadm bootstrap sets it too, here it stays so when cephadm changes
        ceph_admin $fsid config set mon auth_allow_insecure_global_id_reclaim false &&
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
    stc_progress 80 "binding the metrics of the mgrs to the addresses of their hosts"
    ceph_prometheus_bind $fsid || echo "binding the mgr prometheus modules failed: the health check does it again"
    mons=$(mon_addrs_json)
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

# make_osd <hostname> <device> <kernel name> <media>: an OSD on the device (one there already counts), up and with
# the device class of its media; sets osd_id
function make_osd()
{
    local name=$1 dev=$2 kname=$3 media=$4 id res
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
    osd_id=$id
}

# osd_row <json row>: reads and checks an OSD to make (key, name, dev, kname, media)
function osd_row()
{
    key=$(jq -r .key <<<"$1")
    name=$(jq -r .hostname <<<"$1")
    dev=$(jq -r .device <<<"$1")
    kname=$(jq -r .kname <<<"$1")
    media=$(jq -r '.media // ""' <<<"$1")
    valid_name "$name" || stc_fail "invalid host name $name"
    [[ "$dev" =~ ^/dev/[A-Za-z0-9/_.-]+$ ]] && [[ "$kname" =~ ^[A-Za-z0-9_.-]+$ ]] || stc_fail "invalid device $dev ($kname)"
    [ -z "$media" ] || [[ "$media" =~ ^(hdd|ssd|nvme)$ ]] || stc_fail "invalid media $media"
}

function do_create_osds()
{
    local row key name dev kname media osd_id out="[]" n=0 total
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    total=$(jq '.osds | length' <<<"$input")
    # 20.2 checks the device against the inventory of the host, which a host added a moment ago does not have yet and
    # which never lists a logical volume (a loop device under LVM); 19.2 inventories with --filter-for-batch, so a host
    # whose disks are all in use has an empty one for good, and does not check. The inventory is refreshed, and the
    # check is skipped when it gets in the way
    ceph_admin $fsid orch device ls --refresh >/dev/null 2>&1
    for row in $(jq -c '.osds[]' <<<"$input"); do
        osd_row "$row"
        n=$((n + 1))
        stc_progress $((5 + 85 * (n - 1) / total)) "making an OSD on $name:$dev"
        make_osd "$name" "$dev" "$kname" "$media"
        out=$(jq -c --arg k "$key" --argjson i "$osd_id" '. + [{key: $k, osd_id: $i}]' <<<"$out")
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

# removal_room <OSD ids as JSON>: the OSDs that stay take the data of the ones leaving without getting near full
# (shared-storage-design.md §8.5; Ceph stops the writes of a full OSD). Per device class: the pools of a media keep
# their data on OSDs of that class. What moves is the data of the leaving OSDs (kb_used_data, not their own metadata)
function removal_room()
{
    local ids=$1 df ratio short
    df=$(ceph_admin $fsid osd df -f json 2>/dev/null) || stc_fail "reading the capacity of the OSDs failed"
    ratio=$(ceph_admin $fsid osd dump -f json 2>/dev/null | jq -r '.nearfull_ratio // 0.85')
    short=$(jq -r --argjson i "$ids" --argjson r "$ratio" '
        [.nodes[] | {c: (.device_class // "none"), kb, used: .kb_used, data: (.kb_used_data // .kb_used), gone: (.id as $x | $i | index($x) != null)}]
        | group_by(.c)[] | select(any(.gone))
        | {c: .[0].c, left: (map(select(.gone | not) | .kb) | add // 0), stay: (map(select(.gone | not) | .used) | add // 0),
           moving: (map(select(.gone) | .data) | add // 0)}
        | select(.moving > 0 and (.left == 0 or .stay + .moving >= .left * $r))
        | if .left == 0 then "no \(.c) OSD would be left for the data"
          else "the \(.c) OSDs that stay would be \(((.stay + .moving) * 100 / .left) | floor)% full" end' <<<"$df" | head -1)
    [ -z "$short" ] || stc_fail "the data of the OSDs has no room on the others: $short (Ceph calls $(awk -v r="$ratio" 'BEGIN { printf "%d", r * 100 }')% near full)"
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
        removal_room "$ids"
        # orch osd rm marks them out, waits until their data is elsewhere, removes them and zaps the devices
        ceph_admin $fsid orch osd rm $(jq -r 'join(" ")' <<<"$ids") --zap || stc_fail "starting the removal of the OSDs failed"
        wait_removed "$ids"
    fi
    stc_result "$(jq -cn --argjson i "$ids" '{removed: ($i | length)}')"
}

# osd_status <id>: up, down or destroyed as the OSD map has it; nothing when the OSD is not in it
function osd_status()
{
    ceph_admin $fsid osd tree -f json 2>/dev/null | jq -r --argjson i "$1" '.nodes[]? | select(.id == $i) | .status' | head -1
}

# The disk of an OSD failed: the OSD is destroyed and keeps its id, and the new disk of the same host takes the id
# (cephadm hands the destroyed ids of a host to its next OSDs). Not orch osd rm --replace: cephadm waits for
# safe-to-destroy even with --force, and with as many OSD hosts as replicas the degraded data has nowhere to go, so a
# failed OSD never is. Its daemon is removed and it is destroyed directly; the data comes back from the other copies
function do_replace_osd()
{
    local old row key name dev kname media osd_id status
    old=$(jq -r '.old_id' <<<"$input")
    [[ "$old" =~ ^[0-9]+$ ]] || stc_fail "invalid OSD id $old"
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    [ "$(jq '.osds | length' <<<"$input")" = 1 ] || stc_fail "one disk replaces one OSD"
    row=$(jq -c '.osds[0]' <<<"$input")
    osd_row "$row"
    if [ "$(osd_of "$name" "$kname")" = "$old" ]; then
        echo "osd.$old is on $name:$dev already"
    else
        status=$(osd_status $old)
        case "$status" in
            destroyed) echo "osd.$old is destroyed already" ;;
            up) stc_fail "osd.$old is up: its disk works, remove it the normal way" ;;
            "") stc_fail "osd.$old is not in the cluster" ;;
            *)
                stc_progress 10 "destroying osd.$old, keeping its id"
                # A removal queued by hand or by an earlier version would destroy or purge it under us
                ceph_admin $fsid orch osd rm stop $old >/dev/null 2>&1
                ceph_admin $fsid osd out $old || stc_fail "marking osd.$old out failed"
                if ceph_admin $fsid orch ps --daemon_type osd -f json 2>/dev/null | jq -e --arg n "osd.$old" 'any(.[]; .daemon_name == $n)' >/dev/null; then
                    ceph_admin $fsid orch daemon rm osd.$old --force || stc_fail "removing the daemon of osd.$old failed"
                fi
                ceph_admin $fsid osd destroy $old --force --yes-i-really-mean-it || stc_fail "destroying osd.$old failed"
                wait_for 120 "osd.$old destroyed" eval '[ "$(osd_status $old)" = destroyed ]' || stc_fail "osd.$old was not destroyed"
                ;;
        esac
        stc_progress 40 "making the new OSD on $name:$dev"
    fi
    make_osd "$name" "$dev" "$kname" "$media"
    [ "$osd_id" = "$old" ] || echo "the new disk became osd.$osd_id, not osd.$old"
    # The old one was marked out: the new one takes the data back
    ceph_admin $fsid osd in $osd_id >/dev/null 2>&1
    ceph_admin $fsid orch set-unmanaged osd.default >/dev/null 2>&1
    stc_result "$(jq -cn --arg k "$key" --argjson i "$osd_id" '{osds: [{key: $k, osd_id: $i}]}')"
}

# The labels of a host follow its roles: mon, mgr and _admin are added and taken away, osd only added (it follows the
# disks, which their own tasks move); a client that gets its first label becomes a cephadm host
function do_set_labels()
{
    local name ip want have mons l
    name=$(jq -r .hostname <<<"$input")
    ip=$(jq -r '.ip // ""' <<<"$input")
    mons=$(jq -r '.mons // 1' <<<"$input")
    want=$(jq -r '.labels | join(" ")' <<<"$input")
    valid_name "$name" || stc_fail "invalid host name $name"
    [[ "$mons" =~ ^[1-9]$ ]] || stc_fail "invalid mon count $mons"
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    have=$(ceph_admin $fsid orch host ls --format json | jq -r --arg h "$name" '.[] | select(.hostname == $h) | (.labels // []) | join(" ")')
    if ! ceph_admin $fsid orch host ls --format json | jq -e --arg h "$name" '.[] | select(.hostname == $h)' >/dev/null; then
        [ -n "$want" ] || { stc_result '{"labels": []}'; return 0; }
        valid_ip "$ip" || stc_fail "invalid address $ip"
        ceph_admin $fsid orch host add "$name" "$ip" --labels "${want// /,}" || stc_fail "adding $name ($ip) failed: can the mgr log in to it?"
    else
        for l in $want; do
            [[ " $have " == *" $l "* ]] && continue
            stc_progress 20 "labelling $name $l"
            ceph_admin $fsid orch host label add "$name" "$l" >/dev/null || stc_fail "labelling $name $l failed"
        done
        for l in $have; do
            case "$l" in mon|mgr|_admin) ;; *) continue ;; esac
            [[ " $want " == *" $l "* ]] && continue
            stc_progress 40 "taking label $l from $name"
            ceph_admin $fsid orch host label rm "$name" "$l" >/dev/null || stc_fail "taking label $l from $name failed"
        done
    fi
    stc_progress 60 "waiting for $mons monitors in quorum"
    wait_for 900 "monitors in quorum" eval '[ "$(ceph_admin $fsid quorum_status -f json 2>/dev/null | jq ".quorum_names | length")" = "$mons" ]' ||
        stc_fail "the monitors did not settle at $mons in 15 minutes"
    wait_for 300 "an active mgr" eval 'ceph_admin $fsid mgr stat -f json 2>/dev/null | jq -e .available >/dev/null' ||
        stc_fail "no mgr became active"
    stc_result "$(jq -cn --arg l "$want" '{labels: ($l | split(" ") | map(select(. != "")))}')"
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
        [ "$(jq length <<<"$ids")" -gt 0 ] && ! osds_gone "$ids" && removal_room "$ids"
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

# fence_ceph <ceph arguments>: with the admin key of a managed cluster, or the CloudLand client of an imported one
function fence_ceph()
{
    local user
    user=$(jq -r '.client_user // empty' <<<"$input")
    if [ -n "$user" ]; then
        [[ "$user" =~ ^[A-Za-z0-9_.-]+$ ]] || stc_fail "invalid client user $user"
        timeout ${CEPH_TIMEOUT:-120} ceph --conf /etc/ceph/$uuid.conf --id $user "$@"
    else
        ceph_admin $fsid "$@"
    fi
}

# blocklisted <address>: whether the blocklist has the range of one address
function blocklisted()
{
    fence_ceph osd blocklist ls 2>/dev/null | awk '{print $1}' | grep -qx "cidr:$1:0/32"
}

# fence: a blocklist range for the host's address, with a long expiry: the default one (an hour) would let the old
# writer back in while what was recovered elsewhere uses its disks
function do_fence()
{
    local addr expire
    addr=$(jq -r .address <<<"$input")
    expire=$(jq -r '.expire // 315360000' <<<"$input")
    [[ "$addr" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || stc_fail "invalid address $addr"
    [[ "$expire" =~ ^[0-9]+$ ]] || stc_fail "invalid expiry $expire"
    stc_progress 30 "blocklisting $addr"
    fence_ceph osd blocklist range add $addr/32 $expire || stc_fail "adding $addr to the blocklist failed"
    blocklisted $addr || stc_fail "$addr is not on the blocklist"
    stc_result "$(jq -cn --arg a "$addr" '{address: $a}')"
}

function do_unfence()
{
    local addr
    addr=$(jq -r .address <<<"$input")
    [[ "$addr" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || stc_fail "invalid address $addr"
    if blocklisted $addr; then
        stc_progress 30 "removing $addr from the blocklist"
        fence_ceph osd blocklist range rm $addr/32 ||
            stc_fail "removing $addr from the blocklist failed (an imported cluster needs its admin to run: ceph osd blocklist range rm $addr/32)"
    else
        echo "$addr is not on the blocklist"
    fi
    blocklisted $addr && stc_fail "$addr is still on the blocklist"
    stc_result "$(jq -cn --arg a "$addr" '{address: $a}')"
}

# check_hosts: the orchestrator logs in to every host of the input
function check_hosts()
{
    local name ip failed=""
    while read -r name ip; do
        valid_name "$name" && valid_ip "$ip" || stc_fail "invalid host $name $ip"
        ceph_admin $fsid cephadm check-host "$name" "$ip" >/dev/null 2>&1 || failed="$failed $name"
    done < <(jq -r '.hosts[] | "\(.hostname) \(.ip)"' <<<"$input")
    [ -z "$failed" ] || stc_fail "the orchestrator can not log in to:$failed"
}

# cephadm_has_key: the orchestrator holds the key pair of the input, as stored
function cephadm_has_key()
{
    [ "$(ceph_admin $fsid config-key get mgr/cephadm/ssh_identity_pub 2>/dev/null | xargs)" = "$(jq -r .public_key <<<"$input" | xargs)" ] &&
        [ "$(ceph_admin $fsid config-key get mgr/cephadm/ssh_identity_key 2>/dev/null)" = "$(jq -r .private_key <<<"$input")" ]
}

# mgr_restart: the active mgr fails over, so the orchestrator loads its keys again and drops the connections it keeps
# open to the hosts (a check through one of those proves nothing about the key)
function mgr_restart()
{
    local i
    ceph_admin $fsid mgr fail >/dev/null || stc_fail "ceph mgr fail failed"
    for i in $(seq 1 60); do
        sleep 3
        [ "$(ceph_admin $fsid mgr stat -f json 2>/dev/null | jq -r '.available // false')" = "true" ] &&
            ceph_admin $fsid cephadm get-pub-key >/dev/null 2>&1 && return 0
    done
    stc_fail "no mgr with the orchestrator came back in 3 minutes"
}

function do_cephadm_key()
{
    local tmp
    [[ "$(jq -r .public_key <<<"$input")" =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+$ ]] || stc_fail "invalid public key"
    if cephadm_has_key; then
        echo "the orchestrator holds the new key already"
    else
        tmp=$(mktemp -d)
        (umask 077 && jq -r .private_key <<<"$input" >$tmp/key && jq -r .public_key <<<"$input" >$tmp/key.pub)
        stc_progress 30 "giving the orchestrator the new key"
        # Both at once: cephadm set-priv-key / set-pub-key check the new half against the other, old one, and quietly
        # keep the old pair (exit status 0, "Public key mismatch" in the mgr log; seen on 20.2)
        if ! ceph_admin $fsid config-key set mgr/cephadm/ssh_identity_key -i $tmp/key >/dev/null ||
            ! ceph_admin $fsid config-key set mgr/cephadm/ssh_identity_pub -i $tmp/key.pub >/dev/null; then
            rm -rf $tmp
            stc_fail "storing the new key of the orchestrator failed"
        fi
        rm -rf $tmp
    fi
    stc_progress 50 "restarting the mgr so the orchestrator takes it"
    mgr_restart
    cephadm_has_key || stc_fail "the orchestrator does not hold the new key"
    [ "$(ceph_admin $fsid cephadm get-pub-key 2>/dev/null | xargs)" = "$(jq -r .public_key <<<"$input" | xargs)" ] ||
        stc_fail "the orchestrator did not load the new key"
    stc_progress 70 "checking the orchestrator reaches every host with it"
    check_hosts
    stc_result '{"switched": true}'
}

# cephadm_check: after the old key is refused, the orchestrator (with fresh connections) still reaches every host
function do_cephadm_check()
{
    [ "$(ceph_admin $fsid cephadm get-pub-key 2>/dev/null | xargs)" = "$(jq -r .public_key <<<"$input" | xargs)" ] ||
        stc_fail "the orchestrator does not use the key of the cluster"
    mgr_restart
    check_hosts
    stc_result '{"checked": true}'
}

function client_entity()
{
    local user
    user=$(jq -r .client_user <<<"$input")
    [[ "$user" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] || stc_fail "invalid client user"
    echo "client.$user"
}

function do_client_pending()
{
    local entity key
    entity=$(client_entity) || exit 1
    stc_progress 30 "making a pending key for $entity"
    key=$(ceph_admin $fsid auth get-or-create-pending $entity -f json 2>/dev/null | jq -r '.[0].pending_key // empty')
    [[ "$key" =~ ^[A-Za-z0-9+/]{38,64}={0,2}$ ]] || stc_fail "the cluster made no pending key for $entity"
    stc_result "$(jq -cn --arg k "$key" '{client_key_pending: $k}')"
}

function do_client_commit()
{
    local entity want auth key pending
    entity=$(client_entity) || exit 1
    want=$(jq -r .client_key <<<"$input")
    [[ "$want" =~ ^[A-Za-z0-9+/]{38,64}={0,2}$ ]] || stc_fail "invalid client key"
    auth=$(ceph_admin $fsid auth get $entity -f json 2>/dev/null) || stc_fail "reading $entity failed"
    key=$(jq -r '.[0].key // empty' <<<"$auth")
    pending=$(jq -r '.[0].pending_key // empty' <<<"$auth")
    if [ "$key" = "$want" ]; then
        # The usual case: a host used the pending key (the check of its client configuration), which committed it
        echo "the key of $entity is the new one already"
    else
        # The key every host was given: another pending key would leave them all out
        [ "$pending" = "$want" ] || stc_fail "the pending key of $entity is not the one the hosts were given: run the rotation again"
        stc_progress 50 "committing the pending key of $entity"
        ceph_admin $fsid auth commit-pending $entity >/dev/null || stc_fail "committing the pending key of $entity failed"
        key=$(ceph_admin $fsid auth get $entity -f json 2>/dev/null | jq -r '.[0].key // empty')
        [ "$key" = "$want" ] || stc_fail "the key of $entity is not the new one after the commit"
    fi
    stc_result '{"committed": true}'
}

# daemon_versions: the releases the daemons of the cluster run, one a line
function daemon_versions()
{
    ceph_admin $fsid versions -f json 2>/dev/null | jq -r '.overall // {} | keys[]' | awk '{print $3}' | sort -u
}

# upgrade_status: the upgrade cephadm runs as JSON; {} when it runs none (it then answers in words, not JSON)
function upgrade_status()
{
    local out
    out=$(ceph_admin $fsid orch upgrade status -f json 2>/dev/null)
    jq -ce 'objects' <<<"$out" 2>/dev/null || echo '{}'
}

function do_upgrade()
{
    local version image status have checks done total
    version=$(jq -r .version <<<"$input")
    image=$(jq -r .image <<<"$input")
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || stc_fail "invalid release $version"
    [[ "$image" =~ ^[a-z0-9][a-z0-9./:_@-]{0,199}$ ]] || stc_fail "invalid image $image"
    have=$(daemon_versions | xargs)
    [ -n "$have" ] || stc_fail "the cluster does not tell the releases of its daemons"
    status=$(upgrade_status)
    if [ "$have" = "$version" ] && [ "$(jq -r '.in_progress // false' <<<"$status")" != "true" ]; then
        echo "every daemon runs Ceph $version already"
        stc_result "$(jq -cn --arg v "$version" '{version: $v}')"
        return 0
    fi
    if [ "$(jq -r '.in_progress // false' <<<"$status")" = "true" ]; then
        # An upgrade going on (an earlier run of this step): it is followed if it goes where this one goes
        [ "$(jq -r '.target_image // ""' <<<"$status")" = "$image" ] ||
            stc_fail "cephadm upgrades to $(jq -r '.target_image // "another image"' <<<"$status") already: stop it (ceph orch upgrade stop) or let it end first"
        echo "following the upgrade to $image going on"
    else
        stc_progress 5 "upgrading the daemons from Ceph $have to $version ($image)"
        ceph_admin $fsid orch upgrade start --image "$image" || stc_fail "ceph orch upgrade start failed"
    fi
    while true; do
        sleep 15
        status=$(upgrade_status)
        checks=$(ceph_admin $fsid health detail -f json 2>/dev/null | jq -r '.checks // {} | to_entries[] | select(.key | startswith("UPGRADE_")) |
            "\(.key): \(.value.summary.message)"')
        [ -n "$checks" ] && stc_fail "the upgrade stopped: $checks (ceph orch upgrade status tells more; fix it, then retry)"
        [ "$(jq -r '.is_paused // false' <<<"$status")" = "true" ] && stc_fail "the upgrade is paused: $(jq -r '.message // ""' <<<"$status")"
        [ "$(jq -r '.in_progress // false' <<<"$status")" = "true" ] || break
        # "5/12 daemons upgraded"
        read -r done total < <(jq -r '.progress // ""' <<<"$status" | grep -oE '^[0-9]+/[0-9]+' | tr '/' ' ')
        if [[ "$done" =~ ^[0-9]+$ ]] && [[ "$total" =~ ^[1-9][0-9]*$ ]]; then
            stc_progress $(( 5 + 90 * done / total )) "$done of $total daemons on Ceph $version"
        fi
    done
    have=$(daemon_versions | xargs)
    [ "$have" = "$version" ] || stc_fail "the daemons run Ceph $have after the upgrade, not $version"
    echo "every daemon runs Ceph $version"
    stc_result "$(jq -cn --arg v "$version" '{version: $v}')"
}

function stc_main()
{
    local action uuid
    input=$(cat)
    action=$(jq -r .action <<<"$input")
    uuid=$(jq -r .cluster_uuid <<<"$input")
    fsid=$(jq -r .fsid <<<"$input")
    valid_uuid "$uuid" && valid_uuid "$fsid" || stc_fail "invalid cluster uuid or fsid"
    # A fence is urgent and does not wait for a structural job that may hang on the very host that is down
    case "$action" in
        fence | unfence) stc_lock "ceph-fence-$uuid" ;;
        *) stc_lock "ceph-$uuid" ;;
    esac
    case "$action" in
        fence) do_fence ;;
        unfence) do_unfence ;;
        cephadm_key) do_cephadm_key ;;
        cephadm_check) do_cephadm_check ;;
        client_pending) do_client_pending ;;
        client_commit) do_client_commit ;;
        upgrade) do_upgrade ;;
        bootstrap) do_bootstrap ;;
        add_hosts) do_add_hosts ;;
        configure) do_configure ;;
        mon_addrs) do_mon_addrs ;;
        create_osds) do_create_osds ;;
        remove_osds) do_remove_osds ;;
        remove_host) do_remove_host ;;
        replace_osd) do_replace_osd ;;
        set_labels) do_set_labels ;;
        teardown) do_teardown ;;
        *) stc_fail "unknown action $action" ;;
    esac
}

stc_run "$@"
