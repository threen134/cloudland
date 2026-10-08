/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A host that comes back reconciles the instances it has defined with the database (shared-storage-design.md §11.4).
// After a boot, and after a gap in its heartbeats (cloudlet restarted or got its connection back), it reports every
// domain with node_recovered; clapi tells it which of them it may start and which it must remove (node_reconcile.sh),
// because while it was away an instance may have been recovered on another host or deleted. Instances with a disk in a
// shared pool wait at a boot for this answer before they start: two copies would write to the same disks.

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
)

const (
	// A delete still deleting after this long had its command dropped (the host was offline): it is sent again
	reconcileDeleteResendAfter = 2 * time.Minute
	// A migration involving the host updated within this time is taken as still going on
	reconcileMigrationActive = 24 * time.Hour
)

// ReconcileDomain is a domain the host reported
type ReconcileDomain struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
}

type reconcileStale struct {
	ID     int64 `json:"id"`
	Router int64 `json:"router"`
}

// reconcilePlan is what the host is told, plus the deletes clapi sends again itself
type reconcilePlan struct {
	Start  []int64           `json:"start"`
	Stale  []reconcileStale  `json:"stale"`
	Leave  []int64           `json:"-"`
	Resend []*model.Instance `json:"-"`
}

// MarkHyperOffline records when cland took a host offline and the status it had, once, as the host goes offline
func MarkHyperOffline(ctx context.Context, hostid int32) {
	db := dbs.DBContext(ctx)
	err := db.Model(&model.Hyper{}).Where("hostid = ? AND status <> ?", hostid, model.HyperStatusOffline).
		Updates(map[string]interface{}{
			"offline_at":    time.Now(),
			"offline_prior": gorm.Expr("CASE WHEN status IN (0, 2) THEN status ELSE NULL END"),
		}).Error
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to record that host %d went offline: %v", hostid, err)
	}
}

// evacuationHoldsSource tells whether an evacuation of the instance from the host is going on or done without the
// host having removed its copy yet: the host's reports about the instance are not to be believed until it did
func evacuationHoldsSource(db *gorm.DB, instanceID int64, hostid int32) bool {
	var n int64
	err := db.Model(&model.Migration{}).
		Where("instance_id = ? AND type = ? AND source_hyper = ? AND source_cleaned = ? AND status NOT IN ?",
			instanceID, model.MigrationTypeEvacuate, hostid, false, []string{migrationStatusFailed, migrationStatusNotDoing}).
		Count(&n).Error
	return err == nil && n > 0
}

// EvacuatedFrom is evacuationHoldsSource for the callbacks of a host (inst_status, launch_vm)
func EvacuatedFrom(ctx context.Context, instanceID int64, hostid int32) bool {
	_, db := GetContextDB(ctx)
	return evacuationHoldsSource(db, instanceID, hostid)
}

// instanceDiskPools counts the volumes of an instance in shared pools and in local ones (the builtin pool included)
func instanceDiskPools(db *gorm.DB, instanceID int64) (shared, local int64, err error) {
	row := struct {
		Shared int64
		Local  int64
	}{}
	err = db.Table("volumes").
		Joins("LEFT JOIN storage_pools ON storage_pools.id = volumes.storage_pool_id").
		Where("volumes.instance_id = ? AND volumes.deleted_at IS NULL", instanceID).
		Select("COALESCE(SUM(CASE WHEN storage_pools.driver IS NOT NULL AND storage_pools.driver <> ? THEN 1 ELSE 0 END), 0) AS shared, "+
			"COALESCE(SUM(CASE WHEN storage_pools.driver IS NULL OR storage_pools.driver = ? THEN 1 ELSE 0 END), 0) AS local",
			model.StorageDriverLocal, model.StorageDriverLocal).
		Scan(&row).Error
	return row.Shared, row.Local, err
}

// migrationInvolves tells whether a migration of the instance from or to the host is going on
func migrationInvolves(db *gorm.DB, instanceID int64, hostid int32, now time.Time) bool {
	var n int64
	err := db.Model(&model.Migration{}).
		Where("instance_id = ? AND (source_hyper = ? OR target_hyper = ?) AND status IN ? AND updated_at > ?", instanceID, hostid, hostid,
			[]string{"in_progress", "target_prepared", "source_prepared", migrationStatusFencing}, now.Add(-reconcileMigrationActive)).
		Count(&n).Error
	return err == nil && n > 0
}

