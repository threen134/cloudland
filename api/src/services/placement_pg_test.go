/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Placement tests that need PostgreSQL (placement-group-plan.md §10.1): the occupancy of a group, the grouped
// statistics, the migrations of members. They run against an empty database given by CLAPI_TEST_DB_URI, e.g.
//
//	CLAPI_TEST_DB_URI="postgres://user:pass@127.0.0.1:5432/testdb?sslmode=disable" go test ./src/services -run PG
//
// and are skipped without it. Every test uses its own host ids, zone and organization, so they can share the database.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var pgOnce sync.Once

// pgContext opens the test database and returns a context of a system admin acting in organization org
func pgContext(t *testing.T, org int64) (context.Context, *gorm.DB) {
	t.Helper()
	uri := os.Getenv("CLAPI_TEST_DB_URI")
	if uri == "" {
		t.Skip("CLAPI_TEST_DB_URI is not set: this test needs PostgreSQL")
	}
	pgOnce.Do(func() {
		dbs.OpenDB = func() *gorm.DB {
			db, err := gorm.Open(postgres.Open(uri), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: gormlogger.Discard})
			if err != nil {
				panic(err)
			}
			return db
		}
	})
	db := dbs.DB()
	membership := &MemberShip{UserID: 1, UserName: "tester", OrgID: org, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}
	return membership.SetContext(context.Background()), db
}

type pgFixture struct {
	t       *testing.T
	ctx     context.Context
	db      *gorm.DB
	zone    *model.Zone
	org     int64
	builtin *model.StoragePool
}

func newPGFixture(t *testing.T, org int64) *pgFixture {
	ctx, db := pgContext(t, org)
	zone := &model.Zone{Name: fmt.Sprintf("pg-zone-%d-%d", org, time.Now().UnixNano())}
	must(t, db.Create(zone).Error)
	builtin, err := BuiltinPool(ctx)
	must(t, err)
	return &pgFixture{t: t, ctx: ctx, db: db, zone: zone, org: org, builtin: builtin}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// host registers an active host with room for cpu vCPUs, memGiB of memory and diskGiB in the built-in pool
func (f *pgFixture) host(hostid int32, cpu, memGiB, diskGiB int64) {
	// Left by an earlier run on the same database
	must(f.t, f.db.Unscoped().Where("hostid = ?", hostid).Delete(&model.Hyper{}).Error)
	must(f.t, f.db.Where("hostid = ?", hostid).Delete(&model.Resource{}).Error)
	must(f.t, f.db.Create(&model.Hyper{Hostid: hostid, Hostname: fmt.Sprintf("pg-host-%d", hostid), Status: 1, ZoneID: f.zone.ID}).Error)
	must(f.t, f.db.Create(&model.Resource{Hostid: hostid, Cpu: cpu, CpuTotal: cpu, Memory: memGiB << 20, MemoryTotal: memGiB << 20,
		Disk: diskGiB * gib, DiskTotal: diskGiB * gib}).Error)
	// Reported a minute ago: members landed since then are not in these figures yet
	must(f.t, f.db.Model(&model.Resource{}).Where("hostid = ?", hostid).UpdateColumn("updated_at", time.Now().Add(-time.Minute)).Error)
}

// name makes a group name unique to this run: the names of an organization are unique and the database is kept
func (f *pgFixture) name(base string) string {
	return fmt.Sprintf("%s-%d", base, f.zone.ID)
}

func (f *pgFixture) group(name, policy string, strict bool) *model.PlacementGroup {
	g := &model.PlacementGroup{Owner: f.org, Name: f.name(name), Policy: policy, Strict: strict, ZoneID: f.zone.ID}
	must(f.t, f.db.Create(g).Error)
	return g
}

// member creates an instance of the group with a boot disk of diskGiB in the built-in pool. hyper -1 with a
// placement host is a member being created; age sets created_at and updated_at that far back.
func (f *pgFixture) member(g *model.PlacementGroup, name string, hyper, placement int32, status model.InstanceStatus, cpu, memMB, diskGiB int32, age time.Duration) *model.Instance {
	inst := &model.Instance{Owner: f.org, Hostname: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), Status: status, Hyper: hyper,
		ZoneID: f.zone.ID, Cpu: cpu, Memory: memMB, Disk: diskGiB, PlacementHyper: placement}
	if g != nil {
		inst.PlacementGroupID = g.ID
	}
	must(f.t, f.db.Create(inst).Error)
	volHyper := hyper
	if volHyper < 0 {
		volHyper = 0
	}
	must(f.t, f.db.Create(&model.Volume{Owner: f.org, Name: inst.Hostname + "-boot", Size: diskGiB, Booting: true, Status: model.VolumeStatusAttached,
		InstanceID: inst.ID, Hyper: volHyper, StoragePoolID: f.builtin.ID, Path: fmt.Sprintf("instance/inst-%d.disk", inst.ID)}).Error)
	at := time.Now().Add(-age)
	must(f.t, f.db.Model(&model.Instance{}).Where("id = ?", inst.ID).UpdateColumns(map[string]interface{}{"created_at": at, "updated_at": at}).Error)
	inst.CreatedAt, inst.UpdatedAt = at, at
	return inst
}

