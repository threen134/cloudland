/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var SecruleAdmin = &SecruleAdminService{}

type SecruleAdminService struct{}

func (a *SecruleAdminService) ApplySecgroup(ctx context.Context, secgroup *model.SecurityGroup) (err error) {
	logger.Infof("ENTER SecruleAdmin.ApplySecgroup: secgroupID=%d", secgroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.ApplySecgroup: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.ApplySecgroup: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	err = secgroupAdmin.GetSecgroupInterfaces(ctx, secgroup)
	if err != nil {
		logger.Error("DB failed to get security group related interfaces", err)
		return
	}
	for _, iface := range secgroup.Interfaces {
		logger.Debugf("iface: %+v", iface)
		if iface.Instance > 0 {
			instance := &model.Instance{Model: model.Model{ID: iface.Instance}}
			err = db.Take(instance).Error
			if err != nil {
				logger.Error("DB failed to get instance, %v", err)
				err = nil
				continue
			}
			if iface.Address != nil {
				err = ApplyInterface(ctx, instance, iface, false)
				if err != nil {
					logger.Error("DB failed to apply interface, %v", err)
					err = nil
					continue
				}
			}
		}
	}
	return
}

func (a *SecruleAdminService) Update(ctx context.Context, secrule *model.SecurityRule, secgroup *model.SecurityGroup, name, remoteIp, direction, protocol string, portMin, portMax int32) (err error) {
	logger.Infof("ENTER SecruleAdmin.Update: id=%d, name=%s, remoteIp=%s, direction=%s, protocol=%s, portMin=%d, portMax=%d", secrule.ID, name, remoteIp, direction, protocol, portMin, portMax)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.Update: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, secrule.Owner)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if remoteIp != "" {
		netLen := strings.Split(remoteIp, "/")
		if len(netLen) != 2 {
			err = NewCLError(ErrInvalidParameter, "Invalid CIDR format for RemoteIp", nil)
			return
		}
		n, _ := strconv.Atoi(netLen[1])
		if n < 0 || n > 32 {
			err = NewCLError(ErrInvalidParameter, "Invalid Netmask for RemoteIp", nil)
			return
		}
		secrule.RemoteIp = remoteIp
	}
	if direction != "" {
		secrule.Direction = direction
	}
	if protocol != "" {
		secrule.Protocol = protocol
	}
	if name != "" {
		secrule.Name = name
	}
	// Note: protocol field is already updated above, so secrule.Protocol reflects the new value
	if secrule.Protocol == "icmp" {
		secrule.PortMin = -1
		secrule.PortMax = -1
	} else {
		if portMin > 0 && portMin <= 65535 {
			secrule.PortMin = portMin
		} else if portMin > 65535 {
			err = NewCLError(ErrInvalidParameter, "PortMin out of range, must be 1-65535", nil)
			return
		}
		if portMax > 0 && portMax <= 65535 {
			secrule.PortMax = portMax
		} else if portMax > 65535 {
			err = NewCLError(ErrInvalidParameter, "PortMax out of range, must be 1-65535", nil)
			return
		}
		if secrule.PortMin > secrule.PortMax {
			err = NewCLError(ErrInvalidParameter, "PortMax should be greater than or equal to PortMin", nil)
			return
		}
	}
	err = db.Model(&model.SecurityRule{}).Where("id = ?", secrule.ID).Updates(map[string]interface{}{"name": secrule.Name, "remote_ip": secrule.RemoteIp, "direction": secrule.Direction, "protocol": secrule.Protocol, "port_min": secrule.PortMin, "port_max": secrule.PortMax}).Error
	if err != nil {
		logger.Error("DB failed to save security rule ", err)
		err = NewCLError(ErrSecurityRuleUpdateFailed, "Failed to update security rule", err)
		return
	}
	err = a.ApplySecgroup(ctx, secgroup)
	if err != nil {
		logger.Error("Failed to apply security group", err)
		return
	}
	return
}

func (a *SecruleAdminService) Create(ctx context.Context, name, remoteIp, direction, protocol string, portMin, portMax int32, secgroup *model.SecurityGroup) (secrule *model.SecurityRule, err error) {
	logger.Infof("ENTER SecruleAdmin.Create: name=%s, remoteIp=%s, direction=%s, protocol=%s, portMin=%d, portMax=%d, secgroupID=%d", name, remoteIp, direction, protocol, portMin, portMax, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.Create: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, secgroup.Owner)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	_, err = SecruleAdmin.GetRule(ctx, remoteIp, direction, protocol, portMin, portMax, secgroup)
	if err == nil {
		logger.Errorf("Existing rule %s %s %s %d %d %d for security group %d", remoteIp, direction, protocol, portMin, portMax, secgroup.ID)
		return
	}
	if protocol == "icmp" {
		portMin = -1
		portMax = -1
	}
	secrule = &model.SecurityRule{
		Model:     model.Model{Creater: memberShip.UserID},
		Owner:     memberShip.OrgID,
		Secgroup:  secgroup.ID,
		RemoteIp:  remoteIp,
		Direction: direction,
		IpVersion: "ipv4",
		Protocol:  protocol,
		PortMin:   portMin,
		PortMax:   portMax,
		Name:      name,
	}
	err = db.Create(secrule).Error
	if err != nil {
		logger.Error("DB failed to create security rule", err)
		err = NewCLError(ErrSecurityRuleCreateFailed, "Failed to create security rule", err)
		return
	}
	err = a.ApplySecgroup(ctx, secgroup)
	if err != nil {
		logger.Error("Failed to apply security rule", err)
		return
	}
	return
}

