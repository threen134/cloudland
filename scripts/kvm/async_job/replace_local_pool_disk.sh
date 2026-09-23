#!/bin/bash
# Replace a failed member disk of a RAID1 pool (§4.9 of the local storage plan): the new disk joins the array and
# the array rebuilds in the background; the pool reports degraded with the progress until it is done.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 6 ] && die "$0 <pool_uuid> <hostid> <array> <failed_disk_id> <new_disk_id> <wipe:0|1>"

pool=$1
hostid=$2
array=$3
failed_id=$4
new_id=$5
wipe=$6
root=$pools_dir/$pool

function fail()
{
    pool_status_callback replace $pool error "$1" ""
    exit 0
}

valid_uuid "$pool" || { pool_status_callback replace "$pool" error "invalid pool uuid" ""; exit 0; }
[[ "$hostid" =~ ^[0-9]+$ ]] && [ "$hostid" = "$NODE_ID" ] || fail "host id $hostid does not match this node ($NODE_ID)"
[[ "$array" =~ ^cl_${pool:0:8}_[0-9]+$ ]] || fail "array $array does not belong to pool $pool"
pool_lock $pool exclusive 60 || fail "another operation on the pool is running"
disks_lock 60 || fail "another storage operation is running"
vg=$(pool_vg $pool)
[ -n "$vg" ] || fail "volume group of pool $pool not found"
md=/dev/md/$array
[ -e "$md" ] || fail "array $array is not assembled"

system_disk_list=$(system_disks)
local_pool_list=$(local_pools)
new_dev=$(disk_path_of "$new_id")
[ -b "$new_dev" ] || fail "disk $new_id not found"
classify_disk $new_dev
case $disk_state in
    free) ;;
    dirty)
        [ "$wipe" = "1" ] || fail "disk $new_id has data on it and wipe was not asked for"
        wipe_disk $new_dev || fail "failed to wipe $new_dev"
        ;;
    *)
        fail "disk $new_id can not be used: $disk_state ($disk_detail)"
        ;;
esac
# The array needs a member at least as large as the others
need=$(timeout 20 mdadm --detail $md 2>/dev/null | sed -n 's/^ *Used Dev Size : \([0-9]*\).*/\1/p')
have=$(( $(lsblk -dbno SIZE $new_dev) / 1024 ))
[ -n "$need" ] && [ "$have" -lt "$need" ] && fail "disk $new_id is smaller than the members of $array"

# The failed disk may be gone already (kicked out by the kernel, or pulled): remove it only when it is still listed
failed_dev=$(disk_path_of "$failed_id")
if [ -b "$failed_dev" ]; then
    member=$(timeout 20 mdadm --detail $md 2>/dev/null | awk -v d="$(readlink -f $failed_dev)" '$NF == d {print $NF}')
    if [ -n "$member" ]; then
        mdadm --manage $md --fail $member >/dev/null 2>&1
        mdadm --manage $md --remove $member >/dev/null 2>&1 || fail "failed to remove $failed_id from $array"
    fi
fi
mdadm --manage $md --remove detached >/dev/null 2>&1
mdadm --manage $md --add $new_dev >/dev/null 2>&1 || fail "failed to add $new_id to $array"
# Keep mdadm.conf in step so the array assembles with its new member at boot
mdadm_conf_add $md

vg_raid_health $vg
details=$(jq -cn --arg vg "$vg" --argjson devices "$(vg_devices_json $vg)" \
    --argjson df "{$(pool_df_json $root)}" --arg sync "$raid_sync" \
    '{layout: "raid1", vg: $vg, devices: $devices, size: $df.size, used: $df.used, avail: $df.avail, sync: ($sync | tonumber)}')
pool_status_callback replace $pool $raid_state "" "$details"
