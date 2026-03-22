/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/utils"
)

var ImageAdmin = &ImageAdminService{}

type ImageAdminService struct{}

func FileExist(filename string) bool {
	_, err := os.Lstat(filename)
	return !os.IsNotExist(err)
}
func (a *ImageAdminService) Create(ctx context.Context, osCode, name, osVersion, virtType, userName, url, architecture, bootLoader string, isRescue bool, instID int64, uuid string, rescueImage *model.Image, osFamily string) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.Create: name=%s, osCode=%s, osVersion=%s", name, osCode, osVersion)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Create: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Create: success")
		}
	}()
	logger.Debugf("Creating image %s %s %s %s %s %s %s %s %t %d %s %s", osCode, name, osVersion, virtType, userName, url, architecture, bootLoader, isRescue, instID, uuid, osFamily)
	memberShip := GetMemberShip(ctx)
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var instance *model.Instance
	if instID > 0 {
		instance = &model.Instance{Model: model.Model{ID: instID}}
		err = db.Preload("Image").Preload("Volumes").Take(instance).Error
		if err != nil {
			logger.Error("DB failed to query instance", err)
			return nil, NewCLError(ErrInstanceNotFound, "Instance not found", err)
		}
		if instance.Status != model.InstanceStatusShutoff {
			err = fmt.Errorf("instance [%s] is running, shut it down first before capturing", instance.Hostname)
			logger.Error(err)
			return nil, NewCLError(ErrInstanceInvalidState, "Instance is running", err)
		}
		image = instance.Image.Clone()
		image.Model = model.Model{Creater: memberShip.UserID}
		image.Owner = memberShip.OrgID
		image.Name = name
		image.Status = "creating"
		image.CaptureFromInstanceID = instance.ID
		image.CaptureFromInstance = instance
		image.OsFamily = osFamily
	} else {
		image = &model.Image{
			Model:        model.Model{Creater: memberShip.UserID},
			Owner:        memberShip.OrgID,
			OsVersion:    osVersion,
			VirtType:     virtType,
			UserName:     userName,
			Name:         name,
			OSCode:       osCode,
			Status:       "creating",
			Architecture: architecture,
			BootLoader:   bootLoader,
			OsFamily:     osFamily,
		}
		if uuid != "" {
			logger.Debugf("Creating image with UUID %s", uuid)
			image.UUID = uuid
		}
	}
	// Set visibility based on creator identity: SystemAdmin → public, others → private
	// Must be set after both branches (upload and capture) to override Clone() inheritance
	if memberShip.IsSystemAdmin() {
		image.Visibility = model.ImageVisibilityPublic
	} else {
		image.Visibility = model.ImageVisibilityPrivate
	}
	image.QAEnabled = true
	image.IsRescue = isRescue
	if rescueImage != nil {
		image.RescueImage = rescueImage.ID
	}
	image.StorageType = GetVolumeDriver()
	logger.Debugf("Creating image %+v", image)
	err = db.Create(image).Error
	if err != nil {
		logger.Error("DB create image failed, %v", err)
		return nil, NewCLError(ErrImageCreateFailed, "Failed to create image record", err)
	}

	// create default storage
	defaultPool := viper.GetString("volume.default_wds_pool_id")
	storageID := int64(0)
	if defaultPool != "" {
		storage := &model.ImageStorage{
			ImageID: image.ID,
			Image:   image,
			PoolID:  defaultPool,
			Status:  model.StorageStatusUnknown,
		}
		if err = db.Create(storage).Error; err != nil {
			logger.Error("Failed to create default image storage", err)
			return nil, NewCLError(ErrImageStorageCreateFailed, "Failed to create default image storage", err)
		}
		storageID = storage.ID
	}

	// create with default pool id
	prefix := strings.Split(image.UUID, "-")[0]
	control := "inter="
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_image.sh '%d' '%s' '%s' '%d'", image.ID, prefix, url, storageID)
	if instID > 0 {
		bootVolumeUUID := ""
		if instance.Volumes != nil {
			for _, volume := range instance.Volumes {
				if volume.Booting {
					bootVolumeUUID = volume.GetOriginVolumeID()
					break
				}
			}
		}
		control = fmt.Sprintf("inter=%d", instance.Hyper)
		command = fmt.Sprintf("/opt/cloudland/scripts/backend/capture_image.sh '%d' '%s' '%d' '%s' '%d'", image.ID, prefix, instance.ID, bootVolumeUUID, storageID)
	}
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Error("Create image command execution failed", err)
		return
	}
	return
}

