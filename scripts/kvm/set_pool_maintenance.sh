#!/bin/bash
# Put a pool of this host in or out of maintenance (§5.11 of the local storage plan): while the flag is there the
# probe does not mount the pool again and reports "maintenance", so an admin can unmount it by hand.

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 2 ] && die "$0 <pool_uuid> <on|off>"

pool=$1
valid_uuid "$pool" || die "invalid pool $pool"
mkdir -p $pool_state_dir
if [ "$2" = "on" ]; then
    touch $pool_state_dir/$pool.maint
else
    rm -f $pool_state_dir/$pool.maint $pool_state_dir/$pool.mount
fi
# Report in the next heartbeat instead of waiting up to 5 minutes
rm -f $pool_state_dir/$pool.state $pool_state_dir/$pool.reported
exit 0
