#!/bin/bash

# Build (or refresh) the skeleton of a VPN gateway on one of its two VRRP nodes: directories, the public
# port with its policy routing, keepalived holding the floating IP with the VPN notify hooks, INPUT rules
# for IKE / ESP / WireGuard, MSS clamping and the router sysctls. Idempotent: rerun after any change or
# on recovery. Connections, BGP and WireGuard peers are pushed by their own scripts afterwards.
#
# stdin JSON: {"vrid", "floating_ip": {"address","vlan","gateway","mark_id","inbound","outbound"},
#              "ipsec_enabled", "client_enabled", "client_port", "client_cidr", "wg_address", "wg_private_key",
#              "enabled"}   enabled=false pauses the gateway (vpn_lib.sh vpn_disabled)

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
vpn_json_read "$content" vrid=.vrid ipsec_enabled=.ipsec_enabled client_enabled=.client_enabled client_port=.client_port client_cidr=.client_cidr wg_address=.wg_address enabled=.enabled
vpn_json_read "$content" vip=.floating_ip.address ext_vlan=.floating_ip.vlan ext_gw=.floating_ip.gateway mark_id=.floating_ip.mark_id inbound=.floating_ip.inbound outbound=.floating_ip.outbound
wg_private_key=$(jq -r '.wg_private_key' <<<$content)
[ -z "$vip" -o "$vip" = "null" ] && die "VPN gateway $gw has no floating ip"
[ -z "$vrid" -o "$vrid" = "null" -o "$vrid" = "0" ] && vrid=$vrrp_ID
ext_ip=${vip%/*}

vpn_dir=$(vpn_dir_of $ID $gw)
vrrp_dir=$router_dir/$router/vrrp-$vrrp_ID
mkdir -p $vpn_dir/run $vrrp_dir
chmod 700 $vpn_dir
echo $vrrp_ID >$vpn_dir/vrrp_id
echo $vrrp_vlan >$vpn_dir/vrrp_vlan
echo $ext_ip >$vpn_dir/vip
echo ${peer_ip%/*} >$vpn_dir/peer_ip
echo $ext_vlan >$vpn_dir/ext_vlan
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
ip netns exec $router sysctl -qw net.ipv4.conf.all.rp_filter=2 net.ipv4.conf.default.rp_filter=2 \
    net.ipv4.conf.all.send_redirects=0 net.ipv4.conf.default.send_redirects=0 >/dev/null 2>&1

# Public port and the fip-<vlan> policy routing; ROUTES_FILE is what notify_master replays to restore the
# default route of that table after the floating IP moved
export ROUTES_FILE=$vrrp_dir/routes
export KEEPALIVE_CONF=$vrrp_dir/keepalived.conf
rm -f $ROUTES_FILE
suffix=${ID}-${ext_vlan}
ext_dev=te-$suffix
./create_lb_floating.sh $ID $vip $ext_gw $ext_vlan $mark_id $inbound $outbound >/dev/null 2>&1
# Its exit status is that of its last tc cleanup, which fails whenever no bandwidth limit is set: judge by the result
ip netns exec $router ip link show $ext_dev >/dev/null 2>&1 || die "Failed to build the public port $ext_dev for $vip"

# INPUT rules keyed on the floating IP: rebuild them from scratch (the router netns drops by default). A
# paused gateway accepts nothing: peers and clients see no answer at all
for num in $(ip netns exec $router iptables -n -L INPUT --line-numbers | grep "\<$ext_ip\>" | awk '{print $1}' | sort -nr); do
    ip netns exec $router iptables -D INPUT $num
done
if [ "$ipsec_enabled" = "true" ] && [ "$now_disabled" = "false" ]; then
    ip netns exec $router iptables -A INPUT -p udp -d $ext_ip --dport 500 -j ACCEPT
    ip netns exec $router iptables -A INPUT -p udp -d $ext_ip --dport 4500 -j ACCEPT
    # peers without NAT send plain ESP; conntrack would only let it through after we sent first
    ip netns exec $router iptables -A INPUT -p esp -d $ext_ip -j ACCEPT
fi
if [ "$client_enabled" = "true" ] && [ "$now_disabled" = "false" ]; then
    ip netns exec $router iptables -A INPUT -p udp -d $ext_ip --dport $client_port -j ACCEPT
fi
# TCP inside the tunnels: clamp the MSS to the tunnel MTU (1360 for IPsec, 1390 for WireGuard)
for dev in ipsec+ wg+; do
    for dir in -i -o; do
        ip netns exec $router iptables -t mangle -C FORWARD $dir $dev -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null ||
            ip netns exec $router iptables -t mangle -A FORWARD $dir $dev -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu
    done
done
# VPN clients and remote sites must never reach each other through the gateway, in either direction:
# whatever the routes (client AllowedIPs, BGP-learned prefixes, a local network list that covers the
# client pool) a packet between a WireGuard and an IPsec interface is dropped. Rules go first in FORWARD
# so that no later ACCEPT (nonat, router defaults) can let it through; both nodes carry them.
vpn_isolate_rules $router add

# strongswan.conf: private vici socket and log; the pid file path is compiled in, which is why charon is
# started with a private mount over /run (vpn_lib.sh). Routes are never installed by charon.
cat >$vpn_dir/strongswan.conf <<EOF
# Generated by CloudLand for VPN gateway $gw
charon {
    load_modular = yes
    install_routes = no
    install_virtual_ip = no
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
        $vip dev $ext_dev
    }
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
lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf || die "keepalived did not start for VPN gateway $gw"

# Pause switch flipped: stop or start the tunnel processes and move the routes now, by the current role
# (keepalived only calls the notify script on a role change)
if [ "$was_disabled" != "$now_disabled" ]; then
    ./vpn_notify.sh $gw sync >/dev/null 2>&1
fi

echo "|:-COMMAND-:| vpn_gateway.sh '$gw' '$NODE_ID' 'ready'"
