/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// Callbacks of the reconcile of a host that comes back (shared-storage-design.md §11.4)

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"
)

func init() {
	Add("node_recovered", NodeRecovered)
	Add("node_reconciled", NodeReconciled)
	Add("clear_stale_vm", ClearStaleVM)
}

// reportingHost is the host a callback came from; a report naming another host is refused
func reportingHost(ctx context.Context, named string) (int32, error) {
	hostid, ok := ctx.Value("hostid").(int32)
	if !ok || hostid <= 0 {
		return 0, fmt.Errorf("no host in the callback")
	}
	if named != "" {
		if n, err := strconv.Atoi(named); err != nil || int32(n) != hostid {
			return 0, fmt.Errorf("host %d reported for host %s", hostid, named)
		}
	}
	return hostid, nil
}

// NodeRecovered takes the instances a host has defined, after a boot or a gap in its heartbeats
func NodeRecovered(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| node_recovered '<NODE_ID>' '<boot_id>' '<boot|reconnect>' '<base64 JSON [{id, state}]>'
	if len(args) < 5 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args of node_recovered", err)
		return
	}
	hostid, err := reportingHost(ctx, args[1])
	if err != nil {
		logger.Ctx(ctx).Error("Refused node_recovered", err)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(args[4])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid domain list of node_recovered", err)
		return
	}
	domains := []services.ReconcileDomain{}
	if err = json.Unmarshal(raw, &domains); err != nil {
		logger.Ctx(ctx).Error("Invalid domain list of node_recovered", err)
		return
	}
	err = services.ReconcileNode(ctx, hostid, args[2], args[3], domains)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to reconcile host %d: %v", hostid, err)
	}
	return
}

// NodeReconciled: the host applied the answer for this boot
func NodeReconciled(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| node_reconciled '<NODE_ID>' '<boot_id>' '<reason>'
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		return
	}
	hostid, err := reportingHost(ctx, args[1])
	if err != nil {
		logger.Ctx(ctx).Error("Refused node_reconciled", err)
		return
	}
	reason := ""
	if len(args) > 3 {
		reason = args[3]
	}
	err = services.NodeReconciled(ctx, hostid, args[2], reason)
	return
}

// ClearStaleVM: the host removed its copy of an instance it no longer owns. For an evacuated instance it also gives
// up what its router still does for it (floating IPs, secondary addresses), as the source of a migration does
func ClearStaleVM(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| clear_stale_vm.sh '<instance>' '<done|error>'
	if len(args) < 3 {
		err = fmt.Errorf("Wrong params")
		return
	}
	hostid, err := reportingHost(ctx, "")
	if err != nil {
		logger.Ctx(ctx).Error("Refused clear_stale_vm", err)
		return
	}
	instID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	evacuations, err := services.StaleInstanceCleared(ctx, hostid, instID, args[2] == "done")
	if err != nil || len(evacuations) == 0 {
		return
	}
	instance := &model.Instance{Model: model.Model{ID: instID}}
	_, db := GetContextDB(ctx)
	if err = db.Unscoped().Take(instance).Error; err != nil {
		return
	}
	if cerr := clearSourceAddresses(ctx, instance, evacuations[len(evacuations)-1]); cerr != nil {
		logger.Ctx(ctx).Warningf("Failed to clear the addresses of instance %d on host %d: %v", instID, hostid, cerr)
	}
	if terr := services.TgwNodeCheckLeave(ctx, instance.RouterID, hostid); terr != nil {
		logger.Ctx(ctx).Warningf("Failed to check the transit gateway of host %d: %v", hostid, terr)
	}
	return
}
