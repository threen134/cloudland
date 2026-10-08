/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"
)

type Console struct {
	Model
	Owner      int64 `gorm:"default:1"` /* The organization ID of the resource */
	Instance   int64
	HashSecret string `gorm:"type:varchar(256)"`
	Type       string
	// Hypervisor of a host console (type host); Instance is 0 for those
	HostID int32
}

func init() {
	dbs.AutoMigrate(&Console{})
}
