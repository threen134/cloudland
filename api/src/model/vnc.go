/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"
)

type Vnc struct {
	Model
	Owner         int64  `gorm:"default:1"` /* The organization ID of the resource */
	LocalAddress  string `gorm:"type:varchar(64)"`
	LocalPort     int32
	AccessAddress string `gorm:"type:varchar(64)"`
	AccessPort    int32
	InstanceID    int64
}

func init() {
	dbs.AutoMigrate(&Vnc{})
	dbs.AutoMigrate(&SerialConsole{})
}

// SerialConsole is the one-time TCP address a hypervisor exposes the instance's serial console on
// (start_serial_console.sh), valid until the console proxy connects or the listener times out
type SerialConsole struct {
	Model
	InstanceID   int64  `gorm:"index"`
	Session      string `gorm:"type:varchar(64);index"` /* Chosen by the request waiting for this record */
	LocalAddress string `gorm:"type:varchar(64)"`
	LocalPort    int32
}
