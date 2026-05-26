/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

)

var (
	backupAdmin = &BackupAdmin{}
)

type BackupAdmin struct{}

// volume backup and snapshot functions
// backup volume to another pool, this is an async operation and will return the task ID
// if the poolID is empty, the backup will be done to the same pool with snapshot
func (a *BackupAdmin) CreateBackupByID(ctx context.Context, volumeID int64, poolID string, name string) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.CreateBackupByID: volumeID=%d, poolID=%s, name=%s", volumeID, poolID, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.CreateBackupByID: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.CreateBackupByID: success, backupID=%d", backup.ID)
		}
	}()
	volume, err := volumeAdmin.Get(ctx, volumeID)
	if err != nil {
		logger.Error("Failed to get volume", err)
		return
	}
	// check the permission
	return a.createBackup(ctx, volume, poolID, name)
}

func (a *BackupAdmin) CreateBackupByUUID(ctx context.Context, uuid string, poolID string, name string) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.CreateBackupByUUID: uuid=%s, poolID=%s, name=%s", uuid, poolID, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.CreateBackupByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.CreateBackupByUUID: success, backupID=%d", backup.ID)
		}
	}()
	volume, err := volumeAdmin.GetVolumeByUUID(ctx, uuid)
	if err != nil {
		logger.Error("Failed to get volume", err)
		return
	}
	// check the permission
	return a.createBackup(ctx, volume, poolID, name)
}

func (a *BackupAdmin) createBackup(ctx context.Context, volume *model.Volume, poolID string, name string) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.createBackup: volumeID=%d, poolID=%s, name=%s", volume.ID, poolID, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.createBackup: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.createBackup: success, backupID=%d", backup.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, volume.Owner)
	if !permit {
		logger.Errorf("Not authorized to backup volume(%s)", volume.UUID)
		err = NewCLError(ErrPermissionDenied, "Not authorized to backup the volume", nil)
		return
	}
	if volume.Status == model.VolumeStatusError {
		logger.Errorf("Volume %s is in error state, cannot backup now", volume.UUID)
		err = NewCLError(ErrVolumeInvalidState, fmt.Sprintf("Volume %s is in error state, cannot backup now", volume.UUID), nil)
		return
	}
	if volume.IsBusy() {
		msg := fmt.Sprintf("Volume %s is in %s state, cannot backup now", volume.UUID, volume.Status)
		logger.Errorf(msg)
		err = NewCLError(ErrVolumeIsBusy, msg, nil)
		return
	}
	// check the pool id is valid
	_, err = (&DictionaryAdminService{}).Find(ctx, model.DICT_CATEGORY_STORAGE_POOL, poolID)
	if err != nil {
		logger.Error("DB: query dictionary failed, storage_pool(%s) not found %+v", poolID, err)
		err = NewCLError(ErrDictionaryRecordsNotFound, fmt.Sprintf("Storage pool (%s) not found", poolID), err)
		return
	}

	backup, task, err := a.createBackupModel(ctx, name, "backup", volume, "")
	if err != nil {
		logger.Errorf("Failed to create backup record for volume(%s), %+v", volume.UUID, err)
		err = NewCLError(ErrDatabaseError, "Failed to create backup record", err)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	err = db.Model(&volume).Updates(map[string]interface{}{"status": model.VolumeStatusBackuping}).Error
	if err != nil {
		logger.Error("Update volume status failed", err)
		err = NewCLError(ErrDatabaseError, "Failed to update volume status", err)
		return
	}
	control := "inter="
	if volume.InstanceID > 0 {
		instance := volume.Instance
		if instance == nil {
			instance = &model.Instance{Model: model.Model{ID: volume.InstanceID}}
			if err = db.Model(instance).Take(instance).Error; err != nil {
				logger.Error("DB: query instance failed", err)
				err = NewCLError(ErrInstanceNotFound, "Instance not found", err)
				return
			}
		}
		control = fmt.Sprintf("inter=%d", instance.Hyper)
	}
	vol_driver := GetVolumeDriver()
	if vol_driver != "local" {
		wdsUUID := volume.GetOriginVolumeID()
		wdsOriginPoolID := volume.GetVolumePoolID()
		if poolID != "" && poolID != wdsOriginPoolID {
			logger.Debugf("Backup volume %s from pool %s to pool %s", volume.UUID, wdsOriginPoolID, poolID)
			command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_snapshot_%s.sh '%d' '%d' '%s' '%s' '%d' '%s' '%s' '%s'", vol_driver, task.ID, backup.ID, backup.UUID, backup.Name, volume.ID, wdsUUID, wdsOriginPoolID, poolID)
			err = HyperExecute(ctx, control, command)
			if err != nil {
				logger.Error("Backup volume execution failed", err)
				return
			}
			return
		} else {
			logger.Debugf("Backup volume %s to same pool %s, use snapshot", volume.UUID, poolID)
			command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_snapshot_%s.sh '%d' '%d' '%s' '%s' '%d' '%s' '%s'", vol_driver, task.ID, backup.ID, backup.UUID, backup.Name, volume.ID, wdsUUID, wdsOriginPoolID)
			err = HyperExecute(ctx, control, command)
			if err != nil {
				logger.Error("Backup volume execution failed", err)
				return
			}
		}
	} else {
		logger.Error("Backup not supported for local volume")
		err = fmt.Errorf("Backup not supported for local volume")
		return
	}
	return
}

