#!/bin/bash
# node_reconcile.sh (scripts/kvm, §11.4 of the shared storage design): when clapi says a held instance is still this
# host's, one that ran before the boot is queued to start; one that was shut off or in rescue (reconcile_last, written
# by the boot sync) is left so and reported in that state, the rescue system started for one in rescue (second code
# review of 2026-10-17: the boot sync reported these before clapi answered, bypassing the hold). virsh, sudo and
# clear_stale_vm.sh are stand-ins; the list helpers and start_rescue_domain are the real ones from storage_lib.sh.
# Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vm/node-reconcile.sh
# KVM=<dir> runs another copy of scripts/kvm.
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
KVM=${KVM:-$R/scripts/kvm}
W=$(mktemp -d)
trap 'rm -rf $W' EXIT
mkdir -p $W/kvm $W/bin $W/run $W/cache $W/def $W/dom
sed 's/\r$//' $KVM/node_reconcile.sh > $W/kvm/node_reconcile.sh
{
    echo 'vnc_ensure_domain_passwd() { :; }'
    sed 's/\r$//' $KVM/storage_lib.sh | sed -n '/^function pending_start_add()/,/^}/p; /^function pending_start_remove()/,/^}/p
        /^reconcile_hold_file=/p; /^function reconcile_hold_add()/,/^}/p; /^function reconcile_hold_remove()/,/^}/p
        /^function reconcile_held()/,/^}/p; /^reconcile_last_file=/p; /^function reconcile_last_set()/,/^}/p
        /^function reconcile_last_get()/,/^}/p; /^function reconcile_last_remove()/,/^}/p; /^function start_rescue_domain()/,/^}/p'
} > $W/kvm/storage_lib.sh
printf '#!/bin/bash\necho "$*" >> %s/stale\n' $W > $W/kvm/clear_stale_vm.sh
cat > $W/cloudrc <<EOF
cache_dir=$W/cache
run_dir=$W/run
log_debug() { shift; echo "\$*" >> $W/log; }
die() { echo "\$*" >&2; exit 1; }
EOF
printf '#!/bin/bash\nexec "$@"\n' > $W/bin/sudo
cat > $W/bin/virsh <<EOF
#!/bin/bash
name=\$2
case "\$1" in
dominfo) [ -f $W/def/\$name ] ;;
domstate) cat $W/dom/\$name 2>/dev/null || echo "shut off" ;;
start) echo "start \$name" >> $W/started ;;
esac
EOF
chmod +x $W/kvm/*.sh $W/bin/*
export PATH=$W/bin:$PATH NODE_ID=7
boot=$(cat /proc/sys/kernel/random/boot_id)

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }
list() { sort $W/cache/$1 2>/dev/null | tr '\n' ' '; }

for d in inst-12 inst-13 inst-14 inst-14-rescue inst-15; do touch $W/def/$d; done
printf '12\n13\n14\n15\n20\n' > $W/cache/reconcile_hold
printf '12 shut_off\n14 rescuing\n20 shut_off\n' > $W/cache/reconcile_last
out=$(echo "{\"boot\":\"$boot\",\"reason\":\"boot\",\"start\":[12,13,14]}" | bash $W/kvm/node_reconcile.sh 2>&1)
check "clapi says 12-14 are still this host's: the one that ran is queued to start" "$(list pending_start)" "13 "
check "  the shut off and the rescued ones are reported as they were, not started" "$(grep -oE "launch_vm.sh '[0-9]+' '[a-z_]+' '7' 'sync'" <<<"$out" | awk -F"'" '{printf "%s:%s ", $2, $4}')" "12:shut_off 14:rescuing "
check "  the rescue system of the rescued one is started, nothing else" "$(cat $W/started 2>/dev/null | tr '\n' ' ')" "start inst-14-rescue "
check "  not held any more; 15 not answered yet stays held; 20, whose domain is gone, is dropped" "$(list reconcile_hold)" "15 "
check "  what 12, 14 and 20 were is forgotten" "$(list reconcile_last)" ""
grep -q "node_reconciled '7' '$boot' 'boot'" <<<"$out"; check "  answered with node_reconciled" "$?" 0

printf '15 shut_off\n' > $W/cache/reconcile_last
echo "{\"boot\":\"$boot\",\"reason\":\"leave\",\"stale\":[{\"id\":15,\"router\":3}]}" | bash $W/kvm/node_reconcile.sh >/dev/null 2>&1
check "15 owned elsewhere now: cleared by clear_stale_vm.sh" "$(cat $W/stale 2>/dev/null)" "15 3"

echo "passed $pass, failed $fail"
[ $fail = 0 ]
