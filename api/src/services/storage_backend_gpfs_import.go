/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Imported GPFS clusters (shared-storage-design.md §7.8): the hosts are members of a cluster their admins run, with
// the file system mounted. CloudLand checks that on every host and runs no GPFS command of its own on the cluster.

import (
	"context"
	"encoding/json"
	"path"
	"regexp"

	"api/src/model"

	"gorm.io/gorm"
)

var gpfsFsNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

// gpfsImportParams are what CloudLand must know of an imported GPFS cluster
type gpfsImportParams struct {
	FsName     string `json:"fs_name"`
	MountPoint string `json:"mount_point,omitempty"`
}

func (b gpfsBackend) ParseImportParams(raw json.RawMessage) (interface{}, error) {
	p := &gpfsImportParams{}
	if err := decodeStorageParams(b.Kind(), raw, p); err != nil {
		return nil, err
	}
	if !gpfsFsNameRe.MatchString(p.FsName) {
		return nil, planError("Give the name of the GPFS file system to use (fs_name)")
	}
	if p.MountPoint == "" {
		p.MountPoint = gpfsMountRoot + "/" + p.FsName
	}
	if path.Clean(p.MountPoint) != p.MountPoint || !path.IsAbs(p.MountPoint) || p.MountPoint == "/" || !gpfsPlainPath.MatchString(p.MountPoint) {
		return nil, planError("The mount point of the file system must be a plain absolute path")
	}
	return p, nil
}

var gpfsPlainPath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func init() {
	registerStorageTaskKind("gpfs:"+StorageTaskImport, &storageTaskKind{
		Slot:   storageSlotNone,
		Steps:  map[string]*storageStepDef{"check_import": {Script: "gpfs_import.sh", Input: gpfsImportInput}},
		Finish: gpfsImportFinish,
	})
}

func gpfsImportOf(cluster *model.StorageCluster) *gpfsImportParams {
	p := &gpfsImportParams{}
	_ = json.Unmarshal([]byte(cluster.Params), p)
	return p
}

func gpfsImportInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	p := gpfsImportOf(cluster)
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "fs_name": p.FsName, "mount_point": p.MountPoint}, nil
}

func gpfsImportFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	p := gpfsImportOf(cluster)
	return storageImportFinish(ctx, tx, task, succeeded, p.FsName, p.MountPoint)
}
