/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"strconv"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

var (
	volumeAdmin = &VolumeAdmin{}
)

type VolumeAdmin struct{}

// A volume stays "deleting" at most this long without an answer from its host
const volumeDeleteTimeout = 30 * time.Minute

func (a *VolumeAdmin) Get(ctx context.Context, id int64) (volume *model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid volume ID: %d", id), nil)
		logger.Ctx(ctx).Error(err)
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	volume = &model.Volume{Model: model.Model{ID: id}}
	if err = db.Preload("Instance").Preload("StoragePool").Where(query, args...).Take(volume).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query volume, %v", err)
		err = NewCLError(ErrVolumeNotFound, "Failed to query volume", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, volume.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to read the volume")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the volume", nil)
		return
	}
	return
}

func (a *VolumeAdmin) GetVolumeByUUID(ctx context.Context, uuID string) (volume *model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.GetVolumeByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.GetVolumeByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.GetVolumeByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	volume = &model.Volume{}
	query, args := memberShip.GetOrgFilter()
	err = db.Preload("Instance").Preload("StoragePool").Where(query, args...).Where("uuid = ?", uuID).Take(volume).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB: query volume failed", err)
		err = NewCLError(ErrVolumeNotFound, "Volume not found", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, volume.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to read the volume")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the volume", nil)
		return
	}
	return
}

// CreateVolume creates the record of a volume in a pool; nothing is written on any host yet
func (a *VolumeAdmin) CreateVolume(ctx context.Context, name string, size int32, instanceID int64, booting bool, pool *model.StoragePool) (volume *model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.CreateVolume: name=%s, size=%d, instanceID=%d, booting=%v", name, size, instanceID, booting)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.CreateVolume: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.CreateVolume: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if pool == nil {
		if pool, err = storagePoolAdmin.GetDefaultPool(ctx); err != nil {
			return
		}
	}
	target := ""
	status := model.VolumeStatusAvailable
	if booting {
		target = "vda"
		status = model.VolumeStatusPending
	}
	memberShip := GetMemberShip(ctx)
	volume = &model.Volume{
		Model:         model.Model{Creater: memberShip.UserID},
		Owner:         memberShip.OrgID,
		Name:          name,
		InstanceID:    instanceID,
		Booting:       booting,
		Format:        "qcow2",
		Target:        target,
		Size:          int32(size),
		Status:        status,
		StoragePoolID: pool.ID,
	}
	if err = db.Create(volume).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to create volume", err)
		err = NewCLError(ErrVolumeCreationFailed, "Failed to create volume", err)
		return
	}
	volume.StoragePool = pool
	volume.Path = PoolRelPath(pool, volume)
	if err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Update("path", volume.Path).Error; err != nil {
		err = NewCLError(ErrVolumeCreationFailed, "Failed to create volume", err)
	}
	return
}

// Create makes a data volume. Local volumes are not written anywhere until they are attached for the first
// time: only then is it known which host they belong to.
func (a *VolumeAdmin) Create(ctx context.Context, name string, size int32, pool *model.StoragePool) (volume *model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.Create: name=%s, size=%d", name, size)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgWriter) {
		logger.Ctx(ctx).Error("Not authorized to create volume")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create volume", nil)
		return
	}
	if pool != nil && pool.Status != model.StoragePoolActive {
		return nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Storage pool %s is disabled", pool.Name), nil)
	}
	return a.CreateVolume(ctx, name, size, 0, false, pool)
}

// attachTarget renders Update's instID for logs: "keep" when the attachment is left alone
func attachTarget(instID *int64) string {
	if instID == nil {
		return "keep"
	}
	return strconv.FormatInt(*instID, 10)
}

