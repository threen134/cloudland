/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Auto join limited to the hosts registered after the setting (shared-storage-design.md §6.3), against PostgreSQL: the
// hosts of the zones registered before are left alone, a host registered after is queued, turning it off queues all.

import (
	"fmt"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestStorageAutoJoinNewOnlyPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db := f.ctx, f.db
	h4 := stcHosts[3]
	zone := &model.Zone{Name: fmt.Sprintf("ajn-%d", time.Now().UnixNano()%1000000)}
	must(t, db.Create(zone).Error)
	hyper := &model.Hyper{}
	must(t, db.Where("hostid = ?", h4).Take(hyper).Error)
	created := hyper.CreatedAt
	t.Cleanup(func() {
		db.Model(&model.Hyper{}).Where("hostid = ?", h4).Update("created_at", created)
		db.Model(&model.StorageCluster{}).Where("id = ?", f.cluster.ID).Updates(map[string]interface{}{"active_task": 0,
			"auto_join_zones": "", "auto_join_since": nil, "pending_clients": ""})
		db.Unscoped().Delete(zone)
	})
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", h4).Updates(map[string]interface{}{"zone_id": zone.ID,
		"created_at": time.Now().Add(-time.Hour)}).Error)
	load := func() *model.StorageCluster {
		c := &model.StorageCluster{}
		must(t, db.Take(c, f.cluster.ID).Error)
		return c
	}
	// The slot is taken: the rounds only queue, nothing starts
	must(t, db.Model(&model.StorageCluster{}).Where("id = ?", f.cluster.ID).Update("active_task", 999999999).Error)
	yes, no := true, false

	// New only needs zones
	_, err := StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinNewOnly: &yes})
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "zones")
	zones := []string{zone.UUID}
	c, err := StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinZones: &zones, AutoJoinNewOnly: &yes})
	must(t, err)
	if c.AutoJoinSince == nil || time.Since(*c.AutoJoinSince) > time.Minute {
		t.Fatalf("since %v", c.AutoJoinSince)
	}
	since := *c.AutoJoinSince

	// The host registered an hour ago is left alone
	maintainAutoJoin(ctx, load())
	if p := ParseStoragePendingClients(load().PendingClients); len(p) != 0 {
		t.Fatalf("an older host was queued: %+v", p)
	}
	// Turned on again: the time stays
	c, err = StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinNewOnly: &yes})
	must(t, err)
	if c.AutoJoinSince == nil || !c.AutoJoinSince.Equal(since) {
		t.Fatalf("since moved: %v, was %v", c.AutoJoinSince, since)
	}
	// A host registered after the setting is queued
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", h4).Update("created_at", since.Add(time.Second)).Error)
	maintainAutoJoin(ctx, load())
	if p := ParseStoragePendingClients(load().PendingClients); len(p) != 1 || p[0].Hostid != h4 || p[0].Status != StoragePendingWaiting {
		t.Fatalf("new host not queued: %+v", p)
	}
	// Back to an older registration: the waiting host leaves the queue
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", h4).Update("created_at", time.Now().Add(-time.Hour)).Error)
	maintainAutoJoin(ctx, load())
	if p := ParseStoragePendingClients(load().PendingClients); len(p) != 0 {
		t.Fatalf("an older host still waits: %+v", p)
	}
	// Turned off: every host of the zones joins
	c, err = StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinNewOnly: &no})
	must(t, err)
	if c.AutoJoinSince != nil {
		t.Fatalf("since kept: %v", c.AutoJoinSince)
	}
	maintainAutoJoin(ctx, load())
	if p := ParseStoragePendingClients(load().PendingClients); len(p) != 1 || p[0].Hostid != h4 {
		t.Fatalf("host not queued with new only off: %+v", p)
	}
	// No zone: new only goes too
	empty := []string{}
	_, err = StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinNewOnly: &yes})
	must(t, err)
	c, err = StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinZones: &empty})
	must(t, err)
	if c.AutoJoinSince != nil || c.PendingClients != "" {
		t.Fatalf("after turning auto join off: since %v pending %q", c.AutoJoinSince, c.PendingClients)
	}
}
