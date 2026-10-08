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
	"api/src/services"
)

func init() {
	Add("create_volume_local", CreateVolumeLocal)
}

func updateInstance(ctx context.Context, volume *model.Volume, status string, reason string) (err error) {
	ctx, db := GetContextDB(ctx)
	if volume.Booting && status == "error" {
		err = db.Model(&model.Instance{}).Where("id = ?", volume.InstanceID).Updates(map[string]interface{}{
			"status": model.InstanceStatus(status),
			"reason": reason,
		}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Update instance status failed", err)
			return err
		}
	}
	return
}

// CreateVolumeLocal handles the boot disk created by launch_vm.sh / reinstall_vm.sh:
//
//	|:-COMMAND-:| create_volume_local '<volume ID>' '<path in pool>' 'attached|error' '<reason>'
//
// The path is decided by clapi and not taken from the callback; the host is the one that ran the script.
func CreateVolumeLocal(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	logger.Ctx(ctx).Debug("CreateVolumeLocal", args)
	argn := len(args)
	if argn < 4 {
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
	if err = db.Take(volume).Error; err != nil {
		logger.Ctx(ctx).Error("Invalid volume ID", err)
		return
	}
	status = args[3]
	reason := ""
	if argn > 4 {
		reason = args[4]
	}
	updates := map[string]interface{}{"status": status}
	if status == string(model.VolumeStatusError) {
		updates["reason"] = reason
	} else {
		updates["reason"] = ""
		if hostid, _ := ctx.Value("hostid").(int32); hostid > 0 {
			updates["hyper"] = hostid
		}
	}
	if err = db.Model(&model.Volume{}).Where("id = ?", volume.ID).Updates(updates).Error; err != nil {
		logger.Ctx(ctx).Error("Update volume status failed", err)
		return
	}
	if err = updateInstance(ctx, volume, status, reason); err != nil {
		logger.Ctx(ctx).Error("Update instance status failed", err)
		return
	}
	// The boot disk is counted by its size from now on, or it is not there at all
	services.ReleaseReservations(ctx, 0, volume.ID, model.ReservationBoot)
	return
}
