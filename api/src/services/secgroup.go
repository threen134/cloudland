/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

var (
	secgroupAdmin = &SecgroupAdmin{}
)

type SecgroupAdmin struct{}

func (a *SecgroupAdmin) Switch(ctx context.Context, newSg *model.SecurityGroup, router *model.Router) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.Switch: newSgID=%d, routerID=%v", newSg.ID, router)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.Switch: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.Switch: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	oldSg := &model.SecurityGroup{}
	if router != nil {
		oldSg.ID = router.DefaultSG
		err = db.Take(oldSg).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query default security group", err)
			err = NewCLError(ErrSecurityGroupNotFound, "Failed to find default security group", err)
			return
		}
		router.DefaultSG = newSg.ID
		err = db.Model(router).Update("default_sg", router.DefaultSG).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to save router", err)
			err = NewCLError(ErrRouterUpdateFailed, "Failed to update router default security group", err)
			return
		}
	} else {
		memberShip := GetMemberShip(ctx)
		var org *model.Organization
		org, err = orgAdmin.Get(ctx, memberShip.OrgID)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query organization ", err)
			err = NewCLError(ErrOrgNotFound, "Failed to find organization", err)
			return
		}
		if org.DefaultSG > 0 {
			oldSg.ID = org.DefaultSG
			err = db.Take(oldSg).Error
			if err != nil {
				logger.Ctx(ctx).Error("Failed to query default security group", err)
				return
			}
		}
		org.DefaultSG = newSg.ID
		err = db.Model(org).Update("default_sg", org.DefaultSG).Error
		if err != nil {
			logger.Ctx(ctx).Error("DB failed to update org default sg", err)
			err = NewCLError(ErrOrgUpdateFailed, "Failed to update organization default security group", err)
			return
		}
	}
	if oldSg.ID > 0 {
		oldSg.IsDefault = false
		err = db.Model(oldSg).Update("is_default", oldSg.IsDefault).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to save new security group", err)
			err = NewCLError(ErrSecurityGroupUpdateFailed, "Failed to update security group", err)
			return
		}
	}
	newSg.IsDefault = true
	err = db.Model(newSg).Update("is_default", newSg.IsDefault).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to save new security group", err)
		err = NewCLError(ErrSecurityGroupUpdateFailed, "Failed to update security group", err)
		return
	}
	return
}

// 三个字段都是指针：nil 表示本次不改。description 传了空串就是要清空（原先 `!= ""`
// 的写法让「清空描述」静默失败，接口还回 200）
func (a *SecgroupAdmin) Update(ctx context.Context, secgroup *model.SecurityGroup, name, description *string, isDefault *bool) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.Update: secgroupID=%d", secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.Update: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if name != nil && *name != "" {
		secgroup.Name = *name
	}
	if description != nil {
		secgroup.Description = *description
	}
	if isDefault != nil && *isDefault && !secgroup.IsDefault {
		secgroup.IsDefault = true
		a.Switch(ctx, secgroup, secgroup.Router)
	}
	err = db.Model(&model.SecurityGroup{}).Where("id = ?", secgroup.ID).Updates(map[string]interface{}{"name": secgroup.Name, "description": secgroup.Description, "is_default": secgroup.IsDefault}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to save security group", err)
		err = NewCLError(ErrSecurityGroupUpdateFailed, "Failed to update security group", err)
		return
	}
	return
}

func (a *SecgroupAdmin) Get(ctx context.Context, id int64) (secgroup *model.SecurityGroup, err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.Get: success")
		}
	}()
	if id <= 0 {
		return a.GetSecgroupByName(ctx, SystemDefaultSGName)
	}
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	query, args := memberShip.GetOrgFilter()
	secgroup = &model.SecurityGroup{Model: model.Model{ID: id}}
	err = db.Where(query, args...).Take(secgroup).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to query secgroup ", err)
		err = NewCLError(ErrSecurityGroupNotFound, "Failed to find security group", err)
		return
	}
	if secgroup.RouterID > 0 {
		secgroup.Router = &model.Router{Model: model.Model{ID: secgroup.RouterID}}
		err = db.Take(secgroup.Router).Error
		if err != nil {
			logger.Ctx(ctx).Error("DB failed to query router", err)
			err = NewCLError(ErrRouterNotFound, "Failed to find router", err)
			return
		}
	}
	if secgroup.Name != "system-default" {
		permit := memberShip.CheckResourceOrg(model.OrgReader, secgroup.Owner)
		if !permit {
			logger.Ctx(ctx).Error("Not authorized to get security group")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get security group", nil)
			return
		}
	}
	return
}

