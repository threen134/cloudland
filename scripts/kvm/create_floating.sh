#!/bin/bash

cd `dirname $0`
source ../cloudrc
source ./fip_lib.sh

[ $# -lt 7 ] && echo "$0 <router> <ext_ip> <ext_gw> <ext_vlan> <int_ip> <int_vlan> <mark_id> <inbound> <outbound>" && exit -1

ID=$1
router=router-$1
ext_cidr=$2
ext_ip=${2%/*}
ext_gw=${3%/*}
ext_vlan=$4
int_addr=$5
int_ip=${int_addr%/*}
int_vlan=$6
mark_id=$(($7 % 2147483647))
inbound=$8
outbound=$9

[ -z "$router" -o "$router" = "router-0" -o  -z "$ext_ip" -o -z "$int_ip" ] && exit 1
ip netns list | grep -q $router
[ $? -ne 0 ] && echo "Router $router does not exist" && exit -1

table=fip-$ext_vlan
ensure_fip_table $ext_vlan
suffix=${ID}-${ext_vlan}
ext_dev=te-$suffix
./create_veth.sh $router ext-$suffix te-$suffix

ip netns exec $router ip addr add $ext_cidr dev $ext_dev
ip netns exec $router ip route replace default via $ext_gw table $table
ip netns exec $router ip -o addr | grep "ns-.* inet " | awk '{print $2, $4}' | while read ns_link ns_gw; do
    ip_net=$(ipcalc -b $ns_gw | grep Network | awk '{print $2}')
    ip netns exec $router ip route add $ip_net dev $ns_link table $table
done
ip netns exec $router ip rule add pref $fip_rule_pref from $int_ip lookup $table
ip netns exec $router ip rule add pref $fip_rule_pref to $int_ip lookup $table
ip netns exec $router iptables -t nat -C PREROUTING -d $ext_ip -j DNAT --to-destination $int_ip
[ $? -ne 0 ] && ip netns exec $router iptables -t nat -I PREROUTING -d $ext_ip -j DNAT --to-destination $int_ip
ip netns exec $router iptables -t nat -C POSTROUTING -s $int_ip -m set ! --match-set nonat dst -j SNAT --to-source $ext_ip
[ $? -ne 0 ] && ip netns exec $router iptables -t nat -I POSTROUTING -s $int_ip -m set ! --match-set nonat dst -j SNAT --to-source $ext_ip
async_exec ip netns exec $router arping -c 1 -A -U -I $ext_dev $ext_ip

# Bandwidth limits (numbering in fip_lib.sh; set_floating_bandwidth.sh changes them later)
fip_instance_limits $ID $ext_ip $ext_vlan $int_vlan $7 $inbound $outbound
