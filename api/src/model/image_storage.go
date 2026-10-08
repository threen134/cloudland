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

// Base copies of images in shared pools (shared-storage-design.md §5.7, §9.6): the boot disks of a pool are clones
// (GPFS mmclone, Ceph RBD clone) or full copies of the copy of their image in that pool, imported once by one host
const (
	ImageStorageSyncing  = "syncing"  // a host imports it
	ImageStorageSynced   = "synced"   // ready to clone from
	ImageStorageError    = "error"    // the import failed: the next use imports it again
	ImageStorageDeleting = "deleting" // its image is deleted: removed once no boot disk is cloned from it
)

// ImageStorage is the copy of an image in a shared pool
type ImageStorage struct {
	Model
	ImageID       int64 `gorm:"index"`
	StoragePoolID int64 `gorm:"index"`
	// Where it is in the pool: images/image-<id>-<prefix>.qcow2 in a file pool, the RBD image image-<id>-<prefix>
	// (with its snapshot base) in an RBD pool
	Path   string `gorm:"type:varchar(256)"`
	Status string `gorm:"type:varchar(16)"`
	Reason string `gorm:"type:varchar(512)"`
	// The host running the import or the removal, the only one whose report is taken, and when it was sent (an import:
	// or when its host last reported its progress)
	Hostid int32
	SentAt *time.Time
	// How far the import got as its host reports it: wait (for an import slot of the host), download, write; and the
	// percent of that phase
	Phase    string `gorm:"type:varchar(16)"`
	Progress int
}

// Kinds of commands waiting for a base copy
const (
	ImageWaitLaunch    = "launch"
	ImageWaitReinstall = "reinstall"
)

// ImageStorageWaiter is a command that clones a boot disk from a base copy being imported: sent once the copy is
// synced, given up with the instance when the import fails (§9.6). The command holds the metadata of the instance as
// the launch would have sent it right away
type ImageStorageWaiter struct {
	Model
	ImageStorageID int64 `gorm:"index"`
	InstanceID     int64 `gorm:"index"`
	VolumeID       int64
	Kind           string `gorm:"type:varchar(16)"`
	// The size the boot disk of a reinstalled instance goes back to when the copy can not be made (its disk was not
	// touched; the record was grown with the request)
	PriorSize int32
	Control   string `gorm:"type:text"`
	Command   string `gorm:"type:text"`
}

func init() {
	dbs.AutoMigrate(&ImageStorage{}, &ImageStorageWaiter{})
	// Waiters are removed for good (their commands carry the passwords and keys of the instances): the ones only
	// marked deleted go
	dbs.AutoUpgrade("image_storage_waiters_purge_v1", func(db *gorm.DB) error {
		return db.Exec("DELETE FROM image_storage_waiters WHERE deleted_at IS NOT NULL").Error
	})
	dbs.AutoUpgrade("image_storage_unique_v1", func(db *gorm.DB) error {
		// One copy of an image per pool among the live rows
		return db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS uq_image_storage_live ON image_storages (image_id, storage_pool_id) WHERE deleted_at IS NULL").Error
	})
}
