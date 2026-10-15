#!/bin/bash
# sandbox tests of the metrics of the storage clusters added to the node scripts (shared-storage-design.md §14.3): the
# capacity of the GPFS storage pools (mmdf, KiB) and the inodes of the filesets of the CloudLand pools, an imported Ceph
# cluster read by its client user, and the info line of every cluster. Stub commands stand in for GPFS and Ceph.
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
gp=eeeeeeee-1414-2525-3636-474747474747
cp=ffffffff-1414-2525-3636-474747474747
pool=abababab-1111-2222-3333-444444444444
fake=$(mktemp -d)
export FAKE=$fake
cat >$fake/mmdf <<'EOF'
#!/bin/bash
echo "mmdf:nsd:HEADER:version:reserved:reserved:nsdName:storagePool:diskSize:"
echo "mmdf:nsd:0:1:::cl1h1d1:system:1048576:"
echo "mmdf:poolTotal:HEADER:version:reserved:reserved:poolName:poolSize:freeBlocks:freeBlocksPct:"
echo "mmdf:poolTotal:0:1:::system:1073741824:536870912:50:"
echo "mmdf:poolTotal:0:1:::data:2147483648:2147483648:100:"
EOF
cat >$fake/mmrepquota <<'EOF'
#!/bin/bash
echo "mmrepquota::HEADER:version:reserved:reserved:filesystemName:quotaType:id:name:blockUsage:blockQuota:blockLimit:blockInDoubt:blockGrace:filesUsage:filesQuota:"
echo "mmrepquota::0:1:::fs1:FILESET:1:cl_abababab:1024:0:0:0:none:1234:0:"
echo "mmrepquota::0:1:::fs1:FILESET:0:root:1:0:0:0:none:9:0:"
EOF
cat >$fake/mmlsfileset <<'EOF'
#!/bin/bash
echo "mmlsfileset::HEADER:version:reserved:reserved:filesystemName:filesetName:id:rootInode:status:path:maxInodes:allocInodes:"
echo "mmlsfileset::0:1:::fs1:root:0:3:Linked:%2Fgpfs%2Ffs1:0:0:"
echo "mmlsfileset::0:1:::fs1:cl_abababab:1:131075:Linked:%2Fgpfs%2Ffs1%2Fcl_abababab:100000:100352:"
EOF
cat >$fake/ceph <<'EOF'
#!/bin/bash
echo "ceph $*" >>$FAKE/calls
case "$*" in
    *" -s -f json") echo '{"pgmap":{"bytes_total":3000000000000,"bytes_used":1200000000},"osdmap":{"num_osds":3,"num_up_osds":3,"num_in_osds":2}}' ;;
    *"df detail -f json") echo '{"pools":[{"name":"vms","stats":{"rd":10,"rd_bytes":4096,"wr":5,"wr_bytes":8192}},{"name":"b","stats":{"rd":1,"rd_bytes":100,"wr":2,"wr_bytes":200}}]}' ;;
