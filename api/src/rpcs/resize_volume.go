/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	. "api/src/common"
	"api/src/model"
	"context"
	"fmt"
	"strconv"
)

func init() {
	Add("resize_volume", ResizeVolume)
}

func ResizeVolume(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| resize_volume.sh 5 success
	//|:-COMMAND-:| resize_volume.sh 5 error [<actual size in GiB>]
	logger.Ctx(ctx).Debug("ResizeVolumeLocal", args)
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	argn := len(args)
	if argn < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	volID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid volume ID", err)
		return
	}
	volume := &model.Volume{Model: model.Model{ID: volID}}
	err = db.Where(volume).Take(volume).Error
	if err != nil {
		logger.Ctx(ctx).Error("Invalid volume ID", err)
		return
	}
	usable := model.VolumeStatusAvailable.String()
	if volume.InstanceID != 0 {
		usable = model.VolumeStatusAttached.String()
	}
	updates := map[string]interface{}{}
	status = args[2]
	if status != "error" {
		status = usable
	} else if actual := actualSize(args); actual > 0 {
		// The resize did not happen and the node says how big the image really is: put back the size
		// recorded when the resize was requested and keep the volume usable. Without this it would stay
		// in "error" with a size it does not have, and error volumes can be neither resized nor detached.
		logger.Ctx(ctx).Warningf("Resize of volume %d failed on the node, rolling its size back from %d to %d GiB", volume.ID, volume.Size, actual)
		status = usable
		updates["size"] = actual
		if volume.Booting && volume.InstanceID != 0 {
			if err = db.Model(&model.Instance{}).Where("id = ?", volume.InstanceID).Update("disk", actual).Error; err != nil {
				logger.Ctx(ctx).Error("Update instance disk failed", err)
				return
			}
		}
	}
	updates["status"] = status
	err = db.Model(&volume).Updates(updates).Error
	if err != nil {
		logger.Ctx(ctx).Error("Update volume status failed", err)
		return
	}
	return
}

// actualSize is the image size a failed resize reports as its 4th argument (0 when absent or invalid)
func actualSize(args []string) int32 {
	if len(args) < 4 {
		return 0
	}
	size, err := strconv.ParseInt(args[3], 10, 32)
	if err != nil || size <= 0 {
		return 0
	}
	return int32(size)
}
