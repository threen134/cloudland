# -*- mode: sh -*-
# Node side of the Ceph storage backend (shared-storage-design.md §4.5, §8). Sourced by the step scripts with
# backend_load; defines the hooks every backend file defines, plus helpers of the ceph_*.sh scripts.
#
# A managed cluster runs its daemons in containers under cephadm; its fsid is the uuid of the cluster. The admin
# hosts reach it with the admin configuration cephadm keeps in /var/lib/ceph/<fsid>/config, never /etc/ceph/ceph.conf,
# so a host can be the client of other clusters. Every host reaches every cluster it uses as the client user of that
# cluster: /etc/ceph/<cluster uuid>.conf, its keyring next to it, and the libvirt secret holding the same key.

ceph_conf_dir=/etc/ceph
# CloudLand installed cephadm and pulled the image on this host (ceph_install.sh)
ceph_ours=$run_dir/storage/ceph.cloudland

# ceph_admin <fsid> <args...>: the ceph command as client.admin of a managed cluster, on one of its admin hosts
function ceph_admin()
{
    local fsid=$1
    shift
    timeout ${CEPH_TIMEOUT:-120} ceph --conf /var/lib/ceph/$fsid/config/ceph.conf --keyring /var/lib/ceph/$fsid/config/ceph.client.admin.keyring "$@"
}

function rbd_admin()
{
    local fsid=$1
    shift
    timeout ${CEPH_TIMEOUT:-120} rbd --conf /var/lib/ceph/$fsid/config/ceph.conf --keyring /var/lib/ceph/$fsid/config/ceph.client.admin.keyring "$@"
}

function rados_admin()
{
    local fsid=$1
    shift
    timeout ${CEPH_TIMEOUT:-120} rados --conf /var/lib/ceph/$fsid/config/ceph.conf --keyring /var/lib/ceph/$fsid/config/ceph.client.admin.keyring "$@"
}

# ceph_admin_ready <fsid>: this host has the admin configuration of the cluster and the monitors answer
function ceph_admin_ready()
{
    [ -f /var/lib/ceph/$1/config/ceph.client.admin.keyring ] && CEPH_TIMEOUT=30 ceph_admin $1 fsid >/dev/null 2>&1
}

# backend_metrics <cluster uuid> <managed|external>: the curves of an imported cluster, whose mgr CloudLand does not
# scrape (a managed one is scraped from its mgr, nothing here): read as the client user of the host, which may run
# ceph -s and ceph df (shared-storage-design.md §14.3). Capacity, OSDs, and the counters of all pools summed
function backend_metrics()
{
    local uuid=$1 mode=$2 l="cluster=\"$1\"" conf user status df
    [ "$mode" = external ] || return 0
    conf=$(ceph_client_conf $uuid)
    [ -f "$conf" ] && command -v ceph >/dev/null || return 0
    user=$(sed -n 's/^\[client\.\([A-Za-z0-9][A-Za-z0-9_.-]*\)\]$/\1/p' "$conf" | head -1)
    [ -n "$user" ] || return 0
    status=$(timeout 20 ceph --conf "$conf" --id "$user" -s -f json 2>/dev/null) || return 0
    jq -r --arg l "$l" '
        "cloudland_ceph_total_bytes{\($l)} \(.pgmap.bytes_total // 0)",
        "cloudland_ceph_used_bytes{\($l)} \(.pgmap.bytes_used // 0)",
        "cloudland_ceph_osds_up{\($l)} \(.osdmap.num_up_osds // .osdmap.osdmap.num_up_osds // 0)",
        "cloudland_ceph_osds_in{\($l)} \(.osdmap.num_in_osds // .osdmap.osdmap.num_in_osds // 0)"' <<<"$status" 2>/dev/null
    df=$(timeout 20 ceph --conf "$conf" --id "$user" df detail -f json 2>/dev/null) || return 0
    jq -r --arg l "$l" '[.pools[]?.stats] |
        "cloudland_ceph_read_bytes_total{\($l)} \(map(.rd_bytes // 0) | add // 0)",
        "cloudland_ceph_write_bytes_total{\($l)} \(map(.wr_bytes // 0) | add // 0)",
        "cloudland_ceph_reads_total{\($l)} \(map(.rd // 0) | add // 0)",
        "cloudland_ceph_writes_total{\($l)} \(map(.wr // 0) | add // 0)"' <<<"$df" 2>/dev/null
}

