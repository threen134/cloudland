/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"api/src/dbs"
	"api/src/model"
	"api/src/utils/tracing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PoolReport is the JSON detail of the local_pool_status callback
type PoolReport struct {
	Layout    string        `json:"layout"`
	Vg        string        `json:"vg"`
	Devices   []*PoolDevice `json:"devices"`
	Size      int64         `json:"size"`
	Used      int64         `json:"used"`
	Avail     int64         `json:"avail"`
	Own       int64         `json:"own"`
	OverRatio float64       `json:"over_ratio"`
	Sync      int32         `json:"sync"`
	Files     []string      `json:"files"`
	OldHostid int32         `json:"old_hostid"`
	Mode      string        `json:"mode"`
}

const (
	// A pool row that has not been reported for this long is taken as unreachable
	poolReportStale = 15 * time.Minute
	reservationTTL  = 24 * time.Hour
)

// applyCapacity copies the figures of a report onto a row
func applyCapacity(row *model.HyperStoragePool, pool *model.StoragePool, r *PoolReport, now time.Time) {
	if r.Size <= 0 && r.Avail <= 0 {
		return
	}
	row.UsedBytes, row.AvailBytes = r.Used, r.Avail
	if pool.Builtin {
		// The root file system is shared with the OS and the control plane: CloudLand may use what it holds already plus what is free
		row.OwnBytes = r.Own
		row.CapacityBytes = r.Own + r.Avail
		if r.OverRatio > 0 {
			row.OverRatio = r.OverRatio
		}
	} else {
		row.CapacityBytes = r.Size
	}
	row.SyncPercent = r.Sync
	row.CapacityAt = &now
}

// mergeDevices takes the disks reported by a node and keeps the media the admin set in the scan result
func mergeDevices(db *gorm.DB, hostid int32, devices []*PoolDevice) string {
	for _, d := range devices {
		disk := &model.HyperDisk{}
		if db.Where("hostid = ? AND disk_id = ?", hostid, d.ID).Take(disk).Error == nil && disk.Media != "" {
			d.Media = disk.Media
		}
	}
	b, _ := json.Marshal(devices)
	return string(b)
}

