#!/bin/bash
# sandbox tests of the fixes of the design review of 2026-10-07 (shared-storage-design.md §7.6, §8.6) with stub commands: the
# GPFS teardown goes on when mmdelnode -a fails with members offline (and fails without), the leave a host missed
# leaves its GPFS alone when it is in another GPFS cluster of CloudLand by then, and does not wipe a disk a local pool
# took since; Ceph: the OSDs that stay must have room for the data of the ones removed (per device class), the
# prometheus module of every mgr listens on the address of its host (a standby whose address changed restarts, the
# active one fails over, nothing is done when they are bound already)
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
uuid=eeeeeeee-1818-2222-3333-444444444444
other=eeeeeeee-1818-2222-3333-555555555555
fake=$(mktemp -d)
export FAKE=$fake
run_dir=/opt/cloudland/run

# ---- stubs of GPFS ----
cat >$fake/mmlscluster <<'EOF'
#!/bin/bash
echo "mmlscluster:clusterSummary:HEADER:version:reserved:reserved:clusterName:clusterId:"
echo "mmlscluster:clusterSummary:0:1:::cl-test18.cloudland:4242:"
EOF
for c in mmumount mmshutdown; do
    printf '#!/bin/bash\necho "%s $*" >>$FAKE/calls\n' $c >$fake/$c
