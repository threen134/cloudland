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

# backend_health <cluster uuid> <input>: the health of the cluster as this host sees it, the JSON of
# shared-storage-design.md §14.1 on stdout: ceph health detail (HEALTH_OK / WARN / ERR and its checks), the hosts
# (ceph orch host ls), the OSDs (ceph osd tree), the capacity (ceph df), nearfull when Ceph says an OSD or a pool is
# near full. A managed cluster is checked as client.admin on an admin host, an imported one as its client user, which
# may not see the hosts or the OSDs. On a managed cluster the mgr prometheus module is switched on when it is off
# (§14.3: the metrics come from it).
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
