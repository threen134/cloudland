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
)

// placeMigration decides the target of a migration of a placement group member when none is given, or checks the
// given one (placement-group-plan.md §6.3). It runs in the transaction of createOne and locks the group before any
// storage pool row. The target is -1 when the scheduler still picks it (ignore_placement without a target); warning
// names the rule of a best-effort group the given target breaks.
func placeMigration(ctx context.Context, tx *gorm.DB, instance *model.Instance, tgtHyper int32, opts *MigrationOptions, call *migrationCall) (target int32, warning string, err error) {
	group, err := lockPlacementGroup(tx, instance.PlacementGroupID)
	if err != nil {
		return -1, "", err
	}
	if opts.IgnorePlacement {
		return tgtHyper, "", nil
	}
	if group.Policy == model.PlacementPolicyPack && group.Strict {
		return placeStrictPack(ctx, tx, group, instance, tgtHyper, opts, call)
	}
	// The instance leaves its source: its share there does not count
	st, err := loadPlacementState(tx, group, map[int64]bool{instance.ID: true}, time.Now())
	if err != nil {
		return -1, "", err
	}
	label := hostLabeler(tx, true)
	if tgtHyper >= 0 {
		rule := placementViolation(group, st.Occ, tgtHyper, 1, label)
		if rule == "" {
			return tgtHyper, "", nil
		}
		if group.Strict {
			return -1, "", NewCLError(ErrPlacementGroupConflict, fmt.Sprintf("Target %s breaks the placement group: %s; migrate with ignore_placement to do it anyway", label(tgtHyper), rule), nil)
		}
		return tgtHyper, rule, nil
	}
	need, err := migrationDemand(ctx, tx, instance)
	if err != nil {
		return -1, "", err
	}
	g1, g2, err := migrationCandidates(tx, ctx, instance, opts)
	if err != nil {
		return -1, "", err
	}
	// Each set is filtered by the rules before choosing between them: when the hosts keeping every pool are all
	// taken by the group, a host needing a fallback pool is still fine
	var lastErr error
	for _, hostids := range [][]int32{g1, g2} {
		if len(hostids) == 0 {
			continue
		}
		slots, serr := hostSlots(tx, hostids)
		if serr != nil {
			return -1, "", serr
		}
		host, perr := Place(group, slots, st.Occ, st.Pending, need, need)
		if perr == nil {
			return host, "", nil
		}
		lastErr = perr
	}
	if lastErr == nil {
		return -1, "", NewCLError(ErrNoQualifiedHypervisor, fmt.Sprintf("No hypervisor can take the disks of instance %s", instance.Hostname), nil)
	}
	return -1, "", explainPlacement(st, zoneNameOf(tx, group.ZoneID), "the instance", true, lastErr)
}

