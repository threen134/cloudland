#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 6 ] && die "$0 <migration_ID> <task_ID> <vm_ID> <router> <target_hyper> <migration_type>"

migration_ID=$1
task_ID=$2
ID=$3
vm_ID=inst-$ID
router=$4
target_hyper=$5
migration_type=$6
state=failed

vm_xml=$(cat $xml_dir/$vm_ID/$vm_ID.xml)
# 迁移成功后本节点不应再有该虚拟机（--undefinesource 已移除）。本地存储下若它还在，说明回滚已把它重新定义并启动，
# 此时绝不能销毁它、更不能删磁盘（本节点是唯一一份）；判断必须在 destroy/undefine 之前做
if [ -z "$wds_address" ] && virsh domstate $vm_ID >/dev/null 2>&1; then
    log_debug $ID "finish_source_migration.sh: vm still defined on source, skip cleanup"
    exit 0
fi
virsh destroy $vm_ID
virsh undefine --nvram $vm_ID
if [ -n "$wds_address" ]; then
    ./clear_hyper_vhost.sh $ID
else
    # 本地存储：磁盘已随迁移复制到目标节点，删除源节点上的磁盘文件（只删 cache 目录下的）
    for disk in $(echo $vm_xml | xmllint --xpath "/domain/devices/disk[@device='disk']/source/@file" - 2>/dev/null | sed 's/file="\([^"]*\)"/\1/g'); do
        case "$disk" in
            $volume_dir/*|$image_dir/*) rm -f "$disk" ;;
        esac
    done
fi

count=$(echo $vm_xml | xmllint --xpath 'count(/domain/devices/interface)' -)
for (( i=1; i <= $count; i++ )); do
    vif_dev=$(echo $vm_xml | xmllint --xpath "string(/domain/devices/interface[$i]/target/@dev)" -)
    ./clear_sg_chain.sh $vif_dev
done
./clear_local_router.sh $router

# Update vm_instance_map metrics - remove VM from source hypervisor
echo "Updating vm_instance_map metrics: removing VM $vm_ID from source hypervisor"
./generate_vm_instance_map.sh remove $vm_ID

rm -f ${cache_dir}/meta/${vm_ID}.iso
rm -rf $xml_dir/$vm_ID

# Clean up all custom metrics for migrated VM
echo "=== Starting VM custom metrics cleanup ==="
if [ -f "/opt/cloudland/scripts/kvm/cleanup_vm_custom_metrics.sh" ]; then
    echo "Cleaning up all custom metrics for migrated VM: $vm_ID (ID: $ID)"
    /opt/cloudland/scripts/kvm/cleanup_vm_custom_metrics.sh $ID || {
        echo "Warning: VM custom metrics cleanup failed, but migration completed successfully"
    }
else
    echo "Warning: VM custom metrics cleanup script not found, skipping metrics cleanup"
fi
echo "=== VM custom metrics cleanup completed ==="
