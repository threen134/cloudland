/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Shared pools: CloudLand pools on a storage cluster (shared-storage-design.md §5.5, §7.4, §9.2). A pool of a managed
// cluster is made, changed and removed by a task of the cluster's backend on the pool slot; what is common to every
// kind lives here: the request checks, the pool lists the hosts keep, their availability reports, and the end of the
// pool tasks.

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
	StorageTaskCreatePool = "create_pool"
	StorageTaskUpdatePool = "update_pool"
	StorageTaskDeletePool = "delete_pool"

	// A host that stopped reporting a shared pool for this long has it unavailable (§9.2)
	sharedPoolReportStale = 15 * time.Minute
	// A shared volume whose creation was never confirmed: the host lost the command or the callback
	sharedVolumePendingTimeout = 10 * time.Minute
)

// SharedPoolCreate is a request for a pool on a storage cluster
type SharedPoolCreate struct {
	Name        string
	ClusterUUID string
	Media       string
	QuotaGB     int64
	OverRatio   float64
	IsDefault   bool
	Description string
	// Kind specific parameters, read by the backend (StorageBackend.PoolSetup)
	Params json.RawMessage
}

// sharedPoolStatuses are the pools of a cluster its hosts keep in their lists and probe: what may hold volumes or
// is being made. A pool in error or being removed is not probed
var sharedPoolStatuses = []string{model.StoragePoolActive, model.StoragePoolDisabled, model.StoragePoolCreating}

// CreateShared records a pool on a storage cluster and starts the task making it there (§7.4). The pool can not be
// chosen until the task succeeds
func (a *StoragePoolAdmin) CreateShared(ctx context.Context, req *SharedPoolCreate) (pool *model.StoragePool, task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if err = validatePoolFields(req.Name, req.Media, "", req.OverRatio); err != nil {
		return
	}
	if req.Name == "" {
		return nil, nil, NewCLError(ErrInvalidParameter, "Pool name is required", nil)
	}
	if req.QuotaGB < 0 || req.QuotaGB > 1<<30 {
		return nil, nil, NewCLError(ErrInvalidParameter, "The quota must be 0 (none) or a number of GB", nil)
	}
	db := dbs.DBContext(ctx)
	cluster := &model.StorageCluster{}
	if err = db.Where("uuid = ?", req.ClusterUUID).Take(cluster).Error; err != nil {
		return nil, nil, NewCLError(ErrStorageClusterNotFound, "Storage cluster not found", err)
	}
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil {
		return
	}
	if !storageClusterCapabilities(backend, cluster).Pools {
		return nil, nil, planError("Pools on %s clusters are not supported yet", cluster.Kind)
	}
	if cluster.Status != model.StorageClusterReady {
		return nil, nil, NewCLError(ErrStorageClusterBusy, fmt.Sprintf("Storage cluster %s is %s, not ready", cluster.Name, cluster.Status), nil)
	}
	if cluster.Mode != model.StorageModeManaged && req.QuotaGB > 0 {
		return nil, nil, planError("A pool of an imported cluster has no quota CloudLand could set")
	}
	ratio := req.OverRatio
	if ratio == 0 {
		ratio = 1
	}
	memberShip := GetMemberShip(ctx)
	pool = &model.StoragePool{
		Model:       model.Model{Creater: memberShip.UserID},
		Name:        req.Name,
		Driver:      backend.PoolDriver(),
		Media:       req.Media,
		Status:      model.StoragePoolCreating,
		OverRatio:   ratio,
		Description: req.Description,
		ClusterID:   cluster.ID,
		QuotaBytes:  req.QuotaGB * gib,
	}
	if _, err = poolDriverOf(pool); err != nil {
		return
	}
	_, nodes, _, err := StorageClusters.Get(ctx, cluster.UUID)
	if err != nil {
		return
	}
	steps, err := backend.TaskPlan(StorageTaskCreatePool, cluster, &StorageTaskScope{Nodes: nodes})
	if err != nil {
		return
	}
	task, err = startStorageTask(ctx, &storageTaskSpec{Backend: cluster.Kind, Kind: StorageTaskCreatePool, Plan: steps,
		Params: map[string]interface{}{"pool_id": &pool.ID, "is_default": req.IsDefault},
		Prepare: func(tx *gorm.DB) (int64, error) {
			// The cluster as it is under its row lock, which the deletion of the cluster takes as well: a cluster
			// that went deleting since it was read gets no pool
			c := &model.StorageCluster{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(c, cluster.ID).Error; err != nil {
				return 0, NewCLError(ErrStorageClusterNotFound, "Storage cluster not found", err)
			}
			if c.Status != model.StorageClusterReady {
				return 0, NewCLError(ErrStorageClusterBusy, fmt.Sprintf("Storage cluster %s is %s, not ready", c.Name, c.Status), nil)
			}
			var count int64
			if err := tx.Model(&model.StoragePool{}).Where("name = ?", req.Name).Count(&count).Error; err != nil {
				return 0, err
			}
			if count > 0 {
				return 0, NewCLError(ErrInvalidParameter, fmt.Sprintf("Storage pool %s already exists", req.Name), nil)
			}
			if err := tx.Create(pool).Error; err != nil {
				return 0, err
			}
			if err := backend.PoolSetup(tx, cluster, pool, req.Params); err != nil {
				return 0, err
			}
			if err := tx.Model(pool).Updates(map[string]interface{}{"filesystem_id": pool.FilesystemID, "driver_params": pool.DriverParams,
				"mount_path": pool.MountPath, "clone_mode": pool.CloneMode, "replicas": pool.Replicas}).Error; err != nil {
				return 0, err
			}
			return cluster.ID, nil
		}})
	if err != nil {
		return nil, nil, err
	}
	return
}

// sharedPoolTaskArgs is what a pool task works on: the pool, and the other parameters
type sharedPoolTaskArgs struct {
	PoolID     int64 `json:"pool_id"`
	QuotaBytes int64 `json:"quota_bytes"`
	IsDefault  bool  `json:"is_default"`
}

func sharedPoolOfTask(db *gorm.DB, task *model.StorageTask) (*model.StoragePool, *sharedPoolTaskArgs, error) {
	args := &sharedPoolTaskArgs{}
	_ = json.Unmarshal([]byte(task.Params), args)
	pool := &model.StoragePool{}
	if err := db.Take(pool, args.PoolID).Error; err != nil {
		return nil, args, fmt.Errorf("the pool of task %d is gone: %v", task.ID, err)
	}
	return pool, args, nil
}

// sharedPoolOfCluster checks a pool is a shared pool of a managed cluster and returns the cluster and its hosts
func sharedPoolCluster(ctx context.Context, pool *model.StoragePool) (cluster *model.StorageCluster, nodes []*model.StorageClusterNode, backend StorageBackend, err error) {
	if !pool.Shared() || pool.ClusterID == 0 {
		return nil, nil, nil, NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s is not on a storage cluster", pool.Name), nil)
	}
	db := dbs.DBContext(ctx)
	cluster = &model.StorageCluster{}
	if err = db.Take(cluster, pool.ClusterID).Error; err != nil {
		return nil, nil, nil, NewCLError(ErrStorageClusterNotFound, "The storage cluster of the pool is gone", err)
	}
	if err = db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&nodes).Error; err != nil {
		return
	}
	backend, err = storageBackendOf(cluster.Kind)
	return
}

