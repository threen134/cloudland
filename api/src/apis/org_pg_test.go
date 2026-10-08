/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// The org sync endpoints and the X-Org-UUID resolution of Authorize, against PostgreSQL:
//
//	CLAPI_TEST_DB_URI="postgres://user:pass@127.0.0.1:5432/testdb?sslmode=disable" go test ./src/apis -run PG
//
// Skipped without CLAPI_TEST_DB_URI. The test uses its own UUIDs and slugs and can share the database.

import (
	"context"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var apiPGOnce sync.Once

func apiPGDB(t *testing.T) *gorm.DB {
	t.Helper()
	uri := os.Getenv("CLAPI_TEST_DB_URI")
	if uri == "" {
		t.Skip("CLAPI_TEST_DB_URI is not set: this test needs PostgreSQL")
	}
	apiPGOnce.Do(func() {
		dbs.OpenDB = func() *gorm.DB {
			db, err := gorm.Open(postgres.Open(uri), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: gormlogger.Discard})
			if err != nil {
				panic(err)
			}
			return db
		}
	})
	return dbs.DB()
}

// FAIL-1 / FAIL-2 / D1 end to end: what CPGateway pushes and how Authorize resolves X-Org-UUID afterwards
func TestOrgSyncAndAuthorizePG(t *testing.T) {
	db := apiPGDB(t)
	gin.SetMode(gin.TestMode)
	viper.Set("cpgateway.secret", "pg-secret")
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(Authorize())
	g.POST("/internal/orgs/sync", SyncOrg)
	g.POST("/internal/orgs/delete", DeleteOrg)
	g.GET("/whoami", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"org_id": GetMemberShip(c).OrgID}) })

	call := func(method, path, orgUUID string, body interface{}) (int, map[string]interface{}) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-Secret", "pg-secret")
		if orgUUID != "" {
			req.Header.Set("X-Org-UUID", orgUUID)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := map[string]interface{}{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	localID := func(u string) int64 {
		t.Helper()
		org := &model.Organization{}
		if err := db.Where("uuid = ?", u).Take(org).Error; err != nil {
			t.Fatalf("org %s not stored: %v", u, err)
		}
		return org.ID
	}
	whoami := func(u string) (int, float64) {
		code, out := call("GET", "/api/v1/whoami", u, nil)
		id, _ := out["org_id"].(float64)
		return code, id
	}

	slug := fmt.Sprintf("pg-api-%d", time.Now().UnixNano())
	orgUUID := uuid.NewString()
	if code, out := call("POST", "/api/v1/internal/orgs/sync", "", gin.H{"uuid": orgUUID, "name": "pg api", "slug": slug, "org_type": 1}); code != http.StatusOK || out["status"] != "ok" {
		t.Fatalf("sync: %d %v", code, out)
	}
	id := localID(orgUUID)
	// A new org resolves to its own row right away, not to 0 (FAIL-1)
	if code, got := whoami(orgUUID); code != http.StatusOK || int64(got) != id || id <= 0 {
		t.Fatalf("whoami: %d org_id=%v, want %d", code, got, id)
	}

	// Resources keep the org in the region: 409, still resolvable
	task := &model.Task{Owner: id, Name: "pg-api-task"}
	if err := db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if code, out := call("POST", "/api/v1/internal/orgs/delete", "", gin.H{"uuid": orgUUID}); code != http.StatusConflict || out["error_code"] != float64(ErrOrgHasResources) {
		t.Fatalf("delete with resources: %d %v", code, out)
	}
	if code, _ := whoami(orgUUID); code != http.StatusOK {
		t.Fatalf("org with resources must still resolve: %d", code)
	}
	db.Unscoped().Delete(task)

	// Deleted: the UUID no longer resolves, and a stale sync does not bring it back
	if code, out := call("POST", "/api/v1/internal/orgs/delete", "", gin.H{"uuid": orgUUID}); code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, out)
	}
	if code, out := call("GET", "/api/v1/whoami", orgUUID, nil); code != http.StatusNotFound || out["error_code"] != float64(ErrOrgNotFound) {
		t.Fatalf("deleted org: %d %v", code, out)
	}
	if code, out := call("POST", "/api/v1/internal/orgs/sync", "", gin.H{"uuid": orgUUID, "name": "pg api", "slug": slug}); code != http.StatusOK || out["status"] != "deleted" {
		t.Fatalf("stale sync: %d %v", code, out)
	}
	if code, _ := whoami(orgUUID); code != http.StatusNotFound {
		t.Fatalf("stale sync recreated the org: %d", code)
	}

	// A new org with the same slug gets its own row (D1)
	newUUID := uuid.NewString()
	if code, out := call("POST", "/api/v1/internal/orgs/sync", "", gin.H{"uuid": newUUID, "name": "pg api 2", "slug": slug}); code != http.StatusOK {
		t.Fatalf("sync new org: %d %v", code, out)
	}
	if code, got := whoami(newUUID); code != http.StatusOK || int64(got) == id || got <= 0 {
		t.Fatalf("new org resolved to %v (%d), old org was %d", got, code, id)
	}

	// An org this region never received: the deletion leaves a tombstone and its late creation push is ignored
	ghost := uuid.NewString()
	if code, out := call("POST", "/api/v1/internal/orgs/delete", "", gin.H{"uuid": ghost, "name": "pg ghost"}); code != http.StatusOK {
		t.Fatalf("delete unknown org: %d %v", code, out)
	}
	if code, out := call("POST", "/api/v1/internal/orgs/sync", "", gin.H{"uuid": ghost, "name": "pg ghost", "slug": slug + "-ghost"}); code != http.StatusOK || out["status"] != "deleted" {
		t.Fatalf("late sync of a deleted org: %d %v", code, out)
	}
	if code, _ := whoami(ghost); code != http.StatusNotFound {
		t.Fatalf("tombstoned org resolves: %d", code)
	}

	// Bad requests and a wrong secret
	if code, _ := call("POST", "/api/v1/internal/orgs/delete", "", gin.H{}); code != http.StatusBadRequest {
		t.Fatalf("delete without uuid: %d", code)
	}
	req := httptest.NewRequest("POST", "/api/v1/internal/orgs/delete", bytes.NewReader([]byte(`{"uuid":"`+newUUID+`"}`)))
	req.Header.Set("X-Forwarded-Secret", "wrong")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong secret: %d", w.Code)
	}
}

