/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// More file systems on a managed GPFS cluster (shared-storage-design.md §7.3): a new one is made on new disks of the
// members, each host its own failure group, the GPFS storage pools by the media of its own disks (SSD or NVMe with HDD:
// metadata on the fast ones in system, data on the HDDs in data); one with no pool on it is deleted with its NSDs and
// its disks are wiped. Pools pick their file system by name (gpfsPoolParams.Filesystem).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	registerStorageTaskKind("gpfs:"+StorageTaskCreateFs, &storageTaskKind{Slot: storageSlotStructural, Finish: gpfsCreateFsFinish,
		Steps: map[string]*storageStepDef{
			"resolve_disks": {Script: "stc_resolve_disks.sh", Input: gpfsResolveInput},
			"create_nsd":    {Script: "gpfs_nsd.sh", Input: gpfsNSDInput, RetryFrom: "resolve_disks"},
			"create_fs":     {Script: "gpfs_fs.sh", Input: gpfsNewFsInput, RetryFrom: "resolve_disks"},
		}})
	registerStorageTaskKind("gpfs:"+StorageTaskDeleteFs, &storageTaskKind{Slot: storageSlotStructural, Finish: gpfsDeleteFsFinish,
		Steps: map[string]*storageStepDef{
			"delete_fs":     {Script: "gpfs_fs.sh", Input: gpfsDeleteFsInput},
			"release_disks": {Script: "stc_release_disks.sh", Input: gpfsReleaseDisksInput},
		}})
}

// NewFilesystem checks a new file system on the disks given: a block size GPFS takes, as many failure groups (hosts)
// as copies of the data, at least 3 of them outside the test layout (the descriptors of the file system have a quorum
// per failure group, §6.3); its metadata gets three copies once there are three failure groups
func (gpfsBackend) NewFilesystem(cluster *model.StorageCluster, nodes []*model.StorageClusterNode, plan *StorageFilesystemPlan,
	disks []*StorageDiskPlan, params interface{}) (*model.StorageFilesystem, map[string]map[string]interface{}, error) {
	p := params.(*gpfsParams)
	if p.ece() {
		return nil, nil, planError("A cluster of the erasure code layout keeps its file system on its vdisk set")
	}
	if !gpfsFsNameRe.MatchString(plan.Name) {
		return nil, nil, planError("The file system name of a GPFS cluster starts with a letter and has only letters, digits and _, at most 32")
	}
	bs := plan.BlockSize
	if bs == "" {
		bs = "4M"
	}
	if !strings.Contains(" 1M 2M 4M 8M 16M ", " "+bs+" ") {
		return nil, nil, planError("The block size of a GPFS file system must be 1M, 2M, 4M, 8M or 16M")
	}
	hosts := map[int32]bool{}
	fast, slow := false, false
	for _, d := range disks {
		hosts[d.Hostid] = true
		if d.Media == "hdd" {
			slow = true
		} else {
			fast = true
		}
	}
	groups := len(hosts)
	data := plan.DataReplicas
	if data == 0 {
		data = 2
		if p.Test {
			data = 1
		}
	}
	if data < 1 || data > 3 {
		return nil, nil, planError("A GPFS file system keeps 1 to 3 copies of the data")
	}
	if !p.Test && groups < 3 {
		return nil, nil, planError("A file system needs disks on at least 3 hosts (failure groups); %d given", groups)
	}
	if groups < data {
		return nil, nil, planError("%d copies of the data need disks on as many hosts; %d given", data, groups)
	}
	meta := groups
	if meta > 3 {
		meta = 3
	}
	if meta < data {
		meta = data
	}
	diskAttrs := map[string]map[string]interface{}{}
	for _, d := range disks {
		usage, pool := "dataAndMetadata", gpfsPoolSystem
		if fast && slow && d.Media == "hdd" {
			usage, pool = "dataOnly", gpfsPoolData
		}
		diskAttrs[storageDiskKey(d.Hostid, d.DiskID)] = map[string]interface{}{"usage": usage, "gpfs_pool": pool}
	}
	fs := &model.StorageFilesystem{Name: plan.Name, MountPoint: gpfsMountRoot + "/" + plan.Name, BlockSize: bs, DataReplicas: int32(data),
		MetaReplicas: int32(meta), Status: "creating"}
	return fs, diskAttrs, nil
}

// gpfsFsTaskPlan lays out making and deleting a file system: the NSDs of the new disks made, then the file system on
// them and mounted everywhere; or the file system unmounted and deleted with its NSDs, then its disks wiped
func gpfsFsTaskPlan(task string, scope *StorageTaskScope) ([]*StorageStepPlan, error) {
	admins := gpfsWorkingAdmins(scope.Nodes)
	if len(admins) == 0 {
		return nil, planError("No admin host of the cluster is left to run the change")
	}
	step := func(name, s string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: s, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	switch task {
	case StorageTaskCreateFs:
		hosts := gpfsDiskHosts(scope.Disks, model.StorageDiskClaiming)
		if len(hosts) == 0 {
			return nil, planError("A new file system needs new disks")
		}
		return []*StorageStepPlan{step("resolve_disks", n, hosts, 5*time.Minute), step("create_nsd", a, admins, 15*time.Minute),
			step("create_fs", a, admins, 60*time.Minute)}, nil
	case StorageTaskDeleteFs:
		hosts := gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)
		plan := []*StorageStepPlan{step("delete_fs", a, admins, 60*time.Minute)}
		if len(hosts) > 0 {
			plan = append(plan, step("release_disks", n, hosts, 30*time.Minute))
		}
		return plan, nil
	}
	return nil, planError("GPFS clusters have no task %s", task)
}

