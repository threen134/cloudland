/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A host id is never given again (CLAPI_TEST_DB_URI, see placement_pg_test.go): the newest host deleted with no volume
// nor instance left its disk and pool rows soft deleted under the unique (hostid, ...) indexes, and the host registered
// next got the same id, so its built-in pool and every disk scan failed on a duplicate key (2026-10-10, work-04).

import (
	"testing"

	"api/src/model"
)

func TestAllocateHostIDSkipsDeletedHostPG(t *testing.T) {
	f := newPGFixture(t, 1)
	ctx, db := f.ctx, f.db
	admin := &HyperAdmin{}
	for _, c := range []struct {
		name string
		row  func(hostid int32) interface{}
	}{
		{"disk", func(hostid int32) interface{} {
			return &model.HyperDisk{Hostid: hostid, DiskID: "wwn-0xtest-hostid", Name: "sdb", State: "in_use"}
		}},
		{"pool", func(hostid int32) interface{} {
			return &model.HyperStoragePool{Hostid: hostid, PoolID: f.builtin.ID, Status: "ready"}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, err := admin.AllocateHostID(ctx)
			must(t, err)
			row := c.row(id)
			must(t, db.Create(row).Error)
			t.Cleanup(func() { db.Unscoped().Delete(row) })
			// what deleting the host does to it
			must(t, db.Delete(row).Error)
			next, err := admin.AllocateHostID(ctx)
			must(t, err)
			if next <= id {
				t.Fatalf("host id %d given again after a deleted host left its %s row (next %d)", id, c.name, next)
			}
		})
	}
}
