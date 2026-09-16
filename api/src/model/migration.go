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
}

func init() {
	dbs.AutoMigrate(&Migration{})
}
