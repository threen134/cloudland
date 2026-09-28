#!/bin/bash

# Install the networks reachable through a VPN gateway on this node's router netns. Declarative: the JSON
# carries the whole desired set, the node reconciles against what it installed last time.
#
# Every node hosting the VPC gets the nonat entries (otherwise traffic to the remote side is SNATed on its
# way to router-0). Routes depend on the role of the node (plan §6.3 ownership table):
#   - a VRRP member only records tunnel_routes and lets vpn_notify.sh install them by role (on an
#     active_active gateway FRR owns the tunnel prefixes: their target is "frr")
#   - any other node routes every prefix through the gateway nodes over ns-<vrrp vlan>: the first one of
#     next_hops it can reach, or with ecmp all the reachable ones. vpn_nexthop_watch.sh probes the gateway
#     nodes (host_ips) and moves the routes within a second when one stops answering (plan §8.2, F7)
#
# stdin JSON: {"ha_mode", "vrrp_nodes":[..], "master_ip", "vrrp_ips": {"<hostid>": "ip"}, "vrrp_vlan", "vrrp_gateway",
#              "host_ips": {"<vrrp ip>": "<node ip>"}, "prefixes": [{"cidr","type","target","next_hops":[],"ecmp"}]}

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 2 ] && die "$0 <router> <gw_ID>"

ID=$1
router=router-$ID
gw=$2
[ -f /var/run/netns/$router ] || exit 0
vpn_dir=$(vpn_dir_of $ID $gw)
mkdir -p $vpn_dir

content=$(cat)
vpn_json_read "$content" master_ip=.master_ip vrrp_vlan=.vrrp_vlan vrrp_gateway=.vrrp_gateway
vrrp_nodes=$(jq -r '.vrrp_nodes[]?' <<<$content)
is_vrrp=false
for node in $vrrp_nodes; do
    [ "$node" = "$NODE_ID" ] && is_vrrp=true
done
prefixes=$(jq -r '.prefixes[] | "\(.cidr) \(.type) \(.target)"' <<<$content)
# one line, space separated: the membership checks below match " $cidr " inside " $desired "
desired=$(echo "$prefixes" | awk '{print $1}' | tr '\n' ' ' | sed 's/ *$//')

# The VRRP subnet port normally arrives with the fdb entries of the VRRP NICs; build it if it is missing
# so the nexthop is reachable regardless of dispatch order
if [ -n "$vrrp_vlan" -a "$vrrp_vlan" != "null" -a "$vrrp_vlan" != "0" ]; then
    ip netns exec $router ip link show ns-$vrrp_vlan >/dev/null 2>&1 || {
        [ -n "$vrrp_gateway" -a "$vrrp_gateway" != "null" ] && ./set_subnet_gw.sh $ID $vrrp_vlan $vrrp_gateway >/dev/null 2>&1
    }
fi

# nonat: remote networks must not be SNATed, and traffic from them may reach the router itself
current=$(cat $vpn_dir/nonat.current 2>/dev/null)
for cidr in $desired; do
    ip netns exec $router ipset add nonat $cidr -exist
done
for cidr in $current; do
    case " $desired " in
    *" $cidr "*) ;;
    *) ip netns exec $router ipset del nonat $cidr 2>/dev/null ;;
    esac
done
echo $desired >$vpn_dir/nonat.current

if [ "$is_vrrp" = "true" ]; then
    # A node that routed this gateway's prefixes through the gateway nodes before it became one of them
    # (the BACKUP added to a single-node gateway, plan §18.15) drops that state, or vpn_nexthop_watch.sh
    # keeps re-pointing the prefixes at the gateway nodes over the routes of its role. All three files go
    # together under the watcher's lock: with routes.current left alone, the next vpn_apply_nexthops would
    # delete the very prefixes vpn_notify.sh installs. The routes themselves are replaced by vpn_notify.sh.
    (
        flock 9
        rm -f $vpn_dir/nexthops $vpn_dir/nexthop_hosts $vpn_dir/routes.current
    ) 9>$vpn_nexthop_lock
    # Record, do not install: the role decides (vpn_notify.sh)
    peer=""
    for node in $vrrp_nodes; do
        [ "$node" != "$NODE_ID" ] && peer=$(jq -r ".vrrp_ips[\"$node\"] // empty" <<<$content)
    done
    [ -n "$peer" ] && echo $peer >$vpn_dir/peer_ip
    echo $vrrp_vlan >$vpn_dir/vrrp_vlan
    echo "$prefixes" | awk 'NF == 3' >$vpn_dir/tunnel_routes.new
    mv -f $vpn_dir/tunnel_routes.new $vpn_dir/tunnel_routes
    ./vpn_notify.sh $gw sync >/dev/null 2>&1
    exit 0
fi

# Other nodes: every prefix through the gateway nodes (master_ip for a clapi that sends no next_hops)
ip netns exec $router sysctl -qw net.ipv4.fib_multipath_hash_policy=1 >/dev/null 2>&1
echo $vrrp_vlan >$vpn_dir/vrrp_vlan
jq -r --arg m "$master_ip" '.prefixes[] | "\(.cidr) \(if .ecmp then 1 else 0 end) \((.next_hops // []) | if length > 0 then join(",") else $m end)"' <<<$content |
    awk 'NF == 3 && $3 != "null"' >$vpn_dir/nexthops.new
jq -r '.host_ips // {} | to_entries[] | "\(.key) \(.value)"' <<<$content >$vpn_dir/nexthop_hosts.new
mv -f $vpn_dir/nexthops.new $vpn_dir/nexthops
mv -f $vpn_dir/nexthop_hosts.new $vpn_dir/nexthop_hosts
(
    flock 9
    vpn_apply_nexthops $router $vpn_dir
) 9>$vpn_nexthop_lock
if [ -z "$desired" ]; then
    # nothing left for this node: the directory would otherwise outlive the gateway
    rm -rf $vpn_dir
else
    vpn_nexthop_watch_start
fi
exit 0
