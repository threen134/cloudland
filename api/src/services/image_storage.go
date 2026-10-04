/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Base copies of images in shared pools (shared-storage-design.md §9.6). The first boot disk of an image in a shared
// pool waits for one host to import the image into the pool (import_image_shared.sh, a background job, so the serial
// command queue of the host is not held for minutes); the commands that clone boot disks from it wait meanwhile in
// image_storage_waiters and are sent once the copy is synced. A copy is removed when its image is deleted and no
// boot disk is cloned from it any more: a GPFS clone parent and an RBD snapshot with clones can not go before.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// An import that did not report back in this time is taken for failed: the host restarted or the job died
	imageStorageImportTimeout = 3 * time.Hour
	// A removal that did not report back is sent again after this time
	imageStorageDeleteTimeout = 30 * time.Minute
)

// prepareImageStorage returns the copy of an image in a shared pool, in the transaction of a request that clones a
// boot disk from it. A missing or failed copy is (re)imported: the command to send after the commit is returned. The
// row of the pool is locked first, so concurrent requests on one pool see one copy and one import
func prepareImageStorage(ctx context.Context, tx *gorm.DB, pool *model.StoragePool, image *model.Image) (is *model.ImageStorage, importCmd *volumeCommand, err error) {
	d, err := poolDriverOf(pool)
	if err != nil {
		return
	}
	locked := &model.StoragePool{}
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(locked, pool.ID).Error; err != nil {
		return nil, nil, NewCLError(ErrStoragePoolNotFound, "Storage pool not found", err)
	}
	if locked.Status != model.StoragePoolActive {
		return nil, nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Storage pool %s is %s", pool.Name, locked.Status), nil)
	}
	is = &model.ImageStorage{}
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("image_id = ? AND storage_pool_id = ?", image.ID, pool.ID).Take(is).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		is = &model.ImageStorage{ImageID: image.ID, StoragePoolID: pool.ID, Path: d.ImageBaseRef(pool, image), Status: model.ImageStorageSyncing}
		if err = tx.Create(is).Error; err != nil {
			return nil, nil, NewCLError(ErrSQLSyntaxError, "Failed to record the copy of the image in the pool", err)
		}
	case err != nil:
		return nil, nil, NewCLError(ErrSQLSyntaxError, "Failed to query the copy of the image in the pool", err)
	case is.Status == model.ImageStorageSynced:
		return is, nil, nil
	case is.Status == model.ImageStorageDeleting:
		return nil, nil, NewCLError(ErrImageNotAvailable, fmt.Sprintf("The copy of image %s in storage pool %s is being removed", image.Name, pool.Name), nil)
	case is.Status == model.ImageStorageSyncing && is.SentAt != nil && time.Since(*is.SentAt) < imageStorageImportTimeout:
		// Another request started the import: wait for it
		return is, nil, nil
	}
	// New, failed or given up: import it (again)
	if importCmd, err = imageStorageImportCommand(ctx, tx, is, pool, image); err != nil {
		return nil, nil, err
	}
	return is, importCmd, nil
}

// imageStorageImportCommand picks the host that imports a copy and builds its command; the copy is syncing with that
// host from now on, the only one whose report is taken
func imageStorageImportCommand(ctx context.Context, tx *gorm.DB, is *model.ImageStorage, pool *model.StoragePool, image *model.Image) (*volumeCommand, error) {
	host, err := pickPoolHost(tx, pool)
	if err != nil {
		return nil, err
	}
	url, err := BuildImageDownloadURLParam(ctx, image)
	if err != nil {
		return nil, err
	}
	// The file of the image in the cache of the host, named as launches name it
	imageName := image.FileBase() + "." + image.Format
	args, err := imageStorageArgs(pool, is, map[string]interface{}{
		"image_name": imageName, "image_url_b64": url, "clone_mode": pool.CloneMode})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if err = tx.Model(&model.ImageStorage{}).Where("id = ?", is.ID).Updates(map[string]interface{}{
		"status": model.ImageStorageSyncing, "reason": "", "hostid": host, "sent_at": &now}).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the import of the image", err)
	}
	is.Status, is.Hostid, is.SentAt = model.ImageStorageSyncing, host, &now
	return &volumeCommand{control: fmt.Sprintf("inter=%d", host),
		command: fmt.Sprintf("/opt/cloudland/scripts/backend/import_image_shared.sh '%d' <<'EOF'\n%s\nEOF", is.ID, args)}, nil
}

