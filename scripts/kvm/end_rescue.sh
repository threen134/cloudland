#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 1 ] && die "$0 <vm_ID>"

ID=$1
vm_ID=inst-$ID
vm_rescue=$vm_ID-rescue
./generate_vm_instance_map.sh remove $vm_rescue
# --nvram: libvirt refuses to undefine a UEFI domain that has an NVRAM file otherwise
virsh undefine --nvram $vm_rescue >/dev/null 2>&1 || virsh undefine $vm_rescue >/dev/null 2>&1
virsh destroy $vm_rescue >/dev/null 2>&1
rm -f $xml_dir/$vm_ID/*rescue*
rm -f ${cache_dir}/meta/${vm_ID}-rescue.iso
rm -f ${image_dir}/${vm_rescue}.* ${image_dir}/${vm_rescue}_VARS.fd

virsh start $vm_ID >/dev/null 2>&1