// The audit snapshot of a resource name must not show another organization's resource in the caller's activity
// feed: handlers refuse it with 400, 403 or 404, so the lookup itself checks who may see the resource
func TestAuditNameOnlyForVisibleResourcesPG(t *testing.T) {
	db := apiPGDB(t)
	orgA, orgB := int64(910001), int64(910002)
	suffix := time.Now().UnixNano()
	key := &model.Key{Model: model.Model{UUID: uuid.New().String()}, Owner: orgA, Name: fmt.Sprintf("audit-key-%d", suffix)}
	if err := db.Create(key).Error; err != nil {
		t.Fatalf("create key: %v", err)
	}
	public := &model.Image{Model: model.Model{UUID: uuid.New().String()}, Owner: orgA, Name: fmt.Sprintf("audit-pub-%d", suffix), Visibility: model.ImageVisibilityPublic}
	private := &model.Image{Model: model.Model{UUID: uuid.New().String()}, Owner: orgA, Name: fmt.Sprintf("audit-priv-%d", suffix), Visibility: model.ImageVisibilityPrivate}
	for _, img := range []*model.Image{public, private} {
		if err := db.Create(img).Error; err != nil {
			t.Fatalf("create image: %v", err)
		}
	}
	as := func(m *MemberShip) context.Context {
		return SetContextDB(context.WithValue(context.Background(), "membership", m), db)
	}
	memberA, memberB := as(&MemberShip{OrgID: orgA}), as(&MemberShip{OrgID: orgB})
	admin := as(&MemberShip{OrgID: orgB, SystemRole: model.SystemAdmin})
	cases := []struct {
		ctx      context.Context
		resource string
		uuid     string
		want     string
	}{
		{memberA, "key", key.UUID, key.Name},
		{memberB, "key", key.UUID, ""},
		{admin, "key", key.UUID, key.Name},
		{memberB, "image", public.UUID, public.Name},
		{memberB, "image", private.UUID, ""},
		{memberA, "image", private.UUID, private.Name},
	}
	for i, c := range cases {
		if got := lookupResourceName(c.ctx, c.resource, c.uuid); got != c.want {
			t.Errorf("case %d (%s): got %q, want %q", i, c.resource, got, c.want)
		}
	}
	db.Unscoped().Where("uuid = ?", key.UUID).Delete(&model.Key{})
	db.Unscoped().Where("uuid IN ?", []string{public.UUID, private.UUID}).Delete(&model.Image{})
}
