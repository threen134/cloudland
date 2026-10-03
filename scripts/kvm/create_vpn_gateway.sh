#!/bin/bash

# Build (or refresh) the skeleton of a VPN gateway on one of its two VRRP nodes: directories, the public
# port with its policy routing, keepalived holding the floating IPs with the VPN notify hooks, INPUT rules
# for IKE / ESP / WireGuard, MSS clamping and the router sysctls. Idempotent: rerun after any change or
# on recovery. Connections, BGP and WireGuard peers are pushed by their own scripts afterwards.
#
# stdin JSON: {"ha_mode", "vrid", "floating_ip": {"address","vlan","gateway","mark_id","inbound","outbound"},
#              "floating_ips": [{"endpoint", "host", "address", ...}]   every public address, vip1 first
#              "ipsec_enabled", "client_enabled", "client_port", "client_cidr", "wg_address", "wg_private_key",
#              "enabled"}   enabled=false pauses the gateway (vpn_lib.sh vpn_disabled)
# active_standby: every address is a floating IP (host -1) that keepalived holds on the master; a second one
# gives connections a second local address for their standby tunnel.
# active_active: node1 / node2 are fixed addresses, each on the node its host names (on the port for good,
# tunnels from it run on that node whatever the VRRP role); only the client VPN floating IP (vip1) is left
# to keepalived, and without client VPN there is no keepalived at all. floating_ip is vip1 and may be null.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 9 ] && die "$0 <router> <gw_ID> <vrrp_ID> <vrrp_vlan> <local_ip> <local_mac> <peer_ip> <peer_mac> <role>"

ID=$1
router=router-$ID
gw=$2
vrrp_ID=$3
vrrp_vlan=$4
local_ip=$5
local_mac=$6
peer_ip=$7
peer_mac=$8
role=$9

# Every failure below must reach clapi as well (VpnGatewayReady's error branch): die exits non-zero and the
# gateway would otherwise stay pending forever without a word
vpn_gateway_exit() { [ $? -eq 0 ] || echo "|:-COMMAND-:| vpn_gateway.sh '$gw' '$NODE_ID' 'error'"; }
trap vpn_gateway_exit EXIT

content=$(cat)
vpn_json_read "$content" ha_mode=.ha_mode vrid=.vrid ipsec_enabled=.ipsec_enabled client_enabled=.client_enabled client_port=.client_port client_cidr=.client_cidr wg_address=.wg_address enabled=.enabled
vpn_json_read "$content" vip=.floating_ip.address ext_vlan=.floating_ip.vlan ext_gw=.floating_ip.gateway mark_id=.floating_ip.mark_id inbound=.floating_ip.inbound outbound=.floating_ip.outbound
wg_private_key=$(jq -r '.wg_private_key' <<<$content)
[ -z "$ha_mode" -o "$ha_mode" = "null" ] && ha_mode=active_standby
if [ -z "$vip" -o "$vip" = "null" ]; then
    [ "$ha_mode" = "active_active" ] || die "VPN gateway $gw has no floating ip"
    vip=""