func (a *SecruleAdminService) GetRule(ctx context.Context, remoteIp, direction, protocol string, portMin, portMax int32, secgroup *model.SecurityGroup) (secrule *model.SecurityRule, err error) {
	logger.Infof("ENTER SecruleAdmin.GetRule: remoteIp=%s, direction=%s, protocol=%s, portMin=%d, portMax=%d, secgroupID=%d", remoteIp, direction, protocol, portMin, portMax, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.GetRule: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.GetRule: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgReader, secgroup.Owner)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	secrule = &model.SecurityRule{
		Secgroup:  secgroup.ID,
		RemoteIp:  remoteIp,
		Direction: direction,
		IpVersion: "ipv4",
		Protocol:  protocol,
		PortMin:   portMin,
		PortMax:   portMax,
	}
	err = db.Where(secrule).Take(secrule).Error
	if err != nil {
		logger.Error("Failed to query secrule", err)
		err = NewCLError(ErrSecurityRuleNotFound, "Failed to find security rule", err)
		return
	}
	return
}

func (a *SecruleAdminService) Delete(ctx context.Context, secrule *model.SecurityRule, secgroup *model.SecurityGroup) (err error) {
	logger.Infof("ENTER SecruleAdmin.Delete: secruleID=%d, secgroupID=%d", secrule.ID, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, secrule.Owner)
	if !permit {
		logger.Error("Not authorized to delete the router")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	if err = db.Delete(secrule).Error; err != nil {
		logger.Error("DB failed to delete security rule, %v", err)
		err = NewCLError(ErrSecurityRuleDeleteFailed, "Failed to delete security rule", err)
		return
	}
	err = a.ApplySecgroup(ctx, secgroup)
	if err != nil {
		logger.Error("Failed to apply security rule", err)
		return
	}
	return
}

func (a *SecruleAdminService) List(ctx context.Context, offset, limit int64, order string, secgroup *model.SecurityGroup) (total int64, secrules []*model.SecurityRule, err error) {
	logger.Infof("ENTER SecruleAdmin.List: offset=%d, limit=%d, order=%s, secgroupID=%d", offset, limit, order, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.List: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.List: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgReader, secgroup.Owner)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	query, args := memberShip.GetOrgFilter()
	secrules = []*model.SecurityRule{}
	if err = db.Model(&model.SecurityRule{}).Where("secgroup = ?", secgroup.ID).Where(query, args...).Count(&total).Error; err != nil {
		logger.Error("DB failed to count security rule(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count security rule(s)", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Where("secgroup = ?", secgroup.ID).Where(query, args...).Find(&secrules).Error; err != nil {
		logger.Error("DB failed to query security rule(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query security rule(s)", err)
		return
	}

	return
}

func (a *SecruleAdminService) Get(ctx context.Context, id int64, secgroup *model.SecurityGroup) (secrule *model.SecurityRule, err error) {
	logger.Infof("ENTER SecruleAdmin.Get: id=%d, secgroupID=%d", id, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = fmt.Errorf("Invalid security rule ID: %d", id)
		logger.Error(err)
		return
	}
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	db := DB()
	secrule = &model.SecurityRule{Model: model.Model{ID: id}}
	err = db.Where(query, args...).Take(secrule).Error
	if err != nil {
		logger.Error("Failed to query secrule", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, secrule.Owner)
	if !permit {
		logger.Error("Not authorized to get security group")
		err = fmt.Errorf("Not authorized")
		return
	}
	return
}

func (a *SecruleAdminService) GetSecruleByUUID(ctx context.Context, uuID string, secgroup *model.SecurityGroup) (secrule *model.SecurityRule, err error) {
	logger.Infof("ENTER SecruleAdmin.GetSecruleByUUID: uuID=%s, secgroupID=%d", uuID, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SecruleAdmin.GetSecruleByUUID: error=%v", err)
		} else {
			logger.Info("EXIT SecruleAdmin.GetSecruleByUUID: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	db := DB()
	secrule = &model.SecurityRule{}
	err = db.Where(query, args...).Where("uuid = ? and secgroup = ?", uuID, secgroup.ID).Take(secrule).Error
	if err != nil {
		logger.Error("Failed to query secrule", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, secrule.Owner)
	if !permit {
		logger.Error("Not authorized to get security group")
		err = fmt.Errorf("Not authorized")
		return
	}
	return
}