func (f *pgFixture) migration(inst *model.Instance, target int32, status string, age time.Duration) *model.Migration {
	m := &model.Migration{InstanceID: inst.ID, Name: "pg", Type: "warm", SourceHyper: inst.Hyper, TargetHyper: target, Status: status}
	must(f.t, f.db.Create(m).Error)
	must(f.t, f.db.Model(&model.Migration{}).Where("id = ?", m.ID).UpdateColumn("updated_at", time.Now().Add(-age)).Error)
	return m
}

func TestPlacementStateKindsPG(t *testing.T) {
	f := newPGFixture(t, 9101)
	for _, h := range []int32{9101, 9102, 9103, 9104} {
		f.host(h, 64, 256, 2000)
	}
	g := f.group("state", model.PlacementPolicySpread, true)
	landed := f.member(g, "landed", 9101, 0, model.InstanceStatusRunning, 2, 2048, 10, 10*time.Minute)
	creating := f.member(g, "creating", -1, 9102, model.InstanceStatusProvisioning, 4, 4096, 20, time.Minute)
	f.member(g, "refused", -1, 9103, "error", 8, 8192, 40, time.Minute)
	deleting := f.member(g, "deleting", -1, 9104, model.InstanceStatusDeleting, 1, 1024, 10, time.Minute)
	gone := f.member(g, "gone", 9103, 0, model.InstanceStatusRunning, 1, 1024, 10, time.Minute)
	must(t, f.db.Delete(gone).Error)
	moving := f.member(g, "moving", 9103, 0, model.InstanceStatusMigrating, 2, 2048, 30, 10*time.Minute)
	mig := f.migration(moving, 9104, "source_prepared", 2*time.Hour)
	stuck := f.member(g, "stuck", -1, 9102, model.InstanceStatusProvisioning, 1, 1024, 10, 2*time.Hour)
	fresh := f.member(g, "fresh", 9101, 0, model.InstanceStatusRunning, 1, 1024, 10, 0)
	// A migration that ended does not count
	f.migration(landed, 9104, "completed", time.Minute)

	st, err := loadPlacementState(f.db, g, nil, time.Now())
	must(t, err)
	wantOcc := map[int32]int{9101: 2, 9102: 2, 9103: 1, 9104: 2}
	for h, n := range wantOcc {
		if st.Occ[h] != n {
			t.Errorf("occ[%d] = %d, want %d (all %v)", h, st.Occ[h], n, st.Occ)
		}
	}
	// Pending: members being created on their chosen host, the migration target, and the member landed after the
	// last resource report of its host; not the one that landed before it
	wantPending := map[int32]Demand{
		9101: demand(1, 1, 10),
		9102: demand(4, 4, 20).Add(demand(1, 1, 10)),
		9104: demand(1, 1, 10).Add(demand(2, 2, 30)),
	}
	for h, d := range wantPending {
		if st.Pending[h] != d {
			t.Errorf("pending[%d] = %+v, want %+v", h, st.Pending[h], d)
		}
	}
	if _, ok := st.Pending[9103]; ok {
		t.Errorf("pending[9103] = %+v, want none", st.Pending[9103])
	}
	flags := map[int64]*placementMember{}
	for _, m := range st.Members {
		flags[m.Instance.ID] = m
	}
	if len(st.Members) != 7 {
		t.Errorf("%d members, want 7 (the deleted one left out)", len(st.Members))
	}
	if !flags[stuck.ID].StaleProvisioning || flags[creating.ID].StaleProvisioning {
		t.Error("only the member created two hours ago is stale")
	}
	if m := flags[moving.ID]; m.Target != 9104 || m.Migration == nil || m.Migration.ID != mig.ID || !m.StaleMigration {
		t.Errorf("moving: target %d, stale %t; want 9104 and stale", m.Target, m.StaleMigration)
	}
	if flags[deleting.ID].Host != 9104 || flags[fresh.ID].Host != 9101 {
		t.Error("a member being deleted before landing holds its chosen host")
	}
	if len(st.Migrating) != 1 || st.compliant() {
		t.Errorf("migrating %d, compliant %t; want 1 and not compliant (two members on 9101 and on 9102)", len(st.Migrating), st.compliant())
	}

	// A migrating member leaves its source, its target still counts
	st, err = loadPlacementState(f.db, g, map[int64]bool{moving.ID: true}, time.Now())
	must(t, err)
	if st.Occ[9103] != 0 || st.Occ[9104] != 2 {
		t.Errorf("excluded: occ %v, want 9103 empty and 9104 at 2", st.Occ)
	}
}

