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
	// 迁移进度：source_migration.sh 迁移期间每几秒上报一次 virsh domjobinfo（内存 + 本地磁盘合计）
	Progress    int32
	Transferred int64
	Total       int64
}

func init() {
	dbs.AutoMigrate(&Migration{})
}
