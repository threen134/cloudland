#!/bin/bash
# Apply a state produced by the clapi service layer (TestTgwPG writes pg-*.json) on a fresh simulated node:
# routers and one VM per own network are built from the state itself, then every VM pair is probed and the
# result compared with the reachable / blackhole lists of the state.
SIM="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(cd "$SIM/../../../scripts" && pwd)"
pass=0; failn=0
for ns in $(ip netns list | awk '{print $1}'); do ip netns del $ns; done
rm -rf /opt/cloudland
mkdir -p /opt/cloudland/run /opt/cloudland/log /opt/cloudland/cache/router
cp -r $SRC /opt/cloudland/scripts
find /opt/cloudland/scripts -type f -exec sed -i 's/\r$//' {} +
chmod +x /opt/cloudland/scripts/kvm/*.sh
printf 'NODE_ID=1\nvxlan_interface=eth0\n' >/opt/cloudland/scripts/cloudrc.local
export PATH=/opt/fakebin:$PATH
K=/opt/cloudland/scripts/kvm
ip netns add router-0
ip -n router-0 link set lo up
ip -n router-0 link add up0 type dummy
ip -n router-0 addr add 203.0.113.1/24 dev up0
ip -n router-0 link set up0 up
ip -n router-0 route add default via 203.0.113.254

for file in "$@"; do
    state=$SIM/$file
    tgw=$(jq -r .tgw $state)
    gen=$(jq -r .generation $state)
    echo "== $file (gateway $tgw, generation $gen)"
    vni=500
    # routers and VMs from the throw lists (the VPC's own networks)
    for r in $(jq -r '.attachments[].router' $state); do
        [ -f /var/run/netns/router-$r ] || $K/create_local_router.sh router-$r >/dev/null 2>&1
        for net in $(jq -r --argjson r $r '.attachments[] | select(.router == $r) | .throw[]' $state | grep -v '^192.168.196.0/24$'); do
            vm=vm-$r-${net%%/*}
            [ -f /var/run/netns/$vm ] && continue
            vni=$((vni + 1))
            base=${net%.*}
            ip link add ns-$vni type veth peer name v$vni
            ip link set ns-$vni netns router-$r
            ip netns add $vm
            ip link set v$vni netns $vm
            ip -n router-$r addr add $base.1/${net#*/} dev ns-$vni
            ip -n router-$r link set ns-$vni up
            ip -n $vm link set lo up
            ip -n $vm addr add $base.10/${net#*/} dev v$vni
            ip -n $vm link set v$vni up
            ip -n $vm route add default via $base.1
            ip netns exec router-$r ipset add nonat $net -exist
        done
    done
    out=$($K/apply_tgw.sh $tgw $gen <$state 2>/dev/null)
    if [[ "$out" == *"'ok'"* ]]; then echo "PASS apply: $out"; pass=$((pass + 1)); else echo "FAIL apply: $out"; failn=$((failn + 1)); fi
    # every VM pair: reachable when the target network is in the source router's reachable list (and the reverse
    # holds for the reply), otherwise dropped
    for src in $(jq -r '.attachments[].router' $state); do
        for dst in $(jq -r '.attachments[].router' $state); do
            [ "$src" = "$dst" ] && continue
            for net in $(jq -r --argjson r $dst '.attachments[] | select(.router == $r) | .throw[]' $state | grep -v '^192.168.196.0/24$'); do
                srcnet=$(jq -r --argjson r $src '.attachments[] | select(.router == $r) | .throw[0]' $state)
                fwd=$(jq -r --argjson r $src --arg n $net '.attachments[] | select(.router == $r) | .reachable | index($n) != null' $state)
                back=$(jq -r --argjson r $dst --arg n $srcnet '.attachments[] | select(.router == $r) | .reachable | index($n) != null' $state)
                want=fail; [ "$fwd" = true ] && [ "$back" = true ] && want=ok
                if ip netns exec vm-$src-${srcnet%%/*} ping -c 1 -W 1 ${net%.*}.10 >/dev/null 2>&1; then got=ok; else got=fail; fi
                if [ "$got" = "$want" ]; then pass=$((pass + 1)); echo "PASS router-$src -> $net: $got"; else failn=$((failn + 1)); echo "FAIL router-$src -> $net: got $got, want $want"; fi
            done
        done
    done
done
echo "RESULT pass=$pass fail=$failn"