function ceph_client_conf()
{
    echo $ceph_conf_dir/$1.conf
}

function ceph_client_keyring()
{
    echo $ceph_conf_dir/$1.client.$2.keyring
}

# ceph_marker_text <pool uuid>: what the marker object of a pool holds, like the marker file of a file pool
function ceph_marker_text()
{
    printf 'driver=ceph_rbd\npool_uuid=%s\n' "$1"
}

# ceph_loop_vg <cluster uuid> <disk id>: the LVM volume group CloudLand puts on a loop device for an OSD, since
# ceph-volume refuses loop devices themselves
function ceph_loop_vg()
{
    echo "clceph-${1:0:8}-$(echo -n "$2" | md5sum | cut -c1-12)"
}

# backend_existing prints why the host already runs Ceph that CloudLand did not set up, nothing when it does not.
# The daemons of a cluster this host is a member of are its own (a deployment run again)
function backend_existing()
{
    local d fsid
    for d in /var/lib/ceph/*-*-*-*-*/; do
        [ -d "$d" ] || continue
        fsid=$(basename "$d")
        [ -f $run_dir/storage/$fsid/member ] && continue
        echo "the host runs daemons of Ceph cluster $fsid"
        return 0
    done
}

# backend_disk_path <cluster uuid> <disk id> <device>: the device an OSD is made on. A loop device (test setups,
# storage_allow_loop) gets a volume group of its own and the OSD goes on its logical volume
function backend_disk_path()
{
    local uuid=$1 id=$2 dev=$3 vg
    case "$id" in
        loop:*) ;;
        *) echo "$dev"; return 0 ;;
    esac
    vg=$(ceph_loop_vg "$uuid" "$id")
    if ! vgs $vg >/dev/null 2>&1; then
        pvcreate -ff -y $dev >&2 && vgcreate $vg $dev >&2 && lvcreate -y -l 100%FREE -n osd $vg >&2 || return 1
    fi
    # Not active after the loop device was set up again (a restart)
    vgchange -ay $vg >&2 || return 1
    echo "/dev/$vg/osd"
}

# backend_disks_resolved <cluster uuid> <stable id>...: the disks of the cluster on this host; the volume groups of
# loop devices that left go, so the device can be wiped
function backend_disks_resolved()
{
    local uuid=$1 id vg keep=""
    shift
    for id in "$@"; do
        keep="$keep $(ceph_loop_vg "$uuid" "$id")"
    done
    for vg in $(vgs --noheadings -o vg_name 2>/dev/null | grep -o "clceph-${uuid:0:8}-[0-9a-f]*"); do
        [[ " $keep " == *" $vg "* ]] && continue
        ceph_drop_vg $vg
    done
    return 0
}

function ceph_drop_vg()
{
    local vg=$1 pv
    pv=$(pvs --noheadings -o pv_name -S vg_name=$vg 2>/dev/null | xargs)
    vgremove -f -y $vg >/dev/null 2>&1
    [ -n "$pv" ] && pvremove -ff -y $pv >/dev/null 2>&1
    return 0
}

# ceph_unit_order <cluster uuid>: the daemons of a managed cluster on this host stop before its mon. At shutdown
# systemd stops the units of a host together, and an OSD telling the cluster it goes down may tell the mon of its own
# host, stopping at the same moment: the cluster then marks the OSD down only after the heartbeat grace, and the I/O of
# every client on its placement groups stalls that long (13 seconds when a host was rebooted). The units of the Ceph
# packages order the OSDs after the mons for this reason; the ones cephadm writes do not. %l is the short host name,
# which cephadm names the mon after; on the mon itself systemd drops the ordering on itself, with a warning
function ceph_unit_order()
{
    local d=/etc/systemd/system/ceph-$1@.service.d
    mkdir -p $d || return 1
    printf '[Unit]\n# CloudLand: stop before the mon of this host, so the cluster hears the daemons going down\nAfter=ceph-%s@mon.%%l.service\n' "$1" >$d/cloudland-order.conf || return 1
    systemctl daemon-reload
}

