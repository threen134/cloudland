/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var (
	migrationAdmin = &MigrationAdmin{}
)

type MigrationAdmin struct{}

// MigrationResult is what happened to one instance of a migration request
type MigrationResult struct {
	Instance  *model.Instance
	Migration *model.Migration
	Error     error
}

// Create starts migrating instances. Every instance gets its own transaction and its command is sent after the commit,
// so one instance failing does not undo the others whose commands are already on their way (§7.1).
// With batch set (maintenance), an instance that can not move is recorded as not_doing and the rest go on;
// otherwise the first failure is returned.
func (a *MigrationAdmin) Create(ctx context.Context, name string, instances []*model.Instance, force bool, tgtHyper int32, opts *MigrationOptions, batch bool) (migrations []*model.Migration, results []*MigrationResult, err error) {
	logger.Ctx(ctx).Debugf("Start migrating instances to %d, migration type %t", tgtHyper, force)
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckSystemPermission() {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	if opts == nil {
		opts = &MigrationOptions{AllowPoolFallback: true}
	}
	if len(opts.Disks) > 0 && tgtHyper < 0 {
		err = NewCLError(ErrInvalidParameter, "Target pools of disks can only be given together with a target hypervisor", nil)
		return
	}
	db := dbs.DBContext(ctx)
	if tgtHyper > -1 {
		targetHyper := &model.Hyper{}
		if err = db.Where("hostid = ?", tgtHyper).Take(targetHyper).Error; err != nil {
			err = NewCLError(ErrHypervisorNotFound, "Failed to find target hypervisor", err)
			return
		}
		// Only an active target: maintenance (2), disabled (0) and deploying (4) hosts would be bad places to land
		if targetHyper.Status != 1 {
			err = NewCLError(ErrHypervisorInvalidState,
				fmt.Sprintf("Target hypervisor %s is not active (status %d)", targetHyper.Hostname, targetHyper.Status), nil)
			return
		}
	}
	// A forced migration is an evacuation of instances whose source host is down: their disks must all be in shared
	// pools (shared-storage-design.md §11.3); local disks are only on the source host
	if force {
		return a.createForced(ctx, instances, tgtHyper, opts, batch)
	}
	for _, instance := range instances {
		sourceHyper := &model.Hyper{}
		if err = db.Where("hostid = ?", instance.Hyper).Take(sourceHyper).Error; err != nil {
			err = NewCLError(ErrHypervisorNotFound, "Failed to query source hypervisor", err)
			return
		}
		if sourceHyper.Status == 10 {
			err = NewCLError(ErrOperationNotSupported, fmt.Sprintf("Source hypervisor of instance %s is offline, instances with local storage can not be migrated", instance.Hostname), nil)
			return
		}
	}
	call := &migrationCall{ids: map[int64]bool{}, started: map[int64]bool{}, failed: map[int64]bool{}}
	for _, instance := range instances {
		call.ids[instance.ID] = true
	}
	for _, instance := range instances {
		if instance.Status != model.InstanceStatusShutoff && instance.Status != model.InstanceStatusRunning && instance.Status != model.InstanceStatusPaused {
			continue
		}
		// A reinstall waiting for its image runs later on the host it was meant for
		if werr := refuseWhileWaiting(dbs.DBContext(ctx), instance); werr != nil {
			results = append(results, &MigrationResult{Instance: instance, Error: werr})
			if !batch {
				err = werr
				return
			}
			continue
		}
		if instance.Hyper == tgtHyper {
			logger.Ctx(ctx).Error("No need to migrate if source and target hypervisors are the same")
			continue
		}
		migration, merr := a.createOne(ctx, name, instance, tgtHyper, opts, batch, call)
		results = append(results, &MigrationResult{Instance: instance, Migration: migration, Error: merr})
		if merr != nil && !batch {
			err = merr
			return
		}
		if migration != nil {
			migrations = append(migrations, migration)
		}
		if merr == nil {
			call.started[instance.ID] = true
		} else {
			call.failed[instance.ID] = true
		}
	}
	return
}

// migrationCall is what one Create call knows about its instances, for the members of a strict pack placement group
// that have to move together (placement-group-plan.md §6.3). Where the started ones go is read from the database.
type migrationCall struct {
	ids     map[int64]bool // every instance of the call
	started map[int64]bool // instances whose migration was created and sent
	failed  map[int64]bool // instances whose migration could not be created or sent: they stay where they are
}

func (a *MigrationAdmin) createOne(ctx context.Context, name string, instance *model.Instance, tgtHyper int32, opts *MigrationOptions, batch bool, call *migrationCall) (migration *model.Migration, err error) {
	memberShip := GetMemberShip(ctx)
	// An instance paused because its pool is full would stay paused on the target, and the reason would be lost
	if instance.Status == model.InstanceStatusPaused && instance.Reason == InstanceReasonStorageFull {
		return nil, NewCLError(ErrInstanceInvalidState, fmt.Sprintf("Instance %s is paused because its storage is full; free some space or shut it off first", instance.Hostname), nil)
	}
	tx := dbs.DBContext(ctx).Begin()
	txCtx := SetContextDB(ctx, tx)
	finished := false
	defer func() {
		if !finished {
			tx.Rollback()
		}
	}()
	sourceHyper := &model.Hyper{}
	if err = tx.Where("hostid = ?", instance.Hyper).Take(sourceHyper).Error; err != nil {
		return nil, NewCLError(ErrHypervisorNotFound, "Failed to query source hypervisor", err)
	}
	// The boot volume is checked before the migration record is written: a record left in in_progress would keep the
	// heartbeat from updating the instance for 10 minutes
	bootVolume := &model.Volume{}
	if err = tx.Where("instance_id = ? AND booting = ?", instance.ID, true).Take(bootVolume).Error; err != nil {
		return nil, NewCLError(ErrBootVolumeNotFound, "Instance has no boot volume", err)
	}
	status := "in_progress"
	task1 := &model.Task{Name: "Prepare_Target", Summary: "Prepare resources on target hypervisor", Status: model.TaskStatus(status)}
	requests, _ := json.Marshal(opts.Disks)
	migration = &model.Migration{
		Model:             model.Model{Creater: memberShip.UserID},
		Name:              name,
		InstanceID:        instance.ID,
		Type:              "warm",
		CreaterName:       memberShip.UserName,
		CreaterUUID:       memberShip.UserUUID,
		SourceHyper:       instance.Hyper,
		TargetHyper:       tgtHyper,
		Phases:            []*model.Task{task1},
		Status:            status,
		DiskRequests:      string(requests),
		AllowPoolFallback: opts.AllowPoolFallback,
		IgnoreCapacity:    opts.IgnoreCapacity,
		IgnorePlacement:   opts.IgnorePlacement && instance.PlacementGroupID > 0,
		PriorStatus:       string(instance.Status),
	}
	migration.Instance = instance
	if err = tx.Create(migration).Error; err != nil {
		return nil, NewCLError(ErrMigrationCreateFailed, "DB create migration failed", err)
	}
	control := fmt.Sprintf("inter=%d", tgtHyper)
	var planErr error
	// Members of a placement group: the group decides or checks the target, under its lock (placement-group-plan.md §6.3)
	placed := false
	if instance.PlacementGroupID > 0 {
		var target int32
		target, migration.PlacementWarning, planErr = placeMigration(txCtx, tx, instance, tgtHyper, opts, call)
		if planErr == nil && tgtHyper < 0 && target >= 0 {
			// Chosen here, so handled like a given target from now on. It must be written, not only set in memory:
			// the callbacks and the occupancy of the group read target_hyper from the database
			placed, tgtHyper, migration.TargetHyper = true, target, target
			if uerr := tx.Model(&model.Migration{}).Where("id = ?", migration.ID).Update("target_hyper", target).Error; uerr != nil {
				planErr = NewCLError(ErrMigrationUpdateFailed, "Failed to save the target of the migration", uerr)
			}
		}
	}
	if planErr == nil && tgtHyper >= 0 {
		// Target known: plan, check and reserve now, a failure is returned at once
		_, planErr = PlanMigrationTarget(txCtx, migration, instance, tgtHyper)
		control = fmt.Sprintf("inter=%d", tgtHyper)
		if planErr == nil && placed {
			// cland still checks CPU and memory, on that single host
			var volumes []*model.Volume
			if volumes, planErr = instanceDisks(tx, txCtx, instance); planErr == nil {
				rcNeeded := schedulerResources(instance.Cpu, instance.Memory, builtinDiskGB(txCtx, volumes))
				control = "select=" + hyperGroupOf(instance.ZoneID, []int32{tgtHyper}) + " " + rcNeeded
			}
		}
	} else if planErr == nil {
		// Prefer hosts where every disk keeps its pool; hosts needing a fallback pool only when there are none
		var g1, g2 []int32
		g1, g2, planErr = migrationCandidates(tx, txCtx, instance, opts)
		candidates := g1
		if len(candidates) == 0 {
			candidates = g2
		}
		if planErr == nil && len(candidates) == 0 {
			planErr = NewCLError(ErrNoQualifiedHypervisor, fmt.Sprintf("No hypervisor can take the disks of instance %s", instance.Hostname), nil)
		}
		var volumes []*model.Volume
		if planErr == nil {
			volumes, planErr = instanceDisks(tx, txCtx, instance)
		}
		if planErr == nil {
			rcNeeded := schedulerResources(instance.Cpu, instance.Memory, builtinDiskGB(txCtx, volumes))
			// A space must separate the group from the resources: cland takes select= up to the first blank as the group
			control = "select=" + hyperGroupOf(instance.ZoneID, candidates) + " " + rcNeeded
		}
	}
	if planErr != nil {
		if !batch {
			return nil, planErr
		}
		// Maintenance: keep a record saying why this instance stays
		tx.Rollback()
		finished = true
		migration.ID, task1.ID = 0, 0
		task1.Status, task1.Summary, task1.Message = "not_doing", "No qualified target", planErr.Error()
		migration.Status = "not_doing"
		migration.Phases = []*model.Task{task1}
		if cerr := dbs.DBContext(ctx).Create(migration).Error; cerr != nil {
			logger.Ctx(ctx).Errorf("Failed to record the skipped migration of %s: %v", instance.Hostname, cerr)
		}
		return migration, planErr
	}
	if err = tx.Model(&model.Instance{}).Where("id = ?", instance.ID).Update("status", model.InstanceStatusMigrating).Error; err != nil {
		return nil, NewCLError(ErrInstanceUpdateFailed, "Failed to update instance status", err)
	}
	var metadata string
	if metadata, err = instanceAdmin.GetMetadata(txCtx, instance, ""); err != nil {
		return nil, err
	}
	cpu, memory, disk := instance.Cpu, instance.Memory, instance.Disk
	if flavor := instance.Flavor; flavor != nil {
		cpu, memory, disk = flavor.Cpu, flavor.Memory, flavor.Disk
	}
	bootLoader := "bios"
	if instance.Image != nil {
		bootLoader = instance.Image.BootLoader
	}
	finished = true
	if err = tx.Commit().Error; err != nil {
		return nil, NewCLError(ErrMigrationCreateFailed, "Failed to commit the migration", err)
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/target_migration.sh '%d' '%d' '%d' '%s' '%d' '%d' '%d' '%s' '%s' '%s' '%s'<<'EOF'\n%s\nEOF", migration.ID, task1.ID, instance.ID, ShellEscape(instance.Hostname), cpu, memory, disk, ShellEscape(sourceHyper.Hostname), ShellEscape(migration.Type), ShellEscape(bootLoader), ShellEscape(instance.UUID), base64.StdEncoding.EncodeToString([]byte(metadata)))
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Error("Target migration command execution failed", err)
		db := dbs.DBContext(ctx)
		db.Model(&model.Migration{}).Where("id = ?", migration.ID).Update("status", "failed")
		db.Model(&model.Task{}).Where("id = ?", task1.ID).Updates(map[string]interface{}{"status": "failed", "message": err.Error()})
		db.Model(&model.Instance{}).Where("id = ?", instance.ID).Update("status", instance.Status)
		ReleaseReservations(ctx, migration.ID, 0, model.ReservationMigration)
		return migration, err
	}
	return migration, nil
}

// requireMigrationAdmin refuses everyone but system admins. Migrations are not scoped to an organization: a record
// names instances of any organization and the hosts they move between, so reading one is an admin operation like
// creating one. Checked before any query, so that other users can not tell whether a migration exists
func requireMigrationAdmin(ctx context.Context) error {
	if !GetMemberShip(ctx).CheckSystemPermission() {
		logger.Ctx(ctx).Error("Not authorized to get migration")
		return NewCLError(ErrPermissionDenied, "Not authorized to get migration", nil)
	}
	return nil
}

func (a *MigrationAdmin) GetMigrationByUUID(ctx context.Context, uuID string) (migration *model.Migration, err error) {
	if err = requireMigrationAdmin(ctx); err != nil {
		return
	}
	ctx, db := GetContextDB(ctx)
	migration = &model.Migration{}
	err = db.Preload("Instance").Preload("Phases").Where("uuid = ?", uuID).Take(migration).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query migration, %v", err)
		err = NewCLError(ErrMigrationNotFound, "Failed to find migration", err)
		return
	}
	return
}

