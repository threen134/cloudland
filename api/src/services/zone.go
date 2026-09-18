/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var (
	zoneAdmin = &ZoneAdmin{}
)

type ZoneAdmin struct{}

func (a *ZoneAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, zones []*model.Zone, err error) {
	logger.Ctx(ctx).Infof("ENTER ZoneAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT ZoneAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT ZoneAdmin.List: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "name"
	}

	zones = []*model.Zone{}
	if err = db.Model(&model.Zone{}).Scopes(dbs.Contains(query, "name")).Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count zones", err)
		return
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Model(&model.Zone{}).Scopes(dbs.Contains(query, "name")).Find(&zones).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to query zones", err)
		return
	}
	db = db.Offset(0).Limit(-1)
	return
}

func (a *ZoneAdmin) Get(ctx context.Context, id int64) (zone *model.Zone, err error) {
	logger.Ctx(ctx).Infof("ENTER ZoneAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT ZoneAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT ZoneAdmin.Get: success, zoneID=%d", zone.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	zone = &model.Zone{ID: id}
	if err = db.Take(zone).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query zone", err)
		err = NewCLError(ErrZoneNotFound, "Zone not found", err)
		return
	}
	return
}

func (a *ZoneAdmin) GetZoneByName(ctx context.Context, name string) (zone *model.Zone, err error) {
	logger.Ctx(ctx).Infof("ENTER ZoneAdmin.GetZoneByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT ZoneAdmin.GetZoneByName: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT ZoneAdmin.GetZoneByName: success, zoneID=%d", zone.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	zone = &model.Zone{}
	err = db.Where("name = ?", name).Take(zone).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query zone, %v", err)
		err = NewCLError(ErrZoneNotFound, "Zone not found", err)
		return
	}
	return
}

func (a *ZoneAdmin) GetDefaultZone(ctx context.Context) (zone *model.Zone, err error) {
	logger.Ctx(ctx).Infof("ENTER ZoneAdmin.GetDefaultZone")
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT ZoneAdmin.GetDefaultZone: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT ZoneAdmin.GetDefaultZone: success, zoneID=%d", zone.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	zone = &model.Zone{}
	err = db.Where("\"default\" = ?", true).Take(zone).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query default zone, %v", err)
		err = NewCLError(ErrZoneNotFound, "Default zone not found in database. Please set a default zone or specify one.", err)
		return
	}
	return
}

func (a *ZoneAdmin) Create(ctx context.Context, name string, isDefault bool, remark string) (zone *model.Zone, err error) {
	logger.Ctx(ctx).Infof("ENTER ZoneAdmin.Create: name=%s, isDefault=%t, remark=%s", name, isDefault, remark)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT ZoneAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT ZoneAdmin.Create: success, zoneID=%d", zone.ID)
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
		logger.Ctx(ctx).Error("Not authorized to create zone")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create zone", nil)
		return
	}

	if isDefault {
		err = db.Model(&model.Zone{}).Where(`"default" = ?`, true).Update("default", false).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to unset existing default zone", err)
			err = NewCLError(ErrUnsetDefaultZoneFailed, "Failed to unset existing default zone", err)
			return
		}
	}

	zone = &model.Zone{
		Name:    name,
		Default: isDefault,
		Remark:  remark,
	}

	err = db.Create(zone).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB create zone failed, %v", err)
		err = NewCLError(ErrZoneCreationFailed, "Failed to create zone", err)
		return
	}

	logger.Ctx(ctx).Debugf("Zone created successfully: %+v", zone)
	return
}

func (a *ZoneAdmin) Update(ctx context.Context, zone *model.Zone, isDefault bool, remark string) (err error) {
	logger.Ctx(ctx).Infof("ENTER ZoneAdmin.Update: zoneID=%d, isDefault=%t, remark=%s", zone.ID, isDefault, remark)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT ZoneAdmin.Update: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT ZoneAdmin.Update: success")
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
		logger.Ctx(ctx).Error("Not authorized to update zone")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update zone", nil)
		return
	}

	if isDefault && !zone.Default {
		err = db.Model(&model.Zone{}).Where(`"default" = ? AND id != ?`, true, zone.ID).Update("default", false).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to unset existing default zone", err)
			err = NewCLError(ErrUnsetDefaultZoneFailed, "Failed to unset existing default zone", err)
			return
		}
	}

	zone.Default = isDefault
	zone.Remark = remark
	err = db.Model(zone).Updates(map[string]interface{}{
		"remark":  remark,
		"default": isDefault,
	}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update zone", err)
		err = NewCLError(ErrZoneUpdateFailed, "Failed to update zone", err)
		return
	}

	logger.Ctx(ctx).Debugf("Zone updated successfully: %+v", zone)
	return
}

func (a *ZoneAdmin) Delete(ctx context.Context, zone *model.Zone) (err error) {
	logger.Ctx(ctx).Infof("ENTER ZoneAdmin.Delete: zoneID=%d, name=%s", zone.ID, zone.Name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT ZoneAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT ZoneAdmin.Delete: success")
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
		logger.Ctx(ctx).Error("Not authorized to delete zone")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete zone", nil)
		return
	}

	hyperCount := int64(0)
	err = db.Model(&model.Hyper{}).Where("zone_id = ?", zone.ID).Count(&hyperCount).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to count hypervisors in zone", err)
		return
	}
	if hyperCount > 0 {
		logger.Ctx(ctx).Error("Zone cannot be deleted while hypervisors belong to this zone")
		err = NewCLError(ErrHypersInZone, "Zone cannot be deleted while hypervisors belong to this zone", nil)
		return
	}

	err = db.Delete(zone).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to delete zone", err)
		err = NewCLError(ErrZoneDeleteFailed, "Failed to delete zone", err)
		return
	}

	logger.Ctx(ctx).Debugf("Zone deleted successfully: %+v", zone)
	return
}
