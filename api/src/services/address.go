/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"

	. "api/src/common"
	"api/src/model"
)

type AddressAdmin struct{}

func (a *AddressAdmin) GetAddressByUUID(ctx context.Context, uuID string) (addr *model.Address, err error) {
	logger.Infof("ENTER AddressAdmin.GetAddressByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AddressAdmin.GetAddressByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT AddressAdmin.GetAddressByUUID: success, addrID=%d", addr.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	addr = &model.Address{}
	err = db.Preload("Subnet").Where("uuid = ?", uuID).Take(addr).Error
	if err != nil {
		logger.Error("Failed to query address, %v", err)
		return nil, NewCLError(ErrAddressNotFound, "Address not found", err)
	}
	return
}

func (a *AddressAdmin) Update(ctx context.Context, addr *model.Address) (err error) {
	logger.Infof("ENTER AddressAdmin.Update: addrID=%d", addr.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AddressAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT AddressAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = fmt.Errorf("Not authorized for this operation")
		logger.Error("Not authorized for this operation", err)
		return NewCLError(ErrPermissionDenied, "Not authorized for this operation", err)
	}

	if err = db.Model(&model.Address{}).Where("id = ?", addr.ID).Updates(map[string]interface{}{
		"remark":   addr.Remark,
		"reserved": addr.Reserved,
	}).Error; err != nil {
		logger.Error("Failed to update address, %v", err)
		return NewCLError(ErrAddressUpdateFailed, "Failed to update address", err)
	}

	return
}

func (a *AddressAdmin) ListBySubnetID(ctx context.Context, subnetID int64) (addresses []*model.Address, err error) {
	logger.Infof("ENTER AddressAdmin.ListBySubnetID: subnetID=%d", subnetID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AddressAdmin.ListBySubnetID: error=%v", err)
		} else {
			logger.Infof("EXIT AddressAdmin.ListBySubnetID: count=%d", len(addresses))
		}
	}()
	ctx, db := GetContextDB(ctx)
	addresses = []*model.Address{}
	err = db.Preload("Subnet").Where("subnet_id = ?", subnetID).Order("id").Find(&addresses).Error
	if err != nil {
		logger.Error("Failed to query addresses for subnet, %v", err)
		return nil, NewCLError(ErrDatabaseError, "Failed to list addresses", err)
	}
	return
}
