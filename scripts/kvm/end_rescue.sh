#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./vnc_lib.sh

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

# A cold start: a domain defined without a VNC password gets one now
vnc_ensure_domain_passwd $vm_ID
virsh start $vm_ID >/dev/null 2>&1

# Report the state the instance is in now (handled like action_vm.sh): clapi set it to shut_off when the rescue
# ended, and the heartbeat only reports instances whose state changed since it last looked, which an instance
# rescued for a few seconds never did, so it showed shut_off for minutes while it was running
state=$(timeout 30 virsh domstate $vm_ID 2>/dev/null | sed 's/shut off/shut_off/g')
if [ -n "$state" ]; then
    echo "|:-COMMAND-:| action_vm.sh '$ID' '$state'"
fi
