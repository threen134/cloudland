/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A storage cluster deleted while one of its hosts is offline (shared-storage-design.md §7.6, §8.6), against PostgreSQL with
// the fake cland: the deletion goes on without the host and records the leave it missed; the host joins no cluster
// meanwhile; once it is online the leave runs on it, again a while later when it fails, and the record goes when it
// succeeded. A disk claimed again since is not wiped; the record goes with the host.

import (
	"strings"
	"testing"
	"time"

	"api/src/model"
)

func TestStoragePendingCleanupPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	t.Cleanup(func() { db.Unscoped().Where("hostid IN ?", stcHosts).Delete(&model.StoragePendingCleanup{}) })
	for _, k := range []string{"gpfs:", "ceph:"} {
		if !storageTaskKinds[k+StorageTaskDeleteCluster].Steps["leave"].OnlineOnly {
			t.Fatalf("the leave of %sdelete_cluster runs on every host", k)
		}
	}
	succeed := func(sent []*stcSent) {
		for _, s := range sent {
			must(t, f.report(s, "succeeded", "", "{}"))
		}
	}
	cluster := f.cluster
	f.setHostStatus(h[2], 10)

	del, err := StorageClusters.Delete(ctx, cluster.UUID, &StorageClusterDelete{ConfirmName: cluster.Name})
	must(t, err)
	sent := f.expect("teardown", "gpfs_cluster.sh", h[0])
	if off, _ := sent[0].input["offline"].([]interface{}); len(off) != 1 || off[0] != "10.93.0.3" {
		t.Fatalf("teardown input names the members offline: %v", sent[0].input)
	}
	succeed(sent)
	// The leave runs on the hosts online only
	succeed(f.expect("leave", "stc_leave.sh", h[0], h[1]))
	f.wantTask(del.ID, "succeeded", "")
	if db.Take(&model.StorageCluster{}, cluster.ID).Error == nil {
		t.Fatalf("the cluster is still there")
	}
	rows := []*model.StoragePendingCleanup{}
	must(t, db.Where("hostid IN ?", stcHosts).Find(&rows).Error)
	if len(rows) != 1 || rows[0].Hostid != h[2] || rows[0].Step != "leave" || rows[0].ClusterUUID != cluster.UUID ||
		!strings.Contains(rows[0].Input, `"wipe":[{"id":"wwn-shared-2"`) || !strings.Contains(rows[0].Input, cluster.UUID) {
		t.Fatalf("pending cleanups %+v", rows)
	}
	row := rows[0]

	// The host joins no storage cluster before its cleanup ran
	gpfs, _ := storageBackendOf(model.StorageKindGPFS)
	why, err := storageHostConflict(db, gpfs, model.StorageKindGPFS, 0, h[2], []string{"nsd"})
	must(t, err)
	if !strings.Contains(why, "still has to clean up storage cluster "+cluster.Name) {
		t.Fatalf("conflict of a host with a pending cleanup: %q", why)
	}
	list, err := HostStorageCleanups(ctx, h[2])
	must(t, err)
	if len(list) != 1 || list[0].Cluster != cluster.Name || list[0].Attempts != 0 || list[0].TaskID != "" {
		t.Fatalf("the cleanups of the host %+v", list)
	}

	// Offline: nothing runs
	maintainStorageCleanups(ctx)
	if s := f.sent(); len(s) != 0 {
		t.Fatalf("a cleanup ran on a host offline: %v", s[0].script)
	}
	// Back: the leave runs, with what was recorded
	f.setHostStatus(h[2], 1)
	maintainStorageCleanups(ctx)
	sent = f.expect("cleanup", "stc_leave.sh", h[2])
	if sent[0].input["cluster_uuid"] != cluster.UUID || len(sent[0].input["wipe"].([]interface{})) != 1 {
		t.Fatalf("cleanup input %v", sent[0].input)
	}
	must(t, db.Take(row, row.ID).Error)
	cleanupTask := f.task(row.TaskID)
	if row.Attempts != 1 || row.TriedAt == nil || cleanupTask.Kind != StorageTaskCleanup || cleanupTask.ClusterID != 0 {
		t.Fatalf("cleanup row %+v task %+v", row, cleanupTask)
	}
	// It fails: recorded and aborted, tried again only later
	must(t, f.report(sent[0], "failed", "wiping wwn-shared-2 failed", ""))
	f.wantTask(row.TaskID, "failed", "")
	maintainStorageCleanups(ctx)
	f.wantTask(row.TaskID, "aborted", "")
	must(t, db.Take(row, row.ID).Error)
	if !strings.Contains(row.Message, "wiping wwn-shared-2 failed") {
		t.Fatalf("the failure is not recorded: %+v", row)
	}
	maintainStorageCleanups(ctx)
	if s := f.sent(); len(s) != 0 {
		t.Fatalf("a failed cleanup ran again at once")
	}
	if storageCleanupDelay(1) != 10*time.Minute || storageCleanupDelay(3) != 40*time.Minute || storageCleanupDelay(30) != storageCleanupRetryMax {
		t.Fatalf("retry delays %v %v %v", storageCleanupDelay(1), storageCleanupDelay(3), storageCleanupDelay(30))
	}
	// A while later it runs again; the disk claimed by another cluster since is left out of the wipe
	claim := &model.StorageClusterDisk{ClusterID: cluster.ID + 100000, Hostid: h[2], DiskID: "wwn-shared-2", Role: "nsd", Status: model.StorageDiskActive}
	must(t, db.Create(claim).Error)
	defer db.Unscoped().Delete(claim)
	must(t, db.Model(&model.StoragePendingCleanup{}).Where("id = ?", row.ID).UpdateColumn("tried_at", time.Now().Add(-11*time.Minute)).Error)
	maintainStorageCleanups(ctx)
	sent = f.expect("cleanup again", "stc_leave.sh", h[2])
	if len(sent[0].input["wipe"].([]interface{})) != 0 {
		t.Fatalf("a disk claimed again is to be wiped: %v", sent[0].input)
	}
	succeed(sent)
	must(t, db.Unscoped().Take(row, row.ID).Error)
	if !row.DeletedAt.Valid {
		t.Fatalf("the cleanup that succeeded is still recorded: %+v", row)
	}
	if why, _ = storageHostConflict(db, gpfs, model.StorageKindGPFS, 0, h[2], []string{"nsd"}); why != "" {
		t.Fatalf("conflict after the cleanup: %q", why)
	}

	// The record goes with the host
	other := &model.StoragePendingCleanup{Hostid: h[2], Kind: model.StorageKindCeph, ClusterUUID: "aaaaaaaa-0000-0000-0000-000000000001",
		ClusterName: "gone", Step: "leave", Input: "{}"}
	must(t, db.Create(other).Error)
	must(t, hyperAdmin.releaseStorage(db, h[2], false))
	if db.Take(&model.StoragePendingCleanup{}, other.ID).Error == nil {
		t.Fatalf("the cleanup of a deleted host is still recorded")
	}
}