done
cat >$fake/mmdelnode <<'EOF'
#!/bin/bash
echo "mmdelnode $*" >>$FAKE/calls
[ -f $FAKE/delnode_fails ] && { echo "mmdelnode: 10.0.0.3 is not reachable" >&2; exit 1; }
exit 0
EOF
printf '#!/bin/bash\nexit 1\n' >$fake/mmlsfs
printf '#!/bin/bash\nexit 0\n' >$fake/mmlsnsd
chmod +x $fake/*

teardown() {
    (
        cd $S
        source <(sed -e 's/\r$//' -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' gpfs_cluster.sh)
        gpfs_bin=$fake
        stc_progress() { :; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        do_teardown "$1"
    ) 2>&1
}
mkdir -p $run_dir/storage/$uuid && echo gpfs >$run_dir/storage/$uuid/member
in='{"action":"teardown","cluster_uuid":"'$uuid'","cluster_name":"cl-test18","filesystems":[],"nsds":[]}'

: >$fake/calls
out=$(teardown "$in")
grep -q '^RESULT {"removed": true}' <<<"$out" && grep -qx 'mmdelnode -a' $fake/calls && ok "teardown: every member online, the cluster deleted" || ko "teardown: $out"
touch $fake/delnode_fails
out=$(teardown "$in")
grep -q '^FAILED mmdelnode failed' <<<"$out" && ok "teardown: mmdelnode -a failing with every member online fails" || ko "teardown fails: $out"
out=$(teardown "$(jq -c '.offline = ["10.0.0.3"]' <<<"$in")")
grep -q '^RESULT {"removed": true}' <<<"$out" && grep -q "members offline (10.0.0.3)" <<<"$out" &&
    ok "teardown: mmdelnode -a failing with a member offline goes on" || ko "teardown offline: $out"
rm -f $fake/delnode_fails

# ---- the leave a host missed: GPFS of another cluster of CloudLand is left alone ----
mkdir -p /var/mmfs/gen
had=0
[ -f /var/mmfs/gen/mmsdrfs ] && { had=1; cp -p /var/mmfs/gen/mmsdrfs /root/mmsdrfs.stc18-bak; }
leave_gpfs() {
    (
        cd $S/..
        source ../cloudrc
        source ./storage_lib.sh
        source ./storage/backends/gpfs.sh
        gpfs_bin=$fake
        pgrep() { return 1; }
        backend_leave $uuid false
    ) 2>&1
}
echo "the other cluster" >/var/mmfs/gen/mmsdrfs
mkdir -p $run_dir/storage/$other && echo gpfs >$run_dir/storage/$other/member
out=$(leave_gpfs)
[ -f /var/mmfs/gen/mmsdrfs ] && grep -q "in GPFS storage cluster $other now" <<<"$out" &&
    ok "leave: a host in another GPFS cluster of CloudLand keeps its configuration" || ko "leave other: $out"
# A Ceph cluster of the host is no reason
echo ceph >$run_dir/storage/$other/member
out=$(leave_gpfs)
[ ! -f /var/mmfs/gen/mmsdrfs ] && ok "leave: a member of a Ceph cluster too still leaves GPFS" || ko "leave ceph: $out"
rm -rf $run_dir/storage/$other
echo "the cluster" >/var/mmfs/gen/mmsdrfs
out=$(leave_gpfs)
[ ! -f /var/mmfs/gen/mmsdrfs ] && ok "leave: only this cluster, the configuration goes" || ko "leave: $out"
[ $had = 1 ] && mv /root/mmsdrfs.stc18-bak /var/mmfs/gen/mmsdrfs

# ---- the leave a host missed: a disk a local pool took since is not wiped ----
leave_wipe() {
    (
        cd $S
        source <(sed -e 's/\r$//' -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' stc_leave.sh)
        stc_progress() { :; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_lock() { :; }
        backend_load() { return 1; }
        stc_keys_update() { return 0; }
        shared_pools_prune() { :; }
        disk_identity() { identity_path=/dev/fake-$1; return 0; }
        disk_tags() { tag_pool=""; [ "$1" = /dev/fake-taken ] && tag_pool=local-ssd; }
        lsblk() { :; }
        wipe_disk() { echo "wiped $1" >>$FAKE/wiped; }
        stc_main <<<"$1"
    ) 2>&1
}
: >$fake/wiped
out=$(leave_wipe '{"cluster_uuid":"'$uuid'","kind":"gpfs","wipe":[{"id":"taken"},{"id":"mine"}]}')
grep -q '^RESULT {"wiped":1}' <<<"$out" && grep -qx "wiped /dev/fake-mine" $fake/wiped && ! grep -q taken $fake/wiped &&
    grep -q "holds CloudLand pool local-ssd now" <<<"$out" && ok "leave: the disk of a local pool is not wiped, the other is" ||
    ko "leave wipe: $out / $(cat $fake/wiped)"

# ---- Ceph: room for the data of the OSDs removed ----
ceph_run() {
    (
        cd $S
        source <(sed -e 's/\r$//' -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' ceph_cluster.sh)
        fsid=$uuid
        stc_fail() { echo "FAILED $*"; exit 1; }
        sleep() { :; }
        ceph_admin() {
            shift
            echo "$*" >>$FAKE/ceph-calls
            case "$*" in
                "osd df -f json") cat $FAKE/df ;;
                "osd dump -f json") echo '{"nearfull_ratio": 0.85}' ;;
                "orch host ls -f json") cat $FAKE/hosts ;;
                "orch ps --daemon-type mgr -f json") echo '[{"daemon_id":"h1.abc","hostname":"h1"},{"daemon_id":"h2.def","hostname":"h2"}]' ;;
                "mgr stat -f json") echo '{"active_name":"h1.abc","available":true}' ;;
                "config dump -f json") cat $FAKE/dump ;;
                "config set mgr mgr/prometheus/"*)
                    k=$(cut -d' ' -f4 <<<"$*"); v=$(cut -d' ' -f5 <<<"$*")
                    jq -c --arg k "$k" --arg v "$v" '[.[] | select(.name != $k)] + [{section: "mgr", name: $k, value: $v}]' $FAKE/dump >$FAKE/dump.x && mv $FAKE/dump.x $FAKE/dump ;;
                "orch status -f json") echo '{}' ;;
            esac
        }
        "$@"
    ) 2>&1
}
osd() { echo "{\"id\":$1,\"device_class\":\"$2\",\"kb\":$3,\"kb_used\":$4,\"kb_used_data\":$5}"; }
echo "{\"nodes\":[$(osd 0 hdd 1000 300 280),$(osd 1 hdd 1000 300 280),$(osd 2 hdd 1000 300 280),$(osd 3 ssd 500 50 10)]}" >$fake/df
out=$(ceph_run removal_room '[2]')
[ -z "$out" ] && ok "room: two HDD OSDs take the data of the third" || ko "room: $out"
out=$(ceph_run removal_room '[3]')
grep -q "^FAILED .*no ssd OSD would be left for the data (Ceph calls 85% near full)" <<<"$out" && ok "room: the only SSD with data on it can not go" || ko "room ssd: $out"
echo "{\"nodes\":[$(osd 0 hdd 1000 700 680),$(osd 1 hdd 1000 700 680),$(osd 2 hdd 1000 700 680),$(osd 3 ssd 500 50 0)]}" >$fake/df
out=$(ceph_run removal_room '[2]')
grep -q "^FAILED .*the hdd OSDs that stay would be 104% full" <<<"$out" && ok "room: too full without the OSD" || ko "room full: $out"
out=$(ceph_run removal_room '[3]')
[ -z "$out" ] && ok "room: an OSD without data goes, the last of its class too" || ko "room empty: $out"

# ---- Ceph: the mgr prometheus modules listen on the addresses of their hosts ----
echo '[{"hostname":"h1","addr":"10.0.0.1"},{"hostname":"h2","addr":"10.0.0.2"}]' >$fake/hosts
echo '[]' >$fake/dump
: >$fake/ceph-calls
out=$(ceph_run ceph_prometheus_bind $uuid)
grep -qx "config set mgr mgr/prometheus/h1.abc/server_addr 10.0.0.1" $fake/ceph-calls &&
    grep -qx "config set mgr mgr/prometheus/h2.def/server_addr 10.0.0.2" $fake/ceph-calls &&
    grep -qx "orch daemon restart mgr.h2.def" $fake/ceph-calls && grep -qx "mgr fail h1.abc" $fake/ceph-calls &&
    ok "bind: both mgrs bound, the standby restarted, the active failed over" || ko "bind: $out / $(cat $fake/ceph-calls)"
: >$fake/ceph-calls
out=$(ceph_run ceph_prometheus_bind $uuid)
! grep -q "config set\|restart\|mgr fail" $fake/ceph-calls && ok "bind: nothing to do when they are bound" || ko "bind again: $(cat $fake/ceph-calls)"
echo '[{"hostname":"h1","addr":"10.0.0.1"},{"hostname":"h2","addr":"10.0.0.22"}]' >$fake/hosts
: >$fake/ceph-calls
out=$(ceph_run ceph_prometheus_bind $uuid)
grep -qx "config set mgr mgr/prometheus/h2.def/server_addr 10.0.0.22" $fake/ceph-calls && grep -qx "orch daemon restart mgr.h2.def" $fake/ceph-calls &&
    ! grep -q "mgr fail\|h1.abc/server_addr" $fake/ceph-calls && ok "bind: a standby whose address changed restarts, the active stays" ||
    ko "bind standby: $(cat $fake/ceph-calls)"

rm -rf $run_dir/storage/$uuid $fake
echo "$pass passed, $fail failed"
[ $fail = 0 ]
