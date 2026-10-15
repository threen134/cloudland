#!/bin/bash
# sandbox: ceph_oom_protect.sh and ceph_unit_oom (shared-storage-design.md §6.7.2) against containers named like
# cephadm daemons and a template unit of a fake cluster. Needs docker and the image quay.io/ceph/ceph:v20.2.0 (the Ceph
# end-to-end test pulls it), and no Ceph cluster in the sandbox.
# wsl.exe -u root --exec bash <repo under /mnt/c>/test-items/sandbox/storage/stc-test9-oom.sh
S=/opt/cloudland/scripts/kvm/storage
P="bash $S/ceph_oom_protect.sh"
uuid=0e0e0e0e-1111-2222-3333-444444444444
img=quay.io/ceph/ceph:v20.2.0
MON=2147483648 MGR=1073741824 OSD=1610612736
pass=0 fail=0
ok() { echo "ok   $1"; pass=$((pass + 1)); }
no() { echo "FAIL $1"; fail=$((fail + 1)); }
cleanup() {
    systemctl stop ceph-$uuid@osd.7.service >/dev/null 2>&1
    docker rm -f ceph-$uuid-osd-7 ceph-$uuid-mgr-a-b ceph-$uuid-crash-x >/dev/null 2>&1
    rm -rf /etc/systemd/system/ceph-$uuid@.service.d /etc/systemd/system/ceph-$uuid@.service /var/lib/ceph/$uuid
    rmdir /var/lib/ceph 2>/dev/null
    chmod +x $S/ceph_oom_protect.sh
    systemctl daemon-reload
}
cleanup
[ -z "$(ls -d /var/lib/ceph/*-*-*-*-*/ 2>/dev/null)" ] || { echo "a Ceph cluster runs in the sandbox"; exit 1; }
docker image inspect $img >/dev/null 2>&1 || { echo "no image $img"; exit 1; }
slice0=$(systemctl show -p MemoryLow --value system.slice)
start() { docker run -d --rm --init --name "$1" --entrypoint sleep $img 600 >/dev/null; }
adjs() { local pid cg; pid=$(docker inspect -f '{{.State.Pid}}' "$1" 2>/dev/null) || return; cg=/sys/fs/cgroup$(awk -F: '$1 == "0" {print $3}' /proc/$pid/cgroup)
    for p in $(cat $cg/cgroup.procs); do cat /proc/$p/oom_score_adj; done | sort -u | tr '\n' ' '; }
low() { local pid; pid=$(docker inspect -f '{{.State.Pid}}' "$1" 2>/dev/null) || return; cat /sys/fs/cgroup$(awk -F: '$1 == "0" {print $3}' /proc/$pid/cgroup)/memory.low; }
slice() { systemctl show -p MemoryLow --value system.slice; }
# wait_for <seconds> <command...>: until the command succeeds
wait_for() { local n=$1 i; shift; for i in $(seq 1 $n); do "$@" && return 0; sleep 1; done; return 1; }
is_protected() { [ "$(adjs ceph-$uuid-osd-7)" = "-900 " ] && [ "$(low ceph-$uuid-osd-7)" = $OSD ]; }