func (a *MigrationAdmin) GetMigrationByName(ctx context.Context, name string) (migration *model.Migration, err error) {
	if err = requireMigrationAdmin(ctx); err != nil {
		return
	}
	ctx, db := GetContextDB(ctx)
	migration = &model.Migration{}
	err = db.Where("name = ?", name).Take(migration).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query migration, %v", err)
		err = NewCLError(ErrMigrationNotFound, "Failed to find migration", err)
		return
	}
	return
}

func (a *MigrationAdmin) Get(ctx context.Context, id int64) (migration *model.Migration, err error) {
	if id <= 0 {
		err = fmt.Errorf("Invalid migration ID: %d", id)
		logger.Ctx(ctx).Error(err)
		return
	}
	if err = requireMigrationAdmin(ctx); err != nil {
		return
	}
	ctx, db := GetContextDB(ctx)
	migration = &model.Migration{Model: model.Model{ID: id}}
	err = db.Take(migration).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to query migration, %v", err)
		err = NewCLError(ErrMigrationNotFound, "Failed to find migration", err)
		return
	}
	return
}

func (a *MigrationAdmin) GetMigration(ctx context.Context, reference *BaseReference) (migration *model.Migration, err error) {
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = fmt.Errorf("Migration base reference must be provided with either uuid or name")
		return
	}
	if reference.ID != "" {
		migration, err = a.GetMigrationByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		migration, err = a.GetMigrationByName(ctx, reference.Name)
		return
	}
	return
}

func (a *MigrationAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, migrations []*model.Migration, err error) {
	// The records are not filtered by organization: the list is for system admins only, like a single record
	if err = requireMigrationAdmin(ctx); err != nil {
		return
	}
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	migrations = []*model.Migration{}
	if err = db.Model(&model.Migration{}).Scopes(dbs.Contains(query, "name")).Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count migrations", err)
		return
	}
	db = db.Offset(int(offset)).Limit(int(limit))
	for _, o := range dbs.NewOrders(order) {
		// Records from before the progress column existed have NULL progress; keep them last in both directions
		if o == "progress" || o == "progress DESC" {
			o += " NULLS LAST"
		}
		db = db.Order(o)
	}
	if err = db.Preload("Instance").Preload("Phases").Scopes(dbs.Contains(query, "name")).Find(&migrations).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to query migrations", err)
		return
	}

	return
}