// snapshot volume, this is an async operation and will return the task ID
func (a *BackupAdmin) CreateSnapshotByID(ctx context.Context, volumeID int64, name string) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.CreateSnapshotByID: volumeID=%d, name=%s", volumeID, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.CreateSnapshotByID: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.CreateSnapshotByID: success, snapshotID=%d", backup.ID)
		}
	}()
	volume, err := volumeAdmin.Get(ctx, volumeID)
	if err != nil {
		logger.Error("Failed to get volume", err)
		return
	}
	return a.createSnapshot(ctx, name, volume)
}

func (a *BackupAdmin) CreateSnapshotByUUID(ctx context.Context, uuid, name string) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.CreateSnapshotByUUID: uuid=%s, name=%s", uuid, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.CreateSnapshotByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.CreateSnapshotByUUID: success, snapshotID=%d", backup.ID)
		}
	}()
	volume, err := volumeAdmin.GetVolumeByUUID(ctx, uuid)
	if err != nil {
		logger.Error("Failed to get volume", err)
		return
	}
	return a.createSnapshot(ctx, name, volume)
}

func (a *BackupAdmin) createSnapshot(ctx context.Context, name string, volume *model.Volume) (snapshot *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.createSnapshot: name=%s, volumeID=%d", name, volume.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.createSnapshot: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.createSnapshot: success, snapshotID=%d", snapshot.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, volume.Owner)
	if !permit {
		logger.Error("Not authorized to snapshot volume")
		err = NewCLError(ErrPermissionDenied, "Not authorized to snapshot the volume", nil)
		return
	}
	if volume.Status == model.VolumeStatusError {
		logger.Errorf("Volume %s is in error state, cannot snapshot now", volume.UUID)
		err = NewCLError(ErrVolumeInvalidState, fmt.Sprintf("Volume %s is in error state, cannot snapshot now", volume.UUID), nil)
		return
	}
	if volume.IsBusy() {
		msg := fmt.Sprintf("Volume %s is in %s state, cannot snapshot now", volume.UUID, volume.Status)
		logger.Errorf(msg)
		err = NewCLError(ErrVolumeIsBusy, msg, nil)
		return
	}
	snapshot, task, err := a.createBackupModel(ctx, name, "snapshot", volume, "")
	if err != nil {
		logger.Error("Failed to create snapshot", err)
		err = NewCLError(ErrDatabaseError, "Failed to create snapshot record", err)
		return
	}
	control := "inter="
	vol_driver := GetVolumeDriver()
	uuid := volume.UUID
	logger.Debugf("creating snapshot (%s) for volume %s", snapshot.UUID, uuid)
	if vol_driver != "local" {
		wdsUUID := volume.GetOriginVolumeID()
		wdsOriginPoolID := volume.GetVolumePoolID()
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_snapshot_%s.sh '%d' '%d' '%s' '%s' '%d' '%s' '%s'", vol_driver, task.ID, snapshot.ID, snapshot.UUID, snapshot.Name, volume.ID, wdsUUID, wdsOriginPoolID)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Backup volume execution failed", err)
			return
		}
	} else {
		logger.Error("Snapshot not supported for local volume")
		err = fmt.Errorf("Snapshot not supported for local volume")
		return
	}

	return
}

