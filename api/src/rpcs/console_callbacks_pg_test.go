/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// Needs PostgreSQL: CLAPI_TEST_DB_URI="postgres://..." go test ./src/rpcs -run PG; skipped without it

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"api/src/dbs"
	"api/src/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func consoleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	uri := os.Getenv("CLAPI_TEST_DB_URI")
	if uri == "" {
		t.Skip("CLAPI_TEST_DB_URI is not set: this test needs PostgreSQL")
	}
	dbs.OpenDB = func() *gorm.DB {
		db, err := gorm.Open(postgres.Open(uri), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: gormlogger.Discard})
		if err != nil {
			panic(err)
		}
		return db
	}
	return dbs.DB()
}

// nodeCallback runs a callback line of a node script as clapi does (rpcs/frontback.go), from node hostid
func nodeCallback(t *testing.T, hostid int32, line string) {
	t.Helper()
	cmd, args := DecodeCommand(line)
	handler := Get(cmd)
	if handler == nil {
		t.Fatalf("command %q has no handler", cmd)
	}
	if _, err := handler(context.WithValue(context.Background(), "hostid", hostid), args); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
}

func consoleTestInstanceState(t *testing.T, db *gorm.DB, id int64) (model.InstanceStatus, string) {
	t.Helper()
	inst := &model.Instance{}
	if err := db.Where("id = ?", id).Take(inst).Error; err != nil {
		t.Fatal(err)
	}
	return inst.Status, inst.Reason
}

// rescue_vm.sh confirms the rescue; only its early failure turns the instance back to shut_off (D11)
func TestRescueCallbackPG(t *testing.T) {
	db := consoleTestDB(t)
	const node, other = int32(9811), int32(9812)
	inst := &model.Instance{Hostname: fmt.Sprintf("rescue-%d", time.Now().UnixNano()), Status: model.InstanceStatusRescuing, Hyper: node}
	if err := db.Create(inst).Error; err != nil {
		t.Fatal(err)
	}
	expect := func(want model.InstanceStatus) {
		t.Helper()
		if status, _ := consoleTestInstanceState(t, db, inst.ID); status != want {
			t.Fatalf("instance %s, want %s", status, want)
		}
	}
	nodeCallback(t, node, fmt.Sprintf("rescue_vm.sh '%d' 'rescuing' '%d' 'sync'", inst.ID, node))
	expect(model.InstanceStatusRescuing)
	nodeCallback(t, node, fmt.Sprintf("rescue_vm.sh '%d' 'error' '%d' 'sync'", inst.ID, node))
	expect(model.InstanceStatusRescuing) // end_rescue removes the rescue domain that did not start
	nodeCallback(t, other, fmt.Sprintf("rescue_vm.sh '%d' 'error' '%d' 'failed'", inst.ID, other))
	expect(model.InstanceStatusRescuing) // not the node of the instance
	nodeCallback(t, node, fmt.Sprintf("rescue_vm.sh '%d' 'error' '%d' 'failed'", inst.ID, node))
	expect(model.InstanceStatusShutoff)
	// A late report does not bring back a rescue that is over
	nodeCallback(t, node, fmt.Sprintf("rescue_vm.sh '%d' 'rescuing' '%d' 'sync'", inst.ID, node))
	expect(model.InstanceStatusShutoff)
}

// set_user_passwd.sh leaves a failure on the instance until a change succeeds (D17)
func TestSetUserPasswdCallbackPG(t *testing.T) {
	db := consoleTestDB(t)
	const node, other = int32(9821), int32(9822)
	inst := &model.Instance{Hostname: fmt.Sprintf("passwd-%d", time.Now().UnixNano()), Status: model.InstanceStatusRunning, Hyper: node, Reason: "init"}
	if err := db.Create(inst).Error; err != nil {
		t.Fatal(err)
	}
	expect := func(want string) {
		t.Helper()
		if _, reason := consoleTestInstanceState(t, db, inst.ID); reason != want {
			t.Fatalf("reason %q, want %q", reason, want)
		}
	}
	nodeCallback(t, node, fmt.Sprintf("set_user_passwd.sh '%d' 'success'", inst.ID))
	expect("init")
	nodeCallback(t, node, fmt.Sprintf("set_user_passwd.sh '%d' 'error' 'agent_unavailable'", inst.ID))
	expect(InstanceReasonPasswordFailed)
	nodeCallback(t, other, fmt.Sprintf("set_user_passwd.sh '%d' 'success'", inst.ID))
	expect(InstanceReasonPasswordFailed) // not the node of the instance
	nodeCallback(t, node, fmt.Sprintf("set_user_passwd.sh '%d' 'success'", inst.ID))
	expect("")
}

// The console resolver finds an address only when the node set the VNC password (D6)
func TestSetVncPasswdCallbackPG(t *testing.T) {
	db := consoleTestDB(t)
	const node, other = int32(9831), int32(9832)
	inst := &model.Instance{Hostname: fmt.Sprintf("vnc-%d", time.Now().UnixNano()), Status: model.InstanceStatusRunning, Hyper: node}
	if err := db.Create(inst).Error; err != nil {
		t.Fatal(err)
	}
	record := func() (*model.Vnc, error) {
		vnc := &model.Vnc{}
		return vnc, db.Where("instance_id = ?", inst.ID).Take(vnc).Error
	}
	// What the resolver does before it sends set_vnc_passwd.sh
	reset := func() {
		t.Helper()
		if err := db.Where("instance_id = ?", inst.ID).Delete(&model.Vnc{}).Error; err != nil {
			t.Fatal(err)
		}
	}

	nodeCallback(t, node, fmt.Sprintf("set_vnc_passwd.sh '%d' '5906' '10.191.202.40'", inst.ID))
	if vnc, err := record(); err != nil || vnc.LocalAddress != "10.191.202.40" || vnc.LocalPort != 5906 {
		t.Fatalf("record %+v, %v; want the address", vnc, err)
	}
	// A failure right after a success on the same record: the address must go
	nodeCallback(t, node, fmt.Sprintf("set_vnc_passwd.sh '%d' 'error' 'VNC password authentication is off until the instance is stopped and started again'", inst.ID))
	if vnc, err := record(); err != nil || vnc.LocalAddress != "" || vnc.LocalPort != 0 {
		t.Fatalf("record %+v, %v; want one without address", vnc, err)
	}
	reset()
	nodeCallback(t, node, fmt.Sprintf("set_vnc_passwd.sh '%d' 'error' 'inst-%d is not running'", inst.ID, inst.ID))
	if vnc, err := record(); err != nil || vnc.LocalAddress != "" {
		t.Fatalf("record %+v, %v; want one without address", vnc, err)
	}
	reset()
	nodeCallback(t, other, fmt.Sprintf("set_vnc_passwd.sh '%d' '5901' '10.191.202.13'", inst.ID))
	if vnc, err := record(); err == nil {
		t.Fatalf("record %+v from another node", vnc)
	}
}
