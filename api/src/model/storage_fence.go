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

// Statuses of a fence
const (
	StorageFenceFencing = "fencing"
	StorageFenceFenced  = "fenced"
	// An admin confirmed the host is off (powered down through IPMI, the provider): no fence command ran, because the
	// cluster could not run one (no admin host left online, an imported GPFS cluster)
	StorageFenceConfirmed     = "confirmed"
	StorageFenceFailed        = "failed"
	StorageFenceUnfencing     = "unfencing"
	StorageFenceUnfenceFailed = "unfence_failed"
)

// How a host is fenced
const (
	StorageFenceExpel     = "expel"     // GPFS: mmexpelnode, kept until mmexpelnode -r
	StorageFenceBlocklist = "blocklist" // Ceph: a blocklist range for the host's address, with a long expiry
	StorageFenceByAdmin   = "confirmed"
)

// StorageFence keeps a host that is down off the disks of a storage cluster (shared-storage-design.md §11.2): its
// instances are recovered on other hosts, and the host must not write to their disks again, even when its network
// comes back with them still running, before it removed its copies (§11.4). One live row per cluster and host; it is
// removed once the host is let back in
type StorageFence struct {
	Model
	ClusterID int64  `gorm:"index"`
	Hostid    int32  `gorm:"index"`
	Status    string `gorm:"type:varchar(16)"`
	Method    string `gorm:"type:varchar(16)"`
	// The address blocklisted (Ceph), the node name expelled (GPFS)
	Target   string `gorm:"type:varchar(255)"`
	TaskID   int64
	Message  string `gorm:"type:varchar(512)"`
	FencedAt *time.Time
	// The admin who confirmed the host was off, when no fence command could run
	ConfirmedBy string `gorm:"type:varchar(255)"`
}

func init() {
	dbs.AutoMigrate(&StorageFence{})
	// One live row per cluster and host, also when two requests fence the host at once
	dbs.AutoUpgrade("storage_fences_live_unique", func(db *gorm.DB) error {
		return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_storage_fence_live ON storage_fences (cluster_id, hostid) WHERE deleted_at IS NULL`).Error
	})
}