// UpdateByUUID renames the volume (when name is not empty) and changes its attachment:
// instID nil leaves it alone, 0 detaches, any other value attaches to that instance.
func (a *VolumeAdmin) UpdateByUUID(ctx context.Context, uuid string, name string, instID *int64) (volume *model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.UpdateByUUID: uuid=%s, name=%s, instID=%s", uuid, name, attachTarget(instID))
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.UpdateByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.UpdateByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	volume = &model.Volume{}
	if err = db.Where("uuid = ?", uuid).Take(volume).Error; err != nil {
		logger.Ctx(ctx).Error("DB: query volume failed", err)
		err = NewCLError(ErrVolumeNotFound, "Volume not found", err)
		return
	}
	return a.Update(context.WithoutCancel(ctx), volume.ID, name, instID)
}

// volumeCommand is a node command to send once the transaction deciding it has committed
type volumeCommand struct {
	control string
	command string
}

// Update: see UpdateByUUID for the meaning of instIDArg
func (a *VolumeAdmin) Update(ctx context.Context, id int64, name string, instIDArg *int64) (volume *model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.Update: id=%d, name=%s, instID=%s", id, name, attachTarget(instIDArg))
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.Update: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.Update: success")
		}
	}()
	var cmd *volumeCommand
	firstAttach := false
	tx := dbs.DBContext(ctx).Begin()
	txCtx := SetContextDB(ctx, tx)
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		if err = tx.Commit().Error; err != nil {
			err = NewCLError(ErrVolumeUpdateFailed, "Failed to update volume", err)
			return
		}
		// Commands go out only after commit, so their callbacks always see what was decided here
		if cmd != nil {
			err = HyperExecute(ctx, cmd.control, cmd.command)
			if err != nil {
				logger.Ctx(ctx).Error("Volume command execution failed", err)
				a.revertVolumeCommand(ctx, volume, firstAttach)
			}
		}
	}()
	volume = &model.Volume{Model: model.Model{ID: id}}
	if err = tx.Preload("Instance").Preload("StoragePool").Take(volume).Error; err != nil {
		logger.Ctx(ctx).Error("DB: query volume failed", err)
		err = NewCLError(ErrVolumeNotFound, "Volume not found", err)
		return
	}
	// No instance in the request keeps the current attachment, so a rename never detaches
	instID := volume.InstanceID
	if instIDArg != nil {
		instID = *instIDArg
	}
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, volume.Owner) {
		logger.Ctx(ctx).Error("Not authorized to update the volume")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the volume", nil)
		return
	}
	if name != "" {
		volume.Name = name
	}
	if volume.InstanceID != instID {
		switch volume.Status {
		case model.VolumeStatusError, model.VolumeStatusDeleting, model.VolumeStatusDeleteFailed, model.VolumeStatusLost, model.VolumeStatusOrphaned:
			err = NewCLError(ErrVolumeInvalidState, fmt.Sprintf("Volume %s is %s, it can not be attached or detached", volume.UUID, volume.Status), nil)
			return
		}
		if volume.IsBusy() {
			logger.Ctx(ctx).Error("Volume is busy, cannot be updated", volume.Status)
			err = NewCLError(ErrVolumeIsBusy, fmt.Sprintf("Volume is busy, cannot be updated, status: %s", volume.Status), nil)
			return
		}
	}
	if volume.InstanceID > 0 && instID > 0 && volume.InstanceID != instID {
		err = NewCLError(ErrVolumeIsInUse, "Please detach volume before attach it to new instance", nil)
		return
	}
	pool, err := VolumePool(txCtx, volume)
	if err != nil {
		return
	}
	if volume.InstanceID > 0 && instID == 0 && volume.IsAttached() {
		if volume.Booting {
			logger.Ctx(ctx).Error("Boot volume can not be detached")
			err = NewCLError(ErrBootVolumeCannotDetach, "Boot volume can not be detached", nil)
			return
		}
		instance := &model.Instance{Model: model.Model{ID: volume.InstanceID}}
		if err = tx.Take(instance).Error; err != nil {
			logger.Ctx(ctx).Error("DB: query instance failed", err)
			err = NewCLError(ErrInstanceNotFound, "Instance not found", err)
			return
		}
		if err = instanceBusyForVolumes(instance); err != nil {
			return
		}
		cmd = &volumeCommand{
			control: fmt.Sprintf("inter=%d", instance.Hyper),
			command: fmt.Sprintf("/opt/cloudland/scripts/backend/detach_volume_local.sh '%d' '%d' '%s'", instance.ID, volume.ID, ShellEscape(volume.UUID)),
		}
		volume.Status = model.VolumeStatusDetaching
	} else if instID > 0 && volume.InstanceID == 0 && volume.Status == model.VolumeStatusAvailable {
		instance := &model.Instance{Model: model.Model{ID: instID}}
		if err = tx.Take(instance).Error; err != nil {
			logger.Ctx(ctx).Error("DB: query instance failed", err)
			err = NewCLError(ErrInstanceNotFound, "Instance not found", err)
			return
		}
		if !memberShip.CheckResourceOrg(model.OrgWriter, instance.Owner) {
			err = NewCLError(ErrPermissionDenied, "Not authorized to attach volumes to the instance", nil)
			return
		}
		if err = instanceBusyForVolumes(instance); err != nil {
			return
		}
		mode := "existing"
		if volume.Hyper > 0 {
			// The file of a local volume lives on one host only
			if volume.Hyper != instance.Hyper {
				err = NewCLError(ErrVolumeInvalidState, fmt.Sprintf("Local volume %s is on %s, it can not be attached to an instance on %s",
					volume.UUID, hostName(tx, volume.Hyper), hostName(tx, instance.Hyper)), nil)
				return
			}
			if _, err = poolUsableOn(tx, pool, instance.Hyper, false); err != nil {
				return
			}
		} else {
			// First attachment: the file is created on the host of the instance. Admit it and count it there
			// right away (§5.2), so no reservation is needed and a lost callback can not lose track of it.
			if _, err = admitLocked(tx, pool, instance.Hyper, int64(volume.Size)); err != nil {
				return
			}
			volume.Hyper = instance.Hyper
			mode = "new"
			firstAttach = true
		}
		cmd = &volumeCommand{
			control: fmt.Sprintf("inter=%d", instance.Hyper),
			command: fmt.Sprintf("/opt/cloudland/scripts/backend/attach_volume_local.sh '%d' '%d' '%s' '%s' '%d' '%s' '%d' '%s'",
				instance.ID, volume.ID, ShellEscape(VolumeAbsPath(pool, volume)), ShellEscape(volume.UUID), volume.Size,
				ShellEscape(PoolScriptID(pool)), instance.Hyper, ShellEscape(mode)),
		}
		volume.Status = model.VolumeStatusAttaching
	}
	updates := map[string]interface{}{"name": volume.Name, "status": volume.Status, "target": volume.Target, "hyper": volume.Hyper}
	if cmd != nil {
		// An attach or detach starts afresh; a rename keeps the reason of a lost or delete_failed volume
		updates["reason"] = ""
	}
	if err = tx.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(updates).Error; err != nil {
		logger.Ctx(ctx).Error("DB: update volume failed", err)
		err = NewCLError(ErrVolumeUpdateFailed, "Failed to update volume", err)
		return
	}
	return
}

