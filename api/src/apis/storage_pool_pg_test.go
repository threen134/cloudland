/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// PATCH /storage_pools/:id against PostgreSQL (CLAPI_TEST_DB_URI): a quota is changed on its own, and a request that
// is refused changes nothing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"api/src/model"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func TestStoragePoolPatchPG(t *testing.T) {
	db := apiPGDB(t)
	gin.SetMode(gin.TestMode)
	viper.Set("cpgateway.secret", "pg-secret")
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(Authorize())
	g.PATCH("/storage_pools/:id", storagePoolAPI.Patch)
	patch := func(id string, body interface{}) (int, map[string]interface{}) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/storage_pools/"+id, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-Secret", "pg-secret")
		req.Header.Set("X-System-Role", fmt.Sprint(int(model.SystemAdmin)))
		req.Header.Set("X-User-Name", "pg-admin")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := map[string]interface{}{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	name := fmt.Sprintf("pg-patch-%d", time.Now().UnixNano()%1000000)
	pool := &model.StoragePool{Name: name, Driver: "local", Media: "hdd", Status: model.StoragePoolActive, OverRatio: 1, Description: "before"}
	if err := db.Create(pool).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Unscoped().Delete(pool)
	unchanged := func(what string) {
		t.Helper()
		got := &model.StoragePool{}
		if err := db.Take(got, pool.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Name != name || got.Description != "before" || got.IsDefault {
			t.Fatalf("%s: the pool changed although the request was refused: %+v", what, got)
		}
	}
	if code, out := patch(pool.UUID, gin.H{"quota_gb": 10}); code != http.StatusBadRequest {
		t.Fatalf("quota of a local pool: %d %v", code, out)
	}
	unchanged("quota of a local pool")
	if code, out := patch(pool.UUID, gin.H{"name": name + "-x", "description": "after", "quota_gb": 10}); code != http.StatusBadRequest {
		t.Fatalf("quota with other fields: %d %v", code, out)
	}
	unchanged("quota with other fields")
	if code, out := patch(pool.UUID, gin.H{"description": "after"}); code != http.StatusOK || out["description"] != "after" {
		t.Fatalf("description: %d %v", code, out)
	}
}
