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

// StoragePendingCleanup is what a host still has to remove of a storage cluster deleted while it was offline
// (shared-storage-design.md §7.6, §8.6): the deletion went on without it, and the leave it missed runs on it once it
// is back. The input of that leave is made when the cluster is deleted, from records that go with the cluster. One
// live row per host and cluster; it goes once the leave succeeded, or with the host
type StoragePendingCleanup struct {
	Model
	Hostid      int32  `gorm:"index"`
	Kind        string `gorm:"type:varchar(32)"`
	ClusterUUID string `gorm:"type:varchar(64)"`
	ClusterName string `gorm:"type:varchar(255)"`
	// The step the host missed: leave (a managed cluster) or forget (an imported one), and the input of its script
	Step  string `gorm:"type:varchar(32)"`
	Input string `gorm:"type:text"`
	// The cleanup task running or last run, 0 before the first
	TaskID   int64
	Attempts int32
	TriedAt  *time.Time
	Message  string `gorm:"type:varchar(512)"`
}

func init() {
	dbs.AutoMigrate(&StoragePendingCleanup{})
	dbs.AutoUpgrade("storage_pending_cleanups_live_unique", func(db *gorm.DB) error {
		return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_storage_cleanup_live ON storage_pending_cleanups (hostid, cluster_uuid) WHERE deleted_at IS NULL`).Error
	})
}
