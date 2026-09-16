#!/bin/bash

cd $(dirname $0)
source ../../cloudrc

[ $# -lt 3 ] && die "$0 <migrate_ID> <task_ID> <vm_ID> [router] [mac_addresses]"

migrate_ID=$1
task_ID=$2
ID=$3
router=$4
macs=$5
vm_ID=inst-$ID

kill $(cat $run_dir/${vm_ID}-$migrate_ID 2>/dev/null) 2>/dev/null
rm -f $run_dir/${vm_ID}-$migrate_ID
dom_state=$(virsh domstate $vm_ID 2>/dev/null)
if [ -n "$dom_state" ]; then
    virsh shutdown $vm_ID >/dev/null 2>&1
    sleep 5
    virsh destroy $vm_ID >/dev/null 2>&1
    virsh undefine --nvram $vm_ID >/dev/null 2>&1
fi
# 清理 target_migration.sh 中 sync_nic_info 在本节点建的网络资源：安全组链、VPC 路由器（本节点仍有该 VPC 的虚拟机时 clear_local_router.sh 会保留）
for mac in $macs; do
    ./clear_sg_chain.sh tap$(echo $mac | cut -d: -f4- | tr -d :) true >/dev/null 2>&1
done
[ -n "$router" ] && [ "$router" != "0" ] && ../clear_local_router.sh $router >/dev/null 2>&1
../generate_vm_instance_map.sh remove $vm_ID >/dev/null 2>&1
rm -f ${image_dir}/${vm_ID}_VARS.fd
rm -f ${cache_dir}/meta/${vm_ID}.iso
# 回滚时虚拟机留在源节点：删除本节点为本次迁移预建的磁盘，否则重试会卡在"目标已存在同名磁盘"。
# 数据盘路径从随迁移复制过来的设备描述文件里取，避免误删本节点其他虚拟机的卷
if ! virsh domstate $vm_ID >/dev/null 2>&1; then
    for vol_xml in $xml_dir/$vm_ID/disk-*.xml; do
        [ -f "$vol_xml" ] || continue
        vol_path=$(xmllint --xpath "string(/disk/source/@file)" $vol_xml 2>/dev/null)
        case "$vol_path" in
            $volume_dir/*|$image_dir/*) rm -f "$vol_path" ;;
        esac
    done
    rm -f ${image_dir}/${vm_ID}.disk ${volume_dir}/${vm_ID}.disk
fi
rm -rf $xml_dir/$vm_ID
state=rollback
echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$SCI_CLIENT_ID' '$state' 'target hyper clear'"
sync_vm $ID
