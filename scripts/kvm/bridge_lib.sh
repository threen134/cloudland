#!/bin/bash
# Helpers for tearing down VPC bridges and routers. Source after cloudrc.

# Bridges named in the definition of any instance on this node, running or shut off, one per line, sorted:
# domain_bridges. A shut off instance has no tap on its bridge: only its definition says which bridges it
# needs to start again. Fails when libvirt does not answer or an instance that is still defined cannot be
# read; callers then keep what they would have removed.
function domain_bridges()
{
    local doms dom out err rc list=""
    doms=$(timeout 30 virsh list --all --name 2>/dev/null) || return 1
    for dom in $doms; do
        if out=$(timeout 10 virsh domiflist $dom 2>/dev/null); then
            list="$list
$(awk 'NR > 2 && $3 != "" {print $3}' <<<"$out")"
            continue
        fi
        # Only a domain that went away since the listing may be left out; a timeout (124) or any other error
        # says nothing about its bridges
        err=$(timeout 10 virsh domstate $dom 2>&1 >/dev/null)
        rc=$?
        [ $rc -ne 0 ] && [ $rc -ne 124 ] && grep -qE "failed to get domain|Domain not found" <<<"$err" && continue
        return 1
    done
    awk 'NF' <<<"$list" | sort -u
}

# Bridges of the NICs prepared for an instance whose domain is not defined here yet, one per line:
# pending_nic_bridges [<instance ID to leave out>]. target_migration.sh builds the NICs of an incoming instance
# before its domain arrives with the migration (possibly after the whole disk copy): attach_vm_nic.sh finds no
# domain and leaves the interface definitions in $xml_dir/inst-<N>/tap*.xml. The instance being removed by the
# caller is left out.
function pending_nic_bridges()
{
    local f
    for f in $xml_dir/inst-*/tap*.xml; do
        [ -f "$f" ] || continue
        [ -n "$1" ] && [ "${f%/*}" = "$xml_dir/inst-$1" ] && continue
        sed -n "s/.*<source bridge='\([^']*\)'.*/\1/p" "$f"
    done | sort -u
}

# The transit gateway attachment of router <router> on this node (veth tr-<att>, apply_tgw.sh), nothing when
# none: router_tgw_user <router>. The router then forwards between the VPCs of the gateway even with no instance
# of its own VPC here; apply_tgw.sh removes the veth and calls clear_local_router.sh when the node leaves the gateway.
function router_tgw_user()
{
    local dev
    dev=$(ip netns exec $1 ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | sed -n 's/^\(tr-[0-9][0-9]*\)\(@.*\)\{0,1\}$/\1/p' | head -1)
    [ -n "$dev" ] && echo "transit gateway attachment $dev"
}

# What of a load balancer or VPN gateway uses router <router> on this node, nothing when none:
# router_vrrp_user <router>. Their processes run in the router netns and reach backends and peers through its
# gateway ports; the configuration lives in the router directory.
function router_vrrp_user()
{
    local router=$1 d
    for d in $router_dir/$router/lb-* $router_dir/$router/vrrp-*; do
        [ -d "$d" ] && echo "load balancer ${d##*/}" && return
    done
    # A gateway node (vrrp_id); the other nodes of the VPC only keep routes there
    for d in $router_dir/$router/vpn-*; do
        [ -f "$d/vrrp_id" ] && echo "VPN gateway ${d##*/}" && return
    done
    # The VRRP address of a load balancer or VPN gateway placed here: set_vrrp_ip.sh accepts new connections
    # to it, clear_vrrp_ip.sh removes the rule. A load balancer without floating IP has no directory yet.
    if ip netns exec $router iptables -S INPUT 2>/dev/null | grep -qE '^-A INPUT -d [0-9.]+/32 -m conntrack --ctstate NEW -j ACCEPT$'; then
        echo "VRRP address"
    fi
}

# The dnsmasq of gateway port ns-<vni> of router <router> (each router and VNI has its own, set_subnet_dhcp.sh
# starts it with the directory of the router): router_port_dnsmasq <router> <vni> prints its pids. The VNI alone
# is not enough, it would also match the port of another router
function router_port_dnsmasq()
{
    ps -eo pid=,args= | awk -v i="--interface=ns-$2" -v c="$router_dir/$1/$2/dnsmasq.conf" \
        '{ n = 0; for (k = 2; k <= NF; k++) if ($k == i || $k == c) n++ } n == 2 {print $1}'
}

# Takes gateway port ns-<vni> of router <router> down on this node: clear_router_port <router> <vni>. Its DHCP
# server and directory always go. The rest only when the VNI is the router's here: it has the port, or it has none
# but kept the directory of the VNI while no other router holds ln-<vni> (after a reboot NetworkManager brings the
# bridge back without the port, or without the netns at all). Then the veth ln- / ns-, the FORWARD rules of ln- and
# the bridge and VXLAN uplink of the VNI go (clear_link.sh <vni> unused keeps them while something else uses them;
# it does not count ln-, so this is what keeps the port of another router). A VNI whose ln- has its peer in the
# netns of another router (two VPCs on one VNI: subnets from before the VXLAN ID check) keeps its port, bridge and
# uplink; a peer in a netns without a name (deleted while a process kept it, or still being torn down) does not
# count as another router. The caller holds the router lock and made sure nothing uses the port.
# Shared by clear_local_router.sh, clear_subnet_gw.sh and set_subnet_gw.sh
function clear_router_port()
{
    local router=$1 vlan=$2 pid owned="" peer
    # First: a DHCP server is what keeps a netns without a name alive
    for pid in $(router_port_dnsmasq $router $vlan); do
        kill -9 $pid
    done
    if ip netns exec $router ip link show ns-$vlan >/dev/null 2>&1; then
        owned=yes
    elif [ -d $router_dir/$router/$vlan ]; then
        peer=$(ip -o link show ln-$vlan 2>/dev/null | sed -n 's/.* link-netns \([^ ]*\).*/\1/p')
        [ -z "$peer" -o "$peer" = "$router" ] && owned=yes
    fi
    rm -rf $router_dir/$router/$vlan
    [ -n "$owned" ] || return 0
    ip netns exec $router ip link del ns-$vlan 2>/dev/null
    ./clear_link.sh $vlan unused >/dev/null 2>&1
    apply_vnic -D ln-$vlan
}

# Whether two IPv4 networks overlap: nets_overlap <a/len> <b/len>. One contains the other when both give the
# same network at the shorter prefix
function nets_overlap()
{
    local p=${1#*/}
    [ ${2#*/} -lt $p ] && p=${2#*/}
    [ "$(ipcalc -n -b ${1%/*}/$p | awk '/^Network:/ {print $2}')" = "$(ipcalc -n -b ${2%/*}/$p | awk '/^Network:/ {print $2}')" ]
}

# The VNIs of the other gateway ports of router <router> with an address in a network that overlaps <network>:
# router_overlapping_ports <router> <vni> <network>. clapi refuses overlapping subnets in a VPC, so such a port
# belongs to a subnet deleted while this node did not hear of it (offline, or before clear_subnet_gw.sh existed);
# its connected route would win over the new port
function router_overlapping_ports()
{
    local dev addr
    [[ "$3" == */* ]] || return 0
    ip netns exec $1 ip -o -4 addr show 2>/dev/null | awk -v d="ns-$2" '{ sub(/@.*/, "", $2); if ($2 != d && $2 ~ /^ns-[0-9]+$/) print $2, $4 }' |
        while read dev addr; do
            nets_overlap $addr $3 && echo ${dev#ns-}
        done | sort -u
}
