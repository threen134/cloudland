/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Volumes of shared pools (shared-storage-design.md §9.3, §9.4). Unlike a local volume, a shared volume is written
// when it is created, by any host that reaches the pool, and every host of the cluster can open it: volumes.hyper
// stays 0 and "written" means it left pending (§5.6). The node scripts are the same for every driver
// (create_volume_shared.sh, attach_volume_shared.sh, resize_volume_shared.sh, delete_volume_shared.sh): they get the
// pool and the volume as JSON on stdin and call the drv_* functions of the driver.

import (
	"context"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

const sharedScriptDir = "/opt/cloudland/scripts/backend"

// sharedVolumeCommand is a volume script of a shared pool with its arguments on stdin
func sharedVolumeCommand(script string, volume *model.Volume, head string, pool *model.StoragePool, extra map[string]interface{}) (string, error) {
	args, err := sharedVolumeArgs(pool, volume, extra)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s %s <<'EOF'\n%s\nEOF", sharedScriptDir, script, head, args), nil
}

// createShared admits and records a volume of a shared pool and has a host that reaches the pool write it. The
// volume is pending until the host confirms (create_volume_shared)
func (a *VolumeAdmin) createShared(ctx context.Context, name string, size int32, pool *model.StoragePool) (volume *model.Volume, err error) {
	var cmd *volumeCommand
	tx := dbs.DBContext(ctx).Begin()
	txCtx := SetContextDB(ctx, tx)
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		if err = tx.Commit().Error; err != nil {
			err = NewCLError(ErrVolumeCreationFailed, "Failed to create volume", err)
			return
		}
		if xerr := HyperExecute(ctx, cmd.control, cmd.command); xerr != nil {
			logger.Ctx(ctx).Errorf("Failed to send the creation of volume %d: %v", volume.ID, xerr)
			dbs.DBContext(ctx).Model(&model.Volume{}).Where("id = ? AND status = ?", volume.ID, model.VolumeStatusPending).
				Updates(map[string]interface{}{"status": model.VolumeStatusError, "reason": "the create command could not be sent to the host"})
			volume.Status = model.VolumeStatusError
		}
	}()
	if err = admitShared(tx, pool, int64(size), true); err != nil {
		return
	}
	host, err := pickPoolHost(tx, pool)
	if err != nil {
		return
	}
	if volume, err = a.CreateVolume(txCtx, name, size, 0, false, pool); err != nil {
		return
	}
	command, err := sharedVolumeCommand("create_volume_shared.sh", volume, fmt.Sprintf("'%d' '%s'", volume.ID, ShellEscape(volume.UUID)), pool, nil)
	if err != nil {
		return
	}
	cmd = &volumeCommand{control: fmt.Sprintf("inter=%d", host), command: command}
	return
}

// attachSharedCommand is the attachment of a shared volume to an instance: on the host of the instance, which must
// reach the pool now
func attachSharedCommand(tx *gorm.DB, pool *model.StoragePool, volume *model.Volume, instance *model.Instance) (*volumeCommand, error) {
	if _, err := poolUsableOn(tx, pool, instance.Hyper, false); err != nil {
		return nil, err
	}
	command, err := sharedVolumeCommand("attach_volume_shared.sh", volume,
		fmt.Sprintf("'%d' '%d' '%s'", instance.ID, volume.ID, ShellEscape(volume.UUID)), pool, nil)
	if err != nil {
		return nil, err
	}
	return &volumeCommand{control: fmt.Sprintf("inter=%d", instance.Hyper), command: command}, nil
}

// resizeSharedCommand grows a shared volume: on the host of its instance when it is attached (online when the
// instance runs there, the script tells), otherwise on any host that reaches the pool. The new size is admitted
// against the pool
func resizeSharedCommand(tx *gorm.DB, pool *model.StoragePool, volume *model.Volume, size, oldSize int32) (*volumeCommand, error) {
	if err := admitShared(tx, pool, int64(size-oldSize), true); err != nil {
		return nil, err
	}
	var host int32
	if volume.InstanceID > 0 {
		instance := &model.Instance{}
		if err := tx.Take(instance, volume.InstanceID).Error; err != nil {
			return nil, NewCLError(ErrInstanceNotFound, "Instance not found", err)
		}
		if _, err := poolUsableOn(tx, pool, instance.Hyper, false); err != nil {
			return nil, err
		}
		host = instance.Hyper
	} else {
		var err error
		if host, err = pickPoolHost(tx, pool); err != nil {
			return nil, err
		}
	}
	grown := *volume
	grown.Size = size
	command, err := sharedVolumeCommand("resize_volume_shared.sh", &grown, fmt.Sprintf("'%d' '%s'", volume.ID, ShellEscape(volume.UUID)), pool,
		map[string]interface{}{"old_gb": oldSize, "instance_id": volume.InstanceID})
	if err != nil {
		return nil, err
	}
	return &volumeCommand{control: fmt.Sprintf("inter=%d", host), command: command}, nil
}

