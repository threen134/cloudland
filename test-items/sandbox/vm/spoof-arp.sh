#!/bin/bash
# send_spoof_arp.py (scripts/kvm): one gratuitous ARP reply from the guest's MAC, broadcast, sender and target both the
# guest's address, with the Python standard library only (2026-10-17 supplementary run, R2-17: the earlier version
# imported scapy, which no node has, and failed every time). A veth pair in a namespace of its own; tcpdump on the
# other end. Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vm/spoof-arp.sh
# KVM=<dir> runs another copy of scripts/kvm.
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
KVM=${KVM:-$R/scripts/kvm}
W=$(mktemp -d)
NS=sarp-$$
trap 'ip netns del $NS 2>/dev/null; rm -rf $W' EXIT
sed 's/\r$//' $KVM/send_spoof_arp.py > $W/send_spoof_arp.py
ip netns add $NS
ip -n $NS link add sa0 type veth peer name sa1
ip -n $NS link set sa0 up; ip -n $NS link set sa1 up

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }

ip netns exec $NS timeout 5 tcpdump -l -e -n -c 1 -i sa1 arp > $W/cap 2>/dev/null &
sleep 1
ip netns exec $NS python3 -I $W/send_spoof_arp.py sa0 10.9.9.9 52:54:00:12:34:56 > $W/out 2> $W/err; rc=$?
wait
cap=$(cat $W/cap)
check "exit status 0" "$rc" 0
check "nothing on stdout (the node scripts read it as callbacks)" "$(cat $W/out)" ""
check "the frame: from the guest's MAC to broadcast, ARP" "$(grep -c '52:54:00:12:34:56 > ff:ff:ff:ff:ff:ff, ethertype ARP' <<<"$cap")" 1
check "  a reply: 10.9.9.9 is at the guest's MAC" "$(grep -c 'Reply 10.9.9.9 is-at 52:54:00:12:34:56' <<<"$cap")" 1
[ -s $W/err ] && echo "     stderr: $(head -c 200 $W/err)"

ip netns exec $NS python3 -I $W/send_spoof_arp.py no-such-if 10.9.9.9 52:54:00:12:34:56 > $W/out 2> $W/err; rc=$?
check "an interface that does not exist: exit status 0" "$rc" 0
check "  reported on stderr" "$(grep -c 'Failed to send a gratuitous ARP' $W/err)" 1

echo "passed $pass, failed $fail"
[ $fail = 0 ]
