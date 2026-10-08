/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A forced migration (POST /migrations with force) evacuates single instances of a host that is down, against
// PostgreSQL with the fake cland (shared-storage-design.md §11.3): no target pools, all or nothing for one call,
// the grace period and the confirmation of the evacuation, batch going on past a refused instance.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

// wantCodeMsg is wantCode that also checks the message
func wantCodeMsg(t *testing.T, err error, code ErrCode, msg string) {
	t.Helper()
	wantCode(t, err, code, msg)
	if !strings.Contains(err.Error(), msg) {
		t.Fatalf("want an error with %q, got %v", msg, err)
	}
}

func TestForcedMigrationPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("fm-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	image := &model.Image{Name: fmt.Sprintf("fm-img-%d", stamp), Format: "qcow2", Status: "available", BootLoader: "bios"}
	must(t, db.Create(image).Error)
	down := h[1]
	insts := []*model.Instance{}
	t.Cleanup(func() {
		for _, inst := range insts {
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Migration{})
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Volume{})
			db.Unscoped().Delete(inst)
		}
		db.Unscoped().Where("hostid IN ?", h).Delete(&model.StorageFence{})
		db.Unscoped().Delete(image)
	})
	newInstance := func(name string, shared bool) *model.Instance {
		inst := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, stamp), Status: model.InstanceStatusRunning, Hyper: down, Owner: 1,
			ImageID: image.ID, Cpu: 1, Memory: 512, Disk: 10, NestedEnable: true}
		must(t, db.Create(inst).Error)
		insts = append(insts, inst)
		poolID := int64(0)
		if shared {
			poolID = pool.ID
		}
		boot := &model.Volume{Name: inst.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: poolID, Size: 10,
			Status: model.VolumeStatusAttached, Target: "vda"}
		must(t, db.Create(boot).Error)
		must(t, db.Model(&model.Volume{}).Where("id = ?", boot.ID).Update("path", fmt.Sprintf("volumes/volume-%d.disk", boot.ID)).Error)
		return inst
	}
	shared := newInstance("fm-shared", true)
	other := newInstance("fm-other", true)
	local := newInstance("fm-local", false)
	f.cland.take()
	noMigration := func(inst *model.Instance) {
		t.Helper()
		var n int64
		db.Model(&model.Migration{}).Where("instance_id = ?", inst.ID).Count(&n)
		if n != 0 {
			t.Fatalf("instance %s got a migration", inst.Hostname)
		}
	}

	// The source host is online: a forced migration is refused, the instance is migrated the usual way
	_, _, err := migrationAdmin.Create(ctx, "fm", []*model.Instance{shared}, true, -1, nil, false)
	wantCodeMsg(t, err, ErrEvacuationRefused, "not offline")
	MarkHyperOffline(ctx, down)
	f.setHostStatus(down, int(model.HyperStatusOffline))

	// Offline but within the grace period, unless confirmed
	_, _, err = migrationAdmin.Create(ctx, "fm", []*model.Instance{shared}, true, -1, nil, false)
	wantCodeMsg(t, err, ErrEvacuationRefused, "offline for")
	// Target pools of disks make no sense: the disks stay where they are
	_, _, err = migrationAdmin.Create(ctx, "fm", []*model.Instance{shared}, true, h[0], &MigrationOptions{Disks: map[int64]int64{1: pool.ID}}, false)
	wantCodeMsg(t, err, ErrInvalidParameter, "stay in their shared pools")

	// Instances of two hosts in one call: refused before anything is fenced (all or nothing holds within one host;
	// across two the first would be fenced and on its way before the second is refused)
	elsewhere := newInstance("fm-elsewhere", true)
	must(t, db.Model(&model.Instance{}).Where("id = ?", elsewhere.ID).Update("hyper", h[2]).Error)
	elsewhere.Hyper = h[2]
	_, _, err = migrationAdmin.Create(ctx, "fm", []*model.Instance{shared, elsewhere}, true, -1, &MigrationOptions{ConfirmFenced: true}, false)
	wantCodeMsg(t, err, ErrInvalidParameter, "one offline host")
	noMigration(shared)
	noMigration(elsewhere)
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("fenced although the call was refused: %v", cmds)
	}

	// One call with an instance on local storage: none starts, nothing is fenced
	_, _, err = migrationAdmin.Create(ctx, "fm", []*model.Instance{shared, local}, true, -1, &MigrationOptions{ConfirmFenced: true}, false)
	wantCodeMsg(t, err, ErrEvacuationRefused, "local storage pool")
	noMigration(shared)
	noMigration(local)
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("fenced although the call was refused: %v", cmds)
	}

	// Confirmed: the single instance is evacuated; the other one of the host stays where it is
	migs, results, err := migrationAdmin.Create(ctx, "fm", []*model.Instance{shared}, true, -1, &MigrationOptions{ConfirmFenced: true}, false)
	must(t, err)
	if len(migs) != 1 || len(results) != 1 || migs[0].Type != model.MigrationTypeEvacuate || migs[0].Status != migrationStatusFencing ||
		migs[0].InstanceID != shared.ID || !migs[0].Force || migs[0].SourceHyper != down {
		t.Fatalf("forced migration %+v results %+v", migs, results)
	}
	noMigration(other)
	fence := f.expect("fence", "gpfs_cluster.sh", h[0])
	if fence[0].input["action"] != "fence" {
		t.Fatalf("fence input %v", fence[0].input)
	}

	// Batch: the instance already moving and the local one are refused, the other one still goes
	migs, results, err = migrationAdmin.Create(ctx, "fm", []*model.Instance{shared, other, local}, true, -1, &MigrationOptions{ConfirmFenced: true}, true)
	must(t, err)
	if len(migs) != 1 || migs[0].InstanceID != other.ID || len(results) != 3 {
		t.Fatalf("batch forced migration %+v results %+v", migs, results)
	}
	for _, r := range results {
		switch r.Instance.ID {
		case shared.ID:
			if r.Error == nil || !strings.Contains(r.Error.Error(), "being migrated") {
				t.Fatalf("instance being evacuated: %+v", r)
			}
		case local.ID:
			if r.Error == nil || !strings.Contains(r.Error.Error(), "local storage pool") {
				t.Fatalf("local instance: %+v", r)
			}
		case other.ID:
			if r.Error != nil || r.Migration == nil {
				t.Fatalf("other instance: %+v", r)
			}
		}
	}
}
