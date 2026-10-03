#!/bin/bash
# Bring the NSDs of a GPFS cluster back up, run on an admin host by clapi every minute (shared-storage-design.md §7.1):
# in the shared-nothing layout the disks of an NSD host that was down stay down when it comes back, and with two
# copies a second host going down would then take data away. Once every node is active again, the disks that are down
# are started (at most once every 10 minutes). <cluster UUID>, {"filesystems": [name]} on stdin.
# The work runs in the background and writes only to its log: nothing here is a callback.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./backends/gpfs.sh

uuid=$1
input=$(cat)
valid_uuid "$uuid" || exit 1
[ -x $gpfs_bin/mmlsdisk ] || exit 0

function disks_up()
{
    local stamp=$run_dir/storage/$uuid/disks_up fs down states
    [ -f $stamp ] && [ $(( $(date +%s) - $(stat -c %Y $stamp) )) -lt 600 ] && return 0
    for fs in $(jq -r '.filesystems[]?' <<<"$input"); do
        [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || continue
        # mmlsdisk takes -e or -Y, not both: count the disks whose availability says they need a start (recovering
        # is a start under way, a suspended disk is an admin's choice and keeps its availability)
        down=$(timeout 60 $gpfs_bin/mmlsdisk $fs -Y 2>/dev/null | gpfs_y "" availability | grep -cx -E 'down|unrecovered')
        [ "${down:-0}" -gt 0 ] || continue
        states=$(timeout 60 $gpfs_bin/mmgetstate -a -Y 2>/dev/null | gpfs_y "" state)
        if [ -z "$states" ] || grep -vqx active <<<"$states"; then
            echo "$(date -u +%FT%TZ) $fs: $down disks not up, but not every node is active yet"
            continue
        fi
        mkdir -p $(dirname $stamp) && touch $stamp
        echo "$(date -u +%FT%TZ) $fs: starting $down disks"
        $gpfs_bin/mmchdisk $fs start -a
    done
}

mkdir -p $log_dir/storage
exec 9>/var/lock/cloudland-gpfs-disks-up-$uuid.lock
flock -n 9 || exit 0
# Detached from the command stream: its output goes to the log only, and the lock goes with it
disks_up </dev/null >>$log_dir/storage/gpfs-disks-up.log 2>&1 &
exit 0
