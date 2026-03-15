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

	"github.com/spf13/viper"
)

var (
	imageStorageAdmin = &ImageStorageAdmin{}
)

type ImageStorageAdmin struct{}

func (a *ImageStorageAdmin) List(offset, limit int64, order string, image *model.Image, query string) (total int64, storages []*model.ImageStorage, err error) {
	db := DB()
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	if query != "" {
		query = fmt.Sprintf("pool_id = '%s'", query)
	}

	storages = []*model.ImageStorage{}
	if err = db.Model(&model.ImageStorage{}).Where("image_id = ?", image.ID).Where(query).Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count image storage(s)", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Where("image_id = ?", image.ID).Where(query).Find(&storages).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to query image storage(s)", err)
		return
	}

	return
}

// InitStorages initializes the image storage records for a given image and pool
func (a *ImageStorageAdmin) InitStorages(ctx context.Context, image *model.Image, pools []string) (storagesResp []*model.ImageStorage, err error) {
	ctx, db := GetContextDB(ctx)
	defaultPoolID := viper.GetString("volume.default_wds_pool_id")
	containsDefault := false
	// valid pools
	finalPools := make([]string, 0)
	for _, poolID := range pools {
		dictionary := &model.Dictionary{}
		if poolID == defaultPoolID {
			finalPools = append(finalPools, defaultPoolID)
			containsDefault = true
			continue
		}
		dictionary, err = (&DictionaryAdminService{}).Find(ctx, "storage_pool", poolID)
		if err != nil {
			logger.Errorf("Failed to find storage pool %s, %v", poolID, err)
			return
		}
		finalPools = append(finalPools, dictionary.Value)
	}

	// set default
	if !containsDefault {
		finalPools = append(finalPools, defaultPoolID)
	}

	// load exists image storage records
	var storages []*model.ImageStorage
	if err = db.Where("image_id = ?", image.ID).Find(&storages).Error; err != nil {
		logger.Errorf("Failed to list image storage data, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to list image storage data", err)
		return
	}

	storageMap := make(map[string]*model.ImageStorage)
	for _, storage := range storages {
		storageMap[storage.PoolID] = storage
	}

	for _, poolID := range finalPools {
		if storage, exists := storageMap[poolID]; exists {
			if storage.Status != model.StorageStatusSynced && storage.Status != model.StorageStatusSyncing {
				storage.Status = model.StorageStatusUnknown
				if err = db.Save(&storage).Error; err != nil {
					logger.Error("Update image storage failed", err)
					err = NewCLError(ErrImageStorageCreateFailed, "Failed to Create image storage", err)
					return
				}
			}
			storagesResp = append(storagesResp, storage)
		} else {
			newStorage := &model.ImageStorage{
				Image:   image,
				ImageID: image.ID,
				PoolID:  poolID,
				Status:  model.StorageStatusUnknown,
			}
			if err = db.Create(newStorage).Error; err != nil {
				logger.Error("Create new image storage failed", err)
				err = NewCLError(ErrImageStorageCreateFailed, "Failed to Create image storage", err)
				return
			}
			storagesResp = append(storagesResp, newStorage)
		}
	}
	return
}

func (a *ImageStorageAdmin) CheckDefaultPool() (err error) {
	driver := GetVolumeDriver()
	db := DB()
	if driver != "local" {
		defaultPool := viper.GetString("volume.default_wds_pool_id")
		err = db.Where("category='storage_pool' AND value=?", defaultPool).First(&model.Dictionary{}).Error
		if err != nil {
			err = fmt.Errorf("default storage pool %s is not in configurations", defaultPool)
		}
	}
	return
}
