/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Shared pools against PostgreSQL with the fake cland standing in for the hosts (shared-storage-design.md §7.4, §9):
// making a GPFS pool on a cluster, the availability reports of the hosts, data volumes in the pool (create, attach,
// resize, detach, delete), the pool level admission, the plan of a migration with a shared disk, the quota and the
// removal of the pool. The node scripts are covered by the WSL tests and on real hosts.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

var sharedCmdRe = regexp.MustCompile(`(?s)^inter=(-?\d+) \| /opt/cloudland/scripts/backend/(\w+\.sh)( [^\n]*?)?(?: <<'EOF'\n(.*)\nEOF)?$`)

type sharedCmd struct {
	hostid int32
	script string
	args   string
	input  map[string]interface{}
	raw    string
}

// sharedFixture is a ready GPFS cluster of three hosts, recorded as a deployment leaves it
type sharedFixture struct {
	*stcFixture
	cluster *model.StorageCluster
	fs      *model.StorageFilesystem
	hosts   []int32
}

func newSharedFixture(t *testing.T) *sharedFixture {
	f := &sharedFixture{stcFixture: newStcFixture(t), hosts: stcHosts[:3]}
	db := f.db
	f.cluster = &model.StorageCluster{Name: fmt.Sprintf("shared-pg-%d", time.Now().UnixNano()%1000000), Kind: model.StorageKindGPFS,
		Mode: model.StorageModeManaged, Layout: model.StorageLayoutReplica, Status: model.StorageClusterReady, Health: model.StorageHealthUnknown}
	must(t, db.Create(f.cluster).Error)
	for i, h := range f.hosts {
		roles := "quorum,nsd"
		if i == 0 {
			roles = "admin,quorum,nsd"
		}
		must(t, db.Create(&model.StorageClusterNode{ClusterID: f.cluster.ID, Hostid: h, Roles: roles, Status: model.StorageNodeActive,
			Attrs: fmt.Sprintf(`{"failure_group":%d}`, i+1)}).Error)
	}
	f.fs = &model.StorageFilesystem{ClusterID: f.cluster.ID, Name: "fs1", MountPoint: "/gpfs/fs1", BlockSize: "4M", DataReplicas: 2, MetaReplicas: 3, Status: "ready"}
	must(t, db.Create(f.fs).Error)
	for i, h := range f.hosts {
		must(t, db.Create(&model.StorageClusterDisk{ClusterID: f.cluster.ID, Hostid: h, DiskID: fmt.Sprintf("wwn-shared-%d", i), Role: "nsd",
			Media: "hdd", FsID: f.fs.ID, Status: model.StorageDiskActive, Attrs: `{"usage":"dataAndMetadata","gpfs_pool":"system"}`}).Error)
	}
	t.Cleanup(func() {
		pools := []*model.StoragePool{}
		db.Where("cluster_id = ?", f.cluster.ID).Find(&pools)
		for _, p := range pools {
			db.Unscoped().Where("storage_pool_id = ?", p.ID).Delete(&model.Volume{})
			db.Unscoped().Where("pool_id = ?", p.ID).Delete(&model.HyperStoragePool{})
			db.Unscoped().Delete(p)
		}
		db.Unscoped().Where("cluster_id = ?", f.cluster.ID).Delete(&model.StorageFilesystem{})
		db.Unscoped().Where("cluster_id = ?", f.cluster.ID).Delete(&model.StorageClusterDisk{})
		db.Unscoped().Where("cluster_id = ?", f.cluster.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Delete(f.cluster)
		if builtin, err := BuiltinPool(f.ctx); err == nil {
			storagePoolAdmin.setDefault(db, builtin)
		}
	})
	return f
}

// volumeCmds takes the commands sent since the last call that are not storage task commands
func (f *sharedFixture) volumeCmds() (out []*sharedCmd) {
	f.t.Helper()
	for _, rec := range f.cland.take() {
		m := sharedCmdRe.FindStringSubmatch(rec)
		if m == nil {
			continue
		}
		// Disk scans come from the end of tasks, a few seconds later, also of earlier tests
		if m[2] == "scan_host_disks.sh" {
			continue
		}
		hostid, _ := strconv.Atoi(m[1])
		c := &sharedCmd{hostid: int32(hostid), script: m[2], args: strings.TrimSpace(m[3]), raw: rec}
		// The input of a volume script is an object; a pool list is an array, kept in raw only
		if strings.HasPrefix(m[4], "{") {
			if err := json.Unmarshal([]byte(m[4]), &c.input); err != nil {
				f.t.Fatalf("bad input of %s: %v", rec, err)
			}
		}
		out = append(out, c)
	}
	return
}

func (f *sharedFixture) oneCmd(what, script string) *sharedCmd {
	f.t.Helper()
	cmds := []*sharedCmd{}
	for _, c := range f.volumeCmds() {
		// Pool lists also come from the rounds of the background worker, the first one 20 seconds after the test
		// binary started, whenever that falls
		if c.script == "sync_shared_pools.sh" && script != c.script {
			continue
		}
		cmds = append(cmds, c)
	}
	if len(cmds) != 1 || cmds[0].script != script {
		got := []string{}
		for _, c := range cmds {
			got = append(got, c.raw)
		}
		f.t.Fatalf("%s: want one %s, got %v", what, script, got)
	}
	return cmds[0]
}

func (f *sharedFixture) pool(id int64) *model.StoragePool {
	f.t.Helper()
	p := &model.StoragePool{}
	must(f.t, f.db.Take(p, id).Error)
	return p
}

func (f *sharedFixture) row(hostid int32, poolID int64) *model.HyperStoragePool {
	f.t.Helper()
	r := &model.HyperStoragePool{}
	must(f.t, f.db.Where("hostid = ? AND pool_id = ?", hostid, poolID).Take(r).Error)
	return r
}

// createPool makes a pool through its whole task: the admin host makes the fileset, every online host takes the
// list and checks it; check gives what each host found
func (f *sharedFixture) createPool(name string, quotaGB int64, check func(hostid int32, pool string) string) (*model.StoragePool, *model.StorageTask) {
	f.t.Helper()
	pool, task, err := storagePoolAdmin.CreateShared(f.ctx, &SharedPoolCreate{Name: name, ClusterUUID: f.cluster.UUID, QuotaGB: quotaGB})
	must(f.t, err)
	sent := f.expect("create_pool "+name, "gpfs_pool.sh", f.hosts[0])
	must(f.t, f.report(sent[0], model.StorageRunSucceeded, "", `{"policy_installed":false}`))
	online := []int32{}
	for _, h := range f.hosts {
		if _, ok := hostOnline(f.db, h); ok {
			online = append(online, h)
		}
	}
	for _, s := range f.expect("sync_pools "+name, "stc_pools.sh", online...) {
		must(f.t, f.report(s, model.StorageRunSucceeded, "", check(s.hostid, pool.UUID)))
	}
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	return f.pool(pool.ID), task
}

func readyCheck(pool string, size, used int64) string {
	return fmt.Sprintf(`{"pools":1,"check":{"pool":%q,"status":"ready","reason":"","size":%d,"used":%d,"avail":%d}}`, pool, size, used, size-used)
}

func TestSharedPoolPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts

	// Requests that never start a task
	_, _, err := storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: "sp-bad", ClusterUUID: f.cluster.UUID, Params: json.RawMessage(`{"fileset":"x"}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "unknown parameter")
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: "sp-bad", ClusterUUID: f.cluster.UUID, Media: "ssd"})
	wantCode(t, err, ErrStorageInvalidPlan, "no ssd disks")
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: "sp-bad", ClusterUUID: f.cluster.UUID, Params: json.RawMessage(`{"filesystem":"fs9"}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "no such file system")
	must(t, db.Model(f.cluster).Update("status", model.StorageClusterDeploying).Error)
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: "sp-bad", ClusterUUID: f.cluster.UUID})
	wantCode(t, err, ErrStorageClusterBusy, "cluster not ready")
	must(t, db.Model(f.cluster).Update("status", model.StorageClusterReady).Error)
	if f.db.Where("name = ?", "sp-bad").Take(&model.StoragePool{}).Error == nil {
		t.Fatalf("a refused request left a pool behind")
	}

	// Making the pool: the third host is offline and is skipped by the list step
	name := fmt.Sprintf("sp-%d", time.Now().UnixNano()%1000000)
	pool, task, err := storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name, ClusterUUID: f.cluster.UUID, QuotaGB: 100, OverRatio: 1.5,
		IsDefault: true, Params: json.RawMessage(`{"inode_limit":50000}`)})
	must(t, err)
	fileset := "cl_" + pool.UUID[:8]
	p := f.pool(pool.ID)
	if p.Status != model.StoragePoolCreating || p.Driver != "gpfs" || p.MountPath != "/gpfs/fs1/"+fileset || p.FilesystemID != f.fs.ID ||
		p.QuotaBytes != 100*gib || p.IsDefault || !strings.Contains(p.DriverParams, `"gpfs_pool":"system"`) || task.Kind != StorageTaskCreatePool {
		t.Fatalf("pool being made %+v task %+v", p, task)
	}
	c := &model.StorageCluster{}
	must(t, db.Take(c, f.cluster.ID).Error)
	if c.ActivePoolTask != task.ID || c.ActiveTask != 0 {
		t.Fatalf("pool slot %d structural slot %d", c.ActivePoolTask, c.ActiveTask)
	}
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "x", ClusterUUID: f.cluster.UUID})
	wantCode(t, err, ErrStorageClusterBusy, "pool slot held")
	_, err = volumeAdmin.Create(ctx, "v-too-early", 1, p)
	wantCode(t, err, ErrStoragePoolUnavailable, "volume in a pool being made")
	sent := f.expect("create_pool", "gpfs_pool.sh", h[0])
	in := sent[0].input
	if in["action"] != "create" || in["fileset"] != fileset || in["junction"] != "/gpfs/fs1/"+fileset || in["fs_name"] != "fs1" ||
		in["quota_bytes"] != float64(100*gib) || in["inode_limit"] != float64(50000) || in["multi_pool"] != false ||
		len(in["rules"].([]interface{})) != 1 || in["pool_uuid"] != pool.UUID {
		t.Fatalf("create_pool input %v", in)
	}
	f.setHostStatus(h[2], 10)
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"policy_installed":false}`))
	sent = f.expect("sync_pools", "stc_pools.sh", h[0], h[1])
	pools := sent[0].input["pools"].([]interface{})
	entry := pools[0].(map[string]interface{})
	if sent[0].input["check"] != pool.UUID || len(pools) != 1 || entry["root"] != "/gpfs/fs1/"+fileset || entry["fs_type"] != "gpfs" ||
		entry["driver"] != "gpfs" || entry["pool"] != pool.UUID {
		t.Fatalf("sync_pools input %v", sent[0].input)
	}
	for _, s := range sent {
		result := readyCheck(pool.UUID, 100*gib, 10*gib)
		if s.hostid == h[1] {
			result = fmt.Sprintf(`{"pools":1,"check":{"pool":%q,"status":"unavailable","reason":"not mounted"}}`, pool.UUID)
		}
		must(t, f.report(s, model.StorageRunSucceeded, "", result))
	}
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	p = f.pool(pool.ID)
	if p.Status != model.StoragePoolActive || !p.IsDefault || p.CapacityBytes != 100*gib || p.UsedBytes != 10*gib || p.CapacityAt == nil {
		t.Fatalf("pool made %+v", p)
	}
	if r := f.row(h[0], p.ID); r.Status != model.HyperPoolReady || r.CapacityBytes != 100*gib {
		t.Fatalf("row of the admin host %+v", r)
	}
	if r := f.row(h[1], p.ID); r.Status != model.HyperPoolUnavailable || r.Reason != "not mounted" {
		t.Fatalf("row of the host that can not reach it %+v", r)
	}
	if r := f.row(h[2], p.ID); r.Status != model.HyperPoolUnavailable || !strings.Contains(r.Reason, "offline") {
		t.Fatalf("row of the offline host %+v", r)
	}
	must(t, db.Take(c, f.cluster.ID).Error)
	if c.ActivePoolTask != 0 {
		t.Fatalf("the pool slot is still held")
	}
	f.setHostStatus(h[2], 1)

	// Heartbeat reports: a member is taken, a host outside the cluster is not
	must(t, HandleSharedPoolStatus(ctx, h[1], []*SharedPoolReport{{Pool: pool.UUID, Status: "ready", Size: 100 * gib, Used: 12 * gib, Avail: 88 * gib}}))
	must(t, HandleSharedPoolStatus(ctx, stcHosts[3], []*SharedPoolReport{{Pool: pool.UUID, Status: "ready", Size: 1, Used: 1}}))
	if r := f.row(h[1], p.ID); r.Status != model.HyperPoolReady || r.Reason != "" {
		t.Fatalf("row after the report %+v", r)
	}
	if db.Where("hostid = ? AND pool_id = ?", stcHosts[3], p.ID).Take(&model.HyperStoragePool{}).Error == nil {
		t.Fatalf("a host outside the cluster got a row")
	}
	if p = f.pool(pool.ID); p.UsedBytes != 12*gib {
		t.Fatalf("pool capacity after the report %+v", p)
	}
	// A shared default pool is for data volumes: a boot disk that names no pool goes to the builtin pool (boot disks
	// in shared pools come with S4); named outright, the shared pool comes back for the creation to refuse. Done in
	// a transaction that is rolled back, the test database is shared with other packages
	{
		txCtx, tx, _ := StartTransaction(ctx)
		must(t, tx.Model(&model.StoragePool{}).Where("is_default = ?", true).Update("is_default", false).Error)
		must(t, tx.Model(&model.StoragePool{}).Where("id = ?", p.ID).Update("is_default", true).Error)
		if dp, err := storagePoolAdmin.Resolve(txCtx, nil); err != nil || dp.ID != p.ID {
			t.Fatalf("default pool %+v %v", dp, err)
		}
		if bp, err := storagePoolAdmin.ResolveBoot(txCtx, nil); err != nil || !bp.Builtin {
			t.Fatalf("boot pool with a shared default %+v %v", bp, err)
		}
		if bp, err := storagePoolAdmin.ResolveBoot(txCtx, &BaseReference{ID: p.UUID}); err != nil || bp.ID != p.ID {
			t.Fatalf("boot pool named outright %+v %v", bp, err)
		}
		EndTransaction(txCtx, fmt.Errorf("rolled back on purpose"))
	}
	// Host operations of local pools do not apply
	hyper := &model.Hyper{}
	must(t, db.Where("hostid = ?", h[0]).Take(hyper).Error)
	wantCode(t, HyperStorage.SetMaintenance(ctx, hyper, p, true), ErrStoragePoolInvalidState, "maintenance of a shared pool")
	wantCode(t, storagePoolAdmin.Delete(ctx, p), ErrStoragePoolInvalidState, "local removal of a shared pool")
	media := "ssd"
	wantCode(t, storagePoolAdmin.Update(ctx, p, &StoragePoolUpdate{Media: &media}), ErrInvalidParameter, "media of a shared pool")
	// The cluster does not go while it has a pool: checked under the lock of the cluster row, nothing changes
	_, err = StorageClusters.Delete(ctx, f.cluster.UUID, &StorageClusterDelete{ConfirmName: f.cluster.Name})
	wantCode(t, err, ErrStoragePoolInUse, "deleting a cluster with a pool")
	if c := (&model.StorageCluster{}); db.Take(c, f.cluster.ID).Error != nil || c.Status != model.StorageClusterReady || c.ActiveTask != 0 {
		t.Fatalf("a refused deletion changed the cluster: %+v", c)
	}

	// Volumes: created by a host that reaches the pool right away
	vol, err := volumeAdmin.Create(ctx, "sv-1", 10, p)
	must(t, err)
	cmd := f.oneCmd("create volume", "create_volume_shared.sh")
	if vol.Status != model.VolumeStatusPending || vol.Hyper != 0 || vol.Path != fmt.Sprintf("volumes/volume-%d.disk", vol.ID) ||
		(cmd.hostid != h[0] && cmd.hostid != h[1]) || cmd.args != fmt.Sprintf("'%d' '%s'", vol.ID, vol.UUID) ||
		cmd.input["path"] != fmt.Sprintf("/gpfs/fs1/%s/volumes/volume-%d.disk", fileset, vol.ID) || cmd.input["size_gb"] != float64(10) ||
		cmd.input["driver"] != "gpfs" || cmd.input["fs_type"] != "gpfs" || cmd.input["root"] != "/gpfs/fs1/"+fileset {
		t.Fatalf("volume %+v command %+v", vol, cmd)
	}
	// Only a host of the pool reports the creation
	if err := HandleSharedVolumeCreated(ctx, stcHosts[3], vol.ID, "available", "-"); err == nil {
		t.Fatal("a creation reported by a host outside the pool was taken")
	}
	must(t, db.Take(vol, vol.ID).Error)
	if vol.Status != model.VolumeStatusPending {
		t.Fatalf("a report from outside the pool changed the volume: %+v", vol)
	}
	must(t, HandleSharedVolumeCreated(ctx, cmd.hostid, vol.ID, "available", "-"))
	must(t, db.Take(vol, vol.ID).Error)
	if vol.Status != model.VolumeStatusAvailable || vol.Hyper != 0 {
		t.Fatalf("volume created %+v", vol)
	}
	// The pool list sums what the volumes of shared pools are promised and names their cluster, for a page at once
	if s := HyperStorage.Summaries(ctx, []*model.StoragePool{p})[p.ID]; s.AllocatedBytes != 10*gib || s.Cluster == nil || s.Cluster.ID != f.cluster.ID {
		t.Fatalf("pool summary %+v", s)
	}
	// The cluster list and detail show what the volumes of its pools are promised
	if s := (&StorageClusterAdmin{}).Summaries(ctx, []int64{f.cluster.ID})[f.cluster.ID]; s.AllocatedBytes != 10*gib {
		t.Fatalf("cluster summary %+v", s)
	}
	if a := (&StorageClusterAdmin{}).PoolAllocations(ctx, f.cluster.ID); a[p.ID] != 10*gib || len(a) != 1 {
		t.Fatalf("pool allocations %v", a)
	}
	// Pool level admission: 100 GB x 1.5 = 150 GB; 10 GB allocated
	_, err = volumeAdmin.Create(ctx, "sv-big", 141, p)
	wantCode(t, err, ErrStorageCapacityExceeded, "over the capacity of the pool")
	big, err := volumeAdmin.Create(ctx, "sv-2", 140, p)
	must(t, err)
	f.oneCmd("create second volume", "create_volume_shared.sh")
	must(t, HandleSharedVolumeCreated(ctx, h[0], big.ID, "error", "qemu-img create failed"))
	must(t, db.Take(big, big.ID).Error)
	if big.Status != model.VolumeStatusError || !strings.Contains(big.Reason, "qemu-img") {
		t.Fatalf("failed creation %+v", big)
	}
	_, err = volumeAdmin.Create(ctx, "sv-3", 1, p)
	wantCode(t, err, ErrStorageCapacityExceeded, "the failed volume still counts")
	must(t, db.Model(&model.StoragePool{}).Where("id = ?", p.ID).Update("used_bytes", 95*gib).Error)
	_, err = volumeAdmin.Create(ctx, "sv-3", 1, f.pool(p.ID))
	wantCode(t, err, ErrStorageCapacityExceeded, "pool 95% used")
	must(t, db.Model(&model.StoragePool{}).Where("id = ?", p.ID).Update("used_bytes", 12*gib).Error)
	p = f.pool(p.ID)
	// A failed volume is removed through a host all the same: the file may be there
	deferred, err := volumeAdmin.Delete(ctx, big)
	must(t, err)
	cmd = f.oneCmd("delete failed volume", "delete_volume_shared.sh")
	if !deferred || cmd.input["path"] != fmt.Sprintf("/gpfs/fs1/%s/volumes/volume-%d.disk", fileset, big.ID) {
		t.Fatalf("delete of the failed volume %v %+v", deferred, cmd)
	}
	must(t, HandleClearVolume(ctx, cmd.hostid, big.ID, "deleted", ""))
	if db.Take(&model.Volume{}, big.ID).Error == nil {
		t.Fatalf("the failed volume is still there")
	}

	// Attach to an instance on a host that reaches the pool, and not on one that does not
	inst := &model.Instance{Hostname: "sp-inst", Hyper: h[0], Status: model.InstanceStatusRunning, Owner: 1}
	must(t, db.Create(inst).Error)
	defer db.Unscoped().Delete(inst)
	far := &model.Instance{Hostname: "sp-far", Hyper: h[2], Status: model.InstanceStatusRunning, Owner: 1}
	must(t, db.Create(far).Error)
	defer db.Unscoped().Delete(far)
	must(t, db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", h[2], p.ID).Update("reason", "not mounted").Error)
	farID := far.ID
	_, err = volumeAdmin.Update(ctx, vol.ID, "", &farID)
	wantCode(t, err, ErrStoragePoolUnavailable, "attach where the pool is unavailable")
	instID := inst.ID
	_, err = volumeAdmin.Update(ctx, vol.ID, "", &instID)
	must(t, err)
	cmd = f.oneCmd("attach", "attach_volume_shared.sh")
	if cmd.hostid != h[0] || cmd.args != fmt.Sprintf("'%d' '%d' '%s'", inst.ID, vol.ID, vol.UUID) || cmd.input["volume_id"] != float64(vol.ID) {
		t.Fatalf("attach command %+v", cmd)
	}
	// Only the host of the instance reports, and only for an instance on it
	if err := HandleSharedVolumeAttached(ctx, h[1], inst.ID, vol.ID, "vdb", ""); err == nil {
		t.Fatal("an attach reported by another host was taken")
	}
	if err := HandleSharedVolumeAttached(ctx, h[0], far.ID, vol.ID, "vdb", ""); err == nil {
		t.Fatal("an attach to an instance on another host was taken")
	}
	must(t, db.Take(vol, vol.ID).Error)
	if vol.Status != model.VolumeStatusAttaching || vol.InstanceID != 0 {
		t.Fatalf("a refused report changed the volume: %+v", vol)
	}
	must(t, HandleSharedVolumeAttached(ctx, h[0], inst.ID, vol.ID, "vdb", ""))
	must(t, db.Take(vol, vol.ID).Error)
	if vol.Status != model.VolumeStatusAttached || vol.Target != "vdb" || vol.InstanceID != inst.ID || vol.Hyper != 0 {
		t.Fatalf("attached volume %+v", vol)
	}
	// An instance deleted while its volume was attaching: the volume is attached to nothing
	gone := &model.Instance{Hostname: "sp-gone", Hyper: h[0], Status: model.InstanceStatusRunning, Owner: 1}
	must(t, db.Create(gone).Error)
	spare, err := volumeAdmin.Create(ctx, "sv-spare", 1, p)
	must(t, err)
	f.oneCmd("create spare volume", "create_volume_shared.sh")
	must(t, HandleSharedVolumeCreated(ctx, h[0], spare.ID, "available", "-"))
	goneID := gone.ID
	_, err = volumeAdmin.Update(ctx, spare.ID, "", &goneID)
	must(t, err)
	f.oneCmd("attach spare", "attach_volume_shared.sh")
	must(t, db.Delete(gone).Error)
	must(t, HandleSharedVolumeAttached(ctx, h[0], gone.ID, spare.ID, "vdc", ""))
	must(t, db.Take(spare, spare.ID).Error)
	if spare.Status != model.VolumeStatusAvailable || spare.InstanceID != 0 || spare.Reason != "the instance is gone" {
		t.Fatalf("volume of a deleted instance %+v", spare)
	}
	db.Unscoped().Delete(spare)
	db.Unscoped().Delete(gone)

	// Resize of an attached volume: on the host of its instance, admitted against the pool
	wantCode(t, volumeAdmin.Resize(ctx, vol, 200), ErrStorageCapacityExceeded, "resize over the capacity")
	must(t, volumeAdmin.Resize(ctx, vol, 20))
	cmd = f.oneCmd("resize", "resize_volume_shared.sh")
	if cmd.hostid != h[0] || cmd.input["size_gb"] != float64(20) || cmd.input["old_gb"] != float64(10) || cmd.input["instance_id"] != float64(inst.ID) {
		t.Fatalf("resize command %+v", cmd)
	}
	must(t, db.Take(vol, vol.ID).Error)
	if vol.Status != model.VolumeStatusResizing || vol.Size != 20 {
		t.Fatalf("volume being resized %+v", vol)
	}
	must(t, db.Model(&model.Volume{}).Where("id = ?", vol.ID).Update("status", model.VolumeStatusAttached).Error)

	// Migration with the shared data disk and a local boot disk: the shared disk stays, the boot disk is copied
	builtin, err := BuiltinPool(ctx)
	must(t, err)
	boot := &model.Volume{Name: "sp-boot", Size: 5, Booting: true, Status: model.VolumeStatusAttached, InstanceID: inst.ID, Target: "vda",
		Hyper: h[0], StoragePoolID: builtin.ID, Path: fmt.Sprintf("instance/inst-%d.disk", inst.ID), Owner: 1, Format: "qcow2"}
	must(t, db.Create(boot).Error)
	defer db.Unscoped().Delete(boot)
	items, err := planDisks(db, ctx, inst, h[1], &MigrationOptions{})
	must(t, err)
	if len(items) != 2 || items[0].Shared || !items[1].Shared || items[1].DstPath != items[1].SrcPath ||
		items[1].SrcPath != fmt.Sprintf("/gpfs/fs1/%s/volumes/volume-%d.disk", fileset, vol.ID) || items[1].DstPoolID != p.ID {
		t.Fatalf("plan %+v %+v", items[0], items[1])
	}
	_, err = planDisks(db, ctx, inst, h[2], &MigrationOptions{})
	wantCode(t, err, ErrStoragePoolUnavailable, "target that does not reach the shared pool")
	_, err = planDisks(db, ctx, inst, h[1], &MigrationOptions{Disks: map[int64]int64{vol.ID: builtin.ID}})
	wantCode(t, err, ErrStoragePoolUnavailable, "a shared disk asked to move")
	mig := &model.Migration{Name: "sp-mig", Status: "in_progress", SourceHyper: h[0], TargetHyper: h[1]}
	must(t, db.Create(mig).Error)
	defer db.Unscoped().Delete(mig)
	must(t, admitPlan(db, mig, items, h[1], false))
	res := []*model.StorageReservation{}
	db.Where("migration_id = ?", mig.ID).Find(&res)
	if len(res) != 1 || res[0].VolumeID != boot.ID {
		t.Fatalf("reservations %+v", res)
	}
	plan, _ := json.Marshal(items)
	mig.DiskPlan = string(plan)
	must(t, ApplyMigrationPlan(ctx, mig, inst.ID, h[1]))
	must(t, db.Take(vol, vol.ID).Error)
	must(t, db.Take(boot, boot.ID).Error)
	if vol.Hyper != 0 || vol.StoragePoolID != p.ID || boot.Hyper != h[1] {
		t.Fatalf("after the migration: shared %+v boot %+v", vol, boot)
	}
	mig.DiskPlan = ""
	must(t, ApplyMigrationPlan(ctx, mig, inst.ID, h[0]))
	must(t, db.Take(vol, vol.ID).Error)
	if vol.Hyper != 0 {
		t.Fatalf("a migration without a plan gave the shared volume a host: %+v", vol)
	}
	targets, err := MigrationTargets(ctx, inst)
	must(t, err)
	for _, tg := range targets {
		for _, d := range tg.Disks {
			// Only the hosts of the cluster that reach the pool: h[1]; not h[2], nor any host outside the cluster
			if d.VolumeUUID == vol.UUID && (!d.Shared || d.CanStay != (tg.Hostid == h[1])) {
				t.Fatalf("target %d disk %+v", tg.Hostid, d)
			}
		}
	}
	db.Unscoped().Delete(boot)

	// Detach and delete: detached on the host of the instance like a local volume, deleted by any host of the pool
	zero := int64(0)
	_, err = volumeAdmin.Update(ctx, vol.ID, "", &zero)
	must(t, err)
	if cmd = f.oneCmd("detach", "detach_volume_local.sh"); cmd.hostid != h[0] {
		t.Fatalf("detach command %+v", cmd)
	}
	must(t, db.Model(&model.Volume{}).Where("id = ?", vol.ID).Updates(map[string]interface{}{"status": model.VolumeStatusAvailable, "instance_id": 0, "target": ""}).Error)

	// The quota is set on the cluster by a task
	qtask, err := storagePoolAdmin.UpdateSharedQuota(ctx, p, 200)
	must(t, err)
	sent = f.expect("update_pool", "gpfs_pool.sh", h[0])
	if sent[0].input["action"] != "quota" || sent[0].input["quota_bytes"] != float64(200*gib) {
		t.Fatalf("update_pool input %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	f.wantTask(qtask.ID, model.StorageTaskSucceeded, "")
	if p = f.pool(p.ID); p.QuotaBytes != 200*gib {
		t.Fatalf("quota after the task %+v", p)
	}

	// The periodic pool lists go to the online hosts of the ready clusters
	SyncSharedPools(ctx)
	got := map[int32]bool{}
	for _, c := range f.volumeCmds() {
		if c.script == "sync_shared_pools.sh" && strings.Contains(c.raw, "'"+f.cluster.UUID+"'") {
			got[c.hostid] = true
			if !strings.Contains(c.raw, pool.UUID) {
				t.Fatalf("pool list without the pool: %s", c.raw)
			}
		}
	}
	if len(got) != 3 {
		t.Fatalf("pool lists sent to %v", got)
	}

	// Removing the pool: refused while it holds a volume; the hosts drop it from their lists before the fileset goes
	_, err = storagePoolAdmin.DeleteShared(ctx, p)
	wantCode(t, err, ErrStoragePoolInUse, "pool with a volume")
	deferred, err = volumeAdmin.Delete(ctx, vol)
	must(t, err)
	cmd = f.oneCmd("delete volume", "delete_volume_shared.sh")
	must(t, HandleClearVolume(ctx, cmd.hostid, vol.ID, "deleted", ""))
	dtask, err := storagePoolAdmin.DeleteShared(ctx, f.pool(p.ID))
	must(t, err)
	if p = f.pool(p.ID); p.Status != model.StoragePoolDeleting || p.IsDefault {
		t.Fatalf("pool being removed %+v", p)
	}
	sent = f.expect("sync_pools before the removal", "stc_pools.sh", h...)
	if len(sent[0].input["pools"].([]interface{})) != 0 || sent[0].input["check"] != "" {
		t.Fatalf("list of the removal %v", sent[0].input)
	}
	for _, s := range sent {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"pools":0}`))
	}
	sent = f.expect("delete_pool", "gpfs_pool.sh", h[0])
	if sent[0].input["action"] != "delete" || sent[0].input["fileset"] != fileset || len(sent[0].input["rules"].([]interface{})) != 0 {
		t.Fatalf("delete_pool input %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"policy_installed":false}`))
	f.wantTask(dtask.ID, model.StorageTaskSucceeded, "")
	var rows int64
	db.Unscoped().Model(&model.HyperStoragePool{}).Where("pool_id = ?", p.ID).Count(&rows)
	if db.Unscoped().Take(&model.StoragePool{}, p.ID).Error == nil || rows != 0 {
		t.Fatalf("the pool or its rows are still there (%d rows)", rows)
	}
	if b, _ := BuiltinPool(ctx); !f.pool(b.ID).IsDefault {
		t.Fatalf("the built-in pool is not the default again")
	}
}

// A pool whose making fails stays creating while its task waits; aborted, it is in error and can be removed
func TestSharedPoolAbortPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, h := f.ctx, f.hosts
	pool, task, err := storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: fmt.Sprintf("spa-%d", time.Now().UnixNano()%1000000), ClusterUUID: f.cluster.UUID})
	must(t, err)
	sent := f.expect("create_pool", "gpfs_pool.sh", h[0])
	must(t, f.report(sent[0], model.StorageRunFailed, "mmcrfileset failed", ""))
	f.wantTask(task.ID, model.StorageTaskFailed, "mmcrfileset failed")
	if p := f.pool(pool.ID); p.Status != model.StoragePoolCreating {
		t.Fatalf("pool of a failed task %+v", p)
	}
	_, err = storagePoolAdmin.DeleteShared(ctx, f.pool(pool.ID))
	wantCode(t, err, ErrStoragePoolInvalidState, "removing a pool being made")
	must(t, AbortStorageTask(ctx, task.ID))
	f.wantTask(task.ID, model.StorageTaskAborted, "")
	if p := f.pool(pool.ID); p.Status != model.StoragePoolError {
		t.Fatalf("pool of an aborted task %+v", p)
	}
	dtask, err := storagePoolAdmin.DeleteShared(ctx, f.pool(pool.ID))
	must(t, err)
	for _, s := range f.expect("sync_pools", "stc_pools.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"pools":0}`))
	}
	must(t, f.report(f.expect("delete_pool", "gpfs_pool.sh", h[0])[0], model.StorageRunSucceeded, "", "{}"))
	f.wantTask(dtask.ID, model.StorageTaskSucceeded, "")
	if f.db.Unscoped().Take(&model.StoragePool{}, pool.ID).Error == nil {
		t.Fatalf("the pool in error was not removed")
	}
	// A host stops reporting: its row goes unavailable; a volume whose creation was never confirmed goes to error
	p, _ := f.createPool(fmt.Sprintf("spb-%d", time.Now().UnixNano()%1000000), 0, func(hostid int32, pool string) string {
		return readyCheck(pool, 10*gib, 0)
	})
	if p.Status != model.StoragePoolActive || f.row(h[2], p.ID).Status != model.HyperPoolReady {
		t.Fatalf("pool made %+v", p)
	}
	vol, err := volumeAdmin.Create(ctx, "spb-v", 1, p)
	must(t, err)
	f.oneCmd("create volume", "create_volume_shared.sh")
	old := time.Now().Add(-time.Hour)
	must(t, f.db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", h[2], p.ID).UpdateColumn("checked_at", old).Error)
	must(t, f.db.Model(&model.Volume{}).Where("id = ?", vol.ID).UpdateColumn("updated_at", old).Error)
	maintainSharedPools(ctx, time.Now())
	if r := f.row(h[2], p.ID); r.Status != model.HyperPoolUnavailable || r.Reason != model.ReasonNodeOffline {
		t.Fatalf("row of a host that stopped reporting %+v", r)
	}
	if r := f.row(h[1], p.ID); r.Status != model.HyperPoolReady {
		t.Fatalf("row of a host that reports %+v", r)
	}
	must(t, f.db.Take(vol, vol.ID).Error)
	if vol.Status != model.VolumeStatusError {
		t.Fatalf("volume whose creation was never confirmed %+v", vol)
	}
}