func TestPlacementGroupStatsPG(t *testing.T) {
	f := newPGFixture(t, 9201)
	for _, h := range []int32{9201, 9202} {
		f.host(h, 64, 256, 2000)
	}
	spread := f.group("stats-spread", model.PlacementPolicySpread, false)
	f.member(spread, "a", 9201, 0, model.InstanceStatusRunning, 1, 1024, 10, time.Minute)
	f.member(spread, "b", 9201, 0, model.InstanceStatusRunning, 1, 1024, 10, time.Minute)
	f.member(spread, "c", -1, 9202, model.InstanceStatusProvisioning, 1, 1024, 10, time.Minute)
	f.member(spread, "d", -1, 9202, "error", 1, 1024, 10, time.Minute)
	pack := f.group("stats-pack", model.PlacementPolicyPack, true)
	f.member(pack, "e", 9202, 0, model.InstanceStatusRunning, 1, 1024, 10, time.Minute)
	f.member(pack, "f", -1, 9202, model.InstanceStatusProvisioning, 1, 1024, 10, time.Minute)
	empty := f.group("stats-empty", model.PlacementPolicyPack, false)

	stats, err := PlacementGroupAdmin.Stats(f.ctx, []*model.PlacementGroup{spread, pack, empty})
	must(t, err)
	check := func(g *model.PlacementGroup, members, hosts int, compliant bool) {
		s := stats[g.ID]
		if s.MemberCount != members || s.HostCount != hosts || s.Compliant != compliant {
			t.Errorf("%s: %+v, want members %d hosts %d compliant %t", g.Name, *s, members, hosts, compliant)
		}
	}
	check(spread, 4, 2, false)
	check(pack, 2, 1, true)
	check(empty, 0, 0, true)

	members, detail, err := PlacementGroupAdmin.Members(f.ctx, spread)
	must(t, err)
	if detail.HostCount != 2 || detail.Compliant || len(members) != 4 {
		t.Fatalf("detail %+v with %d members", *detail, len(members))
	}
	slots := []int{}
	for _, m := range members {
		slots = append(slots, m.HostSlot)
	}
	if fmt.Sprint(slots) != "[1 1 2 0]" {
		t.Errorf("host slots %v, want [1 1 2 0]", slots)
	}
}

