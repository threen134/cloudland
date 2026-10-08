/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// New allocations stop once the file system of a pool is this full (used / (used + available))
	poolAdmitUsageLimit = 0.90
	gib                 = int64(1024 * 1024 * 1024)
)

// hostOnline reports whether a host is connected and able to run commands
func hostOnline(db *gorm.DB, hostid int32) (hyper *model.Hyper, ok bool) {
	hyper = &model.Hyper{}
	if err := db.Where("hostid = ?", hostid).Take(hyper).Error; err != nil {
		return nil, false
	}
	return hyper, hyper.Status != 10 && hyper.Status != 4 && hyper.Status != 5
}

func hostName(db *gorm.DB, hostid int32) string {
	hyper := &model.Hyper{}
	if err := db.Where("hostid = ?", hostid).Take(hyper).Error; err != nil {
		return fmt.Sprintf("host %d", hostid)
	}
	return hyper.Hostname
}

// getHyperPool loads the row of a pool on a host; lock takes a row lock for admission
func getHyperPool(db *gorm.DB, hostid int32, poolID int64, lock bool) (hsp *model.HyperStoragePool, err error) {
	hsp = &model.HyperStoragePool{}
	q := db
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err = q.Where("hostid = ? AND pool_id = ?", hostid, poolID).Take(hsp).Error
	return
}

// poolUsableOn checks a pool can be used on a host right now: the row exists and is ready or degraded.
// The built-in pool counts as usable until the host reports it for the first time.
func poolUsableOn(db *gorm.DB, pool *model.StoragePool, hostid int32, lock bool) (hsp *model.HyperStoragePool, err error) {
	hsp, err = getHyperPool(db, hostid, pool.ID, lock)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			if pool.Builtin {
				// Hosts registered before storage pools existed get their row at start-up; be lenient in between
				return &model.HyperStoragePool{Hostid: hostid, PoolID: pool.ID, Status: model.HyperPoolReady, Layout: model.LayoutBuiltin}, nil
			}
			return nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("%s has no storage pool %s", hostName(db, hostid), pool.Name), nil)
		}
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the host storage pool", err)
	}
	if pool.Builtin && hsp.CapacityAt == nil {
		return
	}
	if !hsp.Available() {
		reason := hsp.Status
		if hsp.Reason != "" {
			reason = fmt.Sprintf("%s: %s", hsp.Status, hsp.Reason)
		}
		return nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Storage pool %s on %s is not usable (%s)", pool.Name, hostName(db, hostid), reason), nil)
	}
	return
}

// allocatedBytes is what is already promised in a pool on a host: the size of its volumes plus live reservations
func allocatedBytes(db *gorm.DB, hostid int32, poolID int64) (total int64, err error) {
	var volGB, resGB int64
	if err = db.Model(&model.Volume{}).Where("storage_pool_id = ? AND hyper = ?", poolID, hostid).
		Select("COALESCE(SUM(size), 0)").Scan(&volGB).Error; err != nil {
		return
	}
	if err = db.Model(&model.StorageReservation{}).Where("pool_id = ? AND hostid = ? AND expires_at > ?", poolID, hostid, time.Now()).
		Select("COALESCE(SUM(size_gb), 0)").Scan(&resGB).Error; err != nil {
		return
	}
	return (volGB + resGB) * gib, nil
}

// reservedBytes is the part of allocatedBytes held by reservations
func reservedBytes(db *gorm.DB, hostid int32, poolID int64) (total int64) {
	var resGB int64
	db.Model(&model.StorageReservation{}).Where("pool_id = ? AND hostid = ? AND expires_at > ?", poolID, hostid, time.Now()).
		Select("COALESCE(SUM(size_gb), 0)").Scan(&resGB)
	return resGB * gib
}

// capacityLimit is how much may be allocated in a pool on a host
func capacityLimit(pool *model.StoragePool, hsp *model.HyperStoragePool) int64 {
	ratio := pool.OverRatio
	if pool.Builtin {
		ratio = hsp.OverRatio
	}
	if ratio <= 0 {
		ratio = 1
	}
	return int64(float64(hsp.CapacityBytes) * ratio)
}

// storageFullPaused counts instances paused because a local pool of the host ran out of space and that
// have a disk in this pool: while there are some, new allocations would eat the room they need to resume
func storageFullPaused(db *gorm.DB, hostid int32, poolID int64) (count int64) {
	db.Model(&model.Instance{}).
		Where("instances.hyper = ? AND instances.status = ? AND instances.reason = ?", hostid, model.InstanceStatusPaused, InstanceReasonStorageFull).
		Where("EXISTS (SELECT 1 FROM volumes v WHERE v.instance_id = instances.id AND v.storage_pool_id = ? AND v.deleted_at IS NULL)", poolID).
		Count(&count)
	return
}