# 1. An OSD container: every process -900, memory.low the OSD figure
start ceph-$uuid-osd-7
out=$($P $uuid osd.7 -900 $MON $MGR $OSD); rc=$?
[ $rc -eq 0 ] && is_protected && ok "osd protected: $out" || no "osd: rc=$rc $out adj=$(adjs ceph-$uuid-osd-7) low=$(low ceph-$uuid-osd-7)"
# 2. systemd applies the settings of the units again: memory.low stays (it went through docker to systemd)
systemctl daemon-reload; sleep 1
[ "$(low ceph-$uuid-osd-7)" = $OSD ] && ok "memory.low survives a daemon-reload" || no "memory.low after daemon-reload: $(low ceph-$uuid-osd-7)"
# 3. A mgr with dots in its name gets the mgr figure
start ceph-$uuid-mgr-a-b
out=$($P $uuid mgr.a.b -900 $MON $MGR $OSD); rc=$?
[ $rc -eq 0 ] && [ "$(low ceph-$uuid-mgr-a-b)" = $MGR ] && [ "$(adjs ceph-$uuid-mgr-a-b)" = "-900 " ] && ok "mgr.a.b protected" || no "mgr: rc=$rc $out"
# 4. Other daemons are left alone
start ceph-$uuid-crash-x
out=$($P $uuid crash.x -900 1 1 1); rc=$?
[ $rc -eq 0 ] && [ -z "$out" ] && [ "$(adjs ceph-$uuid-crash-x)" = "0 " ] && ok "crash left alone" || no "crash: rc=$rc '$out' adj=$(adjs ceph-$uuid-crash-x)"
# 5. A unit whose process ended stops the wait at once
t0=$(date +%s); out=$($P $uuid osd.9 -900 1 1 1 999999); rc=$?
[ $rc -ne 0 ] && [ $(($(date +%s) - t0)) -lt 5 ] && ok "gives up with the unit: $out" || no "unit gone: rc=$rc $out"
# 6. Bad arguments
$P nope osd.7 -900 1 1 1 >/dev/null && no "accepts a bad uuid" || ok "refuses a bad uuid"
$P $uuid osd.7 -2000 1 1 1 >/dev/null && no "accepts adj -2000" || ok "refuses adj -2000"
$P $uuid 'osd.7;id' -900 1 1 1 >/dev/null && no "accepts a bad daemon" || ok "refuses a bad daemon"
docker rm -f ceph-$uuid-osd-7 ceph-$uuid-crash-x >/dev/null 2>&1

# 7. ceph_unit_oom: the drop-in, and the running daemons of the cluster protected at once. A template unit of the
# cluster stands in for cephadm's (docker run in the foreground like unit.run, start timeout 10 s); the daemon
# directories for cephadm's
mkdir -p /var/lib/ceph/$uuid/osd.7 /var/lib/ceph/$uuid/mgr.a.b /var/lib/ceph/$uuid/crash.x /var/lib/ceph/$uuid/config
unit() {
    cat >/etc/systemd/system/ceph-$uuid@.service <<U
[Service]
TimeoutStartSec=10
ExecStart=/bin/bash -c 'sleep $1; exec /usr/bin/docker run --rm --init --name ceph-$uuid-osd-7 --entrypoint sleep $img 600'
U
    systemctl daemon-reload
}
unit 0
systemctl start ceph-$uuid@osd.7.service
wait_for 30 test "$(docker inspect -f '{{.State.Running}}' ceph-$uuid-osd-7 2>/dev/null)" = true
[ "$(adjs ceph-$uuid-osd-7)" = "0 " ] && ok "running daemon unprotected before" || no "before: $(adjs ceph-$uuid-osd-7)"
out=$(bash -c "source /opt/cloudland/scripts/cloudrc; source $S/backends/ceph.sh; ceph_unit_oom $uuid -900 $MON $MGR $OSD"); rc=$?
d=/etc/systemd/system/ceph-$uuid@.service.d/cloudland-oom.conf
grep -qx "ExecStartPost=-/usr/bin/systemd-run --no-block --quiet --collect -p RuntimeMaxSec=300 /bin/bash $S/ceph_oom_protect.sh $uuid %i -900 $MON $MGR $OSD \$MAINPID" $d \
    && ok "drop-in written" || no "drop-in: $(cat $d)"
[ $rc -eq 0 ] && is_protected && ok "running daemon protected at once: $(echo $out | tr '\n' ' ')" || no "at once: rc=$rc $out adj=$(adjs ceph-$uuid-osd-7)"
# 8. system.slice covers the daemons deployed (osd.7 + mgr.a.b; crash.x and config count nothing)
[ "$(slice)" = $((OSD + MGR)) ] && ok "system.slice memory.low $(slice) = osd + mgr" || no "system.slice memory.low $(slice), wanted $((OSD + MGR))"
# 9. A restart: the unit is active at once, the new container protected shortly after; the journal of the transient
# unit has the line
systemctl restart ceph-$uuid@osd.7.service
[ "$(systemctl is-active ceph-$uuid@osd.7.service)" = active ] && ok "restart: unit active at once" || no "restart: $(systemctl is-active ceph-$uuid@osd.7.service)"
wait_for 30 is_protected && ok "restart: new container protected" || no "restart: adj=$(adjs ceph-$uuid-osd-7) low=$(low ceph-$uuid-osd-7)"
wait_for 10 bash -c "journalctl --since '-1 min' --no-pager | grep -q 'osd.7: oom_score_adj -900 (0 processes missed), memory.low $OSD'" \
    && ok "journal line" || no "journal: $(journalctl --since '-1 min' --no-pager | grep -i oom | tail -3)"
