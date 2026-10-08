/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"time"

	"api/src/dbs"
)

// Storage packages: installers of storage software uploaded by a system admin and kept in S3
// (docs/architecture/plan/shared-storage-design.md §5.8, §6.1). Only GPFS needs one; Ceph installs from the
// distribution and runs container images.

const (
	StoragePackageUploading = "uploading"
	StoragePackageVerifying = "verifying"
	StoragePackageReady     = "ready"
	StoragePackageError     = "error"
)

// StoragePackage is an uploaded installer of storage software
type StoragePackage struct {
	Model
	Kind      string `gorm:"type:varchar(16)"`  // storage kind it installs: gpfs
	Edition   string `gorm:"type:varchar(32)"`  // erasure_code | data_management | data_access | standard | developer
	Version   string `gorm:"type:varchar(32)"`  // 6.0.0.2
	FileName  string `gorm:"type:varchar(256)"` // as uploaded
	SizeBytes int64
	// SHA-256 of the whole installer, computed after the upload, shown so it can be checked against IBM's page
	SHA256 string `gorm:"type:varchar(64)"`
	// storage-packages/<uuid>/<file name> in the S3 bucket
	ObjectKey string `gorm:"type:varchar(256)"`
	// S3 multipart upload in progress, empty once completed or aborted
	UploadID string `gorm:"type:varchar(256)"`
	// Parts uploaded so far, in order: part n is taken only after part n-1 (or again, after a failed send)
	PartsDone int32
	// Set when clapi downloads the installer itself instead of an upload
	SourceURL string `gorm:"type:varchar(1024)"`
	// Line of the installer where its tar.gz payload starts (PGM_BEGIN_TGZ)
	PayloadLine int32
	Distros     string `gorm:"type:varchar(256)"` // json: ["ubuntu22","ubuntu24"]
	Manifest    string `gorm:"type:text"`         // json: package file name -> md5
	LicenseText string `gorm:"type:text"`         // json: language -> license agreement text
	// Who accepted the license and when (a user name snapshot, clapi can not resolve user IDs); empty = not accepted
	AcceptedBy string `gorm:"type:varchar(64)"`
	AcceptedAt *time.Time
	Status     string `gorm:"type:varchar(16)"`
	Reason     string `gorm:"type:varchar(512)"`
	// When the upload last made progress or the verification started; drives the clean-up of stale ones
	ProgressAt *time.Time
}

func init() {
	dbs.AutoMigrate(&StoragePackage{})
}
