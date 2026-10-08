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
	Add("rescue_vm", RescueVM)
}

// rescueStatus tells what the rescue_vm.sh callback makes of an instance that is still rescuing: the status to
// set, or "" to keep it rescuing.
//
//	'<state>' 'sync'    the rescue domain is defined: rescuing when it runs, error when it did not start. Both
//	                    stay rescuing, end_rescue removes a rescue domain that did not start and starts the instance
//	'error' 'failed'    the rescue domain was never defined (no rescue image): the instance was stopped for
//	                    nothing and is shut off, nothing to end
//	'<state>' 'refused' the boot disk of a shared pool is not usable on the host: refused before the instance was
//	                    stopped, it is in the domain state reported (running, shut_off, paused)
func rescueStatus(state, stage string) model.InstanceStatus {
	if stage == "refused" {
		switch s := model.InstanceStatus(state); s {
		case model.InstanceStatusRunning, model.InstanceStatusShutoff, model.InstanceStatusPaused:
			return s
		}
		return model.InstanceStatusShutoff
	}
	if state != "rescuing" && stage == "failed" {
		return model.InstanceStatusShutoff
	}
	return ""
}

// RescueVM confirms the rescue of an instance. clapi sets rescuing when it sends rescue_vm.sh, and only this
// callback reports on the rescue (the stop of the instance it does first reports nothing).
// |:-COMMAND-:| rescue_vm.sh '<instance ID>' '<rescuing|error>' '<hostid>' '<sync|failed>'
func RescueVM(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if len(args) < 5 {
		err = fmt.Errorf("wrong params %q", args)
		logger.Ctx(ctx).Error("Invalid rescue_vm callback", err)
		return
	}
	instID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	state, stage := args[2], args[4]
	instance := &model.Instance{}
	if err = db.Where("id = ?", instID).Take(instance).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query instance for rescue", instID, err)
		return
	}
	if hostid, _ := ctx.Value("hostid").(int32); hostid != instance.Hyper {
		logger.Ctx(ctx).Errorf("Rescue of instance %d reported by node %d, instance is on %d", instID, hostid, instance.Hyper)
		return
	}
	if state != "rescuing" {
		logger.Ctx(ctx).Errorf("Rescue of instance %d failed on node %d (%s)", instID, instance.Hyper, stage)
	}
	// The rescue was ended (or the instance deleted) in the meantime: that request decides
	if instance.Status != model.InstanceStatusRescuing {
		logger.Ctx(ctx).Infof("Rescue of instance %d reported %s, instance is %s now", instID, state, instance.Status)
		return
	}
	newStatus := rescueStatus(state, stage)
	if newStatus == "" {
		return
	}
	err = db.Model(&model.Instance{}).Where("id = ? AND status = ?", instID, model.InstanceStatusRescuing).Update("status", newStatus).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update instance status", err)
	}
	return
}