// HandlePoolStatus processes a local_pool_status callback: the result of a pool command or a periodic report
func HandlePoolStatus(ctx context.Context, hostid int32, op, poolRef, status, reason string, report *PoolReport) (err error) {
	db := dbs.DBContext(ctx)
	var pool *model.StoragePool
	if poolRef == "builtin" {
		if pool, err = BuiltinPool(ctx); err != nil {
			return
		}
	} else if pool, err = poolByRef(db, poolRef); err != nil {
		logger.Ctx(ctx).Warningf("local_pool_status from host %d for unknown pool %s", hostid, poolRef)
		return nil
	}
	now := time.Now()
	return db.Transaction(func(tx *gorm.DB) error {
		row := &model.HyperStoragePool{}
		if terr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hostid = ? AND pool_id = ?", hostid, pool.ID).Take(row).Error; terr != nil {
			if pool.Builtin && op == "report" {
				row = &model.HyperStoragePool{Hostid: hostid, PoolID: pool.ID, Status: model.HyperPoolReady, Layout: model.LayoutBuiltin}
			} else {
				logger.Ctx(ctx).Warningf("local_pool_status %s from host %d for pool %s which is not set up there", op, hostid, pool.Name)
				return nil
			}
		}
		switch op {
		case "report":
			row.ReportedStatus, row.ReportedReason, row.CheckedAt = status, reason, &now
			applyCapacity(row, pool, report, now)
			if report.Layout != "" && !pool.Builtin {
				row.Layout = report.Layout
			}
			if report.Files != nil {
				files, _ := json.Marshal(report.Files)
				row.Files = string(files)
			}
			switch row.Status {
			case model.HyperPoolReady, model.HyperPoolDegraded, model.HyperPoolUnavailable:
				if status == model.HyperPoolMaintenance && !row.Maintenance {
					// A leftover flag on the node; show it as it is
					row.Status, row.Reason = model.HyperPoolMaintenance, reason
				} else {
					row.Status, row.Reason = status, reason
				}
			case model.HyperPoolMaintenance:
				if !row.Maintenance && status != model.HyperPoolMaintenance {
					row.Status, row.Reason = status, reason
				}
			}
			if pool.Builtin && row.Status == "" {
				row.Status = model.HyperPoolReady
			}
		case "create", "adopt":
			if row.LastOp != op || row.Status != model.HyperPoolCreating {
				return nil
			}
			row.DeadlineAt = nil
			if status == model.HyperPoolReady || status == model.HyperPoolDegraded {
				row.Status, row.Reason, row.ReportedStatus, row.CheckedAt = status, reason, status, &now
				row.Layout, row.VgName = report.Layout, report.Vg
				row.Devices = mergeDevices(tx, hostid, report.Devices)
				applyCapacity(row, pool, report, now)
				if report.Files != nil {
					files, _ := json.Marshal(report.Files)
					row.Files = string(files)
				}
				if op == "adopt" {
					if terr := adoptVolumes(tx, pool, hostid, report); terr != nil {
						return terr
					}
				}
			} else {
				row.Status, row.Reason = model.HyperPoolError, reason
			}
		case "extend", "replace":
			if row.LastOp != op || row.Status != model.HyperPoolExtending {
				return nil
			}
			row.DeadlineAt = nil
			if status == model.HyperPoolReady || status == model.HyperPoolDegraded {
				row.Status, row.Reason = status, reason
				if len(report.Devices) > 0 {
					row.Devices = mergeDevices(tx, hostid, report.Devices)
				}
				if report.Layout != "" {
					row.Layout = report.Layout
				}
				applyCapacity(row, pool, report, now)
			} else {
				row.Status, row.Reason = row.PrevStatus, fmt.Sprintf("%s failed: %s", op, reason)
			}
		case "remove":
			if row.Status != model.HyperPoolRemoving {
				return nil
			}
			if status == "removed" {
				return tx.Unscoped().Delete(row).Error
			}
			if len(report.Files) > 0 {
				reason = fmt.Sprintf("%s: %s", reason, strings.Join(report.Files, ", "))
			}
			row.DeadlineAt = nil
			if status == "refused" && row.PrevStatus != "" {
				// The host changed nothing (busy, files left, in use): the pool stays usable as it was. An error here
				// would stick, since the heartbeat reports never touch a pool in error
				row.Status, row.Reason = row.PrevStatus, fmt.Sprintf("remove refused: %s", reason)
			} else {
				row.Status, row.Reason = model.HyperPoolError, reason
			}
		default:
			return nil
		}
		return tx.Save(row).Error
	})
}

// adoptVolumes points the volumes that waited for a pool to the host that adopted it; volumes whose file is
// not on the disks any more are lost
func adoptVolumes(tx *gorm.DB, pool *model.StoragePool, hostid int32, report *PoolReport) error {
	files := map[string]bool{}
	for _, f := range report.Files {
		files[f] = true
	}
	q := tx.Where("storage_pool_id = ? AND status = ?", pool.ID, model.VolumeStatusOrphaned)
	if report.OldHostid > 0 {
		q = q.Where("hyper = ?", report.OldHostid)
	} else {
		q = q.Where("hyper NOT IN (SELECT hostid FROM hypers)")
	}
	volumes := []*model.Volume{}
	if err := q.Find(&volumes).Error; err != nil {
		return err
	}
	for _, v := range volumes {
		updates := map[string]interface{}{"hyper": hostid, "status": model.VolumeStatusAvailable, "reason": "", "instance_id": 0, "target": ""}
		if !files[fileName(v.Path)] {
			updates = map[string]interface{}{"hyper": hostid, "status": model.VolumeStatusLost, "reason": "the file was not found when the pool was adopted"}
		}
		if err := tx.Model(v).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

// HandleClearVolume finishes the deletion of a volume once its host removed the file (§5.5)
func HandleClearVolume(ctx context.Context, hostid int32, volumeID int64, result, reason string) (err error) {
	db := dbs.DBContext(ctx)
	volume := &model.Volume{}
	if err = db.Where("id = ? AND status = ?", volumeID, model.VolumeStatusDeleting).Take(volume).Error; err != nil {
		// Repeated or late callback
		return nil
	}
	if hostid > 0 && volume.Hyper != hostid {
		logger.Ctx(ctx).Warningf("clear_volume for volume %d from host %d, but the volume is on host %d", volumeID, hostid, volume.Hyper)
		return nil
	}
	if result == "deleted" {
		return db.Delete(&model.Volume{}, volume.ID).Error
	}
	return db.Model(volume).Updates(map[string]interface{}{"status": model.VolumeStatusAvailable, "reason": "delete failed: " + reason}).Error
}

// HandlePoolUsage stores the largest files of a pool, as reported by pool_usage.sh (§6.1)
func HandlePoolUsage(ctx context.Context, hostid int32, poolRef string, entries []*UsageEntry) (err error) {
	db := dbs.DBContext(ctx)
	var pool *model.StoragePool
	if poolRef == "builtin" {
		if pool, err = BuiltinPool(ctx); err != nil {
			return
		}
	} else if pool, err = poolByRef(db, poolRef); err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Bytes > entries[j].Bytes })
	if len(entries) > 20 {
		entries = entries[:20]
	}
	for _, e := range entries {
		volume := &model.Volume{}
		if db.Preload("Instance").Where("storage_pool_id = ? AND hyper = ? AND path = ?", pool.ID, hostid, e.Path).Take(volume).Error != nil {
			continue
		}
		e.VolumeUUID, e.VolumeName = volume.UUID, volume.Name
		if volume.Instance != nil {
			e.Instance, e.InstanceID = volume.Instance.Hostname, volume.Instance.UUID
		}
		org := &model.Organization{}
		if db.Take(org, volume.Owner).Error == nil {
			e.Owner = org.Name
		}
	}
	report, _ := json.Marshal(entries)
	now := time.Now()
	return db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", hostid, pool.ID).
		Updates(map[string]interface{}{"usage_report": string(report), "usage_at": &now}).Error
}

