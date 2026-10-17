#!/bin/bash
# finish_source_migration.sh (scripts/kvm) releases the DHCP host and lease of each interface of the migrated
# instance on the source (2026-10-16 supplementary run, R2-13): without it the dnsmasq of the source keeps the lease
# for 2 hours and the next instance there that gets the address is handed a dynamic one. The script runs in a copy of
# the scripts with stand-ins for cloudrc, storage_lib.sh, virsh and the scripts it calls; del_host.sh records its
# arguments. Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vm/finish-source-dhcp.sh
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
W=$(mktemp -d)
trap 'rm -rf $W' EXIT
mkdir -p $W/scripts/kvm $W/bin $W/cache/xml/inst-7 $W/cache/router/router-3/4100 $W/cache/router/router-3/4200
sed 's/\r$//' $R/scripts/kvm/finish_source_migration.sh > $W/scripts/kvm/finish_source_migration.sh
cat > $W/scripts/cloudrc <<EOF
cache_dir=$W/cache
xml_dir=$W/cache/xml
pools_dir=$W/pools
log_debug() { echo "log: \$*" >> $W/log; }
die() { echo "die: \$*" >> $W/log; exit 1; }
EOF
cat > $W/scripts/kvm/storage_lib.sh <<'EOF'
pending_start_remove() { :; }
pool_enter() { return 1; }
EOF
for s in clear_sg_chain clear_local_router generate_vm_instance_map; do printf '#!/bin/bash\necho "%s $*" >> %s/calls\n' $s $W > $W/scripts/kvm/$s.sh; done
printf '#!/bin/bash\necho "del_host $*" >> %s/calls\n' $W > $W/scripts/kvm/del_host.sh
printf '#!/bin/bash\n[ "$1" = domstate ] && exit 1\nexit 0\n' > $W/bin/virsh
chmod +x $W/scripts/kvm/*.sh $W/bin/virsh
# two interfaces in two subnets of the VPC (router 3), and one on a VLAN bridge without DHCP
mkxml() { mkdir -p $W/cache/xml/inst-7; cat > $W/cache/xml/inst-7/inst-7.xml; }
mkxml <<'EOF'
<domain type='kvm'><name>inst-7</name><devices>
<interface type='bridge'><mac address='52:54:00:aa:00:01'/><source bridge='br4100'/><target dev='tapaa0001'/></interface>
<interface type='bridge'><mac address='52:54:00:aa:00:02'/><source bridge='br4200'/><target dev='tapaa0002'/></interface>
<interface type='bridge'><mac address='52:54:00:aa:00:03'/><source bridge='br756'/><target dev='tapaa0003'/></interface>
</devices></domain>
EOF
printf '52:54:00:aa:00:01,inst-7.cloudland.local,192.168.10.5\n52:54:00:bb:00:09,other.cloudland.local,192.168.10.9\n' > $W/cache/router/router-3/4100/dhcp_hosts
printf '52:54:00:aa:00:02,inst-7.cloudland.local,192.168.20.7\n' > $W/cache/router/router-3/4200/dhcp_hosts

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }

echo '[]' | PATH=$W/bin:$PATH bash $W/scripts/kvm/finish_source_migration.sh 11 21 7 3 10.0.0.9 warm 5
calls=$(cat $W/calls 2>/dev/null)
check "the lease of each VPC interface released, with its router, VNI, MAC and address" \
    "$(grep '^del_host' <<<"$calls" | tr '\n' ';')" "del_host 3 4100 52:54:00:aa:00:01 192.168.10.5;del_host 3 4200 52:54:00:aa:00:02 192.168.20.7;"
check "an interface without DHCP (VLAN bridge) left alone" "$(grep -c 'del_host .* 756 ' <<<"$calls")" 0
check "the router cleared as before" "$(grep -c '^clear_local_router 3 7' <<<"$calls")" 1
order=$(grep -nE '^(del_host|clear_local_router)' <<<"$calls" | cut -d: -f1 | tr '\n' ' ')
last_del=$(grep -n '^del_host' <<<"$calls" | tail -1 | cut -d: -f1); clr=$(grep -n '^clear_local_router' <<<"$calls" | cut -d: -f1)
check "released before the router is cleared" "$([ "$last_del" -lt "$clr" ] && echo yes)" yes

# a host where the instance is still defined (a rollback started it again): nothing released
rm -f $W/calls
mkxml <<'EOF'
<domain type='kvm'><name>inst-7</name><devices>
<interface type='bridge'><mac address='52:54:00:aa:00:01'/><source bridge='br4100'/><target dev='tapaa0001'/></interface>
</devices></domain>
EOF
printf '#!/bin/bash\nexit 0\n' > $W/bin/virsh
echo '[]' | PATH=$W/bin:$PATH bash $W/scripts/kvm/finish_source_migration.sh 11 21 7 3 10.0.0.9 warm 5
check "still defined on the source: nothing released" "$(grep -c '^del_host' $W/calls 2>/dev/null || echo 0)" 0

echo "finish-source-dhcp: $pass passed, $fail failed"
[ $fail = 0 ]
