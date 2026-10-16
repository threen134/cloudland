/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// An instance paused because one of its local pools ran out of space only blocks new allocations in the pools
// short of room, not in the other pools it has disks in (PostgreSQL, CLAPI_TEST_DB_URI, see placement_pg_test.go)

import (
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestStorageFullPausedPoolsPG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	now := time.Now()
	const hostid = 9521
	db.Unscoped().Where("hostid = ?", hostid).Delete(&model.Hyper{})
	h := &model.Hyper{Hostid: hostid, Hostname: p2Name("sf"), Status: 1, CpuOverRate: 1, MemOverRate: 1, DiskOverRate: 1}
	must(t, db.Create(h).Error)
	t.Cleanup(func() { db.Unscoped().Delete(h) })
	newPool := func(prefix string, used, avail int64) (*model.StoragePool, *model.HyperStoragePool) {
		p := &model.StoragePool{Name: p2Name(prefix), Driver: "local", Status: model.StoragePoolActive, OverRatio: 3}
		must(t, db.Create(p).Error)
		hsp := &model.HyperStoragePool{Hostid: hostid, PoolID: p.ID, Status: model.HyperPoolReady,
			CapacityBytes: used + avail, UsedBytes: used, AvailBytes: avail, CapacityAt: &now}
		must(t, db.Create(hsp).Error)
		t.Cleanup(func() { db.Unscoped().Delete(hsp); db.Unscoped().Delete(p) })
		return p, hsp
	}
	// full: the pool the instance ran out of space in; roomy: another pool it has a disk in
	full, fullRow := newPool("sffull", 3*gib, 1<<20)
	roomy, roomyRow := newPool("sfroomy", 3*gib, 50*gib)
	inst := &model.Instance{Hostname: p2Name("sfvm"), Hyper: hostid, Status: model.InstanceStatusPaused, Reason: InstanceReasonStorageFull}
	must(t, db.Create(inst).Error)
	t.Cleanup(func() { db.Unscoped().Delete(inst) })
	for _, p := range []*model.StoragePool{full, roomy} {
		v := &model.Volume{Name: p2Name("sfv"), InstanceID: inst.ID, StoragePoolID: p.ID, Hyper: hostid, Size: 1}
		must(t, db.Create(v).Error)
		t.Cleanup(func() { db.Unscoped().Delete(v) })
	}

	if _, err := admit(db, full, hostid, 1, false); errCode(err) != ErrStorageCapacityExceeded || !strings.Contains(err.Error(), "ran out of space and 1 instances are paused") {
		t.Fatalf("full pool: got %v, want the paused refusal", err)
	}
	if _, err := admit(db, roomy, hostid, 1, false); err != nil {
		t.Fatalf("roomy pool: got %v, want admitted", err)
	}
	if n := storageFullPaused(db, fullRow); n != 1 {
		t.Fatalf("full pool counts %d paused instances, want 1", n)
	}
	if n := storageFullPaused(db, roomyRow); n != 0 {
		t.Fatalf("roomy pool counts %d paused instances, want 0", n)
	}

	// At the room the node resumes at it is still closed: the instance may not be resumed yet. With twice that
	// room it opens again too
	must(t, db.Model(fullRow).Updates(map[string]interface{}{"capacity_bytes": 10 * gib, "used_bytes": 8*gib + gib/2, "avail_bytes": gib + gib/2}).Error)
	must(t, db.Take(fullRow, fullRow.ID).Error)
	if _, err := admit(db, full, hostid, 1, false); errCode(err) != ErrStorageCapacityExceeded {
		t.Fatalf("full pool at the resume room: got %v, want the paused refusal", err)
	}
	must(t, db.Model(fullRow).Updates(map[string]interface{}{"used_bytes": 3 * gib, "avail_bytes": 7 * gib}).Error)
	must(t, db.Take(fullRow, fullRow.ID).Error)
	if _, err := admit(db, full, hostid, 1, false); err != nil {
		t.Fatalf("full pool after it grew: got %v, want admitted", err)
	}
	_ = ctx
}

func TestPoolShortOfRoom(t *testing.T) {
	for _, c := range []struct {
		used, avail int64
		want        bool
	}{
		{0, 0, false},                 // nothing reported yet
		{9 * gib, gib, true},          // 10% free of 10 GiB
		{85 * gib, 15 * gib, true},    // 15% free: the node resumes at it, still short
		{70 * gib, 30 * gib, false},   // 30% free: twice the resume room
		{900 * gib, 100 * gib, false}, // 10% free, twice 50 GiB
		{920 * gib, 80 * gib, true},   // 8% free and under 100 GiB
	} {
		hsp := &model.HyperStoragePool{UsedBytes: c.used, AvailBytes: c.avail}
		if got := poolShortOfRoom(hsp); got != c.want {
			t.Errorf("used %d avail %d: got %v, want %v", c.used, c.avail, got, c.want)
		}
	}
}
