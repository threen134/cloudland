#!/bin/bash
# Grow a volume of a shared pool (shared-storage-design.md §9.4): online through QEMU when its instance runs on this
# host, otherwise the file itself. The pool, the volume with its new size, the old size and its instance come as JSON
# on stdin.

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 2 ] && die "$0 <volume_ID> <volume_UUID>"

vol_ID=$1
args=$(cat)
[[ "$vol_ID" =~ ^[0-9]+$ ]] || die "invalid volume id $vol_ID"
size=$(jq -r '.size_gb // 0' <<<"$args")
old_gb=$(jq -r '.old_gb // 0' <<<"$args")
inst=$(jq -r '.instance_id // 0' <<<"$args")
drv_vol=""

# A failed resize reports the size the image really has (GiB), or the old size when it can not be read, so clapi rolls
# the size back instead of leaving the volume in error
function fail()
{
    local bytes="" gb=$old_gb
    [ -n "$drv_vol" ] && bytes=$(drv_size "$drv_vol")
    [[ "$bytes" =~ ^[0-9]+$ ]] && gb=$((bytes / 1024 / 1024 / 1024))
    log_debug $vol_ID "resize of shared volume $vol_ID failed: $1"
    echo "|:-COMMAND-:| resize_volume '$vol_ID' 'error' '$gb'"
    exit 0
}

drv_load "$args" || fail "$guard_error"
if ! drv_volume "$args" "$vol_ID"; then
    drv_vol=""
    fail "$guard_error"
fi
drv_guard || fail "$guard_error"
drv_exists "$drv_vol" || fail "volume $drv_vol not found"
[[ "$size" =~ ^[1-9][0-9]*$ ]] || fail "invalid size $size"
cur=$(drv_size "$drv_vol")
[[ "$cur" =~ ^[0-9]+$ ]] || fail "can not read $drv_vol"
[ "$cur" -ge $((size * 1024 * 1024 * 1024)) ] && fail "the volume is $cur bytes already"
state=""
[[ "$inst" =~ ^[1-9][0-9]*$ ]] && state=$(virsh domstate inst-$inst 2>/dev/null)
if [ -n "$state" ] && [ "$state" != "shut off" ]; then
    # By the device of the disk: an RBD disk has no path virsh could name it by
    dev=$(xmllint --xpath 'string(//target/@dev)' $xml_dir/inst-$inst/disk-${vol_ID}.xml 2>/dev/null)
    [[ "$dev" =~ ^vd[a-z]+$ ]] || fail "the disk of volume $vol_ID is not recorded on this host"
    virsh blockresize inst-$inst "$dev" "${size}G" >/dev/null || fail "virsh blockresize failed"
else
    drv_resize "$drv_vol" "$size" || fail "qemu-img resize failed"
fi
echo "|:-COMMAND-:| resize_volume '$vol_ID' 'success'"