// placeStrictPack moves a member of a strict pack group, which must go with every member on its host (§6.3). The whole
// group is checked each time, not only this instance: the migrations of the others are created one after the other
// and sent at once, so a member found unable to follow once the first ones left would split the group.
func placeStrictPack(ctx context.Context, tx *gorm.DB, group *model.PlacementGroup, instance *model.Instance, tgtHyper int32, opts *MigrationOptions, call *migrationCall) (target int32, warning string, err error) {
	now := time.Now()
	all, err := loadPlacementState(tx, group, nil, now)
	if err != nil {
		return -1, "", err
	}
	// A migration of the group from an earlier request is still running: the group sits on two hosts until it ends
	// and where it goes can not be told, the anchor could even be the source of this instance. The same guard as for
	// new members (§6.2)
	for _, m := range all.Migrating {
		if !call.started[m.Instance.ID] {
			return -1, "", NewCLError(ErrPlacementGroupBusy, fmt.Sprintf("Member %s of strict pack placement group %s is migrating; retry once it is done",
				m.Instance.Hostname, group.Name), nil)
		}
	}
	source := instance.Hyper
	moving := []*model.Instance{}
	exclude := map[int64]bool{}
	for _, m := range all.Members {
		if m.Host != source {
			continue
		}
		id := m.Instance.ID
		exclude[id] = true
		if call.started[id] {
			continue
		}
		if reason := strictPackBlocker(m, call); reason != "" {
			return -1, "", NewCLError(ErrPlacementGroupConflict, fmt.Sprintf("Strict pack placement group %s has members on %s that can not migrate together: %s",
				group.Name, hostName(tx, source), reason), nil)
		}
		moving = append(moving, m.Instance)
	}
	// Without the share of the members leaving the source, the anchor is where the group goes: the target of those
	// already started, or the host of members moved away before (§2.3)
	st, err := loadPlacementState(tx, group, exclude, now)
	if err != nil {
		return -1, "", err
	}
	need, err := migrationDemand(ctx, tx, instance)
	if err != nil {
		return -1, "", err
	}
	rest := Demand{}
	for _, m := range moving {
		d, derr := migrationDemand(ctx, tx, m)
		if derr != nil {
			return -1, "", derr
		}
		rest = rest.Add(d)
	}
	anchor := packAnchor(st.Occ)
	if tgtHyper >= 0 {
		// The admin chose the host: the rules and the disks are checked, not CPU and memory (inter= is not checked
		// by cland either, so the members can not be refused one by one on the way)
		if anchor >= 0 && anchor != tgtHyper {
			return -1, "", NewCLError(ErrPlacementGroupConflict, fmt.Sprintf("The members of strict pack placement group %s are on %s; migrate there, or with ignore_placement",
				group.Name, hostName(tx, anchor)), nil)
		}
		if derr := groupDisksFit(ctx, tx, moving, tgtHyper, opts); derr != nil {
			return -1, "", NewCLError(ErrPlacementGroupConflict, fmt.Sprintf("%s can not take the disks of all %d members of strict pack placement group %s moving together: %s",
				hostName(tx, tgtHyper), len(moving), group.Name, errorMessage(derr)), nil)
		}
		return tgtHyper, "", nil
	}
	if anchor >= 0 {
		if anchor == source {
			// Every member on the source is left out above, so this means the state is not what it seems
			return -1, "", NewCLError(ErrPlacementGroupConflict, fmt.Sprintf("Strict pack placement group %s already sits on the host of %s", group.Name, instance.Hostname), nil)
		}
		hyper := &model.Hyper{}
		if herr := tx.Where("hostid = ?", anchor).Take(hyper).Error; herr != nil || hyper.Status != 1 || hyper.ZoneID != instance.ZoneID {
			return -1, "", NewCLError(ErrPlacementGroupHostFull, fmt.Sprintf("The host of strict pack placement group %s is not available", group.Name), nil)
		}
		// CPU and memory are checked with ignore_capacity too: cland checks them on the single host all the same,
		// ignore_capacity only skips the room of the storage pools
		slots, serr := hostSlots(tx, []int32{anchor})
		if serr != nil {
			return -1, "", serr
		}
		if len(slots) == 0 || !slots[0].fits(st.Pending[anchor], rest) {
			return -1, "", NewCLError(ErrPlacementGroupHostFull, fmt.Sprintf("%s, the host of strict pack placement group %s, can not take the %d members moving there",
				hyper.Hostname, group.Name, len(moving)), nil)
		}
		if derr := groupDisksFit(ctx, tx, moving, anchor, opts); derr != nil {
			return -1, "", NewCLError(ErrPlacementGroupHostFull, fmt.Sprintf("%s, the host of strict pack placement group %s, can not take the disks of the members moving there: %s",
				hyper.Hostname, group.Name, errorMessage(derr)), nil)
		}
		return anchor, "", nil
	}
	g1, g2, err := migrationCandidates(tx, ctx, instance, opts)
	if err != nil {
		return -1, "", err
	}
	// Only a disk that finds no room says more than "no host takes them all"
	var diskErr error
	for _, hostids := range [][]int32{g1, g2} {
		fit := []int32{}
		for _, h := range hostids {
			if derr := groupDisksFit(ctx, tx, moving, h, opts); derr != nil {
				diskErr = derr
				continue
			}
			fit = append(fit, h)
		}
		if len(fit) == 0 {
			continue
		}
		slots, serr := hostSlots(tx, fit)
		if serr != nil {
			return -1, "", serr
		}
		if host, perr := Place(group, slots, st.Occ, st.Pending, need, rest); perr == nil {
			return host, "", nil
		}
	}
	msg := fmt.Sprintf("No available host of zone %s can take all %d members of strict pack placement group %s together (%d vCPUs, %d MiB of memory)",
		zoneNameOf(tx, group.ZoneID), len(moving), group.Name, rest.Cpu, rest.MemKiB/1024)
	if diskErr != nil {
		msg += "; " + errorMessage(diskErr)
	}
	return -1, "", NewCLError(ErrPlacementGroupHostFull, msg, nil)
}

