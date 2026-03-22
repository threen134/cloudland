/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"

	"github.com/jinzhu/gorm"
)

const (
	OS_LINUX   = "linux"
	OS_WINDOWS = "windows"
	OS_OTHER   = "other"
)

const (
	ImageVisibilityPrivate = "private"
	ImageVisibilityPublic  = "public"
)

// OSCodes is a list of supported operating systems
var OSCodes = []string{OS_LINUX, OS_WINDOWS, OS_OTHER}

type StorageStatus string

const (
	StorageStatusSynced   StorageStatus = "synced"
	StorageStatusError    StorageStatus = "error"
	StorageStatusUnknown  StorageStatus = "unknown"
	StorageStatusSyncing  StorageStatus = "syncing"
	StorageStatusNotFound StorageStatus = "not_found"
)

type Image struct {
	Model
	Owner                 int64     `gorm:"default:1"` /* The organization ID of the resource */
	Name                  string    `gorm:"type:varchar(128)"`
	OSCode                string    `gorm:"type:varchar(128);default:'linux'"`
	Format                string    `gorm:"type:varchar(128)"`
	Architecture          string    `gorm:"type:varchar(256)"`
	BootLoader            string    `gorm:"type:varchar(32);default:'bios'"`
	Status                string    `gorm:"type:varchar(128)"`
	Href                  string    `gorm:"type:varchar(256)"`
	Checksum              string    `gorm:"type:varchar(36)"`
	OsHashAlgo            string    `gorm:"type:varchar(36)"`
	OsHashValue           string    `gorm:"type:varchar(36)"`
	Holder                string    `gorm:"type:varchar(36)"`
	Protected             bool      `gorm:"default:false"`
	Visibility            string    `gorm:"type:varchar(36)"`
	MiniDisk              int32     `gorm:"default:0"`
	MiniMem               int32     `gorm:"default:0"`
	Size                  int64     `gorm:"default:0"`
	OsVersion             string    `gorm:"type:varchar(128)"`
	VirtType              string    `gorm:"type:varchar(36)"`
	UserName              string    `gorm:"type:varchar(128)"`
	QAEnabled             bool      `gorm:"default:false"`
	CaptureFromInstanceID int64     `gorm:"default:0"`
	CaptureFromInstance   *Instance `gorm:"foreignkey:InstanceID"`
	IsRescue              bool      `gorm:"default:false"`
	RescueImage           int64     `gorm:"default:0"`
	StorageType           string    `gorm:"type:varchar(36);"`
	OsFamily              string    `gorm:"type:varchar(128);"`
}

type ImageStorage struct {
	Model
	ImageID  int64         `gorm:"default:0"`
	Image    *Image        `gorm:"foreignkey:ImageID"`
	VolumeID string        `gorm:"type:varchar(128)"`
	PoolID   string        `gorm:"type:varchar(128)"`
	Status   StorageStatus `gorm:"type:varchar(128);default:'syncing'"` // syncing, synced, error, not-found
}

func init() {
	dbs.AutoMigrate(&Image{}, &ImageStorage{})
	dbs.AutoUpgrade("image_visibility_default", func(db *gorm.DB) error {
		// Set visibility to 'public' for system org images that have no visibility set yet
		if err := db.Exec(
			"UPDATE images SET visibility = ? WHERE (visibility = '' OR visibility IS NULL) AND owner IN (SELECT id FROM organizations WHERE type = ?)",
			ImageVisibilityPublic, OrgTypeSystem,
		).Error; err != nil {
			return err
		}
		// Set remaining images with no visibility to 'private'
		if err := db.Model(&Image{}).
			Where("visibility = '' OR visibility IS NULL").
			Update("visibility", ImageVisibilityPrivate).Error; err != nil {
			return err
		}
		return nil
	})
}

func (i *Image) Clone() *Image {
	return &Image{
		Owner:                 i.Owner,
		Name:                  i.Name,
		OSCode:                i.OSCode,
		Format:                i.Format,
		Architecture:          i.Architecture,
		BootLoader:            i.BootLoader,
		Status:                i.Status,
		Href:                  i.Href,
		Checksum:              i.Checksum,
		OsHashAlgo:            i.OsHashAlgo,
		OsHashValue:           i.OsHashValue,
		Holder:                i.Holder,
		Protected:             i.Protected,
		Visibility:            i.Visibility,
		MiniDisk:              i.MiniDisk,
		MiniMem:               i.MiniMem,
		Size:                  i.Size,
		OsVersion:             i.OsVersion,
		VirtType:              i.VirtType,
		UserName:              i.UserName,
		QAEnabled:             i.QAEnabled,
		CaptureFromInstanceID: i.CaptureFromInstanceID,
		CaptureFromInstance:   i.CaptureFromInstance,
		StorageType:           i.StorageType,
		OsFamily:              i.OsFamily,
	}
}
