#!/bin/bash
# Remove the file of a volume of a shared pool (shared-storage-design.md §9.4, §12.3): only the file of that volume,
# never by pattern. The pool and the volume come as JSON on stdin; clapi picked this host among those that reach the
# pool. The boot disk of a deleted instance in a file pool takes its UEFI variables (nvram) with it (§9.8).

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
nvram=$(jq -r '.nvram // empty' <<<"$args")
if [ -n "$nvram" ] && [ -n "$drv_root" ] && [ "$(dirname "$nvram")" = "$drv_root/nvram" ] && [[ "$(basename "$nvram")" =~ ^inst-[0-9]+_VARS\.fd$ ]]; then
    rm -f "$nvram"
fi
echo "|:-COMMAND-:| clear_volume '$vol_ID' 'deleted' '-'"
