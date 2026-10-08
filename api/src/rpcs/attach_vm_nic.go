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
	Add("attach_vm_nic", AttachInterface)
}

func AttachInterface(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| attach_nic.sh 5 101 1
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
	instID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid gateway ID", err)
		return
	}
	instance := &model.Instance{Model: model.Model{ID: instID}}
	err = db.Take(instance).Error
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	macAddr := args[2]
	iface := &model.Interface{}
	err = db.Preload("SecondAddresses").Preload("SecondAddresses.Subnet").Preload("Address").Preload("Address.Subnet").Where("instance = ? and mac_addr = ?", instID, macAddr).Take(iface).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to get interface", err)
		return
	}
	hyperID, err := strconv.Atoi(args[3])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid hyper ID", err)
		return
	}
	iface.Hyper = int32(hyperID)
	err = db.Model(&model.Interface{Model: model.Model{ID: int64(iface.ID)}}).Updates(map[string]interface{}{"hyper": int32(hyperID)}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update interface", err)
		return
	}
	err = sendFdbRules(ctx, instance, nil, iface)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to send fdb rules for interface", err)
		return
	}
	// The NIC may bring a transit gateway member VPC to this node for the first time
	routerID := iface.RouterID
	if routerID == 0 {
		routerID = instance.RouterID
	}
	if terr := services.TgwResyncNode(ctx, routerID, int32(hyperID)); terr != nil {
		logger.Ctx(ctx).Warningf("Failed to sync the transit gateway to hyper %d, %v", hyperID, terr)
	}
	return
}
