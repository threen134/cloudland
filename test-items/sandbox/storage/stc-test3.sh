#!/bin/bash
# sandbox tests of the shared pools (shared-storage-design.md §9): the pool lists and probes, the heartbeat report,
# the file pool driver (gpfs.sh over a tmpfs standing in for GPFS), the volume scripts, stc_pools.sh, the pending start
# mapping and attach with a stub virsh
S=/opt/cloudland/scripts/backend
A=/opt/cloudland/run/async_job
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
heartbeat() { for f in $(ls $A/*.done 2>/dev/null); do cat $f; rm -f $f; done; }
command -v qemu-img >/dev/null || apt-get install -y qemu-utils >/dev/null 2>&1
command -v xmllint >/dev/null || apt-get install -y libxml2-utils >/dev/null 2>&1
rm -f $A/*
export NODE_ID=7

cluster=aaaaaaaa-1111-2222-3333-444444444444
pool=bbbbbbbb-1111-2222-3333-444444444444
pool2=cccccccc-1111-2222-3333-444444444444
mnt=/gpfs-sb/fs1
root=$mnt/cl_bbbbbbbb
umount $mnt 2>/dev/null
rm -rf /gpfs-sb /opt/cloudland/run/storage/$cluster
mkdir -p $mnt
mount -t tmpfs -o size=512m tmpfs $mnt
mkdir -p $root/volumes $root/images $root/nvram $root/tmp
list=$(jq -cn --arg p $pool --arg r $root '[{driver:"gpfs", pool:$p, root:$r, fs_type:"tmpfs"}]')

# pool lists
echo '{"not":"a list"}' | $S/sync_shared_pools.sh $cluster && ko "sync took an object" || ok "sync refuses an object"
echo "$list" | $S/sync_shared_pools.sh not-a-uuid && ko "sync took a bad cluster" || ok "sync refuses a bad cluster uuid"
echo "$list" | $S/sync_shared_pools.sh $cluster && [ "$(jq -r '.[0].pool' /opt/cloudland/run/storage/$cluster/shared_pools.json)" = $pool ] &&
    ok "sync keeps the list" || ko "sync list"

# probe: no marker, then ready, then wrong file system type
st=/opt/cloudland/run/pools/$pool.state
$S/shared_pool_probe.sh $pool $cluster
[ "$(jq -r .status $st)" = unavailable ] && [[ "$(jq -r .reason $st)" == *marker* ]] && ok "probe without marker: $(jq -r .reason $st)" || ko "probe no marker: $(cat $st)"
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool
$S/shared_pool_probe.sh $pool $cluster
[ "$(jq -r .status $st)" = ready ] && [ "$(jq -r .size $st)" -gt 0 ] && [ "$(jq -r .shared $st)" = true ] && ok "probe ready, size $(jq -r .size $st)" || ko "probe ready: $(cat $st)"
[ "$(cat /opt/cloudland/run/pools/$pool.shared)" = $cluster ] && ok "probe marks the state as shared" || ko "shared mark"
jq -c '.[0].fs_type = "gpfs"' <<<"$list" | $S/sync_shared_pools.sh $cluster
$S/shared_pool_probe.sh $pool $cluster
[ "$(jq -r .status $st)" = unavailable ] && [[ "$(jq -r .reason $st)" == *"not on a gpfs file system (tmpfs)"* ]] && ok "probe on the wrong file system: $(jq -r .reason $st)" || ko "probe type: $(cat $st)"
echo "$list" | $S/sync_shared_pools.sh $cluster

# heartbeat report: once, then nothing until something changes
cd /opt/cloudland/scripts/kvm
source ../cloudrc
source ./storage_lib.sh
script_dir=$(pwd)
eval "$(sed -n '/^function shared_pool_report()/,/^}/p' report_rc.sh)"
eval "$(sed -n '/^function boot_time()/,/^}/p; /^function pool_state_ok()/,/^}/p; /^function instance_pools()/,/^}/p; /^function instance_pools_ok()/,/^}/p' report_rc.sh)"
rm -f $pool_state_dir/shared.reported
$S/shared_pool_probe.sh $pool $cluster
out=$(shared_pool_report)
rep=$(awk -F"'" '{print $4}' <<<"$out" | base64 -d 2>/dev/null)
[[ "$out" == "|:-COMMAND-:| shared_pool_status '7' "* ]] && [ "$(jq -r '.[0].status' <<<"$rep")" = ready ] && [ "$(jq -r '.[0].pool' <<<"$rep")" = $pool ] &&
    ok "heartbeat reports the pool: $rep" || ko "report: $out"
out=$(shared_pool_report)
[ -z "$out" ] && ok "unchanged: no report" || ko "repeated report: $out"
rm -f $root/.cloudland-pool
$S/shared_pool_probe.sh $pool $cluster
out=$(shared_pool_report)
[ "$(awk -F"'" '{print $4}' <<<"$out" | base64 -d | jq -r '.[0].status')" = unavailable ] && ok "a change is reported at once" || ko "change: $out"
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool
$S/shared_pool_probe.sh $pool $cluster
# a probe that hangs: its state goes stale
touch -d '-3 min' $pool_state_dir/$pool.pid
sleep 300 & echo $! >/dev/null
fake=$!
bash -c "exec -a 'bash $S/shared_pool_probe.sh $pool $cluster ' sleep 300" & hang=$!
echo $hang >$pool_state_dir/$pool.pid
touch -d '-3 min' $pool_state_dir/$pool.pid
out=$(shared_pool_report)
[[ "$(awk -F"'" '{print $4}' <<<"$out" | base64 -d | jq -r '.[0].reason')" == "probe stuck"* ]] && ok "a hanging probe is reported stuck" || ko "stuck: $out"
kill $hang $fake 2>/dev/null; rm -f $pool_state_dir/$pool.pid
# a state written before this boot (the host restarted): not checked yet, whatever it said. A live probe keeps the
# report from starting a new one that would race with the read
bash -c "exec -a 'bash $S/shared_pool_probe.sh $pool $cluster ' sleep 300" & hang=$!
echo $hang >$pool_state_dir/$pool.pid
jq --argjson t $(( $(boot_time) - 60 )) '.ts = $t | .status = "ready"' $st >$st.tmp && mv $st.tmp $st
out=$(shared_pool_report)
r=$(awk -F"'" '{print $4}' <<<"$out" | base64 -d | jq -r '.[0] | "\(.status) \(.reason)"')
[ "$r" = "unavailable not checked yet since the host started" ] && ok "a state from before the boot is not trusted: $r" || ko "before boot: $r ($out)"
kill $hang 2>/dev/null; rm -f $pool_state_dir/$pool.pid

# volumes: create, refuse twice, refuse a wrong path, resize, delete
vol() { jq -cn --arg p $pool --arg r $root --arg path "$1" --argjson s ${2:-1} --argjson o ${3:-0} --argjson i ${4:-0} \
    '{driver:"gpfs", pool:$p, root:$r, fs_type:"tmpfs", path:$path, size_gb:$s, old_gb:$o, instance_id:$i}'; }
out=$(vol $root/volumes/volume-5.disk 1 | $S/create_volume_shared.sh 5 u5)
[ "$out" = "|:-COMMAND-:| create_volume_shared '5' 'available' '-'" ] && qemu-img info $root/volumes/volume-5.disk | grep -q "virtual size: 1 GiB" &&
    ok "volume created" || ko "create: $out"
out=$(vol $root/volumes/volume-5.disk 1 | $S/create_volume_shared.sh 5 u5)
[[ "$out" == *"'error'"*"already exists"* ]] && ok "an existing file is never overwritten" || ko "create twice: $out"
out=$(vol $root/volumes/volume-6.disk 1 | $S/create_volume_shared.sh 5 u5)
[[ "$out" == *"'error'"*"is not the file of volume 5"* ]] && ok "a path that is not the volume's is refused" || ko "path: $out"
out=$(vol $root/volumes/../volume-5.disk 1 | $S/create_volume_shared.sh 5 u5)
[[ "$out" == *"'error'"* ]] && ok "a path with .. is refused" || ko "dotdot: $out"
out=$(jq -cn --arg p $pool '{driver:"gpfs", pool:$p, root:"/", fs_type:"", path:"/volumes/volume-9.disk", size_gb:1}' | $S/create_volume_shared.sh 9 u9)
[[ "$out" == *"'error'"*"invalid pool root"* ]] && [ ! -e /volumes/volume-9.disk ] && ok "root / refused" || ko "root /: $out"
out=$(jq -cn --arg p $pool --arg r $root '{driver:"../x", pool:$p, root:$r, path:"x"}' | $S/create_volume_shared.sh 9 u9)
[[ "$out" == *"invalid pool driver"* ]] && ok "a bad driver name is refused" || ko "driver: $out"
out=$(vol $root/volumes/volume-5.disk 2 1 | $S/resize_volume_shared.sh 5 u5)
[ "$out" = "|:-COMMAND-:| resize_volume '5' 'success'" ] && qemu-img info $root/volumes/volume-5.disk | grep -q "virtual size: 2 GiB" &&
    ok "volume resized offline" || ko "resize: $out"
out=$(vol $root/volumes/volume-5.disk 1 2 | $S/resize_volume_shared.sh 5 u5)
[ "$out" = "|:-COMMAND-:| resize_volume '5' 'error' '2'" ] && ok "shrinking refused, actual size reported" || ko "shrink: $out"

# stub virsh: attach writes the disk definition of the driver
stub=/root/stc-stub; mkdir -p $stub
cat >$stub/virsh <<'EOS'
#!/bin/bash
echo "$*" >>/root/stc-stub/calls
case "$1" in
    domblklist) printf ' Target   Source\n------------------\n vda      /x/inst.disk\n' ;;
    attach-device) cp "$3" /root/stc-stub/attached.xml ;;
    domstate) echo "running" ;;
    blockresize) exit 0 ;;
esac
exit 0
EOS
chmod +x $stub/virsh; rm -f $stub/calls
mkdir -p $xml_dir/inst-99
out=$(vol $root/volumes/volume-5.disk | PATH=$stub:$PATH $S/attach_volume_shared.sh 99 5 u5)
[ "$out" = "|:-COMMAND-:| attach_volume_shared '99' '5' 'vdb'" ] && grep -q "cache='none'" $stub/attached.xml && grep -q "error_policy='stop'" $stub/attached.xml &&
    grep -q "source file='$root/volumes/volume-5.disk'" $stub/attached.xml && [ -f $xml_dir/inst-99/disk-5.xml ] && ok "attached as vdb, cache none, stop on error" || ko "attach: $out $(cat $stub/attached.xml)"
out=$(vol $root/volumes/volume-8.disk | PATH=$stub:$PATH $S/attach_volume_shared.sh 99 8 u8)
[[ "$out" == "|:-COMMAND-:| attach_volume_shared '99' '8' '-' 'volume "*"not found'" ]] && ok "a missing file is not attached" || ko "attach missing: $out"
out=$(vol $root/volumes/volume-5.disk 3 2 99 | PATH=$stub:$PATH $S/resize_volume_shared.sh 5 u5)
[ "$out" = "|:-COMMAND-:| resize_volume '5' 'success'" ] && grep -q "blockresize inst-99 vdb 3G" $stub/calls &&
    ok "a volume of a running instance is resized through QEMU" || ko "online resize: $out $(cat $stub/calls)"

# pending start: a disk under the root of a shared pool belongs to it
cat >$xml_dir/inst-99/inst-99.xml <<EOX
<domain><devices><disk type='file' device='disk'><source file='$root/volumes/volume-5.disk'/></disk>
<disk type='file' device='disk'><source file='$cache_dir/instance/inst-99.disk'/></disk></devices></domain>
EOX
p=$(instance_pools 99 | xargs)
[ "$p" = "$pool builtin" ] || [ "$p" = "builtin $pool" ] && ok "pools of the instance: $p" || ko "instance pools: $p"
$S/shared_pool_probe.sh $pool $cluster
instance_pools_ok 99 && ok "instance can start: its shared pool is ready since boot" || ko "pools ok"
rm -f $root/.cloudland-pool; $S/shared_pool_probe.sh $pool $cluster
instance_pools_ok 99 && ko "started with its pool unavailable" || ok "instance waits while its shared pool is unavailable"
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool
rm -rf $xml_dir/inst-99

# delete
out=$(vol $root/volumes/volume-5.disk | $S/delete_volume_shared.sh 5 u5)
[ "$out" = "|:-COMMAND-:| clear_volume '5' 'deleted' '-'" ] && [ ! -e $root/volumes/volume-5.disk ] && ok "volume deleted" || ko "delete: $out"
touch $root/.keep
out=$(jq -cn --arg p $pool --arg r $root '{driver:"gpfs", pool:$p, root:$r, fs_type:"tmpfs", path:($r + "/.keep")}' | $S/delete_volume_shared.sh 5 u5)
[[ "$out" == *"'error'"* ]] && [ -e $root/.keep ] && ok "only the file of the volume can be deleted" || ko "delete other: $out"

# the pool not mounted: nothing is written on the root file system under it
umount $mnt
out=$(vol $root/volumes/volume-7.disk 1 | $S/create_volume_shared.sh 7 u7)
[[ "$out" == *"'error'"*"not on a tmpfs file system"* ]] && [ ! -e $root/volumes/volume-7.disk ] && ok "unmounted pool: refused, nothing written ($out)" || ko "unmounted: $out"
mount -t tmpfs -o size=512m tmpfs $mnt
mkdir -p $root/volumes $root/tmp
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool

# stc_pools.sh: the list and the check of a new pool, as a task step
r=$((RANDOM * 10 + 3))
jq -cn --arg c $cluster --argjson l "$list" --arg p $pool '{cluster_uuid:$c, pools:$l, check:$p}' | $S/storage/stc_pools.sh $r
sleep 4
line=$(heartbeat | grep "'$r'")
res=$(awk -F"'" '{print $8}' <<<"$line" | base64 -d)
[[ "$line" == *"'$r' 'succeeded'"* ]] && [ "$(jq -r .result.check.status <<<"$res")" = ready ] && ok "stc_pools: list and check ready" || ko "stc_pools: $line $res"

# pruning: a pool no list holds any more loses its probe state
jq -cn --arg p2 $pool2 --arg r $mnt/other '[{driver:"gpfs", pool:$p2, root:$r, fs_type:"tmpfs"}]' | $S/sync_shared_pools.sh $cluster
[ ! -f $pool_state_dir/$pool.state ] && [ ! -f $pool_state_dir/$pool.shared ] && ok "dropped pool: its state is gone" || ko "prune"
echo '[]' | $S/sync_shared_pools.sh $cluster

# gpfs_pool.sh refuses to register a directory that is not on GPFS
r=$((r + 1))
jq -cn --arg c $cluster --arg p $pool --arg r $root --arg m $mnt '{action:"register", cluster_uuid:$c, pool_uuid:$p, junction:$r, mount_point:$m}' | $S/storage/gpfs_pool.sh $r
sleep 3
line=$(heartbeat | grep "'$r'")
[[ "$line" == *"'failed'"* ]] && [[ "$(awk -F"'" '{print $8}' <<<"$line" | base64 -d | jq -r .message)" == *"not on GPFS"* ]] && ok "register refuses a directory not on GPFS" || ko "register: $line"

umount $mnt; rm -rf /gpfs-sb /opt/cloudland/run/storage/$cluster
echo "== $pass passed, $fail failed"
