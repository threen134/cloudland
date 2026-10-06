/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"
)

type Migration struct {
	Model
	Name        string `gorm:"type:varchar(64)"`
	InstanceID  int64
	Instance    *Instance `gorm:"foreignkey:InstanceID"`
	Force       bool      `gorm:"default:false"`
	Type        string    `gorm:"type:varchar(32)"`
	SourceHyper int32
	TargetHyper int32
	Phases      []*Task `gorm:"foreignkey:Mission"`
	Status      string  `gorm:"type:varchar(32)"`
	// CreaterName 是发起迁移的用户名（Model.Creater 只存 ID）。本服务没有用户表、
	// 也不与 cpgateway 同步，无法由 ID 反查；用户名不可改不可复用，作为审计快照是准确的
	CreaterName string `gorm:"type:varchar(255)"`
	// CreaterUUID 与用户名快照互补，是发起人账号的全局唯一标识
	CreaterUUID string `gorm:"type:varchar(64)"`
	// 迁移进度：source_migration.sh 迁移期间每几秒上报一次 virsh domjobinfo（内存 + 本地磁盘合计）
	Progress    int32
	Transferred int64
	Total       int64
	// DiskPlan is fixed once the target host is known and then used unchanged by every later step (JSON, []DiskPlanItem)
	DiskPlan string `gorm:"type:text"`
	// Per-disk target pools asked for in the request (JSON, map of volume id to pool id)
	DiskRequests      string `gorm:"type:text"`
	AllowPoolFallback bool   // no gorm default tag, false is meaningful
	IgnoreCapacity    bool
	// The system admin skipped the placement group checks (no default tag, like IgnoreCapacity)
	IgnorePlacement bool
	// Status of the instance before the migration, restored when cland refuses the target
	PriorStatus string `gorm:"type:varchar(32)"`
	// Transient: the best-effort placement group rule this migration breaks, for the create response
	PlacementWarning string `gorm:"-"`
	// An evacuation (type evacuate, shared-storage-design.md §11) started the instance on another host while its
	// source was down; the copy defined on the source goes when the source comes back and reconciles, which sets
	// SourceCleaned. Until then whatever the source reports about the instance is ignored
	SourceCleaned bool
	// Why an evacuation waits or failed
	Message string `gorm:"type:varchar(512)"`
}

const (
	MigrationTypeWarm     = "warm"
	MigrationTypeEvacuate = "evacuate"
)

// DiskPlanItem is where one disk of a migrating instance goes
type DiskPlanItem struct {
	VolumeID   int64  `json:"volume_id"`
	Device     string `json:"device"`
	Booting    bool   `json:"booting"`
	SizeGB     int32  `json:"size_gb"`
	SrcPoolID  int64  `json:"src_pool_id"`
	SrcPath    string `json:"src_path"` // absolute
	DstPoolID  int64  `json:"dst_pool_id"`
	DstPath    string `json:"dst_path"` // absolute
	DstRelPath string `json:"dst_rel_path"`
	// Pool of the target as the node scripts name it: the pool uuid, or the literal "builtin"
	DstPoolUUID string `json:"dst_pool_uuid"`
	DstPoolRoot string `json:"dst_pool_root"`
	Auto        bool   `json:"auto"`
	Reason      string `json:"reason"`
	NVRAM       bool   `json:"nvram,omitempty"`
	// A disk of a shared pool: the target reaches it where it is, so it is neither copied nor made nor cleaned up
	// (shared-storage-design.md §10)
	Shared bool `json:"shared,omitempty"`
}

func init() {
	dbs.AutoMigrate(&Migration{})
}