// deleteSharedCommand removes the file of a shared volume on any host that reaches the pool; the record goes when
// the host confirms (clear_volume). Only the file of this volume is removed, never by pattern (§12.3)
func deleteSharedCommand(tx *gorm.DB, pool *model.StoragePool, volume *model.Volume) (*volumeCommand, error) {
	host, err := pickPoolHost(tx, pool)
	if err != nil {
		return nil, err
	}
	command, err := sharedVolumeCommand("delete_volume_shared.sh", volume, fmt.Sprintf("'%d' '%s'", volume.ID, ShellEscape(volume.UUID)), pool, nil)
	if err != nil {
		return nil, err
	}
	return &volumeCommand{control: fmt.Sprintf("inter=%d", host), command: command}, nil
}

// HandleSharedVolumeCreated finishes the creation of a shared volume (create_volume_shared)
func HandleSharedVolumeCreated(ctx context.Context, hostid int32, volumeID int64, result, reason string) error {
	db := dbs.DBContext(ctx)
	volume := &model.Volume{}
	if err := db.Where("id = ? AND status = ?", volumeID, model.VolumeStatusPending).Take(volume).Error; err != nil {
		// Repeated, or late after the volume was given up on
		return nil
	}
	// Sent to a host that reaches the pool: a report from a host outside it is not about this volume
	var member int64
	if err := db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", hostid, volume.StoragePoolID).Count(&member).Error; err != nil {
		return err
	}
	if member == 0 {
		return fmt.Errorf("host %d reported the creation of shared volume %d, but it has no row of its pool", hostid, volumeID)
	}
	if result == string(model.VolumeStatusAvailable) {
		return db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{"status": model.VolumeStatusAvailable, "reason": ""}).Error
	}
	logger.Ctx(ctx).Errorf("Creating shared volume %d failed on host %d: %s", volumeID, hostid, reason)
	return db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{"status": model.VolumeStatusError,
		"reason": truncate("create failed: "+reason, 512)}).Error
}

// HandleSharedVolumeAttached finishes the attachment of a shared volume (attach_volume_shared): unlike a local
// volume, the host that ran it does not become the host of the volume
func HandleSharedVolumeAttached(ctx context.Context, hostid int32, instanceID, volumeID int64, device, reason string) error {
	db := dbs.DBContext(ctx)
	volume := &model.Volume{}
	if err := db.Where("id = ? AND status = ?", volumeID, model.VolumeStatusAttaching).Take(volume).Error; err != nil {
		return nil
	}
	// The attach was sent to the host of the instance: only that host reports on it, and only for its instance, or
	// a host could have a volume recorded on an instance of its choosing
	instance := &model.Instance{}
	if err := db.Take(instance, instanceID).Error; err != nil {
		// Deleted meanwhile: the volume is attached to nothing
		logger.Ctx(ctx).Warningf("Shared volume %d was attaching to instance %d, which is gone", volumeID, instanceID)
		return db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{"status": model.VolumeStatusAvailable,
			"instance_id": 0, "target": "", "reason": "the instance is gone"}).Error
	}
	if instance.Hyper != hostid {
		return fmt.Errorf("host %d reported shared volume %d attached to instance %d, which is on host %d", hostid, volumeID, instanceID, instance.Hyper)
	}
	if device == "" || device == "-" {
		logger.Ctx(ctx).Errorf("Attaching shared volume %d failed on host %d: %s", volumeID, hostid, reason)
		return db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{"status": model.VolumeStatusAvailable,
			"instance_id": 0, "target": "", "reason": truncate(reason, 512)}).Error
	}
	return db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(map[string]interface{}{"status": model.VolumeStatusAttached,
		"instance_id": instanceID, "target": device, "reason": ""}).Error
}
