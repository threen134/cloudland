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

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
)

// MigrationOptions are the storage options of a migration request (§7.1 of the local storage plan)
type MigrationOptions struct {
	// Target pool per volume id; only with a target host
	Disks map[int64]int64
	// Replace a pool missing on the target by one of the same fallback group
	AllowPoolFallback bool
	// Skip the capacity checks (system admin, audited)
	IgnoreCapacity bool
}

// instanceDisks loads the disks of an instance that have to move with it, each with its pool
func instanceDisks(db *gorm.DB, ctx context.Context, instance *model.Instance) (volumes []*model.Volume, err error) {
	if err = db.Preload("StoragePool").Where("instance_id = ?", instance.ID).Order("booting DESC, id").Find(&volumes).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the volumes of the instance", err)
	}
	for _, v := range volumes {
		switch v.Status {
		case model.VolumeStatusLost, model.VolumeStatusOrphaned, model.VolumeStatusDeleting, model.VolumeStatusDeleteFailed:
			return nil, NewCLError(ErrVolumeInvalidState, fmt.Sprintf("Volume %s is %s and can not be migrated", v.Name, v.Status), nil)
		}
		if v.IsBusy() {
			return nil, NewCLError(ErrVolumeInvalidState, fmt.Sprintf("Volume %s is %s, retry when it is done", v.Name, v.Status), nil)
		}
		if _, err = VolumePool(ctx, v); err != nil {
			return
		}
	}
	return
}

