/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// The upload of a run log by a host and the answer to the console, against PostgreSQL (CLAPI_TEST_DB_URI)
// (shared-storage-design.md §6.2.6): only the token of that run is taken, a log over 16 MiB is refused, and the
// console gets 200 with the log once it is there

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func TestStorageRunLogUploadPG(t *testing.T) {
	db := apiPGDB(t)
	gin.SetMode(gin.TestMode)
	viper.Set("cpgateway.secret", "pg-secret")
	viper.Set("capture.upload_secret", "pg-log-secret")
	defer viper.Set("capture.upload_secret", "")
	task := &model.StorageTask{Kind: "selftest", Status: model.StorageTaskSucceeded}
	if err := db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	step := &model.StorageTaskStep{TaskID: task.ID, Seq: 1, Name: "echo", Status: model.StorageStepSucceeded}
	db.Create(step)
	run := &model.StorageTaskRun{StepID: step.ID, Hostid: 1, Attempt: 1, Status: model.StorageRunSucceeded, StartedAt: time.Now(), UpdatedAt: time.Now().Add(-time.Minute)}
	db.Create(run)
	defer func() {
		db.Where("run_id = ?", run.ID).Delete(&model.StorageRunLog{})
		db.Delete(run)
		db.Delete(step)
		db.Unscoped().Delete(task)
	}()

	r := gin.New()
	r.POST("/api/v1/internal/storage_runs/:id/log", storageClusterAPI.UploadRunLog)
	g := r.Group("/api/v1")
	g.Use(Authorize())
	g.GET("/storage_tasks/:id/runs/:run/log", storageClusterAPI.RunLog)
	expiry := time.Now().Add(time.Hour).Unix()
	mac := func(purpose string, id, exp int64) string {
		m := hmac.New(sha256.New, []byte("pg-log-secret"))
		fmt.Fprintf(m, "%s%d|%d", purpose, id, exp)
		return hex.EncodeToString(m.Sum(nil))
	}
	post := func(id int64, token string, body []byte) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/internal/storage_runs/%d/log?expiry=%d&size=%d", id, expiry, len(body)+5), bytes.NewReader(body))
		req.Header.Set("X-Log-Token", token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := post(run.ID, mac("", run.ID, expiry), []byte("x")); code != http.StatusUnauthorized {
		t.Fatalf("a capture style token: %d", code)
	}
	if code := post(run.ID+1, mac("log|", run.ID, expiry), []byte("x")); code != http.StatusUnauthorized {
		t.Fatalf("the token of another run: %d", code)
	}
	if code := post(run.ID, mac("log|", run.ID, expiry), make([]byte, services.StorageRunLogMax+1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a log over the limit: %d", code)
	}
	if code := post(run.ID, mac("log|", run.ID, expiry), []byte("hello\n")); code != http.StatusOK {
		t.Fatalf("upload: %d", code)
	}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/storage_tasks/%s/runs/%d/log", task.UUID, run.ID), nil)
	req.Header.Set("X-Forwarded-Secret", "pg-secret")
	req.Header.Set("X-System-Role", fmt.Sprint(int(model.SystemAdmin)))
	req.Header.Set("X-User-Name", "pg-admin")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := &StorageRunLogResponse{}
	json.Unmarshal(w.Body.Bytes(), out)
	if w.Code != http.StatusOK || out.Status != "ready" || out.Content != "hello\n" || out.Size != 11 || !out.Truncated {
		t.Fatalf("the log for the console: %d %+v", w.Code, out)
	}
}
