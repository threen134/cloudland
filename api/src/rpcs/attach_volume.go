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
}

// AttachVolume handles the result of attach_volume_local.sh:
//
//	|:-COMMAND-:| attach_volume_local.sh '<instance ID>' '<volume ID>' '<device>'
//	|:-COMMAND-:| attach_volume_local.sh '<instance ID>' '<volume ID>' '-' '<new|existing>' '<reason>'
func AttachVolume(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	argn := len(args)
	logger.Ctx(ctx).Debugf("AttachVolume called with args: %v, length: %d", args, argn)
	if argn < 3 {
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
	// A repeated callback of an attach already handled changes nothing
	if volume.Status != model.VolumeStatusAttaching {
		logger.Ctx(ctx).Warningf("Volume %d is %s, not attaching, ignore the callback", volume.ID, volume.Status)
		return
	}
	hostid, _ := ctx.Value("hostid").(int32)
	if argn < 4 || args[3] == "" || args[3] == "-" {
		mode, reason := "", ""
		if argn > 4 {
			mode = args[4]
		}
		if argn > 5 {
			reason = args[5]
		}
		updates := map[string]interface{}{"status": model.VolumeStatusAvailable, "instance_id": 0, "target": "", "reason": reason}
		// The node removed the file it had just created, so the volume is again not created anywhere
		if mode == "new" {
			updates["hyper"] = 0
		}
		logger.Ctx(ctx).Errorf("Attaching volume %d failed on host %d: %s", volume.ID, hostid, reason)
		err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(updates).Error
		if err != nil {
			logger.Ctx(ctx).Error("Update volume status failed", err)
		}
		return
	}
	instanceID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	updates := map[string]interface{}{"instance_id": instanceID, "target": args[3], "status": model.VolumeStatusAttached, "reason": ""}
	// The file is on the host that ran the script, whatever instances.hyper says at this moment
	if hostid > 0 {
		updates["hyper"] = hostid
	}
	err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(updates).Error
	if err != nil {
		logger.Ctx(ctx).Error("Update volume status failed", err)
		return
	}
	return
}
