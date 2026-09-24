package model

import (
	"time"

	"cpgateway/src/dbs"
)

type OrgResourceConsumption struct {
	ID            int64     `gorm:"primaryKey" json:"id"`
	OrgID         int64     `gorm:"not null;uniqueIndex:uq_org_region_consumption" json:"org_id"`
	RegionID      int64     `gorm:"not null;uniqueIndex:uq_org_region_consumption" json:"region_id"`
	CPUCores      float64   `gorm:"default:0;not null" json:"cpu_cores"`
	RAMGB         float64   `gorm:"default:0;not null" json:"ram_gb"`
	PublicIPs     int       `gorm:"default:0;not null" json:"public_ips"`
	DiskGB        float64   `gorm:"default:0;not null" json:"disk_gb"`
	VPCs          int       `gorm:"column:vpcs;default:0;not null" json:"vpcs"`
	LoadBalancers int       `gorm:"default:0;not null" json:"load_balancers"`
	Images        int       `gorm:"default:0;not null" json:"images"`
	VpnGateways   int       `gorm:"default:0;not null" json:"vpn_gateways"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (OrgResourceConsumption) TableName() string {
	return "org_resource_consumptions"
}

func init() {
	dbs.AutoMigrate(&OrgResourceConsumption{})
}