// revertVolumeCommand undoes the state change of an attach or detach whose command could not be sent
func (a *VolumeAdmin) revertVolumeCommand(ctx context.Context, volume *model.Volume, firstAttach bool) {
	updates := map[string]interface{}{"reason": "the command could not be sent to the host"}
	switch volume.Status {
	case model.VolumeStatusAttaching:
		updates["status"] = model.VolumeStatusAvailable
		// A first attachment counted the volume on the host before anything was written there
		if firstAttach {
			updates["hyper"] = 0
		}
	case model.VolumeStatusDetaching:
		updates["status"] = model.VolumeStatusAttached
	default:
		return
	}
	dbs.DBContext(ctx).Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(updates)
}

// Delete removes a volume. A volume written on a host is deleted by the host first; the record goes away when
// the host confirms (§5.5), so a host that is offline or a lost command can not leave the file behind silently.
// deferred reports that the deletion continues on the host.
func (a *VolumeAdmin) Delete(ctx context.Context, volume *model.Volume) (deferred bool, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.Delete: volumeID=%d, uuid=%s", volume.ID, volume.UUID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.Delete: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, volume.Owner) {
		logger.Ctx(ctx).Error("Not authorized to delete the volume")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the volume", nil)
		return
	}
	var cmd *volumeCommand
	tx := dbs.DBContext(ctx).Begin()
	txCtx := SetContextDB(ctx, tx)
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		if err = tx.Commit().Error; err != nil {
			err = NewCLError(ErrVolumeDeleteFailed, "Failed to delete volume", err)
			return
		}
		if cmd != nil {
			if err = HyperExecute(ctx, cmd.control, cmd.command); err != nil {
				logger.Ctx(ctx).Error("Delete volume execution failed", err)
				dbs.DBContext(ctx).Model(&model.Volume{}).Where("id = ? AND status = ?", volume.ID, model.VolumeStatusDeleting).
					Updates(map[string]interface{}{"status": model.VolumeStatusAvailable, "reason": "the delete command could not be sent to the host"})
			}
		}
	}()
	if err = tx.Take(volume, volume.ID).Error; err != nil {
		err = NewCLError(ErrVolumeNotFound, "Volume not found", err)
		return
	}
	switch volume.Status {
	case model.VolumeStatusLost:
		// The pool holding the file was declared lost: only the record goes
		err = a.dropRecord(tx, volume)
		return
	case model.VolumeStatusOrphaned:
		err = NewCLError(ErrVolumeInvalidState, "The volume waits for its pool to be adopted by a host; adopt the pool or abandon the orphans first", nil)
		return
	case model.VolumeStatusDeleting:
		err = NewCLError(ErrVolumeIsBusy, "The volume is being deleted", nil)
		return
	}
	if volume.IsAttached() || volume.InstanceID > 0 {
		logger.Ctx(ctx).Errorf("Volume is attached to an instance, cannot be deleted %+v", volume)
		err = NewCLError(ErrVolumeIsInUse, fmt.Sprintf("Volume[%s](%s) is attached to an instance, please detach it first", volume.Name, volume.UUID), nil)
		return
	}
	if volume.IsBusy() {
		logger.Ctx(ctx).Errorf("Volume is busy, cannot be deleted %+v", volume)
		err = NewCLError(ErrVolumeIsBusy, fmt.Sprintf("Volume[%s](%s) is busy, cannot be deleted", volume.Name, volume.UUID), nil)
		return
	}
	if volume.Hyper <= 0 {
		// Never attached: nothing was written on any host
		err = a.dropRecord(tx, volume)
		return
	}
	if _, online := hostOnline(tx, volume.Hyper); !online {
		err = NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline, the volume file can not be deleted now", hostName(tx, volume.Hyper)), nil)
		return
	}
	pool, err := VolumePool(txCtx, volume)
	if err != nil {
		return
	}
	if _, err = poolUsableOn(tx, pool, volume.Hyper, false); err != nil {
		return
	}
	if err = tx.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{
		"status": model.VolumeStatusDeleting, "reason": "",
	}).Error; err != nil {
		err = NewCLError(ErrVolumeDeleteFailed, "Failed to delete volume", err)
		return
	}
	cmd = &volumeCommand{
		control: fmt.Sprintf("inter=%d", volume.Hyper),
		command: fmt.Sprintf("/opt/cloudland/scripts/backend/clear_volume_local.sh '%d' '%s' '%s' '%s' '%d'",
			volume.ID, ShellEscape(volume.UUID), ShellEscape(VolumeAbsPath(pool, volume)), ShellEscape(PoolScriptID(pool)), volume.Hyper),
	}
	deferred = true
	return
}

