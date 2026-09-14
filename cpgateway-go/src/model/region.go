package model

import (
	"time"

	"cpgateway-go/src/dbs"

	"gorm.io/gorm"
)

type Region struct {
	ID               int64   `gorm:"primaryKey" json:"id"`
	UUID             string  `gorm:"type:varchar(36);uniqueIndex" json:"uuid"`
	Name             string  `gorm:"type:varchar(64);uniqueIndex;not null" json:"name"`
	DisplayName      *string `gorm:"type:varchar(128)" json:"display_name,omitempty"`
	InternalEndpoint string  `gorm:"type:varchar(512);not null" json:"-"`
	InternalSecret   string  `gorm:"type:varchar(256);not null" json:"-"`
	IsAvailable      bool    `gorm:"not null" json:"is_available"`
	MaintenanceMode  bool    `gorm:"default:false;not null" json:"maintenance_mode"`
	Description      *string `gorm:"type:text" json:"description,omitempty"`

	// Status monitoring
	LastCheckAt   *time.Time `gorm:"" json:"last_check_at,omitempty"`
	StatusMessage *string    `gorm:"type:varchar(255)" json:"status_message,omitempty"`
	FailCount     int        `gorm:"default:0;not null" json:"fail_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Region) TableName() string {
	return "regions"
}

func (r *Region) BeforeCreate(tx *gorm.DB) error {
	if r.UUID == "" {
		r.UUID = generateUUID()
	}
	return nil
}

func init() {
	dbs.AutoMigrate(&Region{})
}