func (a *ImageAdminService) GetImageByUUID(ctx context.Context, uuID string) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.GetImageByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.GetImageByUUID: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.GetImageByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	image = &model.Image{}
	err = db.Where("uuid = ?", uuID).Take(image).Error
	if err != nil {
		logger.Error("Failed to query image, %v", err)
		return nil, NewCLError(ErrImageNotFound, "Image not found", err)
	}
	memberShip := GetMemberShip(ctx)
	// Public images are accessible to all authenticated users; private images require org ownership
	if image.Visibility != model.ImageVisibilityPublic {
		permit := memberShip.CheckResourceOrg(model.OrgReader, image.Owner)
		if !permit {
			logger.Error("Not authorized to get image")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get image", nil)
			return
		}
	}
	return
}

// GetImageByName returns an image by name.
// Note: name is not globally unique. If multiple orgs have public images with the same name,
// the result is non-deterministic. Internal RPC callbacks may use this; external callers should prefer UUID.
func (a *ImageAdminService) GetImageByName(ctx context.Context, name string) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.GetImageByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.GetImageByName: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.GetImageByName: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	image = &model.Image{}
	err = db.Where("name = ?", name).Take(image).Error
	if err != nil {
		logger.Error("Failed to query image, %v", err)
		return nil, NewCLError(ErrImageNotFound, "Image not found", err)
	}
	memberShip := GetMemberShip(ctx)
	if image.Visibility != model.ImageVisibilityPublic {
		permit := memberShip.CheckResourceOrg(model.OrgReader, image.Owner)
		if !permit {
			logger.Error("Not authorized to get image")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get image", nil)
			return
		}
	}
	return
}

func (a *ImageAdminService) Get(ctx context.Context, id int64) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, "Invalid image ID", nil)
		logger.Error(err)
		return
	}
	ctx, db := GetContextDB(ctx)
	image = &model.Image{Model: model.Model{ID: id}}
	err = db.Take(image).Error
	if err != nil {
		logger.Error("DB failed to query image, %v", err)
		return nil, NewCLError(ErrImageNotFound, "Image not found", err)
	}
	memberShip := GetMemberShip(ctx)
	if image.Visibility != model.ImageVisibilityPublic {
		permit := memberShip.CheckResourceOrg(model.OrgReader, image.Owner)
		if !permit {
			logger.Error("Not authorized to get image")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get image", nil)
			return
		}
	}
	return
}

func (a *ImageAdminService) GetImage(ctx context.Context, reference *BaseReference) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.GetImage: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.GetImage: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.GetImage: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "Image base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		image, err = a.GetImageByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		image, err = a.GetImageByName(ctx, reference.Name)
		return
	}
	return
}

func (a *ImageAdminService) Delete(ctx context.Context, image *model.Image) (err error) {
	logger.Infof("ENTER ImageAdmin.Delete: id=%d, uuid=%s", image.ID, image.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, image.Owner)
	if !permit {
		logger.Error("Not authorized to delete image")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete image", nil)
		return
	}
	refCount := 0
	err = db.Model(&model.Instance{}).Where("image_id = ?", image.ID).Count(&refCount).Error
	if err != nil {
		logger.Error("Failed to count the number of instances using the image", err)
		return NewCLError(ErrSQLSyntaxError, "Failed to count instances using the image", err)
	}
	if refCount > 0 {
		logger.Error("Image can not be deleted if there are instances using it")
		err = NewCLError(ErrImageInUse, "The image can not be deleted if there are instances using it", nil)
		return
	}
	prefix := strings.Split(image.UUID, "-")[0]
	control := "inter=0"
	total, storages, _ := imageStorageAdmin.List(0, -1, "", image, "")
	if image.Status == "available" {
		if total > 0 {
			for _, storage := range storages {
				if storage.Status != model.StorageStatusSynced {
					continue
				}
				command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_image.sh '%d' '%s' '%s' '%s'", image.ID, prefix, image.Format, storage.VolumeID)
				err = HyperExecute(ctx, control, command)
				if err != nil {
					logger.Error("Clear image storage command execution failed", err)
					return
				}
			}
		} else {
			command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_image.sh '%d' '%s' '%s' %s'", image.ID, prefix, image.Format, "")
			err = HyperExecute(ctx, control, command)
			if err != nil {
				logger.Error("Clear image command execution failed", err)
				return
			}
		}
	}
	if err = db.Delete(image).Error; err != nil {
		return NewCLError(ErrImageDeleteFailed, "Failed to delete image record", err)
	}
	if err = db.Where("image_id = ?", image.ID).Delete(&model.ImageStorage{}).Error; err != nil {
		return NewCLError(ErrImageStorageDeleteFailed, "Failed to delete image storage records", err)
	}
	return
}

