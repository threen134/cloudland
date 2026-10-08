/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// What the review of S6 found (TC-24 regression points 16-30), against PostgreSQL with the fake cland: fences made
// once when asked twice at once, a failed fence undone (its command may have gone through) and retried with a pause,
// a fence whose task never started, the fences of a host removed from a cluster; the reconcile leaving an instance
// being created alone and not moving the time a host came back on its repeated asks; an evacuation refused while a
// reinstall waits for an image copy, NICs given back to the source when it fails, members of a strict spread group
// placed on different hosts.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"api/src/model"
)

func TestS6ReviewFencesPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	down := h[1]
	t.Cleanup(func() { db.Unscoped().Where("hostid IN ?", h).Delete(&model.StorageFence{}) })
	db.Unscoped().Where("hostid IN ?", h).Delete(&model.StorageFence{})
	f.cland.take()
	f.setHostStatus(down, int(model.HyperStatusOffline))
	live := func() []*model.StorageFence {
		fs := []*model.StorageFence{}
		must(t, db.Where("hostid = ?", down).Find(&fs).Error)
		return fs
	}

	// Asked five times at once: one fence, one task
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ensureStorageFence(ctx, f.cluster, down, ""); err != nil {
				t.Errorf("ensure: %v", err)
			}
		}()
	}
	wg.Wait()
	if fs := live(); len(fs) != 1 || fs[0].Status != model.StorageFenceFencing || fs[0].TaskID == 0 {
		t.Fatalf("fences after five asks at once: %+v", fs)
	}
	sent := f.expect("fence", "gpfs_cluster.sh", h[0])
	// The fence fails: failed. Fenced again, the old failed task does not fail the new one before its task is there
	must(t, f.report(sent[0], model.StorageRunFailed, "the file systems did not answer", ""))
	maintainStorageFences(ctx)
	fence := live()[0]
	if fence.Status != model.StorageFenceFailed {
		t.Fatalf("fence after its task failed: %+v", fence)
	}
	must(t, db.Model(&model.StorageFence{}).Where("id = ?", fence.ID).Update("task_id", fence.TaskID).Error)
	again, err := ensureStorageFence(ctx, f.cluster, down, "")
	must(t, err)
	maintainStorageFences(ctx)
	if fs := live(); len(fs) != 1 || fs[0].ID != fence.ID || fs[0].Status != model.StorageFenceFencing || fs[0].TaskID == fence.TaskID || again.TaskID == fence.TaskID {
		t.Fatalf("fenced again: %+v (task before %d)", fs, fence.TaskID)
	}
	must(t, f.report(f.expect("fence again", "gpfs_cluster.sh", h[0])[0], model.StorageRunSucceeded, "", `{"node":"stc-h2"}`))

	// A failed fence is undone once the host is back and settled, not deleted: its expel may have gone through
	back := func() {
		f.setHostStatus(down, 1)
		must(t, db.Model(&model.Hyper{}).Where("hostid = ?", down).Update("reconciled_at", time.Now().Add(-storageUnfenceSettle-time.Minute)).Error)
		must(t, db.Model(&model.StorageFence{}).Where("hostid = ?", down).Update("created_at", time.Now().Add(-time.Hour)).Error)
	}
	back()
	must(t, db.Model(&model.StorageFence{}).Where("id = ?", fence.ID).Updates(map[string]interface{}{"status": model.StorageFenceFailed}).Error)
	checkHostUnfence(ctx, down)
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("a fence that failed just now is undone at once: %v", cmds)
	}
	must(t, db.Model(&model.StorageFence{}).Where("id = ?", fence.ID).UpdateColumn("updated_at", time.Now().Add(-storageUnfenceRetry-time.Minute)).Error)
	checkHostUnfence(ctx, down)
	unfence := f.expect("undo a failed fence", "gpfs_cluster.sh", h[0])
	if unfence[0].input["action"] != "unfence" {
		t.Fatalf("undo input %v", unfence[0].input)
	}
	if fs := live(); len(fs) != 1 || fs[0].Status != model.StorageFenceUnfencing {
		t.Fatalf("failed fence deleted instead of undone: %+v", fs)
	}
	// The undo fails: tried again after a pause only
	must(t, f.report(unfence[0], model.StorageRunFailed, "mmexpelnode -l failed", ""))
	maintainStorageFences(ctx)
	if fs := live(); fs[0].Status != model.StorageFenceUnfenceFailed {
		t.Fatalf("after the undo failed: %+v", fs)
	}
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("retried at once: %v", cmds)
	}
	must(t, db.Model(&model.StorageFence{}).Where("id = ?", fence.ID).UpdateColumn("updated_at", time.Now().Add(-storageUnfenceRetry-time.Minute)).Error)
	maintainStorageFences(ctx)
	must(t, f.report(f.expect("undo again", "gpfs_cluster.sh", h[0])[0], model.StorageRunSucceeded, "", `{"node":"stc-h2"}`))
	if fs := live(); len(fs) != 0 {
		t.Fatalf("fence left after it was undone: %+v", fs)
	}

	// A fence whose task never started is given up after a while
	lost := &model.StorageFence{ClusterID: f.cluster.ID, Hostid: down, Status: model.StorageFenceFencing}
	must(t, db.Create(lost).Error)
	maintainStorageFences(ctx)
	if fs := live(); fs[0].Status != model.StorageFenceFencing {
		t.Fatalf("a fence just recorded is given up: %+v", fs)
	}
	must(t, db.Model(lost).UpdateColumn("updated_at", time.Now().Add(-storageFenceTaskLost-time.Minute)).Error)
	maintainStorageFences(ctx)
	if fs := live(); fs[0].Status != model.StorageFenceFailed || !strings.Contains(fs[0].Message, "never started") {
		t.Fatalf("a fence without its task: %+v", fs)
	}
	db.Unscoped().Where("hostid = ?", down).Delete(&model.StorageFence{})

	// A host removed from the cluster has its fences lifted, offline as it is
	f.setHostStatus(down, int(model.HyperStatusOffline))
	removed := &model.StorageFence{ClusterID: f.cluster.ID, Hostid: down, Status: model.StorageFenceFenced, Method: model.StorageFenceExpel,
		Target: "stc-h2", TaskID: 1}
	must(t, db.Create(removed).Error)
	liftRemovedHostFences(ctx, f.cluster.ID, down)
	must(t, f.report(f.expect("removed host", "gpfs_cluster.sh", h[0])[0], model.StorageRunSucceeded, "", `{"address":"10.93.0.2"}`))
	if fs := live(); len(fs) != 0 {
		t.Fatalf("the fence of a removed host stays: %+v", fs)
	}
	f.setHostStatus(down, 1)
}

