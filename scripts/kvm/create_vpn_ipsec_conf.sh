#!/bin/bash

# Rewrite swanctl.conf of a VPN gateway from the full connection list and reconcile the XFRM interfaces.
# Runs on both VRRP nodes; only the node holding the floating IP has charon running and reloads it.
# Routes are not touched here (vpn_notify.sh owns them, see the plan §2.3).
#
# stdin JSON: {"floating_ip": "a.b.c.d", "connections": [{"name","if_id","remote_gateway","remote_id","local_id",
#   "route_mode","local_cidrs":[],"remote_cidrs":[],"psk","ike_proposal","esp_proposal","ike_lifetime","esp_lifetime",
#   "dpd_action","dpd_delay","initiator","tunnel_local_ip","tunnel_peer_ip"}]}

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 2 ] && die "$0 <router> <gw_ID>"

ID=$1
router=router-$ID
gw=$2
vpn_dir=$(vpn_dir_of $ID $gw)
[ -d "$vpn_dir" ] || die "VPN gateway $gw is not built on this node"
[ -f /var/run/netns/$router ] || die "Router $router does not exist"

content=$(cat)
floating_ip=$(jq -r '.floating_ip' <<<$content)
nconn=$(jq '.connections | length' <<<$content)

# XFRM interfaces: one per connection, keyed by if_id; policies bind to the id, not to the device name
desired_ifaces=""
i=0
while [ $i -lt $nconn ]; do
    vpn_json_read "$content" if_id=.connections[$i].if_id route_mode=.connections[$i].route_mode tunnel_local_ip=.connections[$i].tunnel_local_ip
    dev=ipsec-$if_id
    ip netns exec $router ip link show $dev >/dev/null 2>&1 || ip netns exec $router ip link add $dev type xfrm dev lo if_id $if_id
    ip netns exec $router ip link set $dev mtu 1360 up
    if [ "$route_mode" = "bgp" ] && [ -n "$tunnel_local_ip" -a "$tunnel_local_ip" != "null" ]; then
        # the BGP session runs on a /30 inside the tunnel
        ip netns exec $router ip -4 -o addr show dev $dev | grep -qF " $tunnel_local_ip/30 " || {
            ip netns exec $router ip addr flush dev $dev
            ip netns exec $router ip addr add $tunnel_local_ip/30 dev $dev
        }
    else
        ip netns exec $router ip addr flush dev $dev
    fi
    desired_ifaces="$desired_ifaces $dev"
    let i=$i+1
done
for dev in $(ip netns exec $router ip -o link show type xfrm | awk -F': ' '{print $2}' | cut -d@ -f1 | grep '^ipsec-'); do
    case " $desired_ifaces " in
    *" $dev "*) ;;
    *) ip netns exec $router ip link del $dev ;;
    esac
done
echo $desired_ifaces >$vpn_dir/ifaces

