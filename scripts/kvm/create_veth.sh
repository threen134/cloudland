#!/bin/bash

cd $(dirname $0)
source ../cloudrc

# 检查脚本传入的参数数量：如果参数个数小于 3，打印脚本的使用帮助（$0 是脚本自身名称，
# 提示需传入 router、veth_name、peer_name 三个参数），并以退出码 -1 终止脚本。

[ $# -lt 3 ] && echo "$0 <router> <veth_name> <peer_name>" && exit -1
# 第一个参数：指定要操作的网络命名空间名称（router）
router=$1
 # 第二个参数：指定要创建的 veth 端口名称（device）(宿主机namespace)，这个接口会挂在bridege上
device=$2
# 第三个参数：指定要创建的 veth 端口的另一端名称（peerdev， 应该是vpc到namespace的veth）
peerdev=$3

# 创建一对 veth 虚拟网卡：
# 一端命名为 $device，另一端（对等端）命名为 $peerdev；
# veth 设备的特点是两端成对存在，数据从一端进入会从另一端出，常用于网络命名空间之间的通信。
ip link add $device type veth peer name $peerdev
ip link set $device up
ip link set $peerdev netns $router
ip netns exec $router ip link set $peerdev mtu 1450 up
# 对 $device 名称做字符串切割，提取 VLAN 标识和前缀：
# ${device##*-}：删除 $device 中最后一个 - 及之前的所有字符，剩余部分赋值给 vlan（例如 device=ext-100，则 vlan=100）；
# ${device%%-*}：删除 $device 中第一个 - 及之后的所有字符，剩余部分赋值给 prefix（例如 device=ext-100，则 prefix=ext）。
vlan=${device##*-}
prefix=${device%%-*}
if [ "$prefix" == "ext" ]; then
# 判断前缀是否为 ext（外部网络）：
# 如果是，执行同目录下的 create_link.sh 脚本，并传入 $vlan 作为参数（推测 create_link.sh 是用于配置该 VLAN 相关的链路 / 网络规则）
    ./create_link.sh $vlan
fi
# 定义网桥名称：拼接 br 和 vlan，例如 vlan=100 则 bridge=br100（Linux 中网桥通常以 br 开头命名）。
bridge=br$vlan
# 将 $device 这个 veth 端口挂载到 $bridge 网桥下；
# 网桥（bridge）类似物理交换机，挂载到网桥的网卡会处于同一二层网络，实现数据转发。
ip link set dev $device master $bridge
