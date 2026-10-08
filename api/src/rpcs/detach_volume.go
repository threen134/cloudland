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
	Add("detach_volume_local", DetachVolume)
}

// DetachVolume handles the result of detach_volume_local.sh:
//
//	|:-COMMAND-:| detach_volume_local.sh '<instance ID>' '<volume ID>' 'available|attached'
func DetachVolume(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	argn := len(args)
	if argn < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	volID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid volume ID", err)
		return
	}
	volume := &model.Volume{Model: model.Model{ID: volID}}
	if err = db.Take(volume).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query volume", err)
		return
	}
	if volume.Status != model.VolumeStatusDetaching {
		logger.Ctx(ctx).Warningf("Volume %d is %s, not detaching, ignore the callback", volume.ID, volume.Status)
		return
	}
	updates := map[string]interface{}{"status": model.VolumeStatus(args[3])}
	if args[3] == string(model.VolumeStatusAvailable) {
		updates["instance_id"] = 0
		updates["target"] = ""
	}
	err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(updates).Error
	if err != nil {
		logger.Ctx(ctx).Error("Update volume status failed", err)
		return
	}
	return
}