func (a *SecgroupAdmin) GetSecgroupByUUID(ctx context.Context, uuID string) (secgroup *model.SecurityGroup, err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.GetSecgroupByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.GetSecgroupByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.GetSecgroupByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	secgroup = &model.SecurityGroup{}
	err = db.Where(query, args...).Where("uuid = ?", uuID).Take(secgroup).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query secgroup ", err)
		err = NewCLError(ErrSecurityGroupNotFound, "Failed to find security group", err)
		return
	}
	if secgroup.RouterID > 0 {
		secgroup.Router = &model.Router{Model: model.Model{ID: secgroup.RouterID}}
		err = db.Take(secgroup.Router).Error
		if err != nil {
			logger.Ctx(ctx).Error("DB failed to query router", err)
			err = NewCLError(ErrRouterNotFound, "Failed to find router", err)
			return
		}
	}
	if secgroup.Name != "system-default" {
		permit := memberShip.CheckResourceOrg(model.OrgReader, secgroup.Owner)
		if !permit {
			logger.Ctx(ctx).Error("Not authorized to get security group")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get security group", nil)
			return
		}
	}
	return
}

func (a *SecgroupAdmin) GetDefaultSecgroup(ctx context.Context) (secgroup *model.SecurityGroup, err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.GetDefaultSecgroup")
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.GetDefaultSecgroup: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.GetDefaultSecgroup: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	org, err := orgAdmin.Get(ctx, memberShip.OrgID)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query organization ", err)
		return
	}
	if org.DefaultSG == 0 {
		timestamp := time.Now().UnixNano()
		secgroupName := fmt.Sprintf("default-%d", timestamp)
		secgroup, err = a.Create(ctx, secgroupName, "", true, true, nil)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create account secgroup ", err)
			return
		}
		org.DefaultSG = secgroup.ID
		err = db.Model(org).Update("default_sg", org.DefaultSG).Error
		if err != nil {
			logger.Ctx(ctx).Error("DB failed to update org default sg", err)
			err = NewCLError(ErrOrgUpdateFailed, "Failed to update organization default security group", err)
			return
		}
	} else {
		secgroup = &model.SecurityGroup{Model: model.Model{ID: org.DefaultSG}}
		err = db.Model(secgroup).Take(secgroup).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query account secgroup ", err)
			err = NewCLError(ErrSecurityGroupNotFound, "Failed to find security group", err)
			return
		}
	}
	return
}

func (a *SecgroupAdmin) GetSecgroupByName(ctx context.Context, name string) (secgroup *model.SecurityGroup, err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.GetSecgroupByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.GetSecgroupByName: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.GetSecgroupByName: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	secgroup = &model.SecurityGroup{}
	err = db.Where("name = ?", name).Take(secgroup).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query secgroup ", err)
		err = NewCLError(ErrSecurityGroupNotFound, "Failed to find security group", err)
		return
	}
	if secgroup.RouterID > 0 {
		secgroup.Router = &model.Router{Model: model.Model{ID: secgroup.RouterID}}
		err = db.Take(secgroup.Router).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query router ", err)
			err = NewCLError(ErrRouterNotFound, "Failed to find router", err)
			return
		}
	}
	if secgroup.Name != "system-default" {
		permit := memberShip.CheckResourceOrg(model.OrgReader, secgroup.Owner)
		if !permit {
			logger.Ctx(ctx).Error("Not authorized to get security group")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get security group", nil)
			return
		}
	}
	return
}

func (a *SecgroupAdmin) GetSecurityGroup(ctx context.Context, reference *BaseReference) (secgroup *model.SecurityGroup, err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.GetSecurityGroup: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.GetSecurityGroup: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.GetSecurityGroup: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = fmt.Errorf("Security group base reference must be provided with either uuid or name")
		return
	}
	if reference.ID != "" {
		secgroup, err = a.GetSecgroupByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		secgroup, err = a.GetSecgroupByName(ctx, reference.Name)
		return
	}
	return
}

func (a *SecgroupAdmin) GetSecgroupInterfaces(ctx context.Context, secgroup *model.SecurityGroup) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.GetSecgroupInterfaces: secgroupID=%d", secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.GetSecgroupInterfaces: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.GetSecgroupInterfaces: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	err = db.Model(secgroup).Association("Interfaces").Find(&secgroup.Interfaces)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query secgroup, %v", err)
		err = NewCLError(ErrSecurityGroupNotFound, "Failed to find security group", err)
		return
	}
	if len(secgroup.Interfaces) > 0 {
		// Filter interfaces with instance > 0 and preload associations
		var ifaceIDs []int64
		for _, iface := range secgroup.Interfaces {
			if iface.Instance > 0 {
				ifaceIDs = append(ifaceIDs, iface.ID)
			}
		}
		secgroup.Interfaces = nil
		if len(ifaceIDs) > 0 {
			err = db.Where("id IN ?", ifaceIDs).Preload("Address").Preload("Address.Subnet").Preload("SecondAddresses").Preload("SecondAddresses.Subnet").Preload("SiteSubnets").Find(&secgroup.Interfaces).Error
			if err != nil {
				logger.Ctx(ctx).Error("Failed to preload interfaces, %v", err)
				err = NewCLError(ErrSecurityGroupNotFound, "Failed to preload interfaces", err)
				return
			}
		}
	}
	return
}