// imageStorageArgs is the JSON the base copy scripts read: the pool, the copy (base: its absolute path in a file
// pool, its image name in an RBD pool)
func imageStorageArgs(pool *model.StoragePool, is *model.ImageStorage, extra map[string]interface{}) (string, error) {
	d, err := poolDriverOf(pool)
	if err != nil {
		return "", err
	}
	args := d.DriverArgs(pool)
	args["image_storage_id"] = is.ID
	args["base"] = imageBaseLocation(pool, d, is)
	for k, v := range extra {
		args[k] = v
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", NewCLError(ErrJSONMarshalFailed, "Failed to encode the image copy arguments", err)
	}
	return string(b), nil
}

func imageBaseLocation(pool *model.StoragePool, d PoolDriver, is *model.ImageStorage) string {
	if d.Family() == PoolFamilyFile {
		return path.Join(pool.MountPath, is.Path)
	}
	return is.Path
}

// sendImageStorageImport sends an import after the commit that recorded it and its waiters; one that can not be sent
// fails the copy at once, and the commands waiting for it with it
func sendImageStorageImport(ctx context.Context, is *model.ImageStorage, cmd *volumeCommand) {
	if err := HyperExecute(ctx, cmd.control, cmd.command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to send the import of image copy %d: %v", is.ID, err)
		if herr := HandleImageStorageStatus(ctx, is.Hostid, is.ID, model.ImageStorageError, "the import command could not be sent: "+err.Error()); herr != nil {
			logger.Ctx(ctx).Errorf("Failed to give up image copy %d: %v", is.ID, herr)
		}
	}
}

// waitForImageStorage queues a command that clones a boot disk from a copy being imported
func waitForImageStorage(tx *gorm.DB, is *model.ImageStorage, kind string, instanceID, volumeID int64, priorSize int32, control, command string) error {
	w := &model.ImageStorageWaiter{ImageStorageID: is.ID, InstanceID: instanceID, VolumeID: volumeID, Kind: kind,
		PriorSize: priorSize, Control: control, Command: command}
	if err := tx.Create(w).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to queue the command until the image is copied into the pool", err)
	}
	return nil
}

// instanceWaitsForImage tells whether a command of an instance waits for the copy of its image (a reinstall queued
// until the import is done, shared-storage-design.md §9.8): it runs later on the host it was meant for, so nothing may
// change the instance in between, whatever its status says meanwhile
func instanceWaitsForImage(db *gorm.DB, instanceID int64) (bool, error) {
	var n int64
	err := db.Model(&model.ImageStorageWaiter{}).Where("instance_id = ?", instanceID).Count(&n).Error
	return n > 0, err
}

// InstanceWaitsForImage is instanceWaitsForImage for the callbacks; a failed query counts as waiting
func InstanceWaitsForImage(db *gorm.DB, instanceID int64) bool {
	waits, err := instanceWaitsForImage(db, instanceID)
	return waits || err != nil
}

// refuseWhileWaiting refuses an operation on an instance whose reinstall waits for the copy of its image
func refuseWhileWaiting(db *gorm.DB, instance *model.Instance) error {
	waits, err := instanceWaitsForImage(db, instance.ID)
	if err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to query the commands waiting for an image copy", err)
	}
	if waits {
		return NewCLError(ErrInstanceInvalidState, fmt.Sprintf("Instance %s waits for its image to be copied into the storage pool", instance.Hostname), nil)
	}
	return nil
}