func (a *ImageAdminService) List(ctx context.Context, offset, limit int64, order, query, visibility string) (total int64, images []*model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.List: offset=%d, limit=%d, order=%s, query=%s, visibility=%s", offset, limit, order, query, visibility)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.List: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.List: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	// Build base query with parameterized name search (fix SQL injection)
	baseQuery := db.Model(&model.Image{})
	if query != "" {
		baseQuery = baseQuery.Where("name LIKE ?", "%"+query+"%")
	}

	// Apply owner/visibility filtering
	if !memberShip.IsSystemAdmin() {
		// Normal user: see own org images + public images, with optional visibility filter
		switch visibility {
		case model.ImageVisibilityPublic:
			baseQuery = baseQuery.Where("visibility = ?", model.ImageVisibilityPublic)
		case model.ImageVisibilityPrivate:
			baseQuery = baseQuery.Where("owner = ? AND visibility = ?", memberShip.OrgID, model.ImageVisibilityPrivate)
		default:
			// "all" or empty: own + public
			baseQuery = baseQuery.Where("owner = ? OR visibility = ?", memberShip.OrgID, model.ImageVisibilityPublic)
		}
	} else {
		// SystemAdmin: optional visibility filter, no owner restriction
		switch visibility {
		case model.ImageVisibilityPublic:
			baseQuery = baseQuery.Where("visibility = ?", model.ImageVisibilityPublic)
		case model.ImageVisibilityPrivate:
			baseQuery = baseQuery.Where("visibility = ?", model.ImageVisibilityPrivate)
		}
	}

	images = []*model.Image{}
	if err = baseQuery.Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count images", err)
	}
	sortedQuery := dbs.Sortby(baseQuery.Offset(offset).Limit(limit), order)
	if err = sortedQuery.Find(&images).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to find images", err)
	}

	return
}

func (a *ImageAdminService) Update(ctx context.Context, image *model.Image, osCode, name, osVersion, userName string, pools []string, osFamily, uuid string, public *bool) (err error) {
	logger.Infof("ENTER ImageAdmin.Update: id=%d, name=%s, osCode=%s", image.ID, name, osCode)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		logger.Error("Not authorized to update image")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update image", nil)
		return
	}
	if osCode != "" {
		image.OSCode = osCode
	}
	if name != "" {
		image.Name = name
	}
	if osVersion != "" {
		image.OsVersion = osVersion
	}
	if userName != "" {
		image.UserName = userName
	}

	if uuid != "" {
		if !utils.IsUUID(uuid) {
			logger.Error("Invalid UUID format")
			err = NewCLError(ErrInvalidParameter, "Invalid UUID format", nil)
			return
		}
		image.UUID = uuid
	}

	if osFamily != "" {
		image.OsFamily = osFamily
	}

	if public != nil {
		if *public {
			image.Visibility = model.ImageVisibilityPublic
		} else {
			image.Visibility = model.ImageVisibilityPrivate
		}
	}

	if image.Status != "available" {
		logger.Error("Image status is not available, cannot update")
		err = NewCLError(ErrImageNotAvailable, "Image status is not available, cannot update", nil)
		return
	}

	err = db.Model(image).Updates(image).Error
	if err != nil {
		logger.Error("Failed to save image", err)
		return NewCLError(ErrImageUpdateFailed, "Failed to save image", err)
	}

	driver := GetVolumeDriver()
	if driver == "local" {
		return
	}

	defaultPoolID := viper.GetString("volume.default_wds_pool_id")
	storages, err := imageStorageAdmin.InitStorages(ctx, image, pools)
	if err != nil {
		logger.Error("Failed to initialize image storages", err)
		return
	}

	logger.Debugf("Image %s storages: %+v", image.UUID, storages)

	sourceVolumeID := ""
	for _, storage := range storages {
		if storage.PoolID == defaultPoolID {
			sourceVolumeID = storage.VolumeID
			break
		}
	}
	if sourceVolumeID == "" {
		logger.Error("Source volume ID not found for image")
		return NewCLError(ErrImageStorageNotFound, "Source volume ID not found for image", err)
	}
	for _, storage := range storages {
		// ignore already synced or syncing storages
		if storage.Status == model.StorageStatusSynced || storage.Status == model.StorageStatusSyncing {
			logger.Debugf("Image %s storage %s is already synced or syncing, skipping", image.UUID, storage.PoolID)
			continue
		}
		prefix := strings.Split(image.UUID, "-")[0]
		control := "inter="
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clone_image.sh '%d' '%s' '%s' '%d' '%s'", image.ID, prefix, storage.PoolID, storage.ID, sourceVolumeID)
		if storage.PoolID == defaultPoolID {
			command = fmt.Sprintf("/opt/cloudland/scripts/backend/sync_image_info.sh '%d' '%s' '%s' '%d'", image.ID, prefix, storage.PoolID, storage.ID)
		}
		storage.Status = model.StorageStatusSyncing
		if err = db.Model(storage).Updates(storage).Error; err != nil {
			logger.Error("Failed to update image storage status", err)
			return NewCLError(ErrImageStorageUpdateFailed, "Failed to update image storage status", err)
		}
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Sync remote info command execution failed", err)
		}
	}
	return
}

