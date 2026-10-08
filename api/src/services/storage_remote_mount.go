/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Remote mounts (shared-storage-design.md §7.11): a file system of one managed cluster mounted by the hosts of another
// cluster of the kind (GPFS multi-cluster: a host is in one GPFS cluster only, so it reaches another cluster's file
// system this way). The file system keeps its name and mount point on the hosts that mount it, so the pools on it
// have the same paths everywhere and those hosts get them in their pool lists like the owner's hosts. The work is a
// task of the cluster that mounts (its admin host and the owner's take turns: keys, grant, mount); the kind lays out
// the steps (storageRemoteMounter).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StorageTaskRemoteMount   = "remote_mount"
	StorageTaskRemoteUnmount = "remote_unmount"

	StorageRemoteMounting   = "mounting"
	StorageRemoteReady      = "ready"
	StorageRemoteUnmounting = "unmounting"
	StorageRemoteError      = "error"
)

// storageRemoteMounter is a kind whose clusters mount each other's file systems (gpfs)
type storageRemoteMounter interface {
	RemoteMountPlan(owner, access *model.StorageCluster, ownerNodes, accessNodes []*model.StorageClusterNode) ([]*StorageStepPlan, error)
	RemoteUnmountPlan(owner, access *model.StorageCluster, ownerNodes, accessNodes []*model.StorageClusterNode) ([]*StorageStepPlan, error)
}

// StorageRemoteMountView is a remote mount as the API shows it
type StorageRemoteMountView struct {
	Mount  *model.StorageRemoteMount
	Owner  *model.StorageCluster
	Access *model.StorageCluster
}

// RemoteMounts are the remote mounts a cluster has a part in: its file systems others mount, and the ones it mounts
func (a *StorageClusterAdmin) RemoteMounts(ctx context.Context, uuid string) ([]*StorageRemoteMountView, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, _, _, err := a.Get(ctx, uuid)
	if err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	mounts := []*model.StorageRemoteMount{}
	if err = db.Where("owner_cluster_id = ? OR access_cluster_id = ?", cluster.ID, cluster.ID).Order("id").Find(&mounts).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to list the remote mounts", err)
	}
	out := []*StorageRemoteMountView{}
	for _, m := range mounts {
		owner, access := &model.StorageCluster{}, &model.StorageCluster{}
		db.Unscoped().Take(owner, m.OwnerClusterID)
		db.Unscoped().Take(access, m.AccessClusterID)
		out = append(out, &StorageRemoteMountView{Mount: m, Owner: owner, Access: access})
	}
	return out, nil
}

// remoteMountClusters checks the two clusters of a remote mount: managed clusters of one kind that mounts remotely,
// ready, not the same
func remoteMountClusters(ctx context.Context, ownerUUID, accessUUID string) (owner, access *model.StorageCluster, ownerNodes,
	accessNodes []*model.StorageClusterNode, mounter storageRemoteMounter, err error) {
	allowed := func(c *StorageCapabilities) bool { return c.RemoteMount }
	if owner, ownerNodes, _, _, err = storageClusterForRemote(ctx, ownerUUID, allowed); err != nil {
		return
	}
	if access, accessNodes, _, _, err = storageClusterForChange(ctx, accessUUID, allowed); err != nil {
		return
	}
	if owner.ID == access.ID {
		err = planError("A cluster mounts its own file systems already")
		return
	}
	if owner.Kind != access.Kind {
		err = planError("Only a %s cluster mounts the file systems of %s", owner.Kind, owner.Name)
		return
	}
	backend, berr := storageBackendOf(owner.Kind)
	if berr != nil {
		err = berr
		return
	}
	var ok bool
	if mounter, ok = backend.(storageRemoteMounter); !ok {
		err = planError("%s clusters do not mount each other's file systems", owner.Kind)
	}
	return
}

// storageClusterForRemote: the owner of a remote mount must be ready and managed, but its own tasks may run (the
// grant does not change its layout)
func storageClusterForRemote(ctx context.Context, uuid string, allowed func(*StorageCapabilities) bool) (cluster *model.StorageCluster,
	nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk, backend StorageBackend, err error) {
	if cluster, nodes, disks, err = StorageClusters.Get(ctx, uuid); err != nil {
		return
	}
	if backend, err = storageBackendOf(cluster.Kind); err != nil {
		return
	}
	if !allowed(storageClusterCapabilities(backend, cluster)) {
		err = planError("%s clusters (%s) do not support this operation yet", cluster.Kind, cluster.Layout)
		return
	}
	if cluster.Mode != model.StorageModeManaged {
		err = planError("Storage cluster %s is imported: CloudLand does not change it", cluster.Name)
		return
	}
	if cluster.Status != model.StorageClusterReady {
		err = NewCLError(ErrStorageClusterBusy, fmt.Sprintf("Storage cluster %s is %s, not ready", cluster.Name, cluster.Status), nil)
	}
	return
}

