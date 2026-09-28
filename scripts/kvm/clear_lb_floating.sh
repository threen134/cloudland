#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 4 ] && echo "$0 <router> <ext_ip> <ext_vlan> <mark_id>" && exit -1

ID=$1
router=router-$1
ext_addr=$2
ext_ip=${2%/*}
ext_vlan=$3
mark_id=$(($4 % 2147483647))

[ -z "$router" -o -z "$ext_ip" ] && exit 1

table=fip-$ext_vlan
ext_dev=te-$ID-$ext_vlan
# Line numbers of INPUT only: numbers from the other chains would delete unrelated INPUT rules
for num in $(ip netns exec $router iptables -n -L INPUT --line-numbers | grep "\<$ext_ip\>" | awk '{print $1}' | sort -nr); do
    ip netns exec $router iptables -D INPUT $num
done
ip netns exec $router ip rule del from $ext_ip lookup $table
ip netns exec $router ip rule del to $ext_ip lookup $table
ip netns exec $router ip addr del $ext_addr dev $ext_dev
# The port is shared by every floating IP of this router in this VLAN (other load balancers, a VPN gateway).
# A missing address says nothing on a backup node, where keepalived holds none of them: the port stays while
# another floating IP still has its policy rules here, or keepalived of that one faults on the deleted port
if ! ip netns exec $router ip addr show $ext_dev | grep -q 'inet ' && ! ip netns exec $router ip rule | grep -qw "lookup $table"; then
    ip netns exec $router ip link del $ext_dev
fi
ip netns exec $router tc filter del dev $ext_dev protocol ip parent 1:0 prio $mark_id u32 match ip dst $ext_ip/32 flowid 1:$mark_id
ip netns exec $router tc class del dev $ext_dev parent 1: classid 1:$mark_id
let mark_id=$mark_id+2147483647
ip netns exec $router tc filter del dev $ext_dev protocol ip parent 1:0 prio $mark_id u32 match ip src $ext_ip/32 flowid 1:$mark_id
ip netns exec $router tc class del dev $ext_dev parent 1: classid 1:$mark_id
exit 0