// dropRecord soft-deletes a volume record
func (a *VolumeAdmin) dropRecord(tx *gorm.DB, volume *model.Volume) (err error) {
	if err = tx.Delete(&model.Volume{}, volume.ID).Error; err != nil {
		return NewCLError(ErrVolumeDeleteFailed, "Failed to delete volume", err)
	}
	return
}

func (a *VolumeAdmin) DeleteVolumeByUUID(ctx context.Context, uuID string) (deferred bool, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.DeleteVolumeByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.DeleteVolumeByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.DeleteVolumeByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	volume := &model.Volume{}
	if err = db.Where("uuid = ?", uuID).Take(volume).Error; err != nil {
		logger.Ctx(ctx).Error("DB: query volume failed", err)
		err = NewCLError(ErrVolumeNotFound, "Volume not found", err)
		return
	}
	return a.Delete(context.WithoutCancel(ctx), volume)
}

// ForceDetach removes a volume of a lost pool from the definition of its instance without touching the file (§5.8)
func (a *VolumeAdmin) ForceDetach(ctx context.Context, volume *model.Volume) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, volume.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to update the volume", nil)
	}
	if volume.Status != model.VolumeStatusLost {
		return NewCLError(ErrVolumeInvalidState, "Only volumes of a lost pool can be detached by force", nil)
	}
	if volume.InstanceID == 0 {
		return NewCLError(ErrVolumeInvalidState, "The volume is not attached", nil)
	}
	if volume.Booting {
		return NewCLError(ErrBootVolumeCannotDetach, "Boot volume can not be detached", nil)
	}
	db := dbs.DBContext(ctx)
	instance := &model.Instance{}
	if err = db.Take(instance, volume.InstanceID).Error; err != nil {
		return NewCLError(ErrInstanceNotFound, "Instance not found", err)
	}
	if err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{
		"instance_id": 0, "target": "",
	}).Error; err != nil {
		return NewCLError(ErrVolumeUpdateFailed, "Failed to update volume", err)
	}
	// The instance definition may still refer to the file; drop the disk from it for its next start
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/force_detach_volume.sh '%d' '%d' '%s'", instance.ID, volume.ID, ShellEscape(volume.Target))
	if xerr := HyperExecute(ctx, fmt.Sprintf("inter=%d", instance.Hyper), command); xerr != nil {
		logger.Ctx(ctx).Warningf("Force detach of volume %d: the instance definition could not be updated now: %v", volume.ID, xerr)
	}
	return
}

