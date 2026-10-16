#!/bin/bash
# Netns simulation of the gateway ports of a VPC router on one node (local WSL sandbox, not a CloudLand environment):
# set_subnet_gw.sh removing stale ports whose network overlaps a new one, clear_subnet_gw.sh in its port and address
# modes, and that neither touches the port or the DHCP server of another router. A router that lost the port in a
# reboot (bridge and directory of the VNI left) is cleaned up all the same.
SIM="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(cd "$SIM/../../../scripts" && pwd)"
pass=0; failn=0
ok() { echo "PASS $*"; pass=$((pass + 1)); }
bad() { echo "FAIL $*"; failn=$((failn + 1)); }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }
RT=/etc/iproute2/rt_tables.d/cloudland.conf

cleanup() {
    pkill -f "dnsmasq --interface=ns-50[0-9][0-9] " 2>/dev/null
    pkill -f 'sleep 600; :' 2>/dev/null
    for ns in $(ip netns list | awk '{print $1}'); do ip netns del $ns; done
    for l in $(ip -o link show | awk -F': ' '{print $2}' | grep -E '^(br|ln-|tap)50[0-9][0-9]'); do ip link del ${l%@*} 2>/dev/null; done
    iptables -S FORWARD | grep -E 'ln-50[0-9][0-9]' | sed 's/^-A/-D/' | while read r; do iptables $r; done
}
cleanup
rm -rf /opt/cloudland /opt/fakebin
mkdir -p /opt/cloudland/run /opt/cloudland/log /opt/cloudland/cache/router /opt/fakebin /etc/iproute2/rt_tables.d
cp -r $SRC /opt/cloudland/scripts
find /opt/cloudland/scripts -type f -exec sed -i 's/\r$//' {} +
chmod +x /opt/cloudland/scripts/kvm/*.sh
printf 'NODE_ID=1\nvxlan_interface=eth0\ndns_server=8.8.8.8\n' >/opt/cloudland/scripts/cloudrc.local
touch /opt/cloudland/run/debug
for c in virsh nmcli; do printf '#!/bin/bash\nexit 0\n' >/opt/fakebin/$c; chmod +x /opt/fakebin/$c; done
export PATH=/opt/fakebin:$PATH
K=/opt/cloudland/scripts/kvm
rt_had=$(cat $RT 2>/dev/null)
grep -q fip-756 $RT 2>/dev/null || echo "8 fip-756" >>$RT

ip netns add router-0
ip -n router-0 link set lo up
port() { ip link add br$2 type bridge 2>/dev/null; ip link set br$2 up; $K/set_subnet_gw.sh $1 $2 $3 >/dev/null 2>&1; }
has() { ip -n $1 link show ns-$2 >/dev/null 2>&1; }
dns() { pgrep -f "dnsmasq --interface=ns-$2 -C /opt/cloudland/cache/router/$1/$2/" >/dev/null; }
vm() {  # vm <name> <vni> <address>
    ip link add $1 type veth peer name tap$2; ip link set tap$2 master br$2; ip link set tap$2 up
    ip netns add $1; ip link set $1 netns $1
    ip -n $1 addr add $3 dev $1; ip -n $1 link set $1 up; ip -n $1 link set lo up
}
png() { ip netns exec router-21 ping -c 1 -W 1 $1 >/dev/null 2>&1; }

# --- set_subnet_gw.sh: a stale port of the same network (left by a deleted subnet) goes before the new one
port router-21 5001 192.168.223.1/24
ip -n router-21 route add 192.168.223.0/24 dev ns-5001 table fip-756 2>/dev/null
check "stale port ns-5001 (192.168.223.0/24) and its DHCP server" "has router-21 5001 && dns router-21 5001"
port router-21 5002 192.168.223.1/24
check "the new port removes the stale one: ns-5001, ln-5001, br5001 gone" "! has router-21 5001 && ! ip link show ln-5001 >/dev/null 2>&1 && ! ip link show br5001 >/dev/null 2>&1"
check "the DHCP server of the stale port stopped, the new one started" "! dns router-21 5001 && dns router-21 5002"
first=$(ip -n router-21 route show 192.168.223.0/24 | head -1)
check "the new port holds the connected route ($first)" "[[ \"$first\" == *ns-5002* ]]"
check "the fip- table routes the network to the new port" "ip -n router-21 route show table fip-756 | grep -q '192.168.223.0/24 dev ns-5002'"
vm vm5002 5002 192.168.223.2/24
check "a VM on the new subnet is reachable" "png 192.168.223.2"
# overlapping, not equal: a stale /25 under a new /24, and a stale /24 around a new /25
port router-21 5003 10.3.0.1/25
port router-21 5004 10.3.0.1/24
check "a stale /25 inside the new /24 goes" "! has router-21 5003 && has router-21 5004"
port router-21 5005 10.5.0.1/24
port router-21 5006 10.5.0.129/25
check "a stale /24 around the new /25 goes" "! has router-21 5005 && has router-21 5006"
port router-21 5007 10.7.0.1/24
check "ports of other networks stay" "has router-21 5002 && has router-21 5004 && has router-21 5006 && has router-21 5007"

# --- clear_subnet_gw.sh address: another subnet still uses the VNI, only its address goes
$K/set_subnet_gw.sh router-21 5007 10.7.1.1/24 >/dev/null 2>&1
check "VNI 5007 serves two networks" "ip -n router-21 -4 addr show ns-5007 | grep -q 10.7.0.1/24 && ip -n router-21 -4 addr show ns-5007 | grep -q 10.7.1.1/24"
$K/clear_subnet_gw.sh 21 5007 10.7.1.0/24 10.7.1.1/24 address; rc=$?
check "address mode exits 0" "[ $rc = 0 ]"
check "address mode: only 10.7.1.1 goes, the port, its other address and its DHCP server stay" "has router-21 5007 && ip -n router-21 -4 addr show ns-5007 | grep -q 10.7.0.1/24 && ! ip -n router-21 -4 addr show ns-5007 | grep -q 10.7.1.1/24 && dns router-21 5007"
check "address mode: nonat drops 10.7.1.0/24, keeps 10.7.0.0/24" "! ip netns exec router-21 ipset list nonat | grep -q '^10.7.1.0/24' && ip netns exec router-21 ipset list nonat | grep -q '^10.7.0.0/24'"
check "address mode: the fip- table route of 10.7.1.0/24 goes" "! ip -n router-21 route show table fip-756 | grep -q '10.7.1.0/24'"

# --- clear_subnet_gw.sh port: the VNI has no other subnet (the instances on 5002 and 5006 keep router-21)
vm vm5006 5006 10.5.0.130/25
$K/clear_subnet_gw.sh 21 5004 10.3.0.0/24 10.3.0.1/24 port; rc=$?
check "port mode exits 0" "[ $rc = 0 ]"
check "port mode: ns-5004, br5004, its DHCP server and directory are gone, nonat entry removed" "! has router-21 5004 && ! ip link show br5004 >/dev/null 2>&1 && ! dns router-21 5004 && [ ! -d /opt/cloudland/cache/router/router-21/5004 ] && ! ip netns exec router-21 ipset list nonat | grep -q '^10.3.0.0/24'"
check "port mode: the FORWARD rules of ln-5004 are gone" "! iptables -S FORWARD | grep -q ln-5004"
check "port mode: router-21 stays while instances use it" "ip netns list | grep -q '^router-21' && has router-21 5002 && has router-21 5006"

# --- the port of another router on the same node is not touched (same VNI in the DHCP directory of router-21)
port router-22 5009 10.9.0.1/24
mkdir -p /opt/cloudland/cache/router/router-21/5009
bash -c 'sleep 600; :' --interface=ns-5009 -C /opt/cloudland/cache/router/router-21/5009/dnsmasq.conf &
stale_dns=$!
sleep 0.5
$K/clear_subnet_gw.sh 21 5009 10.9.0.0/24 10.9.0.1/24 port
check "router-22 keeps ns-5009, br5009, its DHCP server and its FORWARD rules" "has router-22 5009 && ip link show br5009 >/dev/null 2>&1 && dns router-22 5009 && iptables -S FORWARD | grep -q ln-5009"
check "only the DHCP server and directory of router-21 for VNI 5009 go" "! kill -0 $stale_dns 2>/dev/null && [ ! -d /opt/cloudland/cache/router/router-21/5009 ]"

# --- the router goes with its last port once nothing uses it
ip netns del vm5002; ip link del tap5002 2>/dev/null
$K/clear_subnet_gw.sh 21 5002 192.168.223.0/24 192.168.223.1/24 port
check "an instance on br5006 keeps router-21" "ip netns list | grep -q '^router-21' && has router-21 5006"
ip netns del vm5006; ip link del tap5006 2>/dev/null
$K/clear_subnet_gw.sh 21 5006 10.5.0.128/25 10.5.0.129/25 port
check "nothing uses router-21 any more: the router is removed (ports, netns, directory)" "! ip netns list | grep -q '^router-21' && [ ! -d /opt/cloudland/cache/router/router-21 ] && ! ip link show br5007 >/dev/null 2>&1"
check "router-22 untouched" "ip netns list | grep -q '^router-22' && has router-22 5009"
$K/clear_subnet_gw.sh 21 5009 10.9.0.0/24 10.9.0.1/24 port
check "router-21 gone from this node: the bridge and FORWARD rules of VNI 5009 (router-22's) stay" "has router-22 5009 && ip link show br5009 >/dev/null 2>&1 && iptables -S FORWARD | grep -q ln-5009 && dns router-22 5009"

# --- after a reboot the router came back without the port: the bridge, FORWARD rules and directory of the VNI stayed
port router-23 5012 10.12.0.1/24
port router-23 5014 10.14.0.1/24
pkill -f "dnsmasq --interface=ns-5012 "
pkill -f "dnsmasq --interface=ns-5014 "
sleep 0.3
ip netns del router-23
$K/create_local_router.sh router-23 >/dev/null 2>&1
check "reboot: router-23 back without ns-5012 / ns-5014, their bridges and directories still there" "ip netns list | grep -q '^router-23' && ! has router-23 5012 && ! has router-23 5014 && ip link show br5012 >/dev/null 2>&1 && ip link show br5014 >/dev/null 2>&1 && [ -d /opt/cloudland/cache/router/router-23/5012 ] && [ -d /opt/cloudland/cache/router/router-23/5014 ]"
$K/clear_subnet_gw.sh 23 5012 10.12.0.0/24 10.12.0.1/24 port; rc=$?
check "port mode after a reboot: br5012, the FORWARD rules of ln-5012 and the directory go" "[ $rc = 0 ] && ! ip link show br5012 >/dev/null 2>&1 && ! iptables -S FORWARD | grep -q ln-5012 && [ ! -d /opt/cloudland/cache/router/router-23/5012 ]"
$K/clear_local_router.sh router-23 >/dev/null 2>&1
check "the unused router after a reboot goes with the bridge of the VNI it lost the port of (br5014)" "! ip netns list | grep -q '^router-23' && ! ip link show br5014 >/dev/null 2>&1 && ! iptables -S FORWARD | grep -q ln-5014 && [ ! -d /opt/cloudland/cache/router/router-23 ]"
# ... or without its netns at all
port router-24 5013 10.13.0.1/24
pkill -f "dnsmasq --interface=ns-5013 "
sleep 0.3
ip netns del router-24
$K/clear_subnet_gw.sh 24 5013 10.13.0.0/24 10.13.0.1/24 port; rc=$?
check "no netns after a reboot: br5013 and the directory go, no router is created" "[ $rc = 0 ] && ! ip link show br5013 >/dev/null 2>&1 && [ ! -d /opt/cloudland/cache/router/router-24/5013 ] && ! ip netns list | grep -q '^router-24'"
# ... or with the name of its netns gone while its DHCP server keeps it alive (ln-5016 has a peer without a name)
port router-26 5016 10.16.0.1/24
ip netns del router-26
check "netns name gone, ln-5016 still there with an unnamed peer" "ip -o link show ln-5016 2>/dev/null | grep -q link-netnsid && dns router-26 5016"
$K/clear_subnet_gw.sh 26 5016 10.16.0.0/24 10.16.0.1/24 port; rc=$?
sleep 0.3
check "an unnamed peer is not another router: the DHCP server, ln-5016, br5016 and the directory go" "[ $rc = 0 ] && ! dns router-26 5016 && ! ip link show ln-5016 >/dev/null 2>&1 && ! ip link show br5016 >/dev/null 2>&1 && [ ! -d /opt/cloudland/cache/router/router-26/5016 ]"
# a router without netns whose VNI another router holds here (two VPCs on one VNI)
mkdir -p /opt/cloudland/cache/router/router-25/5009
$K/clear_subnet_gw.sh 25 5009 10.9.0.0/24 10.9.0.1/24 port
check "no netns, VNI held by router-22: its port, bridge, FORWARD rules and DHCP server stay, only the directory of router-25 goes" "has router-22 5009 && ip link show br5009 >/dev/null 2>&1 && iptables -S FORWARD | grep -q ln-5009 && dns router-22 5009 && [ ! -d /opt/cloudland/cache/router/router-25/5009 ]"

# --- nodes without the router or the port, bad input
$K/clear_subnet_gw.sh 99 5010 10.1.0.0/24 10.1.0.1/24 port; rc=$?
check "no router here: exits 0 and creates nothing" "[ $rc = 0 ] && ! ip netns list | grep -q '^router-99'"
$K/clear_subnet_gw.sh 22 5011 10.1.0.0/24 10.1.0.1/24 port; rc=$?
check "router without the port: exits 0, router stays" "[ $rc = 0 ] && ip netns list | grep -q '^router-22'"
$K/clear_subnet_gw.sh x 5001 10.1.0.0/24 10.1.0.1/24 port >/dev/null 2>&1; rc=$?
check "bad router id refused" "[ $rc != 0 ]"
$K/clear_subnet_gw.sh 22 5009 10.9.0.0/24 10.9.0.1/24 all >/dev/null 2>&1; rc=$?
check "unknown mode refused, nothing removed" "[ $rc != 0 ] && has router-22 5009"
$K/clear_subnet_gw.sh 22 5009 10.9.0.0/24 >/dev/null 2>&1; rc=$?
check "missing arguments refused" "[ $rc != 0 ] && has router-22 5009"

cleanup
if [ -n "$rt_had" ]; then echo "$rt_had" >$RT; else rm -f $RT; fi
echo "subnet-gw: $pass passed, $failn failed"
[ $failn = 0 ]
