/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// A real Ceph cluster in the WSL sandbox, deployed, used and deleted through the engine of clapi
// (shared-storage-design.md §8): one host in the test layout (one mon, one mgr, one OSD on an LVM volume over a loop
// file), an RBD pool, a volume made and removed by the shared volume scripts, and the volume attached live to a TCG
// domain and grown online, which checks the libvirt disk (the configuration file of the cluster and the secret of its
// client) against real librbd. Needs what TestStorageTaskWSL needs, plus in WSL: docker, cephadm and ceph-common of
// the distribution, the image quay.io/ceph/ceph:v<version of ceph-common>, libvirt with qemu-system-x86, the root
// mount shared (mount --make-rshared /), and no other Ceph cluster on the instance.
//
//	CEPH_WSL_E2E=1 CLAPI_TEST_DB_URI="postgres://..." go test ./src/rpcs -run TestStorageCephWSL -v -timeout 60m
//
// CEPH_WSL_KEEP=1 leaves the cluster in place. A failed run may leave it too: cephadm rm-cluster --force --zap-osds
// --fsid <cluster uuid> in WSL, then vgremove the clceph-* volume group.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"api/src/model"
	"api/src/services"

	. "api/src/common"

	"github.com/spf13/viper"
)

func TestStorageCephWSL(t *testing.T) {
	if os.Getenv("CEPH_WSL_E2E") == "" {
		t.Skip("CEPH_WSL_E2E is not set: this test deploys Ceph in WSL")
	}
	hw := newWSLHarness(t)
	db, ctx := hw.db, hw.ctx
	viper.Set("vpn.secret_key", "storage-ceph-wsl-test")
	if err := SecretStoreReady(); err != nil {
		t.Fatalf("the credential store: %v", err)
	}
	out, err := wsl(0, `mount --make-rshared /; ls -d /var/lib/ceph/*-*-*-*-*/ 2>/dev/null | wc -l; ip -4 -br addr show eth0 | awk '{print $3}' | cut -d/ -f1`)
	fields := strings.Fields(out)
	if err != nil || len(fields) != 2 {
		t.Fatalf("WSL: %v %s", err, out)
	}
	if fields[0] != "0" {
		t.Fatalf("WSL runs a Ceph cluster already")
	}
	ip := fields[1]
	host := int32(9321)
	// Claims of a cluster an earlier failed run left behind
	must(t, db.Unscoped().Where("hostid = ?", host).Delete(&model.StorageClusterDisk{}).Error)
	must(t, db.Unscoped().Where("hostid = ?", host).Delete(&model.StorageClusterNode{}).Error)
	must(t, db.Unscoped().Where("hostid = ?", host).Delete(&model.Hyper{}).Error)
	must(t, db.Create(&model.Hyper{Hostid: host, Hostname: "wsl-ceph", Status: 1, HostIP: ip}).Error)
	// A file of its own, 12 GiB: an OSD on a smaller device leaves the pool too little room for the checks
	out, err = wsl(0, `set -e; mkdir -p /root/stc-disks; cd /root/stc-disks; f=$PWD/ceph-e2e.img
if [ "$(stat -c %s $f 2>/dev/null || echo 0)" -lt 12884901888 ]; then
    for d in $(losetup -j $f -nO NAME); do losetup -d $d; done; rm -f $f; truncate -s 12G $f
fi
d=$(losetup -j $f -nO NAME | head -1); [ -n "$d" ] || d=$(losetup -f --show $f); wipefs -a -q $d
echo "$(lsblk -dbno SIZE $d)"`)
	var size int64
	if _, perr := fmt.Sscan(strings.TrimSpace(out), &size); err != nil || perr != nil {
		t.Fatalf("loop disk: %v %s", err, out)
	}
	diskID := "loop:/root/stc-disks/ceph-e2e.img"
	must(t, db.Unscoped().Where("hostid = ?", host).Delete(&model.HyperDisk{}).Error)
	must(t, db.Create(&model.HyperDisk{Hostid: host, DiskID: diskID, Name: "loop", SizeBytes: size, Media: "hdd", State: model.DiskFree,
		ScannedAt: time.Now()}).Error)
	// GPFS is installed in the sandbox (the GPFS spike), which makes every blank disk a possible NSD: hide it
	_, _ = wsl(0, "[ -x /usr/lpp/mmfs/bin/mmfsd ] && chmod 000 /usr/lpp/mmfs/bin/mmfsd && touch /tmp/stc-e2e-gpfs-hidden; true")
	defer wsl(0, "[ -f /tmp/stc-e2e-gpfs-hidden ] && chmod 500 /usr/lpp/mmfs/bin/mmfsd && rm -f /tmp/stc-e2e-gpfs-hidden; true")

	logRuns := func(id int64) {
		for _, r := range hw.taskRuns(t, id) {
			if r.Status != model.StorageRunSucceeded {
				t.Logf("run %d on %d: %s %s\n%s", r.ID, r.Hostid, r.Status, r.Message, r.LogTail)
			}
		}
	}
	name := fmt.Sprintf("ceph-wsl-%d", time.Now().Unix()%100000)
	cluster, task, err := services.StorageClusters.Create(ctx, &services.StorageClusterCreate{Name: name, Plan: &services.StoragePlan{
		Kind: model.StorageKindCeph, Params: json.RawMessage(`{"test":true,"osd_memory_target_mib":1024}`),
		Nodes: []*services.StorageNodePlan{{Hostid: host, Roles: []string{"admin", "mon", "mgr", "osd"}}},
		Disks: []*services.StorageDiskPlan{{Hostid: host, DiskID: diskID}}}})
	must(t, err)
	start := time.Now()
	if done := hw.waitTask(t, task.ID, 40*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed); done.Status != model.StorageTaskSucceeded {
		logRuns(task.ID)
		t.Fatalf("deployment: %s", done.Message)
	}
	t.Logf("deployed in %s", time.Since(start).Round(time.Second))
	c := &model.StorageCluster{}
	must(t, db.Take(c, cluster.ID).Error)
	disk := &model.StorageClusterDisk{}
	must(t, db.Where("cluster_id = ?", c.ID).Take(disk).Error)
	if c.Status != model.StorageClusterReady || c.ClusterRef != c.UUID || disk.Name != "osd.0" {
		t.Fatalf("deployed cluster %+v disk %+v", c, disk)
	}
	out, _ = wsl(0, fmt.Sprintf(`ceph --conf /etc/ceph/%[1]s.conf --id cloudland fsid; ceph --conf /var/lib/ceph/%[1]s/config/ceph.conf --keyring /var/lib/ceph/%[1]s/config/ceph.client.admin.keyring osd tree | grep -c ' up '; grep -c cloudland-storage-%[1]s /root/.ssh/authorized_keys; virsh secret-list | grep -c %[1]s`, c.UUID))
	if f := strings.Fields(out); len(f) != 4 || f[0] != c.UUID || f[1] != "1" || f[2] != "1" || f[3] != "1" {
		t.Fatalf("the cluster as the client and the admin see it (fsid, OSDs up, trust lines, secrets): %s", out)
	}
	// The OSD stops before the mon of its host, which then still hears it going down
	out, _ = wsl(0, fmt.Sprintf(`systemctl show -p After --value ceph-%[1]s@osd.0.service | tr ' ' '\n' | grep -cx "ceph-%[1]s@mon.$(hostname -s).service"`, c.UUID))
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("the OSD unit is not ordered after the mon of its host: %s", out)
	}
	// The daemons go after the VMs when the memory runs out (§6.7.2): the real processes, not the units, which only
	// run docker; memory.low is the reservation of each daemon (mon 2 GiB, mgr 1 GiB, OSD 1 GiB target + 512 MiB), and
	// system.slice, above the containers, covers their sum (a cgroup is protected only as far as its parents are)
	oomCheck := `for c in mon mgr osd; do p=$(pgrep -x ceph-$c | head -1); cg=/sys/fs/cgroup$(awk -F: '$1 == "0" {print $3}' /proc/$p/cgroup)
echo "$c $(cat /proc/$p/oom_score_adj) $(cat $cg/memory.low)"; done | tr '\n' ' '; echo "slice $(cat /sys/fs/cgroup/system.slice/memory.low)"`
	wantOom := fmt.Sprintf("mon -900 %d mgr -900 %d osd -900 %d slice %d", 2048<<20, 1024<<20, 1536<<20, (2048+1024+1536)<<20)
	// The protection comes in a transient unit of its own after a unit started: wait for it
	waitOom := func(what string) {
		for i := 0; i < 60; i++ {
			if out, _ = wsl(0, oomCheck); strings.TrimSpace(out) == wantOom {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("OOM protection of the daemons %s: want %q, have %q", what, wantOom, out)
	}
	waitOom("after the deployment")
	// Still there after systemd applied the settings of the units again, and in the OSD started again after a kill
	out, _ = wsl(0, fmt.Sprintf(`systemctl daemon-reload; old=$(pgrep -x ceph-osd); kill -9 $old
for i in $(seq 1 90); do n=$(pgrep -x ceph-osd); [ -n "$n" ] && [ "$n" != "$old" ] && systemctl is-active -q ceph-%s@osd.0.service && break; sleep 1; done`, c.UUID))
	waitOom("after a daemon-reload and a killed OSD")
	for i := 0; i < 60; i++ {
		if out, _ = wsl(0, fmt.Sprintf(`ceph --conf /var/lib/ceph/%[1]s/config/ceph.conf --keyring /var/lib/ceph/%[1]s/config/ceph.client.admin.keyring osd stat -f json | jq .num_up_osds`, c.UUID)); strings.TrimSpace(out) == "1" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// The mgr metrics listen on the address of the host only (§14.3); the mons check the global_id of the clients
	// (CVE-2021-20288); the only OSD has nowhere to put its data, so it can not be removed (§8.5)
	admin := fmt.Sprintf("ceph --conf /var/lib/ceph/%[1]s/config/ceph.conf --keyring /var/lib/ceph/%[1]s/config/ceph.client.admin.keyring", c.UUID)
	out, _ = wsl(0, fmt.Sprintf(`for i in $(seq 1 60); do l=$(ss -ltnH | awk '$4 ~ /:9283$/ {print $4}' | sort -u | xargs); [ -n "$l" ] && break; sleep 2; done
echo "LISTEN $l"; echo "RECLAIM $(%s config get mon auth_allow_insecure_global_id_reclaim)"`, admin))
	if !strings.Contains(out, "LISTEN "+ip+":9283\n") || !strings.Contains(out, "RECLAIM false") {
		t.Fatalf("the mgr metrics (want %s:9283 only) and the global_id reclaim (want false): %s", ip, out)
	}
	out, _ = wsl(0, fmt.Sprintf(`cd /opt/cloudland/scripts/backend/storage && source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' ceph_cluster.sh)
fsid=%s; stc_fail() { echo "FAILED $*"; exit 1; }
(removal_room '[]' && echo "ROOM NONE"); removal_room '[0]'`, c.UUID))
	if !strings.Contains(out, "ROOM NONE") || !strings.Contains(out, "OSD would be left for the data") {
		t.Fatalf("room for the data of the OSDs removed: %s", out)
	}

	// A pool on the hdd OSDs
	pools := &services.StoragePoolAdmin{}
	pool, ptask, err := pools.CreateShared(ctx, &services.SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Media: "hdd"})
	must(t, err)
	if done := hw.waitTask(t, ptask.ID, 10*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed); done.Status != model.StorageTaskSucceeded {
		logRuns(ptask.ID)
		t.Fatalf("pool: %s", done.Message)
	}
	p := &model.StoragePool{}
	must(t, db.Take(p, pool.ID).Error)
	dp := map[string]interface{}{}
	must(t, json.Unmarshal([]byte(p.DriverParams), &dp))
	cephPool, _ := dp["ceph_pool"].(string)
	if p.Status != model.StoragePoolActive || p.CapacityBytes < 5<<30 || cephPool == "" {
		t.Fatalf("pool %+v", p)
	}
	t.Logf("pool %s: capacity %d, used %d", cephPool, p.CapacityBytes, p.UsedBytes)

	// A volume: made by the shared volume script on the host
	volumes := &services.VolumeAdmin{}
	vol, err := volumes.Create(ctx, name+"-v", 1, p)
	must(t, err)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		must(t, db.Take(vol, vol.ID).Error)
		if vol.Status != model.VolumeStatusPending || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if vol.Status != model.VolumeStatusAvailable || vol.Path != fmt.Sprintf("volume-%d", vol.ID) {
		t.Fatalf("volume %+v", vol)
	}

	// Live in a TCG domain: attached by the shared volume script with the disk the RBD driver writes, seen open by
	// the cluster, grown online, not deleted while open. The domain has no guest to answer a hot unplug, so the
	// volume is let go by destroying the domain
	args, _ := json.Marshal(map[string]interface{}{"driver": "ceph_rbd", "pool": p.UUID, "cluster": c.UUID, "conf": "/etc/ceph/" + c.UUID + ".conf",
		"user": "cloudland", "secret_uuid": c.UUID, "ceph_pool": cephPool, "quota_bytes": 0, "volume_id": vol.ID, "image": vol.Path,
		"size_gb": 2, "old_gb": 1, "instance_id": 990001})
	script := fmt.Sprintf(`source /opt/cloudland/scripts/cloudrc
dom=inst-990001; args='%[1]s'; rbd="rbd --conf /etc/ceph/%[2]s.conf --id cloudland"
virsh destroy $dom >/dev/null 2>&1; virsh undefine $dom >/dev/null 2>&1; rm -rf $xml_dir/$dom; mkdir -p $xml_dir/$dom
cat >$xml_dir/$dom/$dom.xml <<'XML'
<domain type='qemu'><name>inst-990001</name><memory unit='MiB'>96</memory><vcpu>1</vcpu>
<os><type arch='x86_64' machine='q35'>hvm</type></os><devices><emulator>/usr/bin/qemu-system-x86_64</emulator></devices></domain>
XML
virsh define $xml_dir/$dom/$dom.xml >/dev/null && virsh start $dom >/dev/null || { echo "DOMAIN FAILED"; exit 1; }
echo "ATTACH: $(/opt/cloudland/scripts/backend/attach_volume_shared.sh 990001 %[3]d x <<<"$args")"
echo "BLK: $(virsh domblklist $dom --details | awk '$1 == "network" {print $3, $4}')"
echo "WATCHERS: $($rbd status --format json %[4]s/%[5]s | jq '.watchers | length')"
echo "RESIZE: $(/opt/cloudland/scripts/backend/resize_volume_shared.sh %[3]d x <<<"$args")"
echo "CAPACITY: $(virsh domblkinfo $dom vdb | awk '/Capacity/ {print $2}') $($rbd info --format json %[4]s/%[5]s | jq .size)"
echo "DELETE OPEN: $(/opt/cloudland/scripts/backend/delete_volume_shared.sh %[3]d x <<<"$args")"
virsh destroy $dom >/dev/null 2>&1; virsh undefine $dom >/dev/null 2>&1; rm -rf $xml_dir/$dom; sleep 2
echo "AFTER: $($rbd status --format json %[4]s/%[5]s | jq '.watchers | length')"`, string(args), c.UUID, vol.ID, cephPool, vol.Path)
	out, err = wsl(0, script)
	t.Logf("libvirt:\n%s", out)
	want := []string{
		fmt.Sprintf("ATTACH: |:-COMMAND-:| attach_volume_shared '990001' '%d' 'vdb'", vol.ID),
		fmt.Sprintf("BLK: vdb %s/%s", cephPool, vol.Path),
		"WATCHERS: 1",
		fmt.Sprintf("RESIZE: |:-COMMAND-:| resize_volume '%d' 'success'", vol.ID),
		"CAPACITY: 2147483648 2147483648",
		fmt.Sprintf("DELETE OPEN: |:-COMMAND-:| clear_volume '%d' 'error' 'the volume is still open by", vol.ID),
		"AFTER: 0",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("libvirt check: want %q (%v)", w, err)
		}
	}

	// Removal: the volume, the pool, the cluster
	deferred, err := volumes.Delete(ctx, vol)
	must(t, err)
	deadline = time.Now().Add(2 * time.Minute)
	for deferred && db.Take(&model.Volume{}, vol.ID).Error == nil && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	if db.Take(&model.Volume{}, vol.ID).Error == nil {
		t.Fatalf("the volume is still there")
	}
	out, _ = wsl(0, fmt.Sprintf(`rbd --conf /etc/ceph/%s.conf --id cloudland ls -p %s | wc -l`, c.UUID, cephPool))
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("images left in the pool: %s", out)
	}

	// Boot disks (S4, §9.6-§9.8): two images copied into the pool by the import script in the background, its report
	// taken by clapi through the heartbeat; a boot disk cloned from one copy runs in a TCG domain, is captured through
	// librbd, swapped like a reinstall, keeps its copy from going, then goes; a full copy has no parent. The second copy
	// is left for the removal of the pool
	poolArgs := map[string]interface{}{"driver": "ceph_rbd", "pool": p.UUID, "cluster": c.UUID, "conf": "/etc/ceph/" + c.UUID + ".conf",
		"user": "cloudland", "secret_uuid": c.UUID, "ceph_pool": cephPool, "quota_bytes": 0}
	copies := []*model.ImageStorage{}
	hw.fake.asyncHost = host
	for i, base := range []string{"image-990077-ab12cd34", "image-990078-cd34ab12"} {
		now := time.Now()
		is := &model.ImageStorage{ImageID: int64(990077 + i), StoragePoolID: p.ID, Path: base, Status: model.ImageStorageSyncing, Hostid: host, SentAt: &now}
		must(t, db.Create(is).Error)
		copies = append(copies, is)
		in := map[string]interface{}{"image_storage_id": is.ID, "base": base, "image_name": base + ".qcow2", "image_url_b64": "", "clone_mode": "clone"}
		for k, v := range poolArgs {
			in[k] = v
		}
		b, _ := json.Marshal(in)
		out, err = wsl(host, fmt.Sprintf(`source /opt/cloudland/scripts/cloudrc; mkdir -p $image_cache; f=$image_cache/%[1]s.qcow2; rm -f $f
qemu-img create -q -f qcow2 $f 64M && qemu-io -f qcow2 -c "write -P 0xab 0 4M" $f >/dev/null
/opt/cloudland/scripts/backend/import_image_shared.sh %[2]d <<'JSON'
%[3]s
JSON`, base, is.ID, string(b)))
		if err != nil || strings.TrimSpace(out) != "" {
			t.Fatalf("import of %s: %v %s", base, err, out)
		}
	}
	deadline = time.Now().Add(3 * time.Minute)
	for _, is := range copies {
		for {
			must(t, db.Take(is, is.ID).Error)
			if is.Status != model.ImageStorageSyncing || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}
		if is.Status != model.ImageStorageSynced {
			t.Fatalf("copy %s: %+v", is.Path, is)
		}
	}
	pa, _ := json.Marshal(poolArgs)
	script = fmt.Sprintf(`source /opt/cloudland/scripts/cloudrc; cd /opt/cloudland/scripts/kvm; source ./storage_lib.sh
pj='%[1]s'; P=%[2]s; rbd="rbd --conf /etc/ceph/%[3]s.conf --id cloudland"; dom=inst-990002; A=%[4]d
echo "SNAP: $($rbd snap ls --format json $P/image-990077-ab12cd34 | jq -r '.[].name')"
bd=$(jq -c ". + {volume_id: 990012, image: \"volume-990012\", size_gb: 1, image_base: \"image-990077-ab12cd34\", image_storage_id: $A, clone_mode: \"clone\"}" <<<"$pj")
shared_boot_load "$bd" 990012 990002 || echo "LOAD FAILED $guard_error"
shared_boot_make volume-990012 1 && echo "MADE cloned=$drv_cloned" || echo "MAKE FAILED $guard_error"
echo "PARENT: $($rbd info --format json $P/volume-990012 | jq -r '.parent.image + "@" + .parent.snapshot') $($rbd info --format json $P/volume-990012 | jq .size)"
virsh destroy $dom >/dev/null 2>&1; virsh undefine $dom >/dev/null 2>&1; rm -rf $xml_dir/$dom; mkdir -p $xml_dir/$dom
{ echo "<domain type='qemu'><name>$dom</name><memory unit='MiB'>96</memory><vcpu>1</vcpu>"
  echo "<os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><devices><emulator>/usr/bin/qemu-system-x86_64</emulator>"
  drv_disk_xml volume-990012 vda; echo "</devices></domain>"; } >$xml_dir/$dom/$dom.xml
virsh define $xml_dir/$dom/$dom.xml >/dev/null && virsh start $dom >/dev/null && echo "STARTED" || echo "DOMAIN FAILED"
echo "BLK: $(virsh domblklist $dom --details | awk '$1 == "network" {print $3, $4}')"
echo "WATCHERS: $($rbd status --format json $P/volume-990012 | jq '.watchers | length')"
echo "CAPTURE: $(bash ./async_job/capture_image.sh 990991 cafe0001 990002 "rbd:$P/volume-990012:id=cloudland:conf=/etc/ceph/%[3]s.conf")"
qemu-io -f qcow2 -c 'read -P 0xab 0 4M' $image_cache/image-990991-cafe0001.qcow2 | grep -q 'read 4194304' && echo "CAPTURED DATA OK"
rm -f $image_cache/image-990991-cafe0001.qcow2
tmp=$(drv_temp_of volume-990012)
shared_boot_make "$tmp" 1 && echo "TEMP $tmp"
virsh destroy $dom >/dev/null 2>&1; sleep 1
drv_drop volume-990012 && drv_rename "$tmp" volume-990012 && echo "SWAPPED" || echo "SWAP FAILED $guard_error"
virsh start $dom >/dev/null && echo "RESTARTED $($rbd info --format json $P/volume-990012 | jq -r '.parent.image')"
echo "DELETE COPY: $(/opt/cloudland/scripts/backend/delete_image_shared.sh $A <<<"$(jq -c ". + {image_storage_id: $A, base: \"image-990077-ab12cd34\"}" <<<"$pj")")"
virsh destroy $dom >/dev/null 2>&1; virsh undefine $dom >/dev/null 2>&1; rm -rf $xml_dir/$dom; sleep 2
echo "DELETE BOOT: $(/opt/cloudland/scripts/backend/delete_volume_shared.sh 990012 x <<<"$bd")"
echo "DELETE COPY2: $(/opt/cloudland/scripts/backend/delete_image_shared.sh $A <<<"$(jq -c ". + {image_storage_id: $A, base: \"image-990077-ab12cd34\"}" <<<"$pj")")"
bd2=$(jq -c ". + {volume_id: 990013, image: \"volume-990013\", size_gb: 1, image_base: \"image-990078-cd34ab12\", image_storage_id: %[5]d, clone_mode: \"copy\"}" <<<"$pj")
shared_boot_load "$bd2" 990013 990003 && shared_boot_make volume-990013 1 && echo "COPIED cloned=$drv_cloned parent=$($rbd info --format json $P/volume-990013 | jq -r '.parent.image // "none"')"
drv_drop volume-990013
echo "LEFT: $($rbd ls -p $P | xargs)"`, string(pa), cephPool, c.UUID, copies[0].ID, copies[1].ID)
	out, err = wsl(host, script)
	t.Logf("boot disks:\n%s", out)
	want = []string{
		"SNAP: base",
		"MADE cloned=1",
		"PARENT: image-990077-ab12cd34@base 1073741824",
		"STARTED",
		fmt.Sprintf("BLK: vda %s/volume-990012", cephPool),
		"WATCHERS: 1",
		"CAPTURE: |:-COMMAND-:| capture_image.sh '990991' 'available' 'qcow2' '1073741824' ''",
		"CAPTURED DATA OK",
		"SWAPPED",
		"RESTARTED image-990077-ab12cd34",
		fmt.Sprintf("DELETE COPY: |:-COMMAND-:| image_storage_status '%d' 'delete_failed' '%s/image-990077-ab12cd34 still has clones", copies[0].ID, cephPool),
		"DELETE BOOT: |:-COMMAND-:| clear_volume '990012' 'deleted' '-'",
		fmt.Sprintf("DELETE COPY2: |:-COMMAND-:| image_storage_status '%d' 'deleted' '-'", copies[0].ID),
		"COPIED cloned=0 parent=none",
		"LEFT: image-990078-cd34ab12",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("boot disk check: want %q (%v)", w, err)
		}
	}

	// Evacuation (S6, §11.2-§11.3): a shut off instance of a host that is down, its boot disk and a data disk in the
	// pool. The host is blocklisted on the cluster first (the only admin host is this one), then the instance is
	// defined here from its record with the disks as they are, and not started. The host comes back and cleaned up
	// (simulated: it is this same WSL instance): the blocklist entry goes. The domain type of the templates is TCG for
	// this part, WSL has no KVM to define a domain with
	evacuateCheck(t, hw, host, c, p, cephPool)

	// Rotation (S6): the SSH key with the orchestrator switched between the passes, the client key made pending and
	// committed once no QEMU started before the new key is left
	rotateCheck(t, hw, c, cephPool)

	// Upgrade (S6) to the release of the distribution, the one the cluster runs here: nothing moves
	upgradeCheck(t, hw, c)

	// The pool goes with the copy left in it: the removal step drops the copies clapi lists
	must(t, db.Take(p, pool.ID).Error)
	dtask, err := pools.DeleteShared(ctx, p)
	must(t, err)
	if done := hw.waitTask(t, dtask.ID, 10*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed); done.Status != model.StorageTaskSucceeded {
		logRuns(dtask.ID)
		t.Fatalf("pool removal: %s", done.Message)
	}
	var copiesLeft int64
	must(t, db.Model(&model.ImageStorage{}).Where("storage_pool_id = ?", p.ID).Count(&copiesLeft).Error)
	if copiesLeft != 0 {
		t.Fatalf("%d image copies of the removed pool are left", copiesLeft)
	}
	if os.Getenv("CEPH_WSL_KEEP") != "" {
		t.Logf("the cluster %s (%s) is kept", name, c.UUID)
		return
	}
	del, err := services.StorageClusters.Delete(ctx, c.UUID, &services.StorageClusterDelete{ConfirmName: name})
	must(t, err)
	if done := hw.waitTask(t, del.ID, 20*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed); done.Status != model.StorageTaskSucceeded {
		logRuns(del.ID)
		t.Fatalf("deletion: %s", done.Message)
	}
	out, _ = wsl(0, fmt.Sprintf(`ls -d /var/lib/ceph/%[1]s 2>/dev/null | wc -l; docker ps -q | wc -l; vgs --noheadings -o vg_name 2>/dev/null | grep -c clceph-; ls /etc/ceph/%[1]s.conf 2>/dev/null | wc -l; virsh secret-list | grep -c %[1]s; grep -c cloudland-storage-%[1]s /root/.ssh/authorized_keys; ls -d /etc/systemd/system/ceph-%[1]s@.service.d 2>/dev/null | wc -l; cat /sys/fs/cgroup/system.slice/memory.low`, c.UUID))
	if strings.Join(strings.Fields(out), " ") != "0 0 0 0 0 0 0 0" {
		t.Fatalf("left after the deletion (cluster dir, containers, volume groups, client conf, secrets, trust lines, unit drop-ins, memory.low of system.slice): %s", out)
	}
}