# 10. A container that runs only after 20 s, past the 10 s start timeout of its unit: the unit is not held up by the
# protection, so it is not stopped, and the container is protected once it runs (the old drop-in waited in
# ExecStartPost: systemd stopped the daemon at its start timeout)
unit 20
systemctl restart ceph-$uuid@osd.7.service
sleep 15
st=$(systemctl is-active ceph-$uuid@osd.7.service)
[ "$st" = active ] && ok "slow container: unit active after 15 s" || no "slow container: unit $st after 15 s"
wait_for 30 is_protected && [ "$(systemctl is-active ceph-$uuid@osd.7.service)" = active ] && [ "$(systemctl show -p NRestarts --value ceph-$uuid@osd.7.service)" = 0 ] \
    && ok "slow container: protected once it ran, never restarted" || no "slow container: $(systemctl is-active ceph-$uuid@osd.7.service) restarts=$(systemctl show -p NRestarts --value ceph-$uuid@osd.7.service) adj=$(adjs ceph-$uuid-osd-7)"
# 11. The script without its mode bits: still run (bash)
unit 0
chmod -x $S/ceph_oom_protect.sh
systemctl restart ceph-$uuid@osd.7.service
wait_for 30 is_protected && ok "protected with the script not executable" || no "not executable: adj=$(adjs ceph-$uuid-osd-7)"
chmod +x $S/ceph_oom_protect.sh
# 12. The sweep puts back what a start missed, and lowers system.slice when a daemon went
pid=$(docker inspect -f '{{.State.Pid}}' ceph-$uuid-osd-7)
for p in $(cat /sys/fs/cgroup$(awk -F: '$1 == "0" {print $3}' /proc/$pid/cgroup)/cgroup.procs); do echo 0 >/proc/$p/oom_score_adj; done
rm -rf /var/lib/ceph/$uuid/mgr.a.b
out=$($P --sweep)
is_protected && [ "$(slice)" = $OSD ] && ok "sweep: $(echo $out | tr '\n' ' ')" || no "sweep: '$out' adj=$(adjs ceph-$uuid-osd-7) slice=$(slice)"
out=$($P --sweep)
[ -z "$out" ] && ok "sweep with nothing to do says nothing" || no "idle sweep: $out"
# 13. The cluster leaves (backend_leave): drop-ins gone, system.slice back to what it was
systemctl stop ceph-$uuid@osd.7.service
docker rm -f ceph-$uuid-mgr-a-b >/dev/null 2>&1
# What cephadm rm-cluster leaves (the directory gone first, so backend_leave does not run cephadm on the fake cluster)
rm -rf /var/lib/ceph/$uuid
bash -c "source /opt/cloudland/scripts/cloudrc; source $S/backends/ceph.sh; backend_leave $uuid false" >/dev/null 2>&1
[ ! -d /etc/systemd/system/ceph-$uuid@.service.d ] && [ "$(slice)" = 0 ] && ok "leave: drop-ins gone, system.slice memory.low 0" || no "leave: $(ls /etc/systemd/system/ceph-$uuid@.service.d 2>&1) slice=$(slice)"
# 14. Left-overs of systemd for the scopes of containers that are gone
cleanup
left=$(ls -d /run/systemd/system.control/docker-*.scope.d 2>/dev/null | while read x; do s=$(basename $x .d); systemctl is-active -q $s || echo $s; done | wc -l)
[ "$left" = 0 ] && ok "no drop-ins left for gone scopes" || no "$left drop-in directories left for gone scopes in /run/systemd/system.control"
[ "$slice0" = 0 ] || systemctl set-property system.slice MemoryLow=$slice0
echo "passed $pass, failed $fail"
