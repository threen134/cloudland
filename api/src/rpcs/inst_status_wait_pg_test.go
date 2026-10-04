/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// Needs PostgreSQL: CLAPI_TEST_DB_URI="postgres://..." go test ./src/rpcs -run PG; skipped without it

import (
	"fmt"
	"testing"
	"time"

	"api/src/model"
)

// The heartbeat keeps an instance reinstalling while its reinstall waits for the copy of its image (a power state
// it reports meanwhile would let other operations in), and reports its state as usual otherwise
func TestInstStatusKeepsWaitingReinstallPG(t *testing.T) {
	db := consoleTestDB(t)
	const node = int32(9841)
	db.Unscoped().Where("hostid = ?", node).Delete(&model.Hyper{})
	if err := db.Create(&model.Hyper{Hostid: node, Hostname: "wait-node", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Unscoped().Where("hostid = ?", node).Delete(&model.Hyper{}) })
	inst := &model.Instance{Hostname: fmt.Sprintf("wait-%d", time.Now().UnixNano()), Status: model.InstanceStatusReinstalling, Hyper: node}
	if err := db.Create(inst).Error; err != nil {
		t.Fatal(err)
	}
	waiter := &model.ImageStorageWaiter{ImageStorageID: 1, InstanceID: inst.ID, Kind: model.ImageWaitReinstall, Control: fmt.Sprintf("inter=%d", node), Command: "x"}
	if err := db.Create(waiter).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.ImageStorageWaiter{})
		db.Unscoped().Delete(inst)
	})
	nodeCallback(t, node, fmt.Sprintf("inst_status.sh '%d' '%d shut_off'", node, inst.ID))
	if status, _ := consoleTestInstanceState(t, db, inst.ID); status != model.InstanceStatusReinstalling {
		t.Fatalf("heartbeat changed an instance whose reinstall waits to %s", status)
	}
	db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.ImageStorageWaiter{})
	nodeCallback(t, node, fmt.Sprintf("inst_status.sh '%d' '%d shut_off'", node, inst.ID))
	if status, _ := consoleTestInstanceState(t, db, inst.ID); status != model.InstanceStatusShutoff {
		t.Fatalf("heartbeat without a waiting reinstall: %s", status)
	}
}
