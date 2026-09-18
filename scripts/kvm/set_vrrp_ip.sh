#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 8 ] && die "$0 <router> <vrrp_ID> <vrrp_vlan> <local_mac> <local_ip> <peer_mac> <peer_ip> <role> [reply]"

router=$1
[ "${router/router-/}" = "$router" ] && router=router-$1
vrrp_ID=$2
vrrp_vlan=$3
local_mac=$4
local_ip=$5
peer_mac=$6
peer_ip=$7
role=$8
reply=$9

./create_local_router.sh $router
./create_link.sh $vrrp_vlan
cat /proc/net/dev | grep -q "^\<ln-$vrrp_vlan\>"
if [ $? -ne 0 ]; then
    ./create_veth.sh $router ln-$vrrp_vlan ns-$vrrp_vlan
    apply_vnic -I ln-$vrrp_vlan
    ip netns exec $router ip link set ns-$vrrp_vlan address $local_mac
else
    cur_mac=$(ip netns exec $router cat /sys/class/net/ns-$vrrp_vlan/address)
    if [ "$cur_mac" = "$(subnet_gw_mac $vrrp_vlan)" ]; then
        # 设备只是被 add_fwrule.sh → set_subnet_gw.sh 当作普通网关口建出来的（节点重启后 launch_vm sync 下发的转发条目
        # 可能先于负载均衡恢复到达），还不是 VRRP 网卡：改成数据库记录的 MAC，否则对端按该 MAC 写的转发条目收不到本端，双主
        ip netns exec $router ip link set ns-$vrrp_vlan address $local_mac
    else
        # 同一 VPC 的多个负载均衡共用 VRRP 子网网卡，已被其他 VRRP 实例使用时沿用其 MAC（回调会把实际 MAC 写回数据库）
        local_mac=$cur_mac
    fi
fi
brctl addif br$vrrp_vlan ln-$vrrp_vlan
ip netns exec $router ip addr add $local_ip dev ns-$vrrp_vlan
read -d'\n' -r network < <(ipcalc -nb $local_ip | awk '/Network/ {print $2}')
ip netns exec $router ipset add nonat $network
ns_ip=${local_ip%/*}
prefix=${local_ip#*/}
ip netns exec $router iptables -C INPUT -d $ns_ip -m conntrack --ctstate NEW -j ACCEPT
[ $? -ne 0 ] && ip netns exec $router iptables -A INPUT -d $ns_ip -m conntrack --ctstate NEW -j ACCEPT
read -d'\n' -r gateway < <(ipcalc -nb $local_ip | awk '/HostMin/ {print $2}')
./set_subnet_gw.sh $router $vrrp_vlan $gateway/$prefix

if [ "$reply" = "true" ]; then
    echo "|:-COMMAND-:| $(basename $0) '$vrrp_ID' '$NODE_ID' '$role' '$local_mac'"
fi
