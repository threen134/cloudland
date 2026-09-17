/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var (
	flavorAdmin = &FlavorAdmin{}
)

type FlavorAdmin struct{}

func (a *FlavorAdmin) Create(ctx context.Context, name string, cpu, memory, disk int32) (flavor *model.Flavor, err error) {
	logger.Ctx(ctx).Infof("ENTER FlavorAdmin.Create: name=%s, cpu=%d, memory=%d, disk=%d", name, cpu, memory, disk)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT FlavorAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT FlavorAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	flavor = &model.Flavor{
		Name:   name,
		Cpu:    cpu,
		Disk:   disk,
		Memory: memory,
	}
	err = db.Create(flavor).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create flavor, %v", err)
		return nil, NewCLError(ErrFlavorCreateFailed, "Failed to create flavor", err)
	}
	return flavor, nil
}

func (a *FlavorAdmin) GetFlavorByName(ctx context.Context, name string) (flavor *model.Flavor, err error) {
	logger.Ctx(ctx).Infof("ENTER FlavorAdmin.GetFlavorByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT FlavorAdmin.GetFlavorByName: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT FlavorAdmin.GetFlavorByName: success")
		}
	}()
	_, db := GetContextDB(ctx)
	flavor = &model.Flavor{}
	err = db.Where("name = ?", name).Take(flavor).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query flavor, %v", err)
		return nil, NewCLError(ErrFlavorNotFound, "Flavor not found", err)
	}
	return
}

func (a *FlavorAdmin) Get(ctx context.Context, id int64) (flavor *model.Flavor, err error) {
	logger.Ctx(ctx).Infof("ENTER FlavorAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT FlavorAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT FlavorAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid flavor ID: %d", id), nil)
		logger.Ctx(ctx).Error(err)
		return nil, err
	}
	_, db := GetContextDB(ctx)
	flavor = &model.Flavor{Model: model.Model{ID: id}}
	err = db.Take(flavor).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to query flavor, err", err)
		return nil, NewCLError(ErrFlavorNotFound, "Flavor not found", err)
	}
	return
}

func (a *FlavorAdmin) Delete(ctx context.Context, flavor *model.Flavor) (err error) {
	logger.Ctx(ctx).Infof("ENTER FlavorAdmin.Delete: id=%d, name=%s", flavor.ID, flavor.Name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT FlavorAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT FlavorAdmin.Delete: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var refCount int64
	err = db.Model(&model.Instance{}).Where("flavor_id = ?", flavor.ID).Count(&refCount).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to count the number of instances using the flavor", err)
		return NewCLError(ErrSQLSyntaxError, "Failed to count instances using the flavor", err)
	}
	if refCount > 0 {
		logger.Ctx(ctx).Error("Flavor can not be deleted if there are instances using it")
		err = NewCLError(ErrFlavorInUse, "The flavor can not be deleted if there are instances using it", nil)
		return err
	}
	if err = db.Delete(flavor).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to delete flavor", err)
		return NewCLError(ErrFlavorDeleteFailed, "Failed to delete flavor", err)
	}
	return
}

func (a *FlavorAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, flavors []*model.Flavor, err error) {
	logger.Ctx(ctx).Infof("ENTER FlavorAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT FlavorAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT FlavorAdmin.List: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	flavors = []*model.Flavor{}
	if err = db.Model(&model.Flavor{}).Scopes(dbs.Contains(query, "name")).Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count flavors", err)
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Scopes(dbs.Contains(query, "name")).Find(&flavors).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to list flavors", err)
	}

	return
}
