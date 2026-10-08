#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 9 ] && echo "$0 <volume_ID> <volume_UUID> <size> <booting> <vm_ID> <volume_path> <pool_uuid|builtin> <hostid> <old_size>" && exit -1

vol_ID=$1
vol_UUID=$2
vol_size=$3
booting=$4
vm_ID=$5
vol_path=$6
pool=$7
hostid=$8
old_gb=$9

# virtual size in bytes; -U reads an image a running VM has open
image_size() {
    qemu-img info -U "$vol_path" 2>/dev/null | grep 'virtual size:' | cut -d' ' -f5 | tr -d '('
}

# A failed resize also reports the image's actual size (GiB), or the size clapi passed when the image
# can not be read, so clapi rolls the size back instead of leaving the volume in "error"
fail() {
    local bytes=$(image_size) gb=$old_gb
    [ -n "$bytes" ] && gb=$((bytes / 1024 / 1024 / 1024))
    echo "|:-COMMAND-:| resize_volume '$vol_ID' 'error' '$gb'"
    exit -1
}

pool_enter "$pool" "$hostid" "$vol_path" || fail

[ -f "$vol_path" ] || fail

# new size must be larger than current size
old_size=$(image_size)
let new_size=$vol_size*1024*1024*1024
[ -z "$old_size" ] && fail
[ "$old_size" -ge "$new_size" ] && fail

# While a QEMU process has the image open (running, paused, crashed, ...) resize it online through
# QEMU: the guest gets a capacity-change notification on its virtio disk and keeps running, and still
# has to grow its partition and file system itself. Only without a QEMU process ("shut off", or no such
# domain) is the image resized directly.
state=""
[ "$vm_ID" != "0" ] && state=$(virsh domstate inst-$vm_ID 2>/dev/null)
if [ -n "$state" ] && [ "$state" != "shut off" ]; then
    virsh blockresize inst-$vm_ID "$vol_path" "${vol_size}G" >/dev/null || fail
else
    qemu-img resize -q $vol_path "${vol_size}G" || fail
fi
echo "|:-COMMAND-:| resize_volume '$vol_ID' 'success'"
