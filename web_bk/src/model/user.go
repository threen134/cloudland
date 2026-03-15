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
	"web/src/dbs"

	"github.com/jinzhu/gorm"
)

type SystemRole int

const (
	SystemUser  SystemRole = iota // 0 - 普通用户
	SystemAdmin                   // 1 - 系统管理员
)

type UserStatus int

const (
	UserActive   UserStatus = 1 // 正常：属于至少一个 Org，可登录
	UserDormant  UserStatus = 2 // 休眠：不属于任何 Org，可登录但资源操作受限
	UserDisabled UserStatus = 3 // 禁用：被 SystemAdmin 主动禁用，无法登录
)

func (s UserStatus) String() string {
	switch s {
	case UserActive:
		return "Active"
	case UserDormant:
		return "Dormant"
	case UserDisabled:
		return "Disabled"
	default:
		return "Unknown"
	}
}

type User struct {
	Model
	// Email is the unique login identifier. Partial unique index is created by AutoUpgrade.
	Email      string     `gorm:"size:255" json:"email,omitempty"`
	Password   string     `gorm:"size:255" json:"password,omitempty"`
	FirstName  string     `gorm:"size:128"`
	LastName   string     `gorm:"size:128"`
	Remark     string     `gorm:"size:512"`
	Region     string     `gorm:"size:64"`
	Language   string     `gorm:"size:16;default:'zh'"`
	SystemRole SystemRole `gorm:"default:0"`
	Status     UserStatus `gorm:"default:1"`
	Members    []*Member  `gorm:"foreignkey:UserID"`
}

func (User) TableName() string {
	return "users"
}

func init() {
	var sr SystemRole
	var st UserStatus
	gob.Register(sr)
	gob.Register(st)
	dbs.AutoMigrate(&User{})
	// Create a partial unique index on email that only covers non-deleted rows with a non-empty email.
	// This allows: (1) soft-deleted users' emails to be reused, (2) rows with empty email to coexist.
	// GORM's struct tag `unique_index` cannot express the WHERE clause, so we do it here.
	dbs.AutoUpgrade("idx_users_email_partial", func(db *gorm.DB) error {
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email
			ON users (email)
			WHERE deleted_at IS NULL AND email != ''
		`).Error
	})
}