// strictPackBlocker tells why a member on the source host of a strict pack group can not move with the others in
// this call, empty when it can: it must be in the request and in a state Create and createOne let through
func strictPackBlocker(m *placementMember, call *migrationCall) string {
	inst := m.Instance
	if !call.ids[inst.ID] {
		return fmt.Sprintf("%s is not in this migration request", inst.Hostname)
	}
	// Its own migration failed earlier in this call (maintenance goes on after a failure): it stays, so none may go
	if call.failed[inst.ID] {
		return fmt.Sprintf("the migration of %s failed", inst.Hostname)
	}
	if !m.Landed {
		return fmt.Sprintf("%s is still being created", inst.Hostname)
	}
	switch inst.Status {
	case model.InstanceStatusRunning, model.InstanceStatusShutoff:
		return ""
	case model.InstanceStatusPaused:
		if inst.Reason != InstanceReasonStorageFull {
			return ""
		}
		return fmt.Sprintf("%s is paused because its storage is full", inst.Hostname)
	}
	return fmt.Sprintf("%s is %s", inst.Hostname, inst.Status)
}

// migrationDemand is what an instance takes on its target: CPU, memory and its disks in the built-in pool
func migrationDemand(ctx context.Context, tx *gorm.DB, instance *model.Instance) (d Demand, err error) {
	volumes, err := instanceDisks(tx, ctx, instance)
	if err != nil {
		return
	}
	return Demand{Cpu: int64(instance.Cpu), MemKiB: int64(instance.Memory) * 1024, DiskBytes: builtinDiskGB(ctx, volumes) * gib}, nil
}

// groupDisksFit checks that the disks of every instance moving together find a pool on host and, added up per pool,
// fit in it (the whole group check of §6.3). Each migration still admits its own disks when it is created.
func groupDisksFit(ctx context.Context, tx *gorm.DB, instances []*model.Instance, host int32, opts *MigrationOptions) error {
	items := []*model.DiskPlanItem{}
	for _, inst := range instances {
		planned, err := planDisks(tx, ctx, inst, host, opts)
		if err != nil {
			return fmt.Errorf("instance %s: %s", inst.Hostname, errorMessage(err))
		}
		items = append(items, planned...)
	}
	if opts.IgnoreCapacity {
		return nil
	}
	return admitPlan(tx, nil, items, host, false)
}

// errorMessage is the message of an error without the code and details CLError.Error() adds
func errorMessage(err error) string {
	if clErr, ok := err.(*CLError); ok {
		return clErr.Message
	}
	return err.Error()
}

func zoneNameOf(db *gorm.DB, zoneID int64) string {
	zone := &model.Zone{}
	if err := db.Where("id = ?", zoneID).Take(zone).Error; err != nil {
		return fmt.Sprintf("#%d", zoneID)
	}
	return zone.Name
}

// annotatePlacementTargets marks the migration targets that break the placement group of the instance (§7.1):
// unusable for a strict group, a warning for a best-effort one. The other members of a strict pack group on the same
// host are expected in the same request, so they do not count and a note says so.
func annotatePlacementTargets(db *gorm.DB, instance *model.Instance, targets []*MigrationTarget) error {
	if instance.PlacementGroupID == 0 {
		return nil
	}
	group := &model.PlacementGroup{}
	if err := db.Where("id = ?", instance.PlacementGroupID).Take(group).Error; err != nil {
		return nil
	}
	now := time.Now()
	exclude := map[int64]bool{instance.ID: true}
	others := 0
	if group.Policy == model.PlacementPolicyPack && group.Strict {
		all, err := loadPlacementState(db, group, nil, now)
		if err != nil {
			return err
		}
		for _, m := range all.Members {
			if m.Host == instance.Hyper && m.Instance.ID != instance.ID {
				exclude[m.Instance.ID] = true
				others++
			}
		}
	}
	st, err := loadPlacementState(db, group, exclude, now)
	if err != nil {
		return err
	}
	label := hostLabeler(db, true)
	for _, t := range targets {
		rule := placementViolation(group, st.Occ, t.Hostid, 1, label)
		switch {
		case rule != "" && group.Strict:
			if t.Usable {
				t.Usable, t.Reason, t.PlacementBlocked = false, rule, true
			} else {
				t.Reason += "; " + rule
			}
		case rule != "":
			t.PlacementWarning = rule
		case others > 0:
			t.PlacementWarning = fmt.Sprintf("the other %d member(s) of strict pack placement group %s on this host must migrate in the same request", others, group.Name)
		}
	}
	return nil
}
