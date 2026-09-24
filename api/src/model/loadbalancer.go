/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"

	"gorm.io/gorm"
)

type VrrpInstance struct {
	Model
	Owner        int64 `gorm:"default:1"` /* The organization ID of the resource */
	Hyper        int32 `gorm:"default:-1"`
	Peer         int32 `gorm:"default:-1"`
	Vrid         int   /* VRRP virtual router id (1-255), unique within the VRRP subnet; the primary key is not bounded */
	VrrpSubnetID int64
	VrrpSubnet   *Subnet `gorm:"foreignkey:VrrpSubnetID"`
	ZoneID       int64
	RouterID     int64
}

type LoadBalancer struct {
	Model
	HaFlag         int           `default:"1" json:"ha_flag"`
	Owner          int64         `gorm:"default:1"` /* The organization ID of the resource */
	Name           string        `gorm:"uniqueIndex:idx_router_lb;type:varchar(64)"`
	Description    string        `gorm:"type:varchar(255)"`
	Status         string        `gorm:"type:varchar(32)"`
	FloatingIps    []*FloatingIp `gorm:"foreignkey:LoadBalancerID"`
	RouterID       int64         `gorm:"uniqueIndex:idx_router_lb"`
	Router         *Router
	Listeners      []*Listener   `gorm:"foreignkey:LoadBalancerID"`
	VrrpInstanceID int64         `gorm:"index"`
	VrrpInstance   *VrrpInstance `gorm:"foreignkey:VrrpInstanceID"`
	OwnerInfo      *Organization `gorm:"-"` /* Transient: populated for SystemAdmin list view */
}

type Listener struct {
	Model
	Owner          int64         `gorm:"default:1"` /* The organization ID of the resource */
	Name           string        `gorm:"uniqueIndex:idx_lb_listener;type:varchar(64)"`
	Status         string        `gorm:"type:varchar(32)"`
	Mode           string        `gorm:"type:varchar(32)"`
	Port           int32         `gorm:"default:-1"`
	LoadBalancerID int64         `gorm:"uniqueIndex:idx_lb_listener"`
	Certificate    string        `gorm:"type:text"`
	Key            string        `gorm:"type:text"`
	Backends       []*Backend    `gorm:"foreignkey:ListenerID"`
	OwnerInfo      *Organization `gorm:"-"` /* Transient: populated for SystemAdmin list view */
}

type Backend struct {
	Model
	Owner       int64  `default:"1"` /* The organization ID of the resource */
	Name        string `gorm:"uniqueIndex:idx_listener_be;type:varchar(64)"`
	ListenerID  int64  `gorm:"uniqueIndex:idx_listener_be"`
	BackendAddr string `gorm:"uniqueIndex:idx_listener_be;type:varchar(128)"`
	Status      string `gorm:"type:varchar(32)"`
	SSL         bool
	Health      string        `gorm:"type:varchar(16)"` /* Health check result reported by the master haproxy: up, down, or empty when unknown */
	OwnerInfo   *Organization `gorm:"-"`                /* Transient: populated for SystemAdmin list view */
}

func init() {
	dbs.AutoMigrate(&LoadBalancer{})
	dbs.AutoMigrate(&Listener{})
	dbs.AutoMigrate(&Backend{})
	dbs.AutoMigrate(&VrrpInstance{})
	// Instances created before the vrid column used their primary key as the VRRP id; keep that value
	// so their running keepalived configuration stays valid
	dbs.AutoUpgrade("vrrp_instances_backfill_vrid_v2", func(db *gorm.DB) error {
		// AutoMigrate adds the column as NULL for existing rows
		return db.Exec("UPDATE vrrp_instances SET vrid = id WHERE (vrid IS NULL OR vrid = 0) AND id <= 255").Error
	})
	// Rows beyond 255 (never backfilled) must not stay NULL: allocateVrid scans the column into ints, and a
	// NULL there fails every later creation in that VPC. 0 means unassigned. Two instances of one VRRP subnet
	// can never share a VRID (keepalived would merge them into one virtual router)
	dbs.AutoUpgrade("vrrp_instances_vrid_not_null_unique_v1", func(db *gorm.DB) error {
		for _, sql := range []string{
			"UPDATE vrrp_instances SET vrid = 0 WHERE vrid IS NULL",
			"ALTER TABLE vrrp_instances ALTER COLUMN vrid SET DEFAULT 0",
			"ALTER TABLE vrrp_instances ALTER COLUMN vrid SET NOT NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_vrrp_subnet_vrid ON vrrp_instances (vrrp_subnet_id, vrid) WHERE deleted_at IS NULL AND vrid > 0",
		} {
			if err := db.Exec(sql).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
