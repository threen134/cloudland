/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// Callbacks of the shared pools (shared-storage-design.md §9.2, §9.4): named after the operation, not the driver

import (
	"context"
	"fmt"
	"strconv"

	"api/src/services"
)

func init() {
	Add("shared_pool_status", SharedPoolStatus)
	Add("create_volume_shared", CreateVolumeShared)
	Add("attach_volume_shared", AttachVolumeShared)
}

// SharedPoolStatus takes how a host sees the shared pools of its clusters, sent by the heartbeat:
//
//	|:-COMMAND-:| shared_pool_status '<NODE_ID>' '<base64 json array of {pool, status, reason, size, used, avail}>'
func SharedPoolStatus(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	hostid := callbackHost(ctx, args[1])
	reports := []*services.SharedPoolReport{}
	if err = decodeDetails(args[2], &reports); err != nil {
		logger.Ctx(ctx).Errorf("Invalid shared pool report of host %d: %v", hostid, err)
		return
	}
	if err = services.HandleSharedPoolStatus(ctx, hostid, reports); err != nil {
		logger.Ctx(ctx).Errorf("Failed to handle the shared pool report of host %d: %v", hostid, err)
	}
	return
}

// CreateVolumeShared handles the result of create_volume_shared.sh:
//
//	|:-COMMAND-:| create_volume_shared '<volume ID>' 'available|error' '<reason>'
func CreateVolumeShared(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	volID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid volume ID", err)
		return
	}
	reason := ""
	if len(args) > 3 {
		reason = dashEmpty(args[3])
	}
	hostid, _ := ctx.Value("hostid").(int32)
	if err = services.HandleSharedVolumeCreated(ctx, hostid, volID, args[2], reason); err != nil {
		logger.Ctx(ctx).Error("Failed to finish creating the volume", err)
	}
	return
}

// AttachVolumeShared handles the result of attach_volume_shared.sh:
//
//	|:-COMMAND-:| attach_volume_shared '<instance ID>' '<volume ID>' '<device>'
//	|:-COMMAND-:| attach_volume_shared '<instance ID>' '<volume ID>' '-' '<reason>'
func AttachVolumeShared(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
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
	reason := ""
	if len(args) > 4 {
		reason = dashEmpty(args[4])
	}
	hostid, _ := ctx.Value("hostid").(int32)
	if err = services.HandleSharedVolumeAttached(ctx, hostid, instanceID, volID, args[3], reason); err != nil {
		logger.Ctx(ctx).Error("Failed to finish attaching the volume", err)
	}
	return
}