function ceph_oom_script()
{
    echo "$(dirname "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")")/ceph_oom_protect.sh"
}

# ceph_unit_oom <cluster uuid> <oom_score_adj> <mon bytes> <mgr bytes> <osd bytes>: every start of a mon, mgr or OSD
# of the cluster on this host gives its container the oom_score_adj and the memory.low of its kind of daemon
# (ceph_oom_protect.sh, shared-storage-design.md §6.7.2); the daemons running already get them now. The script runs
# in a transient unit of its own: it waits for the container, and the start timeout of the daemon's unit (200 s)
# would count that wait, stopping a daemon that was coming up. Run through bash, not depending on its mode bits
function ceph_unit_oom()
{
    local uuid=$1 adj=$2 mon=$3 mgr=$4 osd=$5 d=/etc/systemd/system/ceph-$1@.service.d u inst script
    script=$(ceph_oom_script)
    [[ "$adj" =~ ^-?[0-9]{1,4}$ ]] && [[ "$mon$mgr$osd" =~ ^[0-9]+$ ]] || return 1
    mkdir -p $d || return 1
    printf '[Service]\n# CloudLand: the daemon goes after the VMs when the memory runs out, and keeps what the host holds back for it\nExecStartPost=-/usr/bin/systemd-run --no-block --quiet --collect -p RuntimeMaxSec=300 /bin/bash %s %s %%i %s %s %s %s $MAINPID\n' \
        "$script" "$uuid" "$adj" "$mon" "$mgr" "$osd" >$d/cloudland-oom.conf || return 1
    systemctl daemon-reload
    for u in $(systemctl list-units --no-legend --plain --state=active "ceph-$uuid@*.service" | awk '{print $1}'); do
        inst=${u#ceph-$uuid@}
        inst=${inst%.service}
        bash "$script" "$uuid" "$inst" "$adj" "$mon" "$mgr" "$osd" || echo "protecting $inst failed"
    done
    return 0
}

# ceph_unit_order_remove <cluster uuid>: the drop-ins of the units of the cluster (order and OOM protection)
function ceph_unit_order_remove()
{
    [ -d /etc/systemd/system/ceph-$1@.service.d ] || return 0
    rm -rf /etc/systemd/system/ceph-$1@.service.d
    systemctl daemon-reload
}

# ceph_client_remove <cluster uuid> <secret uuid>: the client configuration, keyring and libvirt secret of a cluster
function ceph_client_remove()
{
    local uuid=$1 secret=${2:-$1}
    rm -f $(ceph_client_conf $uuid) $ceph_conf_dir/$uuid.client.*.keyring
    virsh secret-undefine $secret >/dev/null 2>&1
    return 0
}

# backend_leave <cluster uuid> <purge true|false>: what is left of a deleted managed cluster on a host: its daemons
# and OSDs (cephadm rm-cluster zaps the OSD devices), the admin files, the client configuration, the volume groups of
# loop devices. purge also removes cephadm and the image, when no other cluster uses them
function backend_leave()
{
    local uuid=$1 purge=$2 vg f others
    if [ -d /var/lib/ceph/$uuid ] && command -v cephadm >/dev/null; then
        cephadm rm-cluster --force --zap-osds --fsid $uuid || return 1
    fi
    ceph_unit_order_remove $uuid
    for f in $ceph_conf_dir/ceph.conf $ceph_conf_dir/ceph.client.admin.keyring $ceph_conf_dir/ceph.pub; do
        [ -f $f ] || continue
        # Only what belongs to this cluster: the configuration names its fsid, the key and public key came with it
        if [ "$f" = "$ceph_conf_dir/ceph.conf" ]; then
            grep -q "fsid = $uuid" $f && rm -f $f
        elif ! grep -qs "fsid = " $ceph_conf_dir/ceph.conf; then
            rm -f $f
        fi
    done
    ceph_client_remove $uuid
    for vg in $(vgs --noheadings -o vg_name 2>/dev/null | grep -o "clceph-${uuid:0:8}-[0-9a-f]*"); do
        ceph_drop_vg $vg
    done
    rm -rf /var/lib/ceph/$uuid /var/log/ceph/$uuid /var/run/ceph/$uuid
    # The protection of system.slice covers the daemons left on the host
    bash "$(ceph_oom_script)" --sweep >/dev/null 2>&1
    if [ "$purge" = "true" ] && [ -f $ceph_ours ]; then
        others=$(ls -d /var/lib/ceph/*-*-*-*-*/ 2>/dev/null)
        if [ -z "$others" ]; then
            DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=600 purge -y -q cephadm >/dev/null 2>&1
            docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | grep '^quay.io/ceph/ceph:' | xargs -r docker rmi >/dev/null 2>&1
            rm -f $ceph_ours
        fi
    fi
    return 0
}

# backend_forget <cluster uuid>: CloudLand stops using an imported cluster: only the client part goes
function backend_forget()
{
    ceph_client_remove "$1"
}

# ceph_prometheus_bind <fsid>: the prometheus module of each mgr of a managed cluster listens on the address of its
# host (the one cephadm knows it by, the address the metrics are scraped from), not on every address of the host
# (shared-storage-design.md §14.3): its default :: answers on the public network too. The address is a localized
# option of the mgr (mgr/prometheus/<mgr name>/server_addr); the module reads it when it starts, so a standby whose
# address changed is restarted and the active mgr fails over, and the orchestrator is waited for (the next steps of a
# task use it). A mgr placed later (a host added, a role changed) is bound at the next health check
function ceph_prometheus_bind()
{
    local fsid=$1 hosts mgrs dump row id host addr cur active activechanged=0 i
    local -a restart=()
    hosts=$(ceph_admin $fsid orch host ls -f json 2>/dev/null) || return 1
    mgrs=$(ceph_admin $fsid orch ps --daemon-type mgr -f json 2>/dev/null) || return 1
    active=$(ceph_admin $fsid mgr stat -f json 2>/dev/null | jq -r '.active_name // empty')
    # What is set, read from the configuration database: a localized module option is not one config get knows
    dump=$(ceph_admin $fsid config dump -f json 2>/dev/null) || return 1
    while read -r row; do
        [ -z "$row" ] && continue
        id=$(jq -r .id <<<"$row")
        host=$(jq -r .host <<<"$row")
        addr=$(jq -r --arg h "$host" '.[] | select(.hostname == $h) | .addr' <<<"$hosts" | head -1)
        [[ "$id" =~ ^[A-Za-z0-9_.-]+$ ]] && [[ "$addr" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
        cur=$(jq -r --arg n "mgr/prometheus/$id/server_addr" '.[] | select(.name == $n) | .value' <<<"$dump" | head -1)
        [ "$cur" = "$addr" ] && continue
        ceph_admin $fsid config set mgr mgr/prometheus/$id/server_addr $addr >/dev/null || return 1
        echo "the prometheus module of mgr $id listens on $addr"
        if [ "$id" = "$active" ]; then
            activechanged=1
        else
            restart+=("$id")
        fi
    done < <(jq -c '.[] | {id: .daemon_id, host: .hostname}' <<<"$mgrs")
    for id in "${restart[@]}"; do
        ceph_admin $fsid orch daemon restart mgr.$id >/dev/null 2>&1
    done
    [ $activechanged = 1 ] || return 0
    ceph_admin $fsid mgr fail "$active" >/dev/null 2>&1
    for i in $(seq 1 60); do
        sleep 2
        ceph_admin $fsid mgr stat -f json 2>/dev/null | jq -e '.available == true' >/dev/null &&
            ceph_admin $fsid orch status -f json >/dev/null 2>&1 && return 0
    done
    echo "the mgrs did not come back in 2 minutes after the failover"
    return 1
}

# backend_health <cluster uuid> <input>: the health of the cluster as this host sees it, the JSON of
# shared-storage-design.md §14.1 on stdout: ceph health detail (HEALTH_OK / WARN / ERR and its checks), the hosts
# (ceph orch host ls), the OSDs (ceph osd tree), the capacity (ceph df), nearfull when Ceph says an OSD or a pool is
# near full. A managed cluster is checked as client.admin on an admin host, an imported one as its client user, which
# may not see the hosts or the OSDs. On a managed cluster the mgr prometheus module is switched on when it is off, and
# every mgr's listens on the address of its host (§14.3: the metrics come from it).
function backend_health()
{
    local uuid=$1 input=$2 mode fsid user conf detail status health hosts tree df
    local -a c
    mode=$(jq -r '.mode // empty' <<<"$input")
    fsid=$(jq -r '.fsid // empty' <<<"$input")
    if [ "$mode" = managed ]; then
        if ! ceph_admin_ready "$fsid"; then
            jq -cn '{health: "unknown", error: "this host has no working admin configuration of the cluster"}'
            return
        fi
        c=(ceph --conf /var/lib/ceph/$fsid/config/ceph.conf --keyring /var/lib/ceph/$fsid/config/ceph.client.admin.keyring)
    else
        user=$(jq -r '.client_user // empty' <<<"$input")
        conf=$(jq -r '.conf // empty' <<<"$input")
        [[ "$user" =~ ^[A-Za-z0-9_.-]+$ ]] && [ -f "$conf" ] || {
            jq -cn '{health: "unknown", error: "this host has no client configuration of the cluster"}'
            return
        }
        c=(ceph --conf "$conf" --id "$user")
    fi
    detail=$(timeout 60 "${c[@]}" health detail -f json 2>/dev/null)
    status=$(jq -r '.status // empty' <<<"$detail" 2>/dev/null)
    case "$status" in
        HEALTH_OK) health=healthy ;;
        HEALTH_WARN) health=warning ;;
        HEALTH_ERR) health=error ;;
        *)
            jq -cn '{health: "unknown", error: "the monitors of the cluster do not answer"}'
            return
            ;;
    esac
    if [ "$mode" = managed ]; then
        hosts=$(timeout 60 "${c[@]}" orch host ls -f json 2>/dev/null)
        if ! timeout 60 "${c[@]}" mgr module ls -f json 2>/dev/null | jq -e '.enabled_modules | index("prometheus")' >/dev/null 2>&1; then
            timeout 60 "${c[@]}" mgr module enable prometheus >/dev/null 2>&1
        fi
        CEPH_TIMEOUT=60 ceph_prometheus_bind "$fsid" >/dev/null 2>&1
    fi
    tree=$(timeout 60 "${c[@]}" osd tree -f json 2>/dev/null)
    df=$(timeout 60 "${c[@]}" df -f json 2>/dev/null)
    jq -n --arg h $health --arg s "$status" --argjson d "$detail" --arg hosts "$hosts" --arg tree "$tree" --arg df "$df" '
        def parsed($x): ($x | try fromjson catch null);
        {health: $h, summary: $s,
         messages: [($d.checks // {}) | to_entries[] | "\(.key): \(.value.summary.message // "")"],
         flags: (if ($d.checks // {}) | keys | any(. == "OSD_NEARFULL" or . == "POOL_NEARFULL" or . == "OSD_FULL" or . == "POOL_FULL"
                     or . == "OSD_BACKFILLFULL" or . == "POOL_BACKFILLFULL") then ["nearfull"] else [] end),
         nodes: [(parsed($hosts) // [])[] | {name: .hostname, state: (if (.status // "") == "" then "active" else (.status | ascii_downcase) end)}],
         disks: [((parsed($tree) // {}).nodes // [])[] | select(.type == "osd") | {name: .name, state: (.status // "unknown")}],
         capacity: ((parsed($df) // {}).stats // null | if . then {total: .total_bytes, free: .total_avail_bytes} else null end)}'
}
