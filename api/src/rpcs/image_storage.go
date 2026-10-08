/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// Callbacks of the boot disks in shared pools (shared-storage-design.md §9.6, §9.7): the copy of an image in a pool,
// and the boot disk cloned from it

import (
	"context"
	"fmt"
	"strconv"

	"api/src/services"
)

func init() {
	Add("image_storage_status", ImageStorageStatus)
	Add("image_storage_progress", ImageStorageProgress)
	Add("create_boot_shared", CreateBootShared)
}

// ImageStorageProgress takes the progress of an import the heartbeat of its host reports (image_import_report):
//
//	|:-COMMAND-:| image_storage_progress '<image storage ID>' 'wait|download|write' '<percent>'
func ImageStorageProgress(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid image storage ID", err)
		return
	}
	percent, err := strconv.Atoi(args[3])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid percent", err)
		return
	}
	hostid, _ := ctx.Value("hostid").(int32)
	if err = services.HandleImageStorageProgress(ctx, hostid, id, args[2], percent); err != nil {
		logger.Ctx(ctx).Errorf("Failed to take the progress of image copy %d: %v", id, err)
	}
	return
}

// ImageStorageStatus takes the end of an import (import_image_shared.sh) or a removal (delete_image_shared.sh):
//
//	|:-COMMAND-:| image_storage_status '<image storage ID>' 'synced|error|deleted|delete_failed' '<reason>'
func ImageStorageStatus(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid image storage ID", err)
		return
	}
	reason := ""
	if len(args) > 3 {
		reason = dashEmpty(args[3])
	}
	hostid, _ := ctx.Value("hostid").(int32)
	if err = services.HandleImageStorageStatus(ctx, hostid, id, args[2], reason); err != nil {
		logger.Ctx(ctx).Errorf("Failed to handle the report on image copy %d: %v", id, err)
	}
	return
}

// CreateBootShared takes the boot disk launch_vm.sh or reinstall_vm.sh made in a shared pool:
//
//	|:-COMMAND-:| create_boot_shared '<volume ID>' 'attached|error|nocopy' '<image storage ID cloned from, 0 for a copy>' '<reason>'
//
// nocopy: the copy named was not in the pool
func CreateBootShared(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	volID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid volume ID", err)
		return
	}
	baseID, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid image storage ID", err)
		return
	}
	reason := ""
	if len(args) > 4 {
		reason = dashEmpty(args[4])
	}
	hostid, _ := ctx.Value("hostid").(int32)
	if err = services.HandleSharedBootCreated(ctx, hostid, volID, args[2], baseID, reason); err != nil {
		logger.Ctx(ctx).Errorf("Failed to finish the boot disk %d: %v", volID, err)
	}
	return
}
