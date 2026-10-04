#!/bin/bash
# The import of the copy of an image into a shared pool, run in the background by import_image_shared.sh: the image
# is fetched into the local cache, written into the pool through a temporary file or image of this job and moved into
# place (drv_import_image). Only the callback line goes to stdout.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

id=$1
args=$(base64 -d <<<"$2" 2>/dev/null)

function fail()
{
    log_debug image-copy-$id "import failed: $1"
    echo "|:-COMMAND-:| image_storage_status '$id' 'error' '${1//\'/}'"
    exit 0
}

drv_load "$args" || fail "$guard_error"
base=$(jq -r '.base // empty' <<<"$args")
img_name=$(jq -r '.image_name // empty' <<<"$args")
url=$(jq -r '.image_url_b64 // empty' <<<"$args" | base64 -d 2>/dev/null)
drv_base_check "$base" || fail "$guard_error"
drv_guard || fail "$guard_error"
drv_import_cleanup "$base"
# A copy already there: an earlier import whose report was lost
if ! drv_base_exists "$base"; then
    ensure_image_cached "$img_name" "$url" || fail "image $img_name not available"
    touch "$image_cache/$img_name"
    fmt=$(qemu-img info --output=json "$image_cache/$img_name" 2>/dev/null | jq -r '.format // empty')
    [ -n "$fmt" ] || fail "can not read the format of $img_name"
    drv_import_image "$image_cache/$img_name" "$fmt" "$base" || fail "$guard_error"
fi
log_debug image-copy-$id "imported $img_name as $base into pool $drv_pool"
echo "|:-COMMAND-:| image_storage_status '$id' 'synced' '-'"
