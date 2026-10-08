/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Fencing a host that is down (shared-storage-design.md §11.2): before its instances are started elsewhere, the
// storage clusters their disks are on are told to refuse the host, so that if it was only cut off from the control
// plane and still runs them, it can not write to the disks the recovered copies use. GPFS expels the node
// (mmexpelnode, kept when it comes back); Ceph blocklists its address with a long expiry. The host is let back in
// once it came back and removed its copies (§11.4). Fences are tasks of no slot: a failed structural task of the
// cluster must not keep a host from being fenced

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StorageTaskFence   = "fence"
	StorageTaskUnfence = "unfence"

	storageFenceTimeout = 15 * time.Minute
	// A fence that failed, or failed to be lifted, is tried again this long after
	storageUnfenceRetry = 5 * time.Minute
	// A fence still without its task this long after it was recorded lost it (clapi went away in between)
	storageFenceTaskLost = 5 * time.Minute
	// How long a Ceph blocklist entry of a fence lasts: as good as forever, it is removed when the host is let back
	// in. Without an expiry Ceph keeps it an hour (mon_osd_blocklist_default_expire) and the old writer comes back
	storageBlocklistExpire = 10 * 365 * 24 * 3600
)

// storageFencer is what a backend does to fence: the step fencing a host (or letting it back in), run on another host
// of the cluster. A nil plan with a reason: CloudLand can not fence the host on this cluster
type storageFencer interface {
	FencePlan(db *gorm.DB, cluster *model.StorageCluster, nodes []*model.StorageClusterNode, hostid int32, kind string) (*StorageStepPlan, string)
}

type storageFenceParams struct {
	FenceID int64  `json:"fence_id"`
	Hostid  int32  `json:"hostid"`
	Target  string `json:"target"`
}

func liveStorageFence(db *gorm.DB, clusterID int64, hostid int32) (*model.StorageFence, error) {
	f := &model.StorageFence{}
	err := db.Where("cluster_id = ? AND hostid = ?", clusterID, hostid).Order("id DESC").Take(f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return f, err
}

// onlineStepHosts keeps the hosts of a plan that are online, the step runs on the first of them
func onlineStepHosts(db *gorm.DB, plan *StorageStepPlan) *StorageStepPlan {
	if plan == nil {
		return nil
	}
	hosts := []int32{}
	for _, h := range plan.Hostids {
		if _, online := hostOnline(db, h); online {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	p := *plan
	p.Hostids = hosts
	return &p
}

// ensureStorageFence makes sure a host is fenced on a cluster, or being fenced. confirmedBy: the admin who confirmed
// the host is off; it stands in for a fence CloudLand can not run. It returns the fence; an error when the host can
// not be fenced and nobody confirmed it is off
func ensureStorageFence(ctx context.Context, cluster *model.StorageCluster, hostid int32, confirmedBy string) (fence *model.StorageFence, err error) {
	db := dbs.DBContext(ctx)
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil {
		return nil, err
	}
	nodes := []*model.StorageClusterNode{}
	if err = db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&nodes).Error; err != nil {
		return nil, err
	}
	var plan *StorageStepPlan
	reason := fmt.Sprintf("storage type %s has no fencing", cluster.Kind)
	if f, ok := backend.(storageFencer); ok {
		plan, reason = f.FencePlan(db, cluster, nodes, hostid, StorageTaskFence)
		if plan != nil && onlineStepHosts(db, plan) == nil {
			plan, reason = nil, "no host of the cluster that could fence it is online"
		}
	}
	// Decided and recorded under the lock of the cluster: a request and the worker (or two requests) fencing the host
	// at once would each make a fence. The task starts after: a fence without its task yet is left alone by
	// maintainStorageFences
	start := false
	err = db.Transaction(func(tx *gorm.DB) error {
		if lerr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", cluster.ID).Take(&model.StorageCluster{}).Error; lerr != nil {
			return lerr
		}
		f, lerr := liveStorageFence(tx, cluster.ID, hostid)
		if lerr != nil {
			return lerr
		}
		if f != nil {
			switch f.Status {
			case model.StorageFenceFenced, model.StorageFenceConfirmed, model.StorageFenceFencing:
				fence = f
				return nil
			case model.StorageFenceUnfencing:
				return NewCLError(ErrStorageFenceUnavailable, fmt.Sprintf("%s is being let back into storage cluster %s", hostName(tx, hostid), cluster.Name), nil)
			}
			// Failed (or failed to be lifted): fenced again, with a new task
		} else {
			f = &model.StorageFence{ClusterID: cluster.ID, Hostid: hostid}
		}
		if plan == nil {
			if confirmedBy == "" {
				return NewCLError(ErrStorageFenceUnavailable, fmt.Sprintf("%s can not be fenced on storage cluster %s: %s. Confirm it is powered off to go on",
					hostName(tx, hostid), cluster.Name, reason), nil)
			}
			now := time.Now()
			f.Status, f.Method, f.ConfirmedBy, f.FencedAt, f.Message, f.TaskID = model.StorageFenceConfirmed,
				model.StorageFenceByAdmin, confirmedBy, &now, truncate("confirmed off by "+confirmedBy+": "+reason, 512), 0
		} else {
			f.Status, f.Message, f.ConfirmedBy, f.TaskID = model.StorageFenceFencing, "", confirmedBy, 0
			start = true
		}
		fence = f
		return tx.Save(f).Error
	})
	if err != nil {
		return nil, err
	}
	if !start {
		return
	}
	task, err := startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskFence,
		Plan: []*StorageStepPlan{plan}, Params: &storageFenceParams{FenceID: fence.ID, Hostid: hostid}})
	if err != nil {
		db.Model(fence).Updates(map[string]interface{}{"status": model.StorageFenceFailed, "message": truncate(err.Error(), 512)})
		return nil, err
	}
	err = db.Model(fence).Update("task_id", task.ID).Error
	fence.TaskID = task.ID
	return
}

