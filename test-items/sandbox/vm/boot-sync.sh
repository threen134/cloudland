#!/bin/bash
# sync_instance of report_rc.sh (scripts/kvm), the boot sync of a node: it starts what ran before the boot, leaves an
# instance that was shut off off and starts the rescue system of one in rescue, from the "<id> <state>" list the last
# heartbeat wrote (old_inst_list); without that list every instance is started, as before (2026-10-17 supplementary
# run, R2-16: rebooting work-03 started a guest that had been off for ten days). An instance with a shared-pool disk is
# held for clapi whatever it was, and when it was off or in rescue node_reconcile.sh reports it so (reconcile_last).
# inst_status writes that list: names compared whole, never from a failed listing, nothing while the system shuts
# down (second code review of 2026-10-17). sudo, virsh, iptables and the storage helpers are stand-ins; the
# reconcile_last helpers are the real ones from storage_lib.sh.
# Run in WSL as root: wsl.exe -u root --exec bash <repo>/test-items/sandbox/vm/boot-sync.sh
# KVM=<dir> runs another copy of scripts/kvm.
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="$(cd $D/../../.. && pwd)"
KVM=${KVM:-$R/scripts/kvm}
W=$(mktemp -d)
trap 'rm -rf $W' EXIT
mkdir -p $W/bin $W/run $W/xml $W/img $W/dom
for c in iptables iptables-restore; do printf '#!/bin/bash\nexit 0\n' > $W/bin/$c; done
printf '#!/bin/bash\nexec "$@"\n' > $W/bin/sudo
printf '#!/bin/bash\ncat %s/sysstate 2>/dev/null || echo running\n' $W > $W/bin/systemctl
cat > $W/bin/virsh <<EOF
#!/bin/bash
for a in "\$@"; do case "\$a" in inst-*) id=\${a#inst-} ;; esac; done
case "\$1" in
domstate) cat $W/dom/\$id 2>/dev/null || echo "shut off" ;;
list) [ -f $W/virsh-fail ] && { echo "error: failed to connect to the hypervisor" >&2; exit 1; }
      printf ' Id   Name      State\n-------------------------\n'; cat $W/virsh-list; echo ;;
