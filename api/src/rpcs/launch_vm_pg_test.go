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

// The last argument of launch_vm.sh is either how the host came to report the instance ("init" on its first start,
// "sync" after a boot) or a reason; "init" is not kept as the reason of the instance (R2-15). "sync" is not run here:
// it takes the boot sync path, which sends commands to the hosts (TC-14 HA-06 checks it on a real reboot)
func TestLaunchVMReasonPG(t *testing.T) {
	db := consoleTestDB(t)
	const node = int32(9861)
	db.Unscoped().Where("hostid = ?", node).Delete(&model.Hyper{})
	if err := db.Create(&model.Hyper{Hostid: node, Hostname: fmt.Sprintf("lvr-%d", node), Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	inst := &model.Instance{Hostname: fmt.Sprintf("lvr-%d", time.Now().UnixNano()), Status: model.InstanceStatusProvisioning, Hyper: -1}
	if err := db.Create(inst).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Unscoped().Delete(inst)
		db.Unscoped().Where("hostid = ?", node).Delete(&model.Hyper{})
	})
	for _, c := range []struct{ word, want string }{{"init", ""}, {"something else", "something else"}} {
		if err := db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("reason", "storage_pending").Error; err != nil {
			t.Fatal(err)
		}
		nodeCallback(t, node, fmt.Sprintf("launch_vm.sh '%d' 'running' '%d' '%s'", inst.ID, node, c.word))
		got := &model.Instance{}
		if err := db.Unscoped().Take(got, inst.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Reason != c.want || got.Status != model.InstanceStatusRunning || got.Hyper != node {
			t.Errorf("launch_vm.sh with %q: reason %q status %s host %d, want reason %q running on %d", c.word, got.Reason, got.Status, got.Hyper, c.want, node)
		}
	}
}