fi
[ -z "$vrid" -o "$vrid" = "null" -o "$vrid" = "0" ] && vrid=$vrrp_ID
ext_ip=${vip%/*}
# Every public address as "address vlan gateway mark inbound outbound endpoint host", vip1 first. The
# floating ones (host -1) go to keepalived; the fixed ones whose host is this node stay on the port
floating_addrs=$(jq -r '(.floating_ips // [.floating_ip])[] | select(((.host // -1) | tostring) == "-1") | "\(.address) \(.vlan) \(.gateway) \(.mark_id) \(.inbound) \(.outbound)"' <<<$content)
fixed_addrs=$(jq -r --arg me "$NODE_ID" '(.floating_ips // [])[] | select(((.host // -1) | tostring) == $me) | "\(.address) \(.vlan) \(.gateway) \(.mark_id) \(.inbound) \(.outbound)"' <<<$content)
public_addrs=$(printf '%s\n%s\n' "$floating_addrs" "$fixed_addrs" | awk 'NF')
# The addresses tunnels start from on this node: the floating ones (active_standby) or this node's own
tunnel_addrs=$floating_addrs
[ "$ha_mode" = "active_active" ] && tunnel_addrs=$fixed_addrs

vpn_dir=$(vpn_dir_of $ID $gw)
vrrp_dir=$router_dir/$router/vrrp-$vrrp_ID
mkdir -p $vpn_dir/run $vrrp_dir
chmod 700 $vpn_dir
echo $ha_mode >$vpn_dir/ha_mode
echo $vrrp_ID >$vpn_dir/vrrp_id
echo $vrrp_vlan >$vpn_dir/vrrp_vlan
# vip: the floating IP whose holder is the VRRP master (vpn_holds_vip); none on an active_active gateway
# without client VPN
if [ -n "$ext_ip" ]; then
    echo $ext_ip >$vpn_dir/vip
else
    rm -f $vpn_dir/vip
fi
old_vips=$(cat $vpn_dir/vips 2>/dev/null)
old_addrs=$(cat $vpn_dir/public_addrs 2>/dev/null)
old_floating=$(awk '/virtual_ipaddress/,/}/' $vrrp_dir/keepalived.conf 2>/dev/null | awk '$2 == "dev" {sub("/.*", "", $1); print $1}')
awk '{sub("/.*", "", $1); print $1}' <<<"$public_addrs" | awk 'NF' >$vpn_dir/vips
echo "$public_addrs" >$vpn_dir/public_addrs
echo ${peer_ip%/*} >$vpn_dir/peer_ip
echo $ext_vlan >$vpn_dir/ext_vlan
# The client pool, for the address-based isolation rules (vpn_isolate_pool_rules)
if [ "$client_enabled" = "true" ] && [ -n "$client_cidr" -a "$client_cidr" != "null" ]; then
    echo $client_cidr >$vpn_dir/client_cidr
else
    rm -f $vpn_dir/client_cidr
fi
# Pause switch: remember whether it flips, vpn_notify.sh applies the new state at the end
was_disabled=false
vpn_disabled $vpn_dir && was_disabled=true
if [ "$enabled" = "false" ]; then
    touch $vpn_dir/disabled
else
    rm -f $vpn_dir/disabled
fi
now_disabled=false
vpn_disabled $vpn_dir && now_disabled=true

# The VRRP NIC is normally built by set_vrrp_ip.sh before this script; guard against ordering on recovery
ip netns exec $router ip addr show ns-$vrrp_vlan 2>/dev/null | grep -q "$local_ip" || ./set_vrrp_ip.sh $ID $vrrp_ID $vrrp_vlan $local_mac $local_ip $peer_mac $peer_ip $role >/dev/null 2>&1

# Traffic from instances on other nodes enters the master on ns-<vrrp vlan> while the source route points
# to ns-<vni>: strict reverse path filtering would drop all of it. Redirects are useless noise here.
# Multipath routes (tunnels sharing a connection's traffic) hash flows on addresses and ports.
ip netns exec $router sysctl -qw net.ipv4.conf.all.rp_filter=2 net.ipv4.conf.default.rp_filter=2 \
    net.ipv4.conf.all.send_redirects=0 net.ipv4.conf.default.send_redirects=0 net.ipv4.fib_multipath_hash_policy=1 >/dev/null 2>&1

# Public port and the fip-<vlan> policy routing; ROUTES_FILE is what notify_master replays to restore the
# default route of that table after the floating IP moved
export ROUTES_FILE=$vrrp_dir/routes
export KEEPALIVE_CONF=$vrrp_dir/keepalived.conf
rm -f $ROUTES_FILE
virtual_ips=""
while read addr vlan gateway mark in_bw out_bw; do
    [ -n "$addr" ] || continue
    dev=te-${ID}-${vlan}
    ./create_lb_floating.sh $ID $addr $gateway $vlan $mark $in_bw $out_bw >/dev/null 2>&1
    # Its exit status is that of its last tc cleanup, which fails whenever no bandwidth limit is set: judge by the result
    ip netns exec $router ip link show $dev >/dev/null 2>&1 || die "Failed to build the public port $dev for $addr"
    virtual_ips="$virtual_ips        $addr dev $dev
"
done <<<"$floating_addrs"
# Fixed addresses (active_active): on the port for good, and the default route of their fip table right
# away (it needs the address on the port; a floating IP gets it from notify_master instead)
node_routes=$vpn_dir/node_routes
rm -f $node_routes
while read addr vlan gateway mark in_bw out_bw; do
    [ -n "$addr" ] || continue
    dev=te-${ID}-${vlan}
    ROUTES_FILE=$node_routes ./create_lb_floating.sh $ID $addr $gateway $vlan $mark $in_bw $out_bw >/dev/null 2>&1
    ip netns exec $router ip link show $dev >/dev/null 2>&1 || die "Failed to build the public port $dev for $addr"
    ip netns exec $router ip addr replace $addr dev $dev
    ip netns exec $router arping -c 1 -A -U -I $dev ${addr%/*} >/dev/null 2>&1 &
done <<<"$fixed_addrs"
if [ -f $node_routes ]; then
    sort -u $node_routes | while read line; do
        [ -n "$line" ] && ip netns exec $router sh -c "$line" >/dev/null 2>&1
    done
fi
# A public address that is not used on this node any more: nothing of it may stay on the port, nor its policy
# routing and bandwidth rules. Called once keepalived runs the new configuration: a floating IP is
# keepalived's to take off, and one deleted under it makes the master leave the MASTER state and drop every
# other address too (the gateway goes down until a new election)
clear_stale_addrs()
{
    local addr dev full vlan mark i
    for addr in $old_vips; do
        grep -qxF $addr $vpn_dir/vips && continue
        if grep -qxF $addr <<<"$old_floating"; then
            for i in {1..12}; do
                ip netns exec $router ip -o addr show | awk -v a=$addr '$4 ~ "^"a"/"' | grep -q . || break
                sleep 0.25
            done
        fi
        for dev in $(ip netns exec $router ip -o addr show | awk -v a=$addr '$4 ~ "^"a"/" {print $2}'); do
            ip netns exec $router ip addr del $(ip netns exec $router ip -o addr show dev $dev | awk -v a=$addr '$4 ~ "^"a"/" {print $4}') dev $dev >/dev/null 2>&1
        done
        read full vlan _ mark _ < <(awk -v a=$addr '{split($1, p, "/")} p[1] == a' <<<"$old_addrs")
        [ -n "$vlan" ] && ./clear_lb_floating.sh $ID $full $vlan $mark >/dev/null 2>&1
    done
}

# INPUT rules keyed on the floating IPs: rebuild them from scratch (the router netns drops by default). A
# paused gateway accepts nothing: peers and clients see no answer at all
for addr in $(echo $old_vips $(cat $vpn_dir/vips) | tr ' ' '\n' | sort -u); do
    for num in $(ip netns exec $router iptables -n -L INPUT --line-numbers | grep "\<$addr\>" | awk '{print $1}' | sort -nr); do
        ip netns exec $router iptables -D INPUT $num
    done
done
if [ "$ipsec_enabled" = "true" ] && [ "$now_disabled" = "false" ]; then
    for addr in $(awk '{sub("/.*", "", $1); print $1}' <<<"$tunnel_addrs"); do
        ip netns exec $router iptables -A INPUT -p udp -d $addr --dport 500 -j ACCEPT
        ip netns exec $router iptables -A INPUT -p udp -d $addr --dport 4500 -j ACCEPT
        # peers without NAT send plain ESP; conntrack would only let it through after we sent first
        ip netns exec $router iptables -A INPUT -p esp -d $addr -j ACCEPT
    done
fi
if [ "$client_enabled" = "true" ] && [ "$now_disabled" = "false" ] && [ -n "$ext_ip" ]; then
    ip netns exec $router iptables -A INPUT -p udp -d $ext_ip --dport $client_port -j ACCEPT
fi
# TCP inside the tunnels: MSS that fits the tunnel MTU in both directions (vpn_lib.sh)
vpn_mss_rules $router add
# VPN clients and remote sites must never reach each other through the gateway, in either direction:
# whatever the routes (client AllowedIPs, BGP-learned prefixes, a local network list that covers the
# client pool) a packet between a WireGuard and an IPsec interface is dropped. Rules go first in FORWARD
# so that no later ACCEPT (nonat, router defaults) can let it through; both nodes carry them.
vpn_isolate_rules $router add
vpn_isolate_pool_rules $router $vpn_dir add

# strongswan.conf: private vici socket and log; the pid file path is compiled in, which is why charon is
# started with a private mount over /run (vpn_lib.sh). Routes are never installed by charon.
# Retransmissions: 3 retries starting at 1.5 s (1.5 + 2.1 + 2.9 + 4.1 s) declare an unresponsive peer in
# about 11 s after the liveness check, instead of about 165 s with the defaults (5 retries from 4 s)
cat >$vpn_dir/strongswan.conf <<EOF
# Generated by CloudLand for VPN gateway $gw
charon {
    load_modular = yes
    install_routes = no
    install_virtual_ip = no
    retransmit_tries = 3
    retransmit_timeout = 1.5
    retransmit_base = 1.4
    plugins {
        include /etc/strongswan.d/charon/*.conf
        vici {
            socket = unix://$vpn_dir/run/charon.vici
        }
    }
    filelog {
        charon {
            path = $vpn_dir/charon.log
            time_format = %b %e %T
            ike_name = yes
            default = 1
        }
    }
}
EOF

# WireGuard: the interface exists on both nodes (no daemon, no state to fail over); peers via wg.conf
wg_dev=wg-$gw
if [ "$client_enabled" = "true" ] && [ -n "$wg_private_key" -a "$wg_private_key" != "null" ]; then
    echo $wg_address >$vpn_dir/wg_address
    ip netns exec $router ip link show $wg_dev >/dev/null 2>&1 || ip netns exec $router ip link add $wg_dev type wireguard
    umask 077
    printf '%s\n' "$wg_private_key" >$vpn_dir/wg.key
    umask 022
    ip netns exec $router wg set $wg_dev private-key $vpn_dir/wg.key listen-port $client_port
    ip netns exec $router ip addr replace $wg_address dev $wg_dev
    if [ "$now_disabled" = "true" ]; then
        ip netns exec $router ip link set $wg_dev mtu 1390 down
    else
        ip netns exec $router ip link set $wg_dev mtu 1390 up
    fi
else
    ip netns exec $router ip link del $wg_dev >/dev/null 2>&1
    rm -f $vpn_dir/wg.key $vpn_dir/wg.conf $vpn_dir/wg_address
fi

# keepalived: same skeleton as the load balancer (BACKUP + nopreempt, priority by role) but with the VPN
# notify hooks. notify_master restores the fip route table before anything else (vpn_notify.sh).
# An active_active gateway without client VPN has no floating IP: no keepalived (check_lb_process.sh
# restarts one only where a keepalived.conf exists)
if [ -z "$virtual_ips" ]; then
    (
        flock 9
        lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf && kill $(cat $vrrp_dir/keepalived.pid) 2>/dev/null
        rm -f $vrrp_dir/keepalived.conf $vrrp_dir/keepalived.pid
    ) 9>$lb_lock_file
    clear_stale_addrs
    ./vpn_notify.sh $gw sync >/dev/null 2>&1
    echo "|:-COMMAND-:| vpn_gateway.sh '$gw' '$NODE_ID' 'ready'"
    exit 0
fi
priority=100
[ "$role" = "MASTER" ] && priority=110
cat >$vrrp_dir/keepalived.conf.new <<EOF
vrrp_instance vpn_gateway_${vrrp_ID} {
    state BACKUP
    interface ns-$vrrp_vlan
    virtual_router_id ${vrid}
    priority $priority
    advert_int 1
    nopreempt

    unicast_src_ip ${local_ip%/*}
    unicast_peer {
        ${peer_ip%/*}
    }

    authentication {
        auth_type PASS
        auth_pass 123456
    }

    virtual_ipaddress {
$virtual_ips    }
    notify_master "$PWD/vpn_notify.sh $gw master"
    notify_backup "$PWD/vpn_notify.sh $gw backup"
    notify_fault "$PWD/vpn_notify.sh $gw fault"
}
EOF
(
    flock 9
    mv -f $vrrp_dir/keepalived.conf.new $vrrp_dir/keepalived.conf
    if lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf; then
        kill -HUP $(cat $vrrp_dir/keepalived.pid)
    else
        start_keepalived $router $vrrp_dir
    fi
) 9>$lb_lock_file
for i in {1..12}; do
    lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf && break
    sleep 0.25
done
clear_stale_addrs
lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf || die "keepalived did not start for VPN gateway $gw"

# Pause switch flipped: stop or start the tunnel processes and move the routes now, by the current role
# (keepalived only calls the notify script on a role change). An active_active node always does: its
# tunnels do not wait for a role
if [ "$was_disabled" != "$now_disabled" ] || [ "$ha_mode" = "active_active" ]; then
    ./vpn_notify.sh $gw sync >/dev/null 2>&1
fi

echo "|:-COMMAND-:| vpn_gateway.sh '$gw' '$NODE_ID' 'ready'"