// UpdateSharedQuota changes the quota of a shared pool on its cluster (task update_pool)
func (a *StoragePoolAdmin) UpdateSharedQuota(ctx context.Context, pool *model.StoragePool, quotaGB int64) (task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if quotaGB < 0 || quotaGB > 1<<30 {
		return nil, NewCLError(ErrInvalidParameter, "The quota must be 0 (none) or a number of GB", nil)
	}
	cluster, nodes, backend, err := sharedPoolCluster(ctx, pool)
	if err != nil {
		return
	}
	if pool.Status != model.StoragePoolActive && pool.Status != model.StoragePoolDisabled {
		return nil, NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s is %s", pool.Name, pool.Status), nil)
	}
	if cluster.Status != model.StorageClusterReady {
		return nil, NewCLError(ErrStorageClusterBusy, fmt.Sprintf("Storage cluster %s is %s, not ready", cluster.Name, cluster.Status), nil)
	}
	if cluster.Mode != model.StorageModeManaged {
		return nil, planError("A pool of an imported cluster has no quota CloudLand could set")
	}
	steps, err := backend.TaskPlan(StorageTaskUpdatePool, cluster, &StorageTaskScope{Nodes: nodes})
	if err != nil {
		return
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskUpdatePool, Plan: steps,
		Params: map[string]interface{}{"pool_id": pool.ID, "quota_bytes": quotaGB * gib}})
}