// usablePoolNames lists the pools a disk could go to on a host, for the error message
func usablePoolNames(db *gorm.DB, hostid int32) string {
	rows := []*model.HyperStoragePool{}
	db.Preload("Pool").Where("hostid = ?", hostid).Find(&rows)
	names := []string{}
	for _, r := range rows {
		if r.Pool != nil && r.Pool.Status == model.StoragePoolActive && (r.Available() || (r.Pool.Builtin && r.CapacityAt == nil)) {
			names = append(names, r.Pool.Name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// fallbackPool picks, among the pools of the same fallback group usable on the host, the one with most room
func fallbackPool(db *gorm.DB, src *model.StoragePool, hostid int32) (pool *model.StoragePool) {
	if src.Builtin || src.FallbackGroup == "" {
		return nil
	}
	candidates := []*model.StoragePool{}
	db.Where("fallback_group = ? AND builtin = ? AND status = ? AND id <> ?", src.FallbackGroup, false, model.StoragePoolActive, src.ID).Find(&candidates)
	var best int64 = -1
	for _, c := range candidates {
		hsp, err := poolUsableOn(db, c, hostid, false)
		if err != nil {
			continue
		}
		if hsp.AvailBytes > best {
			best, pool = hsp.AvailBytes, c
		}
	}
	return
}

// planDisks decides where every disk of an instance goes on the target host (§7.1)
func planDisks(db *gorm.DB, ctx context.Context, instance *model.Instance, target int32, opts *MigrationOptions) (items []*model.DiskPlanItem, err error) {
	volumes, err := instanceDisks(db, ctx, instance)
	if err != nil {
		return
	}
	for _, v := range volumes {
		src := v.StoragePool
		item := &model.DiskPlanItem{
			VolumeID: v.ID, Device: v.Target, Booting: v.Booting, SizeGB: v.Size,
			SrcPoolID: src.ID, SrcPath: VolumeAbsPath(src, v),
		}
		var dst *model.StoragePool
		if poolID, ok := opts.Disks[v.ID]; ok {
			dst = &model.StoragePool{}
			if err = db.Take(dst, poolID).Error; err != nil {
				return nil, NewCLError(ErrStoragePoolNotFound, fmt.Sprintf("Storage pool asked for volume %s not found", v.Name), err)
			}
			if _, err = poolUsableOn(db, dst, target, false); err != nil {
				return
			}
			if dst.ID != src.ID {
				item.Reason = fmt.Sprintf("moved from %s to %s as requested", src.Name, dst.Name)
			}
		} else if _, uerr := poolUsableOn(db, src, target, false); uerr == nil {
			dst = src
		} else if opts.AllowPoolFallback {
			if dst = fallbackPool(db, src, target); dst != nil {
				item.Auto = true
				item.Reason = fmt.Sprintf("pool %s is not usable on %s, replaced by %s of the same fallback group", src.Name, hostName(db, target), dst.Name)
			}
		}
		if dst == nil {
			return nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Volume %s can not stay in pool %s on %s; pools usable there: %s",
				v.Name, src.Name, hostName(db, target), usablePoolNames(db, target)), nil)
		}
		item.DstPoolID = dst.ID
		item.DstPoolUUID = PoolScriptID(dst)
		item.DstPoolRoot = dst.Root()
		if dst.ID == src.ID {
			item.DstRelPath = v.Path
		} else {
			item.DstRelPath = PoolRelPath(dst, v)
		}
		item.DstPath = dst.Root() + "/" + item.DstRelPath
		item.NVRAM = v.Booting
		items = append(items, item)
	}
	return
}

// admitPlan checks the plan fits on the target and holds the space with migration reservations (§6, §7.1)
func admitPlan(tx *gorm.DB, migration *model.Migration, items []*model.DiskPlanItem, target int32, lock bool) (err error) {
	perPool := map[int64]int64{}
	for _, item := range items {
		perPool[item.DstPoolID] += int64(item.SizeGB)
	}
	poolIDs := make([]int64, 0, len(perPool))
	for id := range perPool {
		poolIDs = append(poolIDs, id)
	}
	// Lock in a fixed order so two migrations never wait on each other
	sort.Slice(poolIDs, func(i, j int) bool { return poolIDs[i] < poolIDs[j] })
	for _, id := range poolIDs {
		pool := &model.StoragePool{}
		if err = tx.Take(pool, id).Error; err != nil {
			return NewCLError(ErrStoragePoolNotFound, "Storage pool of the plan not found", err)
		}
		if migration != nil && migration.IgnoreCapacity {
			if _, err = poolUsableOn(tx, pool, target, lock); err != nil {
				return
			}
			continue
		}
		if _, err = admit(tx, pool, target, perPool[id], lock); err != nil {
			return
		}
	}
	if migration == nil {
		return
	}
	for _, item := range items {
		if _, err = reserve(tx, target, item.DstPoolID, item.VolumeID, migration.ID, model.ReservationMigration, item.SizeGB, reservationTTL); err != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to reserve storage for the migration", err)
		}
	}
	return
}

// migrationCandidates lists the hosts an instance can move to: g1 keeps every disk in its pool, g2 needs a
// fallback pool for some (§7.1)
func migrationCandidates(db *gorm.DB, ctx context.Context, instance *model.Instance, opts *MigrationOptions) (g1, g2 []int32, err error) {
	hypers := []*model.Hyper{}
	q := db.Where("status = 1 AND hostid >= 0 AND hostid <> ?", instance.Hyper)
	if instance.ZoneID > 0 {
		q = q.Where("zone_id = ?", instance.ZoneID)
	}
	if err = q.Order("hostid").Find(&hypers).Error; err != nil {
		return nil, nil, NewCLError(ErrSQLSyntaxError, "Failed to query hypervisors", err)
	}
	stay := &MigrationOptions{IgnoreCapacity: opts.IgnoreCapacity}
	for _, h := range hypers {
		if items, perr := planDisks(db, ctx, instance, h.Hostid, stay); perr == nil {
			if admitPlan(db, nil, items, h.Hostid, false) == nil || opts.IgnoreCapacity {
				g1 = append(g1, h.Hostid)
				continue
			}
		}
		if !opts.AllowPoolFallback {
			continue
		}
		if items, perr := planDisks(db, ctx, instance, h.Hostid, opts); perr == nil {
			if admitPlan(db, nil, items, h.Hostid, false) == nil || opts.IgnoreCapacity {
				g2 = append(g2, h.Hostid)
			}
		}
	}
	return
}

// MigrationPlan returns the stored plan of a migration
func MigrationPlan(migration *model.Migration) (items []*model.DiskPlanItem) {
	items = []*model.DiskPlanItem{}
	if migration.DiskPlan != "" {
		_ = json.Unmarshal([]byte(migration.DiskPlan), &items)
	}
	return
}

func migrationOptions(migration *model.Migration) *MigrationOptions {
	opts := &MigrationOptions{AllowPoolFallback: migration.AllowPoolFallback, IgnoreCapacity: migration.IgnoreCapacity, Disks: map[int64]int64{}}
	if migration.DiskRequests != "" {
		_ = json.Unmarshal([]byte(migration.DiskRequests), &opts.Disks)
	}
	return opts
}

// PlanMigrationTarget makes the plan once the target is known: at creation when the target was given, otherwise
// when target_prepared tells which host the scheduler chose. It runs in the transaction of the caller.
func PlanMigrationTarget(ctx context.Context, migration *model.Migration, instance *model.Instance, target int32) (items []*model.DiskPlanItem, err error) {
	ctx, db := GetContextDB(ctx)
	if migration.DiskPlan != "" {
		return MigrationPlan(migration), nil
	}
	if items, err = planDisks(db, ctx, instance, target, migrationOptions(migration)); err != nil {
		return
	}
	if err = admitPlan(db, migration, items, target, true); err != nil {
		return
	}
	plan, _ := json.Marshal(items)
	migration.DiskPlan = string(plan)
	if err = db.Model(&model.Migration{}).Where("id = ?", migration.ID).Update("disk_plan", migration.DiskPlan).Error; err != nil {
		return nil, NewCLError(ErrMigrationUpdateFailed, "Failed to save the disk plan", err)
	}
	return
}

// ApplyMigrationPlan moves the volumes of a completed migration to their new place (§7.3 step 7)
func ApplyMigrationPlan(ctx context.Context, migration *model.Migration, instanceID int64, target int32) (err error) {
	_, db := GetContextDB(ctx)
	items := MigrationPlan(migration)
	if len(items) == 0 {
		// Migrations created before the plans existed: the files kept their path
		err = db.Model(&model.Volume{}).Where("instance_id = ?", instanceID).Update("hyper", target).Error
	}
	for _, item := range items {
		if err = db.Model(&model.Volume{}).Where("id = ?", item.VolumeID).Updates(map[string]interface{}{
			"hyper": target, "storage_pool_id": item.DstPoolID, "path": item.DstRelPath,
		}).Error; err != nil {
			return
		}
	}
	ReleaseReservations(ctx, migration.ID, 0, model.ReservationMigration)
	return
}

// hyperGroupOf builds the select= group of cland from host ids
func hyperGroupOf(zoneID int64, hostids []int32) string {
	group := fmt.Sprintf("group-zone-%d", zoneID)
	for i, id := range hostids {
		sep := ","
		if i == 0 {
			sep = ":"
		}
		group = fmt.Sprintf("%s%s%d", group, sep, id)
	}
	return group
}

// PoolChoice is a pool a disk can go to on a target host
type PoolChoice struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	AvailBytes int64  `json:"avail_bytes"`
	Fits       bool   `json:"fits"`
	Reason     string `json:"reason,omitempty"`
}

