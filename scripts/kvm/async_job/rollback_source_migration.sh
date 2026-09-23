#!/bin/bash

cd $(dirname $0)
source ../../cloudrc

[ $# -lt 6 ] && die "$0 <migration_ID> <task_ID> <vm_ID> <router> <target_hyper> <migration_type> [target_hostid]"

migration_ID=$1
task_ID=$2
ID=$3
vm_ID=inst-$ID
router=$4
target_hyper=$5
migration_type=$6

# The instance stays here: define it again from the definition saved before the migration and start it.
# A live migration that failed keeps the domain running and defined, then both commands change nothing.
vm_xml=$xml_dir/$vm_ID/$vm_ID.xml
virsh define $vm_xml >/dev/null 2>&1
virsh autostart $vm_ID --disable >/dev/null 2>&1
virsh start $vm_ID >/dev/null 2>&1
sync_vm $ID
