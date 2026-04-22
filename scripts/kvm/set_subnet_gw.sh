#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 3 ] && echo "$0 <router> <vlan> <gateway>" && exit -1

router=$1
[ "${router/router-/}" = "$router" ] && router=router-$1
vlan=$2
gateway=$3

 # 特殊路由器（router-0）无需配置，直接退出
[ "$router" = "router-0" ] && exit 0

 # 创建 veth 对：ln-$vlan（挂桥上二层）ns-$vlan（路由器命名空间侧，三层）if [ $? -ne 0 ]; then
./create_local_router.sh $router
cat /proc/net/dev | grep -q "^\<ln-$vlan\>"
if [ $? -ne 0 ]; then
    ./create_veth.sh $router ln-$vlan ns-$vlan
    apply_vnic -I ln-$vlan
    mac_map=$(printf "%06x" $vlan)
    hw_addr=52:$(echo $mac_map | cut -c 1-2):$(echo $mac_map | cut -c 3-4):$(echo $mac_map | cut -c 5-6)
    hyper_map=$(printf "%04x" $(($SCI_CLIENT_ID & 0xffff)))
    hw_addr=$hw_addr:$(echo $hyper_map | cut -c 1-2):$(echo $hyper_map | cut -c 3-4)
    if [ $? -eq 0 ]; then
        ip netns exec $router ip link set ns-$vlan address $hw_addr
    fi
fi
# 将 ln-$vlan 接口添加到对应 VLAN 网桥（br$vlan）
brctl addif br$vlan ln-$vlan
# 通过 ipcalc 解析网关 IP，提取网段、广播地址、最小/最大主机 IP
read -r network bcast hostmin hostmax < <(ipcalc $gateway | awk '/^Network:/ {n=$2} /^Broadcast:/ {b=$2} /^HostMin:/ {min=$2} /^HostMax:/ {max=$2} END {print n,b,min,max}')
# 将网段加入 nonat 集合（避免 NAT 转换）
ip netns exec $router ipset add nonat $network
 # 为路由器内 ns-$vlan 接口配置网关 IP + 广播地址
ip netns exec $router ip addr add $gateway brd $bcast dev ns-$vlan
# 路由表配置文件路径
rt_file=/etc/iproute2/rt_tables
 # 筛选以 fip- 为前缀的路由表
tables=$(cat $rt_file | grep fip- | awk '{print $2}')
# 读取系统路由表配置，筛选 fip- 前缀的自定义路由表；
# 为每个有效路由表补充网段路由规则，确保不同路由表下该子网的流量能通过对应虚拟接口转发。
for table in $tables; do
 # 校验路由表是否存在
    ip netns exec $router ip route list table $table
    [ $? -ne 0 ] && continue
     # 为每个路由表添加网段路由（指向对应虚拟接口）
    ip netns exec $router ip -o addr | grep "ns-.* inet " | awk '{print $2, $4}' | while read ns_link ns_gw; do
        ip_net=$(ipcalc -b $ns_gw | grep Network | awk '{print $2}')
        # 为当前路由表添加子网路由规则
        ip netns exec $router ip route add $ip_net dev $ns_link table $table
    done
done
# 调用 set_subnet_dhcp.sh 脚本，将网关、网段、IP 范围等参数传递过去，完成该子网的 DHCP 配置（如分配 IP 池、网关指向等）
./set_subnet_dhcp.sh "$router" "$vlan" "$gateway" "$network" "$hostmin" "$hostmax"
