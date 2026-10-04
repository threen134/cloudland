# -*- mode: sh -*-
# Shared helpers of the local storage pool scripts. Source it after ../cloudrc.
#
# Nothing here may print to stdout: every stdout line of a node script is sent to clapi as a callback.

# vnc_ensure_domain_passwd for try_start_instance (the scripts source this file from different directories)
source "$(dirname "${BASH_SOURCE[0]}")/vnc_lib.sh"

pools_dir=/opt/cloudland/pools
pool_state_dir=$run_dir/pools
# Partition type of a GPFS NSD of the newer format, and how much of the head of a disk tells it is blank
gpfs_part_type=37affc90-ef7d-4e96-91c3-2d7ae055b174
disk_head_bytes=4194304
pool_lock_dir=/var/lock
disks_lock=$pool_lock_dir/cloudland-disks.lock

function valid_uuid()
{
    [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
}

# Root directory of a pool: <pool uuid|builtin>
function pool_root()
{
    if [ "$1" = "builtin" ]; then
        echo $cache_dir
        return 0
    fi
    valid_uuid "$1" || return 1
    echo $pools_dir/$1
}

# Take the lock of a pool on fd 8 until the script exits: <pool uuid|builtin> <shared|exclusive> [wait seconds]
# A structural change (create, extend, remove, adopt) takes it exclusive, anything writing into the pool shared.
# Every wait is bounded so one stuck operation can not block the serial command queue of cloudlet.
function pool_lock()
{
    local pool=$1 mode=$2 wait=${3:-10}
    exec 8>$pool_lock_dir/cloudland-pool-$pool.lock
    if [ "$mode" = "exclusive" ]; then
        flock -w $wait -x 8
    else
        flock -w $wait -s 8
    fi
}

# Take the lock guarding which disk belongs to which pool on fd 7: [wait seconds]
function disks_lock()
{
    exec 7>$disks_lock
    flock -w ${1:-60} -x 7
}

# Check a pool is really mounted and belongs to this pool on this host: <root> <pool uuid> <expected hostid>
# The expected hostid is passed by the caller, so the check also works over ssh on another host.
# Sets guard_error on failure.
function pool_guard()
{
    local root=$1 uuid=$2 hostid=$3 marker
    guard_error=""
    if ! timeout 10 mountpoint -q "$root"; then
        guard_error="pool $uuid is not mounted at $root"
        return 1
    fi
    if [ "$(timeout 10 stat -f -c %T "$root" 2>/dev/null)" != "xfs" ]; then
        guard_error="pool $uuid at $root is not an xfs file system"
        return 1
    fi
    marker=$(timeout 10 cat "$root/.cloudland-pool" 2>/dev/null)
    if ! grep -qx "driver=local" <<<"$marker" || ! grep -qx "pool_uuid=$uuid" <<<"$marker" || ! grep -qx "hostid=$hostid" <<<"$marker"; then
        guard_error="marker of $root does not match pool $uuid on host $hostid"
        return 1
    fi
    return 0
}

# Lock a pool shared and check it before touching a file in it: <pool uuid|builtin> <hostid> <file path>
# The file must be inside the pool root. Sets guard_error on failure.
function pool_enter()
{
    local pool=$1 hostid=$2 file=$3 root real_root real_file
    guard_error=""
    root=$(pool_root "$pool") || { guard_error="invalid pool $pool"; return 1; }
    # Compare the normalized paths: this is the only guard between a path clapi sends and rm running as root, so
    # it must not be fooled by ".." anywhere in the path (glob patterns miss a trailing "/..")
    if [ -n "$file" ]; then
        real_root=$(realpath -m "$root" 2>/dev/null)
        real_file=$(realpath -m "$file" 2>/dev/null)
        if [ -z "$real_root" ] || [ -z "$real_file" ] || [[ "$real_file" != "$real_root"/* ]]; then
            guard_error="$file is not inside pool $pool"
            return 1
        fi
    fi
    if ! pool_lock "$pool" shared 30; then
        guard_error="pool $pool is busy"
        return 1
    fi
    [ "$pool" = "builtin" ] && return 0
    pool_guard "$root" "$pool" "$hostid"
}

# mdadm.conf entries are keyed by the array UUID: `mdadm --detail --brief` writes no name= field, and an array
# assembled from a temporary config comes up as /dev/mdNNN. The entry names the array after its superblock so it
# assembles as /dev/md/cl_<pool>_<n> at boot.
function md_uuid()
{
    timeout 20 mdadm --detail $1 2>/dev/null | sed -n 's/^ *UUID : \([^ ]*\).*/\1/p' | head -1
}

# Drop the entry of an array: <array uuid>
function mdadm_conf_del()
{
    [ -n "$1" ] && sed -i "/UUID=$1\( \|\$\)/d" /etc/mdadm/mdadm.conf 2>/dev/null
    return 0
}

# Write the entry of a running array: <md device>
function mdadm_conf_add()
{
    local md=$1 uuid name
    uuid=$(md_uuid $md)
    name=$(timeout 20 mdadm --detail $md 2>/dev/null | sed -n 's/^ *Name : \([^ ]*\).*/\1/p' | head -1)
    name=${name#*:}
    [ -n "$uuid" ] && [ -n "$name" ] || return 1
    mkdir -p /etc/mdadm
    mdadm_conf_del $uuid
    echo "ARRAY /dev/md/$name metadata=1.2 UUID=$uuid" >>/etc/mdadm/mdadm.conf
}

function write_pool_marker()
{
    local root=$1 uuid=$2 hostid=$3
    printf 'driver=local\npool_uuid=%s\nhostid=%s\n' "$uuid" "$hostid" >$root/.cloudland-pool.tmp && mv -f $root/.cloudland-pool.tmp $root/.cloudland-pool
}

# Pools of this host: uuids of the fstab entries mounted under $pools_dir
function local_pools()
{
    awk -v d="$pools_dir/" '$1 !~ /^#/ && index($2, d) == 1 {print substr($2, length(d) + 1)}' /etc/fstab | while read uuid; do
        valid_uuid "$uuid" && echo $uuid
    done
}

# Stable identifier of a disk: <device path>
# Preference: wwn-* > nvme-eui.* > ata-*/scsi-*/nvme-*; loop devices (test switch only) use their backing file
function disk_stable_id()
{
    local dev=$1 name link target best="" rank=9 r
    name=$(basename $dev)
    if [[ "$name" == loop* ]]; then
        echo "loop:$(losetup -nO BACK-FILE $dev 2>/dev/null | xargs)"
        return
    fi
    for link in /dev/disk/by-id/*; do
        target=$(readlink -f $link)
        [ "$target" = "/dev/$name" ] || continue
        case $(basename $link) in
            wwn-*) r=1 ;;
            nvme-eui.*) r=2 ;;
            ata-*|scsi-*|nvme-*) r=3 ;;
            *) r=5 ;;
        esac
        [ $r -lt $rank ] && rank=$r && best=$(basename $link)
    done
    [ -n "$best" ] && echo $best || echo "dev:$name"
}

# Device path of a stable identifier: <stable id>
function disk_path_of()
{
    local id=$1
    case $id in
        loop:*)
            losetup -j "${id#loop:}" -nO NAME 2>/dev/null | head -1
            ;;
        dev:*)
            echo /dev/${id#dev:}
            ;;
        *)
            [ -e /dev/disk/by-id/$id ] && readlink -f /dev/disk/by-id/$id
            ;;
    esac
}

# Disks holding what the host itself runs on: /, /boot, swap, the CloudLand cache
function system_disks()
{
    local src
    {
        for m in / /boot /boot/efi /opt/cloudland /opt/cloudland/cache /var /var/lib/libvirt; do
            src=$(findmnt -no SOURCE $m 2>/dev/null)
            [ -n "$src" ] && echo $src
        done
        swapon --show=NAME --noheadings 2>/dev/null
    } | sort -u | while read src; do
        [ -b "$src" ] || continue
        lsblk -nso NAME,TYPE $src 2>/dev/null | awk '$2 == "disk" {gsub(/[^a-zA-Z0-9_-]/, "", $1); print $1}'
    done | sort -u
}

# CloudLand ownership written on a disk: <device path>
# Sets tag_pool (pool uuid, or only its first 8 characters when just an md array name is readable),
# tag_host (hostid, empty when unreadable) and lvm_member / raid_member (1 when such a signature is found).
# Reads the labels on the disk itself instead of the LVM or md view of this host: after an OS reinstall
# the LVM devices file does not list the old disks and the arrays are not assembled.
function disk_tags()
{
    local dev=$1 part out name vg tags fstype
    tag_pool=""
    tag_host=""
    lvm_member=0
    raid_member=0
    ceph_osd_id=""
    ceph_fsid=""
    for part in $(lsblk -nlpo NAME $dev 2>/dev/null); do
        fstype=$(blkid -p -o value -s TYPE $part 2>/dev/null)
        case $fstype in
            LVM2_member)
                lvm_member=1
                out=$(timeout 20 pvs --devicesfile "" --devices $part --noheadings -o vg_name,vg_tags 2>/dev/null | head -1)
                parse_vg_tags "$out"
                # A Ceph OSD: volume group ceph-<uuid>, its logical volume tagged with the OSD id and cluster fsid
                if [[ "$(awk '{print $1}' <<<"$out")" == ceph-* ]]; then
                    parse_ceph_tags "$(timeout 20 lvs --devicesfile "" --devices $part --noheadings -o lv_tags 2>/dev/null | head -1)"
                    [ -z "$ceph_osd_id" ] && ceph_osd_id="?"
                fi
                ;;
            linux_raid_member)
                raid_member=1
                name=$(timeout 20 mdadm --examine $part 2>/dev/null | sed -n 's/^ *Name : \([^ ]*\).*/\1/p' | head -1)
                name=${name#*:}
                if [[ "$name" =~ ^cl_([0-9a-f]{8})_[0-9]+$ ]]; then
                    [ -z "$tag_pool" ] && tag_pool=${BASH_REMATCH[1]}
                fi
                ;;
        esac
    done
    # An assembled md array holding a CloudLand volume group: the full tags are readable on the array
    for part in $(lsblk -nlpo NAME,TYPE $dev 2>/dev/null | awk '$2 ~ /^raid/ {print $1}' | sort -u); do
        out=$(timeout 20 pvs --devicesfile "" --devices $part --noheadings -o vg_name,vg_tags 2>/dev/null | head -1)
        parse_vg_tags "$out"
    done
    return 0
}

function parse_ceph_tags()
{
    local t
    for t in ${1//,/ }; do
        case $t in
            ceph.osd_id=*) ceph_osd_id=${t#ceph.osd_id=} ;;
            ceph.cluster_fsid=*) ceph_fsid=${t#ceph.cluster_fsid=} ;;
        esac
    done
}

function parse_vg_tags()
{
    local tags
    tags=$(awk '{print $2}' <<<"$1")
    [ -z "$tags" ] && return
    local t
    for t in ${tags//,/ }; do
        case $t in
            cloudland_pool=*) tag_pool=${t#cloudland_pool=} ;;
            cloudland_host=*) tag_host=${t#cloudland_host=} ;;
        esac
    done
}

# Classify a disk for scanning and for the checks right before it is formatted: <device path>
# Sets disk_state (free|dirty|in_use|system|cloudland_pool|unknown_member|shared), disk_detail, disk_pool, disk_owner.
# system_disk_list and local_pool_list must be set by the caller (system_disks, local_pools).
function classify_disk()
{
    local dev=$1 name tran mounts m holders sig wwn dup mp
    name=$(basename $dev)
    disk_state=""
    disk_detail=""
    disk_pool=""
    disk_owner=0
    if grep -qx "$name" <<<"$system_disk_list"; then
        disk_state=system
        disk_detail="holds the operating system"
        return
    fi
    tran=$(lsblk -dno TRAN $dev 2>/dev/null | xargs)
    wwn=$(lsblk -dno WWN $dev 2>/dev/null | xargs)
    dup=0
    [ -n "$wwn" ] && [ $(lsblk -dno WWN 2>/dev/null | grep -cx "$wwn") -gt 1 ] && dup=1
    holders=$(lsblk -nlo TYPE $dev 2>/dev/null | tail -n +2 | sort -u | xargs)
    if [ "$tran" = "fc" ] || [ "$tran" = "iscsi" ] || [[ " $holders " == *" mpath "* ]] || [ $dup -eq 1 ]; then
        disk_state=shared
        disk_detail="may be a shared LUN (${tran:-multipath})"
        return
    fi
    disk_tags $dev
    mounts=$(lsblk -nlo MOUNTPOINTS $dev 2>/dev/null | grep -v '^$' | xargs)
    if [ -n "$tag_pool" ]; then
        disk_pool=$tag_pool
        disk_owner=${tag_host:-0}
        # A member of a pool this host is running: listed in fstab and tagged with this host
        if [ -n "$tag_host" ] && [ "$tag_host" = "$NODE_ID" ] && grep -qx "$tag_pool" <<<"$local_pool_list"; then
            disk_state=in_use
            disk_detail="member of local pool $tag_pool"
            return
        fi
        disk_state=cloudland_pool
        disk_detail="holds CloudLand pool $tag_pool of host ${tag_host:-unknown}"
        return
    fi
    if [ -n "$mounts" ]; then
        disk_state=in_use
        disk_detail="mounted at $mounts"
        return
    fi
    if [ -n "$ceph_osd_id" ]; then
        disk_state=ceph_osd
        disk_detail="Ceph OSD $ceph_osd_id of cluster ${ceph_fsid:-unknown}"
        return
    fi
    if [ $lvm_member -eq 1 ] || [ $raid_member -eq 1 ]; then
        disk_state=unknown_member
        disk_detail="LVM or md member whose owner can not be read"
        return
    fi
    if [ -n "$(lsblk -nlo TYPE $dev 2>/dev/null | tail -n +2 | grep -vx part)" ]; then
        disk_state=in_use
        disk_detail="used by $holders"
        return
    fi
    # An NSD of the newer GPFS format is a GPT disk with one partition of the GPFS type: not just a disk with a
    # partition table that a wipe may take (shared-storage-design.md §6.4)
    if lsblk -nlo PARTTYPE $dev 2>/dev/null | grep -qix "$gpfs_part_type"; then
        disk_state=unknown_member
        disk_detail="GPFS NSD (a partition of the GPFS type)"
        return
    fi
    sig=$(lsblk -nlpo NAME $dev 2>/dev/null | while read p; do wipefs -n $p 2>/dev/null | tail -n +2; done)
    if [ -n "$sig" ] || [ $(lsblk -nlo NAME $dev 2>/dev/null | wc -l) -gt 1 ]; then
        disk_state=dirty
        disk_detail="has a partition table or file system"
        return
    fi
    # An NSD of the older GPFS format carries no signature blkid knows, only its descriptor at the start of the disk:
    # on a host with GPFS installed a disk with data in its first MiBs may well be an NSD of a cluster CloudLand does
    # not manage (§6.4). A disk zero there is blank (new, or wiped by wipe_disk)
    if [ -x /usr/lpp/mmfs/bin/mmfsd ] && ! timeout 30 cmp -s -n $disk_head_bytes $dev /dev/zero; then
        disk_state=unknown_member
        disk_detail="has data in its first $((disk_head_bytes / 1048576)) MiB and GPFS is installed on this host: it may be a GPFS NSD"
        return
    fi
    disk_state=free
}

# Find a disk by its stable identifier and check it is the disk that was claimed (shared-storage-design.md §6.4):
# <stable id> <serial> <wwn> <size in bytes>. Empty expected values are not checked. Sets identity_path, or
# identity_error and returns 1. Scripts writing a disk call this right before they do, never trusting a device name
# found earlier: names like /dev/sdb move between reboots
function disk_identity()
{
    local id=$1 serial=$2 wwn=$3 size=$4 path have
    identity_path=""
    identity_error=""
    path=$(disk_path_of "$id")
    if [ -z "$path" ] || [ ! -b "$path" ]; then
        identity_error="disk $id is not on this host"
        return 1
    fi
    if [ -n "$serial" ]; then
        have=$(lsblk -dno SERIAL $path 2>/dev/null | xargs)
        if [ "$have" != "$serial" ]; then
            identity_error="disk $id ($path) has serial '$have', expected '$serial'"
            return 1
        fi
    fi
    if [ -n "$wwn" ]; then
        have=$(lsblk -dno WWN $path 2>/dev/null | xargs)
        if [ "${have#0x}" != "${wwn#0x}" ]; then
            identity_error="disk $id ($path) has WWN '$have', expected '$wwn'"
            return 1
        fi
    fi
    if [ -n "$size" ] && [ "$size" != "0" ]; then
        have=$(lsblk -dbno SIZE $path 2>/dev/null | xargs)
        if [ "$have" != "$size" ]; then
            identity_error="disk $id ($path) is $have bytes, expected $size"
            return 1
        fi
    fi
    identity_path=$path
}

# Wipe every partition, then the whole disk, and make the kernel drop the partitions: <device path>
# Wiping only the whole disk leaves the file system signature of the first partition at the start of the
# logical volume, and lvcreate / mkfs refuse to go on
function wipe_disk()
{
    local dev=$1 p mib
    for p in $(lsblk -nlpo NAME,TYPE $dev | awk '$2 == "part" {print $1}' | sort -r); do
        wipefs -a -q $p || return 1
    done
    wipefs -a -q $dev || return 1
    # Zero 10 MiB at both ends too (shared-storage-design.md §6.4): what a partition left at the head (the descriptor
    # of an NSD) would make classify_disk take the disk for a GPFS NSD on a host with GPFS installed
    mib=$(( $(blockdev --getsize64 $dev) / 1048576 ))
    if [ $mib -le 20 ]; then
        dd if=/dev/zero of=$dev bs=1M count=$mib oflag=direct conv=fsync status=none || return 1
    else
        dd if=/dev/zero of=$dev bs=1M count=10 oflag=direct conv=fsync status=none || return 1
        dd if=/dev/zero of=$dev bs=1M count=10 seek=$((mib - 10)) oflag=direct conv=fsync status=none || return 1
    fi
    blockdev --rereadpt $dev 2>/dev/null
    udevadm settle 2>/dev/null
    # BLKRRPART fails on a busy disk and on a loop device without partition scanning: drop what is left by hand
    if [ $(lsblk -nlo NAME $dev | wc -l) -gt 1 ]; then
        partx -d $dev 2>/dev/null
        udevadm settle 2>/dev/null
    fi
    [ $(lsblk -nlo NAME $dev | wc -l) -eq 1 ]
}

# Media of a disk as detected: <device path>
function disk_media()
{
    local dev=$1 tran rota
    tran=$(lsblk -dno TRAN $dev 2>/dev/null | xargs)
    rota=$(lsblk -dno ROTA $dev 2>/dev/null | xargs)
    if [ "$tran" = "nvme" ]; then
        echo nvme
    elif [ "$rota" = "0" ]; then
        echo ssd
    else
        echo hdd
    fi
}

# Volume group of a pool, found by its tag: <pool uuid>
function pool_vg()
{
    timeout 20 vgs --noheadings -o vg_name,vg_tags 2>/dev/null | awk -v t="cloudland_pool=$1" '{n = split($2, a, ","); for (i = 1; i <= n; i++) if (a[i] == t) print $1}' | head -1
}

# JSON array of the disks of a volume group, with the RAID1 array and pair each belongs to: <vg>
function vg_devices_json()
{
    local vg=$1 pv m disk pair=0 items="" array
    for pv in $(timeout 20 pvs --noheadings -o pv_name --select vg_name=$vg 2>/dev/null); do
        if [[ "$(lsblk -dno TYPE $pv 2>/dev/null)" == raid* ]]; then
            array=$(timeout 20 mdadm --detail $pv 2>/dev/null | sed -n 's/^ *Name : \([^ ]*\).*/\1/p' | head -1)
            array=${array#*:}
            for m in $(timeout 20 mdadm --detail $pv 2>/dev/null | awk '$NF ~ /^\/dev\// && ($0 ~ /active|spare|rebuilding|faulty/) {print $NF}'); do
                disk=$(lsblk -nso NAME,TYPE $m 2>/dev/null | awk '$2 == "disk" || $2 == "loop" {print $1}' | tail -1)
                [ -z "$disk" ] && continue
                items="$items$(disk_json /dev/$disk "$array" $pair)"$'\n'
            done
            pair=$((pair + 1))
        else
            disk=$(lsblk -nso NAME,TYPE $pv 2>/dev/null | awk '$2 == "disk" || $2 == "loop" {print $1}' | tail -1)
            [ -z "$disk" ] && disk=$(basename $pv)
            items="$items$(disk_json /dev/$disk "" -1)"$'\n'
        fi
    done
    printf '%s' "$items" | jq -cs '.'
}

function disk_json()
{
    jq -cn --arg id "$(disk_stable_id $1)" --arg name "$(basename $1)" \
        --arg serial "$(lsblk -dno SERIAL $1 2>/dev/null | xargs)" --arg model "$(lsblk -dno MODEL $1 2>/dev/null | xargs)" \
        --arg size "$(lsblk -dbno SIZE $1 2>/dev/null | xargs)" --arg media "$(disk_media $1)" --arg array "$2" --arg pair "$3" \
        '{id: $id, name: $name, serial: $serial, model: $model, size_bytes: ($size | tonumber? // 0), media: $media,
          array: $array, pair: ($pair | tonumber)}'
}

# Layout of a volume group: <vg>
function vg_layout()
{
    local pvs=$(timeout 20 pvs --noheadings -o pv_name --select vg_name=$1 2>/dev/null)
    local n=$(wc -w <<<"$pvs") first=$(awk '{print $1}' <<<"$pvs")
    if [[ "$(lsblk -dno TYPE $first 2>/dev/null)" == raid* ]]; then
        echo raid1
    elif [ "$n" -gt 1 ]; then
        echo linear
    else
        echo single
    fi
}

# Health of the RAID1 arrays of a volume group: sets raid_state (ready|degraded|unavailable) and raid_sync (0-100)
function vg_raid_health()
{
    local pv rc pct
    raid_state=ready
    raid_sync=100
    for pv in $(timeout 20 pvs --noheadings -o pv_name --select vg_name=$1 2>/dev/null); do
        [[ "$(lsblk -dno TYPE $pv 2>/dev/null)" == raid* ]] || continue
        timeout 20 mdadm --detail --test $pv >/dev/null 2>&1
        rc=$?
        if [ $rc -ge 2 ]; then
            raid_state=unavailable
        elif [ $rc -eq 1 ] && [ "$raid_state" = "ready" ]; then
            raid_state=degraded
        fi
        pct=$(grep -A 3 "^$(basename $(readlink -f $pv)) " /proc/mdstat | sed -n 's/.*\(resync\|recovery\) *= *\([0-9]*\)\..*/\2/p' | head -1)
        if [ -n "$pct" ]; then
            [ "$raid_state" = "ready" ] && raid_state=degraded
            [ "$pct" -lt "$raid_sync" ] && raid_sync=$pct
        fi
    done
}

# Report the result of a pool command: <op> <pool uuid|builtin> <status> <reason> <details json>
function pool_status_callback()
{
    local json=$5
    [ -z "$json" ] && json='{}'
    local details=$(echo -n "$json" | base64 -w0)
    echo "|:-COMMAND-:| local_pool_status '$NODE_ID' '$1' '$2' '$3' '${4//\'/}' '$details'"
}

# File system usage of a pool root as JSON fields: <root>
function pool_df_json()
{
    timeout 10 df -B1 --output=size,used,avail "$1" 2>/dev/null | tail -1 | awk '{printf "\"size\":%s,\"used\":%s,\"avail\":%s", $1, $2, $3}'
}

# Remove an instance from the pending start list (§4.8 of the plan): <instance id>
function pending_start_remove()
{
    local list=$cache_dir/pending_start
    [ -f $list ] && sed -i "/^$1\$/d" $list
    rm -f $run_dir/start_attempt-$1 $run_dir/start_failed-$1 $run_dir/start_pending-$1.lock
    return 0
}

# Start an instance of the pending start list once, keeping when it was tried and, when it fails, what libvirt
# said (the heartbeat reports it as start_failed and retries with a back-off): <instance id>
function try_start_instance()
{
    local id=$1 err
    date +%s >$run_dir/start_attempt-$id
    # A cold start (after a host reboot, or once its pools are back): a domain defined without a VNC password
    # gets one now, as with action_vm.sh start
    vnc_ensure_domain_passwd inst-$id
    if err=$(timeout 120 sudo virsh start inst-$id 2>&1 >/dev/null); then
        rm -f $run_dir/start_attempt-$id $run_dir/start_failed-$id
        return 0
    fi
    echo "${err:-timed out}" | head -c 500 >$run_dir/start_failed-$id
    log_debug $id "starting instance $id failed: $(head -c 200 $run_dir/start_failed-$id)"
    return 1
}

# Whether a pid still belongs to the probe of a pool: <pid> <pool uuid|builtin>. The pid file survives a reboot
# and the number may then be some other process, which kill -0 alone would take for the probe and never restart it
function probe_alive()
{
    [ -n "$1" ] && [ -r /proc/$1/cmdline ] && tr '\0' ' ' </proc/$1/cmdline | grep -qF "pool_probe.sh $2 "
}

# ---- shared pools (shared-storage-design.md §4.5.2, §9.2) ----
# A host keeps the pools of each storage cluster it is in at $shared_storage_dir/<cluster uuid>/shared_pools.json,
# written by sync_shared_pools.sh and stc_pools.sh: [{driver, pool, root, fs_type}]. Each pool has a background probe
# (shared_pool_probe.sh) whose state file sits next to those of the local pools ($pool_state_dir/<pool uuid>.state),
# marked as shared by <pool uuid>.shared (holding the cluster uuid).

shared_storage_dir=$run_dir/storage

# drv_load <json>: source the driver named in the JSON of a shared pool (scripts/kvm/storage/drivers/<driver>.sh)
# and read the pool from it (drv_pool, drv_root, drv_fs_type...). Sets guard_error on failure
function drv_load()
{
    local json=$1 driver file
    guard_error=""
    driver=$(jq -r '.driver // empty' <<<"$json" 2>/dev/null)
    if ! [[ "$driver" =~ ^[a-z][a-z0-9_]*$ ]]; then
        guard_error="invalid pool driver '$driver'"
        return 1
    fi
    file=$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/storage/drivers/$driver.sh
    if [ ! -f "$file" ]; then
        guard_error="this host has no pool driver $driver"
        return 1
    fi
    source "$file"
    drv_init "$json"
}

# shared_pools_apply <cluster uuid> <json array>: keep the pool list of a cluster and stop the probes of the pools no
# list holds any more. Sets guard_error on failure
function shared_pools_apply()
{
    local cluster=$1 list=$2 dir
    guard_error=""
    if ! valid_uuid "$cluster"; then
        guard_error="invalid cluster uuid $cluster"
        return 1
    fi
    if ! jq -e 'type == "array" and all(.[]; (.pool | type) == "string" and (.driver | type) == "string")' >/dev/null 2>&1 <<<"$list"; then
        guard_error="invalid pool list"
        return 1
    fi
    dir=$shared_storage_dir/$cluster
    mkdir -p $dir
    printf '%s\n' "$list" >$dir/shared_pools.json.tmp && mv -f $dir/shared_pools.json.tmp $dir/shared_pools.json
    shared_pools_prune
}

# shared_pools_all: the pools of every cluster this host is in, one JSON array, each pool with its cluster
function shared_pools_all()
{
    local f c
    for f in $shared_storage_dir/*/shared_pools.json; do
        [ -f "$f" ] || continue
        c=$(basename "$(dirname "$f")")
        valid_uuid "$c" || continue
        jq -c --arg c "$c" '.[]? | . + {cluster: $c}' "$f" 2>/dev/null
    done | jq -cs '.'
}

# node_textfile_dir: the directory node_exporter reads its textfile metrics from; nothing when there is none
function node_textfile_dir()
{
    local dir
    dir=$(ps -eo args= 2>/dev/null | grep -o -- '--collector.textfile.directory[= ][^ ]*' | head -1 | sed 's/^--collector.textfile.directory[= ]//')
    [ -z "$dir" ] && dir=/var/lib/prometheus/node-exporter
    [ -d "$dir" ] && echo "$dir"
}

# shared_pools_prune: stop the probe of every shared pool no list holds any more and drop its state
function shared_pools_prune()
{
    local keep f pool pid dir
    keep=$(shared_pools_all | jq -r '.[].pool')
    for f in $pool_state_dir/*.shared; do
        [ -f "$f" ] || continue
        pool=$(basename "$f" .shared)
        grep -qx "$pool" <<<"$keep" && continue
        pid=$(cat $pool_state_dir/$pool.pid 2>/dev/null)
        probe_alive "$pid" $pool && kill $pid 2>/dev/null
        rm -f $pool_state_dir/$pool.state $pool_state_dir/$pool.pid $pool_state_dir/$pool.reported "$f"
        dir=$(node_textfile_dir) && rm -f $dir/cloudland_shared_pool_$pool.prom
    done
    return 0
}

# shared_pool_of_path <path> [pools json]: the shared pool whose root holds a file, from the lists of this host
function shared_pool_of_path()
{
    local pools=${2:-$(shared_pools_all)}
    jq -r --arg p "$1" '.[] | (.root // "") as $r | select($r != "" and ($p | startswith($r + "/"))) | .pool' <<<"$pools" 2>/dev/null | head -1
}

# --- Boot disks in shared pools (shared-storage-design.md §9.7, §9.8)

# shared_boot_load <boot_disk json> <volume id> <instance id>: the pool and the boot disk launch_vm.sh, reinstall_vm.sh
# get in the metadata: sets drv_* and drv_vol, sb_base (the copy of the image to make it from), sb_copy_id, sb_mode
# and sb_nvram (empty when the UEFI variables stay on the host). Sets guard_error on failure
function shared_boot_load()
{
    local json=$1 vol=$2 inst=$3
    drv_load "$json" || return 1
    drv_volume "$json" "$vol" || return 1
    sb_base=$(jq -r '.image_base // empty' <<<"$json" 2>/dev/null)
    sb_copy_id=$(jq -r '.image_storage_id // empty' <<<"$json" 2>/dev/null)
    sb_mode=$(jq -r '.clone_mode // empty' <<<"$json" 2>/dev/null)
    sb_nvram=$(jq -r '.nvram // empty' <<<"$json" 2>/dev/null)
    if ! [[ "$sb_copy_id" =~ ^[1-9][0-9]*$ ]]; then
        guard_error="invalid image copy id '$sb_copy_id'"
        return 1
    fi
    drv_base_check "$sb_base" || return 1
    drv_nvram_check "$sb_nvram" "$inst" || return 1
    drv_guard
}

# shared_boot_make <target> <size GB>: make a boot disk from the copy of its image (shared_boot_load), check the image
# fits and grow it to the size of the flavor. Sets drv_cloned, or guard_error on failure; a disk this call made is
# removed again
function shared_boot_make()
{
    local target=$1 gb=$2 vsize
    sb_missing=0
    if ! drv_base_exists "$sb_base"; then
        # Gone from the pool (removed out of band, a restore): clapi imports it again for the next boot disk
        drv_base_missing "$sb_base" && sb_missing=1
        guard_error="the copy $sb_base of the image is not in pool $drv_pool"
        return 1
    fi
    drv_clone "$sb_base" "$target" "$sb_mode" || return 1
    vsize=$(drv_size "$target")
    if ! [[ "$vsize" =~ ^[0-9]+$ ]]; then
        drv_drop "$target"
        guard_error="failed to read the size of $target"
        return 1
    fi
    if [ "$vsize" -gt $((gb * 1024 * 1024 * 1024)) ]; then
        drv_drop "$target"
        guard_error="flavor is smaller than image size"
        return 1
    fi
    if [ "$vsize" -lt $((gb * 1024 * 1024 * 1024)) ] && ! drv_resize "$target" "$gb"; then
        drv_drop "$target"
        guard_error="failed to resize $target to ${gb}G"
        return 1
    fi
    return 0
}

# shared_boot_report <volume id> <attached|error> [reason]: the report of a boot disk launch_vm.sh or reinstall_vm.sh
# made in a shared pool: attached with the copy it was cloned from (0 for a full copy); nocopy with the copy when the
# copy is surely not in the pool (shared_boot_make), otherwise error
function shared_boot_report()
{
    local state=$2 copy=0 reason=${3:--}
    if [ "$state" = "attached" ]; then
        [ "$drv_cloned" = "1" ] && copy=$sb_copy_id
    elif [ "$sb_missing" = "1" ]; then
        state=nocopy
        copy=$sb_copy_id
    fi
    echo "|:-COMMAND-:| create_boot_shared '$1' '$state' '$copy' '${reason//\'/}'"
}

# drv_dev_of <domain> <volume>: the device of a domain whose disk is this volume of the pool (drv_load), found by the
# source element its driver writes (drv_disk_xml), whatever the driver
function drv_dev_of()
{
    local pred
    pred=$(drv_disk_xml "$2" vdz | xmllint --xpath '//source/@*' - 2>/dev/null | tr '\n' ' ' |
        sed -E "s/[[:space:]]*([a-z_]+)=\"([^\"]*)\"/ and source\/@\1='\2'/g; s/^ and //")
    [ -n "$pred" ] || return 1
    virsh dumpxml "$1" 2>/dev/null | xmllint --xpath "string(//devices/disk[$pred]/target/@dev)" - 2>/dev/null
}

# xml_replace_disk <domain xml file> <device> <disk xml>: put a disk element in place of the one of a device, keeping
# its PCI address; the rest of the definition is left as it is, byte for byte
function xml_replace_disk()
{
    python3 - "$1" "$2" "$3" <<'PYEOF'
import re, sys
path, dev, new = sys.argv[1], sys.argv[2], sys.argv[3].strip()
xml = open(path).read()
for m in re.finditer(r"<disk\b[^>]*>.*?</disk>", xml, re.S):
    if re.search(r"<target\s+dev=['\"]%s['\"]" % re.escape(dev), m.group(0)):
        addr = re.search(r"<address\b[^>]*/>", m.group(0))
        if addr:
            new = new.replace("</disk>", "   " + addr.group(0) + "\n</disk>")
        open(path, "w").write(xml[:m.start()] + new + xml[m.end():])
        sys.exit(0)
sys.exit(1)
PYEOF
}

# nvram_undefine_flag <nvram path>: how a domain is undefined without removing UEFI variables another host still
# uses: those of a shared pool are kept, the local ones (built-in cache, local pools) go with the domain
function nvram_undefine_flag()
{
    case "$1" in
        "") echo "--nvram" ;;
        $image_dir/* | $cache_dir/* | $pools_dir/*) echo "--nvram" ;;
        *) echo "--keep-nvram" ;;
    esac
}