esac
exit 0
EOF
chmod +x $W/bin/*
export PATH=$W/bin:$PATH NODE_ID=7
run_dir=$W/run xml_dir=$W/xml image_dir=$W/img cache_dir=$W/cache
mkdir -p $W/cache
eval "$(sed 's/\r$//' $KVM/report_rc.sh | sed -n '/^function sync_instance()/,/^}/p; /^function inst_status()/,/^}/p')"
eval "$(sed 's/\r$//' $KVM/storage_lib.sh | sed -n '/^function reconcile_last_set()/,/^}/p; /^function reconcile_last_get()/,/^}/p; /^function reconcile_last_remove()/,/^}/p')"
reconcile_last_file=$W/cache/reconcile_last
paused_nospace() { false; }
reconcile_held() { false; }
instance_shared() { [ -f $W/shared-$1 ]; }
instance_pools_ok() { true; }
reconcile_hold_add() { echo "hold $1" >> $W/calls; }
pending_start_add() { echo "pending $1" >> $W/calls; }
try_start_instance() { echo "start $1" >> $W/calls; }
start_rescue_domain() { echo "rescue $1" >> $W/calls; }

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else fail=$((fail + 1)); echo "FAIL $1: got [$2] want [$3]"; fi; }
# boot <old_inst_list or "none">: one boot sync, prints the calls and what is reported ("<id>:<state>")
boot() {
    rm -f $W/calls $W/run/need_to_sync
    if [ "$1" = none ]; then rm -f $W/img/old_inst_list; else printf "$1" > $W/img/old_inst_list; fi
    out=$(sync_instance 2>/dev/null)
    echo "$(sort $W/calls 2>/dev/null | tr '\n' ' ')| reported: $(grep -oE "launch_vm.sh '[0-9]+' '[a-z_]+' '7' 'sync'" <<<"$out" | awk -F"'" '{printf "%s:%s ", $2, $4}')"
}
for i in 11 12 13 14 15 16 17; do touch $W/xml/inst-$i; done
echo running > $W/dom/16

r=$(boot '11 running\n12 shut_off\n13 pending_storage\n14 paused\n16 running\n17 rescuing\n')
check "after a boot: what ran, waited for a pool or was paused starts; shut off is not started, in rescue gets its rescue system; both reported for the NIC sync; one not in the list starts" "$r" "rescue 17 start 11 start 13 start 14 start 15 | reported: 11:running 12:shut_off 13:running 14:running 15:running 16:running 17:rescuing "
[ "$(cat $W/run/need_to_sync)" = "$(cat /proc/sys/kernel/random/boot_id)" ]; check "  the boot is marked as synced" "$?" 0

r=$(boot none)
check "no list from before the boot (a new node): everything starts, as before" "$r" "start 11 start 12 start 13 start 14 start 15 start 17 | reported: 11:running 12:running 13:running 14:running 15:running 16:running 17:running "

touch $W/shared-12 $W/shared-13 $W/shared-14
r=$(boot '12 shut_off\n13 running\n14 rescuing\n')
check "shared-disk instances are held for clapi whatever they were, reported to nobody; only the one that ran waits to start" "$r" "hold 12 hold 13 hold 14 pending 13 start 11 start 15 start 17 | reported: 11:running 15:running 16:running 17:running "
check "  what the off and the rescued ones were is kept for node_reconcile.sh" "$(sort $W/cache/reconcile_last | tr '\n' ' ')" "12 shut_off 14 rescuing "
r=$(boot '12 pending_reconcile\n13 pending_reconcile\n14 rescuing\n')
check "  a second boot before clapi answered (the list says pending_reconcile): still not queued to start" "$r" "hold 12 hold 13 hold 14 pending 13 start 11 start 15 start 17 | reported: 11:running 15:running 16:running 17:running "
check "  and what they were is still kept" "$(sort $W/cache/reconcile_last | tr '\n' ' ')" "12 shut_off 14 rescuing "

rm -f $W/cache/reconcile_last $W/shared-14
r=$(boot '1 shut_off\n112 shut_off\n')
check "an id that only contains another one's digits does not match" "$r" "hold 12 hold 13 pending 12 pending 13 start 11 start 14 start 15 start 17 | reported: 11:running 14:running 15:running 16:running 17:running "

# inst_status writes the list the next boot reads: replaced whole, never deleted, never from a failed listing; not
# while the system shuts down (libvirt-guests has stopped the guests then, and a heartbeat may still be running);
# every state reported again every 10 minutes
printf ' 3    inst-11   running\n -    inst-12   shut off\n' > $W/virsh-list
rm -f $W/img/old_inst_list $W/run/inst_status_full; echo running > $W/sysstate
out=$(inst_status 2>/dev/null)
check "inst_status writes each instance's state" "$(tr '\n' ' ' < $W/img/old_inst_list)" "11 running 12 shut_off "
check "  and reports them" "$(grep -c "inst_status.sh '7' '12 shut_off 11 running'" <<<"$out")" 1
out=$(inst_status 2>/dev/null)
check "  nothing changed: nothing reported, the list still there" "$(echo -n "$out" | wc -l)/$(tr '\n' ' ' < $W/img/old_inst_list)" "0/11 running 12 shut_off "
touch -d '-11 minutes' $W/run/inst_status_full
out=$(inst_status 2>/dev/null)
check "  ten minutes on: everything reported again, the list kept" "$(grep -c "inst_status.sh '7' '12 shut_off 11 running'" <<<"$out")/$(tr '\n' ' ' < $W/img/old_inst_list)" "1/11 running 12 shut_off "
touch $W/virsh-fail
out=$(inst_status 2>/dev/null)
check "  virsh fails (libvirtd restarting): nothing reported, the list kept" "$(echo -n "$out" | wc -l)/$(tr '\n' ' ' < $W/img/old_inst_list)" "0/11 running 12 shut_off "
rm -f $W/virsh-fail
printf ' -    inst-11   shut off\n -    inst-12   shut off\n' > $W/virsh-list; echo stopping > $W/sysstate
out=$(inst_status 2>/dev/null)
check "  during a shutdown nothing is reported and the list is kept, not rewritten with every guest off" "$(echo -n "$out" | wc -l)/$(tr '\n' ' ' < $W/img/old_inst_list)" "0/11 running 12 shut_off "
echo running > $W/sysstate
inst_status >/dev/null 2>&1
check "  running again: rewritten" "$(tr '\n' ' ' < $W/img/old_inst_list)" "11 shut_off 12 shut_off "

# The rescue domain of inst-5 makes inst-5 rescuing; inst-50, shut off by its user, stays itself
printf ' -    inst-5    shut off\n 4    inst-5-rescue   running\n -    inst-50   shut off\n 6    inst-500  running\n' > $W/virsh-list
inst_status >/dev/null 2>&1
check "an instance in rescue is rescuing, the ones whose names begin with its name are left alone" "$(tr '\n' ' ' < $W/img/old_inst_list)" "5 rescuing 50 shut_off 500 running "

echo "passed $pass, failed $fail"
[ $fail = 0 ]