// waiterStillValid checks, when the copy is there, that the command of a waiter still fits its instance: a launch
// for an instance still provisioning, a reinstall for one still reinstalling on the host the command goes to
func waiterStillValid(tx *gorm.DB, w *model.ImageStorageWaiter) (string, bool) {
	inst := &model.Instance{}
	if err := tx.Take(inst, w.InstanceID).Error; err != nil {
		return "the instance is gone", false
	}
	if w.Kind == model.ImageWaitLaunch {
		return "the instance is " + string(inst.Status), inst.Status == model.InstanceStatusProvisioning
	}
	if inst.Status != model.InstanceStatusReinstalling || fmt.Sprintf("inter=%d", inst.Hyper) != w.Control {
		return fmt.Sprintf("the instance changed while its reinstall waited (%s on host %d)", inst.Status, inst.Hyper), false
	}
	return "", true
}

// giveUpReinstall leaves a reinstall that will not run as one that failed on its host: the instance is in error (its
// record already names the new image, flavor and password; reinstalling again is the way out), its disk was not
// touched, so the disk and its instance keep the old size
func giveUpReinstall(db *gorm.DB, w *model.ImageStorageWaiter, msg string) error {
	inst := map[string]interface{}{"status": "error", "reason": msg}
	vol := map[string]interface{}{"status": model.VolumeStatusAttached, "reason": msg}
	if w.PriorSize > 0 {
		vol["size"] = w.PriorSize
		if err := db.Model(&model.Instance{}).Where("id = ?", w.InstanceID).Update("disk", w.PriorSize).Error; err != nil {
			return err
		}
	}
	if err := db.Model(&model.Instance{}).Where("id = ? AND status = ?", w.InstanceID, model.InstanceStatusReinstalling).Updates(inst).Error; err != nil {
		return err
	}
	return db.Model(&model.Volume{}).Where("id = ? AND status = ?", w.VolumeID, model.VolumeStatus("reinstalling")).Updates(vol).Error
}

// giveUpLaunch fails a new instance whose launch will not run, as long as it is still being created
func giveUpLaunch(db *gorm.DB, w *model.ImageStorageWaiter, msg string) error {
	if err := db.Model(&model.Instance{}).Where("id = ? AND status = ?", w.InstanceID, model.InstanceStatusProvisioning).
		Updates(map[string]interface{}{"status": "error", "reason": msg}).Error; err != nil {
		return err
	}
	return db.Model(&model.Volume{}).Where("id = ? AND status = ?", w.VolumeID, model.VolumeStatusPending).
		Updates(map[string]interface{}{"status": model.VolumeStatusError, "reason": msg}).Error
}

// markImageStorageMissing fails a synced copy a host did not find in its pool (removed out of band, a restore): the
// next boot disk made from it imports it again instead of failing the same way
func markImageStorageMissing(tx *gorm.DB, id, poolID int64, reason string) error {
	return tx.Model(&model.ImageStorage{}).Where("id = ? AND storage_pool_id = ? AND status = ?", id, poolID, model.ImageStorageSynced).
		Updates(map[string]interface{}{"status": model.ImageStorageError, "reason": truncate("not found in the pool: "+reason, 512)}).Error
}

// HandleImageStorageStatus takes the report of the host an import or a removal was sent to:
// synced / error for an import, deleted / delete_failed for a removal
func HandleImageStorageStatus(ctx context.Context, hostid int32, id int64, status, reason string) (err error) {
	return handleImageStorageStatus(ctx, hostid, id, status, reason, nil)
}

