#!/bin/bash
# Remove the file of a volume of a shared pool (shared-storage-design.md §9.4, §12.3): only the file of that volume,
# never by pattern. The pool and the volume come as JSON on stdin; clapi picked this host among those that reach the
# pool.

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 2 ] && die "$0 <volume_ID> <volume_UUID>"

vol_ID=$1
args=$(cat)
[[ "$vol_ID" =~ ^[0-9]+$ ]] || die "invalid volume id $vol_ID"

function fail()
{
    echo "|:-COMMAND-:| clear_volume '$vol_ID' 'error' '${1//\'/}'"
    exit 0
}

drv_load "$args" || fail "$guard_error"
drv_volume "$args" "$vol_ID" || fail "$guard_error"
drv_guard || fail "$guard_error"
users=$(drv_users "$drv_vol")
[ -n "$users" ] && fail "the volume is still open by $users"
drv_delete "$drv_vol" || fail "$guard_error"
echo "|:-COMMAND-:| clear_volume '$vol_ID' 'deleted' '-'"
