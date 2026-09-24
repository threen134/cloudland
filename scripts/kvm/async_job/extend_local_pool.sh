#!/bin/bash
# Add disks to a local storage pool while it is mounted and in use (§4.4 of the local storage plan).
# A linear or single pool takes any number of disks, a RAID1 pool takes them in pairs. The file system grows online.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 4 ] && die "$0 <pool_uuid> <hostid> <wipe:0|1> <disk_id>..."

pool=$1
hostid=$2
wipe=$3
shift 3
disk_ids="$@"
root=$pools_dir/$pool
created_md=()
added_pvs=()

function rollback()
{
    local pv md m
    for pv in "${added_pvs[@]}"; do
        vgreduce $vg $pv >/dev/null 2>&1
        pvremove -y -q $pv >/dev/null 2>&1
    done
    for md in "${created_md[@]}"; do
        local members=$(mdadm --detail $md 2>/dev/null | awk '$NF ~ /^\/dev\// {print $NF}')
        local uuid=$(md_uuid $md)
        mdadm --stop $md >/dev/null 2>&1
        for m in $members; do
            mdadm --zero-superblock $m >/dev/null 2>&1
        done
        mdadm_conf_del $uuid
    done
}

function fail()
{
    rollback
    pool_status_callback extend $pool error "$1" ""
    exit 0
}

valid_uuid "$pool" || { pool_status_callback extend "$pool" error "invalid pool uuid" ""; exit 0; }
[[ "$hostid" =~ ^[0-9]+$ ]] && [ "$hostid" = "$NODE_ID" ] || fail "host id $hostid does not match this node ($NODE_ID)"
pool_lock $pool exclusive 60 || fail "another operation on the pool is running"
disks_lock 60 || fail "another storage operation is running"
pool_guard $root $pool $hostid || fail "$guard_error"
vg=$(pool_vg $pool)
[ -n "$vg" ] || fail "volume group of pool $pool not found"
layout=$(vg_layout $vg)

system_disk_list=$(system_disks)
local_pool_list=$(local_pools)
devs=()
for id in $disk_ids; do
    dev=$(disk_path_of "$id")
    [ -b "$dev" ] || fail "disk $id not found"
    classify_disk $dev
    case $disk_state in
        free) ;;
        dirty)
            [ "$wipe" = "1" ] || fail "disk $id has data on it and wipe was not asked for"
            wipe_disk $dev || fail "failed to wipe $dev"
            ;;
        *)
            fail "disk $id can not be used: $disk_state ($disk_detail)"
            ;;
    esac
    devs+=($dev)
done
n=${#devs[@]}
[ $n -ge 1 ] || fail "no disk given"

pvs_list=()
if [ "$layout" = "raid1" ]; then
    [ $((n % 2)) -eq 0 ] || fail "a RAID1 pool takes disks in pairs"
    # New arrays are numbered after the existing ones of the pool
    k=$(timeout 20 pvs --noheadings -o pv_name --select vg_name=$vg 2>/dev/null | wc -l)
    for ((i = 0; i < n; i += 2)); do
        name=cl_${pool:0:8}_$k
        while [ -e /dev/md/$name ]; do
            k=$((k + 1))
            name=cl_${pool:0:8}_$k
        done
        md=/dev/md/$name
        mdadm --create $md --level=1 --raid-devices=2 --metadata=1.2 --name=$name --homehost=any --run --quiet \
            ${devs[$i]} ${devs[$((i + 1))]} >/dev/null 2>&1 || fail "failed to create RAID1 array from ${devs[$i]} and ${devs[$((i + 1))]}"
        created_md+=($md)
        mdadm_conf_add $md
        pvs_list+=($md)
        k=$((k + 1))
    done
    udevadm settle 2>/dev/null
else
    pvs_list=("${devs[@]}")
fi

for pv in "${pvs_list[@]}"; do
    pvcreate --yes -q $pv >/dev/null 2>&1 || fail "pvcreate $pv failed"
    vgextend -q $vg $pv >/dev/null 2>&1 || { pvremove -y -q $pv >/dev/null 2>&1; fail "vgextend with $pv failed"; }
    added_pvs+=($pv)
done
lvextend -q -l +100%FREE /dev/$vg/data >/dev/null 2>&1 || fail "lvextend failed"
# From here on the logical volume uses the new disks: no rollback any more
added_pvs=()
created_md=()
xfs_growfs $root >/dev/null 2>&1 || { pool_status_callback extend $pool error "the pool grew but xfs_growfs failed; run it by hand" ""; exit 0; }

status=ready
sync=100
# A single-disk pool becomes linear once it has two disks
layout=$(vg_layout $vg)
if [ "$layout" = "raid1" ]; then
    vg_raid_health $vg
    status=$raid_state
    sync=$raid_sync
fi
details=$(jq -cn --arg layout "$layout" --arg vg "$vg" --argjson devices "$(vg_devices_json $vg)" \
    --argjson df "{$(pool_df_json $root)}" --arg sync "$sync" \
    '{layout: $layout, vg: $vg, devices: $devices, size: $df.size, used: $df.used, avail: $df.avail, sync: ($sync | tonumber)}')
pool_status_callback extend $pool $status "" "$details"
