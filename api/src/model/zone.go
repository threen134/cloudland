/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"time"

	"api/src/dbs"
	"github.com/google/uuid"
)

type Zone struct {
	ID        int64  `gorm:"primary_key"`
	UUID      string `gorm:"type:varchar(64);index" json:"uuid"`
	Name      string `gorm:"unique_index"`
	Remark    string `gorm:"type:varchar(512);default:''"`
	Default   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (z *Zone) BeforeCreate() (err error) {
	if z.UUID == "" {
		z.UUID = uuid.New().String()
	}
	return
}

func init() {
	dbs.AutoMigrate(&Zone{})
}