# swanctl.conf: one connection per site, one child per (local, remote) pair in static mode (peers that
# only accept a single traffic selector pair per CHILD_SA would otherwise narrow to the first pair)
umask 077
conf=$vpn_dir/swanctl.conf.new
: >$conf
: >$vpn_dir/conn_names.new
echo "connections {" >>$conf
i=0
while [ $i -lt $nconn ]; do
    conn=$(jq -c ".connections[$i]" <<<$content)
    vpn_json_read "$conn" name=.name if_id=.if_id remote_gateway=.remote_gateway remote_id=.remote_id local_id=.local_id route_mode=.route_mode \
        ike_proposal=.ike_proposal esp_proposal=.esp_proposal ike_lifetime=.ike_lifetime esp_lifetime=.esp_lifetime dpd_action=.dpd_action dpd_delay=.dpd_delay initiator=.initiator
    remote_addrs=$remote_gateway
    [ -z "$remote_addrs" -o "$remote_addrs" = "null" ] && remote_addrs="%any"
    [ -z "$local_id" -o "$local_id" = "null" ] && local_id=$floating_ip
    [ -z "$remote_id" -o "$remote_id" = "null" ] && remote_id=$remote_gateway
    # close_action stays none on purpose: with start, charon re-creates every CHILD_SA the peer closed with
    # the selectors that SA had, not with the current configuration, so after a change of route mode or
    # networks the two sides keep re-establishing the old selectors forever (the BGP link address is then
    # outside every SA). Re-establishment after the peer closed the tunnel is done by the watchdog from
    # the configuration instead (vpn_ipsec_reconcile)
    start_action=none
    close_action=none
    [ "$initiator" = "true" ] && start_action=start
    # swanctl knows clear / trap / start for dpd_action: the API's restart is start, none is clear
    case $dpd_action in
    restart) dpd_action=start ;;
    none) dpd_action=clear ;;
    esac
    echo "$name" >>$vpn_dir/conn_names.new
    cat >>$conf <<EOF
    $name {
        local_addrs = $floating_ip
        remote_addrs = $remote_addrs
        version = 2
        proposals = $ike_proposal
        fragmentation = yes
        dpd_delay = ${dpd_delay}s
        rekey_time = ${ike_lifetime}s
        keyingtries = 0
        unique = replace
        local {
            auth = psk
            id = $local_id
        }
        remote {
            auth = psk
            id = $remote_id
        }
        children {
EOF
    child=0
    if [ "$route_mode" = "bgp" ]; then
        pairs="0.0.0.0/0|0.0.0.0/0"
    else
        pairs=""
        for l in $(jq -r '.local_cidrs[]' <<<$conn); do
            for r in $(jq -r '.remote_cidrs[]' <<<$conn); do
                pairs="$pairs $l|$r"
            done
        done
    fi
    for pair in $pairs; do
        cat >>$conf <<EOF
            net$child {
                local_ts = ${pair%|*}
                remote_ts = ${pair#*|}
                esp_proposals = $esp_proposal
                if_id_in = $if_id
                if_id_out = $if_id
                mode = tunnel
                start_action = $start_action
                close_action = $close_action
                dpd_action = $dpd_action
                rekey_time = ${esp_lifetime}s
            }
EOF
        let child=$child+1
    done
    cat >>$conf <<EOF
        }
    }
EOF
    let i=$i+1
done
echo "}" >>$conf
echo "secrets {" >>$conf
i=0
while [ $i -lt $nconn ]; do
    vpn_json_read "$content" name=.connections[$i].name remote_id=.connections[$i].remote_id remote_gateway=.connections[$i].remote_gateway
    psk=$(jq -r ".connections[$i].psk" <<<$content)
    [ -z "$remote_id" -o "$remote_id" = "null" ] && remote_id=$remote_gateway
    cat >>$conf <<EOF
    ike-$name {
        id = $remote_id
        secret = "$psk"
    }
EOF
    let i=$i+1
done
echo "}" >>$conf
umask 022

# Connections whose definition changed: loading the new config does not touch an established IKE SA,
# so its CHILD_SA keeps the old traffic selectors (a static -> bgp switch would silently keep the narrow
# ones); those are terminated after the reload and, when we initiate, started again.
conn_block()
{
    awk -v n="    $2 {" '$0 == n {f = 1} f {print} f && /^    }$/ {exit}' $1 2>/dev/null
}
changed=""
if [ -f $vpn_dir/swanctl.conf ]; then
    for name in $(cat $vpn_dir/conn_names.new); do
        old=$(conn_block $vpn_dir/swanctl.conf $name)
        [ -n "$old" ] && [ "$old" != "$(conn_block $conf $name)" ] && changed="$changed $name"
    done
fi

(
    flock 9
    mv -f $conf $vpn_dir/swanctl.conf
    mv -f $vpn_dir/conn_names.new $vpn_dir/conn_names
    rm -f $vpn_dir/status.reported
    if vpn_holds_vip $router $vpn_dir; then
        if vpn_charon_alive $vpn_dir; then
            vpn_load_swanctl $router $vpn_dir
            for name in $changed; do
                vpn_swanctl $router $vpn_dir --terminate --ike $name --force --timeout 10 >/dev/null
                # start_action sits deep in the block (past the ike / local / remote sections): check the block itself
                if awk -v n="    $name {" '$0 == n {f = 1} f && /start_action = start/ {found = 1} f && /^    }/ {exit} END {exit !found}' $vpn_dir/swanctl.conf; then
                    # every child: a static connection has one per (local, remote) pair
                    for child in $(vpn_conn_children $vpn_dir/swanctl.conf $name); do
                        vpn_swanctl $router $vpn_dir --initiate --ike $name --child $child --timeout 20 >/dev/null
                    done
                fi
            done
        else
            vpn_start_charon $router $vpn_dir
        fi
    fi
) 9>$lb_lock_file
exit 0
