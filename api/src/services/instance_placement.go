/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	hostids, err := instanceHyperIDs(tx, zoneID)
	if err != nil {
		return "", err
	}
	return hyperGroupOf(zoneID, hostids), nil
}

// instanceHyperIDs lists the hosts of instanceHyperGroup
func instanceHyperIDs(tx *gorm.DB, zoneID int64) (hostids []int32, err error) {
	hypers := []*model.Hyper{}
	q := tx.Where("status = 1 AND hostid >= 0")
	if zoneID > 0 {
		q = q.Where("zone_id = ?", zoneID)
	}
	if err = q.Order("hostid").Find(&hypers).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query hypervisors", err)
	}
	if len(hypers) == 0 {
		return nil, NewCLError(ErrNoQualifiedHypervisor, "No qualified hypervisor found", nil)
	}
	builtin := &model.StoragePool{}
	if err = tx.Where("builtin = ?", true).Take(builtin).Error; err != nil {
		return nil, NewCLError(ErrStoragePoolNotFound, "Built-in storage pool not found", err)
	}
	for _, h := range hypers {
		hsp, gerr := getHyperPool(tx, h.Hostid, builtin.ID, false)
		if gerr == nil && hsp.CapacityAt != nil && hsp.UsageRatio() >= poolAdmitUsageLimit {
			logger.Infof("Host %s is skipped for new instances: its built-in storage is %.0f%% full", h.Hostname, hsp.UsageRatio()*100)
			continue
		}
		hostids = append(hostids, h.Hostid)
	}
	if len(hostids) == 0 {
		return nil, NewCLError(ErrNoQualifiedHypervisor, "No hypervisor available: built-in storage is full on every host", nil)
	}
	return
}

// creationPlacement places the instances of one creation request in a placement group, one after the other, under
// the lock of the group (placement-group-plan.md §6.2)
type creationPlacement struct {
	group *model.PlacementGroup
	state *placementState
	zone  string
	slots []*HostSlot
	need  Demand
	count int
	left  int
	// The host the admin gave: only the rules are checked (§6.2), -1 otherwise
	fixedHost int32
}

// startCreationPlacement locks the group and reads its occupancy. Candidates are what the code without groups
// would consider (§3.4): the active hosts of the zone whose built-in pool is not too full, or those where the pool of
// the boot disk admits the disk. bootHost is left as it is: instances outside groups keep choosing hosts as before.
func startCreationPlacement(tx *gorm.DB, group *model.PlacementGroup, zone *model.Zone, bootPool *model.StoragePool, hyperID, count int, cpu, memoryMB, diskGB int32) (p *creationPlacement, err error) {
	if group, err = lockPlacementGroup(tx, group.ID); err != nil {
		return
	}
	if group.ZoneID != zone.ID {
		return nil, NewCLError(ErrPlacementGroupConflict, fmt.Sprintf("Placement group %s is in another zone than %s", group.Name, zone.Name), nil)
	}
	st, err := loadPlacementState(tx, group, nil, time.Now())
	if err != nil {
		return
	}
	// The anchor of a strict pack group is between two hosts while its members migrate: a new member could go the
	// other way than the group (§6.2)
	if group.Policy == model.PlacementPolicyPack && group.Strict && len(st.Migrating) > 0 {
		names := []string{}
		for _, m := range st.Migrating {
			note := m.Instance.Hostname
			if m.StaleMigration {
				note += fmt.Sprintf(" (migration %s has not moved for over %s)", m.Migration.UUID, placementStaleAfter)
			}
			names = append(names, note)
		}
		return nil, NewCLError(ErrPlacementGroupBusy, fmt.Sprintf("Members of strict pack placement group %s are migrating (%s); retry once they are done",
			group.Name, strings.Join(names, ", ")), nil)
	}
	p = &creationPlacement{group: group, state: st, zone: zone.Name, count: count, left: count, fixedHost: -1,
		need: Demand{Cpu: int64(cpu), MemKiB: int64(memoryMB) * 1024, DiskBytes: int64(diskGB) * gib}}
	if hyperID >= 0 {
		if rule := placementViolation(group, st.Occ, int32(hyperID), count, hostLabeler(tx, true)); rule != "" && group.Strict {
			return nil, NewCLError(ErrPlacementGroupConflict, "The given hypervisor breaks the placement group: "+rule, nil)
		}
		// A best-effort group accepts it: the admin chose the host on purpose, the group detail shows it is broken
		p.fixedHost = int32(hyperID)
		return
	}
	if bootPool.Builtin {
		var hostids []int32
		if hostids, err = instanceHyperIDs(tx, zone.ID); err != nil {
			return nil, err
		}
		p.slots, err = hostSlots(tx, hostids)
		return
	}
	// The pool of the boot disk bounds the disk; the pending disks of the members are in the pool's reservations
	// already (§5), so only their CPU and memory stay pending
	for host, d := range st.Pending {
		d.DiskBytes = 0
		st.Pending[host] = d
	}
	hypers := []*model.Hyper{}
	if err = tx.Where("status = 1 AND hostid >= 0 AND zone_id = ?", zone.ID).Order("hostid").Find(&hypers).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query hypervisors", err)
	}
	hostids := []int32{}
	for _, h := range hypers {
		hostids = append(hostids, h.Hostid)
	}
	slots, err := hostSlots(tx, hostids)
	if err != nil {
		return nil, err
	}
	var admitErr error
	for _, s := range slots {
		hsp, aerr := admit(tx, bootPool, s.Hostid, int64(diskGB), false)
		if aerr != nil {
			admitErr = aerr
			continue
		}
		allocated, aerr := allocatedBytes(tx, s.Hostid, bootPool.ID)
		if aerr != nil {
			return nil, NewCLError(ErrSQLSyntaxError, "Failed to sum the allocations of the pool", aerr)
		}
		s.FreeDiskBytes = capacityLimit(bootPool, hsp) - allocated
		p.slots = append(p.slots, s)
	}
	// The pool refuses the disk everywhere: say why, like bootHost does for instances outside groups, rather than a
	// message about the group that would send the user to look at the wrong thing
	if len(p.slots) == 0 && admitErr != nil {
		return nil, admitErr
	}
	return
}

// next picks the host of the next instance and counts it in the group
func (p *creationPlacement) next() (host int32, err error) {
	if p.fixedHost >= 0 {
		host = p.fixedHost
	} else if host, err = Place(p.group, p.slots, p.state.Occ, p.state.Pending, p.need, p.need.Times(int64(p.left))); err != nil {
		return -1, explainPlacement(p.state, p.zone, p.what(), false, err)
	}
	p.state.Occ[host]++
	p.state.Pending[host] = p.state.Pending[host].Add(p.need)
	p.left--
	return
}

// what names the instances next places, for the messages: the members placed earlier by the same request count
// in the group already, and a strict pack group needs room for all that are left at once
func (p *creationPlacement) what() string {
	switch {
	case p.count == 1:
		return "the instance"
	case p.group.Policy == model.PlacementPolicyPack && p.group.Strict:
		return fmt.Sprintf("the %d remaining instance(s) of this request", p.left)
	}
	return fmt.Sprintf("instance %d of the %d in this request", p.count-p.left+1, p.count)
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
