#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 4 ] && echo "$0 <vm_ID> <vm_name> <os_code> <update_meta>" && exit -1

ID=$1
vm_ID=inst-$ID
vm_name=$2
os_code=$3
update_meta=$4

# 1. 读取vlan_info中的vlan、ip、mac等关键网络配置信息，并生成网卡名称nic_name和网桥名称vm_br；
# 2. 执行create_link.sh脚本创建对应vlan的网络链路；
# 3. 设置vm_br网桥的老化时间为120秒；
# 4. 检查虚拟机（vm_ID）是否已存在该mac地址的网卡，若不存在则进行以下操作：
#    - 复制网卡配置模板文件到指定路径，替换模板中的MAC、网桥、网卡名、队列数等变量；
#    - 尝试通过virsh attach-device命令为虚拟机实时且持久化挂载网卡，若失败则仅配置持久化挂载；
#    - 将虚拟机IP、网桥、路由器信息写入异步任务文件；
# 5. 等待udev设备规则生效完成；
# 6. 异步执行send_spoof_arp.py脚本发送ARP欺骗包；
# 7. 执行set_nic_speed.sh脚本设置网卡的入站、出站速率；
# 8. 执行reapply_secgroup.sh脚本重新应用安全组规则；
# 9. 执行set_subnet_gw.sh脚本设置子网网关；
# 10. 执行set_host.sh脚本配置主机相关映射（路由器、vlan、mac、虚拟机名、IP）；
# 11. 读取vlan_info中的额外IP地址信息，若存在额外IP或需要更新元数据，则执行apply_second_ips.sh脚本应用辅助IP；
# 12. 输出包含脚本执行信息的COMMAND标记行。

vlan_info=$(cat)
# 从标准输入读取 JSON 格式的vlan_info，并通过jq工具解析出关键网络配置项，赋值给对应变量：
# vlan：VLAN 编号；
# ip：虚拟机 IP 地址（可能带子网掩码，如 192.168.1.10/24）；
# mac：网卡 MAC 地址；
# gateway：网关地址；
# router：路由器标识；
# inbound/outbound：网卡入站 / 出站速率限制；
# allow_spoofing：是否允许 ARP 欺骗。
read -d'\n' -r vlan ip mac gateway router inbound outbound allow_spoofing is_private < <(jq -r ".vlan, .ip_address, .mac_address, .gateway, .router, .inbound, .outbound, .allow_spoofing, .is_private" <<<$vlan_info)
# 从MAC生成网卡名（截取后3段，去冒号，前缀tap）
nic_name=tap$(echo $mac | cut -d: -f4- | tr -d :)
vm_br=br$vlan
# 判断是否为 RFC 1918 私有网段，选择对应物理接口
nic_dev=""
if [ "$is_private" = "true" ] && [ -n "$private_vlan_interface" ]; then
    nic_dev=$private_vlan_interface
fi
./create_link.sh $vlan $nic_dev
# 设置网桥老化时间为120秒（减少ARP表冗余）
brctl setageing $vm_br 120
# 检查虚拟机是否已挂载该MAC的网卡
virsh domiflist $vm_ID | grep $mac
# 第三个参数：指定要创建的 veth 端口的另一端名称（peerdev， 应该是vpc到namespace的veth）
if [ $? -ne 0 ]; then 
    #template_dir=/opt/cloudland/scripts/xml
      # 网卡配置模板路径（全局配置cloudrc中定义）
    template=$template_dir/interface.xml
     # 生成当前网卡的XML配置文件路径，这个网卡是虚拟机的网卡
    interface_xml=$xml_dir/$vm_ID/$nic_name.xml 
    # 计算队列数：(虚拟机CPU数+1)/2（整数运算）
    let queue_num=($(virsh dominfo $vm_ID | grep 'CPU(s)' | awk '{print $2}')+1)/2
    # 复制模板并替换变量（MAC、网桥、网卡名、队列数）
    cp $template $interface_xml
    sed -i "s/VM_MAC/$mac/g; s/VM_BRIDGE/$vm_br/g; s/VM_VTEP/$nic_name/g; s/QUEUE_NUM/$queue_num/g" $interface_xml
    # 挂载网卡：优先实时+持久化，失败则仅持久化
    virsh attach-device $vm_ID $interface_xml --live --persistent
    [ $? -ne 0 ] && virsh attach-device $vm_ID $interface_xml --config
    # 写入异步任务文件（记录IP、网桥、路由器信息）
    echo "vm_ip=${ip%/*} vm_br=$vm_br router=$router" >> "$async_job_dir/$nic_name"
fi
# 核心逻辑
# 通过virsh domiflist检查网卡是否已挂载，避免重复操作；
# 基于 XML 模板生成当前网卡的配置文件，替换模板中的占位符（MAC、网桥、队列数等）；
# 调用virsh attach-device挂载网卡：
# --live：实时生效（不重启虚拟机）；
# --persistent：持久化（虚拟机重启后仍生效）；
# 若实时 + 持久化失败，则仅执行--config（仅持久化）；
# 将关键网络信息写入异步任务文件，供后续流程使用。


# 等待系统设备管理规则加载完成，避免网卡未就绪导致后续操作失败；
udevadm settle
# 异步执行 ARP 欺骗脚本，让网络内其他节点快速感知该虚拟机的 IP-MAC 映射；
async_exec ./send_spoof_arp.py "$vm_br" "${ip%/*}" "$mac"
# set_nic_speed.sh：限制网卡入站 / 出站带宽，避免资源滥用；
./set_nic_speed.sh "$ID" "$nic_name" "$inbound" "$outbound"
# reapply_secgroup.sh：重新应用安全组规则（如防火墙策略、端口限制等）；
./reapply_secgroup.sh "$ip" "$mac" "$allow_spoofing" "$nic_name" <<< $vlan_info
# set_subnet_gw.sh：配置子网网关，确保虚拟机可访问外网；
./set_subnet_gw.sh "$router" "$vlan" "$gateway" "$ext_vlan"
# set_host.sh：更新主机级映射（如 /etc/hosts、路由表、DHCP 配置等）。
./set_host.sh "$router" "$vlan" "$mac" "$vm_name" "$ip"
# 若虚拟机有辅助 IP（more_addresses）或需要更新元数据，调用apply_second_ips.sh配置辅助 IP，适配多 IP 场景。
more_addresses=$(jq -r .more_addresses <<< $vlan_info)
if [ -n "$more_addresses" -o "$update_meta" = true ]; then
    ./apply_second_ips.sh "$ID" "$mac" "$os_code" "$update_meta" "$ip" "$gateway" <<<$more_addresses
fi
echo "|:-COMMAND-:| $(basename $0) '$ID' '$mac' '$SCI_CLIENT_ID'"
