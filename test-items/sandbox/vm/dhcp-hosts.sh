#!/bin/bash
# set_host.sh / del_host.sh (scripts/kvm), the DHCP bindings of a VPC subnet ("<mac>,<name>.<domain>,<ip>" lines in
# dhcp_hosts): a binding is dropped by its address and MAC fields compared whole, never by a pattern on the line,
# which also took a guest whose name holds the address (ip-192-168-30-5; the dots match any character). Since
# 2026-10-16 del_host.sh runs after every migration too (R2-13), so this was found by the code review of
# 2026-10-17. dhcp_release (ip netns exec) and dnsmasq are stand-ins.
# Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vm/dhcp-hosts.sh
# KVM=<dir> runs another copy of scripts/kvm.
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
KVM=${KVM:-$R/scripts/kvm}
W=$(mktemp -d)
trap 'rm -rf $W' EXIT
mkdir -p $W/kvm $W/bin $W/cache/router/router-9/100
for f in set_host.sh del_host.sh; do sed 's/\r$//' $KVM/$f > $W/kvm/$f; done
cat > $W/cloudrc <<EOF
cache_dir=$W/cache
cloud_domain=cloudland.local
die() { echo "\$*" >&2; exit 1; }
EOF
printf '#!/bin/bash\necho "$*" >> %s/released\n' $W > $W/bin/ip
chmod +x $W/bin/ip
export PATH=$W/bin:$PATH
H=$W/cache/router/router-9/100/dhcp_hosts

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }
lines() { cut -d, -f1,3 $H | tr '\n' ' '; }

printf '52:54:00:00:00:0a,ip-192-168-30-5.cloudland.local,192.168.30.7\n52:54:00:00:00:0b,old.cloudland.local,192.168.30.5\n' > $H
bash $W/kvm/set_host.sh 9 100 52:54:00:00:00:0c a 192.168.30.5/24 >/dev/null
check "set_host: the old binding of the address goes, the guest named ip-192-168-30-5 stays" "$(lines)" "52:54:00:00:00:0a,192.168.30.7 52:54:00:00:00:0c,192.168.30.5 "

bash $W/kvm/del_host.sh 9 100 52:54:00:00:00:0c 192.168.30.5 >/dev/null
check "del_host: only the binding of that MAC and address goes" "$(lines)" "52:54:00:00:00:0a,192.168.30.7 "
grep -q "dhcp_release ns-100 192.168.30.5 52:54:00:00:00:0c" $W/released; check "  the lease released" "$?" 0

rm -rf $W/cache/router/router-9/200
bash $W/kvm/del_host.sh 9 200 52:54:00:00:00:0d 192.168.31.5 >/dev/null 2>&1
check "del_host on a subnet without bindings: no file left behind" "$(ls $W/cache/router/router-9/200 2>/dev/null | wc -l)" 0

# second code review of 2026-10-17: an address changed on the same NIC leaves one binding for the MAC; the file stays
# readable by dnsmasq (nobody) whatever the umask; no temporary file left
(umask 077; bash $W/kvm/set_host.sh 9 100 52:54:00:00:00:0a b 192.168.30.9/24 >/dev/null)
check "set_host with a new address for a MAC: the binding of its old address goes" "$(lines)" "52:54:00:00:00:0a,192.168.30.9 "
check "  the file is 644 under umask 077" "$(stat -c %a $H)" 644
(umask 077; bash $W/kvm/del_host.sh 9 100 52:54:00:00:00:0e 192.168.30.200 >/dev/null)
check "  del_host keeps it 644 too" "$(stat -c %a $H)" 644
check "  no temporary file left in the subnet directory" "$(ls $(dirname $H) | tr '\n' ' ')" "dhcp_hosts "

echo "passed $pass, failed $fail"
[ $fail = 0 ]