// handleImageStorageStatus with staleBefore is the maintenance giving up an import: skipped when the import was sent
// again since (a creation found it stale and imported it anew)
func handleImageStorageStatus(ctx context.Context, hostid int32, id int64, status, reason string, staleBefore *time.Time) (err error) {
	db := dbs.DBContext(ctx)
	var send []*model.ImageStorageWaiter
	var failed []*model.ImageStorageWaiter
	err = db.Transaction(func(tx *gorm.DB) error {
		is := &model.ImageStorage{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(is, id).Error; err != nil {
			// Removed meanwhile: a late or repeated report
			return nil
		}
		if is.Hostid != hostid {
			return fmt.Errorf("host %d reported on image copy %d, which was sent to host %d", hostid, id, is.Hostid)
		}
		switch status {
		case model.ImageStorageSynced, model.ImageStorageError:
			if is.Status != model.ImageStorageSyncing {
				return nil
			}
			if staleBefore != nil && is.SentAt != nil && is.SentAt.After(*staleBefore) {
				return nil
			}
			if err := tx.Model(is).Updates(map[string]interface{}{"status": status, "reason": truncate(reason, 512)}).Error; err != nil {
				return err
			}
			waiters := []*model.ImageStorageWaiter{}
			if err := tx.Where("image_storage_id = ?", is.ID).Order("id").Find(&waiters).Error; err != nil {
				return err
			}
			if len(waiters) > 0 {
				// For good: the commands carry the passwords and keys of the instances
				if err := tx.Unscoped().Where("image_storage_id = ?", is.ID).Delete(&model.ImageStorageWaiter{}).Error; err != nil {
					return err
				}
			}
			if status == model.ImageStorageSynced {
				for _, w := range waiters {
					if why, ok := waiterStillValid(tx, w); !ok {
						logger.Ctx(ctx).Warningf("The %s command of instance %d is dropped: %s", w.Kind, w.InstanceID, why)
						if w.Kind == model.ImageWaitReinstall {
							if err := giveUpReinstall(tx, w, truncate("The reinstall was given up: "+why, 512)); err != nil {
								return err
							}
						}
						continue
					}
					send = append(send, w)
				}
				return nil
			}
			failed = waiters
			return failImageStorageWaiters(tx, is, waiters, reason)
		case "deleted":
			if is.Status != model.ImageStorageDeleting {
				return nil
			}
			return tx.Delete(is).Error
		case "delete_failed":
			// Kept deleting: the maintenance sends the removal again
			return tx.Model(is).Update("reason", truncate("removal failed: "+reason, 512)).Error
		}
		return fmt.Errorf("unknown image copy status %q", status)
	})
	if err != nil {
		return
	}
	for _, w := range failed {
		logger.Ctx(ctx).Warningf("Instance %d gave up: the image copy %d failed: %s", w.InstanceID, id, reason)
	}
	sendImageStorageWaiters(ctx, send)
	return nil
}

// failImageStorageWaiters gives up the commands of a copy that could not be imported: a new instance fails, a
// reinstall fails like one that failed on its host (giveUpReinstall)
func failImageStorageWaiters(tx *gorm.DB, is *model.ImageStorage, waiters []*model.ImageStorageWaiter, reason string) error {
	msg := truncate("Copying the image into the storage pool failed: "+reason, 512)
	for _, w := range waiters {
		var err error
		if w.Kind == model.ImageWaitReinstall {
			err = giveUpReinstall(tx, w, msg)
		} else {
			err = giveUpLaunch(tx, w, msg)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// sendImageStorageWaiters sends the commands that waited for a copy, after the commit that took them off the queue.
// A launch that can not be sent fails its instance like any launch that could not be sent (launchNotSent); a reinstall
// leaves the disk as it was, like a failed import does
func sendImageStorageWaiters(ctx context.Context, waiters []*model.ImageStorageWaiter) {
	for _, w := range waiters {
		err := HyperExecute(ctx, w.Control, w.Command)
		if err == nil {
			continue
		}
		logger.Ctx(ctx).Errorf("Failed to send the %s command of instance %d after its image was copied: %v", w.Kind, w.InstanceID, err)
		db := dbs.DBContext(ctx)
		if w.Kind == model.ImageWaitLaunch {
			instanceAdmin.launchNotSent(ctx, &ExecutionCommand{InstanceID: w.InstanceID, BootVolumeID: w.VolumeID}, err)
			db.Model(&model.Volume{}).Where("id = ? AND status = ?", w.VolumeID, model.VolumeStatusPending).Update("status", model.VolumeStatusError)
			continue
		}
		if gerr := giveUpReinstall(db, w, truncate("The command could not be sent to the hosts: "+err.Error(), 512)); gerr != nil {
			logger.Ctx(ctx).Errorf("Failed to give up the reinstall of instance %d: %v", w.InstanceID, gerr)
		}
	}
}

// imageStorageRefs counts the live boot disks cloned from a copy
func imageStorageRefs(tx *gorm.DB, id int64) (int64, error) {
	var n int64
	err := tx.Model(&model.Volume{}).Where("base_image_storage_id = ?", id).Count(&n).Error
	return n, err
}

// releaseImageStorage removes a copy whose image is deleted once no boot disk is cloned from it any more. Called after
// a boot disk that referenced it is deleted or reinstalled from another image; the command is sent at once (a lost
// one is sent again by the maintenance, which passes staleBefore: a copy another clapi handled since is skipped)
func releaseImageStorage(ctx context.Context, id int64) {
	releaseImageStorageIf(ctx, id, nil)
}

func releaseImageStorageIf(ctx context.Context, id int64, staleBefore *time.Time) {
	if id <= 0 {
		return
	}
	db := dbs.DBContext(ctx)
	var cmd *volumeCommand
	err := db.Transaction(func(tx *gorm.DB) error {
		is := &model.ImageStorage{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(is, id).Error; err != nil {
			return nil
		}
		if is.Status != model.ImageStorageDeleting {
			return nil
		}
		if staleBefore != nil && is.SentAt != nil && is.SentAt.After(*staleBefore) {
			return nil
		}
		var err error
		cmd, err = imageStorageDeleteCommand(tx, is)
		return err
	})
	if err != nil {
		logger.Ctx(ctx).Warningf("Image copy %d is not removed yet: %v", id, err)
		return
	}
	sendVolumeCommand(ctx, cmd)
}

// imageStorageDeleteCommand builds the removal of a copy nothing is cloned from any more, nil while there is one.
// While clones remain, the next look of the maintenance is put off (sent_at) instead of every minute
func imageStorageDeleteCommand(tx *gorm.DB, is *model.ImageStorage) (*volumeCommand, error) {
	refs, err := imageStorageRefs(tx, is.ID)
	if err != nil {
		return nil, err
	}
	if refs > 0 {
		now := time.Now()
		return nil, tx.Model(&model.ImageStorage{}).Where("id = ?", is.ID).Update("sent_at", &now).Error
	}
	pool := &model.StoragePool{}
	if err := tx.Take(pool, is.StoragePoolID).Error; err != nil {
		// The pool went first: its copies went with it
		return nil, tx.Delete(is).Error
	}
	host, err := pickPoolHost(tx, pool)
	if err != nil {
		return nil, err
	}
	args, err := imageStorageArgs(pool, is, nil)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if err := tx.Model(&model.ImageStorage{}).Where("id = ?", is.ID).Updates(map[string]interface{}{"hostid": host, "sent_at": &now}).Error; err != nil {
		return nil, err
	}
	return &volumeCommand{control: fmt.Sprintf("inter=%d", host),
		command: fmt.Sprintf("/opt/cloudland/scripts/backend/delete_image_shared.sh '%d' <<'EOF'\n%s\nEOF", is.ID, args)}, nil
}

func sendVolumeCommand(ctx context.Context, cmd *volumeCommand) {
	if cmd == nil {
		return
	}
	if err := HyperExecute(ctx, cmd.control, cmd.command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to send %s: %v", cmd.control, err)
	}
}

// deleteImageStorages marks the copies of an image being deleted and returns the removals that can go at once. An
// import still running refuses the deletion: removed meanwhile, its copy would be put in place after the removal and
// stay behind with no record (no command waits for it then: an image instances use is not deleted)
func deleteImageStorages(tx *gorm.DB, image *model.Image) (cmds []*volumeCommand, err error) {
	copies := []*model.ImageStorage{}
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("image_id = ?", image.ID).Find(&copies).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the copies of the image", err)
	}
	for _, is := range copies {
		// One past its time is given up by the maintenance in a minute anyway
		if is.Status == model.ImageStorageSyncing && is.SentAt != nil && time.Since(*is.SentAt) < imageStorageImportTimeout {
			pool := &model.StoragePool{}
			tx.Unscoped().Take(pool, is.StoragePoolID)
			return nil, NewCLError(ErrImageInUse, fmt.Sprintf("Image %s is being copied into storage pool %s, delete it once the copy is done", image.Name, pool.Name), nil)
		}
	}
	for _, is := range copies {
		if err = tx.Model(is).Updates(map[string]interface{}{"status": model.ImageStorageDeleting, "reason": ""}).Error; err != nil {
			return nil, NewCLError(ErrSQLSyntaxError, "Failed to mark the copies of the image", err)
		}
		is.Status = model.ImageStorageDeleting
		cmd, cerr := imageStorageDeleteCommand(tx, is)
		if cerr != nil {
			// No host reaches the pool now: the maintenance tries again
			continue
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return
}

// maintainImageStorages applies the time-based rules of the copies: an import that never reported back fails its
// waiters (the next use imports again), a removal that never reported back is sent again
func maintainImageStorages(ctx context.Context) {
	db := dbs.DBContext(ctx)
	stale := []*model.ImageStorage{}
	importBefore := time.Now().Add(-imageStorageImportTimeout)
	db.Where("status = ? AND (sent_at IS NULL OR sent_at < ?)", model.ImageStorageSyncing, importBefore).Find(&stale)
	for _, is := range stale {
		if err := handleImageStorageStatus(ctx, is.Hostid, is.ID, model.ImageStorageError, fmt.Sprintf("the import did not report back in %s", imageStorageImportTimeout), &importBefore); err != nil {
			logger.Ctx(ctx).Warningf("Failed to give up image copy %d: %v", is.ID, err)
		}
	}
	before := time.Now().Add(-imageStorageDeleteTimeout)
	deleting := []*model.ImageStorage{}
	db.Where("status = ? AND (sent_at IS NULL OR sent_at < ?)", model.ImageStorageDeleting, before).Find(&deleting)
	for _, is := range deleting {
		releaseImageStorageIf(ctx, is.ID, &before)
	}
}

// metadataWithBootDisk adds the boot disk of a shared pool to the metadata a launch or a reinstall sends
func metadataWithBootDisk(metadata string, bootDisk map[string]interface{}) (string, error) {
	md := map[string]interface{}{}
	if err := json.Unmarshal([]byte(metadata), &md); err != nil {
		return "", NewCLError(ErrInvalidMetadata, "Failed to read the instance metadata", err)
	}
	md["boot_disk"] = bootDisk
	b, err := json.Marshal(md)
	if err != nil {
		return "", NewCLError(ErrJSONMarshalFailed, "Failed to encode the instance metadata", err)
	}
	return string(b), nil
}

// sharedBootDisk is what launch_vm.sh and reinstall_vm.sh get about a boot disk in a shared pool (§9.7): the pool,
// the disk, the copy to clone it from, where the UEFI variables go
func sharedBootDisk(pool *model.StoragePool, volume *model.Volume, instanceID int64, is *model.ImageStorage) (map[string]interface{}, error) {
	d, err := poolDriverOf(pool)
	if err != nil {
		return nil, err
	}
	bd := sharedVolumeArgMap(pool, d, volume)
	bd["image_storage_id"] = is.ID
	bd["image_base"] = imageBaseLocation(pool, d, is)
	bd["clone_mode"] = pool.CloneMode
	if d.Family() == PoolFamilyFile {
		bd["nvram"] = path.Join(pool.MountPath, "nvram", fmt.Sprintf("inst-%d_VARS.fd", instanceID))
	}
	return bd, nil
}

// BootDiskSource is the boot disk of an instance as QEMU tools read it on its host (capture, rescue): the file of a
// local or file pool, the rbd: address of an RBD image
func BootDiskSource(pool *model.StoragePool, volume *model.Volume) string {
	if pool.Shared() {
		if d, err := poolDriverOf(pool); err == nil {
			return d.QemuSource(pool, volume)
		}
	}
	return VolumeAbsPath(pool, volume)
}
