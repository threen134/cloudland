#!/bin/bash

cd $(dirname $0)
source ../../cloudrc

[ $# -lt 4 ] && die "$0 <migrate_ID> <task_ID> <vm_ID> <migration_type>"

migrate_ID=$1
task_ID=$2
ID=$3
migration_type=$4
vm_ID=inst-$ID
echo $$ >$run_dir/${vm_ID}-$migrate_ID
state="failed"

for i in {1..1800}; do
    [ $i -gt 1 ] && sleep 1
    if [ "$migration_type" = "warm" ]; then
        # 本脚本在源节点迁移完成后才下发；离线迁移在目标节点没有 completed job 信息，域已持久定义即视为完成
        if virsh domjobinfo --completed --keep-completed $vm_ID 2>/dev/null | grep -q Completed || virsh dominfo $vm_ID 2>/dev/null | grep -qE "^Persistent:\s+yes"; then
            state="completed"
            if [ -z "$wds_address" ]; then
                vm_xml=$xml_dir/$vm_ID/${vm_ID}.xml
                virsh dumpxml --security-info $vm_ID 2>/dev/null | sed "s/autoport='yes'/autoport='no'/g" > $vm_xml.dump && mv -f $vm_xml.dump $vm_xml
            fi
        fi
    else
        vm_state=$(virsh domstate $vm_ID)
        if [ -n "$vm_state" ]; then
            state="completed"
            vm_xml=$xml_dir/$vm_ID/${vm_ID}.xml
            virsh define $vm_xml
            virsh start $vm_xml
            virsh autostart $vm_ID --disable
        fi
    fi
    if [ "$state" = "completed" ]; then 
        rm -f $run_dir/${vm_ID}-$migrate_ID
        # Update vm_instance_map metrics - add VM to current hypervisor
        # 热迁移时本脚本同步执行，stdout 会作为回调发给 clapi，只输出回调行
        ../generate_vm_instance_map.sh add $vm_ID >/dev/null 2>&1

        # 带上虚拟机实际状态：迁移期间心跳已上报过该虚拟机（clapi 因 migrating 跳过），之后状态不变就不会再上报
        vm_state=$(virsh domstate $vm_ID 2>/dev/null | head -1 | sed 's/shut off/shut_off/')
        echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$SCI_CLIENT_ID' '$state' 'vm_state=$vm_state'"
        exit 0
    fi
done

state="timeout"
# Migration timeout, clean up metrics for VM
echo "Migration timeout, cleaning up metrics for VM $vm_ID"
../generate_vm_instance_map.sh remove $vm_ID

virsh undefine --nvram $vm_ID
rm -f ${cache_dir}/meta/${vm_ID}.iso
rm -rf $xml_dir/$vm_ID
rm -f $run_dir/${vm_ID}-$migrate_ID
echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$SCI_CLIENT_ID' '$state' 'cleanup target'"
sync_vm $ID
