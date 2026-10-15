#!/bin/bash
# Netns simulation of apply_tgw.sh on one node (local WSL sandbox, not a CloudLand environment).
# VPC routers router-11/12/13 (A/B/C) with "VMs" as netns behind their gateway ports.
SIM="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(cd "$SIM/../../../scripts" && pwd)"
pass=0; failn=0
ok() { echo "PASS $*"; pass=$((pass + 1)); }
bad() { echo "FAIL $*"; failn=$((failn + 1)); }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }

for ns in $(ip netns list | awk '{print $1}'); do ip netns del $ns; done
rm -rf /opt/cloudland /opt/fakebin
mkdir -p /opt/cloudland/run /opt/cloudland/log /opt/cloudland/cache/router /opt/fakebin
cp -r $SRC /opt/cloudland/scripts
find /opt/cloudland/scripts -type f -exec sed -i 's/\r$//' {} +
chmod +x /opt/cloudland/scripts/kvm/*.sh
printf 'NODE_ID=1\nvxlan_interface=eth0\n' >/opt/cloudland/scripts/cloudrc.local
touch /opt/cloudland/run/debug
printf '#!/bin/bash\nexit 0\n' >/opt/fakebin/virsh; chmod +x /opt/fakebin/virsh
export PATH=/opt/fakebin:$PATH
K=/opt/cloudland/scripts/kvm

# router-0 with a default route, as on a node
ip netns add router-0
ip -n router-0 link set lo up
ip -n router-0 link add up0 type dummy
ip -n router-0 addr add 203.0.113.1/24 dev up0
ip -n router-0 link set up0 up
ip -n router-0 route add default via 203.0.113.254

for r in 11 12 13; do $K/create_local_router.sh router-$r >/dev/null 2>&1; done
# gateway port + VM: mkvm <router> <vni> <gateway/len> <vm ip/len> <vm netns>
mkvm() {
    ip link add ns-$2 type veth peer name v$2
    ip link set ns-$2 netns router-$1
    ip netns add $5
    ip link set v$2 netns $5
    ip -n router-$1 addr add $3 dev ns-$2
    ip -n router-$1 link set ns-$2 up
    ip -n $5 link set lo up
    ip -n $5 addr add $4 dev v$2
    ip -n $5 link set v$2 up
    ip -n $5 route add default via ${3%/*}
    ip netns exec router-$1 ipset add nonat $(ipcalc -n -b $3 | awk '/^Network:/ {print $2}') -exist
}
mkvm 11 101 192.168.1.1/24 192.168.1.10/24 vmA1
mkvm 11 111 192.168.11.1/24 192.168.11.10/24 vmA2
mkvm 12 102 192.168.2.1/24 192.168.2.10/24 vmB1
ip -n vmB1 addr add 192.168.2.20/24 dev v102
ip -n vmB1 addr add 192.168.2.200/24 dev v102
mkvm 12 112 192.168.12.1/24 192.168.12.10/24 vmB2
mkvm 13 103 192.168.3.1/24 192.168.3.10/24 vmC1
# A floating IP of vmA1 created before the attachment: policy rules and table as create_floating.sh leaves them
ip -n router-11 link add te-756 type dummy
ip -n router-11 addr add 52.0.0.2/24 dev te-756
ip -n router-11 link set te-756 up
ip -n router-11 route add default via 52.0.0.1 dev te-756 table 77
ip -n router-11 route add 192.168.1.0/24 dev ns-101 table 77
ip -n router-11 rule add from 192.168.1.10 lookup 77
ip -n router-11 rule add to 192.168.1.10 lookup 77

png() { ip netns exec $1 ping -c 1 -W 1 $2 >/dev/null 2>&1; }
apply() { $K/apply_tgw.sh 7 $1 <$SIM/$2 2>/dev/null; }

echo "== before the gateway"
check "A1 -> B1 unreachable before attach" "! png vmA1 192.168.2.10"

echo "== g1 full mesh"
out=$(apply 1 g1-mesh.json)
check "g1 reports ok with 3 attachments: $out" "[[ \"\$out\" == *\"'7' '1' 'ok' '-' '3'\"* ]]"
check "A1 -> B1" "png vmA1 192.168.2.10"
check "A1 -> B2" "png vmA1 192.168.12.10"
check "A2 -> C1" "png vmA2 192.168.3.10"
check "B1 -> C1" "png vmB1 192.168.3.10"
check "C1 -> A1 (floating IP VM, reply through the gateway)" "png vmC1 192.168.1.10"
ip netns exec vmB1 timeout 4 tcpdump -c 1 -ni v102 icmp and dst 192.168.2.10 2>/dev/null >/tmp/tgw-src.txt &
sleep 1; png vmA1 192.168.2.10; wait
check "source address preserved (B1 sees 192.168.1.10)" "grep -q '192.168.1.10 > 192.168.2.10' /tmp/tgw-src.txt"
check "router-11 policy rule pref 100" "ip -n router-11 rule show | grep -q '^100:.*lookup tgw'"
check "router-11 throws its own networks" "ip -n router-11 route show table 252 | grep -q '^throw 192.168.11.0/24'"
check "router-11 nonat has B and C" "ip netns exec router-11 ipset list nonat | grep -q 192.168.12.0/24 && ip netns exec router-11 ipset list nonat | grep -q 192.168.3.0/24"
check "router-11 forward isolation rules" "[ \$(ip netns exec router-11 iptables -S FORWARD | grep -c 'tr+.*-j DROP') -eq 9 ]"
check "router-11 drops gateway to gateway (-i tr+ -o tr+)" "ip netns exec router-11 iptables -S FORWARD | grep -q -- '-i tr+ -o tr+ -j DROP'"
check "tgw-7 rp_filter 0" "[ \$(ip netns exec tgw-7 sysctl -n net.ipv4.conf.all.rp_filter) = 0 ]"
check "tr/ta MTU 1450" "ip -n router-11 link show tr-101 | grep -q 'mtu 1450' && ip -n tgw-7 link show ta-101 | grep -q 'mtu 1450'"

echo "== floating IP rules"
ip -n router-11 rule add from 192.168.11.10 lookup 77
autopref=$(ip -n router-11 rule show | awk '/from 192.168.11.10 lookup 77/ {sub(":", "", $1); print $1}')
check "a rule added without priority lands ahead of the gateway rule (pref $autopref)" "[ \"$autopref\" = 99 ]"
check "... and then takes A2 -> B1 to the floating IP table" "! png vmA2 192.168.2.10"
ip -n router-11 rule del from 192.168.11.10 lookup 77
ip -n router-11 rule add pref 32000 from 192.168.11.10 lookup 77
check "with create_floating.sh's pref 32000 A2 -> B1 goes through the gateway" "png vmA2 192.168.2.10"
ip -n router-11 rule del from 192.168.11.10 lookup 77

echo "== idempotency"
before=$(ip -n router-11 route show table 252; ip -n tgw-7 route show table all; ip -n tgw-7 rule show)
out=$(apply 1 g1-mesh.json)
after=$(ip -n router-11 route show table 252; ip -n tgw-7 route show table all; ip -n tgw-7 rule show)
check "same state again: ok and nothing changed" "[[ \"\$out\" == *\"'ok'\"* ]] && [ \"\$before\" = \"\$after\" ]"

echo "== no leak to router-0"
ip -n router-12 link set ns-112 down
ip -n router-12 addr flush dev ns-112
drops0=$(ip netns exec router-12 iptables -nvxL FORWARD | awk '$3 == "DROP" && $6 == "tr+" && $7 == "ti-+" {print $1}')
ip netns exec router-0 timeout 4 tcpdump -c 1 -ni any host 192.168.12.10 2>/dev/null >/tmp/tgw-leak.txt &
sleep 1; png vmA1 192.168.12.10; sleep 1; wait
drops1=$(ip netns exec router-12 iptables -nvxL FORWARD | awk '$3 == "DROP" && $6 == "tr+" && $7 == "ti-+" {print $1}')
check "subnet without gateway port here: dropped by -i tr+ -o ti-+ ($drops0 -> $drops1)" "[ \"$drops1\" -gt \"$drops0\" ]"
check "nothing reached router-0" "! grep -q 192.168.12.10 /tmp/tgw-leak.txt"
ip -n router-12 addr add 192.168.12.1/24 dev ns-112
ip -n router-12 link set ns-112 up
check "B2 back" "png vmA1 192.168.12.10"

echo "== router reclaim guard"
$K/clear_local_router.sh router-12 >/dev/null 2>&1
check "clear_local_router.sh keeps a router with a transit gateway attachment" "[ -f /var/run/netns/router-12 ]"
check "router_tgw_user names the attachment" "(cd $K && source ../cloudrc && source ./bridge_lib.sh && router_tgw_user router-12) | grep -q 'tr-102'"

echo "== g2 hub-spoke"
out=$(apply 2 g2-hubspoke.json)
check "g2 ok" "[[ \"\$out\" == *\"'ok'\"* ]]"
check "hub A -> spoke B" "png vmA1 192.168.2.10"
check "spoke C -> hub A" "png vmC1 192.168.1.10"
check "spoke B -> spoke C dropped" "! png vmB1 192.168.3.10"
check "router-12 blackholes C" "ip -n router-12 route show table 252 | grep -q '^blackhole 192.168.3.0/24'"
check "tgw-7 rule of ta-102 points to the spoke table" "ip -n tgw-7 rule show | grep -q 'iif ta-102 lookup 1001'"

echo "== g3 static routes"
out=$(apply 3 g3-static.json)
check "g3 ok" "[[ \"\$out\" == *\"'ok'\"* ]]"
check "blackhole 192.168.2.0/25 drops .20" "! png vmA1 192.168.2.20"
check "the /32 static route keeps .10" "png vmA1 192.168.2.10"
check "outside the blackhole .200 still works" "png vmA1 192.168.2.200"
check "host route normalized (no churn on re-apply)" "out2=\$(apply 3 g3-static.json); [[ \"\$out2\" == *\"'ok'\"* ]] && ip -n router-11 route show table 252 | grep -q '^192.168.2.10 via'"

echo "== g4 prefix filter"
out=$(apply 4 g4-prefix.json)
check "g4 ok" "[[ \"\$out\" == *\"'ok'\"* ]]"
check "propagated 192.168.2.0/24 works" "png vmA1 192.168.2.20"
check "not propagated 192.168.12.0/24 dropped" "! png vmA1 192.168.12.10"
check "router-11 nonat no longer has 192.168.12.0/24" "! ip netns exec router-11 ipset list nonat | grep -q 192.168.12.0/24"

echo "== older generation"
out=$(apply 0 g0-old.json)
check "generation 0 after 4 is dropped silently" "[ -z \"$out\" ] && [ \$(cat /opt/cloudland/cache/tgw/7/generation) = 4 ]"

echo "== g5 B detached"
out=$(apply 5 g5-detachB.json)
check "g5 ok with 2 attachments" "[[ \"\$out\" == *\"'ok' '-' '2'\"* ]]"
check "A1 -> B1 no longer reachable" "! png vmA1 192.168.2.10"
check "A1 -> C1 still works" "png vmA1 192.168.3.10"
check "router-11 nonat lost B" "! ip netns exec router-11 ipset list nonat | grep -q 192.168.2.0/24"
check "router-12 released (no instance user in this simulation)" "[ ! -f /var/run/netns/router-12 ]"
check "ta-102 gone" "! ip -n tgw-7 link show ta-102 >/dev/null 2>&1"

echo "== a VPC moving to another gateway, dispatches out of order"
jq '.tgw = 8 | .generation = 1 | .attachments |= map(select(.router == 11) | .id = 201) | .tables |= map(.routes |= map(select(.att != 103) | if .att == 101 then .att = 201 else . end))' $SIM/g5-detachB.json >$SIM/tgw8.json
out=$($K/apply_tgw.sh 8 1 <$SIM/tgw8.json 2>/dev/null)
check "gateway 8 takes router-11 first: $out" "[[ \"\$out\" == *\"'8' '1' 'ok'\"* ]] && ip -n router-11 link show tr-201 >/dev/null 2>&1"
jq '.generation = 6 | .attachments |= map(select(.router == 13)) | .tables |= map(.routes |= map(select(.att != 101)))' $SIM/g5-detachB.json >$SIM/g6-onlyC.json
out=$(apply 6 g6-onlyC.json)
check "gateway 7 drops router-11 afterwards: ok" "[[ \"\$out\" == *\"'7' '6' 'ok'\"* ]]"
check "tr-101 removed" "! ip -n router-11 link show tr-101 >/dev/null 2>&1"
check "gateway 8 keeps its veth, rule and routes on router-11" "ip -n router-11 link show tr-201 >/dev/null 2>&1 && ip -n router-11 rule show | grep -q '^100:' && ip -n router-11 route show table 252 | grep -q throw"
check "router-11 owner is gateway 8" "[ \"\$(cat /opt/cloudland/cache/router/router-11/tgw.owner)\" = '8 201' ]"
$K/apply_tgw.sh 8 2 <<<'{"tgw":8,"generation":2,"attachments":[],"tables":[]}' >/dev/null 2>&1

echo "== review fixes"
# A rule whose ta- is gone shows "[detached]" and must still be removed
ip -n tgw-7 rule add iif ta-999 lookup 1009
check "detached rule present before" "ip -n tgw-7 rule show | grep -q 'iif ta-999 \[detached\]'"
# A failed run: an unusable prefix fails the routes; the generation is recorded, the state is not
jq '.attachments |= map(if .id == 103 then .reachable += ["300.1.1.0/24"] else . end) | .generation = 7' $SIM/g6-onlyC.json >$SIM/g7-bad.json
out=$(apply 7 g7-bad.json)
check "an invalid prefix fails the run: $out" "[[ \"\$out\" == *\"'7' '7' 'error'\"* ]]"
check "the generation of the failed run is recorded" "[ \$(cat /opt/cloudland/cache/tgw/7/generation) = 7 ]"
check "the state of the failed run is not" "! grep -q 300.1.1.0 /opt/cloudland/cache/tgw/7/state.json"
out=$(apply 6 g6-onlyC.json)
check "the older generation 6 is now dropped" "[ -z \"\$out\" ]"
check "the detached rule was removed by the run" "! ip -n tgw-7 rule show | grep -q 'ta-999'"
# Left over of an attachment the recorded state does not know (a run that failed after plugging it in)
$K/create_local_router.sh router-14 >/dev/null 2>&1
ip link add tr-150 type veth peer name ta-150
ip link set tr-150 netns router-14
ip link set ta-150 netns tgw-7
mkdir -p /opt/cloudland/cache/router/router-14
echo "7 150" >/opt/cloudland/cache/router/router-14/tgw.owner
echo "192.168.3.0/24 10.99.0.0/16" >/opt/cloudland/cache/router/router-14/tgw.nonat
ip netns exec router-14 ipset add nonat 10.99.0.0/16 -exist
ip netns exec router-14 ipset add nonat 192.168.3.0/24 -exist
# 10.99.0.0/16 is also a VPN prefix of a VPN gateway route set on router-14
mkdir -p /opt/cloudland/cache/router/router-14/vpn-5
echo 3 >/opt/cloudland/cache/router/router-14/vpn-5/vrrp_id
echo "10.99.0.0/16" >/opt/cloudland/cache/router/router-14/vpn-5/nonat.current
ip netns exec router-14 iptables -I FORWARD 1 -i tr+ -o ti-+ -j DROP
# A large state: compressed with a "gz:" prefix, as clapi sends it above 64 KiB
jq -c '.generation = 8' $SIM/g6-onlyC.json | gzip | base64 -w0 | sed 's/^/gz:/' >$SIM/g8-gz.txt
out=$($K/apply_tgw.sh 7 8 <$SIM/g8-gz.txt 2>/dev/null)
check "a gz: state applies: $out" "[[ \"\$out\" == *\"'7' '8' 'ok' '-' '1'\"* ]]"
check "the left over tr-150 / ta-150 found through tgw.owner are removed" "! ip -n router-14 link show tr-150 >/dev/null 2>&1 && ! ip -n tgw-7 link show ta-150 >/dev/null 2>&1"
check "tgw.owner of router-14 removed" "[ ! -f /opt/cloudland/cache/router/router-14/tgw.owner ]"
check "the nonat entry only the gateway had is removed" "! ip netns exec router-14 ipset list nonat | grep -q 192.168.3.0/24"
check "the nonat entry the VPN gateway owns too is kept" "ip netns exec router-14 ipset list nonat | grep -q 10.99.0.0/16"
check "router-14 forward rules removed" "! ip netns exec router-14 iptables -S FORWARD | grep -q 'tr+'"
check "router-14 kept by its VPN gateway" "[ -f /var/run/netns/router-14 ]"
rm -rf /opt/cloudland/cache/router/router-14/vpn-5
$K/clear_local_router.sh router-14 >/dev/null 2>&1

echo "== g6 empty"
out=$($K/apply_tgw.sh 7 9 <<<'{"tgw":7,"generation":9,"attachments":[],"tables":[]}' 2>/dev/null)
check "empty state ok with 0 attachments" "[[ \"\$out\" == *\"'ok' '-' '0'\"* ]]"
check "tgw-7 and tgw-8 removed" "[ ! -f /var/run/netns/tgw-7 ] && [ ! -f /var/run/netns/tgw-8 ]"
check "state removed, generation kept" "[ ! -f /opt/cloudland/cache/tgw/7/state.json ] && [ \$(cat /opt/cloudland/cache/tgw/7/generation) = 9 ]"
out=$(apply 3 g3-static.json)
check "a delayed older state after the removal is dropped" "[ -z \"\$out\" ] && [ ! -f /var/run/netns/tgw-7 ]"
check "no tgw policy rule left in surviving routers" "! ip netns list | awk '{print \$1}' | grep '^router-[1-9]' | while read r; do ip -n \$r rule show | grep -q '^100:' && echo x; done | grep -q x"

echo "== locks"
check "router locks released" "! fuser /opt/cloudland/run/lock/*.lock 2>/dev/null | grep -q '[0-9]'"

echo "RESULT pass=$pass fail=$failn"