esac
EOF
chmod +x $fake/*

mkdir -p /opt/cloudland/run/storage/$gp
echo "[{\"driver\":\"gpfs\",\"pool\":\"$pool\",\"root\":\"/gpfs/fs1/cl_abababab\",\"fs_type\":\"gpfs\"},{\"driver\":\"gpfs\",\"pool\":\"$pool\",\"root\":\"/gpfs/fs9/cl_other\"}]" \
    >/opt/cloudland/run/storage/$gp/shared_pools.json

# ---- GPFS: the storage pools and the filesets ----
out=$(cd $S && source ../../cloudrc && source ../storage_lib.sh && source ./backends/gpfs.sh && gpfs_bin=$fake && gpfs_pool_metrics $gp fs1 /gpfs/fs1)
grep -qx "cloudland_gpfs_pool_total_bytes{cluster=\"$gp\",fs=\"fs1\",gpfs_pool=\"system\"} 1099511627776" <<<"$out" && ok "pool total in bytes, no exponent" || ko "pool total: $out"
grep -qx "cloudland_gpfs_pool_free_bytes{cluster=\"$gp\",fs=\"fs1\",gpfs_pool=\"data\"} 2199023255552" <<<"$out" && ok "the data pool too" || ko "data pool: $out"
grep -qx "cloudland_gpfs_fileset_inodes_used{cluster=\"$gp\",pool=\"$pool\"} 1234" <<<"$out" && grep -qx "cloudland_gpfs_fileset_inodes_max{cluster=\"$gp\",pool=\"$pool\"} 100000" <<<"$out" &&
    ok "the inodes of the fileset of the pool" || ko "inodes: $out"
[ "$(grep -c inodes <<<"$out")" = 2 ] && ok "only the pools under this mount point" || ko "other pools: $out"
# No mmdf answer: nothing, no error
out=$(cd $S && source ../../cloudrc && source ../storage_lib.sh && source ./backends/gpfs.sh && gpfs_bin=/nonexistent && gpfs_pool_metrics $gp fs1 /gpfs/fs1 2>&1)
! grep -q 'cloudland_gpfs_pool_' <<<"$out" && ok "GPFS not answering: no pool line" || ko "no gpfs: $out"

# ---- an imported Ceph cluster, read by its client user ----
mkdir -p /etc/ceph
printf '[global]\n    fsid = %s\n    mon_host = 10.0.0.1\n[client.cloudland]\n    keyring = /etc/ceph/%s.client.cloudland.keyring\n' $cp $cp >/etc/ceph/$cp.conf
: >$fake/calls
out=$(cd $S && export PATH=$fake:$PATH && source ../../cloudrc && source ../storage_lib.sh && source ./backends/ceph.sh && backend_metrics $cp external)
grep -qx "cloudland_ceph_total_bytes{cluster=\"$cp\"} 3000000000000" <<<"$out" && grep -qx "cloudland_ceph_osds_in{cluster=\"$cp\"} 2" <<<"$out" &&
    ok "capacity and OSDs" || ko "ceph status: $out"
grep -qx "cloudland_ceph_write_bytes_total{cluster=\"$cp\"} 8392" <<<"$out" && grep -qx "cloudland_ceph_reads_total{cluster=\"$cp\"} 11" <<<"$out" &&
    ok "the counters of all pools summed" || ko "ceph counters: $out"
grep -q -- "--id cloudland" $fake/calls && ok "as the client user of the configuration" || ko "calls: $(cat $fake/calls)"
: >$fake/calls
out=$(cd $S && export PATH=$fake:$PATH && source ../../cloudrc && source ../storage_lib.sh && source ./backends/ceph.sh && backend_metrics $cp managed)
[ -z "$out" ] && [ ! -s $fake/calls ] && ok "a managed cluster: nothing from its hosts (its mgr is scraped)" || ko "managed: $out"

# ---- the collector: the info line, the imported Ceph cluster found by its configuration ----
mkdir -p /opt/cloudland/run/storage/$cp
tf=/var/lib/prometheus/node-exporter
mkdir -p $tf
(cd $S && export PATH=$fake:$PATH && bash ./stc_metrics.sh)
f=$tf/cloudland_storage_$cp.prom
grep -qx "cloudland_storage_cluster_info{cluster=\"$cp\",kind=\"ceph\",mode=\"external\"} 1" $f 2>/dev/null && grep -q cloudland_ceph_total_bytes $f &&
    ok "the collector writes the info line and the imported cluster" || ko "collector: $(cat $f 2>&1)"

# review 2026-10-07: the pool list of a cluster whose file system this host's cluster mounts (remote) is not reported
rp=cccccccc-1414-2222-3333-777777777777
(cd $S && source ../../cloudrc && source ../storage_lib.sh && shared_pools_apply $rp '[{"pool":"dddddddd-1414-2222-3333-888888888888","driver":"gpfs"}]' remote)
[ -f /opt/cloudland/run/storage/$rp/remote ] && ok "a remote pool list is marked" || ko "no remote marker"
(cd $S && export PATH=$fake:$PATH && bash ./stc_metrics.sh)
[ ! -f $tf/cloudland_storage_$rp.prom ] && ok "the collector skips the owner of a remote mount" || ko "remote reported: $(cat $tf/cloudland_storage_$rp.prom 2>&1)"
(cd $S && source ../../cloudrc && source ../storage_lib.sh && shared_pools_apply $rp '[]')
[ ! -f /opt/cloudland/run/storage/$rp/remote ] && ok "the marker goes with a list that is not remote" || ko "marker stays"
rm -rf /opt/cloudland/run/storage/$rp

rm -rf $fake /opt/cloudland/run/storage/$gp /opt/cloudland/run/storage/$cp /etc/ceph/$cp.conf $tf/cloudland_storage_$cp.prom $tf/cloudland_storage_$gp.prom
echo "== $pass passed, $fail failed"
