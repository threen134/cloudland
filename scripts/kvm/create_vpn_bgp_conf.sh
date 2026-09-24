#!/bin/bash

# Rewrite the FRR configuration of a VPN gateway (one pathspace per gateway: /etc/frr/vpn-<gw>/) from its
# BGP connections and reload it on the node holding the floating IP. Every neighbor gets an inbound
# prefix-list built from the user's summary networks: with the IPsec traffic selectors open to 0.0.0.0/0
# this list is the only thing bounding what the peer can inject (plan §2.6, §4.3.1).
#
# stdin JSON: {"connections": [{"name","local_asn","peer_asn","tunnel_local_ip","tunnel_peer_ip",
#   "password","keepalive","hold","max_prefixes","local_cidrs":[],"remote_summary_cidrs":[]}]}

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
nconn=$(jq '.connections | length' <<<$content)

# INPUT rules for the BGP sessions (the /30 link addresses are not in nonat): rebuild from the tracked list
old_rules=$(cat $vpn_dir/bgp_rules 2>/dev/null)
for rule in $old_rules; do
    ip netns exec $router iptables -D INPUT -p tcp -s ${rule%>*} -d ${rule#*>} --dport 179 -j ACCEPT 2>/dev/null
done
: >$vpn_dir/bgp_rules
: >$vpn_dir/bgp_peers.new

if [ $nconn -eq 0 ]; then
    (
        flock 9
        vpn_stop_frr $gw
        rm -rf /etc/frr/$ns
        mv -f $vpn_dir/bgp_peers.new $vpn_dir/bgp_peers
    ) 9>$lb_lock_file
    exit 0
fi

mkdir -p /etc/frr/$ns
conf=/etc/frr/$ns/frr.conf.new
vpn_json_read "$content" local_asn=.connections[0].local_asn router_id=.connections[0].tunnel_local_ip
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
    cat >>$conf <<EOF
route-map IN-$name permit 10
 match ip address prefix-list REMOTE-$name
exit
route-map OUT-$name permit 10
 match ip address prefix-list LOCAL-$name
exit
!
EOF
    let i=$i+1
done
# import-check stays on (FRR default): a subnet is only advertised once its gateway port exists here,
# i.e. once it has an instance somewhere in the VPC; advertising it earlier would attract traffic that
# the router could only forward to the default route
# The summary blackholes belong to staticd (distance 250) so that BGP routes for the same prefix win
for cidr in $(jq -r '[.connections[].remote_summary_cidrs[]] | unique | .[]' <<<$content); do
    echo "ip route $cidr Null0 250" >>$conf
done
echo "!" >>$conf
cat >>$conf <<EOF
router bgp $local_asn
 bgp router-id $router_id
EOF
i=0
while [ $i -lt $nconn ]; do
    conn=$(jq -c ".connections[$i]" <<<$content)
    vpn_json_read "$conn" name=.name peer_asn=.peer_asn local_ip=.tunnel_local_ip peer_ip=.tunnel_peer_ip keepalive=.keepalive hold=.hold
    password=$(jq -r '.password' <<<$conn)
    cat >>$conf <<EOF
 neighbor $peer_ip remote-as $peer_asn
 neighbor $peer_ip description $name
 neighbor $peer_ip update-source $local_ip
 neighbor $peer_ip timers $keepalive $hold
 neighbor $peer_ip timers connect 10
EOF
    [ -n "$password" -a "$password" != "null" ] && echo " neighbor $peer_ip password $password" >>$conf
    echo "$name $peer_ip" >>$vpn_dir/bgp_peers.new
    ip netns exec $router iptables -C INPUT -p tcp -s $peer_ip -d $local_ip --dport 179 -j ACCEPT 2>/dev/null ||
        ip netns exec $router iptables -A INPUT -p tcp -s $peer_ip -d $local_ip --dport 179 -j ACCEPT
    echo "$peer_ip>$local_ip" >>$vpn_dir/bgp_rules
    let i=$i+1
done
echo " address-family ipv4 unicast" >>$conf
# Networks: the union of every connection's local networks (each neighbor's outbound list filters them)
networks=$(jq -r '[.connections[].local_cidrs[]] | unique | .[]' <<<$content)
for cidr in $networks; do
    echo "  network $cidr" >>$conf
done
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
cat >>$conf <<EOF
 exit-address-family
exit
!
EOF
echo "service integrated-vtysh-config" >/etc/frr/$ns/vtysh.conf
chown -R frr:frr /etc/frr/$ns
chmod 640 $conf

(
    flock 9
    mv -f $conf /etc/frr/$ns/frr.conf
    mv -f $vpn_dir/bgp_peers.new $vpn_dir/bgp_peers
    rm -f $vpn_dir/bgp.reported
    if vpn_holds_vip $router $vpn_dir; then
        vpn_reload_frr $router $gw
    else
        vpn_stop_frr $gw
    fi
) 9>$lb_lock_file
exit 0
