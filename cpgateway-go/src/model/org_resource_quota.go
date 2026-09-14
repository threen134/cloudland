package model

import (
	"time"

	"cpgateway-go/src/dbs"
)

type OrgResourceQuota struct {
	ID           int64     `gorm:"primaryKey" json:"id"`
	OrgID        int64     `gorm:"not null;uniqueIndex:uq_org_region_quota" json:"org_id"`
	RegionID     int64     `gorm:"not null;uniqueIndex:uq_org_region_quota" json:"region_id"`
	MaxCPUCores  float64   `gorm:"default:0;not null" json:"max_cpu_cores"`
	MaxRAMGB     float64   `gorm:"default:0;not null" json:"max_ram_gb"`
	MaxPublicIPs int       `gorm:"default:0;not null" json:"max_public_ips"`
	MaxDiskGB    float64   `gorm:"default:0;not null" json:"max_disk_gb"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (OrgResourceQuota) TableName() string {
	return "org_resource_quotas"
}

func init() {
	dbs.AutoMigrate(&OrgResourceQuota{})
}