// storageFenceDone marks the fence of a task done: run in the transaction of its step
func storageFenceDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	p := &storageFenceParams{}
	if err := json.Unmarshal([]byte(task.Params), p); err != nil || p.FenceID == 0 {
		return fmt.Errorf("the task names no fence")
	}
	target := ""
	for _, r := range runs {
		res := map[string]string{}
		if json.Unmarshal([]byte(r.Result), &res) == nil {
			target = res["node"] + res["address"]
		}
	}
	method := model.StorageFenceExpel
	if task.Backend == model.StorageKindCeph {
		method = model.StorageFenceBlocklist
	}
	now := time.Now()
	return tx.Model(&model.StorageFence{}).Where("id = ?", p.FenceID).Updates(map[string]interface{}{
		"status": model.StorageFenceFenced, "method": method, "target": target, "fenced_at": &now, "message": ""}).Error
}

// storageUnfenceDone removes the fence once the host is let back in
func storageUnfenceDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	p := &storageFenceParams{}
	if err := json.Unmarshal([]byte(task.Params), p); err != nil || p.FenceID == 0 {
		return fmt.Errorf("the task names no fence")
	}
	return tx.Delete(&model.StorageFence{}, p.FenceID).Error
}

// registerStorageFenceKinds registers the fence and unfence tasks of a backend, with the script that runs them
func registerStorageFenceKinds(backend string, fence, unfence *storageStepDef) {
	fence.Done, unfence.Done = storageFenceDone, storageUnfenceDone
	registerStorageTaskKind(storageTaskKindName(backend, StorageTaskFence), &storageTaskKind{Slot: storageSlotNone,
		Steps: map[string]*storageStepDef{"fence": fence}})
	registerStorageTaskKind(storageTaskKindName(backend, StorageTaskUnfence), &storageTaskKind{Slot: storageSlotNone,
		Steps: map[string]*storageStepDef{"unfence": unfence}})
}

// maintainStorageFences follows the fences whose task ended without the step succeeding (failed, aborted): the fence
// failed. A fence still waiting gives a host that came back the chance to be let in
func maintainStorageFences(ctx context.Context) {
	db := dbs.DBContext(ctx)
	fences := []*model.StorageFence{}
	if err := db.Where("status IN ?", []string{model.StorageFenceFencing, model.StorageFenceUnfencing}).Find(&fences).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to list the storage fences: %v", err)
		return
	}
	for _, f := range fences {
		if f.TaskID == 0 {
			// Recorded, its task not started yet; given up when it never came
			if time.Since(f.UpdatedAt) > storageFenceTaskLost {
				status := model.StorageFenceFailed
				if f.Status == model.StorageFenceUnfencing {
					status = model.StorageFenceUnfenceFailed
				}
				db.Model(f).Where("status = ? AND task_id = 0", f.Status).Updates(map[string]interface{}{"status": status,
					"message": "the task was never started"})
			}
			continue
		}
		task := &model.StorageTask{}
		if err := db.Take(task, f.TaskID).Error; err != nil {
			continue
		}
		if task.Status != model.StorageTaskFailed && task.Status != model.StorageTaskAborted {
			continue
		}
		status := model.StorageFenceFailed
		if f.Status == model.StorageFenceUnfencing {
			status = model.StorageFenceUnfenceFailed
		}
		msg := task.Message
		if msg == "" {
			msg = "the task " + task.Status
		}
		db.Model(f).Where("status = ?", f.Status).Updates(map[string]interface{}{"status": status, "message": truncate(msg, 512)})
	}
	hostids := []int32{}
	db.Model(&model.StorageFence{}).Where("status IN ?", storageFencesToLift).Distinct("hostid").Pluck("hostid", &hostids)
	for _, h := range hostids {
		checkHostUnfence(ctx, h)
	}
}