func TestS6ReviewEvacuationPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("rv-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	image := &model.Image{Name: fmt.Sprintf("rv-img-%d", stamp), Format: "qcow2", Status: "available", BootLoader: "bios"}
	must(t, db.Create(image).Error)
	down := h[1]
	insts := []*model.Instance{}
	group := &model.PlacementGroup{Owner: 1, Name: fmt.Sprintf("rv-spread-%d", stamp), Policy: model.PlacementPolicySpread, Strict: true}
	must(t, db.Create(group).Error)
	for _, hh := range h {
		db.Where("hostid = ?", hh).Delete(&model.Resource{})
		must(t, db.Create(&model.Resource{Hostid: hh, Cpu: 16, CpuTotal: 16, Memory: 32 << 20, MemoryTotal: 32 << 20, Disk: 100 * gib, DiskTotal: 100 * gib}).Error)
	}
	t.Cleanup(func() {
		for _, inst := range insts {
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Migration{})
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Volume{})
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.ImageStorageWaiter{})
			db.Unscoped().Where("instance = ?", inst.ID).Delete(&model.Interface{})
			db.Unscoped().Delete(inst)
		}
		db.Unscoped().Where("hostid IN ?", h).Delete(&model.StorageFence{})
		db.Unscoped().Where("hostid IN ?", h).Delete(&model.Resource{})
		db.Unscoped().Delete(group)
		db.Unscoped().Delete(image)
	})
	newInstance := func(name string, status model.InstanceStatus, hyper int32, groupID int64) *model.Instance {
		inst := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, stamp), Status: status, Hyper: hyper, Owner: 1, ImageID: image.ID, Cpu: 1,
			Memory: 512, Disk: 10, PlacementGroupID: groupID}
		must(t, db.Create(inst).Error)
		insts = append(insts, inst)
		boot := &model.Volume{Name: inst.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: pool.ID, Size: 10,
			Status: model.VolumeStatusAttached, Target: "vda"}
		must(t, db.Create(boot).Error)
		must(t, db.Model(&model.Volume{}).Where("id = ?", boot.ID).Update("path", fmt.Sprintf("volumes/volume-%d.disk", boot.ID)).Error)
		return inst
	}

	// The reconcile: an instance being created (defined, its callback not come yet) is left alone, not removed
	creating := newInstance("rv-creating", model.InstanceStatusProvisioning, -1, 0)
	mine := newInstance("rv-mine", model.InstanceStatusProvisioning, down, 0)
	plan, err := planReconcile(db, down, []ReconcileDomain{{ID: creating.ID, State: "running"}, {ID: mine.ID, State: "running"}}, time.Now())
	must(t, err)
	if len(plan.Stale) != 0 || fmt.Sprint(plan.Leave) != fmt.Sprint([]int64{creating.ID}) || fmt.Sprint(plan.Start) != fmt.Sprint([]int64{mine.ID}) {
		t.Fatalf("reconcile of instances being created: %+v", plan)
	}
	// The asks repeated while some instances are left do not move the time the host came back
	before := time.Now().Add(-time.Hour).Truncate(time.Second)
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", down).Update("reconciled_at", before).Error)
	must(t, NodeReconciled(ctx, down, "b", "leave"))
	hyper := &model.Hyper{}
	must(t, db.Where("hostid = ?", down).Take(hyper).Error)
	if hyper.ReconciledAt == nil || !hyper.ReconciledAt.Equal(before) {
		t.Fatalf("a repeated ask moved reconciled_at to %v", hyper.ReconciledAt)
	}
	must(t, NodeReconciled(ctx, down, "b", "boot"))
	must(t, db.Where("hostid = ?", down).Take(hyper).Error)
	if hyper.ReconciledAt == nil || !hyper.ReconciledAt.After(before) {
		t.Fatalf("a boot reconcile did not move reconciled_at: %v", hyper.ReconciledAt)
	}
	must(t, db.Model(&model.Instance{}).Where("id IN ?", []int64{creating.ID, mine.ID}).Update("status", model.InstanceStatusDeleting).Error)

	// The host goes down with an instance whose reinstall waits for an image copy and two members of a strict spread
	// group
	waiting := newInstance("rv-waiting", model.InstanceStatusReinstalling, down, 0)
	must(t, db.Create(&model.ImageStorageWaiter{InstanceID: waiting.ID, Kind: "reinstall"}).Error)
	m1 := newInstance("rv-m1", model.InstanceStatusRunning, down, group.ID)
	m2 := newInstance("rv-m2", model.InstanceStatusRunning, down, group.ID)
	f.setHostStatus(down, int(model.HyperStatusOffline))
	MarkHyperOffline(ctx, down)
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", down).Update("offline_at", time.Now().Add(-10*time.Minute)).Error)
	f.cland.take()
	results, err := hyperAdmin.Evacuate(ctx, down, &EvacuateRequest{TargetHyper: -1, Instances: []int64{waiting.ID, m1.ID, m2.ID}})
	must(t, err)
	byID := map[int64]*EvacuateResult{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	if r := byID[waiting.ID]; r == nil || r.Status != migrationStatusNotDoing || !strings.Contains(r.Reason, "waits for its image") {
		t.Fatalf("instance whose reinstall waits: %+v", r)
	}
	must(t, f.report(f.expect("fence", "gpfs_cluster.sh", h[0])[0], model.StorageRunSucceeded, "", `{"node":"stc-h2"}`))
	advanceEvacuations(ctx)
	launches := f.cland.take()
	if len(launches) != 2 {
		t.Fatalf("launches of the two members: %v", launches)
	}
	targets := map[int32]bool{}
	for _, inst := range []*model.Instance{m1, m2} {
		m := &model.Migration{}
		must(t, db.Where("instance_id = ? AND type = ?", inst.ID, model.MigrationTypeEvacuate).Take(m).Error)
		if m.Status != migrationStatusInProgress || m.TargetHyper < 0 || m.TargetHyper == down {
			t.Fatalf("evacuation of member %s: %+v", inst.Hostname, m)
		}
		targets[m.TargetHyper] = true
	}
	if len(targets) != 2 {
		t.Fatalf("members of a strict spread group sent to one host: %v", targets)
	}
	for _, c := range launches {
		if !strings.HasPrefix(c, "select=") || strings.Count(strings.SplitN(c, " ", 2)[0], ",") != 0 {
			t.Fatalf("a member is not sent to the one host chosen: %s", c)
		}
	}

	// The launch of m1 got as far as its NICs on the target, then failed: the NICs are the source's again
	m := &model.Migration{}
	must(t, db.Where("instance_id = ? AND type = ?", m1.ID, model.MigrationTypeEvacuate).Take(m).Error)
	iface := &model.Interface{Instance: m1.ID, Hyper: m.TargetHyper, MacAddr: fmt.Sprintf("52:54:00:%02x:%02x:01", stamp%256, (stamp/256)%256)}
	must(t, db.Create(iface).Error)
	if !FailEvacuationOf(ctx, m1.ID, "starting it on the target failed") {
		t.Fatal("no evacuation to fail")
	}
	must(t, db.Take(iface, iface.ID).Error)
	got := &model.Instance{}
	must(t, db.Take(got, m1.ID).Error)
	if iface.Hyper != down || got.Hyper != down || got.Status != model.InstanceStatusRunning {
		t.Fatalf("after the launch failed: interface on %d, instance %+v", iface.Hyper, got)
	}
}
