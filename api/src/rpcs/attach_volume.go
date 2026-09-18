/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"fmt"
	"strconv"

	. "api/src/common"
	"api/src/model"
)

func init() {
	Add("attach_volume_local", AttachVolume)
	Add("attach_volume_wds_vhost", AttachVolume)
}

func AttachVolume(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| attach_volume.sh 5 7 vdb
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	argn := len(args)

	logger.Ctx(ctx).Debugf("AttachVolume called with args: %v, length: %d", args, argn)

	if argn < 4 {
		volID, parseErr := strconv.ParseInt(args[2], 10, 64)
		if parseErr != nil {
			err = fmt.Errorf("Invalid volume ID: %w", parseErr)
			logger.Ctx(ctx).Error("Invalid volume ID", err)
			return
		}
		volume := &model.Volume{Model: model.Model{ID: volID}}
		err = db.Where(volume).Take(volume).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query volume", err)
			return
		}
		err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Update("status", model.VolumeStatusAvailable).Error
		if err != nil {
			logger.Ctx(ctx).Error("Update volume status failed", err)
			return
		}
		return
	}
	instanceID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	volID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid volume ID", err)
		return
	}
	target := args[3]
	volume := &model.Volume{Model: model.Model{ID: volID}}
	err = db.Where(volume).Take(volume).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query volume", err)
		return
	}
	instance := &model.Instance{Model: model.Model{ID: instanceID}}
	err = db.Take(instance).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query instance", err)
		return
	}
	updates := map[string]interface{}{"instance_id": instanceID, "target": target, "status": model.VolumeStatusAttached}
	// 本地卷文件在虚拟机所在节点上（首次挂载时由 attach_volume_local.sh 创建）
	if instance.Hyper > 0 {
		updates["hyper"] = instance.Hyper
	}
	err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(updates).Error
	if err != nil {
		logger.Ctx(ctx).Error("Update volume status failed", err)
		return
	}
	return
}
