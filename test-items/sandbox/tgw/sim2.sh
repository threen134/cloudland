#!/bin/bash
# Concurrency of apply_tgw.sh with clear_local_router.sh / create_local_router.sh on the same routers.
SIM="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
K=/opt/cloudland/scripts/kvm
export PATH=/opt/fakebin:$PATH
ok=0; bad=0
for i in $(seq 1 10); do
    $K/apply_tgw.sh 7 $((100 + 2 * i)) <<<"{\"tgw\":7,\"generation\":$((100 + 2 * i)),\"attachments\":[],\"tables\":[]}" >/dev/null 2>&1
    for r in 11 12 13; do $K/clear_local_router.sh router-$r >/dev/null 2>&1; done
    gen=$((101 + 2 * i))
    jq ".generation = $gen" $SIM/g1-mesh.json >/tmp/g.json
    $K/apply_tgw.sh 7 $gen </tmp/g.json >/tmp/out.$i 2>/dev/null &
    $K/clear_local_router.sh router-12 >/dev/null 2>&1 &
    $K/create_local_router.sh router-13 >/dev/null 2>&1 &
    $K/clear_local_router.sh router-13 >/dev/null 2>&1 &
    wait
    good=true
    for a in "11 101" "12 102" "13 103"; do
        set -- $a
        ip -n router-$1 link show tr-$2 >/dev/null 2>&1 || good=false
        ip -n router-$1 rule show | grep -q '^100:' || good=false
    done
    grep -q "'ok'" /tmp/out.$i || good=false
    if $good; then ok=$((ok + 1)); else bad=$((bad + 1)); echo "round $i: $(cat /tmp/out.$i)"; ip netns list; fi
done
echo "RESULT rounds ok=$ok bad=$bad"
