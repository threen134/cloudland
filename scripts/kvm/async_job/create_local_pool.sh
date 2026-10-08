#!/bin/bash
# Build a local storage pool from whole disks (§4.3 of the local storage plan)

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 7 ] && die "$0 <pool_uuid> <layout> <vg_name> <hostid> <wipe:0|1> <destroy_pools:csv|-> <disk_id>..."

pool=$1
layout=$2
vg=$3
hostid=$4
wipe=$5
destroy=$6
shift 6
disk_ids="$@"
root=$pools_dir/$pool
created_md=()
fstab_added=0
vg_created=0

function rollback()
{
    # The fstab line goes away even when the pool was never mounted (that is the case that brought us here):
    # leaving it behind makes the pool look set up on this host for good and blocks every later attempt
    if [ $fstab_added -eq 1 ]; then
        umount $root 2>/dev/null
        sed -i "\# $root #d" /etc/fstab
        systemctl daemon-reload >/dev/null 2>&1
    fi
    [ $vg_created -eq 1 ] && vgremove -f $vg >/dev/null 2>&1
    local md m
    for md in "${created_md[@]}"; do
        local members=$(mdadm --detail $md 2>/dev/null | awk '$NF ~ /^\/dev\// {print $NF}')
        local uuid=$(md_uuid $md)
        mdadm --stop $md >/dev/null 2>&1
        for m in $members; do
            mdadm --zero-superblock $m >/dev/null 2>&1
        done
        mdadm_conf_del $uuid
    done
    rmdir $root 2>/dev/null
}

function fail()
{
    rollback
    pool_status_callback create $pool error "$1" ""
    exit 0
}

valid_uuid "$pool" || { pool_status_callback create "$pool" error "invalid pool uuid" ""; exit 0; }
[[ "$vg" =~ ^cl_[0-9a-f]{8}_[0-9a-f]{4}$ ]] || fail "invalid volume group name $vg"
[[ "$hostid" =~ ^[0-9]+$ ]] && [ "$hostid" = "$NODE_ID" ] || fail "host id $hostid does not match this node ($NODE_ID)"
pool_lock $pool exclusive 60 || fail "another operation on the pool is running"
disks_lock 60 || fail "another storage operation is running"

if grep -q " $root " /etc/fstab || timeout 10 mountpoint -q $root; then
    fail "pool $pool already exists on this host"
fi
if timeout 20 vgs $vg >/dev/null 2>&1; then
    fail "volume group $vg already exists"
fi

system_disk_list=$(system_disks)
local_pool_list=$(local_pools)
devs=()
states=()
for id in $disk_ids; do
    dev=$(disk_path_of "$id")
    [ -b "$dev" ] || fail "disk $id not found"
    classify_disk $dev
    case $disk_state in
        free) ;;
        dirty)
            [ "$wipe" = "1" ] || fail "disk $id has data on it and wipe was not asked for"
            ;;
        cloudland_pool)
            allowed=0
            for d in ${destroy//,/ }; do
                [ "$d" != "-" ] && [ -n "$disk_pool" ] && [[ "$d" == "$disk_pool"* ]] && allowed=1
            done
            [ $allowed -eq 1 ] || fail "disk $id holds CloudLand pool $disk_pool; it can only be adopted"
            ;;
        *)
            fail "disk $id can not be used: $disk_state ($disk_detail)"
            ;;
    esac
    devs+=($dev)
    states+=($disk_state)
done

n=${#devs[@]}
case $layout in
    single) [ $n -eq 1 ] || fail "layout single takes exactly one disk" ;;
    linear) [ $n -ge 1 ] || fail "layout linear takes at least one disk" ;;
    raid1) [ $n -ge 2 ] && [ $((n % 2)) -eq 0 ] || fail "layout raid1 takes an even number of disks" ;;
    *) fail "unknown layout $layout" ;;
esac

for i in "${!devs[@]}"; do
    if [ "${states[$i]}" != "free" ]; then
        wipe_disk ${devs[$i]} || fail "failed to wipe ${devs[$i]}"
    fi
done

pvs_list=()
if [ "$layout" = "raid1" ]; then
    k=0
    for ((i = 0; i < n; i += 2)); do
        name=cl_${pool:0:8}_$k
        md=/dev/md/$name
        # No --assume-clean: the array is usable right away and syncs in the background
        mdadm --create $md --level=1 --raid-devices=2 --metadata=1.2 --name=$name --homehost=any --run --quiet \
            ${devs[$i]} ${devs[$((i + 1))]} >/dev/null 2>&1 || fail "failed to create RAID1 array from ${devs[$i]} and ${devs[$((i + 1))]}"
        created_md+=($md)
        mkdir -p /etc/mdadm
        mdadm_conf_add $md
        pvs_list+=($md)
        k=$((k + 1))
    done
    udevadm settle 2>/dev/null
else
    pvs_list=("${devs[@]}")
fi

pvcreate --yes -q "${pvs_list[@]}" >/dev/null 2>&1 || fail "pvcreate failed"
vgcreate --yes -q --addtag cloudland_pool=$pool --addtag cloudland_host=$hostid $vg "${pvs_list[@]}" >/dev/null 2>&1 || fail "vgcreate failed"
vg_created=1
lvcreate --yes -q -l 100%FREE -n data $vg >/dev/null 2>&1 || fail "lvcreate failed"
# -K: do not discard the whole device while making the file system
mkfs.xfs -f -q -K /dev/$vg/data >/dev/null 2>&1 || fail "mkfs.xfs failed"
fsuuid=$(blkid -s UUID -o value /dev/$vg/data)
[ -n "$fsuuid" ] || fail "file system uuid not found"
mkdir -p $root
# nofail keeps the host booting without the pool; the device timeout is longer than mdadm needs to start a degraded array
echo "UUID=$fsuuid $root xfs defaults,nofail,x-systemd.device-timeout=120s 0 2" >>/etc/fstab
fstab_added=1
systemctl daemon-reload >/dev/null 2>&1
mount $root || fail "failed to mount $root"
mkdir -p $root/volumes $root/nvram $root/tmp
write_pool_marker $root $pool $hostid || fail "failed to write the pool marker"
chown cland:cland $root $root/volumes $root/nvram $root/tmp 2>/dev/null

status=ready
sync=100
if [ "$layout" = "raid1" ]; then
    vg_raid_health $vg
    status=$raid_state
    sync=$raid_sync
fi
details=$(jq -cn --arg layout "$layout" --arg vg "$vg" --argjson devices "$(vg_devices_json $vg)" \
    --argjson df "{$(pool_df_json $root)}" --arg sync "$sync" \
    '{layout: $layout, vg: $vg, devices: $devices, size: $df.size, used: $df.used, avail: $df.avail, sync: ($sync | tonumber)}')
pool_status_callback create $pool $status "" "$details"
