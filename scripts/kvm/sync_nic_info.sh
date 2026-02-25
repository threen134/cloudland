#!/bin/bash

cd `dirname $0`
source ../cloudrc

# vm_ID：虚拟机唯一标识；
# hostname：虚拟机主机名；
# os_code：操作系统编码（如 centos7、ubuntu20 等）；
# update_meta（可选）：是否更新元数据，默认 false。
[ $# -lt 3 ] && echo "$0 <vm_ID> <hostname> <os_code> [update_meta]" && exit -1

ID=$1
vm_name=$2
os_code=$3
update_meta=$4
[ -z "$update_meta" ] && update_meta=false
# 从标准输入（stdin） 读取内容并赋值给 vlans，结合后续 jq 操作，
# 可推断输入是 JSON 格式的网卡 / VLAN 列表（如 ["vlan100", "vlan200"] 或更复杂的网卡对象数组）。
# 通过给脚本的管道符传入
vlans=$(cat)
# 通过 jq 工具解析 vlans 这个 JSON 数组，获取其长度（即网卡 / VLAN 数量），赋值给 nvlan。
nvlan=$(jq length <<< $vlans)
i=0
while [ $i -lt $nvlan ]; do
# jq -r .[$i] <<< $vlans：提取数组中第 i 个元素（-r 表示输出原始字符串，而非 JSON 格式化字符串）；
# 通过管道将该元素传递给 ./attach_vm_nic.sh 脚本，并传入虚拟机 ID、主机名、系统编码、是否更新元数据这 4 个参数；
    jq -r .[$i] <<< $vlans | ./attach_vm_nic.sh "$ID" "$vm_name" "$os_code" "$update_meta"
    let i=$i+1
done
# 该脚本是一个批量处理封装层：
# 接收虚拟机基础信息和可选的元数据更新标识；
# 从标准输入读取 JSON 格式的网卡 / VLAN 列表；
# 逐个提取列表中的网卡 / VLAN 信息，调用 attach_vm_nic.sh 完成单网卡的具体配置；
# 最终实现 “一台虚拟机批量挂载多张网卡” 的操作。
