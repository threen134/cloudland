/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"
)

const (
	// PlacementPolicySpread keeps the members of a group on different hosts
	PlacementPolicySpread = "spread"
	// PlacementPolicyPack keeps the members of a group on one host
	PlacementPolicyPack = "pack"
)

// PlacementGroup tells where the instances created in it may land, relative to each other (placement-group-plan.md)
type PlacementGroup struct {
	Model
	Owner       int64  `gorm:"uniqueIndex:idx_owner_placement_group"` // organization ID
	Name        string `gorm:"uniqueIndex:idx_owner_placement_group;type:varchar(64)"`
	Description string `gorm:"type:varchar(256)"`
	Policy      string `gorm:"type:varchar(16)"` // spread | pack
	// No default tag: false (best effort) is a real choice and GORM would replace it with the column default
	Strict bool
	ZoneID int64
	Zone   *Zone `gorm:"foreignkey:ZoneID"`
}

func init() {
	dbs.AutoMigrate(&PlacementGroup{})
}