// admitLocked decides whether addGB more may be allocated in a pool on a host (§6 of the plan). It must run inside a
// transaction: the row of the pool on the host is locked, so concurrent admissions see each other's allocations once
// the caller writes the volume or the reservation in the same transaction.
func admitLocked(tx *gorm.DB, pool *model.StoragePool, hostid int32, addGB int64) (hsp *model.HyperStoragePool, err error) {
	return admit(tx, pool, hostid, addGB, true)
}

// admit is admitLocked without the row lock when lock is false: a pre-check whose result can change before it is used
func admit(tx *gorm.DB, pool *model.StoragePool, hostid int32, addGB int64, lock bool) (hsp *model.HyperStoragePool, err error) {
	if pool.Status != model.StoragePoolActive {
		return nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Storage pool %s is disabled", pool.Name), nil)
	}
	if hsp, err = poolUsableOn(tx, pool, hostid, lock); err != nil {
		return
	}
	host := hostName(tx, hostid)
	if hsp.CapacityAt == nil {
		if pool.Builtin {
			logger.Infof("Admission: capacity of the built-in pool on %s is not reported yet, allowing %d GB", host, addGB)
			return
		}
		return nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Capacity of storage pool %s on %s is not known yet", pool.Name, host), nil)
	}
	if n := storageFullPaused(tx, hostid, pool.ID); n > 0 {
		return nil, NewCLError(ErrStorageCapacityExceeded, fmt.Sprintf("Storage pool %s on %s ran out of space and %d instances are paused; free some space first", pool.Name, host, n), nil)
	}
	if ratio := hsp.UsageRatio(); ratio >= poolAdmitUsageLimit {
		return nil, NewCLError(ErrStorageCapacityExceeded, fmt.Sprintf("Storage pool %s on %s is %.0f%% full", pool.Name, host, ratio*100), nil)
	}
	allocated, err := allocatedBytes(tx, hostid, pool.ID)
	if err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to sum the allocations of the pool", err)
	}
	limit := capacityLimit(pool, hsp)
	if allocated+addGB*gib > limit {
		return nil, NewCLError(ErrStorageCapacityExceeded, fmt.Sprintf("Storage pool %s on %s can not take %d GB more: %d of %d GB allocated",
			pool.Name, host, addGB, allocated/gib, limit/gib), nil)
	}
	return
}

// reserve writes a reservation; it must run in the same transaction as the admission
func reserve(tx *gorm.DB, hostid int32, poolID int64, volumeID, migrationID int64, kind string, sizeGB int32, ttl time.Duration) (res *model.StorageReservation, err error) {
	res = &model.StorageReservation{
		Hostid:      hostid,
		PoolID:      poolID,
		VolumeID:    volumeID,
		MigrationID: migrationID,
		Kind:        kind,
		SizeGB:      sizeGB,
		ExpiresAt:   time.Now().Add(ttl),
	}
	err = tx.Create(res).Error
	return
}

// ReleaseReservations drops the reservations of a migration or of a volume
func ReleaseReservations(ctx context.Context, migrationID, volumeID int64, kind string) {
	if migrationID <= 0 && volumeID <= 0 {
		return
	}
	_, db := GetContextDB(ctx)
	q := db.Where("kind = ?", kind)
	if migrationID > 0 {
		q = q.Where("migration_id = ?", migrationID)
	}
	if volumeID > 0 {
		q = q.Where("volume_id = ?", volumeID)
	}
	if err := q.Delete(&model.StorageReservation{}).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to release %s reservations (migration %d, volume %d): %v", kind, migrationID, volumeID, err)
	}
}

// Instance reasons set by the storage code
const (
	InstanceReasonStorageFull    = "storage_full"
	InstanceReasonStoragePending = "storage_pending"
	// Shut off after a boot of its host until clapi said it is still that host's (shared-storage-design.md §11.4)
	InstanceReasonReconcilePending = "reconcile_pending"
	// The pools of the instance were fine but libvirt could not start it after the host booted; the host retries
	InstanceReasonStartFailed = "start_failed"
)

