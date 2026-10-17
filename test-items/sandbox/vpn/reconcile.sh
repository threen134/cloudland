#!/bin/bash
# vpn_tunnel_reconcile (scripts/kvm/vpn_lib.sh): when it takes the IKE SA of a tunnel down and when it only
# initiates the missing children (2026-10-16 supplementary run, R2-11). swanctl and charon are stand-ins: the
# installed selectors come from a fake listing, vpn_tunnel_run only records how it was called.
# Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vpn/reconcile.sh
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
W=$(mktemp -d)
trap 'rm -rf $W' EXIT
sed 's/\r$//' $R/scripts/kvm/vpn_lib.sh > $W/vpn_lib.sh
source $W/vpn_lib.sh
log_debug() { echo "$2" >> $W/log; }
vpn_tunnel_sas() { cat $W/sas; }
vpn_tunnel_run() { echo "run terminate=$4" >> $W/calls; }

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }

# conf <start_action> <local remote>...: swanctl.conf with one connection c1-t1, a child per pair
conf() {
    local action=$1 i=0; shift
    { echo "connections {"; echo "    c1-t1 {"; echo "        children {"
      while [ $# -gt 0 ]; do
          echo "            net$i {"; echo "                local_ts = $1"; echo "                remote_ts = $2"
          echo "                start_action = $action"; echo "                close_action = none"; echo "            }"
          i=$((i + 1)); shift 2
      done
      echo "        }"; echo "    }"; echo "}"; } > $W/swanctl.conf
}
# sas <local remote>...: a swanctl --list-sas listing with one installed child per pair
sas() {
    local i=0
    { echo "c1-t1: #1, ESTABLISHED, IKEv2, 1_i* 2_r"
      while [ $# -gt 0 ]; do
          echo "  net$i: #$((i + 10)), reqid $((i + 1)), INSTALLED, TUNNEL, ESP:AES_CBC-256/HMAC_SHA2_256_128"
          echo "    local  $1"; echo "    remote $2"; i=$((i + 1)); shift 2
      done; } > $W/sas
}
run() { rm -f $W/calls $W/reconcile-c1-t1; vpn_tunnel_reconcile router-1 $W c1-t1; cat $W/calls 2>/dev/null | tr '\n' ' ' | sed 's/ $//'; }

conf start 10.1.0.0/24 10.9.0.0/24 10.2.0.0/24 10.9.0.0/24
sas 10.1.0.0/24 10.9.0.0/24 10.2.0.0/24 10.9.0.0/24
check "all children installed: nothing" "$(run)" ""

sas 10.1.0.0/24 10.9.0.0/24
check "a child missing (the peer lacks a new subnet of ours), initiator: initiate only" "$(run)" "run terminate=no"
grep -q "lacks children for \[10.2.0.0/24|10.9.0.0/24\]" $W/log; check "  logged as missing children" "$?" 0

conf start 10.1.0.0/24 10.9.0.0/24
sas 10.1.0.0/24 10.9.0.0/24 10.3.0.0/24 10.9.0.0/24
check "an installed child the configuration no longer has: terminate and initiate" "$(run)" "run terminate=yes"

conf start 10.1.0.0/24 10.9.0.0/24
sas 10.4.0.0/24 10.9.0.0/24
check "old selectors only (route mode or networks changed): terminate and initiate" "$(run)" "run terminate=yes"

: > $W/sas
check "nothing installed, initiator: initiate" "$(run)" "run terminate=no"

conf none 10.1.0.0/24 10.9.0.0/24 10.2.0.0/24 10.9.0.0/24
sas 10.1.0.0/24 10.9.0.0/24
check "a child missing, responder only: nothing (only the peer can bring it up)" "$(run)" ""
: > $W/sas
check "nothing installed, responder only: nothing" "$(run)" ""
sas 10.5.0.0/24 10.9.0.0/24
check "stale selectors, responder only: terminate (no initiate here)" "$(run)" "run terminate=yes"

conf start 10.1.0.0/24 10.9.0.0/24 10.2.0.0/24 10.9.0.0/24
sas 10.1.0.0/24 10.9.0.0/24
rm -f $W/calls; touch $W/reconcile-c1-t1; vpn_tunnel_reconcile router-1 $W c1-t1
check "at most once a minute" "$(cat $W/calls 2>/dev/null)" ""

echo "reconcile: $pass passed, $fail failed"
[ $fail = 0 ]