func (a *SecgroupAdmin) GetInterfaceSecgroups(ctx context.Context, iface *model.Interface) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.GetInterfaceSecgroups: ifaceID=%d", iface.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.GetInterfaceSecgroups: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.GetInterfaceSecgroups: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	err = db.Model(iface).Association("SecurityGroups").Find(&iface.SecurityGroups)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query interface, %v", err)
		err = NewCLError(ErrInterfaceNotFound, "Failed to find interface", err)
		return
	}
	return
}

// Create 创建安全组并预置默认规则。withLoginRules 为 true 时额外预置对全网开放的 SSH/RDP，
// 只用于系统自动创建的默认组（系统默认组、组织默认组、VPC native 组）；
// 用户通过 API 创建的组无论 is_default 取值都传 false，避免勾选“默认”就对公网暴露登录端口
func (a *SecgroupAdmin) Create(ctx context.Context, name, description string, isDefault, withLoginRules bool, router *model.Router) (secgroup *model.SecurityGroup, err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.Create: name=%s, description=%s, isDefault=%t, withLoginRules=%t, routerID=%v", name, description, isDefault, withLoginRules, router)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	owner := memberShip.OrgID
	var routerID int64
	if router != nil {
		permit := memberShip.CheckResourceOrg(model.OrgWriter, router.Owner)
		if !permit {
			logger.Ctx(ctx).Error("Not authorized for this operation")
			err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
			return
		}
		routerID = router.ID
	} else {
		permit := memberShip.CheckOrgPermission(model.OrgAdmin)
		if !permit {
			logger.Ctx(ctx).Error("Not authorized for this operation")
			err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
			return
		}
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	secgroup = &model.SecurityGroup{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, Name: name, Description: description, IsDefault: isDefault, RouterID: routerID}
	err = db.Create(secgroup).Error
	if err != nil {
		logger.Ctx(ctx).Errorf("DB failed to create security group %s, %v", name, err)
		err = NewCLError(ErrSecurityGroupCreateFailed, "Failed to create security group", err)
		return
	}
	_, err = (&SecruleAdminService{}).Create(ctx, "default-egress-tcp", "0.0.0.0/0", "egress", "tcp", 1, 65535, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create security rule", err)
		return
	}
	_, err = (&SecruleAdminService{}).Create(ctx, "default-egress-udp", "0.0.0.0/0", "egress", "udp", 1, 65535, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create security rule", err)
		return
	}
	// 对全网开放 SSH/RDP 只预置在系统自动创建的默认组里方便登录（见函数注释）
	if withLoginRules {
		_, err = (&SecruleAdminService{}).Create(ctx, "default-ingress-ssh", "0.0.0.0/0", "ingress", "tcp", 22, 22, secgroup)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create security rule", err)
			return
		}
		_, err = (&SecruleAdminService{}).Create(ctx, "default-ingress-rdp", "0.0.0.0/0", "ingress", "tcp", 3389, 3389, secgroup)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create security rule", err)
			return
		}
	}
	_, err = (&SecruleAdminService{}).Create(ctx, "default-ingress-dhcp", "0.0.0.0/0", "ingress", "udp", 68, 68, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create security rule", err)
		return
	}
	_, err = (&SecruleAdminService{}).Create(ctx, "default-egress-icmp", "0.0.0.0/0", "egress", "icmp", -1, -1, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create security rule", err)
		return
	}
	_, err = (&SecruleAdminService{}).Create(ctx, "default-ingress-icmp", "0.0.0.0/0", "ingress", "icmp", -1, -1, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create security rule", err)
		return
	}
	if router != nil {
		var subnets []*model.Subnet
		err = db.Where("router_id = ?", router.ID).Find(&subnets).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create security rule", err)
			err = NewCLError(ErrSubnetNotFound, "Failed to find subnets for the router", err)
			return
		}
		for _, subnet := range subnets {
			_, err = (&SecruleAdminService{}).Create(ctx, fmt.Sprintf("subnet-%s-ingress-tcp", strings.ReplaceAll(subnet.Network, "/", "-")), subnet.Network, "ingress", "tcp", 1, 65535, secgroup)
			if err != nil {
				logger.Ctx(ctx).Error("Failed to create security rule", err)
				return
			}
			_, err = (&SecruleAdminService{}).Create(ctx, fmt.Sprintf("subnet-%s-ingress-udp", strings.ReplaceAll(subnet.Network, "/", "-")), subnet.Network, "ingress", "udp", 1, 65535, secgroup)
			if err != nil {
				logger.Ctx(ctx).Error("Failed to create security rule", err)
				return
			}
		}
	}
	if isDefault {
		err = a.Switch(ctx, secgroup, router)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to set default security group", err)
			return
		}
	}
	return
}