// instanceBusyForVolumes refuses volume operations while the instance is in the middle of something (§5.10)
func instanceBusyForVolumes(instance *model.Instance) error {
	switch instance.Status {
	case model.InstanceStatusPaused, model.InstanceStatusRescuing, model.InstanceStatusMigrating, model.InstanceStatusRollback,
		model.InstanceStatusReinstalling, model.InstanceStatusResizing, model.InstanceStatusProvisioning, model.InstanceStatusDeleting:
		return NewCLError(ErrInstanceInvalidState, fmt.Sprintf("Instance %s is %s, its volumes can not be changed now", instance.Hostname, instance.Status), nil)
	}
	return nil
}

// admitShared decides whether addGB more may be allocated in a shared pool (shared-storage-design.md §9.5): the
// volumes of the pool, with those of the pools sharing its capacity (CapacityGroup), plus addGB stay within the
// capacity times the over ratio of the pool, and the pool is less than 90% used. With lock it must run in a
// transaction: the rows of the pools are locked, in id order, so concurrent admissions see each other's volumes once
// the caller writes them in the same transaction. A capacity no host reported yet is refused: a full Ceph cluster
// blocks every pool, there is no "allow for now"
func admitShared(tx *gorm.DB, pool *model.StoragePool, addGB int64, lock bool) (err error) {
	if pool.Status != model.StoragePoolActive {
		return NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Storage pool %s is %s", pool.Name, pool.Status), nil)
	}
	d, err := poolDriverOf(pool)
	if err != nil {
		return
	}
	q := tx
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	candidates := []*model.StoragePool{}
	key := d.CapacityGroup(pool)
	if key == "" {
		err = q.Where("id = ?", pool.ID).Find(&candidates).Error
	} else {
		err = q.Where("cluster_id = ? AND driver = ? AND quota_bytes = 0", pool.ClusterID, pool.Driver).Order("id").Find(&candidates).Error
	}
	if err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to lock the storage pools", err)
	}
	var cur *model.StoragePool
	ids := []int64{}
	for _, c := range candidates {
		if c.ID == pool.ID {
			cur = c
		}
		if c.ID == pool.ID || (key != "" && d.CapacityGroup(c) == key) {
			ids = append(ids, c.ID)
		}
	}
	if cur == nil {
		return NewCLError(ErrStoragePoolNotFound, fmt.Sprintf("Storage pool %s not found", pool.Name), nil)
	}
	if cur.Status != model.StoragePoolActive {
		return NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Storage pool %s is %s", cur.Name, cur.Status), nil)
	}
	if cur.CapacityAt == nil || cur.CapacityBytes <= 0 {
		return NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Capacity of storage pool %s is not known yet", cur.Name), nil)
	}
	if ratio := cur.UsageRatio(); ratio >= poolAdmitUsageLimit {
		return NewCLError(ErrStorageCapacityExceeded, fmt.Sprintf("Storage pool %s is %.0f%% full", cur.Name, ratio*100), nil)
	}
	var allocatedGB int64
	if err = tx.Model(&model.Volume{}).Where("storage_pool_id IN ?", ids).Select("COALESCE(SUM(size), 0)").Scan(&allocatedGB).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to sum the volumes of the pool", err)
	}
	ratio := cur.OverRatio
	if ratio <= 0 {
		ratio = 1
	}
	limit := int64(float64(cur.CapacityBytes) * ratio)
	if (allocatedGB+addGB)*gib > limit {
		shared := ""
		if len(ids) > 1 {
			shared = fmt.Sprintf(" (with %d pools sharing its disks)", len(ids)-1)
		}
		return NewCLError(ErrStorageCapacityExceeded, fmt.Sprintf("Storage pool %s can not take %d GB more: %d of %d GB allocated%s",
			cur.Name, addGB, allocatedGB, limit/gib, shared), nil)
	}
	return
}

// sharedAllocatedBytes is what the volumes of a shared pool take
func sharedAllocatedBytes(db *gorm.DB, poolID int64) (total int64) {
	var gb int64
	db.Model(&model.Volume{}).Where("storage_pool_id = ?", poolID).Select("COALESCE(SUM(size), 0)").Scan(&gb)
	return gb * gib
}

// sharedAllocatedByPool is sharedAllocatedBytes of many pools in one grouped query
func sharedAllocatedByPool(db *gorm.DB, poolIDs []int64) map[int64]int64 {
	rows := []struct {
		StoragePoolID int64
		GB            int64 `gorm:"column:gb"`
	}{}
	db.Model(&model.Volume{}).Where("storage_pool_id IN ?", poolIDs).Select("storage_pool_id, COALESCE(SUM(size), 0) AS gb").
		Group("storage_pool_id").Scan(&rows)
	total := make(map[int64]int64, len(rows))
	for _, r := range rows {
		total[r.StoragePoolID] = r.GB * gib
	}
	return total
}