// CreateRemoteMount lets the hosts of another cluster mount a file system of this one
func (a *StorageClusterAdmin) CreateRemoteMount(ctx context.Context, ownerUUID, filesystem, accessUUID string) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	owner, access, ownerNodes, accessNodes, mounter, err := remoteMountClusters(ctx, ownerUUID, accessUUID)
	if err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	fs := &model.StorageFilesystem{}
	if err = db.Where("cluster_id = ? AND name = ? AND status = ?", owner.ID, filesystem, "ready").Take(fs).Error; err != nil {
		return nil, planError("Storage cluster %s has no ready file system %s", owner.Name, filesystem)
	}
	steps, err := mounter.RemoteMountPlan(owner, access, ownerNodes, accessNodes)
	if err != nil {
		return nil, err
	}
	mount := &model.StorageRemoteMount{OwnerClusterID: owner.ID, AccessClusterID: access.ID, FilesystemID: fs.ID, Name: fs.Name,
		MountPoint: fs.MountPoint, Status: StorageRemoteMounting}
	params := map[string]interface{}{}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: access.ID, Backend: access.Kind, Kind: StorageTaskRemoteMount, Plan: steps,
		Params: params,
		// The name and mount point must be free on the cluster that mounts: its own file systems and other mounts
		Prepare: func(tx *gorm.DB) (int64, error) {
			locked := &model.StorageCluster{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(locked, access.ID).Error; err != nil {
				return 0, err
			}
			var taken int64
			tx.Model(&model.StorageFilesystem{}).Where("cluster_id = ? AND (name = ? OR mount_point = ?)", access.ID, fs.Name, fs.MountPoint).Count(&taken)
			if taken > 0 {
				return 0, planError("Storage cluster %s has a file system named %s or mounted at %s: the remote file system keeps its name and mount point",
					access.Name, fs.Name, fs.MountPoint)
			}
			tx.Model(&model.StorageRemoteMount{}).Where("access_cluster_id = ? AND (name = ? OR mount_point = ?)", access.ID, fs.Name, fs.MountPoint).Count(&taken)
			if taken > 0 {
				return 0, planError("Storage cluster %s mounts a remote file system named %s or at %s already", access.Name, fs.Name, fs.MountPoint)
			}
			if err := tx.Create(mount).Error; err != nil {
				return 0, err
			}
			params["mount_id"] = mount.ID
			return access.ID, nil
		}})
}

// DeleteRemoteMount stops the hosts of the other cluster mounting the file system: refused while instances on them use
// volumes of its pools
func (a *StorageClusterAdmin) DeleteRemoteMount(ctx context.Context, ownerUUID, mountUUID string) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	mount := &model.StorageRemoteMount{}
	if err := db.Where("uuid = ?", mountUUID).Take(mount).Error; err != nil {
		return nil, planError("No such remote mount")
	}
	owner, access := &model.StorageCluster{}, &model.StorageCluster{}
	if err := db.Take(owner, mount.OwnerClusterID).Error; err != nil || (owner.UUID != ownerUUID && ownerUUID != "") {
		return nil, planError("The remote mount is not of storage cluster %s", ownerUUID)
	}
	if err := db.Take(access, mount.AccessClusterID).Error; err != nil {
		return nil, planError("The cluster that mounts is gone")
	}
	if mount.Status != StorageRemoteReady && mount.Status != StorageRemoteError {
		return nil, planError("The remote mount is %s", mount.Status)
	}
	busy, err := remoteMountUsers(db, mount)
	if err != nil {
		return nil, err
	}
	if busy > 0 {
		return nil, NewCLError(ErrStoragePoolInUse, fmt.Sprintf("%d volumes of the pools on %s are attached to instances on hosts of %s: move or stop them first",
			busy, mount.Name, access.Name), nil)
	}
	_, ownerNodes, _, _, err := storageClusterForRemote(ctx, owner.UUID, func(c *StorageCapabilities) bool { return c.RemoteMount })
	if err != nil {
		return nil, err
	}
	_, accessNodes, _, backend, err := storageClusterForChange(ctx, access.UUID, func(c *StorageCapabilities) bool { return c.RemoteMount })
	if err != nil {
		return nil, err
	}
	mounter, ok := backend.(storageRemoteMounter)
	if !ok {
		return nil, planError("%s clusters do not mount each other's file systems", access.Kind)
	}
	steps, err := mounter.RemoteUnmountPlan(owner, access, ownerNodes, accessNodes)
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: access.ID, Backend: access.Kind, Kind: StorageTaskRemoteUnmount, Plan: steps,
		Params: map[string]interface{}{"mount_id": mount.ID},
		Prepare: func(tx *gorm.DB) (int64, error) {
			locked := &model.StorageRemoteMount{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(locked, mount.ID).Error; err != nil {
				return 0, err
			}
			if n, err := remoteMountUsers(tx, locked); err != nil || n > 0 {
				return 0, NewCLError(ErrStoragePoolInUse, fmt.Sprintf("%d volumes of the pools on %s are attached to instances on hosts of %s", n, mount.Name, access.Name), err)
			}
			return access.ID, tx.Model(locked).Update("status", StorageRemoteUnmounting).Error
		}})
}

