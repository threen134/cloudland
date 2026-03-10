/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"web/src/dbs"
)

type FloatingIp struct {
	Model
	Owner          int64  `gorm:"default:1"`              /* The organization ID of the resource */
	Name           string `gorm:"type:varchar(64);index"` /* The name of the resource */
	FipAddress     string `gorm:"type:varchar(64);index" json:"fip_address"`
	IntAddress     string `gorm:"type:varchar(64)"`
	InstanceID     int64
	Instance       *Instance `gorm:"foreignkey:InstanceID;PRELOAD:false" json:"instance"`
	LoadBalancerID int64
	LoadBalancer   *LoadBalancer `gorm:"foreignkey:LoadbalancerID;PRELOAD:false" json:"load_balancer"`
	Interface      *Interface    `gorm:"foreignkey:FloatingIp"`
	RouterID       int64
	Inbound        int32
	Outbound       int32
	Router         *Router `gorm:"foreignkey:RouterID"`
	IPAddress      string
	Type           string   `gorm:"type:varchar(64)"`
	GroupID        int64    `gorm:"index"`
	Group          *IpGroup `gorm:"foreignkey:GroupID"`
	SubnetID       int64
	Subnet         *Subnet       `gorm:"foreignkey:SubnetID"`
	OwnerInfo      *Organization `gorm:"-"` /* Transient: populated for SystemAdmin list view */
}

func init() {
	dbs.AutoMigrate(&FloatingIp{})
}