// DeleteShared starts the removal of a shared pool from its cluster. A pool with volumes is refused; a pool whose
// making failed (error) is removed the same way, every step of the task skips what is not there
func (a *StoragePoolAdmin) DeleteShared(ctx context.Context, pool *model.StoragePool) (task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	cluster, nodes, backend, err := sharedPoolCluster(ctx, pool)
	if err != nil {
		return
	}
	switch pool.Status {
	case model.StoragePoolCreating, model.StoragePoolDeleting:
		return nil, NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s is %s: a task works on it", pool.Name, pool.Status), nil)
	}
	db := dbs.DBContext(ctx)
	var count int64
	if err = db.Unscoped().Model(&model.Volume{}).Where("storage_pool_id = ? AND deleted_at IS NULL", pool.ID).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to count volumes", err)
	}
	if count > 0 {
		return nil, NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Storage pool %s still has %d volumes", pool.Name, count), nil)
	}
	if err = poolImportsIdle(db, pool); err != nil {
		return
	}
	if cluster.Status != model.StorageClusterReady {
		return nil, NewCLError(ErrStorageClusterBusy, fmt.Sprintf("Storage cluster %s is %s, not ready", cluster.Name, cluster.Status), nil)
	}
	steps, err := backend.TaskPlan(StorageTaskDeletePool, cluster, &StorageTaskScope{Nodes: nodes})
	if err != nil {
		return
	}
	prevStatus := pool.Status
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskDeletePool, Plan: steps,
		Params: map[string]interface{}{"pool_id": pool.ID},
		Prepare: func(tx *gorm.DB) (int64, error) {
			// Locked against a volume created at the same moment
			locked := &model.StoragePool{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(locked, pool.ID).Error; err != nil {
				return 0, err
			}
			if locked.Status != prevStatus {
				return 0, NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s changed meanwhile", pool.Name), nil)
			}
			var count int64
			tx.Unscoped().Model(&model.Volume{}).Where("storage_pool_id = ? AND deleted_at IS NULL", pool.ID).Count(&count)
			if count > 0 {
				return 0, NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Storage pool %s still has %d volumes", pool.Name, count), nil)
			}
			// An import locks the pool row too: none starts from here on
			if err := poolImportsIdle(tx, pool); err != nil {
				return 0, err
			}
			updates := map[string]interface{}{"status": model.StoragePoolDeleting}
			if locked.IsDefault {
				builtin, err := BuiltinPool(ctx)
				if err != nil {
					return 0, err
				}
				if err := a.setDefault(tx, builtin); err != nil {
					return 0, err
				}
				updates["is_default"] = false
			}
			return cluster.ID, tx.Model(&model.StoragePool{}).Where("id = ?", pool.ID).Updates(updates).Error
		}})
}

// poolImportsIdle refuses while an image is being copied into a pool: the import writes into it, launches wait for it
func poolImportsIdle(tx *gorm.DB, pool *model.StoragePool) error {
	var n int64
	if err := tx.Model(&model.ImageStorage{}).Where("storage_pool_id = ? AND status = ?", pool.ID, model.ImageStorageSyncing).Count(&n).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to query the image copies of the pool", err)
	}
	if n > 0 {
		return NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Storage pool %s is busy: %d images are being copied into it", pool.Name, n), nil)
	}
	return nil
}

// poolImageBases are the copies of images in a pool, as the pool scripts remove them with the pool: RBD image names,
// or paths in the pool directory
func poolImageBases(tx *gorm.DB, pool *model.StoragePool) ([]string, error) {
	bases := []string{}
	err := tx.Model(&model.ImageStorage{}).Where("storage_pool_id = ?", pool.ID).Order("id").Pluck("path", &bases).Error
	return bases, err
}

// sharedPoolsOfCluster are the pools the hosts of a cluster keep in their lists
func sharedPoolsOfCluster(db *gorm.DB, clusterID int64) (pools []*model.StoragePool, err error) {
	err = db.Where("cluster_id = ? AND status IN ?", clusterID, sharedPoolStatuses).Order("id").Find(&pools).Error
	return
}

func sharedPoolEntries(pools []*model.StoragePool) []map[string]interface{} {
	entries := []map[string]interface{}{}
	for _, p := range pools {
		if e := sharedPoolEntry(p); e != nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// sharedPoolsSyncInput is the input of the step that gives every host of a cluster its pool list (stc_pools.sh): the
// whole list, and the pool a creation checks right away
func sharedPoolsSyncInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	pools, err := sharedPoolsOfCluster(db, cluster.ID)
	if err != nil {
		return nil, err
	}
	check := ""
	if task.Kind == StorageTaskCreatePool {
		pool, _, err := sharedPoolOfTask(db, task)
		if err != nil {
			return nil, err
		}
		check = pool.UUID
	}
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "pools": sharedPoolEntries(pools), "check": check}, nil
}

