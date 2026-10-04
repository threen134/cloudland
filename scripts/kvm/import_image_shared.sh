#!/bin/bash
# Import the copy of an image into a shared pool (shared-storage-design.md §9.6): checked here, written by a background
# job (async_job/import_image_shared.sh) so the serial command queue of this host is not held while a large image is
# copied. The pool and the copy come as JSON on stdin; clapi picked this host among those that reach the pool and
# sends one import of a copy at a time.

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 1 ] && die "$0 <image_storage_ID>"

id=$1
args=$(cat)
[[ "$id" =~ ^[0-9]+$ ]] || die "invalid image copy id $id"

function fail()
{
    echo "|:-COMMAND-:| image_storage_status '$id' 'error' '${1//\'/}'"
    exit 0
}

drv_load "$args" || fail "$guard_error"
drv_base_check "$(jq -r '.base // empty' <<<"$args")" || fail "$guard_error"
[[ "$(jq -r '.image_name // empty' <<<"$args")" =~ ^image-[0-9]+-[0-9a-f]+\.[a-z0-9]+$ ]] || fail "invalid image name"
async_exec ./async_job/import_image_shared.sh "$id" "$(base64 -w0 <<<"$args")"
