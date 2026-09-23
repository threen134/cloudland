/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"api/src/services"
)

func init() {
	Add("host_disks", HostDisks)
	Add("local_pool_status", LocalPoolStatus)
	Add("clear_volume", ClearVolume)
	Add("pool_usage", PoolUsage)
}

// callbackHost is the host that ran the script; the id a script passes is only used when the context has none
func callbackHost(ctx context.Context, arg string) int32 {
	if hostid, ok := ctx.Value("hostid").(int32); ok && hostid > 0 {
		return hostid
	}
	id, _ := strconv.ParseInt(arg, 10, 32)
	return int32(id)
}

func decodeDetails(raw string, v interface{}) error {
	if raw == "" || raw == "-" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func dashEmpty(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// HostDisks stores the result of scan_host_disks.sh:
//
//	|:-COMMAND-:| host_disks '<NODE_ID>' '<base64 json array>'
func HostDisks(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	hostid := callbackHost(ctx, args[1])
	if args[2] == "" || args[2] == "-" {
		// The scan did not run; keep the last result
		if len(args) > 3 {
			logger.Ctx(ctx).Warningf("Disk scan on host %d skipped: %s", hostid, args[3])
		}
		return
	}
	disks := []*services.ScannedDisk{}
	if err = decodeDetails(args[2], &disks); err != nil {
		logger.Ctx(ctx).Error("Invalid disk list", err)
		return
	}
	if err = services.SaveScannedDisks(ctx, hostid, disks); err != nil {
		logger.Ctx(ctx).Error("Failed to save the disks", err)
	}
	return
}

// LocalPoolStatus handles the result of a pool command and the pool reports of the heartbeat:
//
//	|:-COMMAND-:| local_pool_status '<NODE_ID>' '<op>' '<pool uuid|builtin>' '<status>' '<reason>' '<base64 json>'
func LocalPoolStatus(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 5 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	hostid := callbackHost(ctx, args[1])
	reason, details := "", ""
	if len(args) > 5 {
		reason = dashEmpty(args[5])
	}
	if len(args) > 6 {
		details = args[6]
	}
	report := &services.PoolReport{}
	if err = decodeDetails(details, report); err != nil {
		logger.Ctx(ctx).Errorf("Invalid pool report of host %d: %v", hostid, err)
		return
	}
	if err = services.HandlePoolStatus(ctx, hostid, args[2], args[3], args[4], reason, report); err != nil {
		logger.Ctx(ctx).Errorf("Failed to handle %s of pool %s on host %d: %v", args[2], args[3], hostid, err)
	}
	return
}

// ClearVolume handles the result of clear_volume_local.sh:
//
//	|:-COMMAND-:| clear_volume '<volume ID>' 'deleted|error' '<reason>'
func ClearVolume(ctx context.Context, args []string) (status string, err error) {
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
	if err = services.HandleClearVolume(ctx, hostid, volID, args[2], reason); err != nil {
		logger.Ctx(ctx).Error("Failed to finish deleting the volume", err)
	}
	return
}

// PoolUsage stores the largest files of a pool reported by pool_usage.sh:
//
//	|:-COMMAND-:| pool_usage '<NODE_ID>' '<pool uuid|builtin>' '<base64 json array>'
func PoolUsage(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	hostid := callbackHost(ctx, args[1])
	entries := []*services.UsageEntry{}
	if err = decodeDetails(args[3], &entries); err != nil {
		logger.Ctx(ctx).Error("Invalid usage report", err)
		return
	}
	if err = services.HandlePoolUsage(ctx, hostid, args[2], entries); err != nil {
		logger.Ctx(ctx).Error("Failed to save the usage report", err)
	}
	return
}
