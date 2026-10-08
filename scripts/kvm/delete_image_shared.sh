#!/bin/bash
# Remove the copy of an image from a shared pool (shared-storage-design.md §9.6): clapi sends it once the image is
# deleted and no boot disk is made from the copy any more. Only that copy is removed, never by pattern (§12.3).

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 1 ] && die "$0 <image_storage_ID>"

id=$1
args=$(cat)
[[ "$id" =~ ^[0-9]+$ ]] || die "invalid image copy id $id"

function fail()
{
    echo "|:-COMMAND-:| image_storage_status '$id' 'delete_failed' '${1//\'/}'"
    exit 0
}

drv_load "$args" || fail "$guard_error"
base=$(jq -r '.base // empty' <<<"$args")
drv_base_check "$base" || fail "$guard_error"
drv_guard || fail "$guard_error"
drv_base_delete "$base" || fail "$guard_error"
echo "|:-COMMAND-:| image_storage_status '$id' 'deleted' '-'"
