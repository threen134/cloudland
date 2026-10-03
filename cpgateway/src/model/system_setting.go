package model

import (
	"time"

	"cpgateway/src/dbs"
)

type SystemSetting struct {
	Key         string    `gorm:"type:varchar(128);primaryKey" json:"key"`
	Value       *string   `gorm:"type:text" json:"value,omitempty"`
	ValueType   string    `gorm:"type:varchar(32);not null" json:"value_type"`
	Category    string    `gorm:"type:varchar(32);not null" json:"category"`
	Description *string   `gorm:"type:varchar(256)" json:"description,omitempty"`
	IsSecret    bool      `gorm:"default:false;not null" json:"is_secret"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (SystemSetting) TableName() string {
	return "system_settings"
}

type SystemConfigVersion struct {
	ID        int64     `gorm:"primaryKey;autoIncrement:false" json:"id"`
	Version   int64     `gorm:"not null;default:0" json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (SystemConfigVersion) TableName() string {
	return "system_config_version"
}

func init() {
	dbs.AutoMigrate(&SystemSetting{})
	dbs.AutoMigrate(&SystemConfigVersion{})
}