// hostStillHeld tells whether a host must stay fenced: it is offline, or it has not reconciled since it was fenced,
// or an evacuation from it is going on, or one is done and the host has not removed its copy
func hostStillHeld(db *gorm.DB, hostid int32, fencedSince time.Time) (bool, string) {
	hyper := &model.Hyper{}
	if err := db.Where("hostid = ?", hostid).Take(hyper).Error; err != nil {
		return true, "the host is not known"
	}
	if _, online := hostOnline(db, hostid); !online {
		return true, "the host is offline"
	}
	if hyper.ReconciledAt == nil || hyper.ReconciledAt.Before(fencedSince) {
		return true, "the host has not reconciled its instances since it was fenced"
	}
	var n int64
	db.Model(&model.Migration{}).Where("type = ? AND source_hyper = ? AND source_cleaned = ? AND status NOT IN ?", model.MigrationTypeEvacuate, hostid,
		false, []string{migrationStatusFailed, migrationStatusNotDoing}).Count(&n)
	if n > 0 {
		return true, fmt.Sprintf("%d instances evacuated from the host are not removed from it yet", n)
	}
	return false, ""
}

// storageUnfenceSettle is how long a host that came back works on its own before a fence command is undone: letting
// it back into GPFS right after the reconcile, while its boot sync still rebuilt the router namespaces, once hung the
// kernel's network lock on it, and the hung host stalled the file system on every node (TC-24 REC-04)
var storageUnfenceSettle = 5 * time.Minute

// storageFencesToLift are the fences lifted once their host is back: a failed fence too, its command may have gone
// through before it failed (expelled, then the file systems did not answer in time), and one that failed to be
// lifted, again a while later
var storageFencesToLift = []string{model.StorageFenceFenced, model.StorageFenceConfirmed, model.StorageFenceFailed, model.StorageFenceUnfenceFailed}

// checkHostUnfence lets a host back into the clusters that fenced it, once nothing holds it (hostStillHeld) and it has
// settled (storageUnfenceSettle); a confirmation only goes at once, nothing runs for it
func checkHostUnfence(ctx context.Context, hostid int32) {
	db := dbs.DBContext(ctx)
	fences := []*model.StorageFence{}
	if err := db.Where("hostid = ? AND status IN ?", hostid, storageFencesToLift).Find(&fences).Error; err != nil || len(fences) == 0 {
		return
	}
	since := fences[0].CreatedAt
	for _, f := range fences {
		if f.CreatedAt.After(since) {
			since = f.CreatedAt
		}
	}
	if held, why := hostStillHeld(db, hostid, since); held {
		logger.Ctx(ctx).Debugf("Host %d stays fenced: %s", hostid, why)
		return
	}
	hyper := &model.Hyper{}
	settling := db.Where("hostid = ?", hostid).Take(hyper).Error != nil || hyper.ReconciledAt == nil ||
		time.Since(*hyper.ReconciledAt) < storageUnfenceSettle
	for _, f := range fences {
		if settling && f.Method != model.StorageFenceByAdmin {
			logger.Ctx(ctx).Debugf("Host %d stays fenced in storage cluster %d: it came back less than %v ago", hostid, f.ClusterID, storageUnfenceSettle)
			continue
		}
		if (f.Status == model.StorageFenceFailed || f.Status == model.StorageFenceUnfenceFailed) && time.Since(f.UpdatedAt) < storageUnfenceRetry {
			continue
		}
		if err := startStorageUnfence(ctx, f); err != nil {
			logger.Ctx(ctx).Warningf("Failed to let host %d back into storage cluster %d: %v", hostid, f.ClusterID, err)
		}
	}
}

