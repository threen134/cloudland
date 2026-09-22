#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 5 ] && echo "$0 <volume_ID> <volume_UUID> <size> <booting> <vm_ID>" && exit -1

vol_ID=$1
vol_UUID=$2
vol_size=$3
booting=$4
vm_ID=$5
if [ "$booting" = "false" ]; then
    vol_path="$volume_dir/volume-${vol_ID}.disk"
else
    vol_path="$image_dir/inst-${vm_ID}.disk"
fi
if [ ! -f "$vol_path" ]; then
    echo "|:-COMMAND-:| resize_volume '$vol_ID' 'error'"
    exit -1
fi

# new size must be larger than current size; checked before touching the VM (-U reads an image in use)
old_size=$(qemu-img info -U $vol_path | grep 'virtual size:' | cut -d' ' -f5 | tr -d '(')
let new_size=$vol_size*1024*1024*1024
if [ "$old_size" -ge "$new_size" ]; then
    echo "|:-COMMAND-:| resize_volume '$vol_ID' 'error'"
    exit -1
fi

# The image cannot be resized while a VM has it open: stop the VM if it is up and start it again
# afterwards, on success and on failure alike. A VM that was shut off stays shut off. A paused VM
# loses its memory state to the hard stop either way, so it is brought back running.
# action_vm.sh reports the resulting state to clapi; a bare virsh start would leave the instance
# recorded as shut_off, and the heartbeat only reports changes since its previous run.
restart_vm=false
if [ "$vm_ID" != "0" ]; then
    state=$(virsh domstate inst-$vm_ID 2>/dev/null)
    if [ -n "$state" ] && [ "$state" != "shut off" ]; then
        restart_vm=true
        ./action_vm.sh $vm_ID hard_stop
    fi
fi

qemu-img resize -q $vol_path "${vol_size}G"
rc=$?
[ "$restart_vm" = "true" ] && ./action_vm.sh $vm_ID start
if [ $rc -eq 0 ]; then
    echo "|:-COMMAND-:| resize_volume '$vol_ID' 'success'"
else
    echo "|:-COMMAND-:| resize_volume '$vol_ID' 'error'"
    exit -1
fi