func (a *BackupAdmin) createBackupModel(ctx context.Context, name, backupType string, volume *model.Volume, poolID string) (backup *model.VolumeBackup, task *model.Task, err error) {
	logger.Infof("ENTER BackupAdmin.createBackupModel: name=%s, backupType=%s, volumeID=%d, poolID=%s", name, backupType, volume.ID, poolID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.createBackupModel: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.createBackupModel: success, backupID=%d, taskID=%d", backup.ID, task.ID)
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	backup = &model.VolumeBackup{
		Owner:      memberShip.OrgID,
		Name:       name,
		VolumeID:   volume.ID,
		BackupType: backupType,
		Status:     "pending",
	}
	err = db.Create(backup).Error
	if err != nil {
		logger.Error("DB failed to create backup", err)
		return
	}
	backup.Volume = volume
	task = &model.Task{
		Owner:     memberShip.OrgID,
		Name:      fmt.Sprintf("create_%s_%s", backupType, volume.UUID),
		Summary:   fmt.Sprintf("Taking %s(%s [%d]) for volume %s to pool %s", backupType, name, backup.ID, volume.UUID, poolID),
		Status:    model.TaskStatusRunning,
		Source:    model.TaskSourceManual,
		Action:    model.TaskActionBackup,
		Resources: fmt.Sprintf(`["%d"]`, backup.ID),
	}
	err = db.Create(task).Error
	if err != nil {
		logger.Error("DB failed to create task", err)
		return
	}
	backup.TaskID = task.ID
	backup.Task = task
	err = db.Model(backup).Updates(map[string]interface{}{"task_id": task.ID}).Error
	if err != nil {
		logger.Error("DB failed to update task id to backup", err)
		return
	}
	return
}

func (a *BackupAdmin) GetBackupByID(ctx context.Context, backupID int64) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.GetBackupByID: backupID=%d", backupID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.GetBackupByID: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.GetBackupByID: success, backupUUID=%s", backup.UUID)
		}
	}()
	if backupID <= 0 {
		err_msg := fmt.Sprintf("Invalid backup ID: %d", backupID)
		logger.Error(err_msg)
		err = NewCLError(ErrInvalidParameter, err_msg, nil)
		return
	}
	db := DB()
	memberShip := GetMemberShip(ctx)
	backup = &model.VolumeBackup{Model: model.Model{ID: backupID}}
	if err = db.Preload("Volume").Take(backup).Error; err != nil {
		logger.Errorf("Failed to query backup/snapshot, %v", err)
		err = NewCLError(ErrBackupNotFound, "Backup/Snapshot not found", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, backup.Owner)
	if !permit {
		logger.Error("Not authorized to read the backup")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the backup", nil)
		return
	}
	return
}

func (a *BackupAdmin) GetBackupByUUID(ctx context.Context, uuID string) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.GetBackupByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.GetBackupByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.GetBackupByUUID: success, backupID=%d", backup.ID)
		}
	}()
	db := DB()
	memberShip := GetMemberShip(ctx)
	backup = &model.VolumeBackup{}
	query, args := memberShip.GetOrgFilter()
	err = db.Preload("Volume").Where(query, args...).Where("uuid = ?", uuID).Take(backup).Error
	if err != nil {
		logger.Error("DB: query backup failed", err)
		err = NewCLError(ErrVolumeNotFound, "Volume not found", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, backup.Owner)
	if !permit {
		logger.Error("Not authorized to read the backup")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the backup", nil)
		return
	}
	return
}

func (a *BackupAdmin) Delete(ctx context.Context, backup *model.VolumeBackup) (err error) {
	logger.Infof("ENTER BackupAdmin.Delete: backupID=%d, backupUUID=%s", backup.ID, backup.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT BackupAdmin.Delete: success")
		}
	}()
	if !backup.CanDelete() {
		msg := fmt.Sprintf("Backup %s is in %s state, cannot be deleted now", backup.UUID, backup.Status)
		logger.Errorf(msg)
		err = NewCLError(ErrBackupInvalidState, msg, nil)
		return
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, backup.Owner)
	if !permit {
		logger.Error("Not authorized to delete the backup")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the backup", nil)
		return
	}
	err = db.Delete(backup).Error
	if err != nil {
		logger.Error("DB: delete backup failed", err)
		err = NewCLError(ErrDatabaseError, "Failed to delete the backup record", err)
		return
	}
	vol_driver := GetVolumeDriver()
	control := "inter="
	wdsUUID := backup.GetOriginBackupID()
	if wdsUUID != "" {
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/delete_snapshot_%s.sh '%s'", vol_driver, wdsUUID)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Delete snapshot execution failed", err)
			return
		}
	}
	return
}

