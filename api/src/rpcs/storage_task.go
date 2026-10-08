/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"fmt"
	"strconv"

	"api/src/services"
)

func init() {
	Add("storage_task_run", StorageTaskRun)
	Add("storage_health", StorageHealth)
	Add("storage_run_log", StorageRunLog)
}

// StorageTaskRun takes the report of a storage task run (shared-storage-design.md §6.2.4):
//
//	|:-COMMAND-:| storage_task_run '<run ID>' '<running|succeeded|failed|missing>' '<progress>' '<base64 json>'
func StorageTaskRun(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	runID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid run ID", err)
		return
	}
	progress, _ := strconv.ParseInt(args[3], 10, 32)
	payload := &services.StorageRunPayload{}
	if len(args) > 4 {
		if err = decodeDetails(args[4], payload); err != nil {
			logger.Ctx(ctx).Errorf("Invalid report of storage run %d: %v", runID, err)
			return
		}
	}
	hostid, ok := ctx.Value("hostid").(int32)
	if !ok {
		err = fmt.Errorf("no host in the report of storage run %d", runID)
		logger.Ctx(ctx).Error(err)
		return
	}
	if err = services.HandleStorageTaskRun(ctx, hostid, runID, args[2], int32(progress), payload); err != nil {
		logger.Ctx(ctx).Errorf("Failed to handle the report of storage run %d: %v", runID, err)
	}
	return
}

// StorageHealth takes the health report of a storage cluster from the host that checked it (shared-storage-design.md
// §14.1):
//
//	|:-COMMAND-:| storage_health '<cluster UUID>' '<base64 json>'
func StorageHealth(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	hostid, ok := ctx.Value("hostid").(int32)
	if !ok {
		err = fmt.Errorf("no host in the health report of storage cluster %s", args[1])
		logger.Ctx(ctx).Error(err)
		return
	}
	report := &services.StorageHealthReport{}
	if err = decodeDetails(args[2], report); err != nil {
		logger.Ctx(ctx).Errorf("Invalid health report of storage cluster %s from host %d: %v", args[1], hostid, err)
		return
	}
	if err = services.HandleStorageHealth(ctx, hostid, args[1], report); err != nil {
		logger.Ctx(ctx).Errorf("Failed to handle the health report of storage cluster %s: %v", args[1], err)
	}
	return
}

// StorageRunLog takes why a host could not upload the whole log of a run (stc_upload_log.sh, §6.2.6); a log that
// was uploaded comes through /internal/storage_runs/<ID>/log instead:
//
//	|:-COMMAND-:| storage_run_log '<run ID>' 'error' '<message>'
func StorageRunLog(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	runID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid run ID", err)
		return
	}
	hostid, ok := ctx.Value("hostid").(int32)
	if !ok {
		err = fmt.Errorf("no host in the log report of run %d", runID)
		logger.Ctx(ctx).Error(err)
		return
	}
	message := ""
	if len(args) > 3 {
		message = args[3]
	}
	if err = services.StorageRunLogFailed(ctx, hostid, runID, message); err != nil {
		logger.Ctx(ctx).Errorf("Failed to record the log report of run %d: %v", runID, err)
	}
	return
}