func (a *SecgroupAdmin) Delete(ctx context.Context, secgroup *model.SecurityGroup) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.Delete: secgroupID=%d, uuid=%s", secgroup.ID, secgroup.UUID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, secgroup.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to delete the security group")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the security group", nil)
		return
	}
	if secgroup.IsDefault == true && secgroup.Name != SystemDefaultSGName {
		if secgroup.RouterID > 0 {
			router := &model.Router{Model: model.Model{ID: secgroup.RouterID}}
			err = db.Where("default_sg = ?", secgroup.ID).Take(&router).Error
			if err == nil {
				logger.Ctx(ctx).Error("Default security group can not be deleted", err)
				err = NewCLError(ErrCannotDeleteDefaultSG, "Default security group can not be deleted", err)
				return
			}
		} else {
			_, err = orgAdmin.Get(ctx, secgroup.Owner)
			if err == nil {
				logger.Ctx(ctx).Error("Default security group can not be deleted", err)
				err = NewCLError(ErrCannotDeleteDefaultSG, "Default security group can not be deleted", err)
				return
			}
		}
	}
	err = db.Model(secgroup).Association("Interfaces").Find(&secgroup.Interfaces)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to count the number of interfaces using the security group", err)
		err = NewCLError(ErrDatabaseError, "Failed to count the number of interfaces using the security group", err)
		return
	}
	if len(secgroup.Interfaces) > 0 {
		logger.Ctx(ctx).Error("Security group can not be deleted if there are associated interfaces")
		err = NewCLError(ErrSGHasInterfaces, "The security group can not be deleted if there are associated interfaces", err)
		return
	}
	err = db.Where("secgroup = ?", secgroup.ID).Delete(&model.SecurityRule{}).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to delete security group rules", err)
		err = NewCLError(ErrSecurityRuleDeleteFailed, "Failed to delete security group rules", err)
		return
	}
	if err = db.Delete(secgroup).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to delete security group", err)
		err = NewCLError(ErrSecurityGroupDeleteFailed, "Failed to delete security group", err)
		return
	}
	secgroup.Name = fmt.Sprintf("%s-%d", secgroup.Name, secgroup.CreatedAt.Unix())
	err = db.Model(&model.SecurityGroup{}).Unscoped().Where("id = ?", secgroup.ID).Update("name", secgroup.Name).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to update security group name", err)
		err = NewCLError(ErrSecurityGroupUpdateFailed, "Failed to update security group name", err)
		return
	}
	return
}

func (a *SecgroupAdmin) List(ctx context.Context, offset, limit int64, order, name string, routerID int64) (total int64, secgroups []*model.SecurityGroup, err error) {
	logger.Ctx(ctx).Infof("ENTER SecgroupAdmin.List: offset=%d, limit=%d, order=%s, name=%s, routerID=%d", offset, limit, order, name, routerID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecgroupAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecgroupAdmin.List: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgReader)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
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
	queryBuilder, args := memberShip.GetOrgFilter()
	// Filters are bound as parameters: the name comes straight from the request
	filter := func(tx *gorm.DB) *gorm.DB {
		tx = dbs.Contains(name, "name")(tx.Where(queryBuilder, args...))
		if routerID > 0 {
			tx = tx.Where("router_id = ?", routerID)
		}
		return tx
	}
	secgroups = []*model.SecurityGroup{}
	if err = db.Model(&model.SecurityGroup{}).Scopes(filter).Count(&total).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to count security group(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count security group(s)", err)
		return
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Scopes(filter).Find(&secgroups).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to query security group(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query security group(s)", err)
		return
	}
	// db still carries the list statement (security_groups, offset, limit): reusing it filled Router with
	// another security group row, so the VPC shown in the list was a security group's name and UUID
	_, db = GetContextDB(ctx)
	for _, secgroup := range secgroups {
		if secgroup.RouterID > 0 {
			secgroup.Router = &model.Router{Model: model.Model{ID: secgroup.RouterID}}
			err = db.Take(secgroup.Router).Error
			if err != nil {
				logger.Ctx(ctx).Error("DB failed to query router", err)
				err = nil
				continue
			}
		}
	}
	if memberShip.IsSystemAdmin() {
		_, db = GetContextDB(ctx) // 链式调用复用 Statement，取新会话查询 OwnerInfo
		for _, sg := range secgroups {
			sg.OwnerInfo = &model.Organization{Model: model.Model{ID: sg.Owner}}
			if err = db.Take(sg.OwnerInfo).Error; err != nil {
				logger.Ctx(ctx).Error("Failed to query owner info", err)
				err = nil
				continue
			}
		}
	}

	return
}
