package model

import (
	"fmt"

	"cpgateway-go/src/dbs"

	"gorm.io/gorm"
)

type SystemRole int

const (
	SystemUser  SystemRole = 0
	SystemAdmin SystemRole = 1
)

type UserStatus int

const (
	UserInvited  UserStatus = 0
	UserActive   UserStatus = 1
	UserDormant  UserStatus = 2
	UserDisabled UserStatus = 3
)

type User struct {
	Model
	Email          string     `gorm:"type:varchar(255);not null" json:"email"`
	Username       string     `gorm:"type:varchar(255);not null" json:"username"`
	HashedPassword string     `gorm:"type:varchar(255);not null" json:"-"`
	FirstName      string     `gorm:"type:varchar(128);default:''" json:"first_name"`
	LastName       string     `gorm:"type:varchar(128);default:''" json:"last_name"`
	Language       string     `gorm:"type:varchar(5);default:'en'" json:"language"`
	Remark         string     `gorm:"type:varchar(512);default:''" json:"remark"`
	SystemRole     SystemRole `gorm:"default:0;not null" json:"system_role"`
	// No gorm default: UserInvited is the zero value and must not be replaced by a column default.
	Status      UserStatus `gorm:"not null" json:"status"`
	IsActive    bool       `gorm:"default:false;not null" json:"is_active"`
	IsSuperuser bool       `gorm:"default:false;not null" json:"is_superuser"`
}

func (User) TableName() string {
	return "users"
}

// IsAdmin matches the Python check `is_superuser or system_role == ADMIN`.
func (u *User) IsAdmin() bool {
	return u.IsSuperuser || u.SystemRole == SystemAdmin
}

func (s UserStatus) Name() string {
	switch s {
	case UserInvited:
		return "invited"
	case UserActive:
		return "active"
	case UserDormant:
		return "dormant"
	case UserDisabled:
		return "disabled"
	}
	return fmt.Sprintf("%d", int(s))
}

func init() {
	dbs.AutoMigrate(&User{})
	dbs.AutoUpgrade("001_user_partial_unique", func(db *gorm.DB) error {
		// Partial unique index on email WHERE deleted_at IS NULL
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS uq_user_email_active
			ON users (email)
			WHERE deleted_at IS NULL
		`).Error
	})
	dbs.AutoUpgrade("002_user_username_unique", func(db *gorm.DB) error {
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS uq_user_username_active
			ON users (username)
			WHERE deleted_at IS NULL
		`).Error
	})
	dbs.AutoUpgrade("003_user_username_global_unique", func(db *gorm.DB) error {
		// 用户名全局唯一且不可复用：索引改为覆盖软删除的行，注销后该用户名不会被他人占用。
		// 邮箱仍只约束未删除的行——账号注销后允许本人用同一邮箱重新注册
		if err := db.Exec(`DROP INDEX IF EXISTS uq_user_username_active`).Error; err != nil {
			return err
		}
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS uq_user_username
			ON users (username)
		`).Error
	})
}