func (a *VolumeAdmin) Resize(ctx context.Context, volume *model.Volume, size int32) (err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.Resize: volumeID=%d, size=%d", volume.ID, size)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.Resize: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.Resize: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, volume.Owner) {
		logger.Ctx(ctx).Error("Not authorized to resize the volume")
		err = NewCLError(ErrPermissionDenied, "Not authorized to resize the volume", nil)
		return
	}
	var cmd *volumeCommand
	oldSize := volume.Size
	tx := dbs.DBContext(ctx).Begin()
	txCtx := SetContextDB(ctx, tx)
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		if err = tx.Commit().Error; err != nil {
			err = NewCLError(ErrVolumeUpdateFailed, "Failed to update volume", err)
			return
		}
		if cmd != nil {
			if err = HyperExecute(ctx, cmd.control, cmd.command); err != nil {
				// Roll back when the command could not be sent, otherwise the volume is left
				// in "resizing" with the new size and no callback ever resets it
				logger.Ctx(ctx).Error("Resize remote exec failed", err)
				dbs.DBContext(ctx).Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{
					"size": oldSize, "status": volumeUsableStatus(volume), "reason": "the resize command could not be sent to the host"})
				if volume.Booting && volume.InstanceID > 0 {
					dbs.DBContext(ctx).Model(&model.Instance{}).Where("id = ?", volume.InstanceID).Update("disk", oldSize)
				}
			}
		}
	}()
	if err = tx.Preload("Instance").Take(volume, volume.ID).Error; err != nil {
		err = NewCLError(ErrVolumeNotFound, "Volume not found", err)
		return
	}
	oldSize = volume.Size
	if volume.IsError() || volume.Status == model.VolumeStatusLost || volume.Status == model.VolumeStatusOrphaned ||
		volume.Status == model.VolumeStatusDeleting || volume.Status == model.VolumeStatusDeleteFailed {
		err = NewCLError(ErrVolumeInvalidState, fmt.Sprintf("Volume is %s", volume.Status), nil)
		return
	}
	if volume.IsBusy() {
		logger.Ctx(ctx).Error("Volume is busy")
		err = NewCLError(ErrVolumeIsBusy, "Volume is busy", nil)
		return
	}
	// The node resizes the image of a live VM through inst-N. During these states the disk is held by
	// something else (the inst-N-rescue domain, a migration copying it, a disk being rebuilt) or the VM
	// is being torn down, and resizing it would fail or corrupt the copy
	if volume.InstanceID > 0 {
		instance := &model.Instance{Model: model.Model{ID: volume.InstanceID}}
		if err = tx.Take(instance).Error; err != nil {
			logger.Ctx(ctx).Error("DB: query instance failed", err)
			err = NewCLError(ErrInstanceNotFound, "Instance not found", err)
			return
		}
		switch instance.Status {
		case model.InstanceStatusProvisioning, model.InstanceStatusMigrating, model.InstanceStatusRollback,
			model.InstanceStatusReinstalling, model.InstanceStatusResizing, model.InstanceStatusDeleting,
			model.InstanceStatusRescuing:
			err = NewCLError(ErrInstanceInvalidState, fmt.Sprintf("Cannot resize the volume while its instance is %s", instance.Status), nil)
			return
		}
	}
	if size <= volume.Size {
		logger.Ctx(ctx).Error("The size must be greater than the original size")
		err = NewCLError(ErrVolumeInvalidSize, "The size must be greater than the original size", nil)
		return
	}
	pool, err := VolumePool(txCtx, volume)
	if err != nil {
		return
	}
	if volume.Hyper > 0 {
		// The new size counts from now on (the volume row is updated below in the same transaction), so no reservation
		if _, err = admitLocked(tx, pool, volume.Hyper, int64(size-volume.Size)); err != nil {
			return
		}
	}
	status := model.VolumeStatusResizing
	if volume.Hyper <= 0 {
		// Not written anywhere yet: the new size is used when it is created at the first attachment
		status = model.VolumeStatusAvailable
	}
	if err = tx.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{
		"size": size, "status": status, "reason": "",
	}).Error; err != nil {
		logger.Ctx(ctx).Error("update volume failed", err)
		err = NewCLError(ErrVolumeUpdateFailed, "Failed to update volume", err)
		return
	}
	if volume.Booting {
		instance := &model.Instance{Model: model.Model{ID: volume.InstanceID}}
		if err = tx.Model(instance).Take(instance).Error; err != nil {
			logger.Ctx(ctx).Error("DB: query instance failed", err)
			err = NewCLError(ErrInstanceNotFound, "Instance not found", err)
			return
		}
		cpu, memory := instance.Cpu, instance.Memory
		if instance.Cpu == 0 {
			var flavor *model.Flavor
			flavor, err = flavorAdmin.Get(txCtx, instance.FlavorID)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to get flavor %+v, %+v", instance.FlavorID, err)
				err = NewCLError(ErrFlavorNotFound, "Flavor not found", err)
				return
			}
			cpu, memory = flavor.Cpu, flavor.Memory
		}
		if err = tx.Model(instance).Updates(map[string]interface{}{
			"cpu":    cpu,
			"memory": memory,
			"disk":   size,
		}).Error; err != nil {
			logger.Ctx(ctx).Error("DB: update instance failed", err)
			err = NewCLError(ErrInstanceUpdateFailed, "Failed to update instance", err)
			return
		}
	}
	if volume.Hyper <= 0 {
		return
	}
	cmd = &volumeCommand{
		control: fmt.Sprintf("inter=%d", volume.Hyper),
		command: fmt.Sprintf("/opt/cloudland/scripts/backend/resize_volume_local.sh '%d' '%s' '%d' '%t' '%d' '%s' '%s' '%d' '%d'",
			volume.ID, ShellEscape(volume.UUID), size, volume.Booting, volume.InstanceID, ShellEscape(VolumeAbsPath(pool, volume)),
			ShellEscape(PoolScriptID(pool)), volume.Hyper, oldSize),
	}
	return
}