// startStorageUnfence lifts one fence: a confirmation only goes, a fence command is undone by a task; a failed one
// too, its command may have gone through (the undo does nothing when it did not)
func startStorageUnfence(ctx context.Context, f *model.StorageFence) error {
	db := dbs.DBContext(ctx)
	if f.Method == model.StorageFenceByAdmin {
		return db.Delete(f).Error
	}
	cluster := &model.StorageCluster{}
	if err := db.Take(cluster, f.ClusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.Delete(f).Error
		}
		return err
	}
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil {
		return err
	}
	fencer, ok := backend.(storageFencer)
	if !ok {
		return db.Delete(f).Error
	}
	nodes := []*model.StorageClusterNode{}
	if err = db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&nodes).Error; err != nil {
		return err
	}
	plan, reason := fencer.FencePlan(db, cluster, nodes, f.Hostid, StorageTaskUnfence)
	if plan = onlineStepHosts(db, plan); plan == nil {
		if reason == "" {
			reason = "no host of the cluster that could do it is online"
		}
		return db.Model(f).Updates(map[string]interface{}{"status": model.StorageFenceUnfenceFailed, "message": truncate(reason, 512)}).Error
	}
	// The task of the fence goes with the claim: maintainStorageFences must not take a failed fence task for this one
	claim := db.Model(&model.StorageFence{}).Where("id = ? AND status = ?", f.ID, f.Status).
		Updates(map[string]interface{}{"status": model.StorageFenceUnfencing, "task_id": 0})
	if claim.Error != nil || claim.RowsAffected == 0 {
		return claim.Error
	}
	task, err := startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskUnfence,
		Plan: []*StorageStepPlan{plan}, Params: &storageFenceParams{FenceID: f.ID, Hostid: f.Hostid, Target: f.Target}})
	if err != nil {
		db.Model(f).Updates(map[string]interface{}{"status": model.StorageFenceUnfenceFailed, "message": truncate(err.Error(), 512)})
		return err
	}
	return db.Model(f).Update("task_id", task.ID).Error
}

// liftRemovedHostFences lifts the fences of a host removed from a cluster: the admin removed it as gone, nothing
// waits for it to come back. GPFS forgot the node with it; a Ceph blocklist entry of its address is removed, or it
// would refuse whatever gets that address later
func liftRemovedHostFences(ctx context.Context, clusterID int64, hostid int32) {
	db := dbs.DBContext(ctx)
	fences := []*model.StorageFence{}
	if err := db.Where("cluster_id = ? AND hostid = ? AND status IN ?", clusterID, hostid, storageFencesToLift).Find(&fences).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to list the fences of host %d on storage cluster %d: %v", hostid, clusterID, err)
		return
	}
	for _, f := range fences {
		if err := startStorageUnfence(ctx, f); err != nil {
			logger.Ctx(ctx).Warningf("Failed to lift the fence of removed host %d on storage cluster %d: %v", hostid, clusterID, err)
		}
	}
}

// HostFence is a fence of a host as the hypervisor detail shows it
type HostFence struct {
	ID          int64      `json:"id"`
	Cluster     string     `json:"cluster"`
	ClusterUUID string     `json:"cluster_uuid"`
	Status      string     `json:"status"`
	Method      string     `json:"method"`
	Target      string     `json:"target,omitempty"`
	Message     string     `json:"message,omitempty"`
	FencedAt    *time.Time `json:"fenced_at,omitempty"`
	ConfirmedBy string     `json:"confirmed_by,omitempty"`
}

// HostFences lists the fences of a host
func HostFences(ctx context.Context, hostid int32) (list []*HostFence, err error) {
	db := dbs.DBContext(ctx)
	fences := []*model.StorageFence{}
	if err = db.Where("hostid = ?", hostid).Order("id").Find(&fences).Error; err != nil {
		return
	}
	list = []*HostFence{}
	for _, f := range fences {
		c := &model.StorageCluster{}
		db.Unscoped().Take(c, f.ClusterID)
		list = append(list, &HostFence{ID: f.ID, Cluster: c.Name, ClusterUUID: c.UUID, Status: f.Status, Method: f.Method, Target: f.Target,
			Message: f.Message, FencedAt: f.FencedAt, ConfirmedBy: f.ConfirmedBy})
	}
	return
}

// UnfenceHost lets a host back into every cluster that fenced it, now, or forgets the fences (forget: after an admin
// lifted them by hand, e.g. the blocklist entry of an imported Ceph cluster). Only for a host that is not held
func UnfenceHost(ctx context.Context, hostid int32, forget bool) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	fences := []*model.StorageFence{}
	if err = db.Where("hostid = ?", hostid).Find(&fences).Error; err != nil {
		return
	}
	if len(fences) == 0 {
		return NewCLError(ErrStorageFenceUnavailable, "The host is not fenced", nil)
	}
	if forget {
		return db.Where("hostid = ?", hostid).Delete(&model.StorageFence{}).Error
	}
	var since time.Time
	for _, f := range fences {
		if f.CreatedAt.After(since) {
			since = f.CreatedAt
		}
	}
	if held, why := hostStillHeld(db, hostid, since); held {
		return NewCLError(ErrStorageFenceUnavailable, "The host can not be let back in yet: "+why, nil)
	}
	for _, f := range fences {
		if f.Status == model.StorageFenceFencing || f.Status == model.StorageFenceUnfencing {
			continue
		}
		if err = startStorageUnfence(ctx, f); err != nil {
			return
		}
	}
	return
}
