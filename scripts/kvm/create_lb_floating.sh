#!/bin/bash

cd `dirname $0`
source ../cloudrc
source ./fip_lib.sh

[ $# -lt 7 ] && echo "$0 <router> <ext_ip> <ext_gw> <ext_vlan> <mark_id> <inbound> <outbound>" && exit -1

ID=$1
router=router-$1
ext_cidr=$2
ext_ip=${2%/*}
ext_gw=${3%/*}
ext_vlan=$4
mark_id=$(($5 % 2147483647))
inbound=$6
outbound=$7

[ -z "$router" -o "$router" = "router-0" -o  -z "$ext_ip" ] && exit 1
ip netns list | grep -q $router
[ $? -ne 0 ] && echo "Router $router does not exist" && exit -1

table=fip-$ext_vlan
ensure_fip_table $ext_vlan
suffix=${ID}-${ext_vlan}
ext_dev=te-$suffix
./create_veth.sh $router ext-$suffix te-$suffix

routes_file=$ROUTES_FILE
echo "ip route replace default via $ext_gw table $table" >>$routes_file
ip netns exec $router ip route replace default via $ext_gw table $table
ip netns exec $router ip -o addr | grep "ns-.* inet " | awk '{print $2, $4}' | while read ns_link ns_gw; do
    ip_net=$(ipcalc -b $ns_gw | grep Network | awk '{print $2}')
    ip netns exec $router ip route add $ip_net dev $ns_link table $table
done
ip netns exec $router ip rule del from $ext_ip lookup $table
ip netns exec $router ip rule add pref $fip_rule_pref from $ext_ip lookup $table
ip netns exec $router ip rule del to $ext_ip lookup $table
ip netns exec $router ip rule add pref $fip_rule_pref to $ext_ip lookup $table

# Bandwidth limits (numbering in fip_lib.sh): inbound on the host side of the port, outbound on te-
fip_vip_limits $ID $ext_ip $ext_vlan $5 $inbound $outbound