// remoteMountUsers counts the volumes of the pools on the file system of a remote mount attached to instances on the
// hosts of the cluster that mounts it
func remoteMountUsers(db *gorm.DB, mount *model.StorageRemoteMount) (int64, error) {
	var busy int64
	err := db.Model(&model.Volume{}).Joins("JOIN instances i ON i.id = volumes.instance_id AND i.deleted_at IS NULL").
		Where("volumes.storage_pool_id IN (SELECT id FROM storage_pools WHERE filesystem_id = ? AND deleted_at IS NULL)", mount.FilesystemID).
		Where("i.hyper IN (SELECT hostid FROM storage_cluster_nodes WHERE cluster_id = ? AND deleted_at IS NULL)", mount.AccessClusterID).
		Count(&busy).Error
	if err != nil {
		return 0, NewCLError(ErrSQLSyntaxError, "Failed to count the volumes in use", err)
	}
	return busy, nil
}

// storageRemoteMountOf is the remote mount a task works on
func storageRemoteMountOf(db *gorm.DB, task *model.StorageTask) (mount *model.StorageRemoteMount, owner, access *model.StorageCluster, err error) {
	mount = &model.StorageRemoteMount{}
	if err = db.Take(mount, storageTaskInt(task, "mount_id")).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("the remote mount of the task is gone")
	}
	owner, access = &model.StorageCluster{}, &model.StorageCluster{}
	if err = db.Take(owner, mount.OwnerClusterID).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("the owner of the remote mount is gone")
	}
	if err = db.Take(access, mount.AccessClusterID).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("the cluster that mounts is gone")
	}
	return
}

// storageRemoteMountFinish: a mount is ready, and the hosts of the cluster that mounts get the owner's pools on the file
// system; an aborted one is an error to delete
func storageRemoteMountFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	mount, _, _, err := storageRemoteMountOf(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		return tx.Model(mount).Updates(map[string]interface{}{"status": StorageRemoteError, "reason": "the mount was aborted; delete it"}).Error
	}
	if err := tx.Model(mount).Updates(map[string]interface{}{"status": StorageRemoteReady, "reason": ""}).Error; err != nil {
		return err
	}
	go func(ctx context.Context) {
		time.Sleep(3 * time.Second)
		SyncSharedPools(ctx)
	}(context.WithoutCancel(ctx))
	return nil
}

// storageRemoteUnmountFinish: the mount goes, the hosts of the cluster that mounted lose the pools on the file system
func storageRemoteUnmountFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	mount, owner, access, err := storageRemoteMountOf(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		return tx.Model(mount).Updates(map[string]interface{}{"status": StorageRemoteError, "reason": "the unmount was aborted; delete it again"}).Error
	}
	if err := tx.Delete(mount).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("pool_id IN (SELECT id FROM storage_pools WHERE filesystem_id = ?)", mount.FilesystemID).
		Where("hostid IN (SELECT hostid FROM storage_cluster_nodes WHERE cluster_id = ? AND deleted_at IS NULL)", access.ID).
		Where("hostid NOT IN (SELECT hostid FROM storage_cluster_nodes WHERE cluster_id = ? AND deleted_at IS NULL)", owner.ID).
		Delete(&model.HyperStoragePool{}).Error; err != nil {
		return err
	}
	go func(ctx context.Context) {
		time.Sleep(3 * time.Second)
		syncRemotePoolLists(ctx, owner, access)
	}(context.WithoutCancel(ctx))
	return nil
}

// remoteMountsOfOwner are the ready mounts of the file systems of a cluster by other clusters
func remoteMountsOfOwner(db *gorm.DB, ownerID int64) ([]*model.StorageRemoteMount, error) {
	mounts := []*model.StorageRemoteMount{}
	err := db.Where("owner_cluster_id = ? AND status = ?", ownerID, StorageRemoteReady).Order("id").Find(&mounts).Error
	return mounts, err
}