func TestPlacementGroupDeletePG(t *testing.T) {
	f := newPGFixture(t, 9301)
	f.host(9301, 64, 256, 2000)
	g := f.group("del", model.PlacementPolicySpread, true)
	inst := f.member(g, "a", -1, 9301, model.InstanceStatusDeleting, 1, 1024, 10, time.Minute)
	if err := PlacementGroupAdmin.Delete(f.ctx, g); errCode(err) != ErrPlacementGroupInUse {
		t.Fatalf("delete with a member being deleted: %v, want ErrPlacementGroupInUse", err)
	}
	must(t, f.db.Delete(inst).Error)
	must(t, PlacementGroupAdmin.Delete(f.ctx, g))
	// The name is free again
	zone := f.zone
	again, err := PlacementGroupAdmin.Create(f.ctx, g.Name, "", model.PlacementPolicySpread, true, zone)
	must(t, err)
	if again.ID == g.ID {
		t.Fatal("a new group was expected")
	}
	if _, err = PlacementGroupAdmin.Create(f.ctx, g.Name, "", model.PlacementPolicyPack, false, zone); errCode(err) != ErrPlacementGroupExists {
		t.Fatalf("duplicate name: %v, want ErrPlacementGroupExists", err)
	}
}

// Creating instances needs images, subnets and the rest; the part of it that matters here is the placement of the
// batch under the lock, which startCreationPlacement and next do
func TestCreationPlacementPG(t *testing.T) {
	f := newPGFixture(t, 9401)
	for _, h := range []int32{9401, 9402, 9403} {
		f.host(h, 16, 64, 2000)
	}
	spread := f.group("create-spread", model.PlacementPolicySpread, true)
	f.member(spread, "a", 9401, 0, model.InstanceStatusRunning, 1, 1024, 10, 10*time.Minute)
	tx := f.db.Begin()
	defer tx.Rollback()
	p, err := startCreationPlacement(tx, spread, f.zone, f.builtin, -1, 2, 2, 2048, 10)
	must(t, err)
	h1, err := p.next()
	must(t, err)
	h2, err := p.next()
	must(t, err)
	if h1 == 9401 || h2 == 9401 || h1 == h2 {
		t.Fatalf("hosts %d %d, want 9402 and 9403", h1, h2)
	}
	tx.Rollback()

	// Three more do not fit three hosts that already hold one: nothing is placed
	tx = f.db.Begin()
	p, err = startCreationPlacement(tx, spread, f.zone, f.builtin, -1, 3, 2, 2048, 10)
	must(t, err)
	_, err1 := p.next()
	_, err2 := p.next()
	_, err3 := p.next()
	tx.Rollback()
	if err1 != nil || err2 != nil || errCode(err3) != ErrPlacementGroupNoHost {
		t.Fatalf("errors %v / %v / %v, want the third to be ErrPlacementGroupNoHost", err1, err2, err3)
	}

	// The admin gives a host: only the rules count
	tx = f.db.Begin()
	if _, err = startCreationPlacement(tx, spread, f.zone, f.builtin, 9401, 1, 2, 2048, 10); errCode(err) != ErrPlacementGroupConflict {
		t.Fatalf("given host with a member: %v, want ErrPlacementGroupConflict", err)
	}
	tx.Rollback()

	// A strict pack group with a member on its way elsewhere refuses new members
	pack := f.group("create-pack", model.PlacementPolicyPack, true)
	moving := f.member(pack, "p", 9402, 0, model.InstanceStatusMigrating, 1, 1024, 10, 10*time.Minute)
	f.migration(moving, 9403, "in_progress", time.Minute)
	tx = f.db.Begin()
	if _, err = startCreationPlacement(tx, pack, f.zone, f.builtin, -1, 1, 1, 1024, 10); errCode(err) != ErrPlacementGroupBusy {
		t.Fatalf("pack group migrating: %v, want ErrPlacementGroupBusy", err)
	}
	tx.Rollback()

	// Another zone
	other := &model.Zone{Name: fmt.Sprintf("pg-other-%d", time.Now().UnixNano())}
	must(t, f.db.Create(other).Error)
	tx = f.db.Begin()
	if _, err = startCreationPlacement(tx, spread, other, f.builtin, -1, 1, 1, 1024, 10); errCode(err) != ErrPlacementGroupConflict {
		t.Fatalf("other zone: %v, want ErrPlacementGroupConflict", err)
	}
	tx.Rollback()
}

