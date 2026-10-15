#!/bin/bash
# sandbox tests of S5 (shared-storage-design.md §14, §6.2.6, §16 S5): the GPFS metrics hook with stub mm commands,
# stc_metrics.sh writing and dropping the textfiles, the pool metrics of the shared pool probe, the whole log upload
# to a local HTTP server, the orphan listing of a pool, the release of a failed disk without wiping it
S=/opt/cloudland/scripts/backend
A=/opt/cloudland/run/async_job
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
heartbeat() { for f in $(ls $A/*.done 2>/dev/null); do cat $f; rm -f $f; done; }
waitdone() { for i in $(seq 1 50); do ls $A/*.done >/dev/null 2>&1 && ! ls $A/*.in_progress >/dev/null 2>&1 && return 0; sleep 0.2; done; return 1; }
rm -f $A/*
export NODE_ID=7
cluster=aaaaaaaa-6666-2222-3333-444444444444
gone=dddddddd-6666-2222-3333-444444444444
pool=bbbbbbbb-6666-2222-3333-444444444444
tf=/var/lib/prometheus/node-exporter
mkdir -p $tf

# ---- the GPFS metrics hook, with stub commands ----
fake=$(mktemp -d)
cat >$fake/mmgetstate <<'EOF'
#!/bin/bash
echo "mmgetstate::HEADER:version:reserved:reserved:nodeName:nodeNumber:state:quorum:nodesUp:totalNodes:remarks:cnfsState:"
echo "mmgetstate::0:1:::sb1:1:active:2:3:3:quorum node:(undefined):"
EOF
cat >$fake/mmlsfs <<'EOF'
#!/bin/bash
echo "mmlsfs::HEADER:version:reserved:reserved:deviceName:fieldName:data:remarks:"
echo "mmlsfs::0:1:::fs1:defaultMountPoint:%2Fgpfs-sb6%2Ffs1::"
echo "mmlsfs::0:1:::fs2:defaultMountPoint:%2Fgpfs-sb6%2Ffs2::"
EOF
cat >$fake/mmpmon <<'EOF'
#!/bin/bash
cat >/dev/null
echo "_fs_io_s_ _n_ 10.0.0.1 _nn_ sb1 _rc_ 0 _t_ 1 _tu_ 2 _cl_ c.sb1 _fs_ fs1 _d_ 3 _br_ 1000 _bw_ 2000 _oc_ 5 _cc_ 4 _rdc_ 10 _wc_ 20 _dir_ 1 _iu_ 2"
echo "_fs_io_s_ _n_ 10.0.0.1 _nn_ sb1 _rc_ 1 _t_ 1 _tu_ 2 _cl_ c.sb1 _fs_ bad;name _d_ 3 _br_ 9 _bw_ 9 _oc_ 5 _cc_ 4 _rdc_ 9 _wc_ 9 _dir_ 1 _iu_ 2"
EOF
cat >$fake/mmhealth <<'EOF'
#!/bin/bash
echo "mmhealth:State:HEADER:version:reserved:reserved:node:component:entityname:entitytype:status:laststatuschange:lastcheck:adminname:"
echo "mmhealth:State:0:1:::sb1:GPFS:sb1:NODE:TIPS:x:y:sb1:"
echo "mmhealth:State:0:1:::sb1:DISK:sb1:NODE:DEGRADED:x:y:sb1:"
echo "mmhealth:State:0:1:::sb1:DISK:d1:NSD:FAILED:x:y:sb1:"
EOF
chmod +x $fake/*
mkdir -p /opt/cloudland/run/storage/$cluster /gpfs-sb6/fs1
out=$(cd /opt/cloudland/scripts/kvm/storage && source ../../cloudrc && source ../storage_lib.sh && source ./stc_lib.sh && backend_load gpfs &&
    gpfs_bin=$fake && backend_metrics $cluster)
l="cluster=\"$cluster\""
grep -qx "cloudland_gpfs_node_active{$l} 1" <<<"$out" && ok "metrics: GPFS active" || ko "active: $out"
grep -qx "cloudland_gpfs_filesystem_mounted{$l,fs=\"fs1\"} 0" <<<"$out" && grep -qx "cloudland_gpfs_filesystem_mounted{$l,fs=\"fs2\"} 0" <<<"$out" &&
    ok "metrics: both file systems, decoded mount points, not GPFS here" || ko "mounted: $out"
[ "$(cat /opt/cloudland/run/storage/$cluster/gpfs_mounts)" = $'fs1\t/gpfs-sb6/fs1\nfs2\t/gpfs-sb6/fs2' ] && ok "mount points kept" || ko "gpfs_mounts: $(cat -A /opt/cloudland/run/storage/$cluster/gpfs_mounts)"
grep -qx "cloudland_gpfs_read_bytes_total{$l,fs=\"fs1\"} 1000" <<<"$out" && grep -qx "cloudland_gpfs_writes_total{$l,fs=\"fs1\"} 20" <<<"$out" &&
    ! grep -q 'bad' <<<"$out" && ok "metrics: I/O counters, a bad row skipped" || ko "io: $out"
grep -qx "cloudland_gpfs_component_healthy{$l,component=\"gpfs\"} 1" <<<"$out" && grep -qx "cloudland_gpfs_component_healthy{$l,component=\"disk\"} 0" <<<"$out" &&
    [ "$(grep -c component_healthy <<<"$out")" = 2 ] && ok "metrics: node components, tips fine, NSD rows left out" || ko "health: $out"
# GPFS not answering: the mount points remembered, no I/O
rm -f $fake/mmlsfs $fake/mmgetstate
out=$(cd /opt/cloudland/scripts/kvm/storage && source ../../cloudrc && source ../storage_lib.sh && source ./stc_lib.sh && backend_load gpfs &&
    gpfs_bin=$fake && printf '#!/bin/bash\nexit 1\n' >$fake/mmgetstate && chmod +x $fake/mmgetstate && backend_metrics $cluster)
grep -qx "cloudland_gpfs_node_active{$l} 0" <<<"$out" && grep -q 'fs="fs2"} 0' <<<"$out" && ! grep -q read_bytes <<<"$out" &&
    ok "GPFS down: not active, the remembered file systems unmounted, no counters" || ko "down: $out"
rm -rf $fake

# ---- stc_metrics.sh: a file per GPFS cluster, the info line only for a managed Ceph cluster (its mgr is scraped), the
# file of a cluster left is removed ----
echo gpfs >/opt/cloudland/run/storage/$cluster/member
mkdir -p /opt/cloudland/run/storage/$gone
echo ceph >/opt/cloudland/run/storage/$gone/member
echo "stale 1" >$tf/cloudland_storage_eeeeeeee-6666-2222-3333-444444444444.prom
echo "ceph 1" >$tf/cloudland_storage_$gone.prom
# the real backend: GPFS is installed here but not running
chmod 000 /usr/lpp/mmfs/bin/mmfsd 2>/dev/null
$S/storage/stc_metrics.sh
f=$tf/cloudland_storage_$cluster.prom
[ -f $f ] && grep -q "cloudland_gpfs_node_active{$l} 0" $f && ok "stc_metrics wrote the GPFS cluster: $(head -1 $f)" || ko "metrics file: $(cat $f 2>&1)"
[ ! -f $tf/cloudland_storage_eeeeeeee-6666-2222-3333-444444444444.prom ] &&
    [ "$(cat $tf/cloudland_storage_$gone.prom 2>/dev/null)" = "cloudland_storage_cluster_info{cluster=\"$gone\",kind=\"ceph\",mode=\"managed\"} 1" ] &&
    ok "stc_metrics dropped the file of a cluster left; a managed Ceph cluster only has its info line" || ko "stale files: $(ls $tf) / $(cat $tf/cloudland_storage_$gone.prom 2>&1)"
grep -qx "cloudland_storage_cluster_info{cluster=\"$cluster\",kind=\"gpfs\",mode=\"managed\"} 1" $f &&
    ok "stc_metrics: the info line of the GPFS cluster" || ko "info line: $(cat $f)"
exec 9>/var/lock/cloudland-storage-metrics.lock; flock 9
rm -f $f
$S/storage/stc_metrics.sh
[ ! -f $f ] && ok "stc_metrics skips a round while one runs" || ko "lock"
exec 9>&-
chmod 755 /usr/lpp/mmfs/bin/mmfsd 2>/dev/null
rm -rf /opt/cloudland/run/storage/$gone

# ---- the shared pool probe writes the pool metrics ----
mnt=/gpfs-sb6/fs1
root=$mnt/cl_bbbbbbbb
umount $mnt 2>/dev/null
mount -t tmpfs -o size=64m tmpfs $mnt
mkdir -p $root/volumes $root/images $root/nvram $root/tmp
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool
jq -cn --arg p $pool --arg r $root '[{driver:"gpfs", pool:$p, root:$r, fs_type:"tmpfs"}]' | $S/sync_shared_pools.sh $cluster
$S/shared_pool_probe.sh $pool $cluster
pf=$tf/cloudland_shared_pool_$pool.prom
pl="cluster=\"$cluster\",pool=\"$pool\""
grep -qx "cloudland_shared_pool_up{$pl} 1" $pf && grep -q "cloudland_shared_pool_size_bytes{$pl} [1-9]" $pf && grep -q "cloudland_shared_pool_used_bytes{$pl} " $pf &&
    ok "probe metrics: $(tr '\n' ' ' <$pf)" || ko "probe metrics: $(cat $pf 2>&1)"
rm -f $root/.cloudland-pool
$S/shared_pool_probe.sh $pool $cluster
[ "$(cat $pf)" = "cloudland_shared_pool_up{$pl} 0" ] && ok "probe metrics of an unavailable pool: only up 0" || ko "down metrics: $(cat $pf)"
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool

# ---- the orphan listing ----
touch $root/volumes/volume-1.disk $root/nvram/inst-2_VARS.fd $root/tmp/x
dd if=/dev/zero of=$root/images/image-3-ab.qcow2 bs=1k count=3 2>/dev/null
touch -d '2 days ago' $root/volumes/volume-1.disk
$S/storage/stc_pool_objects.sh $pool $cluster
waitdone
out=$(heartbeat)
line=$(grep -o "|:-COMMAND-:| shared_pool_objects '$pool' '[^']*'" <<<"$out")
json=$(awk -F"'" '{print $4}' <<<"$line" | base64 -d | gunzip)
names=$(jq -r '.objects | map(.name) | sort | join(",")' <<<"$json")
[ "$names" = ".cloudland-pool,images/image-3-ab.qcow2,nvram/inst-2_VARS.fd,tmp/x,volumes/volume-1.disk" ] && [ "$(jq '.objects[] | select(.name == "images/image-3-ab.qcow2") | .size' <<<"$json")" = 3072 ] &&
    [ "$(jq '.objects[] | select(.name == "volumes/volume-1.disk") | .mtime' <<<"$json")" -lt $(($(date +%s) - 86400)) ] && [ "$(jq .truncated <<<"$json")" = false ] &&
    ok "pool objects: $names" || ko "objects: $out"
$S/storage/stc_pool_objects.sh cccccccc-6666-2222-3333-444444444444 $cluster
waitdone
out=$(heartbeat)
[ "$(awk -F"'" '{print $4}' <<<"$(grep -o "|:-COMMAND-:| shared_pool_objects [^|]*" <<<"$out")" | base64 -d | gunzip | jq -r .error)" = "the pool is not in the pool list of this host" ] &&
    ok "a pool not on this host: an error" || ko "unknown pool: $out"
$S/storage/stc_pool_objects.sh "x'; touch /tmp/pwn6; '" $cluster; [ ! -f /tmp/pwn6 ] && ok "a bad pool uuid is refused" || ko "injection"
objects() { awk -F"'" '{print $4}' <<<"$(grep -o "|:-COMMAND-:| shared_pool_objects '$pool' '[^']*'" <<<"$1")" | base64 -d | gunzip; }
# a listing that fails comes back with its reason (it ran in a subshell before, which kept guard_error to itself)
mv $root/.cloudland-pool $root/.marker-off
$S/storage/stc_pool_objects.sh $pool $cluster
waitdone
out=$(heartbeat)
err=$(objects "$out" | jq -r .error)
[[ "$err" == *"marker of $root is missing"* ]] && ok "a failed listing says why: $err" || ko "failed listing: $out"
mv $root/.marker-off $root/.cloudland-pool
# find exits 1 when a file went away while it walked: the rest is listed; a timeout is a failure
mkdir -p /tmp/sb6bin
printf '#!/bin/bash\n/usr/bin/find "$@"\nexit 1\n' >/tmp/sb6bin/find
chmod +x /tmp/sb6bin/find
PATH=/tmp/sb6bin:$PATH $S/storage/stc_pool_objects.sh $pool $cluster
waitdone
json=$(objects "$(heartbeat)")
[ "$(jq -r '.error // ""' <<<"$json")" = "" ] && [ "$(jq '.objects | length' <<<"$json")" = 5 ] && ok "find exiting 1 still lists the pool" || ko "find 1: $json"
printf '#!/bin/bash\nsleep 5\n' >/tmp/sb6bin/find
DRV_TIMEOUT=1 PATH=/tmp/sb6bin:$PATH $S/storage/stc_pool_objects.sh $pool $cluster
for i in $(seq 1 50); do ls $A/*.done >/dev/null 2>&1 && ! ls $A/*.in_progress >/dev/null 2>&1 && break; sleep 0.2; done
err=$(objects "$(heartbeat)" | jq -r .error)
[[ "$err" == *"failed (find: 124)"* ]] && ok "a listing that times out fails: $err" || ko "timeout: $err"
rm -rf /tmp/sb6bin
# the answer fits one callback line (1 MiB): a pool of many files is cut, and says so
mkdir -p $root/volumes/many
(cd $root/volumes/many && for i in $(seq 1 40); do head -c 30000 /dev/urandom | od -An -tx1 -v | tr -d ' \n' | fold -w 60 | head -1000 | xargs touch; done)
total=$(find $root -type f | wc -l)
$S/storage/stc_pool_objects.sh $pool $cluster
for i in $(seq 1 300); do ls $A/*.done >/dev/null 2>&1 && ! ls $A/*.in_progress >/dev/null 2>&1 && break; sleep 0.2; done
out=$(heartbeat)
payload=$(grep -o "|:-COMMAND-:| shared_pool_objects '$pool' '[^']*'" <<<"$out" | awk -F"'" '{print $4}')
json=$(base64 -d <<<"$payload" | gunzip)
n=$(jq '.objects | length' <<<"$json")
[ ${#payload} -le 1000000 ] && [ "$(jq .truncated <<<"$json")" = true ] && [ "$n" -lt "$total" ] && [ "$n" -gt 1000 ] &&
    ok "a big pool is cut to fit: $n of $total objects, ${#payload} bytes" || ko "big pool: ${#payload} bytes, $n of $total, truncated $(jq .truncated <<<"$json")"
rm -rf $root/volumes/many
# a health check that finds another running returns, so its job still ends as done

exec 8>/var/lock/cloudland-storage-health-$cluster.lock
flock -n 8
echo '{"kind": "gpfs", "mode": "managed"}' | $S/storage/stc_health.sh $cluster
sleep 2
! ls $A/*.in_progress >/dev/null 2>&1 && ls $A/*.done >/dev/null 2>&1 && ok "a skipped health check ends as done" || ko "a skipped health check: $(ls $A)"
heartbeat >/dev/null
exec 8>&-
# the pool list is cleared: the probe and its metrics go
echo '[]' | $S/sync_shared_pools.sh $cluster
[ ! -f $pf ] && ok "a pool dropped from the list loses its metrics" || ko "metrics left: $(ls $tf)"

# ---- the whole log upload ----
log=/opt/cloudland/log/storage/run-424242.log
mkdir -p /opt/cloudland/log/storage
printf 'line 1\nline 2\n' >$log
port=18766
rm -f /tmp/sb6-upload
python3 - $port <<'EOF' &
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(n)
        with open('/tmp/sb6-upload', 'w') as f:
            f.write(self.path + '\n' + self.headers.get('X-Log-Token', '') + '\n' + body.decode())
        code = 200 if self.headers.get('X-Log-Token') == 'a' * 64 else 401
        self.send_response(code); self.end_headers(); self.wfile.write(b'{}')
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
EOF
srv=$!
sleep 1
$S/storage/stc_upload_log.sh 424242 "http://127.0.0.1:$port/api/v1/internal/storage_runs/424242/log?expiry=99" $(printf 'a%.0s' $(seq 64))
waitdone
out=$(heartbeat)
[ "$(sed -n 1p /tmp/sb6-upload)" = "/api/v1/internal/storage_runs/424242/log?expiry=99&size=14" ] && [ "$(sed -n 2p /tmp/sb6-upload)" = "$(printf 'a%.0s' $(seq 64))" ] &&
    [ "$(sed -n '3,4p' /tmp/sb6-upload)" = $'line 1\nline 2' ] && [ -z "$out" ] && ok "upload: path, token, size and the log; no callback" || ko "upload: $(cat /tmp/sb6-upload) / $out"
$S/storage/stc_upload_log.sh 424242 "http://127.0.0.1:$port/api/v1/internal/storage_runs/424242/log?expiry=99" $(printf 'b%.0s' $(seq 64))
waitdone
out=$(heartbeat)
[[ "$out" == *"|:-COMMAND-:| storage_run_log '424242' 'error' 'the upload to clapi failed (HTTP 401)'"* ]] && ok "upload refused: reported" || ko "refused: $out"
$S/storage/stc_upload_log.sh 424243 "http://127.0.0.1:$port/x?expiry=1" $(printf 'a%.0s' $(seq 64))
waitdone
out=$(heartbeat)
[[ "$out" == *"storage_run_log '424243' 'error' 'the host has no log of this run'"* ]] && ok "no log: reported" || ko "no log: $out"
$S/storage/stc_upload_log.sh 424242 "http://127.0.0.1:$port/x?expiry=1;touch /tmp/pwn6b" $(printf 'a%.0s' $(seq 64)); [ ! -f /tmp/pwn6b ] && ok "a bad url is refused" || ko "url injection"
$S/storage/stc_upload_log.sh 424242 "http://127.0.0.1:$port/x?expiry=1" 'short'; ls $A/*.in_progress >/dev/null 2>&1 && ko "a bad token started" || ok "a bad token is refused"
kill $srv 2>/dev/null
rm -f $log /tmp/sb6-upload

# ---- the release of a failed disk: its claim goes, nothing is wiped ----
id="wwn-0xsb6dead"
claimed=/opt/cloudland/run/storage/$cluster/claimed-$(echo -n "$id" | md5sum | cut -c1-16)
touch $claimed
in=$(jq -cn --arg c $cluster --arg i $id '{cluster_uuid: $c, kind: "gpfs", keep: [], release: [{id: $i, serial: "X", size_bytes: 1}], no_wipe: true}')
echo "$in" | $S/storage/stc_release_disks.sh 990601 >/dev/null
for i in $(seq 1 50); do [ -f /opt/cloudland/run/storage/jobs/990601/exit ] && break; sleep 0.2; done
heartbeat >/dev/null
[ ! -f $claimed ] && grep -q "not wiping $id: it failed" /opt/cloudland/log/storage/run-990601.log && [ "$(jq -r .wiped /opt/cloudland/run/storage/jobs/990601/result.json)" = 0 ] &&
    ok "a failed disk is released without wiping" || ko "release: $(cat /opt/cloudland/log/storage/run-990601.log)"
rm -rf /opt/cloudland/run/storage/jobs/990601 /opt/cloudland/log/storage/run-990601.log

umount $mnt 2>/dev/null
rm -rf /gpfs-sb6 /opt/cloudland/run/storage/$cluster $tf/cloudland_storage_$cluster.prom
echo "== $pass passed, $fail failed"
