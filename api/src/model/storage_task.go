/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"time"

	"api/src/dbs"

	"gorm.io/gorm"
)

// Storage tasks: one operation on a storage cluster split into steps, each run on some hosts
// (shared-storage-design.md §6.2)

const (
	StorageTaskRunning   = "running"
	StorageTaskFailed    = "failed"
	StorageTaskAborting  = "aborting" // abort asked, waiting for the runs still going on hosts to end
	StorageTaskSucceeded = "succeeded"
	StorageTaskAborted   = "aborted"

	StorageStepPending   = "pending"
	StorageStepRunning   = "running"
	StorageStepFailed    = "failed"
	StorageStepSucceeded = "succeeded"

	StorageStepScopeNodes = "nodes"
	StorageStepScopeAdmin = "admin"

	StorageRunDispatched = "dispatched"
	StorageRunRunning    = "running"
	StorageRunFailed     = "failed"
	StorageRunSucceeded  = "succeeded"
)

type StorageTask struct {
	Model
	ClusterID int64  `gorm:"index"` // 0 for a precheck that is not tied to a cluster
	Kind      string `gorm:"type:varchar(32)"`
	// Kind of storage the task works on: its steps are the ones that backend registered for Kind
	// (shared-storage-design.md §4.5.1); empty for the tasks every kind shares (precheck, selftest)
	Backend     string `gorm:"type:varchar(16)"`
	Status      string `gorm:"type:varchar(16);index"`
	Params      string `gorm:"type:text"` // json, what the task was asked to do
	CurrentStep int32
	CreatorName string `gorm:"type:varchar(64)"` // user name snapshot: clapi can not resolve user IDs
	Message     string `gorm:"type:varchar(1024)"`
	FinishedAt  *time.Time
}

type StorageTaskStep struct {
	ID         int64  `gorm:"primaryKey"`
	TaskID     int64  `gorm:"index"`
	Seq        int32  // order of the step in the task, from 1
	Name       string `gorm:"type:varchar(64)"`
	Scope      string `gorm:"type:varchar(16)"`   // nodes | admin
	Hostids    string `gorm:"type:varchar(2048)"` // json array: hosts of a nodes step, candidate hosts of an admin step
	Status     string `gorm:"type:varchar(16)"`
	TimeoutSec int32  // 0 = no limit
	StartedAt  *time.Time
	FinishedAt *time.Time
}

type StorageTaskRun struct {
	ID      int64  `gorm:"primaryKey"`
	StepID  int64  `gorm:"index"`
	Hostid  int32  // host the run was sent to
	Attempt int32  // 1 for the first run of the step on the host
	Status  string `gorm:"type:varchar(16);index"`
	// How many times the command was sent: a run the host has no trace of is sent again (§6.2.2)
	Dispatches int32
	Progress   int32  // 0-100
	Message    string `gorm:"type:varchar(1024)"`
	LogTail    string `gorm:"type:text"`
	Result     string `gorm:"type:text"` // json output of the step, read by later steps
	StartedAt  time.Time
	UpdatedAt  time.Time
	// When the run was last polled, so the poller does not ask a host again before it answered
	PolledAt *time.Time
}

// StorageRunLog is the whole log of a run, fetched from its host on request (shared-storage-design.md §6.2.6): the
// callback of a run only carries the last 64 KiB
type StorageRunLog struct {
	ID          int64  `gorm:"primaryKey"`
	RunID       int64  `gorm:"uniqueIndex"`
	Status      string `gorm:"type:varchar(16)"` // requested | ready | error
	Message     string `gorm:"type:varchar(512)"`
	Content     string `gorm:"type:text"`
	Size        int64  // size of the log on the host; larger than the content when only its end was kept
	RequestedAt time.Time
	UpdatedAt   time.Time
}

const (
	StorageRunLogRequested = "requested"
	StorageRunLogReady     = "ready"
	StorageRunLogError     = "error"
)

func init() {
	dbs.AutoMigrate(&StorageTask{}, &StorageTaskStep{}, &StorageTaskRun{}, &StorageRunLog{})
	dbs.AutoUpgrade("storage_task_unique_indexes_v1", func(db *gorm.DB) error {
		for _, stmt := range []string{
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_task_step ON storage_task_steps (task_id, seq)",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_task_run ON storage_task_runs (step_id, hostid, attempt)",
		} {
			if err := db.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