// The migration of a member without a target: the placement picks it and it is written to the database before the
// command goes out (the command then fails, cland does not run in tests)
func TestMigrationPlacementPG(t *testing.T) {
	f := newPGFixture(t, 9501)
	for _, h := range []int32{9501, 9502, 9503} {
		f.host(h, 16, 64, 2000)
	}
	g := f.group("migrate-spread", model.PlacementPolicySpread, true)
	a := f.member(g, "a", 9501, 0, model.InstanceStatusRunning, 1, 1024, 10, 10*time.Minute)
	f.member(g, "b", 9502, 0, model.InstanceStatusRunning, 1, 1024, 10, 10*time.Minute)
	opts := &MigrationOptions{AllowPoolFallback: true}
	_, results, _ := migrationAdmin.Create(f.ctx, "pg-move", []*model.Instance{a}, false, -1, opts, true)
	if len(results) != 1 || results[0].Migration == nil {
		t.Fatalf("results %+v", results)
	}
	stored := &model.Migration{}
	must(t, f.db.Where("id = ?", results[0].Migration.ID).Take(stored).Error)
	if stored.TargetHyper != 9503 || stored.PriorStatus != string(model.InstanceStatusRunning) {
		t.Fatalf("stored target %d, prior status %q; want 9503 and running", stored.TargetHyper, stored.PriorStatus)
	}

	// A strict pack group: a member alone can not go, the whole group can when one host takes all of them
	pack := f.group("migrate-pack", model.PlacementPolicyPack, true)
	p1 := f.member(pack, "p1", 9501, 0, model.InstanceStatusRunning, 2, 1024, 10, 10*time.Minute)
	p2 := f.member(pack, "p2", 9501, 0, model.InstanceStatusRunning, 8, 1024, 10, 10*time.Minute)
	p3 := f.member(pack, "p3", 9501, 0, model.InstanceStatusRunning, 4, 1024, 10, 10*time.Minute)
	must(t, f.db.Model(&model.Resource{}).Where("hostid = ?", 9503).UpdateColumn("cpu", 12).Error)
	tx := f.db.Begin()
	txCtx := SetContextDB(f.ctx, tx)
	call := &migrationCall{ids: map[int64]bool{p1.ID: true}, started: map[int64]bool{}, failed: map[int64]bool{}}
	if _, _, err := placeMigration(txCtx, tx, p1, -1, opts, call); errCode(err) != ErrPlacementGroupConflict {
		t.Fatalf("one member alone: %v, want ErrPlacementGroupConflict", err)
	}
	tx.Rollback()
	tx = f.db.Begin()
	txCtx = SetContextDB(f.ctx, tx)
	call = &migrationCall{ids: map[int64]bool{p1.ID: true, p2.ID: true, p3.ID: true}, started: map[int64]bool{}, failed: map[int64]bool{}}
	target, _, err := placeMigration(txCtx, tx, p1, -1, opts, call)
	tx.Rollback()
	// 14 vCPUs: 9503 has 12 left, 9502 has 16 (minus the pending of the spread group's migration target, none there)
	if err != nil || target != 9502 {
		t.Fatalf("whole group: target %d, err %v; want 9502", target, err)
	}
	must(t, f.db.Model(&model.Resource{}).Where("hostid = ?", 9502).UpdateColumn("cpu", 10).Error)
	tx = f.db.Begin()
	txCtx = SetContextDB(f.ctx, tx)
	_, _, err = placeMigration(txCtx, tx, p1, -1, opts, call)
	tx.Rollback()
	if errCode(err) != ErrPlacementGroupHostFull {
		t.Fatalf("no host for 14 vCPUs: %v, want ErrPlacementGroupHostFull", err)
	}

	// A member whose own migration failed earlier in the call keeps the others where they are
	call = &migrationCall{ids: map[int64]bool{p1.ID: true, p2.ID: true, p3.ID: true}, started: map[int64]bool{}, failed: map[int64]bool{p2.ID: true}}
	must(t, f.db.Model(&model.Resource{}).Where("hostid = ?", 9502).UpdateColumn("cpu", 16).Error)
	tx = f.db.Begin()
	txCtx = SetContextDB(f.ctx, tx)
	_, _, err = placeMigration(txCtx, tx, p1, -1, opts, call)
	tx.Rollback()
	if errCode(err) != ErrPlacementGroupConflict {
		t.Fatalf("a member failed in this call: %v, want ErrPlacementGroupConflict", err)
	}

	// ignore_capacity skips the room of the pools, not CPU and memory: cland checks those on the chosen host
	soft := f.group("migrate-soft", model.PlacementPolicySpread, false)
	s1 := f.member(soft, "s1", 9501, 0, model.InstanceStatusRunning, 1, 1024, 10, 10*time.Minute)
	must(t, f.db.Model(&model.Resource{}).Where("hostid = ?", 9502).UpdateColumn("cpu", 0).Error)
	tx = f.db.Begin()
	txCtx = SetContextDB(f.ctx, tx)
	call = &migrationCall{ids: map[int64]bool{s1.ID: true}, started: map[int64]bool{}, failed: map[int64]bool{}}
	target, _, err = placeMigration(txCtx, tx, s1, -1, &MigrationOptions{AllowPoolFallback: true, IgnoreCapacity: true}, call)
	tx.Rollback()
	if err != nil || target != 9503 {
		t.Fatalf("ignore_capacity: target %d, err %v; want 9503, the host with room", target, err)
	}

	// A strict pack group split by an earlier request, one member still on its way: another one waits
	busy := f.group("migrate-busy", model.PlacementPolicyPack, true)
	m1 := f.member(busy, "m1", 9501, 0, model.InstanceStatusRunning, 1, 1024, 10, 10*time.Minute)
	m2 := f.member(busy, "m2", 9503, 0, model.InstanceStatusMigrating, 1, 1024, 10, 10*time.Minute)
	f.migration(m2, 9501, "source_prepared", time.Minute)
	tx = f.db.Begin()
	txCtx = SetContextDB(f.ctx, tx)
	call = &migrationCall{ids: map[int64]bool{m1.ID: true}, started: map[int64]bool{}, failed: map[int64]bool{}}
	_, _, err = placeMigration(txCtx, tx, m1, -1, opts, call)
	tx.Rollback()
	if errCode(err) != ErrPlacementGroupBusy {
		t.Fatalf("group migrating from an earlier request: %v, want ErrPlacementGroupBusy", err)
	}
}

