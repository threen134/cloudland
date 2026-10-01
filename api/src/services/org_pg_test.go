/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Org sync tests that need PostgreSQL (CLAPI_TEST_DB_URI, see placement_pg_test.go): the UUID -> local ID
// mapping, the deletion pushed by CPGateway and the slug reuse that used to hand a deleted org's resources to
// a new org. Every test uses its own UUIDs and slugs, so they share the database with the other PG tests.

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func pgOrgSlug(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func isCLCode(err error, code ErrCode) bool {
	var clErr *CLError
	return errors.As(err, &clErr) && clErr.Code == code
}

// FAIL-1: a new org was cached as ID 0, so every request of a new org ran as org 0 and new orgs saw each
// other's resources.
func TestUpsertOrgByUUIDCachesRealIDPG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	u := uuid.NewString()
	id, err := orgAdmin.UpsertOrgByUUID(ctx, u, "pg-org", pgOrgSlug("pg-org"), model.OrgTypeTeam)
	must(t, err)
	if id <= 0 {
		t.Fatalf("new org got id %d", id)
	}
	if got, ok := cachedOrgID(u); !ok || got != id {
		t.Fatalf("cache holds %v (present=%v), want %d", got, ok, id)
	}
	orgIDByUUID.Delete(u)
	got, err := orgAdmin.GetOrgIDByUUID(ctx, u)
	must(t, err)
	if got != id {
		t.Fatalf("resolved %d, want %d", got, id)
	}

	// A second sync updates the same row
	again, err := orgAdmin.UpsertOrgByUUID(ctx, u, "pg-org-renamed", pgOrgSlug("pg-org"), model.OrgTypeTeam)
	must(t, err)
	org := &model.Organization{}
	must(t, db.Where("id = ?", id).Take(org).Error)
	if again != id || org.Name != "pg-org-renamed" {
		t.Fatalf("resync: id %d (want %d), name %q", again, id, org.Name)
	}

	// A zero entry, however it got there, is never served
	z := uuid.NewString()
	orgIDByUUID.Store(z, orgIDEntry{expires: time.Now().Add(time.Minute)})
	if _, err := orgAdmin.GetOrgIDByUUID(ctx, z); !isCLCode(err, ErrOrgNotFound) {
		t.Fatalf("zero cache entry: %v", err)
	}
}

// FAIL-3: looking up org 0 took the first row of the table
func TestOrgLookupsIgnoreZeroIDPG(t *testing.T) {
	ctx, _ := pgContext(t, 1)
	_, err := orgAdmin.UpsertOrgByUUID(ctx, uuid.NewString(), "pg-any", pgOrgSlug("pg-any"), model.OrgTypeTeam)
	must(t, err)
	if name := orgAdmin.GetOrgName(ctx, 0); name != "" {
		t.Fatalf("GetOrgName(0) = %q", name)
	}
	if u := orgAdmin.GetOrgUUID(ctx, 0); u != "" {
		t.Fatalf("GetOrgUUID(0) = %q", u)
	}
	if _, err := orgAdmin.Get(ctx, 0); err == nil {
		t.Fatal("Get(0) should fail")
	}
}