// volumeUsableStatus is the status a volume goes back to after a failed operation
func volumeUsableStatus(volume *model.Volume) model.VolumeStatus {
	if volume.InstanceID > 0 {
		return model.VolumeStatusAttached
	}
	return model.VolumeStatusAvailable
}

func (a *VolumeAdmin) GetVolumesByInstanceID(ctx context.Context, instanceID int64) (volumes []*model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.GetVolumesByInstanceID: instanceID=%d", instanceID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.GetVolumesByInstanceID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.GetVolumesByInstanceID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	volumes = []*model.Volume{}
	if err = db.Preload("Instance").Preload("StoragePool").Where(query, args...).Where("instance_id = ?", instanceID).Find(&volumes).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query volumes, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query volumes", err)
		return
	}
	return
}

// list data volumes
func (a *VolumeAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, volumes []*model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.List: success")
		}
	}()
	return a.ListVolume(ctx, offset, limit, order, query, "all")
}

func (a *VolumeAdmin) ListVolume(ctx context.Context, offset, limit int64, order, query string, volume_type string) (total int64, volumes []*model.Volume, err error) {
	logger.Ctx(ctx).Infof("ENTER VolumeAdmin.ListVolume: offset=%d, limit=%d, order=%s, query=%s, volume_type=%s", offset, limit, order, query, volume_type)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT VolumeAdmin.ListVolume: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT VolumeAdmin.ListVolume: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	queryBuilder, args := memberShip.GetOrgFilter()
	booting_where := ""
	switch volume_type {
	case "data":
		booting_where = fmt.Sprintf("booting=%t", false)
	case "boot":
		booting_where = fmt.Sprintf("booting=%t", true)
	case "all":
		booting_where = ""
	default:
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid volume type %s", volume_type), nil)
		return
	}
	volumes = []*model.Volume{}
	if booting_where != "" {
		if err = db.Model(&model.Volume{}).Where(queryBuilder, args...).Scopes(dbs.Contains(query, "name")).Where(booting_where).Count(&total).Error; err != nil {
			err = NewCLError(ErrSQLSyntaxError, "Failed to count volumes", err)
			return
		}
	} else {
		if err = db.Model(&model.Volume{}).Where(queryBuilder, args...).Scopes(dbs.Contains(query, "name")).Count(&total).Error; err != nil {
			err = NewCLError(ErrSQLSyntaxError, "Failed to count volumes", err)
			return
		}
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	listQuery := db.Preload("Instance").Preload("StoragePool").Where(queryBuilder, args...).Scopes(dbs.Contains(query, "name"))
	// 类型条件必须同时作用于计数和取数：此前只加在计数上，type=data 返回 total=0 却列出全部系统盘
	if booting_where != "" {
		listQuery = listQuery.Where(booting_where)
	}
	if err = listQuery.Find(&volumes).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to query volumes", err)
		return
	}
	if memberShip.IsSystemAdmin() {
		_, db = GetContextDB(ctx) // 链式调用复用 Statement，取新会话查询 OwnerInfo
		for _, vol := range volumes {
			vol.OwnerInfo = &model.Organization{Model: model.Model{ID: vol.Owner}}
			if err = db.Take(vol.OwnerInfo).Error; err != nil {
				logger.Ctx(ctx).Error("Failed to query owner info", err)
				err = NewCLError(ErrOwnerNotFound, "Owner organization not found", err)
				return
			}
		}
	}

	return
}
