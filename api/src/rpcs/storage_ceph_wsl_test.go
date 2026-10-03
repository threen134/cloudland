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
	must(t, db.Take(p, pool.ID).Error)
	dtask, err := pools.DeleteShared(ctx, p)
	must(t, err)
	if done := hw.waitTask(t, dtask.ID, 10*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed); done.Status != model.StorageTaskSucceeded {
		logRuns(dtask.ID)
		t.Fatalf("pool removal: %s", done.Message)
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
	out, _ = wsl(0, fmt.Sprintf(`ls -d /var/lib/ceph/%[1]s 2>/dev/null | wc -l; docker ps -q | wc -l; vgs --noheadings -o vg_name 2>/dev/null | grep -c clceph-; ls /etc/ceph/%[1]s.conf 2>/dev/null | wc -l; virsh secret-list | grep -c %[1]s; grep -c cloudland-storage-%[1]s /root/.ssh/authorized_keys; ls -d /etc/systemd/system/ceph-%[1]s@.service.d 2>/dev/null | wc -l`, c.UUID))
	if strings.Join(strings.Fields(out), " ") != "0 0 0 0 0 0 0" {
		t.Fatalf("left after the deletion (cluster dir, containers, volume groups, client conf, secrets, trust lines, unit drop-ins): %s", out)
	}
}
