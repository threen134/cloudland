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

func TestTargetMigrationRefusedPG(t *testing.T) {
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
	inst := &model.Instance{Hostname: fmt.Sprintf("refused-%d", time.Now().UnixNano()), Status: model.InstanceStatusMigrating, Hyper: 9701}
	must(db.Create(inst).Error)
	task := &model.Task{Name: "Prepare_Target", Status: "in_progress"}
	mig := &model.Migration{InstanceID: inst.ID, Name: "refused", SourceHyper: 9701, TargetHyper: 9702, Status: "in_progress",
		PriorStatus: string(model.InstanceStatusShutoff), Phases: []*model.Task{task}}
	must(db.Create(mig).Error)
	must(db.Create(&model.StorageReservation{Hostid: 9702, PoolID: 1, VolumeID: 1, MigrationID: mig.ID, Kind: model.ReservationMigration, SizeGB: 10,
		ExpiresAt: time.Now().Add(time.Hour)}).Error)

	// cland hands the original command back with error=resource (rpcs/frontback.go)
	cmd, args := DecodeCommand(fmt.Sprintf("/opt/cloudland/scripts/backend/target_migration.sh '%d' '%d' '%d' 'refused' '1' '1024' '10' 'src' 'warm' 'bios' 'uuid'", mig.ID, task.ID, inst.ID))
	if cmd != "target_migration" || Get(cmd) == nil {
		t.Fatalf("command %q has no handler", cmd)
	}
	ctx := context.WithValue(context.Background(), "error", "resource")
	for i := 0; i < 2; i++ { // cland retries the callback
		if _, err := Get(cmd)(ctx, args); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	stored := &model.Instance{}
	must(db.Where("id = ?", inst.ID).Take(stored).Error)
	if stored.Status != model.InstanceStatusShutoff {
		t.Errorf("instance %s, want shut_off back", stored.Status)
	}
	storedMig := &model.Migration{}
	must(db.Where("id = ?", mig.ID).Take(storedMig).Error)
	storedTask := &model.Task{}
	must(db.Where("id = ?", task.ID).Take(storedTask).Error)
	if storedMig.Status != "failed" || storedTask.Status != "failed" {
		t.Errorf("migration %s, task %s; want both failed", storedMig.Status, storedTask.Status)
	}
	var left int64
	must(db.Model(&model.StorageReservation{}).Where("migration_id = ?", mig.ID).Count(&left).Error)
	if left != 0 {
		t.Errorf("%d reservations left, want none", left)
	}
}
