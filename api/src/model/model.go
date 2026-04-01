/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package model

import (
	"time"

	"github.com/google/uuid"

	"api/src/utils/log"
)

var logger = log.MustGetLogger("model")

type Model struct {
	ID        int64      `gorm:"primary_key"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time
	DeletedAt *time.Time `gorm:"index"`
	UUID      string     `gorm:"type:varchar(64);index" json:"uuid"`
	Creater   int64      `gorm:"default:0"` /* The user ID who created the resource (audit only) */
}

func (m *Model) BeforeCreate() (err error) {
	if m.UUID == "" {
		m.UUID = uuid.New().String()
		logger.Debugf("Create a new model with uuid: %s", m.UUID)
	}
	return
}