// planReconcile decides about each domain a host reported:
//   - start: the database says it is the host's
//   - stale: deleted, or evacuated from the host, or it has a disk in a shared pool and the database puts it on another
//     host: two copies of it must never run
//   - resend: the host's, but deleting and the delete was dropped while the host was away
//   - leave: a migration from or to the host is going on, an instance being created (defined before its callback put
//     it on a host), or an instance with local disks only that the database puts elsewhere (two copies can not write
//     to the same disk; left as it is, as before)
func planReconcile(db *gorm.DB, hostid int32, domains []ReconcileDomain, now time.Time) (plan *reconcilePlan, err error) {
	plan = &reconcilePlan{Start: []int64{}, Stale: []reconcileStale{}}
	for _, d := range domains {
		if d.ID <= 0 {
			continue
		}
		inst := &model.Instance{}
		if err = db.Unscoped().Take(inst, d.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				err = nil
				plan.Stale = append(plan.Stale, reconcileStale{ID: d.ID})
				continue
			}
			return
		}
		switch {
		case inst.DeletedAt.Valid:
			plan.Stale = append(plan.Stale, reconcileStale{ID: d.ID, Router: inst.RouterID})
		case evacuationHoldsSource(db, inst.ID, hostid) && inst.Hyper != hostid:
			// Recovered elsewhere: this copy goes. An evacuation still waiting (fencing, starting) leaves it alone
			plan.Stale = append(plan.Stale, reconcileStale{ID: d.ID, Router: inst.RouterID})
		case inst.Status == model.InstanceStatusMigrating || migrationInvolves(db, inst.ID, hostid, now):
			plan.Leave = append(plan.Leave, d.ID)
		case inst.Hyper == hostid:
			if inst.Status == model.InstanceStatusDeleting && now.Sub(inst.UpdatedAt) > reconcileDeleteResendAfter {
				plan.Resend = append(plan.Resend, inst)
			} else if inst.Status == model.InstanceStatusDeleting {
				plan.Leave = append(plan.Leave, d.ID)
			} else {
				plan.Start = append(plan.Start, d.ID)
			}
		case inst.Status == model.InstanceStatusProvisioning || inst.Hyper < 0:
			// Being created: launch_vm.sh defined it and its callback (which puts it on the host) has not come yet
			plan.Leave = append(plan.Leave, d.ID)
		default:
			shared, _, qerr := instanceDiskPools(db, inst.ID)
			if qerr != nil {
				return nil, qerr
			}
			if shared > 0 {
				plan.Stale = append(plan.Stale, reconcileStale{ID: d.ID, Router: inst.RouterID})
			} else {
				plan.Leave = append(plan.Leave, d.ID)
			}
		}
	}
	return
}

// ReconcileNode answers the node_recovered report of a host
func ReconcileNode(ctx context.Context, hostid int32, boot, reason string, domains []ReconcileDomain) (err error) {
	db := dbs.DBContext(ctx)
	plan, err := planReconcile(db, hostid, domains, time.Now())
	if err != nil {
		return
	}
	logger.Ctx(ctx).Infof("Reconcile of host %d (%s, boot %s): start %v, stale %v, leave %v, deletes sent again %d", hostid, reason, boot,
		plan.Start, plan.Stale, plan.Leave, len(plan.Resend))
	body, err := json.Marshal(map[string]interface{}{"boot": boot, "reason": reason, "start": plan.Start, "stale": plan.Stale})
	if err != nil {
		return
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/node_reconcile.sh <<'EOF'\n%s\nEOF", body)
	if err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hostid), command); err != nil {
		return
	}
	for _, inst := range plan.Resend {
		if rerr := resendInstanceDelete(ctx, inst); rerr != nil {
			logger.Ctx(ctx).Warningf("Failed to send the delete of instance %d to host %d again: %v", inst.ID, hostid, rerr)
		}
	}
	return
}

// resendInstanceDelete sends the clean-up of an instance being deleted again. The local boot disk is passed when its
// record (removed by the delete request) still names a pool; a boot disk in a shared pool is removed after the
// clear_vm callback, as with any delete
func resendInstanceDelete(ctx context.Context, inst *model.Instance) error {
	db := dbs.DBContext(ctx)
	bootPool, bootPath := "-", ""
	boot := &model.Volume{}
	if err := db.Unscoped().Where("instance_id = ? AND booting = ?", inst.ID, true).Order("id DESC").Take(boot).Error; err == nil &&
		boot.Status != model.VolumeStatusLost {
		if pool, perr := VolumePool(ctx, boot); perr == nil && !pool.Shared() {
			bootPool, bootPath = PoolScriptID(pool), boot.Path
		}
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_vm.sh '%d' '%d' '%s' '%s'<<'EOF'\n[]\nEOF", inst.ID, inst.RouterID,
		ShellEscape(bootPool), ShellEscape(bootPath))
	return HyperExecute(ctx, fmt.Sprintf("inter=%d", inst.Hyper), command)
}

// NodeReconciled records that a host applied a reconcile, then lifts what fenced it once nothing it held is left. The
// asks a host repeats while some of its instances are not decided yet (reason "leave") do not move the time it came
// back, which the lifting of fences waits on (storageUnfenceSettle)
func NodeReconciled(ctx context.Context, hostid int32, boot, reason string) (err error) {
	db := dbs.DBContext(ctx)
	now := time.Now()
	set := map[string]interface{}{"reconciled_boot": boot}
	if reason != "leave" {
		set["reconciled_at"] = &now
	}
	if err = db.Model(&model.Hyper{}).Where("hostid = ?", hostid).Updates(set).Error; err != nil {
		return
	}
	checkHostUnfence(ctx, hostid)
	return
}

// StaleInstanceCleared takes the report of clear_stale_vm.sh: the evacuations of the instance from the host are done
// with the source. It returns them so the caller removes what the source still routes for the instance
func StaleInstanceCleared(ctx context.Context, hostid int32, instanceID int64, ok bool) (evacuations []*model.Migration, err error) {
	db := dbs.DBContext(ctx)
	if !ok {
		logger.Ctx(ctx).Warningf("Host %d could not remove its stale copy of instance %d", hostid, instanceID)
		return
	}
	if err = db.Where("instance_id = ? AND type = ? AND source_hyper = ? AND source_cleaned = ?", instanceID, model.MigrationTypeEvacuate,
		hostid, false).Find(&evacuations).Error; err != nil {
		return
	}
	if len(evacuations) > 0 {
		ids := []int64{}
		for _, m := range evacuations {
			ids = append(ids, m.ID)
		}
		if err = db.Model(&model.Migration{}).Where("id IN ?", ids).Update("source_cleaned", true).Error; err != nil {
			return
		}
	}
	checkHostUnfence(ctx, hostid)
	return
}
