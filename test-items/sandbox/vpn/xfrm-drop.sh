#!/bin/bash
# vpn_xfrm_drop (scripts/kvm/vpn_lib.sh): removes the XFRM states and policies of some interface ids from a router,
# leaving the others. clear_vpn_gateway.sh uses it when another gateway directory exists on the router (a replacement
# created before the clear ran), where flushing everything would cut the other gateway (second code review of
# 2026-10-17; R2-14: deleting an interface under a leftover SA can hang). Real ip xfrm in a temporary network namespace.
# Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vpn/xfrm-drop.sh
# KVM=<dir> runs another copy of scripts/kvm.
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
KVM=${KVM:-$R/scripts/kvm}
W=$(mktemp -d)
NS=xfrmdrop$$
trap 'ip netns del $NS 2>/dev/null; rm -rf $W' EXIT
sed 's/\r$//' $KVM/vpn_lib.sh > $W/vpn_lib.sh
source $W/vpn_lib.sh
ip netns add $NS || exit 1

K=0x00112233445566778899aabbccddeeff A=0x00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff
sa() { ip netns exec $NS ip xfrm state add src $1 dst $2 proto esp spi $3 reqid 1 mode tunnel enc 'cbc(aes)' $K auth-trunc 'hmac(sha256)' $A 128 ${4:+if_id $4}; }
sp() { ip netns exec $NS ip xfrm policy add src $1 dst $2 dir $3 tmpl src 10.0.0.1 dst 10.0.0.2 proto esp reqid 1 mode tunnel ${4:+if_id $4}; }
sa 10.0.0.1 10.0.0.2 0x101 14; sa 10.0.0.2 10.0.0.1 0x102 14
sa 10.0.0.1 10.0.0.3 0x201 25
sa 10.0.0.1 10.0.0.4 0x301
sp 192.168.1.0/24 172.16.0.0/24 out 14; sp 172.16.0.0/24 192.168.1.0/24 fwd 14; sp 172.16.0.0/24 192.168.1.0/24 in 14
sp 192.168.1.0/24 172.17.0.0/24 out 25
sp 192.168.1.0/24 172.18.0.0/24 out

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }
spis() { ip netns exec $NS ip xfrm state | awk '/^\tproto / {print $4}' | sort | tr '\n' ' '; }
dsts() { ip netns exec $NS ip xfrm policy | awk '/^src / {print $4}' | sort | tr '\n' ' '; }

check "before: four states, five policies" "$(spis)/$(dsts)" "0x00000101 0x00000102 0x00000201 0x00000301 /172.16.0.0/24 172.17.0.0/24 172.18.0.0/24 192.168.1.0/24 192.168.1.0/24 "
vpn_xfrm_drop $NS 14
check "vpn_xfrm_drop 14: the two states and three policies on if_id 14 go, the others stay" "$(spis)/$(dsts)" "0x00000201 0x00000301 /172.17.0.0/24 172.18.0.0/24 "
vpn_xfrm_drop $NS 99 abc
check "  an id with nothing on it (and a bad one): nothing changes" "$(spis)/$(dsts)" "0x00000201 0x00000301 /172.17.0.0/24 172.18.0.0/24 "
vpn_xfrm_drop $NS
check "  no id at all: nothing changes" "$(spis)/$(dsts)" "0x00000201 0x00000301 /172.17.0.0/24 172.18.0.0/24 "

echo "passed $pass, failed $fail"
[ $fail = 0 ]
