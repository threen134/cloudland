#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 3 ] && die "$0 <vm_ID> <router> <boot_pool_uuid|builtin|-> [boot_disk_relpath]"

ID=$1
vm_ID=inst-$ID
router=$2
boot_pool=$3
boot_relpath=$4
vm_xml=$(virsh dumpxml $vm_ID)

pending_start_remove $ID
# Call generate_vm_instance_map.sh to remove mapping before VM deletion
./generate_vm_instance_map.sh remove $vm_ID

virsh undefine --nvram $vm_ID >/dev/null 2>&1
virsh destroy $vm_ID >/dev/null 2>&1

# Clean up VM adjust custom metrics
./cleanup_vm_custom_metrics.sh $ID

# Clean up old format rule_id metrics after VM deletion
echo "=== Starting rule_id metrics cleanup for deleted VM ==="
if [ -f "./cleanup_old_rule_id_metrics.sh" ]; then
    echo "Cleaning up old format rule_id metrics for deleted VM: $vm_ID"
    ./cleanup_old_rule_id_metrics.sh --force || {
        echo "Warning: Rule_id metrics cleanup failed, but VM deletion completed successfully"
    }
else
    echo "Warning: Rule_id metrics cleanup script not found, skipping metrics cleanup"
fi
echo "=== Rule_id metrics cleanup completed ==="

count=$(echo $vm_xml | xmllint --xpath 'count(/domain/devices/interface)' -)
for (( i=1; i <= $count; i++ )); do
    vif_dev=$(echo $vm_xml | xmllint --xpath "string(/domain/devices/interface[$i]/target/@dev)" -)
    ./clear_sg_chain.sh $vif_dev
    meta_file="$async_job_dir/$vif_dev"
    [ -f "$meta_file" ] && rm -f "$meta_file"
done
./clear_local_router.sh $router

rm -f ${image_dir}/${vm_ID}_VARS.fd
rm -f ${cache_dir}/meta/${vm_ID}.iso
rm -rf $xml_dir/$vm_ID

./end_rescue.sh $ID
# The boot disk and the NVRAM of a pool other than the builtin one are deleted only after the pool is checked:
# an unmounted pool must not be written, and a lost pool (no pool passed) keeps its files
if [ "$boot_pool" != "-" ] && [ -n "$boot_relpath" ]; then
    root=$(pool_root "$boot_pool")
    if [ -n "$root" ] && pool_enter "$boot_pool" "$NODE_ID" "$root/$boot_relpath"; then
        rm -f "$root/$boot_relpath"
        [ "$boot_pool" != "builtin" ] && rm -f "$root/nvram/${vm_ID}_VARS.fd"
    else
        log_debug $ID "boot disk $boot_relpath of pool $boot_pool is kept: $guard_error"
    fi
    exec 8>&-
fi
# Leftovers of the builtin pool from before the pools (meta, rescue copies)
rm -f ${image_dir}/${vm_ID}.*
echo "|:-COMMAND-:| $(basename $0) '$ID'"