func (a *BackupAdmin) DeleteByID(ctx context.Context, backupID int64) (err error) {
	logger.Infof("ENTER BackupAdmin.DeleteByID: backupID=%d", backupID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.DeleteByID: error=%v", err)
		} else {
			logger.Info("EXIT BackupAdmin.DeleteByID: success")
		}
	}()
	backup, err := a.GetBackupByID(ctx, backupID)
	if err != nil {
		logger.Error("Failed to get backup", err)
		err = NewCLError(ErrBackupNotFound, "Backup/Snapshot not found", err)
		return
	}
	return a.Delete(ctx, backup)
}

func (a *BackupAdmin) DeleteByUUID(ctx context.Context, uuID string) (err error) {
	logger.Infof("ENTER BackupAdmin.DeleteByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.DeleteByUUID: error=%v", err)
		} else {
			logger.Info("EXIT BackupAdmin.DeleteByUUID: success")
		}
	}()
	backup, err := a.GetBackupByUUID(ctx, uuID)
	if err != nil {
		logger.Error("Failed to get backup", err)
		err = NewCLError(ErrBackupNotFound, "Backup/Snapshot not found", err)
		return
	}
	return a.Delete(ctx, backup)
}

func (a *BackupAdmin) Restore(ctx context.Context, backupID int64) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.Restore: backupID=%d", backupID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.Restore: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.Restore: success, taskID=%d", backup.TaskID)
		}
	}()
	backup, err = a.GetBackupByID(ctx, backupID)
	if err != nil {
		logger.Error("Failed to get backup", err)
		return
	}
	if !backup.CanRestore() {
		msg := fmt.Sprintf("Backup %s is in %s state, cannot restore now", backup.UUID, backup.Status)
		logger.Errorf(msg)
		err = NewCLError(ErrCannotRestoreFromBackup, msg, nil)
		return
	}
	volume, err := volumeAdmin.Get(ctx, backup.VolumeID)
	if err != nil {
		logger.Error("Failed to get volume", err)
		return
	}
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, volume.Owner)
	if !permit {
		logger.Errorf("Not authorized to restore volume(%s)", volume.UUID)
		err = NewCLError(ErrPermissionDenied, "Not authorized to restore the volume", nil)
		return
	}
	// check if the instance is running, if so, ask the user to stop it first
	if volume.InstanceID > 0 && volume.Instance.Status != "shut_off" {
		msg := fmt.Sprintf("Volume %s is attached to a running instance, please stop the instance %s first", volume.Name, volume.Instance.Hostname)
		logger.Errorf(msg)
		err = NewCLError(ErrCannotRestoreWhileInstanceIsRunning, msg, nil)
		return
	}
	// update volume status to restoring
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	err = db.Model(&volume).Updates(map[string]interface{}{"status": model.VolumeStatusRestoring}).Error
	if err != nil {
		logger.Error("Update volume status failed", err)
		err = NewCLError(ErrDatabaseError, "Failed to update volume status", err)
		return
	}
	// create restore task
	task := &model.Task{
		Owner:     memberShip.OrgID,
		Name:      fmt.Sprintf("Restoring %s(%s) for volume %s", backup.BackupType, backup.Name, volume.UUID),
		Summary:   fmt.Sprintf("Restoring %s(%s [%d]) for volume %s", backup.BackupType, backup.Name, backup.ID, volume.UUID),
		Status:    model.TaskStatusRunning,
		Source:    model.TaskSourceManual,
		Action:    model.TaskActionRestore,
		Resources: fmt.Sprintf(`["%d"]`, backup.ID),
	}
	err = db.Create(task).Error
	if err != nil {
		logger.Error("DB failed to create restore task", err)
		return
	}
	// update backup
	err = db.Model(&backup).Updates(map[string]interface{}{"task_id": task.ID}).Error
	if err != nil {
		logger.Error("DB failed to update backup task", err)
		return
	}
	control := "inter="
	if volume.InstanceID > 0 {
		control = fmt.Sprintf("inter=%d", volume.Instance.Hyper)
	}
	vol_driver := volume.GetVolumeDriver()
	if vol_driver != "local" {
		volume_wds_uuid := volume.GetOriginVolumeID()
		volume_pool_id := volume.GetVolumePoolID()
		snapshot_wds_uuid := backup.GetOriginBackupID()
		if backup.SnapshotID != "" {
			snapshot_wds_uuid = backup.SnapshotID
		}
		// <task_id> <backup_id> <volume_id> <instance_id> <volume_wds_uuid> <snapshot_wds_uuid> <volume_pool_id>
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/restore_snapshot_%s.sh '%d' '%d' '%d' '%d' '%s' '%s' '%s'", vol_driver, task.ID, backupID, volume.ID, volume.InstanceID, volume_wds_uuid, snapshot_wds_uuid, volume_pool_id)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Restore volume execution failed", err)
			return
		}
	} else {
		logger.Error("Restore not supported for local volume")
		err = fmt.Errorf("Restore not supported for local volume")
		return
	}
	backup.TaskID = task.ID
	backup.Task = task
	return
}

