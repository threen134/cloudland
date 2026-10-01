/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// dryRunDB builds SQL for PostgreSQL without a database: nothing connects, nothing runs
func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable"}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// insertedValue returns the value an INSERT built by GORM writes to column
func insertedValue(t *testing.T, stmt *gorm.Statement, column string) (interface{}, bool) {
	t.Helper()
	// The INSERT lists its columns in order and binds one var per column
	sql := stmt.SQL.String()
	start, end := strings.Index(sql, "("), strings.Index(sql, ")")
	if start < 0 || end < start {
		t.Fatalf("unexpected INSERT: %s", sql)
	}
	for i, c := range strings.Split(sql[start+1:end], ",") {
		if strings.TrimSpace(c) == `"`+column+`"` {
			return stmt.Vars[i], true
		}
	}
	return nil, false
}

// A channel CPGateway pushes as disabled used to be mirrored as enabled: the gorm default:true on Enabled made
// GORM replace the deliberate false with true on INSERT, and the disabled channel kept receiving notifications
func TestNotificationChannelCreateKeepsDisabled(t *testing.T) {
	db := dryRunDB(t)
	stmt := db.Create(&NotificationChannel{OrgUUID: "o1", Name: "n", Type: "webhook", Config: "{}", Enabled: false}).Statement
	value, ok := insertedValue(t, stmt, "enabled")
	if !ok {
		t.Fatalf("INSERT does not write enabled: %s", stmt.SQL.String())
	}
	if value != false {
		t.Fatalf("INSERT writes enabled=%v for a disabled channel: %s %v", value, stmt.SQL.String(), stmt.Vars)
	}
}

// The same trap on the other mirrored / alarm models: a bool whose zero value means something must not carry a
// gorm default other than false, or Create silently turns false into that default
func TestAlarmModelsHaveNoTruthyBoolDefaults(t *testing.T) {
	db := dryRunDB(t)
	for _, m := range []interface{}{&NotificationChannel{}, &AlarmNotificationBinding{}, &AlarmEvent{}, &AlarmDeliveryLog{}, &NodeAlarmRule{}, &RuleGroupV2{}} {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(m); err != nil {
			t.Fatal(err)
		}
		for _, f := range stmt.Schema.Fields {
			if f.FieldType.Kind().String() == "bool" && f.HasDefaultValue && f.DefaultValueInterface != false {
				t.Errorf("%s.%s has gorm default %q", stmt.Schema.Name, f.Name, f.DefaultValue)
			}
		}
	}
}
