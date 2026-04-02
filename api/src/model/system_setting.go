package model

import (
	"time"

	"api/src/dbs"
)

// SystemSettingMirror 系统设置本地镜像表。
// 由 CPGateway 推送同步，clapi 只读此表获取系统级配置（如通知渠道参数）。
type SystemSettingMirror struct {
	Key       string    `gorm:"primary_key;size:128" json:"key"`
	Value     string    `gorm:"type:text"           json:"value"`      // JSON 序列化的值
	ValueType string    `gorm:"size:32"             json:"value_type"` // string|number|boolean|json|secret
	Category  string    `gorm:"size:32"             json:"category"`
	IsSecret  bool      `gorm:"default:false"       json:"is_secret"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"    json:"updated_at"`
}

func (SystemSettingMirror) TableName() string {
	return "system_setting_mirror"
}

// SystemSettingMirrorVersion 记录本地镜像的配置版本号（单行，id=1）。
// 用于乱序保护：只接受 version >= local_version 的同步请求。
type SystemSettingMirrorVersion struct {
	ID      int64     `gorm:"primary_key"      json:"id"`
	Version int64     `gorm:"default:0"        json:"version"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

func (SystemSettingMirrorVersion) TableName() string {
	return "system_setting_mirror_version"
}

func init() {
	dbs.AutoMigrate(&SystemSettingMirror{}, &SystemSettingMirrorVersion{})
}
