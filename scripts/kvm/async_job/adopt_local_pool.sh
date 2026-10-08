#!/bin/bash
# Take over a local storage pool whose disks were set up by an earlier registration of a host (§5.9 of the plan)

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 2 ] && die "$0 <pool_uuid> <new_hostid>"

pool=$1
hostid=$2
root=$pools_dir/$pool

function fail()
{
    pool_status_callback adopt $pool error "$1" ""
    exit 0
}

valid_uuid "$pool" || { pool_status_callback adopt "$pool" error "invalid pool uuid" ""; exit 0; }
[ "$hostid" = "$NODE_ID" ] || fail "host id $hostid does not match this node ($NODE_ID)"
pool_lock $pool exclusive 60 || fail "another operation on the pool is running"
disks_lock 60 || fail "another storage operation is running"
grep -q " $root " /etc/fstab && fail "pool $pool is already set up on this host"

# Assemble the RAID1 arrays of the pool; their names carry the first 8 characters of the pool uuid.
# The scan prints them as "ARRAY /dev/md/cl_<id>_N" (some versions add name=<host>:cl_<id>_N)
arrays=$(mdadm --examine --scan 2>/dev/null | grep -E "(name=([^ ]*:)?|/dev/md/)cl_${pool:0:8}_[0-9]+( |$)")
if [ -n "$arrays" ]; then
    conf=$(mktemp)
    { echo "DEVICE partitions"; echo "$arrays"; } >$conf
    mdadm --assemble --scan --config=$conf --run >/dev/null 2>&1
    rm -f $conf
    udevadm settle 2>/dev/null
fi

# After an OS reinstall the LVM devices file (when in use) does not list the old disks
vg=$(timeout 20 vgs --devicesfile "" --noheadings -o vg_name,vg_tags 2>/dev/null | awk -v t="cloudland_pool=$pool" '{n = split($2, a, ","); for (i = 1; i <= n; i++) if (a[i] == t) print $1}' | head -1)
[ -n "$vg" ] || fail "volume group of pool $pool not found"
if [ "$(lvmconfig --typeconfig full devices/use_devicesfile 2>/dev/null | cut -d= -f2)" = "1" ]; then
    vgimportdevices $vg >/dev/null 2>&1
fi
old_host=$(timeout 20 vgs --noheadings -o vg_tags $vg 2>/dev/null | tr ',' '\n' | sed -n 's/^ *cloudland_host=//p' | head -1)
vgchange -ay $vg >/dev/null 2>&1 || fail "failed to activate volume group $vg"
[ -n "$old_host" ] && vgchange --deltag cloudland_host=$old_host $vg >/dev/null 2>&1
vgchange --addtag cloudland_host=$hostid $vg >/dev/null 2>&1

fsuuid=$(blkid -s UUID -o value /dev/$vg/data)
[ -n "$fsuuid" ] || fail "no file system on /dev/$vg/data"
mkdir -p $root
echo "UUID=$fsuuid $root xfs defaults,nofail,x-systemd.device-timeout=120s 0 2" >>/etc/fstab
systemctl daemon-reload >/dev/null 2>&1
if ! mount $root; then
    sed -i "\# $root #d" /etc/fstab
    fail "failed to mount $root"
fi
marker_old=$(sed -n 's/^hostid=//p' $root/.cloudland-pool 2>/dev/null)
[ -z "$old_host" ] && old_host=$marker_old
write_pool_marker $root $pool $hostid || fail "failed to rewrite the pool marker"
mkdir -p $root/volumes $root/nvram $root/tmp
for pv in $(timeout 20 pvs --noheadings -o pv_name --select vg_name=$vg 2>/dev/null); do
    if [[ "$(lsblk -dno TYPE $pv 2>/dev/null)" == raid* ]]; then
        mdadm_conf_add $pv
    fi
done

status=ready
sync=100
layout=$(vg_layout $vg)
if [ "$layout" = "raid1" ]; then
    vg_raid_health $vg
    status=$raid_state
    sync=$raid_sync
fi
files=$(ls $root/volumes 2>/dev/null)
details=$(jq -cn --arg layout "$layout" --arg vg "$vg" --argjson devices "$(vg_devices_json $vg)" \
    --argjson df "{$(pool_df_json $root)}" --arg sync "$sync" --arg old "${old_host:-0}" --arg files "$files" \
    '{layout: $layout, vg: $vg, devices: $devices, size: $df.size, used: $df.used, avail: $df.avail,
      sync: ($sync | tonumber), old_hostid: ($old | tonumber? // 0), files: ($files | split("\n") | map(select(. != "")))}')
pool_status_callback adopt $pool $status "" "$details"
