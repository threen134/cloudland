package model

import (
	"time"

	"cpgateway-go/src/dbs"

	"gorm.io/gorm"
)

type NotificationChannel struct {
	ID        int64     `gorm:"primaryKey" json:"id"`
	UUID      string    `gorm:"type:varchar(36);uniqueIndex" json:"uuid"`
	OrgID     int64     `gorm:"not null;index" json:"org_id"`
	Name      string    `gorm:"type:varchar(128);not null" json:"name"`
	Type      string    `gorm:"type:varchar(32);not null" json:"type"`
	Config    string    `gorm:"type:text;not null" json:"config"`
	Enabled   bool      `gorm:"not null" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (NotificationChannel) TableName() string {
	return "notification_channels"
}

func (n *NotificationChannel) BeforeCreate(tx *gorm.DB) error {
	if n.UUID == "" {
		n.UUID = generateUUID()
	}
	return nil
}

func init() {
	dbs.AutoMigrate(&NotificationChannel{})
}