// syncRemotePoolLists gives the hosts of a cluster that mounts file systems of an owner the owner's pools on them
// (under the owner's uuid; none any more when it mounts none)
func syncRemotePoolLists(ctx context.Context, owner, access *model.StorageCluster) {
	db := dbs.DBContext(ctx)
	mounts := []*model.StorageRemoteMount{}
	if err := db.Where("owner_cluster_id = ? AND access_cluster_id = ? AND status = ?", owner.ID, access.ID, StorageRemoteReady).Find(&mounts).Error; err != nil {
		return
	}
	fsIDs := []int64{}
	for _, m := range mounts {
		fsIDs = append(fsIDs, m.FilesystemID)
	}
	pools := []*model.StoragePool{}
	if len(fsIDs) > 0 {
		if err := db.Where("cluster_id = ? AND filesystem_id IN ? AND status IN ?", owner.ID, fsIDs, sharedPoolStatuses).Order("id").Find(&pools).Error; err != nil {
			return
		}
	}
	body, _ := json.Marshal(sharedPoolEntries(pools))
	nodes := []*model.StorageClusterNode{}
	db.Where("cluster_id = ?", access.ID).Find(&nodes)
	for _, n := range nodes {
		if _, online := hostOnline(db, n.Hostid); !online {
			continue
		}
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/sync_shared_pools.sh '%s' remote <<'EOF'\n%s\nEOF", ShellEscape(owner.UUID), body)
		if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", n.Hostid), command); err != nil {
			logger.Ctx(ctx).Warningf("Failed to send the pools of cluster %s to host %d: %v", owner.Name, n.Hostid, err)
		}
	}
}

// remoteMountHostLeft: a host leaves a cluster that mounts file systems of other clusters. Its rows of the owners'
// pools go with it: its reports of them are not taken any more, so they would stay ready for good and admission,
// evacuation and migration would keep picking it. Returns the uuids of the owners, whose (empty) pool lists the host
// is given once the transaction is committed (clearRemotePoolLists)
func remoteMountHostLeft(tx *gorm.DB, accessID int64, hostid int32) ([]string, error) {
	owners := []*model.StorageCluster{}
	if err := tx.Where("id IN (SELECT owner_cluster_id FROM storage_remote_mounts WHERE access_cluster_id = ? AND deleted_at IS NULL)", accessID).
		Find(&owners).Error; err != nil {
		return nil, err
	}
	uuids := []string{}
	for _, o := range owners {
		if err := tx.Unscoped().Where("hostid = ? AND pool_id IN (SELECT id FROM storage_pools WHERE cluster_id = ?)", hostid, o.ID).
			Where("hostid NOT IN (SELECT hostid FROM storage_cluster_nodes WHERE cluster_id = ? AND deleted_at IS NULL)", o.ID).
			Delete(&model.HyperStoragePool{}).Error; err != nil {
			return nil, err
		}
		uuids = append(uuids, o.UUID)
	}
	return uuids, nil
}

// clearRemotePoolLists gives a host that left the cluster mounting them empty pool lists of the owners: its probes
// of their pools stop
func clearRemotePoolLists(ctx context.Context, hostid int32, owners []string) {
	db := dbs.DBContext(ctx)
	if _, online := hostOnline(db, hostid); !online {
		return
	}
	for _, uuid := range owners {
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/sync_shared_pools.sh '%s' remote <<'EOF'\n[]\nEOF", ShellEscape(uuid))
		if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", hostid), command); err != nil {
			logger.Ctx(ctx).Warningf("Failed to clear the pools of cluster %s on host %d: %v", uuid, hostid, err)
		}
	}
}

// sharedPoolRemoteHost: whether a host that is no member of a pool's cluster reaches the pool through a remote mount
// of its file system by its own cluster; its cluster
func sharedPoolRemoteHost(db *gorm.DB, pool *model.StoragePool, hostid int32) (int64, bool) {
	if pool.FilesystemID == 0 {
		return 0, false
	}
	mount := &model.StorageRemoteMount{}
	err := db.Where("filesystem_id = ? AND status = ?", pool.FilesystemID, StorageRemoteReady).
		Where("access_cluster_id IN (SELECT cluster_id FROM storage_cluster_nodes WHERE hostid = ? AND deleted_at IS NULL)", hostid).
		Take(mount).Error
	if err != nil {
		return 0, false
	}
	return mount.AccessClusterID, true
}

// storageRemoteMountsOf counts the remote mounts a cluster has a part in, owner or mounting: such a cluster is not
// deleted, nor is a file system others mount
func storageRemoteMountsOf(db *gorm.DB, clusterID int64) int64 {
	var n int64
	db.Model(&model.StorageRemoteMount{}).Where("owner_cluster_id = ? OR access_cluster_id = ?", clusterID, clusterID).Count(&n)
	return n
}
