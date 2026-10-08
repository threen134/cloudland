/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"time"

	"api/src/dbs"
)

// StoragePoolReconcile is the last orphan report of a shared pool (shared-storage-design.md §16 S5): the objects of the
// pool clapi has no record of, only reported
type StoragePoolReconcile struct {
	ID          int64  `gorm:"primaryKey"`
	PoolID      int64  `gorm:"uniqueIndex"`
	Status      string `gorm:"type:varchar(16)"` // running | done | error
	Hostid      int32  // the host asked to list the pool
	Error       string `gorm:"type:varchar(512)"`
	Objects     int    // how many objects the pool holds
	Truncated   bool   // the host listed only the first 100000
	Orphans     string `gorm:"type:text"` // json array of the orphans
	RequestedAt time.Time
	CheckedAt   *time.Time
}

func init() {
	dbs.AutoMigrate(&StoragePoolReconcile{})
}
