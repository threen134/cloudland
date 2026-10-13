/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// What a host leaving a cluster is still sent (CLAPI_TEST_DB_URI, see placement_pg_test.go): no pool list, from the
// list step of a pool task or the periodic round, once its removal started. A list that arrived after the leave
// brought the directory of the cluster back on the host (2026-10-10, work-04 leaving gpfs1)

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"api/src/model"
)

func TestSharedPoolListsSkipLeavingPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	must(t, db.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h[2]).
		Update("status", model.StorageNodeLeaving).Error)

	name := fmt.Sprintf("sp-leave-%d", time.Now().UnixNano()%1000000)
	pool, task, err := storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name, ClusterUUID: f.cluster.UUID, QuotaGB: 10})
	must(t, err)
	sent := f.expect("create_pool", "gpfs_pool.sh", h[0])
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"policy_installed":false}`))
	sent = f.expect("sync_pools to the hosts that stay", "stc_pools.sh", h[0], h[1])
	for _, s := range sent {
		must(t, f.report(s, model.StorageRunSucceeded, "", readyCheck(pool.UUID, 10*gib, 0)))
	}
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")

	SyncSharedPools(ctx)
	got := map[int32]bool{}
	for _, c := range f.volumeCmds() {
		if c.script == "sync_shared_pools.sh" && strings.Contains(c.raw, "'"+f.cluster.UUID+"'") {
			got[c.hostid] = true
		}
	}
	if len(got) != 2 || !got[h[0]] || !got[h[1]] {
		t.Fatalf("periodic pool lists sent to %v, not to the leaving host %d", got, h[2])
	}
}

// A retry of a step that skips offline hosts skips the hosts that went offline since, instead of sending them the
// step again: a deletion whose leave failed on a host that then went offline finishes, the host's cleanup recorded
func TestStorageRetrySkipsOfflinePG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	task, err := StorageClusters.Delete(ctx, f.cluster.UUID, &StorageClusterDelete{ConfirmName: f.cluster.Name})
	must(t, err)
	sent := f.expect("teardown", "gpfs_cluster.sh", h[0])
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"removed":true}`))
	sent = f.expect("leave", "stc_leave.sh", h...)
	for _, s := range sent {
		if s.hostid == h[2] {
			must(t, f.report(s, model.StorageRunFailed, "wiping failed", ""))
		} else {
			must(t, f.report(s, model.StorageRunSucceeded, "", `{"wiped":1}`))
		}
	}
	f.wantTask(task.ID, model.StorageTaskFailed, "wiping failed")
	f.setHostStatus(h[2], 10)
	must(t, RetryStorageTask(ctx, task.ID))
	f.expect("nothing sent to the offline host", "")
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	runs := []*model.StorageTaskRun{}
	must(t, db.Where("step_id IN (SELECT id FROM storage_task_steps WHERE task_id = ? AND name = ?) AND hostid = ?", task.ID, "leave", h[2]).
		Order("attempt").Find(&runs).Error)
	if len(runs) != 2 || runs[1].Status != model.StorageRunSkipped {
		t.Fatalf("runs of the leave on the host that went offline %+v", runs)
	}
	var n int64
	db.Model(&model.StoragePendingCleanup{}).Where("hostid = ? AND cluster_uuid = ?", h[2], f.cluster.UUID).Count(&n)
	if n != 1 {
		t.Fatalf("pending cleanups of the host that went offline: %d", n)
	}
	db.Unscoped().Where("cluster_uuid = ?", f.cluster.UUID).Delete(&model.StoragePendingCleanup{})
	f.setHostStatus(h[2], 1)
}

// The refusal of an operation names the layout only when the kind has one (a Ceph cluster said "ceph clusters ()")
func TestStorageUnsupportedOp(t *testing.T) {
	for _, c := range []struct {
		cluster *model.StorageCluster
		want    string
	}{
		{&model.StorageCluster{Kind: model.StorageKindCeph}, "ceph clusters do not support this operation yet"},
		{&model.StorageCluster{Kind: model.StorageKindGPFS, Layout: model.StorageLayoutECE}, "gpfs clusters (ece) do not support this operation yet"},
	} {
		if err := storageUnsupportedOp(c.cluster); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("storageUnsupportedOp(%s/%s) = %v, want %q", c.cluster.Kind, c.cluster.Layout, err, c.want)
		}
	}
}

// A disk identified by itself (WWN, serial) is the same disk on every host; a loop file or a device name is not
func TestDiskIDOfHostOnly(t *testing.T) {
	for id, want := range map[string]bool{
		"wwn-0x6001405f22c0a3a218e4a1e801591e75": false,
		"nvme-eui.0025385b71b0c0a1":              false,
		"ata-ST2000NM0055_ZC22PH0W":              false,
		"scsi-36001405f22c0a3a218e4a1e801591e75": false,
		"nvme-SAMSUNG_MZ1L2960_S6EXNE0R":         false,
		"loop:/var/lib/cl-s3test/osd1.img":       true,
		"dev:sdc":                                true,
		"dm-name-mpatha":                         true,
		"pci-0000:3b:00.0-scsi-0:0:1:0":          true,
	} {
		if got := diskIDOfHostOnly(id); got != want {
			t.Errorf("diskIDOfHostOnly(%q) = %v, want %v", id, got, want)
		}
	}
}
