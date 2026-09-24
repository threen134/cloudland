#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 11 ] && die "$0 <migrate_ID> <task_ID> <vm_ID> <name> <cpu> <memory> <disk_size> <source_hyper> <migration_type> <boot_loader> <instance_uuid>"

migrate_ID=$1
task_ID=$2
ID=$3
vm_ID=inst-$ID
vm_name=$4
vm_cpu=$5
vm_mem=$6
disk_size=$7
source_hyper=$8
migration_type=$9
boot_loader=${10}
instance_uuid=${11:-$ID}

md=$(cat)
metadata=$(echo $md | base64 -d)

# Local disks are only on the source: source_migration.sh copies them with virsh migrate while the source is online.
# The target only prepares the config drive, a UEFI NVRAM template and the interfaces; the domain definition comes
# with the migration. The disks are not touched here: when the scheduler chose this host, the disk plan does not
# exist yet (clapi makes it on target_prepared).
if [ "$migration_type" != "warm" ]; then
    echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$NODE_ID' 'not_supported' 'cold migration requires shared storage'"
    exit 0
fi
mkdir -p $xml_dir/$vm_ID
./build_meta.sh "$vm_ID" "$vm_name" <<< $md >/dev/null 2>&1
# Template only: source_migration.sh copies the real NVRAM (boot entries, Secure Boot state) over it
[ "$boot_loader" = "uefi" ] && [ ! -f $image_dir/${vm_ID}_VARS.fd ] && cp $nvram_template $image_dir/${vm_ID}_VARS.fd
os_code=$(jq -r '.os_code' <<< $metadata)
# Callbacks muted: the instance is still on the source, and the attach_vm_nic callback would move its interfaces to
# this host and spread forwarding entries over the cluster, sending traffic here before the instance is here.
# LaunchVM sync runs it again after completed, with its callbacks.
jq .vlans <<< $metadata | ./sync_nic_info.sh "$ID" "$vm_name" "$os_code" >/dev/null
./generate_vm_instance_map.sh add $vm_ID >/dev/null 2>&1
echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$NODE_ID' 'target_prepared' ''"
