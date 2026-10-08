#!/bin/bash

# Rewrite the FRR configuration of a VPN gateway (one pathspace per gateway: /etc/frr/vpn-<gw>/) and reload it
# on the nodes that run the tunnels. Every neighbor gets an inbound prefix-list built from the user's summary
# networks: with the IPsec traffic selectors open to 0.0.0.0/0 this list is the only thing bounding what the
# peer can inject (plan §6.4, §5.3).
#
# Each entry of "connections" is the session of one tunnel. The tunnels of a connection are ranked by
# local_pref (our traffic) and prepend (the local ASN prepended on a lesser tunnel's advertisements, for the
# peer's traffic); bfd adds a BFD session with the given interval and multiplier. multipath above 1 lets the
# tunnels of a connection that shares its traffic (ecmp, equal local_pref) be used together.
#
# active_active (plan §4.2): each node only configures the sessions of its own tunnels (host),
# plus an iBGP session to the other node over the VRRP addresses, with BFD. Static connections run through
# FRR too: while the SA of a static tunnel of this node is up, staticd routes its remote networks through
# its interface (vpn_frr_statics, driven by vpn_watch.sh), redistributed into iBGP with the local preference
# of the tunnel's rank; their remote networks keep a Null0 route of last resort (distance 254) so that
# nothing leaks through the default route while no tunnel carries them.
#
# stdin JSON: {"ha_mode", "local_asn", "multipath", "ibgp": {"nodes": [{"host","ip"}], "bfd_interval", "bfd_multiplier"},
#   "static_tunnels": [{"name","host","if_id","tag","distance","local_pref","cidrs":[]}], "static_cidrs": [],
#   "connections": [{"name","host","local_asn","peer_asn","tunnel_local_ip","tunnel_peer_ip","password","keepalive",
#   "hold","max_prefixes","local_cidrs":[],"remote_summary_cidrs":[],"local_pref","prepend","bfd","bfd_interval","bfd_multiplier"}]}

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 2 ] && die "$0 <router> <gw_ID>"

ID=$1
router=router-$ID
gw=$2
ns=$(vpn_frr_ns $gw)
vpn_dir=$(vpn_dir_of $ID $gw)
[ -d "$vpn_dir" ] || die "VPN gateway $gw is not built on this node"

content=$(cat)
# Only what runs on this node: every entry of an active_standby gateway (host -1), the own ones otherwise
mine='select(((.host // -1) | tostring) == "-1" or ((.host // -1) | tostring) == $me)'
content=$(jq -c --arg me "$NODE_ID" "(.connections //= []) | (.static_tunnels //= []) | (.static_cidrs //= []) | .connections |= map($mine) | .static_tunnels |= map($mine)" <<<$content)
nconn=$(jq '.connections | length' <<<$content)
vpn_json_read "$content" local_asn=.local_asn multipath=.multipath ibgp_interval=.ibgp.bfd_interval ibgp_multiplier=.ibgp.bfd_multiplier
[ "$local_asn" = "null" -o -z "$local_asn" -o "$local_asn" = "0" ] && local_asn=$(jq -r '.connections[0].local_asn // empty' <<<$content)
[ "$multipath" = "null" -o -z "$multipath" ] && multipath=1
# The iBGP peer: the other node of an active_active gateway, reached from this node's VRRP address
my_ip=$(jq -r --arg me "$NODE_ID" '.ibgp.nodes[]? | select((.host | tostring) == $me) | .ip' <<<$content | head -1)
ibgp_peer=""
[ -n "$my_ip" ] && ibgp_peer=$(jq -r --arg me "$NODE_ID" '.ibgp.nodes[]? | select((.host | tostring) != $me) | .ip' <<<$content | head -1)

# Static tunnels of this node for vpn_frr_statics: <name> <if_id> <tag> <distance> <cidr>...
jq -r '.static_tunnels[] | "\(.name) \(.if_id) \(.tag) \(.distance) \(.cidrs | join(" "))"' <<<$content >$vpn_dir/static_tunnels.new

