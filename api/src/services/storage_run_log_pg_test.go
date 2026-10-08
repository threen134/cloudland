/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The whole log of a run against PostgreSQL with the fake cland (shared-storage-design.md §6.2.6): the first request
// asks the host with a token for that run only, a request still waiting is not sent twice, the upload is kept, the
// copy of a run still running is fetched again, a failure is only taken from the host of the run, and an offline host
// is refused.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"api/src/model"

	"github.com/spf13/viper"
)

func TestStorageRunLogPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	viper.Set("capture.upload_secret", "pg-log-secret")
	viper.Set("clapi.internal_url", "https://clapi.cloudland.internal:8255/api/v1")
	defer viper.Set("capture.upload_secret", "")
	task := &model.StorageTask{ClusterID: f.cluster.ID, Kind: "selftest", Status: model.StorageTaskSucceeded}
	must(t, db.Create(task).Error)
	step := &model.StorageTaskStep{TaskID: task.ID, Seq: 1, Name: "echo", Scope: model.StorageStepScopeNodes, Status: model.StorageStepSucceeded}
	must(t, db.Create(step).Error)
	run := &model.StorageTaskRun{StepID: step.ID, Hostid: h[0], Attempt: 1, Status: model.StorageRunSucceeded, StartedAt: time.Now(), UpdatedAt: time.Now().Add(-time.Minute)}
	must(t, db.Create(run).Error)
	defer func() {
		db.Where("run_id = ?", run.ID).Delete(&model.StorageRunLog{})
		db.Delete(run)
		db.Delete(step)
		db.Unscoped().Delete(task)
	}()
	upload := regexp.MustCompile(`^inter=(\d+) \| /opt/cloudland/scripts/backend/storage/stc_upload_log\.sh '(\d+)' 'https://clapi\.cloudland\.internal:8255/api/v1/internal/storage_runs/(\d+)/log\?expiry=(\d+)' '([0-9a-f]{64})'$`)
	sent := func() [][]string {
		out := [][]string{}
		for _, cmd := range f.cland.take() {
			if m := upload.FindStringSubmatch(cmd); m != nil {
				out = append(out, m)
			} else if strings.Contains(cmd, "stc_upload_log") {
				t.Fatalf("upload command: %q", cmd)
			}
		}
		return out
	}
	f.cland.take()

	// A run of another task is not found
	if _, err := StorageClusters.RunLog(ctx, "00000000-0000-0000-0000-000000000000", run.ID); err == nil {
		t.Fatal("a run found under a task that does not hold it")
	}
	// The first request asks the host of the run
	log, err := StorageClusters.RunLog(ctx, task.UUID, run.ID)
	must(t, err)
	cmds := sent()
	if log.Status != model.StorageRunLogRequested || len(cmds) != 1 || cmds[0][1] != strconv.Itoa(int(h[0])) || cmds[0][2] != strconv.FormatInt(run.ID, 10) ||
		cmds[0][3] != strconv.FormatInt(run.ID, 10) {
		t.Fatalf("first request: %+v %v", log, cmds)
	}
	expiry, _ := strconv.ParseInt(cmds[0][4], 10, 64)
	token := cmds[0][5]
	if !VerifyStorageRunLogToken(token, run.ID, expiry) || VerifyStorageRunLogToken(token, run.ID+1, expiry) || VerifyStorageRunLogToken(token, run.ID, expiry+1) {
		t.Fatal("the token is not for this run only")
	}
	// A log token does not stand for an image upload, nor an image token for a log
	if VerifyCaptureToken(token, run.ID, expiry) {
		t.Fatal("a log token passed as a capture token")
	}
	if capture, cexp := GenerateCaptureToken(run.ID); VerifyStorageRunLogToken(capture, run.ID, cexp) {
		t.Fatal("a capture token passed as a log token")
	}
	if VerifyStorageRunLogToken(storageRunLogMAC(run.ID, time.Now().Unix()-1), run.ID, time.Now().Unix()-1) {
		t.Fatal("an expired token passed")
	}
	// Asked again while the host has not answered: nothing more is sent
	log, err = StorageClusters.RunLog(ctx, task.UUID, run.ID)
	must(t, err)
	if log.Status != model.StorageRunLogRequested || len(sent()) != 0 {
		t.Fatalf("second request: %+v", log)
	}
	// The upload, with a NUL byte and a log longer on the host
	must(t, SaveStorageRunLog(ctx, run.ID, []byte("line 1\nline\x002\n"), 1000))
	log, err = StorageClusters.RunLog(ctx, task.UUID, run.ID)
	must(t, err)
	if log.Status != model.StorageRunLogReady || log.Content != "line 1\nline2\n" || log.Size != 1000 || len(sent()) != 0 {
		t.Fatalf("after the upload: %+v", log)
	}
	// The run ran on after the copy: fetched again, and the console waits for the new copy instead of saving the old
	// one as the whole log; asked again meanwhile, nothing more is sent
	must(t, db.Model(&model.StorageTaskRun{}).Where("id = ?", run.ID).Updates(map[string]interface{}{"status": model.StorageRunRunning, "updated_at": time.Now().Add(time.Second)}).Error)
	log, err = StorageClusters.RunLog(ctx, task.UUID, run.ID)
	must(t, err)
	if log.Status != model.StorageRunLogRequested || len(sent()) != 1 {
		t.Fatalf("a run going on: %+v", log)
	}
	if log, err = StorageClusters.RunLog(ctx, task.UUID, run.ID); err != nil || log.Status != model.StorageRunLogRequested || len(sent()) != 0 {
		t.Fatalf("asked again while the host uploads: %+v %v", log, err)
	}
	// A failure: not from another host; from the host of the run it only turns a waiting request into an error
	must(t, db.Model(&model.StorageRunLog{}).Where("run_id = ?", run.ID).Update("status", model.StorageRunLogRequested).Error)
	if err := StorageRunLogFailed(ctx, h[1], run.ID, "no"); err == nil {
		t.Fatal("a failure taken from another host")
	}
	must(t, StorageRunLogFailed(ctx, h[0], run.ID, "the host has no log of this run"))
	got := &model.StorageRunLog{}
	must(t, db.Where("run_id = ?", run.ID).Take(got).Error)
	if got.Status != model.StorageRunLogError || got.Message != "the host has no log of this run" {
		t.Fatalf("failure: %+v", got)
	}
	// An offline host: the copy there is, nothing sent; without any copy, refused
	f.setHostStatus(h[0], 10)
	defer f.setHostStatus(h[0], 1)
	log, err = StorageClusters.RunLog(ctx, task.UUID, run.ID)
	if err != nil || log.Status != model.StorageRunLogReady || log.Content != "line 1\nline2\n" || !strings.Contains(log.Message, "offline") || len(sent()) != 0 {
		t.Fatalf("offline host with a copy: %+v %v", log, err)
	}
	db.Where("run_id = ?", run.ID).Delete(&model.StorageRunLog{})
	if _, err := StorageClusters.RunLog(ctx, task.UUID, run.ID); err == nil || !strings.Contains(err.Error(), "offline") || len(sent()) != 0 {
		t.Fatalf("offline host without a copy: %v", err)
	}
	// Without the upload secret the hosts can not upload
	f.setHostStatus(h[0], 1)
	viper.Set("capture.upload_secret", "")
	if _, err := StorageClusters.RunLog(ctx, task.UUID, run.ID); err == nil || !strings.Contains(err.Error(), "CAPTURE_UPLOAD_SECRET") {
		t.Fatalf("no secret: %v", err)
	}
}
