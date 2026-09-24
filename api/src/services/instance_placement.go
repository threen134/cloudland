/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
)

// schedulerResources is the demand cland checks when it picks a host: memory in KiB and disk in bytes, the units the
// hosts report in. The disk figure is only what lands in the built-in pool, since that is the room the hosts report
// to cland; disks in other pools are admitted by clapi itself and must not count against the built-in pool
func schedulerResources(cpu, memoryMB int32, builtinGB int64) string {
	return fmt.Sprintf("cpu=%d memory=%d disk=%d network=%d", cpu, int64(memoryMB)*1024, builtinGB<<30, 0)
}

// builtinDiskGB sums the disks of an instance that sit in the built-in pool. On a migration without a disk plan yet
// they are exactly what the target's built-in pool has to take: every host has the built-in pool so they stay in
// it, and a disk of another pool never falls back into it (fallback candidates exclude the built-in pool)
func builtinDiskGB(ctx context.Context, volumes []*model.Volume) (sizeGB int64) {
	for _, v := range volumes {
		if pool, err := VolumePool(ctx, v); err == nil && pool.Builtin {
			sizeGB += int64(v.Size)
		}
	}
	return
}

// instanceHyperGroup is the select= group for new instances whose boot disk goes to the built-in pool: the active
// hosts of the zone, without those whose built-in pool is too full to take more (§6.1)
func instanceHyperGroup(tx *gorm.DB, zoneID int64) (group string, err error) {
	hypers := []*model.Hyper{}
	q := tx.Where("status = 1 AND hostid >= 0")
	if zoneID > 0 {
		q = q.Where("zone_id = ?", zoneID)
	}
	if err = q.Order("hostid").Find(&hypers).Error; err != nil {
		return "", NewCLError(ErrSQLSyntaxError, "Failed to query hypervisors", err)
	}
	if len(hypers) == 0 {
		return "", NewCLError(ErrNoQualifiedHypervisor, "No qualified hypervisor found", nil)
	}
	builtin := &model.StoragePool{}
	if err = tx.Where("builtin = ?", true).Take(builtin).Error; err != nil {
		return "", NewCLError(ErrStoragePoolNotFound, "Built-in storage pool not found", err)
	}
	hostids := []int32{}
	for _, h := range hypers {
		hsp, gerr := getHyperPool(tx, h.Hostid, builtin.ID, false)
		if gerr == nil && hsp.CapacityAt != nil && hsp.UsageRatio() >= poolAdmitUsageLimit {
			logger.Infof("Host %s is skipped for new instances: its built-in storage is %.0f%% full", h.Hostname, hsp.UsageRatio()*100)
			continue
		}
		hostids = append(hostids, h.Hostid)
	}
	if len(hostids) == 0 {
		return "", NewCLError(ErrNoQualifiedHypervisor, "No hypervisor available: built-in storage is full on every host", nil)
	}
	return hyperGroupOf(zoneID, hostids), nil
}

// bootHost chooses the host of a new instance whose boot disk goes to a local pool other than the built-in one
// (§5.6, L4): clapi decides, as it has to check and reserve the pool before sending the command. Among the hosts
// where the pool is usable and has room, the one with the most free space wins. The row of the pool on the chosen
// host stays locked until the caller wrote the reservation in the same transaction.
func bootHost(tx *gorm.DB, pool *model.StoragePool, zoneID int64, hyperID int, sizeGB, cpu, memoryMB int32) (hostid int32, err error) {
	if hyperID >= 0 {
		if _, err = admitLocked(tx, pool, int32(hyperID), int64(sizeGB)); err != nil {
			return -1, err
		}
		return int32(hyperID), nil
	}
	hypers := []*model.Hyper{}
	q := tx.Preload("Resource").Where("status = 1 AND hostid >= 0")
	if zoneID > 0 {
		q = q.Where("zone_id = ?", zoneID)
	}
	if err = q.Order("hostid").Find(&hypers).Error; err != nil {
		return -1, NewCLError(ErrSQLSyntaxError, "Failed to query hypervisors", err)
	}
	hostid = -1
	var best int64 = -1
	var lastErr error
	for _, h := range hypers {
		// cland checks CPU and memory again; skipping hosts that clearly lack them saves a failed launch
		if r := h.Resource; r != nil && (r.Cpu < int64(cpu) || r.Memory < int64(memoryMB)*1024) {
			continue
		}
		hsp, aerr := admit(tx, pool, h.Hostid, int64(sizeGB), false)
		if aerr != nil {
			lastErr = aerr
			continue
		}
		if hsp.AvailBytes > best {
			best, hostid = hsp.AvailBytes, h.Hostid
		}
	}
	if hostid < 0 {
		if lastErr == nil {
			lastErr = NewCLError(ErrNoQualifiedHypervisor, fmt.Sprintf("No hypervisor has storage pool %s", pool.Name), nil)
		}
		return -1, lastErr
	}
	// Check again under the lock: another request may have taken the room meanwhile
	if _, err = admitLocked(tx, pool, hostid, int64(sizeGB)); err != nil {
		return -1, err
	}
	return
}
