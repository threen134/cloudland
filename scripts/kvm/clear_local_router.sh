#!/bin/bash

# Remove the VPC router of this node (netns, gateway ports, bridges, VXLAN uplinks, DHCP, directory) once
# nothing here uses it any more. Called after an instance is deleted or migrated away (also by the toall
# clear_vm of an instance that never landed, on every node), after a VRRP interface is cleared and when the
# VPC is deleted.
# The router is shared by everything of the VPC on this node: instances, running or shut off (a shut off one
# has no tap on the bridge, but needs the bridge to start again), load balancers and VPN gateways (their VRRP
# NIC is a gateway port of the router, keepalived / haproxy / charon run inside the netns and keep their
# configuration under the router directory). Only the last user takes it down. A transit gateway attachment
# (veth tr-<att>) counts too: the router forwards between the member VPCs even without an instance of its own
# VPC here, and apply_tgw.sh calls this again once it removed the attachment.

cd $(dirname $0)
source ../cloudrc
source ./bridge_lib.sh

[ $# -lt 1 ] && echo "$0 <router> [<instance ID being removed>]" && exit -1

ID=${1#router-}
router=router-$ID
# The instance whose removal calls this: what is left of it (its NIC definitions) does not count
leaving=$2

[[ "$ID" =~ ^[0-9]+$ ]] && [ "$ID" != "0" ] || exit 1

# Checking the users and removing the router is one step: apply_tgw.sh plugs an attachment in under the same lock
exec 8>$(router_lock_file $router)
if ! flock -w 60 8; then
    log_debug $router "clear_local_router.sh: $router kept, its lock is busy"
    exit 0
fi

# The VNIs of the router here: its gateway ports ns-<vni>, and the DHCP directories that stay on disk when
# the netns is gone (after a reboot, before the instances are synced)
vlans=$( { ip netns exec $router ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | sed -n 's/^ns-\([0-9][0-9]*\)\(@.*\)\{0,1\}$/\1/p'
    ls $router_dir/$router 2>/dev/null | grep -xE '[0-9]+'; } | sort -un)

# Prints what still uses the router here, nothing when it is unused
function router_user()
{
    local vlan slaves bridges user
    user=$(router_vrrp_user $router)
    [ -n "$user" ] && echo "$user" && return
    user=$(router_tgw_user $router)
    [ -n "$user" ] && echo "$user" && return
    [ -n "$vlans" ] || return
    for vlan in $vlans; do
        slaves=$(ls -A /sys/devices/virtual/net/br$vlan/brif 2>/dev/null | grep -v "^v-\|^ln-")
        [ -n "$slaves" ] && echo "running instance on br$vlan" && return
    done
    if ! bridges=$(domain_bridges); then
        echo "instances of unknown bridges (libvirt did not answer)"
        return
    fi
    for vlan in $vlans; do
        grep -qx "br$vlan" <<<"$bridges" && echo "instance defined on br$vlan" && return
    done
    # An instance migrating in: its NICs are prepared, its domain comes with the migration
    bridges=$(pending_nic_bridges $leaving)
    for vlan in $vlans; do
        grep -qx "br$vlan" <<<"$bridges" && echo "instance migrating in on br$vlan" && return
    done
}

user=$(router_user)
if [ -n "$user" ]; then
    log_debug $router "clear_local_router.sh: $router kept, used by $user"
    exit 0
fi

for vlan in $vlans; do
    ./clear_link.sh $vlan unused
    apply_vnic -D ln-$vlan
    dnsmasq_pid=$(ps -ef | grep dnsmasq | grep "\<interface=ns-$vlan\>" | awk '{print $2}')
    [ -n "$dnsmasq_pid" ] && kill -9 $dnsmasq_pid
done

ip netns exec $router ip link set lo down
suffix=$ID
ip netns exec router-0 ip link del int-$suffix
ip netns del $router
rm -rf $cache_dir/router/$router

nat_ip=169.$(($NODE_ID % 234)).$(($suffix % 234)).3
route_ip=$(ifconfig $vxlan_interface | grep 'inet ' | awk '{print $2}')
iptables -t nat -D POSTROUTING -s ${nat_ip}/32 -j SNAT --to-source $route_ip
