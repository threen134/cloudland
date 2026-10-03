/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"
)

func init() {
	dbs.AutoMigrate(&SecurityGroup{}, &SecurityRule{})
}

type SecurityGroup struct {
	Model
	Owner       int64        `gorm:"uniqueIndex:idx_account_secgroup;default:1"` /* The organization ID of the resource */
	Name        string       `gorm:"uniqueIndex:idx_account_secgroup;type:varchar(64)"`
	Description string       `gorm:"type:varchar(256)"`
	IsDefault   bool         `gorm:"default:false"`
	Interfaces  []*Interface `gorm:"many2many:secgroup_ifaces;"`
	RouterID    int64
	Router      *Router       `gorm:"foreignkey:RouterID"`
	OwnerInfo   *Organization `gorm:"-"` /* Transient: populated for SystemAdmin list view */
}

// SecurityRule 的规则键（安全组、来源、方向、IP 版本、协议、端口）建唯一索引，防止并发创建或修改出重复规则；
// 只约束未删除的记录，软删除的旧规则不影响重新创建相同规则
type SecurityRule struct {
	Model
	Owner         int64  `gorm:"default:1"` /* The organization ID of the resource */
	Secgroup      int64  `gorm:"uniqueIndex:idx_secrule_unique,where:deleted_at IS NULL"`
	RemoteIp      string `gorm:"type:varchar(32);uniqueIndex:idx_secrule_unique,where:deleted_at IS NULL"`
	RemoteGroupID int64
	RemoteGroup   *SecurityGroup `gorm:"foreignkey:RemoteGroupID"`
	Direction     string         `gorm:"type:varchar(16);uniqueIndex:idx_secrule_unique,where:deleted_at IS NULL"`
	IpVersion     string         `gorm:"type:varchar(12);uniqueIndex:idx_secrule_unique,where:deleted_at IS NULL"`
	Protocol      string         `gorm:"type:varchar(20);uniqueIndex:idx_secrule_unique,where:deleted_at IS NULL"`
	// 不能加 gorm default 标签：ICMP type/code 为 0 是合法值，Create 时 GORM 会把零值替换成默认值
	PortMin int32  `gorm:"uniqueIndex:idx_secrule_unique,where:deleted_at IS NULL"`
	PortMax int32  `gorm:"uniqueIndex:idx_secrule_unique,where:deleted_at IS NULL"`
	Name    string `gorm:"type:varchar(64)"`
}

func init() {
	dbs.AutoMigrate(&SecurityGroup{}, &SecurityRule{})
}
