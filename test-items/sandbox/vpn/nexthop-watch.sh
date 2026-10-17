#!/bin/bash
# vpn_nexthop_watch.sh (scripts/kvm, F7 of the VPN plan): a gateway node that stops answering is marked down within
# about a second. Unreachable only briefly, it comes back after ten answers; unreachable for a while (as a reboot is),
# at once only where the traffic would not go to it (a hop before it is up in every list, and it is not among the
# hops of an ecmp prefix), otherwise after answering for the hold time, a lost probe allowed (2026-10-17
# supplementary run, R2-18 and the two code reviews of that day). The states go when the loop ends by itself; a loop
# started later dates an inherited "down" by its file. ping is a stand-in answering for the addresses listed in $W/up;
# vpn_apply_nexthops only records the states.
# Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vpn/nexthop-watch.sh
# KVM=<dir> runs another copy of scripts/kvm.
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
KVM=${KVM:-$R/scripts/kvm}
W=$(mktemp -d)
cleanup() { [ -n "$pid" ] && kill $pid 2>/dev/null; wait $pid 2>/dev/null; rm -rf $W; }
trap cleanup EXIT
mkdir -p $W/kvm $W/bin $W/up $W/state $W/router/router-1/vpn-5
sed 's/\r$//' $KVM/vpn_nexthop_watch.sh > $W/kvm/vpn_nexthop_watch.sh
sed 's/\r$//' $KVM/vpn_lib.sh > $W/kvm/vpn_lib.sh
cat >> $W/kvm/vpn_lib.sh <<EOF
# stand-ins, after the real library (which sets its own state directory)
vpn_nexthop_state=$W/state
vpn_nexthop_lock=$W/lock
vpn_nexthop_watch_alive() { return 1; }
vpn_apply_nexthops() { echo "\$(cat $W/state/10.0.0.1 2>/dev/null)" >> $W/applied; }
EOF
cat > $W/cloudrc <<EOF
router_dir=$W/router
vpn_nexthop_hold=8
vpn_nexthop_hold_after=2
log_debug() { shift; echo "\$*" >> $W/log; }
EOF
cat > $W/bin/ping <<EOF
#!/bin/bash
for a in "\$@"; do host=\$a; done
[ -f $W/up/\$host ]
EOF
chmod +x $W/bin/ping
export PATH=$W/bin:$PATH
V=$W/router/router-1/vpn-5
hosts() { printf '192.168.196.3 10.0.0.1\n192.168.196.2 10.0.0.2\n' > $V/nexthop_hosts; }
first_a() { echo "172.31.3.0/24 0 192.168.196.3,192.168.196.2" > $V/nexthops.new; mv $V/nexthops.new $V/nexthops; }
first_b() { echo "172.31.3.0/24 0 192.168.196.2,192.168.196.3" > $V/nexthops.new; mv $V/nexthops.new $V/nexthops; }
ecmp_ab() { echo "172.31.3.0/24 1 192.168.196.2,192.168.196.3" > $V/nexthops.new; mv $V/nexthops.new $V/nexthops; }
hosts; first_a
touch $W/up/10.0.0.1 $W/up/10.0.0.2

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }
state() { cat $W/state/${2:-10.0.0.1} 2>/dev/null || echo none; }
# waitfor <state> <seconds> [host]: until the host (A, 10.0.0.1, by default) has that state, or the time is up
waitfor() { local i; for i in $(seq $(( $2 * 10 ))); do [ "$(state x $3)" = "$1" ] && return 0; sleep 0.1; done; return 1; }
# outage <seconds>: A stops answering, is marked down, and stays unreachable that long in all
outage() { rm $W/up/10.0.0.1; waitfor down 3; sleep $1; }

bash $W/kvm/vpn_nexthop_watch.sh & pid=$!
sleep 2
check "both nodes answer: nothing marked" "$(state)" none

rm $W/up/10.0.0.1
waitfor down 3; check "A (first in the list) stops answering: down within about a second" "$(state)" down
grep -q "does not answer" $W/log; check "  logged" "$?" 0
touch $W/up/10.0.0.1; t0=$(date +%s)
waitfor up 8; t=$(( $(date +%s) - t0 ))
check "  unreachable only about a second (a blip): up after the ten answers though first in the list (took $t s)" "$(state)/$([ $t -le 6 ] && echo quick)" up/quick

outage 3; touch $W/up/10.0.0.1
sleep 5
check "A unreachable for 3 s (the hold applies from 2 s here), answers for 5 s, still first in the list: still down" "$(state)" down
first_a; sleep 2
check "clapi pushes the list with A still first (just restarted, master unknown): still down" "$(state)" down

first_b
waitfor up 3; check "clapi pushes the list with B first (and up): A comes back at once, as a fallback" "$(state)" up

outage 3; touch $W/up/10.0.0.1; t0=$(date +%s)
waitfor up 8; t=$(( $(date +%s) - t0 ))
check "A down for 3 s and back while B, before it in the list, is up: up after the ten answers (took $t s)" "$(state)/$([ $t -le 6 ] && echo quick)" up/quick

# B first but down too: the traffic would go to A, though it is not first
rm $W/up/10.0.0.2; waitfor down 3 10.0.0.2
outage 3; touch $W/up/10.0.0.1; t0=$(date +%s)
sleep 5
check "A not first, but B before it is down: A still down after answering 5 s" "$(state)" down
waitfor up 10; t=$(( $(date +%s) - t0 ))
check "  up after the hold time (8 s here; took $t s)" "$(state)/$([ $t -ge 8 ] && [ $t -le 11 ] && echo in-time)" up/in-time
touch $W/up/10.0.0.2; waitfor up 12 10.0.0.2

ecmp_ab
outage 3; touch $W/up/10.0.0.1; t0=$(date +%s)
sleep 5; rm $W/up/10.0.0.1; sleep 0.4; touch $W/up/10.0.0.1
waitfor up 15; t=$(( $(date +%s) - t0 ))
check "A among the hops of an ecmp prefix: up after the hold time, a lost probe not restarting it (took $t s)" "$(state)/$([ $t -ge 8 ] && [ $t -le 11 ] && echo in-time)" up/in-time

# The loop ends by itself once no gateway routes are left: the states go with it
rm $W/up/10.0.0.1; waitfor down 3
rm -f $V/nexthop_hosts $V/nexthops
for i in $(seq 150); do kill -0 $pid 2>/dev/null || break; sleep 0.1; done
kill -0 $pid 2>/dev/null; check "no gateway routes left: the loop ends within about 10 s" "$?" 1
check "  and removes the states (a later loop would hold a node that answered all along)" "$(ls $W/state | grep -c '^10\.')" 0

# A loop started later dates a "down" it finds by the file: down an hour ago and first in the list, it is held
touch $W/up/10.0.0.1; hosts; first_a
echo down > $W/state/10.0.0.1; touch -d '-1 hour' $W/state/10.0.0.1
bash $W/kvm/vpn_nexthop_watch.sh & pid=$!
sleep 5
check "a new loop finds A down since an hour ago, first in the list: still down after 5 s of answers" "$(state)" down
kill $pid; wait $pid 2>/dev/null
echo down > $W/state/10.0.0.1
bash $W/kvm/vpn_nexthop_watch.sh & pid=$!
waitfor up 8
check "  marked down just before the loop started (a blip): up after the ten answers" "$(state)" up

echo "passed $pass, failed $fail"
[ $fail = 0 ]
