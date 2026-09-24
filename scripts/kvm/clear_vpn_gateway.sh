#!/bin/bash

# Tear a VPN gateway down on one of its VRRP nodes: processes, tunnel interfaces, routes, nonat entries,
# INPUT rules, keepalived, the public port and every file. The other nodes only carried routes and nonat
# entries, which set_vpn_route.sh with an empty prefix list removes.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 2 ] && die "$0 <router> <gw_ID> [<ext_ip> <ext_vlan> <mark_id>]"

ID=$1
router=router-$ID
gw=$2
ext_ip=$3
ext_vlan=$4
mark_id=$5
vpn_dir=$(vpn_dir_of $ID $gw)
ns=$(vpn_frr_ns $gw)

exec 9>$lb_lock_file
flock 9

vpn_stop_charon $vpn_dir 2>/dev/null
vpn_stop_frr $gw 2>/dev/null
rm -rf /etc/frr/$ns

if [ -f /var/run/netns/$router ]; then
    for cidr in $(cat $vpn_dir/routes.installed $vpn_dir/routes.current 2>/dev/null); do
        ip netns exec $router ip route del $cidr >/dev/null 2>&1
    done
    for cidr in $(cat $vpn_dir/nonat.current 2>/dev/null); do
        ip netns exec $router ipset del nonat $cidr 2>/dev/null
    done
    for rule in $(cat $vpn_dir/bgp_rules 2>/dev/null); do
        ip netns exec $router iptables -D INPUT -p tcp -s ${rule%>*} -d ${rule#*>} --dport 179 -j ACCEPT 2>/dev/null
    done
    for dev in $(cat $vpn_dir/ifaces 2>/dev/null) wg-$gw; do
        ip netns exec $router ip link del $dev >/dev/null 2>&1
    done
    # MSS clamping and isolation match every ipsec+ / wg+ device of the router: keep them while another
    # gateway directory exists here (a replacement created before this clear ran)
    if ! vpn_router_has_other $router $vpn_dir; then
        for dev in ipsec+ wg+; do
            for dir in -i -o; do
                ip netns exec $router iptables -t mangle -D FORWARD $dir $dev -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null
            done
        done
        vpn_isolate_rules $router del
    fi
    vip=$(cat $vpn_dir/vip 2>/dev/null)
    [ -n "$vip" ] || vip=${ext_ip%/*}
    if [ -n "$vip" -a "$vip" != "-" ]; then
        for num in $(ip netns exec $router iptables -n -L INPUT --line-numbers | grep "\<$vip\>" | awk '{print $1}' | sort -nr); do
            ip netns exec $router iptables -D INPUT $num
        done
    fi
fi

# keepalived and its directory
vrrp_ID=$(cat $vpn_dir/vrrp_id 2>/dev/null)
if [ -n "$vrrp_ID" ]; then
    vrrp_dir=$router_dir/$router/vrrp-$vrrp_ID
    if lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf; then
        kill $(cat $vrrp_dir/keepalived.pid) 2>/dev/null
        sleep 1
    fi
    rm -rf $vrrp_dir
fi

# The public port: address, policy routing and the veth when nothing else uses it
if [ -n "$ext_ip" -a "$ext_ip" != "-" ] && [ -f /var/run/netns/$router ]; then
    ip netns exec $router ip addr del $ext_ip dev te-$ID-$ext_vlan >/dev/null 2>&1
    ./clear_lb_floating.sh $ID $ext_ip $ext_vlan $mark_id >/dev/null 2>&1
fi

rm -rf $vpn_dir
exit 0
