/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The row a host that joins a cluster gets for each pool of the cluster (CLAPI_TEST_DB_URI, see placement_pg_test.go):
// the pool lists reach the joining host before the add task finishes, so its first report may come first; the finish
// kept overwriting it with "waiting for its first report" and the host, seeing nothing change, reported again only
// 5 minutes later (2026-10-10, work-04 joining gpfs1)

import (
	"fmt"
	"testing"
	"time"

	"api/src/model"
)

func TestJoinedSharedPoolRowKeepsReportPG(t *testing.T) {
	f := newSharedFixture(t)
	db := f.db
	pool := &model.StoragePool{Name: fmt.Sprintf("joined-pg-%d", time.Now().UnixNano()%1000000), Driver: model.StorageDriverGPFS,
		ClusterID: f.cluster.ID, Status: model.StoragePoolActive}
	must(t, db.Create(pool).Error)
	reported, fresh, never := f.hosts[0], f.hosts[1], f.hosts[2]
	row := func(hostid int32) *model.HyperStoragePool {
		r := &model.HyperStoragePool{}
		must(t, db.Where("hostid = ? AND pool_id = ?", hostid, pool.ID).Take(r).Error)
		return r
	}
	now := time.Now()
	must(t, HandleSharedPoolStatus(f.ctx, reported, []*SharedPoolReport{{Pool: pool.UUID, Status: model.HyperPoolReady, Size: 1 << 30}}))
	// a host that left once: its old row is soft deleted
	must(t, upsertSharedPoolRow(db, fresh, pool, &SharedPoolReport{Pool: pool.UUID, Status: model.HyperPoolReady}, now))
	must(t, db.Where("hostid = ? AND pool_id = ?", fresh, pool.ID).Delete(&model.HyperStoragePool{}).Error)

	for _, h := range []int32{reported, fresh, never} {
		must(t, joinedSharedPoolRow(db, h, pool, now))
	}
	if r := row(reported); r.Status != model.HyperPoolReady {
		t.Fatalf("the report made before the task finished was overwritten: %s %q", r.Status, r.Reason)
	}
	for _, h := range []int32{fresh, never} {
		if r := row(h); r.Status != model.HyperPoolUnavailable || r.Reason != "joined the cluster: waiting for its first report" {
			t.Fatalf("host %d with no live row got %s %q", h, r.Status, r.Reason)
		}
	}
	// A report after the finish takes the row, and a finish running again leaves it
	must(t, HandleSharedPoolStatus(f.ctx, never, []*SharedPoolReport{{Pool: pool.UUID, Status: model.HyperPoolReady, Size: 1 << 30}}))
	must(t, joinedSharedPoolRow(db, never, pool, now))
	if r := row(never); r.Status != model.HyperPoolReady {
		t.Fatalf("a finish run again overwrote the report: %s %q", r.Status, r.Reason)
	}
}
