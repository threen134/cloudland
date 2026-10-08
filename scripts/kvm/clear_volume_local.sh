#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 5 ] && die "$0 <vol_ID> <vol_UUID> <volume_path> <pool_uuid|builtin> <hostid>"

vol_ID=$1
vol_UUID=$2
vol_path=$3
pool=$4
hostid=$5

if ! pool_enter "$pool" "$hostid" "$vol_path"; then
    echo "|:-COMMAND-:| clear_volume '$vol_ID' 'error' '${guard_error//\'/}'"
    exit 0
fi
if ! rm -f "$vol_path"; then
    echo "|:-COMMAND-:| clear_volume '$vol_ID' 'error' 'failed to delete $vol_path'"
    exit 0
fi
echo "|:-COMMAND-:| clear_volume '$vol_ID' 'deleted' '-'"
