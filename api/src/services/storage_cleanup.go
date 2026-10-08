/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Cleaning up after a storage cluster deleted while some of its hosts were offline (shared-storage-design.md §7.6,
// §8.6). The deletion goes on without them: the leave step runs on the hosts online when it starts, and for each host
// it skipped the input its leave would have had is recorded, made before the records of the cluster go. Once such a
// host is online again a task of no cluster runs that leave on it, again a while later when it fails. Until it
// succeeded the host joins no storage cluster: the leave stops GPFS on it and wipes the disks the deleted cluster had
// claimed

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StorageTaskCleanup = "cleanup"
	// A cleanup that failed is tried again this long after its last try, twice as long after each failure, up to
	// storageCleanupRetryMax
	storageCleanupRetry    = 10 * time.Minute
	storageCleanupRetryMax = 6 * time.Hour
	storageCleanupTimeout  = 30 * time.Minute
)

type storageCleanupParams struct {
	CleanupID int64  `json:"cleanup_id"`
	Hostid    int32  `json:"hostid"`
	Kind      string `json:"kind"`
	Cluster   string `json:"cluster"`
}

func init() {
	def := func(script string) *storageStepDef {
		return &storageStepDef{Script: script, Input: storageCleanupInput}
	}
	registerStorageTaskKind(StorageTaskCleanup, &storageTaskKind{
		Slot:   storageSlotNone,
		Steps:  map[string]*storageStepDef{"leave": def("stc_leave.sh"), "forget": def("stc_forget.sh")},
		Finish: storageCleanupFinish,
	})
}

// storageRecordSkippedHosts records what the hosts the leave (or forget) step of a cluster deletion skipped still have
// to do. Called by the finish of the deletion, before the records of the cluster go
func storageRecordSkippedHosts(ctx context.Context, tx *gorm.DB, task *model.StorageTask, cluster *model.StorageCluster) error {
	kind := storageTaskKindOf(task)
	if kind == nil {
		return nil
	}
	steps := []*model.StorageTaskStep{}
	if err := tx.Where("task_id = ? AND name IN ?", task.ID, []string{"leave", "forget"}).Order("seq").Find(&steps).Error; err != nil {
		return err
	}
	for _, step := range steps {
		def := kind.Steps[step.Name]
		if def == nil || def.Input == nil {
			continue
		}
		runs, err := latestStorageRuns(tx, step.ID)
		if err != nil {
			return err
		}
		done := map[int32]bool{}
		for _, r := range runs {
			done[r.Hostid] = r.Status == model.StorageRunSucceeded
		}
		for _, h := range parseHostids(step.Hostids) {
			if done[h] {
				continue
			}
			in, err := def.Input(ctx, tx, task, step, h)
			if err != nil {
				return fmt.Errorf("the %s input of host %d: %w", step.Name, h, err)
			}
			raw, err := json.Marshal(in)
			if err != nil {
				return err
			}
			row := &model.StoragePendingCleanup{Hostid: h, Kind: cluster.Kind, ClusterUUID: cluster.UUID, ClusterName: cluster.Name,
				Step: step.Name, Input: string(raw)}
			if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
				return err
			}
			logger.Ctx(ctx).Infof("Host %d was offline when storage cluster %s was deleted: it runs the %s it missed once it is back", h, cluster.Name, step.Name)
		}
	}
	return nil
}

func storageCleanupOf(db *gorm.DB, task *model.StorageTask) (*model.StoragePendingCleanup, *storageCleanupParams, error) {
	p := &storageCleanupParams{}
	if err := json.Unmarshal([]byte(task.Params), p); err != nil {
		return nil, nil, err
	}
	row := &model.StoragePendingCleanup{}
	if err := db.Take(row, p.CleanupID).Error; err != nil {
		return nil, p, err
	}
	return row, p, nil
}

// storageCleanupInput is the input recorded when the cluster was deleted, less the disks claimed again since: a disk
// another storage cluster took is not the deleted cluster's to wipe any more (the host joins none while its cleanup
// waits, so only after the wipe list was made by hand)
func storageCleanupInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	row, _, err := storageCleanupOf(db, task)
	if err != nil {
		return nil, fmt.Errorf("the cleanup of task %d: %w", task.ID, err)
	}
	in := map[string]interface{}{}
	if err = json.Unmarshal([]byte(row.Input), &in); err != nil {
		return nil, err
	}
	if wipe, ok := in["wipe"].([]interface{}); ok {
		keep := []interface{}{}
		for _, w := range wipe {
			id, _ := w.(map[string]interface{})["id"].(string)
			var n int64
			db.Model(&model.StorageClusterDisk{}).Where("hostid = ? AND disk_id = ?", hostid, id).Count(&n)
			if n == 0 {
				keep = append(keep, w)
			}
		}
		in["wipe"] = keep
	}
	return in, nil
}

// storageCleanupFinish removes the record of a cleanup that succeeded; an aborted one is tried again later
func storageCleanupFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	row, _, err := storageCleanupOf(tx, task)
	if err != nil {
		// Gone with its host
		return nil
	}
	if succeeded {
		return tx.Delete(row).Error
	}
	return nil
}

