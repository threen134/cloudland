/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"time"

	"api/src/dbs"
)

const (
	StorageDriverLocal = "local"

	// Built-in local pool: every host has it, rooted at the node cache directory
	BuiltinPoolName = "local"
	BuiltinPoolRoot = "/opt/cloudland/cache"
	// Other local pools are mounted at <LocalPoolsDir>/<pool uuid> on every host that has them
	LocalPoolsDir = "/opt/cloudland/pools"

	StoragePoolActive   = "active"
	StoragePoolDisabled = "disabled"
)

// StoragePool is where volume files live. Every volume belongs to exactly one pool.
type StoragePool struct {
	Model
	Name          string  `gorm:"type:varchar(64);uniqueIndex"`
	Driver        string  `gorm:"type:varchar(32)"`
	MountPath     string  `gorm:"type:varchar(256)"`
	Builtin       bool    // the built-in local pool; no gorm default tag, false is meaningful
	Media         string  `gorm:"type:varchar(16)"` // ssd | hdd | nvme | empty, display and allocation checks only
	FallbackGroup string  `gorm:"type:varchar(64)"` // pools sharing a non-empty group may replace each other on migration
	Status        string  `gorm:"type:varchar(32)"` // active | disabled
	IsDefault     bool    // no gorm default tag, false is meaningful
	OverRatio     float64 // max allocated/capacity per host, qcow2 is thin
	Description   string  `gorm:"type:varchar(256)"`
}

// Shared reports whether volumes of the pool are reachable from every host
func (p *StoragePool) Shared() bool {
	return p.Driver != StorageDriverLocal
}

// Root is the absolute directory of the pool on a host
func (p *StoragePool) Root() string {
	if p.Builtin {
		return BuiltinPoolRoot
	}
	return p.MountPath
}

const (
	HyperPoolCreating    = "creating"
	HyperPoolExtending   = "extending"
	HyperPoolRemoving    = "removing"
	HyperPoolReady       = "ready"
	HyperPoolDegraded    = "degraded"
	HyperPoolUnavailable = "unavailable"
	HyperPoolMaintenance = "maintenance"
	HyperPoolLost        = "lost"
	HyperPoolError       = "error"

	LayoutBuiltin = "builtin"
	LayoutSingle  = "single"
	LayoutLinear  = "linear"
	LayoutRaid1   = "raid1"

	// Reason of an unavailable pool whose host stopped reporting (§5.8 of the plan needs to tell it apart)
	ReasonNodeOffline = "node_offline"
)

// HyperStoragePool is the directory of a storage pool on one host
type HyperStoragePool struct {
	Model
	Hostid     int32        `gorm:"uniqueIndex:idx_hyper_pool"`
	PoolID     int64        `gorm:"uniqueIndex:idx_hyper_pool"`
	Pool       *StoragePool `gorm:"foreignkey:PoolID"`
	Status     string       `gorm:"type:varchar(32)"`
	Reason     string       `gorm:"type:varchar(256)"`
	LastOp     string       `gorm:"type:varchar(16)"` // create | extend | replace | remove | adopt
	PrevStatus string       `gorm:"type:varchar(32)"` // status to go back to when an extend or replace fails
	CheckedAt  *time.Time
	DeadlineAt *time.Time
	// Health last reported by the node, kept also while the row is lost (restore needs it)
	ReportedStatus string `gorm:"type:varchar(32)"`
	ReportedReason string `gorm:"type:varchar(256)"`
	// local pools only
	Layout        string `gorm:"type:varchar(16)"`
	Devices       string `gorm:"type:text"` // JSON: [{id, serial, model, size_bytes, media, array, pair}]
	VgName        string `gorm:"type:varchar(64)"`
	CapacityBytes int64
	UsedBytes     int64
	AvailBytes    int64
	OwnBytes      int64   // builtin only: actual usage of CloudLand files
	OverRatio     float64 // builtin only: the node's disk_over_ratio
	CapacityAt    *time.Time
	SyncPercent   int32
	Files         string `gorm:"type:text"` // JSON: file names under volumes/ from the last report (restore, adopt)
	UsageReport   string `gorm:"type:text"` // JSON: largest files by actual usage, from the last on-demand scan
	UsageAt       *time.Time
	Maintenance   bool // no gorm default tag, false is meaningful
}

// Available reports whether new allocations may land on the pool
func (p *HyperStoragePool) Available() bool {
	return p.Status == HyperPoolReady || p.Status == HyperPoolDegraded
}

// UsageRatio is used / (used + available), as df computes Use%: blocks reserved for root do not count
func (p *HyperStoragePool) UsageRatio() float64 {
	if p.UsedBytes+p.AvailBytes <= 0 {
		return 0
	}
	return float64(p.UsedBytes) / float64(p.UsedBytes+p.AvailBytes)
}

const (
	DiskFree          = "free"
	DiskDirty         = "dirty"
	DiskInUse         = "in_use"
	DiskSystem        = "system"
	DiskCloudlandPool = "cloudland_pool"
	DiskUnknownMember = "unknown_member"
	DiskShared        = "shared"
)

// HyperDisk is a block device found by the last disk scan of a host
type HyperDisk struct {
	Model
	Hostid        int32  `gorm:"uniqueIndex:idx_hyper_disk"`
	DiskID        string `gorm:"uniqueIndex:idx_hyper_disk;type:varchar(256)"`
	Name          string `gorm:"type:varchar(32)"`
	Path          string `gorm:"type:varchar(256)"`
	Serial        string `gorm:"type:varchar(128)"`
	DiskModel     string `gorm:"column:model;type:varchar(128)"`
	SizeBytes     int64
	Transport     string `gorm:"type:varchar(16)"`
	DetectedMedia string `gorm:"type:varchar(16)"`
	Media         string `gorm:"type:varchar(16)"`
	MediaSource   string `gorm:"type:varchar(8)"` // auto | manual
	State         string `gorm:"type:varchar(32)"`
	Detail        string `gorm:"type:varchar(256)"`
	PoolUUID      string `gorm:"type:varchar(64)"`
	OwnerHostid   int32
	ScannedAt     time.Time
}

const (
	ReservationBoot      = "boot"
	ReservationMigration = "migration"
)

// StorageReservation holds capacity for allocations that cannot be counted on the volume itself yet
type StorageReservation struct {
	ID          int64 `gorm:"primaryKey"`
	CreatedAt   time.Time
	Hostid      int32 `gorm:"index:idx_res_host_pool"`
	PoolID      int64 `gorm:"index:idx_res_host_pool"`
	VolumeID    int64
	MigrationID int64  `gorm:"index"`
	Kind        string `gorm:"type:varchar(16)"`
	SizeGB      int32
	ExpiresAt   time.Time `gorm:"index"`
}

func init() {
	dbs.AutoMigrate(&StoragePool{}, &HyperStoragePool{}, &HyperDisk{}, &StorageReservation{})
}
