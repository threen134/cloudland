#!/bin/bash
# sandbox tests of the fixes after the code review of 2026-10-03: only root reads the jobs, authorized_keys is
# rewritten under a lock and never loses a line, GPFS is left alone on a host that never joined the cluster, and an
# RBD delete tells a missing image from a cluster out of reach
S=/opt/cloudland/scripts/backend/storage
A=/opt/cloudland/run/async_job
J=/opt/cloudland/run/storage/jobs
L=/opt/cloudland/log/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
wait_cb() { for i in $(seq 1 40); do [ -f $J/$1/callback ] && return 0; sleep 0.5; done; return 1; }
decode() { awk -F"'" '{print $8}' | base64 -d 2>/dev/null; }
base=$((RANDOM * 10 + 7))
cd /opt/cloudland/scripts/kvm
source ../cloudrc
source ./storage_lib.sh
source ./storage/stc_lib.sh

# 1 the jobs (input, result, callback) and the logs: only root reads them
chmod 755 $J $L 2>/dev/null
r=$((base + 1))
echo '{"sleep_sec":0,"lock":"t4","step":1,"secret":"do-not-read"}' | $S/stc_selftest.sh $r
wait_cb $r || ko "1 the job did not end"
[ "$(stat -c %a $J)" = 700 ] && ok "1 jobs directory 700" || ko "1 jobs directory $(stat -c %a $J)"
[ "$(stat -c %a $L)" = 700 ] && ok "1 log directory 700" || ko "1 log directory $(stat -c %a $L)"
id nobody >/dev/null 2>&1 && { su -s /bin/sh nobody -c "cat $J/$r/input" >/dev/null 2>&1 && ko "1 another account reads the input" || ok "1 another account can not read the input"; }
rm -f $A/*.done

# 2 authorized_keys: rewritten under a lock, the other lines kept, mode 600
keys=/root/.ssh/authorized_keys
cp -p $keys /root/authorized_keys.stc4-bak 2>/dev/null || : >/root/authorized_keys.stc4-bak
printf 'ssh-ed25519 AAAAother other@host\nssh-ed25519 AAAAcland cland-key\n' >$keys
chmod 644 $keys
stc_keys_update suffix cloudland-storage-t4a 'from="10.0.0.1" ssh-ed25519 AAAAa cloudland-storage-t4a'
grep -q 'cloudland-storage-t4a$' $keys && [ $(grep -c . $keys) -eq 3 ] && ok "2 line added, others kept" || ko "2 add: $(cat $keys)"
[ "$(stat -c %a $keys)" = 600 ] && ok "2 mode 600" || ko "2 mode $(stat -c %a $keys)"
stc_keys_update suffix cloudland-storage-t4a 'from="10.0.0.2" ssh-ed25519 AAAAa cloudland-storage-t4a'
[ $(grep -c 'cloudland-storage-t4a$' $keys) -eq 1 ] && grep -q '10.0.0.2' $keys && ok "2 line replaced, not duplicated" || ko "2 replace: $(cat $keys)"
stc_keys_update line 'ssh-ed25519 AAAAother other@host'
! grep -q 'other@host' $keys && grep -q cland-key $keys && ok "2 exact line removed" || ko "2 line removal: $(cat $keys)"
# A tag that is the end of another one must not take it along
stc_keys_update suffix storage-t4a
grep -q 'cloudland-storage-t4a$' $keys && ok "2 only whole comments match" || ko "2 suffix took another line"
# Every line goes: an empty file, not a failure (grep -v would have answered 1)
printf 'x cloudland-storage-t4z\n' >$keys
stc_keys_update suffix cloudland-storage-t4z && [ ! -s $keys ] && ok "2 the last line removed" || ko "2 last line: rc or $(cat $keys)"
# 20 jobs at once, each with its own cluster line: none is lost, the cland key stays
printf 'ssh-ed25519 AAAAcland cland-key\n' >$keys
for i in $(seq 1 20); do
    stc_keys_update suffix cloudland-storage-p$i "ssh-ed25519 AAAA$i cloudland-storage-p$i" &
done
wait
[ $(grep -c 'cloudland-storage-p' $keys) -eq 20 ] && grep -q cland-key $keys && ok "2 20 rewrites at once lose nothing" ||
    ko "2 concurrent: $(grep -c 'cloudland-storage-p' $keys) of 20, cland $(grep -c cland-key $keys)"
ls $keys.?????? 2>/dev/null | grep -q . && ko "2 temp files left: $(ls $keys.??????)" || ok "2 no temp file left"
cat /root/authorized_keys.stc4-bak >$keys
chmod 600 $keys
rm -f /root/authorized_keys.stc4-bak

# 3 GPFS on a host that never joined the cluster (its deployment stopped at the precheck) is left alone
u=$(cat /proc/sys/kernel/random/uuid)
mkdir -p /var/mmfs/gen
had=0
[ -f /var/mmfs/gen/mmsdrfs ] && { had=1; cp -p /var/mmfs/gen/mmsdrfs /root/mmsdrfs.stc4-bak; }
echo "foreign cluster" >/var/mmfs/gen/mmsdrfs
(source ./storage/backends/gpfs.sh && backend_leave $u false) >/dev/null
[ -f /var/mmfs/gen/mmsdrfs ] && ok "3 backend_leave keeps the configuration of a host that never joined" || ko "3 mmsdrfs removed"
r=$((base + 3))
jq -cn --arg u $u '{action: "teardown", cluster_uuid: $u, cluster_name: "cl-test4", filesystems: ["fs1"], nsds: ["d1"]}' | $S/gpfs_cluster.sh $r
wait_cb $r
res=$(decode <$J/$r/callback | jq -c .result)
[ "$res" = '{"removed":false}' ] && grep -q "never joined" $L/run-$r.log && ok "3 teardown does nothing on a host that never joined" ||
    ko "3 teardown: $res $(tail -3 $L/run-$r.log)"
mkdir -p $run_dir/storage/$u && echo gpfs >$run_dir/storage/$u/member
(source ./storage/backends/gpfs.sh && backend_leave $u false) >/dev/null
[ ! -f /var/mmfs/gen/mmsdrfs ] && ok "3 backend_leave cleans up a member" || ko "3 a member's configuration stayed"
rm -rf $run_dir/storage/$u
[ $had = 1 ] && mv /root/mmsdrfs.stc4-bak /var/mmfs/gen/mmsdrfs
rm -f $A/*.done

# 4 RBD delete: gone only when Ceph says the image (or pool) is not there
source ./storage/drivers/ceph_rbd.sh
drv_ceph_pool=cl_test4
drv_pool=test4
rbd_case() {
    eval "function drv_rbd() { $1; }"
    guard_error=""
    drv_delete volume-7
}
rbd_case 'return 0' && ok "4 deleted" || ko "4 a deleted image failed"
rbd_case 'echo "rbd: delete error: (2) No such file or directory" >&2; return 2' && ok "4 a missing image counts as deleted" || ko "4 missing image: $guard_error"
rbd_case 'echo "rbd: error opening pool cl_test4: (2) No such file or directory" >&2; return 2' && ok "4 a missing pool counts as deleted" || ko "4 missing pool: $guard_error"
rbd_case 'return 124' && ko "4 a timeout counted as deleted" || { [[ "$guard_error" == *"timed out"* ]] && ok "4 a timeout is a failure ($guard_error)" || ko "4 timeout reason: $guard_error"; }
rbd_case 'echo "rbd: error: image still has watchers" >&2; return 16' && ko "4 an open image counted as deleted" || { [[ "$guard_error" == *watchers* ]] && ok "4 an open image is a failure, with the reason" || ko "4 reason: $guard_error"; }
drv_delete other-7 && ko "4 a name that is not a volume image" || ok "4 only volume images"

echo "== $pass passed, $fail failed"