// StartStorageMaintainer runs the time-based rules of the storage code: expired reservations, commands that never
// came back, hosts that stopped reporting, and volumes stuck in "deleting"
func StartStorageMaintainer() {
	go func() {
		time.Sleep(30 * time.Second)
		for {
			ctx, span := tracing.StartBackground(context.Background(), "storage.maintain")
			maintainStorage(ctx)
			span.End()
			time.Sleep(time.Minute)
		}
	}()
}

func maintainStorage(ctx context.Context) {
	db := dbs.DBContext(ctx)
	now := time.Now()
	db.Where("expires_at < ?", now).Delete(&model.StorageReservation{})
	// Migration reservations of migrations that ended are dropped by the migration code; this catches the rest
	db.Where("kind = ? AND migration_id IN (SELECT id FROM migrations WHERE status IN ?)", model.ReservationMigration,
		[]string{"completed", "failed", "rollback", "not_doing", "not_supported", "timeout"}).Delete(&model.StorageReservation{})

	rows := []*model.HyperStoragePool{}
	db.Where("deadline_at IS NOT NULL AND deadline_at < ?", now).Find(&rows)
	for _, row := range rows {
		updates := map[string]interface{}{"deadline_at": nil}
		switch row.Status {
		case model.HyperPoolCreating, model.HyperPoolRemoving:
			updates["status"] = model.HyperPoolError
			updates["reason"] = fmt.Sprintf("%s timed out", row.LastOp)
		case model.HyperPoolExtending:
			updates["status"] = row.PrevStatus
			updates["reason"] = fmt.Sprintf("%s timed out", row.LastOp)
		}
		db.Model(row).Updates(updates)
	}

	stale := now.Add(-poolReportStale)
	rows = []*model.HyperStoragePool{}
	db.Where("status IN ? AND capacity_at IS NOT NULL AND (checked_at IS NULL OR checked_at < ?)",
		[]string{model.HyperPoolReady, model.HyperPoolDegraded, model.HyperPoolMaintenance}, stale).Find(&rows)
	for _, row := range rows {
		db.Model(row).Updates(map[string]interface{}{"status": model.HyperPoolUnavailable, "reason": model.ReasonNodeOffline})
	}

	db.Model(&model.Volume{}).Where("status = ? AND updated_at < ?", model.VolumeStatusDeleting, now.Add(-volumeDeleteTimeout)).
		Updates(map[string]interface{}{"status": model.VolumeStatusDeleteFailed, "reason": "the host did not confirm the deletion"})
}
