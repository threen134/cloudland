#!/bin/bash

cd `dirname $0`
source ../cloudrc
source ./fip_lib.sh

[ $# -lt 5 ] && echo "$0 <router> <ext_ip> <int_ip> <int_vlan> <mark_id>" && exit -1

ID=$1
router=router-$1
ext_addr=$2
ext_ip=${2%/*}
int_ip=${3%/*}
int_vlan=$4
# The floating IP id, as create_floating.sh derives its mark (the internal address was used here: an
# arithmetic error, no mark, the MARK rule and the tc classes of the floating IP were never removed)
mark_id=$(($5 % 2147483647))

[ -z "$router" -o -z "$ext_ip" -o -z "$int_ip" ] && exit 1

ext_dev=$(ip netns exec $router ip -o addr | grep -F " $ext_ip/" | awk '{print $2}' | head -1)
table=fip-${ext_dev##*-}
ip netns exec $router ip rule del from $int_ip lookup $table
ip netns exec $router ip rule del to $int_ip lookup $table
ip netns exec $router ip addr del $ext_addr dev $ext_dev
ip netns exec $router iptables -t nat -D PREROUTING -d $ext_ip -j DNAT --to-destination $int_ip
ip netns exec $router iptables -t nat -D POSTROUTING -s $int_ip -m set ! --match-set nonat dst -j SNAT --to-source $ext_ip
# Bandwidth limits and the mark (fip_lib.sh), while the external port is still there; its VLAN comes from the
# port holding the address (none when the address is not here)
ext_vlan=-
[ -n "$ext_dev" ] && ext_vlan=${ext_dev##*-}
fip_instance_limits $ID $ext_ip $ext_vlan $int_vlan $5 0 0
ip netns exec $router iptables -S | grep -E "mark $(printf "0x%x" $mark_id)( |/|$)" | while read line; do
    echo $line | cut -d' ' -f2- | xargs ip netns exec $router iptables -D
done
# The port is shared by every floating IP of this router in this VLAN: other instances, load balancers, a VPN
# gateway. On the backup node of a load balancer or gateway keepalived holds none of its addresses, so an
# address-less port can still be in use: it goes only when no policy rule uses the table any more
# (clear_lb_floating.sh does the same)
if [ -n "$ext_dev" ] && ! ip netns exec $router ip addr show $ext_dev | grep -q 'inet ' && ! ip netns exec $router ip rule | grep -qw "lookup $table"; then
    ip netns exec $router ip link del $ext_dev
fi
exit 0
