#!/bin/bash

cd `dirname $0`
source ../cloudrc
source ./bridge_lib.sh

[ $# -lt 1 ] && echo "$0 <vlan> [unused|vrrp]" && exit -1

vlan=$1
# "unused": the caller (clear_local_router.sh) already checked the instance definitions. "vrrp": the VLAN is the
# VRRP subnet of the VPC (clear_vrrp_ip.sh), which a transit gateway never uses (192.168.196.0/24 is thrown out
# of its routes), so only the load balancers and VPN gateways keep it
checked=$2
vm_br=br$vlan
slaves=$(ls -A /sys/devices/virtual/net/$vm_br/brif 2>/dev/null | grep -v "^v-\|^ln-")
[ -n "$slaves" ] && exit 0
# Other users than taps (clear_local_router.sh checked them all already): the gateway port ln- / ns- of a router
# that serves a load balancer or VPN gateway here (backends and peers are reached through it, the VRRP NIC is
# one) or forwards for a transit gateway (the other member VPCs reach this VPC through it), a shut off instance
# (it cannot start again without the bridge), an instance migrating in
if [ "$checked" != "unused" ]; then
    for router in $(ip netns list 2>/dev/null | awk '$1 ~ /^router-[0-9]+$/ && $1 != "router-0" {print $1}'); do
        ip netns exec $router ip link show ns-$vlan >/dev/null 2>&1 || continue
        [ -n "$(router_vrrp_user $router)" ] && exit 0
        [ "$checked" != "vrrp" ] && [ -n "$(router_tgw_user $router)" ] && exit 0
        break
    done
    bridges=$(domain_bridges) || exit 0
    grep -qx "$vm_br" <<<"$bridges" && exit 0
    grep -qx "$vm_br" <<<"$(pending_nic_bridges)" && exit 0
fi
nmcli connection down v-$vlan
nmcli connection del v-$vlan
nmcli connection down ln-$vlan
nmcli connection del ln-$vlan
nmcli connection down $vm_br
nmcli connection del $vm_br
ip link del v-$vlan
ip link del ln-$vlan
ip link del br$vlan
apply_bridge -D $vm_br