// DiskTarget tells where one disk of an instance can go on a target host
type DiskTarget struct {
	VolumeUUID string        `json:"volume_uuid"`
	VolumeName string        `json:"volume_name"`
	Booting    bool          `json:"booting"`
	SizeGB     int32         `json:"size_gb"`
	SourcePool string        `json:"source_pool"`
	CanStay    bool          `json:"can_stay"`
	Fallback   *PoolChoice   `json:"fallback,omitempty"`
	Choices    []*PoolChoice `json:"choices"`
}

// MigrationTarget is one host an instance could move to (§8.1 migration_targets)
type MigrationTarget struct {
	Hostid   int32         `json:"hostid"`
	Hostname string        `json:"hostname"`
	Usable   bool          `json:"usable"`
	Reason   string        `json:"reason,omitempty"`
	Disks    []*DiskTarget `json:"disks"`
}

// MigrationTargets lists the hosts of the zone and, for each, where every local disk of the instance could go
func MigrationTargets(ctx context.Context, instance *model.Instance) (targets []*MigrationTarget, err error) {
	if !GetMemberShip(ctx).CheckSystemPermission() {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)
	volumes, err := instanceDisks(db, ctx, instance)
	if err != nil {
		return
	}
	hypers := []*model.Hyper{}
	q := db.Where("status = 1 AND hostid >= 0 AND hostid <> ?", instance.Hyper)
	if instance.ZoneID > 0 {
		q = q.Where("zone_id = ?", instance.ZoneID)
	}
	if err = q.Order("hostid").Find(&hypers).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query hypervisors", err)
	}
	pools := []*model.StoragePool{}
	db.Where("status = ?", model.StoragePoolActive).Order("name").Find(&pools)
	targets = []*MigrationTarget{}
	for _, h := range hypers {
		t := &MigrationTarget{Hostid: h.Hostid, Hostname: h.Hostname, Usable: true, Disks: []*DiskTarget{}}
		for _, v := range volumes {
			d := &DiskTarget{VolumeUUID: v.UUID, VolumeName: v.Name, Booting: v.Booting, SizeGB: v.Size, SourcePool: v.StoragePool.Name, Choices: []*PoolChoice{}}
			for _, p := range pools {
				hsp, uerr := poolUsableOn(db, p, h.Hostid, false)
				if uerr != nil {
					continue
				}
				c := &PoolChoice{UUID: p.UUID, Name: p.Name, AvailBytes: hsp.AvailBytes, Fits: true}
				if _, aerr := admit(db, p, h.Hostid, int64(v.Size), false); aerr != nil {
					c.Fits, c.Reason = false, aerr.Error()
				}
				d.Choices = append(d.Choices, c)
				if p.ID == v.StoragePoolID && c.Fits {
					d.CanStay = true
				}
			}
			if !d.CanStay {
				if fb := fallbackPool(db, v.StoragePool, h.Hostid); fb != nil {
					for _, c := range d.Choices {
						if c.UUID == fb.UUID && c.Fits {
							d.Fallback = c
						}
					}
				}
				if d.Fallback == nil && t.Usable {
					t.Usable = false
					t.Reason = fmt.Sprintf("volume %s can not stay in pool %s and no pool of its fallback group has room", v.Name, v.StoragePool.Name)
				}
			}
			t.Disks = append(t.Disks, d)
		}
		targets = append(targets, t)
	}
	return
}
