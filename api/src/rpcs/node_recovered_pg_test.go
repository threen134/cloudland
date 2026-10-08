/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// Needs PostgreSQL: CLAPI_TEST_DB_URI="postgres://..." go test ./src/rpcs -run PG; skipped without it

import (
	"context"
	"fmt"
	"testing"
	"time"

	"api/src/model"
	"api/src/services"
)

// A host an instance was evacuated from is not believed about it until it removed its copy
// (shared-storage-design.md §11.4): neither the heartbeat nor a boot sync takes the instance back
func TestEvacuationSourceIgnoredPG(t *testing.T) {
	db := consoleTestDB(t)
	const source, target = int32(9851), int32(9852)
	db.Unscoped().Where("hostid IN ?", []int32{source, target}).Delete(&model.Hyper{})
	for _, h := range []int32{source, target} {
		if err := db.Create(&model.Hyper{Hostid: h, Hostname: fmt.Sprintf("evs-%d", h), Status: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	inst := &model.Instance{Hostname: fmt.Sprintf("evs-%d", time.Now().UnixNano()), Status: model.InstanceStatusRunning, Hyper: target}
	if err := db.Create(inst).Error; err != nil {
		t.Fatal(err)
	}
	mig := &model.Migration{InstanceID: inst.ID, Type: model.MigrationTypeEvacuate, SourceHyper: source, TargetHyper: target, Status: "completed"}
	if err := db.Create(mig).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Migration{})
		db.Unscoped().Delete(inst)
		db.Unscoped().Where("hostid IN ?", []int32{source, target}).Delete(&model.Hyper{})
	})
	where := func() (model.InstanceStatus, int32) {
		t.Helper()
		got := &model.Instance{}
		if err := db.Unscoped().Take(got, inst.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got.Status, got.Hyper
	}
	nodeCallback(t, source, fmt.Sprintf("inst_status.sh '%d' '%d pending_reconcile'", source, inst.ID))
	nodeCallback(t, source, fmt.Sprintf("launch_vm.sh '%d' 'running' '%d' 'sync'", inst.ID, source))
	if st, h := where(); st != model.InstanceStatusRunning || h != target {
		t.Fatalf("the evacuation source took the instance back: %s on %d", st, h)
	}
	// Another host can not claim it removed the source's copy; the source itself can
	nodeCallback(t, target, fmt.Sprintf("clear_stale_vm.sh '%d' 'done'", inst.ID))
	nodeCallback(t, source, fmt.Sprintf("clear_stale_vm.sh '%d' 'error'", inst.ID))
	if db.Take(mig, mig.ID); mig.SourceCleaned {
		t.Fatal("source marked cleaned by another host or by a failed removal")
	}
	// Removed from the source: its reports count again (the instance migrated back later, say)
	nodeCallback(t, source, fmt.Sprintf("clear_stale_vm.sh '%d' 'done'", inst.ID))
	if db.Take(mig, mig.ID); !mig.SourceCleaned {
		t.Fatal("source not marked cleaned")
	}
	nodeCallback(t, source, fmt.Sprintf("inst_status.sh '%d' '%d pending_reconcile'", source, inst.ID))
	st, h := where()
	got := &model.Instance{}
	db.Take(got, inst.ID)
	if st != model.InstanceStatusShutoff || h != source || got.Reason != services.InstanceReasonReconcilePending {
		t.Fatalf("report after the copy was removed: %s on %d (%s)", st, h, got.Reason)
	}
	// A host reporting for another one is refused
	if _, err := NodeReconciled(contextOf(target), []string{"node_reconciled", fmt.Sprint(source), "b"}); err == nil {
		t.Fatal("node_reconciled for another host was taken")
	}
}

// A host back from offline gets the status an admin gave it before (maintenance, disabled), not active
func TestHyperBackFromOfflinePG(t *testing.T) {
	db := consoleTestDB(t)
	const node = int32(9853)
	db.Unscoped().Where("hostid = ?", node).Delete(&model.Hyper{})
	if err := db.Create(&model.Hyper{Hostid: node, Hostname: "back-node", Status: 2, RouteIP: "198.51.100.9"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Unscoped().Where("hostid = ?", node).Delete(&model.Hyper{}) })
	report := fmt.Sprintf("hyper_status.sh '%d' 'back-node' '4' '8' '1000' '2000' '10' '20' '1' '10.98.0.3' 'z' '1' '1' '1' 'cpu'", node)
	for _, prior := range []int32{2, 0, 1} {
		db.Model(&model.Hyper{}).Where("hostid = ?", node).Updates(map[string]interface{}{"status": prior, "offline_at": nil, "offline_prior": nil})
		services.MarkHyperOffline(contextOf(node), node)
		db.Model(&model.Hyper{}).Where("hostid = ?", node).Update("status", model.HyperStatusOffline)
		nodeCallback(t, node, report)
		got := &model.Hyper{}
		if err := db.Where("hostid = ?", node).Take(got).Error; err != nil {
			t.Fatal(err)
		}
		if got.Status != prior || got.OfflineAt != nil || got.OfflinePrior != nil {
			t.Fatalf("back from offline with prior %d: status %d offline_at %v prior %v", prior, got.Status, got.OfflineAt, got.OfflinePrior)
		}
	}
}

func contextOf(hostid int32) context.Context {
	return context.WithValue(context.Background(), "hostid", hostid)
}