func (a *BackupAdmin) List(ctx context.Context, offset, limit int64, order, query string, volumeID int64, backupType string) (total int64, backups []*model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.List: offset=%d, limit=%d, order=%s, query=%s, volumeID=%d, backupType=%s", offset, limit, order, query, volumeID, backupType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT BackupAdmin.List: total=%d, count=%d", total, len(backups))
		}
	}()
	memberShip := GetMemberShip(ctx)
	db := DB()
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}
	if query != "" {
		query = fmt.Sprintf("name like '%%%s%%'", query)
	}
	query, args := memberShip.GetOrgFilter()
	whereSQL := ""
	if volumeID > 0 {
		whereSQL = fmt.Sprintf("volume_id = %d", volumeID)
	}
	if backupType != "" {
		if whereSQL != "" {
			whereSQL = fmt.Sprintf("%s and backup_type = '%s'", whereSQL, backupType)
		} else {
			whereSQL = fmt.Sprintf("backup_type = '%s'", backupType)
		}
	}
	if query != "" {
		if whereSQL != "" {
			whereSQL = fmt.Sprintf("%s and %s", whereSQL, query)
		} else {
			whereSQL = query
		}
	}
	if whereSQL != "" {
		if err = db.Model(&model.VolumeBackup{}).Where(query, args...).Where(whereSQL).Count(&total).Error; err != nil {
			logger.Error("DB: query backup count failed", err)
			err = NewCLError(ErrSQLSyntaxError, "Failed to query backup count", err)
			return
		}
	} else {
		if err = db.Model(&model.VolumeBackup{}).Where(query, args...).Count(&total).Error; err != nil {
			logger.Error("DB: query backup count failed", err)
			err = NewCLError(ErrSQLSyntaxError, "Failed to query backup count", err)
			return
		}
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("Volume").Preload("Task").Where(query, args...).Where(whereSQL).Find(&backups).Error; err != nil {
		logger.Error("DB: query backup failed", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query backup", err)
		return
	}
	permit := memberShip.CheckSystemPermission()
	if permit {
		_ = permit // SystemAdmin can see all backups
	}
	return
}

func (a *BackupAdmin) Update(ctx context.Context, id int64, name, path string, status model.BackupStatus) (backup *model.VolumeBackup, err error) {
	logger.Infof("ENTER BackupAdmin.Update: id=%d, name=%s, path=%s, status=%s", id, name, path, status)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackupAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT BackupAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	backup, err = a.GetBackupByID(ctx, id)
	if err != nil {
		logger.Error("Failed to get backup", err)
		return
	}
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, backup.Owner)
	if !permit {
		logger.Error("Not authorized to update the backup")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the backup", nil)
		return
	}
	if name != "" && name != backup.Name {
		backup.Name = name
	}
	if path != "" && path != backup.Path {
		backup.Path = path
	}
	if status != "" && status != backup.Status {
		backup.Status = status
	}
	if err = db.Model(&model.VolumeBackup{}).Where("id = ?", backup.ID).Updates(map[string]interface{}{"name": backup.Name, "path": backup.Path, "status": backup.Status}).Error; err != nil {
		logger.Error("DB: update backup failed", err)
		err = NewCLError(ErrDatabaseError, "Failed to update backup", err)
		return
	}
	return
}

