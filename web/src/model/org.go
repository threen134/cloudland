/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package model

import (
	"encoding/gob"
	"fmt"

	"web/src/dbs"

	"github.com/jinzhu/gorm"
)

func init() {
	dbs.AutoMigrate(&Member{}, &Organization{})
	var role OrgRole
	gob.Register(role)
	gob.Register(Member{})
	gob.Register([]*Member{})

	// Create a partial unique index on slug that only covers non-deleted organizations.
	// This allows a soft-deleted org's slug to be reused by a new org.
	// GORM's struct tag cannot express the WHERE clause, so we do it here.
	dbs.AutoUpgrade("idx_org_slug_partial", func(db *gorm.DB) error {
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_org_slug
			ON organizations (slug)
			WHERE deleted_at IS NULL AND slug != ''
		`).Error
	})
}

// OrgRole — Org 级角色（存于 members.org_role 字段）
type OrgRole int

const (
	OrgNone   OrgRole = iota // 0 — 无权限（不会实际存入）
	OrgReader                // 1 — 只读
	OrgWriter                // 2 — 读写
	OrgAdmin                 // 3 — 管理
)

func (r OrgRole) String() string {
	switch r {
	case OrgNone:
		return "None"
	case OrgReader:
		return "Reader"
	case OrgWriter:
		return "Writer"
	case OrgAdmin:
		return "Admin"
	default:
		return fmt.Sprintf("%d", int(r))
	}
}

// OrgType 区分 Org 的用途
type OrgType int

const (
	OrgTypeTeam   OrgType = 1 // 普通团队 Org（默认）
	OrgTypeSystem OrgType = 2 // 系统 Org：全局唯一，adminInit 创建，不可删除
)

type Organization struct {
	Model
	Name string `gorm:"size:255"`
	// Slug partial unique index (WHERE deleted_at IS NULL) is created by AutoUpgrade above.
	Slug        string    `gorm:"size:64"`
	OrgType     OrgType   `gorm:"default:1"`
	OwnerUserID int64     `gorm:"not null"`
	Members     []*Member `gorm:"foreignkey:OrgID"`
	OwnerUser   *User     `gorm:"foreignkey:OwnerUserID"`
	DefaultSG   int64
}

func (Organization) TableName() string {
	return "organizations"
}

type Member struct {
	Model
	UserID  int64   `gorm:"not null;uniqueIndex:idx_user_org"`
	OrgID   int64   `gorm:"not null;uniqueIndex:idx_user_org"`
	OrgRole OrgRole `gorm:"default:1"`
}