# INPUT rules for the BGP and BFD sessions (the /30 link addresses and the VRRP addresses are not in nonat):
# rebuild from the tracked list
old_rules=$(cat $vpn_dir/bgp_rules 2>/dev/null)
for rule in $old_rules; do
    ip netns exec $router iptables -D INPUT -p tcp -s ${rule%>*} -d ${rule#*>} --dport 179 -j ACCEPT 2>/dev/null
    ip netns exec $router iptables -D INPUT -p udp -s ${rule%>*} -d ${rule#*>} --dport 3784 -j ACCEPT 2>/dev/null
done
: >$vpn_dir/bgp_rules
: >$vpn_dir/bgp_peers.new

# allow_session <peer> <local> <bfd true|false>
allow_session()
{
    ip netns exec $router iptables -C INPUT -p tcp -s $1 -d $2 --dport 179 -j ACCEPT 2>/dev/null ||
        ip netns exec $router iptables -A INPUT -p tcp -s $1 -d $2 --dport 179 -j ACCEPT
    if [ "$3" = "true" ]; then
        ip netns exec $router iptables -C INPUT -p udp -s $1 -d $2 --dport 3784 -j ACCEPT 2>/dev/null ||
            ip netns exec $router iptables -A INPUT -p udp -s $1 -d $2 --dport 3784 -j ACCEPT
    fi
    echo "$1>$2" >>$vpn_dir/bgp_rules
}

if [ $nconn -eq 0 ] && [ -z "$ibgp_peer" ]; then
    (
        flock 9
        vpn_stop_frr $gw
        rm -rf /etc/frr/$ns
        mv -f $vpn_dir/bgp_peers.new $vpn_dir/bgp_peers
        mv -f $vpn_dir/static_tunnels.new $vpn_dir/static_tunnels
    ) 9>$lb_lock_file
    exit 0
fi
mv -f $vpn_dir/static_tunnels.new $vpn_dir/static_tunnels

mkdir -p /etc/frr/$ns
conf=/etc/frr/$ns/frr.conf.new
router_id=$my_ip
[ -n "$router_id" ] || router_id=$(jq -r '.connections[0].tunnel_local_ip' <<<$content)
cat >$conf <<EOF
frr defaults traditional
hostname $ns
log syslog warnings
!
EOF
# Prefix lists and route maps first: vtysh applies the file top to bottom
i=0
while [ $i -lt $nconn ]; do
    conn=$(jq -c ".connections[$i]" <<<$content)
    vpn_json_read "$conn" name=.name
    seq=5
    for cidr in $(jq -r '.remote_summary_cidrs[]' <<<$conn); do
        echo "ip prefix-list REMOTE-$name seq $seq permit $cidr le 32" >>$conf
        let seq=$seq+5
    done
    seq=5
    for cidr in $(jq -r '.local_cidrs[]' <<<$conn); do
        echo "ip prefix-list LOCAL-$name seq $seq permit $cidr" >>$conf
        let seq=$seq+5
    done
    vpn_json_read "$conn" local_pref=.local_pref prepend=.prepend asn=.local_asn
    [ "$local_pref" = "null" -o -z "$local_pref" -o "$local_pref" = "0" ] && local_pref=100
    cat >>$conf <<EOF
route-map IN-$name permit 10
 match ip address prefix-list REMOTE-$name
 set local-preference $local_pref
exit
route-map OUT-$name permit 10
 match ip address prefix-list LOCAL-$name
EOF
    if [ "$prepend" != "null" ] && [ "${prepend:-0}" -gt 0 ]; then
        echo " set as-path prepend$(for n in $(seq $prepend); do printf ' %s' $asn; done)" >>$conf
    fi
    cat >>$conf <<EOF
exit
!
EOF
    let i=$i+1
done
# Static tunnel routes into iBGP: one entry per rank (tag), with the rank's local preference; everything
# else (the Null0 routes) is left out. weight 0: a route bgpd originates gets weight 32768, which beats any
# local preference, so the node with only a lesser tunnel would keep it over the better tunnel of the
# other node learned over iBGP
if [ -n "$ibgp_peer" ]; then
    jq -r '.static_tunnels[] | "\(.tag) \(.local_pref)"' <<<$content | sort -un | while read tag lp; do
        cat <<EOF
route-map TUNNEL-STATIC permit $tag
 match tag $tag
 set local-preference $lp
 set weight 0
exit
EOF
    done >>$conf
    cat >>$conf <<EOF
route-map TUNNEL-STATIC deny 65535
exit
!
EOF
fi
# BFD profiles: one per tunnel that asks for it, and the iBGP session between the nodes
if [ "$(jq '[.connections[] | select(.bfd == true)] | length' <<<$content)" -gt 0 ] || [ -n "$ibgp_peer" ]; then
    echo "bfd" >>$conf
    i=0
    while [ $i -lt $nconn ]; do
        vpn_json_read "$content" name=.connections[$i].name bfd=.connections[$i].bfd interval=.connections[$i].bfd_interval multiplier=.connections[$i].bfd_multiplier
        if [ "$bfd" = "true" ]; then
            cat >>$conf <<EOF
 profile $name
  detect-multiplier $multiplier
  receive-interval $interval
  transmit-interval $interval
 exit
EOF
        fi
        let i=$i+1
    done
    if [ -n "$ibgp_peer" ]; then
        [ "$ibgp_interval" = "null" -o -z "$ibgp_interval" ] && ibgp_interval=300
        [ "$ibgp_multiplier" = "null" -o -z "$ibgp_multiplier" ] && ibgp_multiplier=3
        cat >>$conf <<EOF
 profile ibgp
  detect-multiplier $ibgp_multiplier
  receive-interval $ibgp_interval
  transmit-interval $ibgp_interval
 exit
EOF
    fi
    echo "exit" >>$conf
    echo "!" >>$conf
fi
# import-check stays on (FRR default): a subnet is only advertised once its gateway port exists here,
# i.e. once it has an instance somewhere in the VPC; advertising it earlier would attract traffic that
# the router could only forward to the default route
# The summary blackholes belong to staticd (distance 250) so that BGP routes for the same prefix win
for cidr in $(jq -r '[.connections[].remote_summary_cidrs[]] | unique | .[]' <<<$content); do
    echo "ip route $cidr Null0 250" >>$conf
done
if [ -n "$ibgp_peer" ]; then
    # The remote networks of the static connections: last resort when no tunnel of either node carries them
    for cidr in $(jq -r '.static_cidrs | unique | .[]' <<<$content); do
        echo "ip route $cidr Null0 254" >>$conf
    done
    # The tunnel routes that are up right now, so that the reload keeps them (vpn_frr_statics follows the
    # SAs from here on)
    vpn_static_wanted $vpn_dir >>$conf
fi
echo "!" >>$conf
cat >>$conf <<EOF
router bgp $local_asn
 bgp router-id $router_id
EOF
[ "$multipath" -gt 1 ] && echo " bgp bestpath as-path multipath-relax" >>$conf
i=0
while [ $i -lt $nconn ]; do
    conn=$(jq -c ".connections[$i]" <<<$content)
    vpn_json_read "$conn" name=.name peer_asn=.peer_asn local_ip=.tunnel_local_ip peer_ip=.tunnel_peer_ip keepalive=.keepalive hold=.hold bfd=.bfd
    password=$(jq -r '.password' <<<$conn)
    # A session that could not start retries every 2 s: once the tunnel is up, BGP follows within 2 s
    # (vpn_watch.sh also restarts it the moment the tunnel comes up)
    cat >>$conf <<EOF
 neighbor $peer_ip remote-as $peer_asn
 neighbor $peer_ip description $name
 neighbor $peer_ip update-source $local_ip
 neighbor $peer_ip timers $keepalive $hold
 neighbor $peer_ip timers connect 2
EOF
    [ -n "$password" -a "$password" != "null" ] && echo " neighbor $peer_ip password $password" >>$conf
    # FRR shows "bfd" and "bfd profile" as two lines: without the first one frr-reload removes BFD from the
    # neighbor and adds it back on every reload, which takes the session down
    [ "$bfd" = "true" ] && printf ' neighbor %s bfd\n neighbor %s bfd profile %s\n' $peer_ip $peer_ip $name >>$conf
    echo "$name $peer_ip" >>$vpn_dir/bgp_peers.new
    allow_session $peer_ip $local_ip $bfd
    let i=$i+1
done
if [ -n "$ibgp_peer" ]; then
    # Between the two nodes: short timers and BFD, the link is private (the VRRP subnet)
    cat >>$conf <<EOF
 neighbor $ibgp_peer remote-as $local_asn
 neighbor $ibgp_peer description ibgp
 neighbor $ibgp_peer update-source $my_ip
 neighbor $ibgp_peer timers 3 9
 neighbor $ibgp_peer timers connect 2
 neighbor $ibgp_peer bfd
 neighbor $ibgp_peer bfd profile ibgp
EOF
    allow_session $ibgp_peer $my_ip true
fi
echo " address-family ipv4 unicast" >>$conf
# Networks: the union of every connection's local networks (each neighbor's outbound list filters them)
networks=$(jq -r '[.connections[].local_cidrs[]] | unique | .[]' <<<$content)
for cidr in $networks; do
    echo "  network $cidr" >>$conf
done
[ "$multipath" -gt 1 ] && echo "  maximum-paths $multipath" >>$conf
[ -n "$ibgp_peer" ] && echo "  redistribute static route-map TUNNEL-STATIC" >>$conf
i=0
while [ $i -lt $nconn ]; do
    vpn_json_read "$content" name=.connections[$i].name peer_ip=.connections[$i].tunnel_peer_ip max_prefixes=.connections[$i].max_prefixes
    cat >>$conf <<EOF
  neighbor $peer_ip activate
  neighbor $peer_ip soft-reconfiguration inbound
  neighbor $peer_ip route-map IN-$name in
  neighbor $peer_ip route-map OUT-$name out
  neighbor $peer_ip maximum-prefix $max_prefixes warning-only
EOF
    let i=$i+1
done
if [ -n "$ibgp_peer" ]; then
    cat >>$conf <<EOF
  neighbor $ibgp_peer activate
  neighbor $ibgp_peer next-hop-self
EOF
fi
cat >>$conf <<EOF
 exit-address-family
exit
!
EOF
echo "service integrated-vtysh-config" >/etc/frr/$ns/vtysh.conf
chown -R frr:frr /etc/frr/$ns
chmod 640 $conf

# Timers are negotiated when a session opens: a reload leaves an established session on the old ones
old_timers=$(grep -E '^ neighbor [0-9.]+ timers [0-9]+ [0-9]+$' /etc/frr/$ns/frr.conf 2>/dev/null)
new_timers=$(grep -E '^ neighbor [0-9.]+ timers [0-9]+ [0-9]+$' $conf)
retimed=$(comm -13 <(echo "$old_timers" | sort) <(echo "$new_timers" | sort) | awk '{print $2}')
(
    flock 9
    mv -f $conf /etc/frr/$ns/frr.conf
    mv -f $vpn_dir/bgp_peers.new $vpn_dir/bgp_peers
    rm -f $vpn_dir/bgp.reported
    if vpn_runs_tunnels $router $vpn_dir && ! vpn_disabled $vpn_dir; then
        vpn_reload_frr $router $gw
        [ -n "$old_timers" ] && for peer in $retimed; do
            timeout 5 vtysh -N $ns -c "clear bgp $peer" >/dev/null 2>&1
        done
        vpn_frr_statics $router $vpn_dir
    else
        vpn_stop_frr $gw
    fi
) 9>$lb_lock_file
exit 0
