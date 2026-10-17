#!/bin/bash
# create_vpn_ipsec_conf.sh (scripts/kvm): an XFRM interface no tunnel uses any more is deleted only after charon
# has dropped its tunnel (configuration reloaded, the SA terminated), and a deletion that hangs in the kernel does
# not hold the script (2026-10-16 supplementary run, R2-14). The router and charon are stand-ins: a fake ip keeps
# the XFRM devices in a file and records deletions, the charon calls are recorded in the same order file.
# Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vpn/ipsec-conf-order.sh
# KVM=<dir> runs another copy of scripts/kvm (for example the scripts of an older commit).
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
KVM=${KVM:-$R/scripts/kvm}
W=$(mktemp -d)
trap 'rm -rf $W' EXIT
mkdir -p $W/kvm $W/bin $W/netns $W/router/router-9/vpn-5
touch $W/netns/router-9 $W/devs
sed 's/\r$//' $KVM/create_vpn_ipsec_conf.sh | sed "s#/var/run/netns/#$W/netns/#" > $W/kvm/create_vpn_ipsec_conf.sh
sed 's/\r$//' $KVM/vpn_lib.sh > $W/kvm/vpn_lib.sh
cat >> $W/kvm/vpn_lib.sh <<EOF
# stand-ins for charon, after the real library
vpn_runs_tunnels() { return 0; }
vpn_charon_alive() { return 0; }
vpn_load_swanctl() { echo load >> $W/order; }
vpn_swanctl() { shift 2; echo "swanctl \$*" >> $W/order; }
vpn_initiate_bg() { :; }
vpn_start_charon() { echo "start charon" >> $W/order; }
EOF
cat > $W/cloudrc <<EOF
router_dir=$W/router
lb_lock_file=$W/lb.lock
log_debug() { shift; echo "\$*" >> $W/log; }
die() { echo "\$*" >&2; exit 1; }
EOF
cat > $W/bin/ip <<EOF
#!/bin/bash
# fake ip: the XFRM devices of the router are the lines of $W/devs
if [ "\$1" = netns ] && [ "\$2" = exec ]; then shift 3; [ "\$1" = ip ] && shift; fi
case "\$*" in
"-o link show type xfrm") while read -r d; do echo "9: \$d@lo: <NOARP,UP,LOWER_UP> mtu 1360"; done < $W/devs ;;
"link show "*) grep -qxF "\$3" $W/devs; exit ;;
"link add "*) echo "\$3" >> $W/devs ;;
"link del "*) [ -f $W/hang ] && sleep 30; grep -vxF "\$3" $W/devs > $W/devs.n; mv $W/devs.n $W/devs; echo "del \$3" >> $W/order ;;
esac
exit 0
EOF
chmod +x $W/bin/ip
export PATH=$W/bin:$PATH NODE_ID=1

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }
# tunnel <name> <if_id>: one BGP tunnel entry
tunnel() { echo "{\"name\":\"$1\",\"if_id\":$2,\"local_addr\":\"52.0.0.1\",\"remote_gateway\":\"52.0.0.$((2 + $2))\",\"route_mode\":\"bgp\",\"local_cidrs\":[],\"remote_cidrs\":[],\"psk\":\"k\",\"ike_proposal\":\"aes256-sha256-modp2048\",\"esp_proposal\":\"aes256-sha256-modp2048\",\"ike_lifetime\":86400,\"esp_lifetime\":3600,\"dpd_action\":\"restart\",\"dpd_delay\":10,\"initiator\":true,\"tunnel_local_ip\":\"169.254.1.$((4 * $2 + 2))\",\"tunnel_peer_ip\":\"169.254.1.$((4 * $2 + 1))\"}"; }
# run <tunnel>...: apply the connection list, print the recorded calls on one line
run() {
    local list=$(IFS=,; echo "$*")
    rm -f $W/order
    echo "{\"ha_mode\":\"active_standby\",\"floating_ip\":\"52.0.0.1\",\"connections\":[$list]}" | bash $W/kvm/create_vpn_ipsec_conf.sh 9 5 >/dev/null 2>$W/err
    tr '\n' ';' < $W/order | sed 's/;$//'
}
# position <pattern>: line of the first recorded call that matches, 0 when none
position() { local n; n=$(grep -n -m1 -- "$1" $W/order | cut -d: -f1); echo ${n:-0}; }
devs() { sort $W/devs | tr '\n' ' ' | sed 's/ $//'; }

run "$(tunnel c1-t1 11)" "$(tunnel c1-t2 12)" >/dev/null
check "two tunnels: two XFRM interfaces" "$(devs)" "ipsec-11 ipsec-12"

out=$(run "$(tunnel c1-t1 11)")
t=$(position "swanctl --terminate --ike c1-t2"); d=$(position "del ipsec-12"); l=$(position "^load")
check "a tunnel removed: its interface deleted" "$(devs)" "ipsec-11"
check "  the configuration reloaded before the deletion ($out)" "$([ $l -gt 0 ] && [ $l -lt $d ] && echo yes)" yes
check "  its SA terminated before the deletion" "$([ $t -gt 0 ] && [ $t -lt $d ] && echo yes)" yes

run "$(tunnel c1-t1 11)" "$(tunnel c1-t2 12)" >/dev/null
out=$(run "$(tunnel c1-t1 11)" "$(tunnel c1-t2 13)")
t=$(position "swanctl --terminate --ike c1-t2"); d=$(position "del ipsec-12")
check "a tunnel re-created on a new if_id: the new interface, the old one deleted" "$(devs)" "ipsec-11 ipsec-13"
check "  the SA of the changed tunnel terminated before the old interface goes ($out)" "$([ $t -gt 0 ] && [ $t -lt $d ] && echo yes)" yes

touch $W/hang
rm -f $W/log
t0=$(date +%s)
out=$(run "$(tunnel c1-t1 11)")
el=$(( $(date +%s) - t0 ))
rm -f $W/hang
check "a deletion that hangs: the script goes on within 15 s (took $el s)" "$([ $el -lt 15 ] && echo yes)" yes
check "  the configuration was still reloaded and the SA terminated ($out)" "$(position "swanctl --terminate --ike c1-t2" | grep -qv '^0$' && position '^load' | grep -qv '^0$' && echo yes)" yes
grep -q "deleting ipsec-13 did not finish in 10 s" $W/log 2>/dev/null; check "  logged" "$?" 0

echo "passed $pass, failed $fail"
[ $fail = 0 ]
