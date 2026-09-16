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
ssh_opts="-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new"
target_uri=qemu+ssh://$target_hyper/system

log_debug $ID "source_migration.sh: Starting migration_ID=$migration_ID, task_ID=$task_ID, target_hyper=$target_hyper, migration_type=$migration_type"

function report()
{
    echo "|:-COMMAND-:| migrate_vm.sh '$migration_ID' '$task_ID' '$ID' '$SCI_CLIENT_ID' '$1' '$2'"
}

# 本地存储迁移失败时删除本次在目标节点创建的磁盘文件（域已在目标节点定义时不删）
function clear_target_disks()
{
    [ -z "$created_disks" ] && return
    ssh -n $ssh_opts $target_hyper virsh domstate $vm_ID >/dev/null 2>&1 && return
    ssh -n $ssh_opts $target_hyper rm -f $created_disks
}

log_debug $ID "source_migration.sh: Dumping XML for $vm_ID"
virsh dumpxml $vm_ID >$xml_dir/$vm_ID/${vm_ID}.xml
if [ "$migration_type" = "warm" ]; then
    state='source_rollback'
    vm_state=$(virsh domstate $vm_ID)
    old_state=$vm_state
    # 预检 SSH，同时把目标节点主机密钥记入 known_hosts（virsh qemu+ssh 不会自动接受新主机密钥）
    if ! ssh -n $ssh_opts $target_hyper true; then
        log_debug $ID "source_migration.sh: ssh to $target_hyper failed"
        report $state "ssh to target host failed"
        exit 0
    fi
    created_disks=""
    if [ -z "$wds_address" ]; then
        # 本地存储：磁盘按原路径复制到目标节点，目标上已存在同名文件时中止，避免覆盖
        disks=$(virsh domblklist $vm_ID --details | awk '$1 == "file" && $2 == "disk" {print $3 ":" $4}')
        if [ -z "$disks" ]; then
            report $state "no local disk found"
            exit 0
        fi
        migrate_disks=""
        for disk in $disks; do
            dev=${disk%%:*}
            path=${disk#*:}
            if ssh -n $ssh_opts $target_hyper test -e $path; then
                clear_target_disks
                report $state "disk $path already exists on target host"
                exit 0
            fi
            if [ "$vm_state" = "shut off" ]; then
                ssh -n $ssh_opts $target_hyper mkdir -p $(dirname $path) && scp -q $ssh_opts $path $target_hyper:$path
            else
                vsize=$(qemu-img info -U --output=json $path | jq -r '."virtual-size"')
                ssh -n $ssh_opts $target_hyper "mkdir -p $(dirname $path) && qemu-img create -q -f qcow2 $path $vsize"
            fi
            if [ $? -ne 0 ]; then
                created_disks="$created_disks $path"
                clear_target_disks
                report $state "failed to prepare disk $path on target host"
                exit 0
            fi
            created_disks="$created_disks $path"
            migrate_disks="$migrate_disks,$dev"
        done
        # 数据盘的设备描述文件，卸载数据盘时要用
        ls $xml_dir/$vm_ID/disk-*.xml >/dev/null 2>&1 && scp -q $ssh_opts $xml_dir/$vm_ID/disk-*.xml $target_hyper:$xml_dir/$vm_ID/
        # UEFI 虚拟机的真实 NVRAM（启动项、Secure Boot 状态）：目标节点上只是空模板，不复制的话关机迁移后会丢
        [ -f $image_dir/${vm_ID}_VARS.fd ] && scp -q $ssh_opts $image_dir/${vm_ID}_VARS.fd $target_hyper:$image_dir/${vm_ID}_VARS.fd
    fi
    # VPC 网络预热：切换那一刻自动生效（virsh migrate 比虚拟机在目标恢复运行晚返回 1 秒多，返回后再切会多断这么久）
    # - 本节点为该虚拟机 MAC 预置指向目标节点的 VXLAN 条目：虚拟机还在本节点时网桥经 tap 本地转发，用不到；
    #   切换后 tap 删除，网桥泛洪到 VXLAN 口即按此条目发往目标（本节点同 VPC 虚拟机、路由器上浮动 IP/SNAT 的回程）
    # - 目标节点预置本节点路由器网关 MAC 指向本节点：切换后虚拟机仍按 ARP 缓存把出方向发给本节点路由器，
    #   浮动 IP 与 SNAT 地址不变，completed 后 clapi 在目标重建浮动 IP 之后再宣告网关切过去
    #   （只写转发表，不写邻居表：VXLAN ARP 代理会用邻居表应答，目标节点其他虚拟机会拿到错误的网关 MAC）
    prewarmed=""
    vm_xml_file=$xml_dir/$vm_ID/${vm_ID}.xml
    vtep_ip=$(ifconfig $vxlan_interface 2>/dev/null | grep 'inet ' | awk '{print $2}')
    if [[ "$target_hyper" =~ ^[0-9.]+$ ]]; then
        count=$(xmllint --xpath 'count(/domain/devices/interface)' $vm_xml_file 2>/dev/null)
        for (( i=1; i <= ${count:-0}; i++ )); do
            mac=$(xmllint --xpath "string(/domain/devices/interface[$i]/mac/@address)" $vm_xml_file)
            vni=$(xmllint --xpath "string(/domain/devices/interface[$i]/source/@bridge)" $vm_xml_file)
            vni=${vni#br}
            [[ "$vni" =~ ^[0-9]+$ ]] && [ "$vni" -ge 4095 ] && ip link show v-$vni >/dev/null 2>&1 || continue
            bridge fdb replace $mac dev v-$vni dst $target_hyper self permanent >/dev/null 2>&1 && prewarmed="$prewarmed $mac/$vni"
            gw_mac=$(ip netns exec router-$router cat /sys/class/net/ns-$vni/address 2>/dev/null)
            [ -n "$gw_mac" ] && [ -n "$vtep_ip" ] && \
                ssh -n $ssh_opts $target_hyper "bridge fdb replace $gw_mac dev v-$vni dst $vtep_ip self permanent" >/dev/null 2>&1
        done
    fi
    # 迁移进度：后台每 3 秒取一次 domjobinfo 上报（stdout 实时转发，主流程阻塞在 virsh migrate 时也能发出）
    (
        while sleep 3; do
            stats=$(virsh domjobinfo $vm_ID --rawstats 2>/dev/null)
            total=$(awk '/^data_total:/ {print $2}' <<<"$stats")
            processed=$(awk '/^data_processed:/ {print $2}' <<<"$stats")
            [[ "$total" =~ ^[0-9]+$ ]] && [[ "$processed" =~ ^[0-9]+$ ]] && [ "$total" -gt 0 ] || continue
            echo "|:-COMMAND-:| migrate_progress.sh '$migration_ID' '$ID' '$(( processed * 100 / total ))' '$processed' '$total'"
        done
    ) &
    progress_pid=$!
    if [ "$vm_state" = "shut off" ]; then
        log_debug $ID "source_migration.sh: Starting offline migration to $target_hyper"
        virsh migrate --undefinesource --persistent --offline $vm_ID $target_uri
    # 热迁移不加 --suspend：QEMU 切换后在目标节点直接恢复运行（源端在切换前已暂停，由 libvirt 保证），
    # 否则虚拟机要等本脚本发现源端域消失、再经 ssh virsh resume，停机时间多出约 0.5–1 秒
    elif [ -z "$wds_address" ]; then
        log_debug $ID "source_migration.sh: Starting live migration with local storage to $target_hyper, disks ${migrate_disks#,}"
        virsh migrate --undefinesource --persistent --live --copy-storage-all --migrate-disks ${migrate_disks#,} --migrateuri tcp://$target_hyper --disks-uri tcp://$target_hyper $vm_ID $target_uri
    else
        log_debug $ID "source_migration.sh: Starting live migration to $target_hyper"
        virsh migrate --undefinesource --persistent --live $vm_ID $target_uri
    fi
    migrate_rc=$?
    kill $progress_pid 2>/dev/null
    if [ $migrate_rc -ne 0 ]; then
        log_debug $ID "source_migration.sh: virsh migrate failed with non-zero exit code"
        # 虚拟机仍在本节点，撤销预置的 VXLAN 条目
        for p in $prewarmed; do
            bridge fdb del ${p%/*} dev v-${p#*/} self >/dev/null 2>&1
        done
        clear_target_disks
        report $state "virsh migrate returns non-zero"
        exit 0
    fi
    # 后台让目标节点删除该虚拟机 MAC 指向其他节点的旧条目（迁入已有同 VPC 虚拟机的节点时存在）；不在这里宣告网关：
    # 目标节点浮动 IP 在 completed 后才重建，提前宣告会让出方向经 router-0 SNAT 成宿主机地址，conntrack 记下错误映射，已有连接断开
    ssh -n $ssh_opts $target_hyper /opt/cloudland/scripts/backend/post_migration_net.sh $ID >/dev/null 2>&1 &
    log_debug $ID "source_migration.sh: virsh migrate command completed, waiting for VM to disappear from source"
    for i in {1..60}; do
        vm_state=$(virsh domstate $vm_ID 2>/dev/null)
        if [ -z "$vm_state" ]; then
            # 兜底：迁移前在运行、到目标后仍处于暂停时才恢复
            if [ "$old_state" = "running" ] && [ "$(ssh -n $ssh_opts $target_hyper virsh domstate $vm_ID 2>/dev/null | head -1)" = "paused" ]; then
                ssh -n $ssh_opts $target_hyper virsh resume $vm_ID >/dev/null
                if [ $? -ne 0 ]; then
                    log_debug $ID "source_migration.sh: failed to resume vm on the target host"
                    report $state "failed to resume vm on target host"
                    exit 0
                fi
                log_debug $ID "source_migration.sh: vm $vm_ID on target host resumed"
            fi
            break
        fi
        sleep 0.5
    done
    if [ -n "$vm_state" ]; then
        log_debug $ID "source_migration.sh: VM still exists after 60 seconds wait"
        report $state "vm remains after virsh migrate"
        exit 0
    fi
    log_debug $ID "source_migration.sh: VM successfully removed from source"
else
    log_debug $ID "source_migration.sh: Cold migration - shutting down VM"
    virsh shutdown $vm_ID
    for i in {1..60}; do
	vm_state=$(virsh domstate $vm_ID)
        [ "$vm_state" = "shut off" ] && break
        sleep 0.5
    done
    if [ "$vm_state" != "shut off" ]; then
        log_debug $ID "source_migration.sh: VM did not shut down cleanly, forcing destroy"
        virsh destroy $vm_ID
    fi
    log_debug $ID "source_migration.sh: VM shutdown/destroy completed"
fi

state="source_prepared"
log_debug $ID "source_migration.sh: Migration preparation completed, reporting state=$state"
report $state ""
