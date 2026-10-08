/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The whole log of a storage task run (shared-storage-design.md §6.2.6). The callback of a run only carries the last
// 64 KiB of its log; the whole of it stays on the host (log/storage/run-<ID>.log). On request clapi sends
// stc_upload_log.sh to that host, which posts the log to /internal/storage_runs/<ID>/log with a token for that run
// only, and clapi keeps it in the database until the next request. The token is the HMAC of the capture uploads with
// a purpose in front ("log|<run>|<expiry>"), so it can not stand for an image upload, nor an image token for it.
//
// Not in S3 as the design first had it: the S3 endpoint is the internal MinIO, which a browser does not reach, and
// the logs of the runs are small (the largest seen on the test hosts is 12 KiB), so they fit the database.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/spf13/viper"
	"gorm.io/gorm/clause"
)

const (
	// StorageRunLogMax is the most of a log kept: its end, when it is longer
	StorageRunLogMax = 16 << 20
	// A request the host did not answer in this time is sent again
	storageRunLogResend = 2 * time.Minute
	// The log of a run still running is fetched again when the copy is older than this
	storageRunLogRefresh  = 30 * time.Second
	storageRunLogTokenTTL = time.Hour
)

func storageRunLogMAC(runID, expiry int64) string {
	mac := hmac.New(sha256.New, []byte(viper.GetString("capture.upload_secret")))
	fmt.Fprintf(mac, "log|%d|%d", runID, expiry)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyStorageRunLogToken checks the token of a log upload and that it did not expire
func VerifyStorageRunLogToken(token string, runID, expiry int64) bool {
	if time.Now().Unix() > expiry || viper.GetString("capture.upload_secret") == "" {
		return false
	}
	return hmac.Equal([]byte(token), []byte(storageRunLogMAC(runID, expiry)))
}

// storageTaskRun finds a run of a task
func storageTaskRun(ctx context.Context, taskUUID string, runID int64) (*model.StorageTaskRun, error) {
	db := dbs.DBContext(ctx)
	task := &model.StorageTask{}
	if err := db.Where("uuid = ?", taskUUID).Take(task).Error; err != nil {
		return nil, NewCLError(ErrStorageTaskNotFound, "Storage task not found", err)
	}
	run := &model.StorageTaskRun{}
	err := db.Where("id = ? AND step_id IN (?)", runID, db.Model(&model.StorageTaskStep{}).Select("id").Where("task_id = ?", task.ID)).Take(run).Error
	if err != nil {
		return nil, NewCLError(ErrStorageTaskNotFound, "No such run in this task", err)
	}
	return run, nil
}

// RunLog returns the whole log of a run as far as clapi has it, and asks the host for it when clapi has none, or
// only a copy from before the run ended (or older than 30 s while it runs), or the last request went unanswered
func (a *StorageClusterAdmin) RunLog(ctx context.Context, taskUUID string, runID int64) (*model.StorageRunLog, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	run, err := storageTaskRun(ctx, taskUUID, runID)
	if err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	log := &model.StorageRunLog{}
	found := db.Where("run_id = ?", run.ID).Take(log).Error == nil
	now := time.Now()
	running := run.Status == model.StorageRunDispatched || run.Status == model.StorageRunRunning
	if found {
		switch log.Status {
		case model.StorageRunLogReady:
			if !log.UpdatedAt.Before(run.UpdatedAt) && (!running || now.Sub(log.UpdatedAt) < storageRunLogRefresh) {
				return log, nil
			}
		case model.StorageRunLogRequested:
			if now.Sub(log.RequestedAt) < storageRunLogResend {
				return log, nil
			}
		}
	}
	// A copy is better than nothing while the host can not send a newer one
	if _, online := hostOnline(db, run.Hostid); !online {
		if found && log.Content != "" {
			log.Status = model.StorageRunLogReady
			log.Message = fmt.Sprintf("%s is offline: this copy is from %s", hostName(db, run.Hostid), log.UpdatedAt.Format(time.RFC3339))
			return log, nil
		}
		return nil, NewCLError(ErrStorageTaskState, fmt.Sprintf("%s is offline: its log can not be fetched now", hostName(db, run.Hostid)), nil)
	}
	secret := viper.GetString("capture.upload_secret")
	url := strings.TrimRight(viper.GetString("clapi.internal_url"), "/")
	if secret == "" || url == "" {
		return nil, NewCLError(ErrStorageTaskState, "The hosts can not upload logs: CAPTURE_UPLOAD_SECRET or the internal clapi address is not set", nil)
	}
	// Requested until the new copy comes: the console waits for it instead of saving the old one as the whole log
	status := model.StorageRunLogRequested
	row := &model.StorageRunLog{RunID: run.ID, Status: status, RequestedAt: now, UpdatedAt: now}
	if found {
		row.UpdatedAt = log.UpdatedAt
	}
	if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"status": status, "message": "", "requested_at": now})}).Create(row).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the log request", err)
	}
	expiry := now.Add(storageRunLogTokenTTL).Unix()
	uploadURL := fmt.Sprintf("%s/internal/storage_runs/%d/log?expiry=%d", url, run.ID, expiry)
	command := fmt.Sprintf("%s/stc_upload_log.sh '%d' '%s' '%s'", storageScriptDir, run.ID, ShellEscape(uploadURL), ShellEscape(storageRunLogMAC(run.ID, expiry)))
	if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", run.Hostid), command); err != nil {
		return nil, NewCLError(ErrStorageTaskState, "Failed to ask the host for the log", err)
	}
	db.Where("run_id = ?", run.ID).Take(log)
	return log, nil
}

// SaveStorageRunLog keeps the log a host uploaded; size is the size of the log on the host
func SaveStorageRunLog(ctx context.Context, runID int64, content []byte, size int64) error {
	if size < int64(len(content)) {
		size = int64(len(content))
	}
	db := dbs.DBContext(ctx)
	if err := db.Take(&model.StorageTaskRun{}, runID).Error; err != nil {
		return err
	}
	// PostgreSQL text can not hold a NUL byte
	text := strings.ToValidUTF8(strings.ReplaceAll(string(content), "\x00", ""), "�")
	now := time.Now()
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"status": model.StorageRunLogReady, "message": "", "content": text, "size": size, "updated_at": now})}).
		Create(&model.StorageRunLog{RunID: runID, Status: model.StorageRunLogReady, Content: text, Size: size, RequestedAt: now, UpdatedAt: now}).Error
}

// StorageRunLogFailed records why the host could not upload the log of a run; only the host the run was sent to
// speaks for it, and a copy fetched before stays
func StorageRunLogFailed(ctx context.Context, hostid int32, runID int64, message string) error {
	db := dbs.DBContext(ctx)
	run := &model.StorageTaskRun{}
	if err := db.Take(run, runID).Error; err != nil {
		return err
	}
	if run.Hostid != hostid {
		return fmt.Errorf("host %d does not run %d", hostid, runID)
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return db.Model(&model.StorageRunLog{}).Where("run_id = ? AND status = ?", runID, model.StorageRunLogRequested).
		Updates(map[string]interface{}{"status": model.StorageRunLogError, "message": message}).Error
}
