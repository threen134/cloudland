#!/bin/bash

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 3 ] && die "$0 <migrate_ID> <task_ID> <vm_ID> [router] [mac_addresses]"

migrate_ID=$1
task_ID=$2
ID=$3
router=$4
macs=$5
vm_ID=inst-$ID
# The disk plan on stdin (empty when the plan was never made)
plan=$(cat)

kill $(cat $run_dir/${vm_ID}-$migrate_ID 2>/dev/null) 2>/dev/null
rm -f $run_dir/${vm_ID}-$migrate_ID
dom_state=$(virsh domstate $vm_ID 2>/dev/null)
# On a rollback the instance stays on the source; on the real target the domain is either missing or only defined.
# A domain running here means this host is where the instance actually runs (a target computed as the source):
# going on would destroy, undefine and delete the disks of a running instance.
if [ "$dom_state" = "running" ]; then
    log_debug $ID "clear_target_migration.sh: $vm_ID is running on this node, refusing target cleanup"
    echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$NODE_ID' 'rollback' 'cleanup skipped: instance is running here'"
    sync_vm $ID
    exit 0
fi
if [ -n "$dom_state" ]; then
    virsh shutdown $vm_ID >/dev/null 2>&1
    sleep 5
    virsh destroy $vm_ID >/dev/null 2>&1
    virsh undefine --nvram $vm_ID >/dev/null 2>&1
fi
# Network resources sync_nic_info built here in target_migration.sh: security group chains, the VPC router
# (clear_local_router.sh keeps it while other instances of the VPC run here)
for mac in $macs; do
    ./clear_sg_chain.sh tap$(echo $mac | cut -d: -f4- | tr -d :) true >/dev/null 2>&1
done
[ -n "$router" ] && [ "$router" != "0" ] && ../clear_local_router.sh $router >/dev/null 2>&1
../generate_vm_instance_map.sh remove $vm_ID >/dev/null 2>&1
rm -f ${image_dir}/${vm_ID}_VARS.fd
rm -f ${cache_dir}/meta/${vm_ID}.iso
if ! virsh domstate $vm_ID >/dev/null 2>&1; then
    # Only the files this migration created (recorded by prepare_migration_disks.sh), so a retry does not stop at
    # "disk already exists", and no file of another instance is touched
    ../prepare_migration_disks.sh cleanup $migrate_ID $NODE_ID $ID >/dev/null 2>&1
    # NVRAM copied into the pool of the boot disk
    boot_pool=$(jq -r '.[]? | select(.booting) | .dst_pool_uuid' <<<"$plan" 2>/dev/null)
    boot_root=$(jq -r '.[]? | select(.booting) | .dst_pool_root' <<<"$plan" 2>/dev/null)
    if [ -n "$boot_pool" ] && [ "$boot_pool" != "builtin" ] && pool_enter "$boot_pool" "$NODE_ID" "$boot_root/nvram/${vm_ID}_VARS.fd"; then
        rm -f $boot_root/nvram/${vm_ID}_VARS.fd
    fi
fi
rm -rf $xml_dir/$vm_ID
state=rollback
echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$NODE_ID' '$state' 'target hyper clear'"
sync_vm $ID
