/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"
)

type VolumeStatus string

const (
	VolumeStatusResizing  VolumeStatus = "resizing"
	VolumeStatusAvailable VolumeStatus = "available"
	VolumeStatusAttached  VolumeStatus = "attached"
	VolumeStatusAttaching VolumeStatus = "attaching"
	VolumeStatusDetaching VolumeStatus = "detaching"
	VolumeStatusError     VolumeStatus = "error"
	VolumeStatusPending   VolumeStatus = "pending"
	// The node is deleting the file; the record goes away when it reports back
	VolumeStatusDeleting VolumeStatus = "deleting"
	// Deleting timed out: the file may be gone already, so the volume can only be deleted again
	VolumeStatusDeleteFailed VolumeStatus = "delete_failed"
	// The pool holding the file was declared lost
	VolumeStatusLost VolumeStatus = "lost"
	// The host holding the file was deleted with its pools kept for adoption
	VolumeStatusOrphaned VolumeStatus = "orphaned"
)

func (s VolumeStatus) String() string {
	return string(s)
}

type Volume struct {
	Model
	Owner int64  `gorm:"default:1;index"` /* The organization ID of the resource */
	Name  string `gorm:"type:varchar(128)"`
	// Path of the file relative to the root of its storage pool
	Path       string `gorm:"type:varchar(256)"`
	Size       int32
	Booting    bool
	Format     string       `gorm:"type:varchar(32)"`
	Status     VolumeStatus `gorm:"type:varchar(32)"`
	Target     string       `gorm:"type:varchar(32)"`
	Href       string       `gorm:"type:varchar(256)"`
	InstanceID int64
	Instance   *Instance `gorm:"foreignkey:InstanceID"`
	// Host holding the file; 0 means not created yet (created on the host of the first attach)
	Hyper         int32
	StoragePoolID int64         `gorm:"index"`
	StoragePool   *StoragePool  `gorm:"foreignkey:StoragePoolID"`
	Reason        string        `gorm:"type:varchar(512)"`
	OwnerInfo     *Organization `gorm:"-"` /* Transient: populated for SystemAdmin list view */
}

func (v *Volume) IsBusy() bool {
	switch v.Status {
	case VolumeStatusPending, VolumeStatusResizing, VolumeStatusAttaching, VolumeStatusDetaching, VolumeStatusDeleting:
		return true
	}
	return false
}

func (v *Volume) IsError() bool {
	return v.Status == VolumeStatusError
}

func (v *Volume) IsAvailable() bool {
	return v.Status == VolumeStatusAvailable
}

func (v *Volume) IsAttached() bool {
	return v.Status == VolumeStatusAttached
}

func init() {
	dbs.AutoMigrate(&Volume{})
}
