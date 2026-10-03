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