// A boot disk in a pool no host can take: the storage reason, not a placement one
func TestCreationPlacementPoolReasonPG(t *testing.T) {
	f := newPGFixture(t, 9701)
	f.host(9701, 16, 64, 2000)
	g := f.group("pool-reason", model.PlacementPolicySpread, true)
	pool := &model.StoragePool{Name: f.name("pg-pool"), Driver: model.StorageDriverLocal, Status: model.StoragePoolActive, OverRatio: 1}
	must(t, f.db.Create(pool).Error)
	tx := f.db.Begin()
	defer tx.Rollback()
	_, err := startCreationPlacement(tx, g, f.zone, pool, -1, 1, 1, 1024, 10)
	if errCode(err) != ErrStoragePoolUnavailable {
		t.Fatalf("pool on no host: %v, want ErrStoragePoolUnavailable", err)
	}
}

func TestLaunchNotSentPG(t *testing.T) {
	f := newPGFixture(t, 9601)
	f.host(9601, 16, 64, 2000)
	inst := f.member(nil, "lost", -1, 0, model.InstanceStatusProvisioning, 1, 1024, 10, 0)
	vol := &model.Volume{}
	must(t, f.db.Where("instance_id = ?", inst.ID).Take(vol).Error)
	must(t, f.db.Create(&model.StorageReservation{Hostid: 9601, PoolID: f.builtin.ID, VolumeID: vol.ID, Kind: model.ReservationBoot, SizeGB: 10,
		ExpiresAt: time.Now().Add(time.Hour)}).Error)
	// The command goes out after the commit: the context still carries the finished transaction
	tx := f.db.Begin()
	tx.Commit()
	instanceAdmin.launchNotSent(SetContextDB(f.ctx, tx), &ExecutionCommand{InstanceID: inst.ID, BootVolumeID: vol.ID}, fmt.Errorf("cland unreachable"))
	stored := &model.Instance{}
	must(t, f.db.Where("id = ?", inst.ID).Take(stored).Error)
	if stored.Status != "error" {
		t.Fatalf("status %s, want error", stored.Status)
	}
	var left int64
	must(t, f.db.Model(&model.StorageReservation{}).Where("volume_id = ?", vol.ID).Count(&left).Error)
	if left != 0 {
		t.Fatalf("%d reservations left, want none", left)
	}
}
