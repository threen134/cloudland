#!/bin/bash
# Start an instance of the pending start list once its pools are usable again (§4.8 of the local storage plan)

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 1 ] && die "$0 <vm_ID>"

ID=$1
exec 9>$run_dir/start_pending-$ID.lock
flock -n 9 || exit 0
grep -qx "$ID" $cache_dir/pending_start 2>/dev/null || exit 0
if try_start_instance $ID; then
    pending_start_remove $ID
    echo "|:-COMMAND-:| launch_vm.sh '$ID' 'running' '$NODE_ID' 'sync'"
fi
