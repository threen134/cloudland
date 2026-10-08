/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// More file systems on a managed cluster (shared-storage-design.md §7.3): a new one is made on new disks of the
// members (create_fs, the expansion of add_disks with a file system of its own), one with no pool on it is deleted
// with its disks (delete_fs). The kind says what a file system needs (storageFilesystemMaker) and lays out the steps.

import (
	"context"
	"fmt"

	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StorageTaskCreateFs = "create_fs"
	StorageTaskDeleteFs = "delete_fs"
)

// CreateFilesystem makes a new file system of a managed cluster on new disks of its members
func (a *StorageClusterAdmin) CreateFilesystem(ctx context.Context, uuid string, plan *StorageFilesystemPlan, disks []*StorageDiskPlan,
	allowUnsupported bool) (*model.StorageTask, error) {
	// What a name may be is the kind's rule (storageFilesystemMaker.NewFilesystem checks it)
	if plan == nil || plan.Name == "" {
		return nil, planError("A new file system needs a name")
	}
	return a.expand(ctx, uuid, StorageTaskCreateFs, &StorageClusterExpand{Disks: disks, NewFilesystem: plan, AllowUnsupported: allowUnsupported})
}

// DeleteFilesystem deletes a file system no pool is on, with its disks, which are wiped; the last file system of a
// cluster goes with the cluster
func (a *StorageClusterAdmin) DeleteFilesystem(ctx context.Context, uuid, name string) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.CreateFilesystem })
	if err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	fs := &model.StorageFilesystem{}
	if err = db.Where("cluster_id = ? AND name = ?", cluster.ID, name).Take(fs).Error; err != nil {
		return nil, planError("Storage cluster %s has no file system %s", cluster.Name, name)
	}
	if fs.Status == "creating" {
		return nil, planError("File system %s is being made", name)
	}
	var others int64
	db.Model(&model.StorageFilesystem{}).Where("cluster_id = ? AND id <> ? AND status = ?", cluster.ID, fs.ID, "ready").Count(&others)
	if others == 0 {
		return nil, planError("%s is the only file system of storage cluster %s: delete the cluster instead", name, cluster.Name)
	}
	gone := map[int64]bool{}
	for _, d := range disks {
		if d.FsID == fs.ID {
			d.Status = model.StorageDiskRemoving
			gone[d.ID] = true
		}
	}
	// The role rules of the kind hold for the cluster without the disks of the file system
	params, err := backend.ParseParams([]byte(cluster.Params))
	if err != nil {
		return nil, err
	}
	afterDisks := []*model.StorageClusterDisk{}
	for _, d := range disks {
		if !gone[d.ID] {
			afterDisks = append(afterDisks, d)
		}
	}
	afterNodes, afterPlanDisks := layoutAfter(nodes, afterDisks, nil, nil, -1, 0)
	withDiskRole(afterNodes, afterPlanDisks, backend.DiskRole())
	if err = backend.CheckLayout(afterNodes, afterPlanDisks, params); err != nil {
		return nil, planError("Without the disks of %s: %s", name, planMessage(err))
	}
	steps, err := backend.TaskPlan(StorageTaskDeleteFs, cluster, &StorageTaskScope{Nodes: nodes, Disks: disks})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskDeleteFs, Plan: steps,
		Params: map[string]interface{}{"filesystem_id": fs.ID},
		// Checked with the cluster locked: a pool made meanwhile, another file system gone meanwhile
		Prepare: func(tx *gorm.DB) (int64, error) {
			var pools, others int64
			if err := tx.Model(&model.StoragePool{}).Where("filesystem_id = ?", fs.ID).Count(&pools).Error; err != nil {
				return 0, err
			}
			if pools > 0 {
				return 0, planError("%d storage pools are on file system %s: delete them first", pools, name)
			}
			var mounts int64
			if err := tx.Model(&model.StorageRemoteMount{}).Where("filesystem_id = ?", fs.ID).Count(&mounts).Error; err != nil {
				return 0, err
			}
			if mounts > 0 {
				return 0, planError("Other clusters mount file system %s: delete their remote mounts first", name)
			}
			if err := tx.Model(&model.StorageFilesystem{}).Where("cluster_id = ? AND id <> ? AND status = ?", cluster.ID, fs.ID, "ready").
				Count(&others).Error; err != nil {
				return 0, err
			}
			if others == 0 {
				return 0, planError("%s is the only file system of storage cluster %s: delete the cluster instead", name, cluster.Name)
			}
			locked := &model.StorageFilesystem{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(locked, fs.ID).Error; err != nil {
				return 0, err
			}
			if err := tx.Model(locked).Update("status", "deleting").Error; err != nil {
				return 0, err
			}
			if err := tx.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND fs_id = ?", cluster.ID, fs.ID).
				Update("status", model.StorageDiskRemoving).Error; err != nil {
				return 0, err
			}
			return cluster.ID, storageRefreshReserve(tx, cluster)
		}})
}

// storageTaskFs is the file system a task names (filesystem_id), else the first of the cluster
func storageTaskFs(db *gorm.DB, task *model.StorageTask, cluster *model.StorageCluster) (*model.StorageFilesystem, error) {
	fs := &model.StorageFilesystem{}
	if id := storageTaskInt(task, "filesystem_id"); id > 0 {
		if err := db.Take(fs, id).Error; err != nil {
			return nil, fmt.Errorf("the file system of the task is gone")
		}
		return fs, nil
	}
	if err := db.Where("cluster_id = ?", cluster.ID).Order("id").Take(fs).Error; err != nil {
		return nil, fmt.Errorf("storage cluster %s has no file system", cluster.Name)
	}
	return fs, nil
}