// D1: deleting an org and creating a new one with the same slug handed the old org's row (and all of its
// resources) to the new org.
func TestDeletedOrgSlugIsNotAdoptedPG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	slug := pgOrgSlug("pg-reuse")
	oldUUID := uuid.NewString()
	oldID, err := orgAdmin.UpsertOrgByUUID(ctx, oldUUID, "pg-old", slug, model.OrgTypeTeam)
	must(t, err)
	group := &model.PlacementGroup{Owner: oldID, Name: pgOrgSlug("pg-pg"), Policy: model.PlacementPolicySpread}
	must(t, db.Create(group).Error)

	// Resources block the deletion and the org stays resolvable
	if err := orgAdmin.DeleteOrgByUUID(ctx, oldUUID, "pg-old"); !isCLCode(err, ErrOrgHasResources) {
		t.Fatalf("delete with resources: %v", err)
	}
	if got, err := orgAdmin.GetOrgIDByUUID(ctx, oldUUID); err != nil || got != oldID {
		t.Fatalf("org with resources must stay: %d %v", got, err)
	}
	must(t, db.Unscoped().Delete(group).Error)

	must(t, orgAdmin.DeleteOrgByUUID(ctx, oldUUID, "pg-old"))
	if _, err := orgAdmin.GetOrgIDByUUID(ctx, oldUUID); !isCLCode(err, ErrOrgNotFound) {
		t.Fatalf("deleted org still resolves: %v", err)
	}
	// Idempotent, also for an org this region never saw
	must(t, orgAdmin.DeleteOrgByUUID(ctx, oldUUID, "pg-old"))
	must(t, orgAdmin.DeleteOrgByUUID(ctx, uuid.NewString(), ""))
	// A deleted org still names the resources it left behind
	if name := orgAdmin.GetOrgName(ctx, oldID); name != "pg-old" {
		t.Fatalf("deleted org name %q", name)
	}

	// A new org reusing the slug gets its own row
	newUUID := uuid.NewString()
	newID, err := orgAdmin.UpsertOrgByUUID(ctx, newUUID, "pg-new", slug, model.OrgTypeTeam)
	must(t, err)
	if newID <= 0 || newID == oldID {
		t.Fatalf("new org id %d, old %d", newID, oldID)
	}
	old := &model.Organization{}
	must(t, db.Unscoped().Where("id = ?", oldID).Take(old).Error)
	if old.UUID != oldUUID || !old.DeletedAt.Valid {
		t.Fatalf("old row changed: uuid %s deleted %v", old.UUID, old.DeletedAt)
	}

	// A stale push of the deleted UUID does not bring it back
	id, err := orgAdmin.UpsertOrgByUUID(ctx, oldUUID, "pg-old", slug+"-x", model.OrgTypeTeam)
	must(t, err)
	var rows int64
	must(t, db.Unscoped().Model(&model.Organization{}).Where("uuid = ?", oldUUID).Count(&rows).Error)
	if id != 0 || rows != 1 {
		t.Fatalf("deleted org recreated: id %d, rows %d", id, rows)
	}
}

// A row whose org was deleted in the control plane without the deletion reaching this region still holds the
// slug. The new org must get a new row; the stale one only loses its slug.
func TestStaleOrgRowKeepsItsIdentityPG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	slug := pgOrgSlug("pg-stale")
	staleUUID := uuid.NewString()
	staleID, err := orgAdmin.UpsertOrgByUUID(ctx, staleUUID, "pg-stale", slug, model.OrgTypeTeam)
	must(t, err)

	newID, err := orgAdmin.UpsertOrgByUUID(ctx, uuid.NewString(), "pg-fresh", slug, model.OrgTypeTeam)
	must(t, err)
	if newID == staleID {
		t.Fatal("the new org took over the stale row")
	}
	stale := &model.Organization{}
	must(t, db.Where("id = ?", staleID).Take(stale).Error)
	if stale.UUID != staleUUID || stale.Slug != "" {
		t.Fatalf("stale row: uuid %s slug %q", stale.UUID, stale.Slug)
	}
	if got, err := orgAdmin.GetOrgIDByUUID(ctx, staleUUID); err != nil || got != staleID {
		t.Fatalf("stale org resolves to %d %v", got, err)
	}
}

// The control plane's system org is linked to the region's own system org (both sides have exactly one),
// and the system org cannot be deleted through the sync.
func TestSystemOrgIsLinkedPG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	system := &model.Organization{}
	res := db.Where("org_type = ?", model.OrgTypeSystem).Order("id").Limit(1).Find(system)
	must(t, res.Error)
	if res.RowsAffected == 0 {
		system = &model.Organization{Name: "admin", Slug: pgOrgSlug("pg-admin"), OrgType: model.OrgTypeSystem, OwnerUserID: 1}
		must(t, db.Create(system).Error)
	}
	for i := 0; i < 2; i++ {
		u := uuid.NewString()
		id, err := orgAdmin.UpsertOrgByUUID(ctx, u, "admin", system.Slug, model.OrgTypeSystem)
		must(t, err)
		if id != system.ID {
			t.Fatalf("system org linked to %d, want %d", id, system.ID)
		}
		if got := orgAdmin.GetOrgUUID(ctx, system.ID); got != u {
			t.Fatalf("system org uuid %s, want %s", got, u)
		}
		if err := orgAdmin.DeleteOrgByUUID(ctx, u, "admin"); !isCLCode(err, ErrPermissionDenied) {
			t.Fatalf("system org delete: %v", err)
		}
	}
}

