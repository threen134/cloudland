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

// FAIL-5: the host confirming the delete of an instance that never got its boot disk drops the room held for it
func TestClearVMReleasesBootReservationPG(t *testing.T) {
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
	db := dbs.DB()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Deleted while provisioning: never placed, and the delete request already removed the boot volume record
	inst := &model.Instance{Hostname: fmt.Sprintf("clear-%d", time.Now().UnixNano()), Status: model.InstanceStatusDeleting, Hyper: -1}
	must(db.Create(inst).Error)
	boot := &model.Volume{Name: inst.Hostname + "-boot", Size: 10, Booting: true, InstanceID: inst.ID, Status: model.VolumeStatusAttached}
	must(db.Create(boot).Error)
	must(db.Delete(boot).Error)
	data := &model.Volume{Name: inst.Hostname + "-data", Size: 1, InstanceID: inst.ID, Status: model.VolumeStatusAttached}
	must(db.Create(data).Error)
	must(db.Create(&model.StorageReservation{Hostid: 9801, PoolID: 1, VolumeID: boot.ID, Kind: model.ReservationBoot, SizeGB: 10,
		ExpiresAt: time.Now().Add(24 * time.Hour)}).Error)

	cmd, args := DecodeCommand(fmt.Sprintf("/opt/cloudland/scripts/backend/clear_vm.sh '%d'", inst.ID))
	if cmd != "clear_vm" || Get(cmd) == nil {
		t.Fatalf("command %q has no handler", cmd)
	}
	if _, err := Get(cmd)(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	var n int64
	must(db.Model(&model.StorageReservation{}).Where("volume_id = ? AND kind = ?", boot.ID, model.ReservationBoot).Count(&n).Error)
	if n != 0 {
		t.Fatalf("%d boot reservations left", n)
	}
	stored := &model.Volume{}
	must(db.Where("id = ?", data.ID).Take(stored).Error)
	if stored.InstanceID != 0 || stored.Status != model.VolumeStatusAvailable {
		t.Fatalf("data volume: instance %d, status %s", stored.InstanceID, stored.Status)
	}
	var deleted int64
	must(db.Unscoped().Model(&model.Instance{}).Where("id = ? AND deleted_at IS NOT NULL", inst.ID).Count(&deleted).Error)
	if deleted != 1 {
		t.Fatal("instance not deleted")
	}
}
