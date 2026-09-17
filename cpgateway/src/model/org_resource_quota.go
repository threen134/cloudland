package model

import (
	"time"

	"cpgateway/src/dbs"
)

type OrgResourceQuota struct {
	ID           int64   `gorm:"primaryKey" json:"id"`
	OrgID        int64   `gorm:"not null;uniqueIndex:uq_org_region_quota" json:"org_id"`
	RegionID     int64   `gorm:"not null;uniqueIndex:uq_org_region_quota" json:"region_id"`
	MaxCPUCores  float64 `gorm:"default:0;not null" json:"max_cpu_cores"`
	MaxRAMGB     float64 `gorm:"default:0;not null" json:"max_ram_gb"`
	MaxPublicIPs int     `gorm:"default:0;not null" json:"max_public_ips"`
	MaxDiskGB    float64 `gorm:"default:0;not null" json:"max_disk_gb"`
	// Count quotas for resources without a size: VPCs, load balancers and private images owned by the org.
	// 0 means the resource is disabled. The default:0 tag is required by AutoMigrate to add a NOT NULL column to a
	// table with rows; since the default equals the zero value, an explicit 0 on Create is never replaced.
	MaxVPCs          int       `gorm:"column:max_vpcs;default:0;not null" json:"max_vpcs"`
	MaxLoadBalancers int       `gorm:"default:0;not null" json:"max_load_balancers"`
	MaxImages        int       `gorm:"default:0;not null" json:"max_images"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (OrgResourceQuota) TableName() string {
	return "org_resource_quotas"
}

func init() {
	dbs.AutoMigrate(&OrgResourceQuota{})
}