// SharedPoolReport is how one host sees one shared pool (shared_pool_status, stc_pools.sh)
type SharedPoolReport struct {
	Pool   string `json:"pool"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	Size   int64  `json:"size"`
	Used   int64  `json:"used"`
	Avail  int64  `json:"avail"`
}

// upsertSharedPoolRow writes how a host sees a shared pool: ready or unavailable, with the capacity it reported
func upsertSharedPoolRow(tx *gorm.DB, hostid int32, pool *model.StoragePool, r *SharedPoolReport, now time.Time) error {
	status := model.HyperPoolUnavailable
	if r.Status == model.HyperPoolReady {
		status = model.HyperPoolReady
	}
	row := &model.HyperStoragePool{}
	err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("hostid = ? AND pool_id = ?", hostid, pool.ID).Take(row).Error
	if err == gorm.ErrRecordNotFound {
		row = &model.HyperStoragePool{Hostid: hostid, PoolID: pool.ID}
	} else if err != nil {
		return err
	}
	row.DeletedAt = gorm.DeletedAt{}
	row.Status, row.Reason = status, truncate(r.Reason, 256)
	row.ReportedStatus, row.ReportedReason = r.Status, truncate(r.Reason, 256)
	row.CheckedAt = &now
	row.Layout = pool.Driver
	if r.Size > 0 {
		row.CapacityBytes, row.UsedBytes, row.AvailBytes, row.CapacityAt = r.Size, r.Used, r.Avail, &now
	}
	if row.ID == 0 {
		return tx.Create(row).Error
	}
	return tx.Unscoped().Save(row).Error
}

// recordSharedPoolCapacity keeps the capacity of a pool as a host that reaches it reported it
func recordSharedPoolCapacity(tx *gorm.DB, pool *model.StoragePool, r *SharedPoolReport, now time.Time) error {
	if r.Status != model.HyperPoolReady || r.Size <= 0 {
		return nil
	}
	return tx.Model(&model.StoragePool{}).Where("id = ?", pool.ID).
		Updates(map[string]interface{}{"capacity_bytes": r.Size, "used_bytes": r.Used, "capacity_at": &now}).Error
}

// HandleSharedPoolStatus takes the shared pool reports of a host's heartbeat (§9.2). Only a host of the pool's
// cluster has a say on it
func HandleSharedPoolStatus(ctx context.Context, hostid int32, reports []*SharedPoolReport) error {
	db := dbs.DBContext(ctx)
	now := time.Now()
	for _, r := range reports {
		if !validUUID(r.Pool) {
			continue
		}
		pool := &model.StoragePool{}
		if err := db.Where("uuid = ?", r.Pool).Take(pool).Error; err != nil || !pool.Shared() {
			continue
		}
		if pool.Status == model.StoragePoolDeleting || pool.Status == model.StoragePoolError {
			continue
		}
		var member int64
		db.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", pool.ClusterID, hostid).Count(&member)
		if member == 0 {
			logger.Ctx(ctx).Warningf("Host %d reported shared pool %s of a cluster it is not in", hostid, pool.Name)
			continue
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := upsertSharedPoolRow(tx, hostid, pool, r, now); err != nil {
				return err
			}
			return recordSharedPoolCapacity(tx, pool, r, now)
		})
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to record shared pool %s on host %d: %v", pool.Name, hostid, err)
		}
	}
	return nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'):
		default:
			return false
		}
	}
	return true
}

// sharedPoolTaskFinish ends a pool task (create, update, delete) of any kind
func sharedPoolTaskFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	pool, args, err := sharedPoolOfTask(tx, task)
	if err != nil {
		return err
	}
	switch task.Kind {
	case StorageTaskCreatePool:
		if !succeeded {
			return tx.Model(pool).Updates(map[string]interface{}{"status": model.StoragePoolError, "is_default": false}).Error
		}
		if err := recordSharedPoolChecks(tx, task, pool); err != nil {
			return err
		}
		if err := tx.Model(pool).Update("status", model.StoragePoolActive).Error; err != nil {
			return err
		}
		if args.IsDefault {
			return storagePoolAdmin.setDefault(tx, pool)
		}
		return nil
	case StorageTaskUpdatePool:
		if !succeeded {
			return nil
		}
		return tx.Model(pool).Update("quota_bytes", args.QuotaBytes).Error
	case StorageTaskDeletePool:
		if !succeeded {
			return tx.Model(pool).Update("status", model.StoragePoolError).Error
		}
		if err := tx.Unscoped().Where("pool_id = ?", pool.ID).Delete(&model.HyperStoragePool{}).Error; err != nil {
			return err
		}
		// The copies of images went with the pool
		if err := tx.Unscoped().Where("image_storage_id IN (?)", tx.Model(&model.ImageStorage{}).Select("id").Where("storage_pool_id = ?", pool.ID)).
			Delete(&model.ImageStorageWaiter{}).Error; err != nil {
			return err
		}
		if err := tx.Where("storage_pool_id = ?", pool.ID).Delete(&model.ImageStorage{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(pool).Error
	}
	return fmt.Errorf("task %s is not a pool task", task.Kind)
}

// recordSharedPoolChecks writes how every host of the cluster saw a new pool in the check of the creation; a host
// that was away is unavailable until its first report
func recordSharedPoolChecks(tx *gorm.DB, task *model.StorageTask, pool *model.StoragePool) error {
	_, nodes, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	results, err := storageStepResults(tx, task.ID, "sync_pools")
	if err != nil {
		return err
	}
	now := time.Now()
	for _, n := range nodes {
		r := &SharedPoolReport{Pool: pool.UUID, Status: model.HyperPoolUnavailable, Reason: "not checked yet: the host was offline"}
		if raw, ok := results[n.Hostid]; ok {
			got := struct {
				Check *SharedPoolReport `json:"check"`
			}{}
			if json.Unmarshal(raw, &got) == nil && got.Check != nil && got.Check.Pool == pool.UUID {
				r = got.Check
			}
		}
		if err := upsertSharedPoolRow(tx, n.Hostid, pool, r, now); err != nil {
			return err
		}
		if err := recordSharedPoolCapacity(tx, pool, r, now); err != nil {
			return err
		}
	}
	return nil
}

// registerSharedPoolTasks registers the pool tasks of a backend: the kind specific steps plus the common step giving
// every host its pool list. A kind that imports clusters registers the steps of both, its plan picks
func registerSharedPoolTasks(kind string, steps map[string]map[string]*storageStepDef) {
	for task, defs := range steps {
		defs["sync_pools"] = &storageStepDef{Script: "stc_pools.sh", Input: sharedPoolsSyncInput, OnlineOnly: true}
		registerStorageTaskKind(kind+":"+task, &storageTaskKind{Slot: storageSlotPool, Steps: defs, Finish: sharedPoolTaskFinish})
	}
}

// SyncSharedPools sends every host of the ready clusters its pool list again (§9.2): a host that was offline when a
// pool was made or removed, or lost a command, catches up within a round
func SyncSharedPools(ctx context.Context) {
	db := dbs.DBContext(ctx)
	clusters := []*model.StorageCluster{}
	if err := db.Where("status = ?", model.StorageClusterReady).Find(&clusters).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to list the storage clusters: %v", err)
		return
	}
	for _, c := range clusters {
		pools, err := sharedPoolsOfCluster(db, c.ID)
		if err != nil {
			continue
		}
		body, _ := json.Marshal(sharedPoolEntries(pools))
		nodes := []*model.StorageClusterNode{}
		db.Where("cluster_id = ?", c.ID).Find(&nodes)
		for _, n := range nodes {
			if _, online := hostOnline(db, n.Hostid); !online {
				continue
			}
			command := fmt.Sprintf("/opt/cloudland/scripts/backend/sync_shared_pools.sh '%s' <<'EOF'\n%s\nEOF", ShellEscape(c.UUID), body)
			if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", n.Hostid), command); err != nil {
				logger.Ctx(ctx).Warningf("Failed to send the pool list of cluster %s to host %d: %v", c.Name, n.Hostid, err)
			}
		}
	}
}

// maintainSharedPools applies the time-based rules of the shared pools: hosts that stopped reporting, and volumes
// whose creation was never confirmed
func maintainSharedPools(ctx context.Context, now time.Time) {
	db := dbs.DBContext(ctx)
	// The rule of the local pools needs capacity_at, which a host without the pool mounted never sets
	db.Model(&model.HyperStoragePool{}).
		Where("status = ? AND (checked_at IS NULL OR checked_at < ?)", model.HyperPoolReady, now.Add(-sharedPoolReportStale)).
		Where("pool_id IN (SELECT id FROM storage_pools WHERE driver <> ?)", model.StorageDriverLocal).
		Updates(map[string]interface{}{"status": model.HyperPoolUnavailable, "reason": model.ReasonNodeOffline})
	db.Model(&model.Volume{}).
		Where("status = ? AND booting = ? AND updated_at < ?", model.VolumeStatusPending, false, now.Add(-sharedVolumePendingTimeout)).
		Where("storage_pool_id IN (SELECT id FROM storage_pools WHERE driver <> ?)", model.StorageDriverLocal).
		Updates(map[string]interface{}{"status": model.VolumeStatusError, "reason": "the host did not confirm the creation; delete the volume"})
}
