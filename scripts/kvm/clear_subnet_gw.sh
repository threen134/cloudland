#!/bin/bash

# Remove what a deleted subnet left in the VPC router of this node. clapi sends it to every node once the deletion
# of a subnet of a VPC (an internal subnet or the VRRP subnet) is committed: the gateway port is per node and a node
# keeps it for as long as its router lives, also a node that only routes through it (the VRRP subnet of a VPN
# gateway, a transit gateway member). A port left behind keeps the connected route of the network, which wins over
# the port of a subnet created again with the same CIDR, so the new subnet is unreachable on this node.
#   port:    no other subnet uses the VNI. The port goes with its DHCP server and directory, the bridge and the VXLAN
#            uplink (clear_router_port), then the router itself if nothing else uses it here (clear_local_router.sh)
#   address: another subnet of the VPC still uses the VNI. Only the gateway address of the network goes from the port
# A node without the port does nothing. A node that misses this (offline) keeps the port until a subnet with an
# overlapping network gets a port here (set_subnet_gw.sh removes it then) or the router goes

cd $(dirname $0)
source ../cloudrc
source ./bridge_lib.sh

[ $# -lt 5 ] && echo "$0 <router> <vlan> <network> <gateway> port|address" && exit -1

ID=${1#router-}
router=router-$ID
vlan=$2
network=$3
gateway=$4
mode=$5

[[ "$ID" =~ ^[0-9]+$ ]] && [ "$ID" != "0" ] && [[ "$vlan" =~ ^[0-9]+$ ]] || exit 1
[ "$mode" = port -o "$mode" = address ] || exit 1

# The same lock as clear_local_router.sh and apply_tgw.sh: they change the ports of the router too
exec 8>$(router_lock_file $router)
flock -w 60 8 || die "$router: lock busy, the gateway port of VNI $vlan is left"

had_port=""
ip netns exec $router ip link show ns-$vlan >/dev/null 2>&1 && had_port=yes
if [ "$mode" = address ]; then
    if [ -n "$had_port" ]; then
        ip netns exec $router ip addr del $gateway dev ns-$vlan 2>/dev/null
        for table in $(awk '/fip-/ {print $2}' $rt_file 2>/dev/null); do
            ip netns exec $router ip route del $network dev ns-$vlan table $table 2>/dev/null
        done
    fi
    # Its DHCP range; the server of the VNI reads it at the next start
    rm -f $router_dir/$router/$vlan/dnsmasq.conf.d/dhcp_$(echo $network | tr -s './' '_').conf
elif [ -n "$had_port" ]; then
    clear_router_port $router $vlan
else
    # No port here: only what the router left of the VNI. The bridge and uplink of the VNI are not its own then
    # (another router, or none: this node may never have had the subnet)
    for pid in $(router_port_dnsmasq $router $vlan); do
        kill -9 $pid
    done
    rm -rf $router_dir/$router/$vlan
fi
if [ -n "$had_port" ]; then
    ip netns exec $router ipset del nonat $network 2>/dev/null
    log_debug $router "clear_subnet_gw.sh: $network removed from VNI $vlan ($mode)"
fi
exec 8>&-

# The router may have had only this port here (a node routing to a VPN gateway, the last load balancer of the VPC)
[ "$mode" = port ] && [ -n "$had_port" ] && ./clear_local_router.sh $router 2>/dev/null | grep '^|:-COMMAND-:|'
exit 0