// gpfsNewFsInput: the new file system of the task on the NSDs of its new disks
func gpfsNewFsInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fs, err := storageTaskFs(db, task, cluster)
	if err != nil {
		return nil, err
	}
	nsds := gpfsDiskStanzas(cluster, nodes, disks, gpfsNewDisks(disks))
	if len(nsds) == 0 {
		return nil, fmt.Errorf("file system %s has no new disk", fs.Name)
	}
	return map[string]interface{}{"action": "create", "cluster_uuid": cluster.UUID, "fs_name": fs.Name, "mount_point": fs.MountPoint,
		"block_size": fs.BlockSize, "data_replicas": fs.DataReplicas, "meta_replicas": fs.MetaReplicas, "nsds": nsds}, nil
}

// gpfsDeleteFsInput: the file system of the task to unmount and delete, and its NSDs
func gpfsDeleteFsInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fs, err := storageTaskFs(db, task, cluster)
	if err != nil {
		return nil, err
	}
	names := gpfsNSDNames(cluster, disks)
	nsds := []string{}
	for _, d := range disks {
		if d.FsID == fs.ID {
			nsds = append(nsds, names[d.ID])
		}
	}
	return map[string]interface{}{"action": "delete", "cluster_uuid": cluster.UUID, "fs_name": fs.Name, "mount_point": fs.MountPoint, "nsds": nsds}, nil
}

// gpfsCreateFsFinish: the file system is ready with its capacity, its disks active under their NSD names; an aborted
// one is in error with its disks failed, to delete
func gpfsCreateFsFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, _, disks, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	fs, err := storageTaskFs(tx, task, cluster)
	if err != nil {
		return err
	}
	if !succeeded {
		if err := tx.Model(fs).Update("status", "error").Error; err != nil {
			return err
		}
		return tx.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageDiskClaiming).
			Updates(map[string]interface{}{"status": model.StorageDiskFailed, "reason": "making the file system was aborted"}).Error
	}
	updates := map[string]interface{}{"status": "ready"}
	if res, err := storageStepResults(tx, task.ID, "create_fs"); err == nil {
		for _, raw := range res {
			r := struct {
				CapacityBytes int64 `json:"capacity_bytes"`
				FreeBytes     int64 `json:"free_bytes"`
			}{}
			if json.Unmarshal(raw, &r) == nil && r.CapacityBytes > 0 {
				updates["capacity_bytes"], updates["free_bytes"], updates["capacity_at"] = r.CapacityBytes, r.FreeBytes, storageNow()
			}
		}
	}
	if err := tx.Model(fs).Updates(updates).Error; err != nil {
		return err
	}
	names := gpfsNSDNames(cluster, disks)
	scan := []int32{}
	for _, d := range gpfsNewDisks(disks) {
		scan = append(scan, d.Hostid)
		if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).
			Updates(map[string]interface{}{"status": model.StorageDiskActive, "name": names[d.ID], "fs_id": fs.ID}).Error; err != nil {
			return err
		}
	}
	go func(ctx context.Context, ids []int32) {
		time.Sleep(3 * time.Second)
		scanStorageHosts(ctx, ids)
	}(context.WithoutCancel(ctx), scan)
	return nil
}

// gpfsDeleteFsFinish: the file system and its disks go; an aborted deletion leaves it in error, its disks failed
func gpfsDeleteFsFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, _, disks, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	fs, err := storageTaskFs(tx, task, cluster)
	if err != nil {
		return err
	}
	if !succeeded {
		if err := tx.Model(fs).Update("status", "error").Error; err != nil {
			return err
		}
		return tx.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND fs_id = ? AND status = ?", cluster.ID, fs.ID, model.StorageDiskRemoving).
			Updates(map[string]interface{}{"status": model.StorageDiskFailed, "reason": "deleting the file system was aborted"}).Error
	}
	scan := []int32{}
	for _, d := range disks {
		if d.FsID == fs.ID {
			scan = append(scan, d.Hostid)
		}
	}
	if err := tx.Where("cluster_id = ? AND fs_id = ?", cluster.ID, fs.ID).Delete(&model.StorageClusterDisk{}).Error; err != nil {
		return err
	}
	if err := tx.Delete(fs).Error; err != nil {
		return err
	}
	go func(ctx context.Context, ids []int32) {
		time.Sleep(3 * time.Second)
		scanStorageHosts(ctx, ids)
	}(context.WithoutCancel(ctx), scan)
	return nil
}
