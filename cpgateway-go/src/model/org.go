package model

import (
	"cpgateway-go/src/dbs"

	"gorm.io/gorm"
)

type OrgType int

const (
	OrgTeam   OrgType = 1
	OrgSystem OrgType = 2
)

type OrgStatus int

const (
	OrgPending   OrgStatus = 0
	OrgActive    OrgStatus = 1
	OrgSuspended OrgStatus = 2
	OrgDisabled  OrgStatus = 3
)

type Organization struct {
	Model
	Name        string    `gorm:"type:varchar(255);not null" json:"name"`
	Slug        string    `gorm:"type:varchar(64);not null" json:"slug"`
	Description string    `gorm:"type:text;default:''" json:"description"`
	OrgType     OrgType   `gorm:"default:1;not null" json:"org_type"`
	OwnerUserID int64     `gorm:"not null" json:"owner_user_id"`
	DefaultSG   int64     `gorm:"default:0" json:"default_sg"`
	Status      OrgStatus `gorm:"default:0;not null" json:"status"`

	// Relationships (not stored in DB)
	Owner   *User    `gorm:"foreignKey:OwnerUserID" json:"owner,omitempty"`
	Members []Member `gorm:"foreignKey:OrgID" json:"members,omitempty"`
}

func (Organization) TableName() string {
	return "organizations"
}

func init() {
	dbs.AutoMigrate(&Organization{})
	dbs.AutoUpgrade("003_org_slug_unique", func(db *gorm.DB) error {
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS uq_org_slug_active
			ON organizations (slug)
			WHERE deleted_at IS NULL
		`).Error
	})
}
