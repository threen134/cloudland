#!/bin/bash

# Install the networks reachable through a VPN gateway on this node's router netns. Declarative: the JSON
# carries the whole desired set, the node reconciles against what it installed last time.
#
# Every node hosting the VPC gets the nonat entries (otherwise traffic to the remote side is SNATed on its
# way to router-0). Routes depend on the role of the node (plan §2.3 ownership table):
#   - a VRRP member only records tunnel_routes and lets vpn_notify.sh install them by role
#   - any other node points every prefix at the master's VRRP address over ns-<vrrp vlan>
#
# stdin JSON: {"vrrp_nodes":[..], "master_ip", "vrrp_ips": {"<hostid>": "ip"}, "vrrp_vlan", "vrrp_gateway",
#              "prefixes": [{"cidr","type","target"}]}

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

# Other nodes: one static route per prefix towards the master
installed=$(cat $vpn_dir/routes.current 2>/dev/null)
new=""
if [ -n "$master_ip" -a "$master_ip" != "null" ] && ip netns exec $router ip link show ns-$vrrp_vlan >/dev/null 2>&1; then
    for cidr in $desired; do
        ip netns exec $router ip route replace $cidr via $master_ip dev ns-$vrrp_vlan onlink
        new="$new $cidr"
    done
fi
for cidr in $installed; do
    case " $new " in
    *" $cidr "*) ;;
    *) ip netns exec $router ip route del $cidr >/dev/null 2>&1 ;;
    esac
done
echo $new >$vpn_dir/routes.current
if [ -z "$desired" ]; then
    # nothing left for this node: the directory would otherwise outlive the gateway
    rm -rf $vpn_dir
fi
exit 0
