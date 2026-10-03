#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./vnc_lib.sh

[ $# -lt 3 ] && die "$0 <vm_ID> <cpu> <memory>"

ID=$1
vm_ID=inst-$1
vm_cpu=$2
vm_mem=$3
state=error

# Graceful shutdown first, fall back to hard stop after 30s timeout. Only this script reports: the
# action_vm.sh callbacks would set the instance shut_off while clapi keeps it resizing
./action_vm.sh $ID stop >/dev/null
wait_vm_status $vm_ID "shut_off"
./action_vm.sh $ID hard_stop >/dev/null

let vm_mem=${vm_mem%[m|M]}*1024

# backup vm xml
vm_xml=$xml_dir/$vm_ID/${vm_ID}.xml
# The definition and its backups carry the VNC password: instances defined before it existed have an open directory
chmod 700 $xml_dir/$vm_ID 2>/dev/null
mv $vm_xml $vm_xml-$(date +'%s.%N')
# --security-info keeps the VNC password in the definition written again below
virsh dumpxml --security-info $vm_ID >$vm_xml
vnc_xml_ensure_passwd $vm_xml

# --keep-nvram: the definition is written again right below, the UEFI variables (boot entries) must survive it
virsh undefine --keep-nvram $vm_ID >/dev/null 2>&1 || virsh undefine $vm_ID >/dev/null 2>&1

# edit vm xml
sed_cmd="s#>.*</memory>#>$vm_mem</memory>#g; s#>.*</currentMemory>#>$vm_mem</currentMemory>#g; s#>.*</vcpu>#>$vm_cpu</vcpu>#g; s#\(<topology[^>]*\)cores='[0-9]*'#\1cores='$vm_cpu'#g"
sed -i "$sed_cmd" $vm_xml
virsh define $vm_xml
# Never autostart: after a host reboot report_rc.sh starts instances once their pools are checked
virsh autostart $vm_ID --disable
virsh start $vm_ID
[ $? -eq 0 ] && state=running
echo "|:-COMMAND-:| inst_status.sh '$NODE_ID' '$ID $state'"