// Two pushes of the same new UUID at the same time (a registration push racing a full region sync) must end
// with one live row, both returning its ID.
func TestConcurrentUpsertSameUUIDPG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	for round := 0; round < 5; round++ {
		u := uuid.NewString()
		slug := pgOrgSlug("pg-race")
		var wg sync.WaitGroup
		start := make(chan struct{})
		ids := make([]int64, 2)
		errs := make([]error, 2)
		for i := range ids {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				ids[i], errs[i] = orgAdmin.UpsertOrgByUUID(ctx, u, "pg-race", slug, model.OrgTypeTeam)
			}(i)
		}
		close(start)
		wg.Wait()
		must(t, errs[0])
		must(t, errs[1])
		var live int64
		must(t, db.Model(&model.Organization{}).Where("uuid = ?", u).Count(&live).Error)
		if live != 1 || ids[0] <= 0 || ids[0] != ids[1] {
			t.Fatalf("round %d: %d live rows, ids %v", round, live, ids)
		}
		org := &model.Organization{}
		must(t, db.Where("uuid = ?", u).Take(org).Error)
		if org.Slug != slug {
			t.Fatalf("round %d: slug %q lost", round, org.Slug)
		}
	}
}

// Deleting an org this region never received leaves a tombstone: the late push of its creation is ignored.
func TestDeleteUnknownOrgLeavesTombstonePG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	u := uuid.NewString()
	must(t, orgAdmin.DeleteOrgByUUID(ctx, u, "pg-never-synced"))
	must(t, orgAdmin.DeleteOrgByUUID(ctx, u, "pg-never-synced"))
	var rows []model.Organization
	must(t, db.Unscoped().Where("uuid = ?", u).Find(&rows).Error)
	if len(rows) != 1 || !rows[0].DeletedAt.Valid || rows[0].Slug != "" || rows[0].Name != "pg-never-synced" {
		t.Fatalf("tombstone: %+v", rows)
	}
	id, err := orgAdmin.UpsertOrgByUUID(ctx, u, "pg-never-synced", pgOrgSlug("pg-late"), model.OrgTypeTeam)
	must(t, err)
	if id != 0 {
		t.Fatalf("late push recreated the org as %d", id)
	}
	if _, err := orgAdmin.GetOrgIDByUUID(ctx, u); !isCLCode(err, ErrOrgNotFound) {
		t.Fatalf("tombstoned org resolves: %v", err)
	}
}

// An org deleted while its sync holds the row is not updated (0 rows) and must not be cached.
func TestUpsertOfOrgDeletedMeanwhileIsNotCachedPG(t *testing.T) {
	ctx, db := pgContext(t, 1)
	u := uuid.NewString()
	id, err := orgAdmin.UpsertOrgByUUID(ctx, u, "pg-vanishing", pgOrgSlug("pg-vanishing"), model.OrgTypeTeam)
	must(t, err)
	orgIDByUUID.Delete(u)
	orgRowLocked = func(tx *gorm.DB, org *model.Organization) {
		if org.UUID == u {
			tx.Delete(&model.Organization{}, org.ID)
		}
	}
	t.Cleanup(func() { orgRowLocked = func(*gorm.DB, *model.Organization) {} })
	again, err := orgAdmin.UpsertOrgByUUID(ctx, u, "pg-vanishing", pgOrgSlug("pg-vanishing"), model.OrgTypeTeam)
	must(t, err)
	if again != 0 {
		t.Fatalf("sync of a deleted org returned %d (org was %d)", again, id)
	}
	if got, ok := cachedOrgID(u); ok {
		t.Fatalf("deleted org cached as %d", got)
	}
	var live int64
	must(t, db.Model(&model.Organization{}).Where("uuid = ?", u).Count(&live).Error)
	if live != 0 {
		t.Fatalf("%d live rows", live)
	}
}