func storageCleanupDelay(attempts int32) time.Duration {
	d := storageCleanupRetry
	for i := int32(1); i < attempts && d < storageCleanupRetryMax; i++ {
		d *= 2
	}
	if d > storageCleanupRetryMax {
		d = storageCleanupRetryMax
	}
	return d
}

// maintainStorageCleanups starts the cleanups of the hosts online again, follows their tasks (a failed one is
// recorded and aborted, to be tried again later) and drops the records of cleanups whose task succeeded
func maintainStorageCleanups(ctx context.Context) {
	db := dbs.DBContext(ctx)
	rows := []*model.StoragePendingCleanup{}
	if err := db.Order("id").Find(&rows).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to list the pending storage cleanups: %v", err)
		return
	}
	admin := storageAutoAdmin(ctx)
	for _, row := range rows {
		if row.TaskID != 0 {
			task := &model.StorageTask{}
			if db.Take(task, row.TaskID).Error == nil {
				switch task.Status {
				case model.StorageTaskRunning, model.StorageTaskAborting:
					continue
				case model.StorageTaskSucceeded:
					db.Delete(row)
					continue
				case model.StorageTaskFailed:
					msg := task.Message
					if msg == "" {
						msg = "the cleanup task failed"
					}
					db.Model(row).Update("message", truncate(msg, 512))
					if err := AbortStorageTask(admin, task.ID); err != nil {
						logger.Ctx(ctx).Warningf("Failed to abort the failed cleanup task %d: %v", task.ID, err)
					}
					continue
				}
			}
		}
		if _, online := hostOnline(db, row.Hostid); !online {
			continue
		}
		if row.TriedAt != nil && time.Since(*row.TriedAt) < storageCleanupDelay(row.Attempts) {
			continue
		}
		startStorageCleanup(ctx, admin, row)
	}
}

func startStorageCleanup(ctx, admin context.Context, row *model.StoragePendingCleanup) {
	db := dbs.DBContext(ctx)
	now := time.Now()
	// Claimed by the attempt it makes: a request racing the worker starts one task, not two
	claim := db.Model(&model.StoragePendingCleanup{}).Where("id = ? AND task_id = ? AND attempts = ?", row.ID, row.TaskID, row.Attempts).
		Updates(map[string]interface{}{"attempts": row.Attempts + 1, "tried_at": &now})
	if claim.Error != nil || claim.RowsAffected == 0 {
		return
	}
	task, err := startStorageTask(admin, &storageTaskSpec{Kind: StorageTaskCleanup,
		Params: &storageCleanupParams{CleanupID: row.ID, Hostid: row.Hostid, Kind: row.Kind, Cluster: row.ClusterName},
		Plan: []*StorageStepPlan{{Name: row.Step, Scope: model.StorageStepScopeNodes, Hostids: []int32{row.Hostid},
			Timeout: storageCleanupTimeout}}})
	if err != nil {
		logger.Ctx(ctx).Warningf("Failed to start the cleanup of host %d for deleted storage cluster %s: %v", row.Hostid, row.ClusterName, err)
		db.Model(row).Update("message", truncate(err.Error(), 512))
		return
	}
	db.Model(row).Updates(map[string]interface{}{"task_id": task.ID, "message": ""})
}

// storagePendingCleanupOf is the cleanup a host still waits for, nil when it has none
func storagePendingCleanupOf(db *gorm.DB, hostid int32) *model.StoragePendingCleanup {
	row := &model.StoragePendingCleanup{}
	if db.Where("hostid = ?", hostid).Order("id").Take(row).Error != nil {
		return nil
	}
	return row
}

// HostStorageCleanup is a cleanup a host waits for, as the hypervisor detail shows it
type HostStorageCleanup struct {
	ID          int64      `json:"id"`
	Cluster     string     `json:"cluster"`
	ClusterUUID string     `json:"cluster_uuid"`
	Kind        string     `json:"kind"`
	Attempts    int32      `json:"attempts"`
	TriedAt     *time.Time `json:"tried_at,omitempty"`
	Message     string     `json:"message,omitempty"`
	TaskID      string     `json:"task_id,omitempty"`
}

// HostStorageCleanups lists the cleanups a host waits for
func HostStorageCleanups(ctx context.Context, hostid int32) (list []*HostStorageCleanup, err error) {
	db := dbs.DBContext(ctx)
	rows := []*model.StoragePendingCleanup{}
	if err = db.Where("hostid = ?", hostid).Order("id").Find(&rows).Error; err != nil {
		return
	}
	list = []*HostStorageCleanup{}
	for _, r := range rows {
		c := &HostStorageCleanup{ID: r.ID, Cluster: r.ClusterName, ClusterUUID: r.ClusterUUID, Kind: r.Kind, Attempts: r.Attempts,
			TriedAt: r.TriedAt, Message: r.Message}
		if r.TaskID != 0 {
			task := &model.StorageTask{}
			if db.Take(task, r.TaskID).Error == nil {
				c.TaskID = task.UUID
			}
		}
		list = append(list, c)
	}
	return
}
