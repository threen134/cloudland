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
	routerAdmin = &RouterAdmin{}
)

type StaticRoute struct {
	Destination string `json:"destination"`
	Nexthop     string `json:"nexthop"`
}

type SubnetIface struct {
	Address string         `json:"ip_address"`
	MacAddr string         `json:"mac_address"`
	Vni     int64          `json:"vni"`
	Routes  []*StaticRoute `json:"routes,omitempty"`
}

type RouterAdmin struct{}

func createRouterIface(ctx context.Context, rtype string, router *model.Router, owner int64) (iface *model.Interface, subnet *model.Subnet, err error) {
	ctx, db := GetContextDB(ctx)
	subnets := []*model.Subnet{}
	err = db.Where("type = ?", rtype).Find(&subnets).Error
	if err != nil {
		logger.Error("Failed to query subnets", err)
		err = NewCLError(ErrDatabaseError, "Failed to query subnets", err)
		return
	}
	name := ""
	ifType := ""
	for _, subnet = range subnets {
		switch rtype {
		case "public":
			name = fmt.Sprintf("pub%d", subnet.ID)
			ifType = "gateway_public"
		case "private":
			name = fmt.Sprintf("pub%d", subnet.ID)
			ifType = "gateway_private"
		default:
			continue
		}
		iface, err = CreateInterface(ctx, subnet, router.ID, owner, router.Hyper, 0, 0, "", "", name, ifType, nil, false)
		if err == nil {
			logger.Error("Created gateway interface from subnet")
			break
		}
	}
	return
}

func (a *RouterAdmin) Create(ctx context.Context, name, description string) (router *model.Router, err error) {
	logger.Infof("ENTER RouterAdmin.Create: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.Create: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized to create routers")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create routers", nil)
		return
	}
	owner := memberShip.OrgID
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	router = &model.Router{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, Name: name, Description: description, Status: "available"}
	err = db.Create(router).Error
	if err != nil {
		logger.Error("DB failed to create router ", err)
		err = NewCLError(ErrRouterCreateFailed, "Failed to create router", err)
		return
	}
	secGroup, err := secgroupAdmin.Create(ctx, name+"-native", true, router)
	if err != nil {
		logger.Error("Failed to create security group", err)
		return
	}
	router.DefaultSG = secGroup.ID
	if err = db.Model(router).Update("default_sg", router.DefaultSG).Error; err != nil {
		logger.Error("Failed to save router", err)
		err = NewCLError(ErrRouterUpdateDefaultSGFailed, "Failed to update router default security group", err)
		return
	}
	return
}

func (a *RouterAdmin) Get(ctx context.Context, id int64) (router *model.Router, err error) {
	logger.Infof("ENTER RouterAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.Get: success")
		}
	}()
	if id <= 0 {
		logger.Error("returning nil router")
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	router = &model.Router{Model: model.Model{ID: id}}
	if err = db.Preload("Subnets").Where(where, args...).Take(router).Error; err != nil {
		logger.Error("DB failed to query router", err)
		return nil, NewCLError(ErrRouterNotFound, "Failed to find router", err)
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, router.Owner)
	if !permit {
		logger.Error("Not authorized to read the router")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the router", nil)
		return
	}
	return
}

func (a *RouterAdmin) GetRouterByUUID(ctx context.Context, uuID string) (router *model.Router, err error) {
	logger.Infof("ENTER RouterAdmin.GetRouterByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.GetRouterByUUID: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.GetRouterByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	router = &model.Router{}
	if err = db.Preload("Subnets").Where(where, args...).Where("uuid = ?", uuID).Take(router).Error; err != nil {
		err = NewCLError(ErrRouterNotFound, "Failed to find router", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, router.Owner)
	if !permit {
		logger.Error("Not authorized to read the router")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the router", nil)
		return
	}
	return
}

func (a *RouterAdmin) GetRouterByName(ctx context.Context, name string) (router *model.Router, err error) {
	logger.Infof("ENTER RouterAdmin.GetRouterByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.GetRouterByName: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.GetRouterByName: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	router = &model.Router{}
	if err = db.Preload("Subnets").Where(where, args...).Where("name = ?", name).Take(router).Error; err != nil {
		err = NewCLError(ErrRouterNotFound, "Failed to find router", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, router.Owner)
	if !permit {
		logger.Error("Not authorized to read the router")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the router", nil)
		return
	}
	return
}

func (a *RouterAdmin) GetRouter(ctx context.Context, reference *BaseReference) (router *model.Router, err error) {
	logger.Infof("ENTER RouterAdmin.GetRouter: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.GetRouter: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.GetRouter: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "Router base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		router, err = a.GetRouterByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		router, err = a.GetRouterByName(ctx, reference.Name)
		return
	}
	return
}

func (a *RouterAdmin) Update(ctx context.Context, id int64, name string, description *string, pubID int64) (router *model.Router, err error) {
	logger.Infof("ENTER RouterAdmin.Update: id=%d, name=%s, description=%v, pubID=%d", id, name, description, pubID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.Update: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	router = &model.Router{Model: model.Model{ID: id}}
	if err = db.Find(router).Error; err != nil {
		logger.Error("Failed to query router", err)
		err = NewCLError(ErrRouterNotFound, "Failed to find router", err)
		return
	}
	updates := map[string]interface{}{}
	if router.Name != name {
		router.Name = name
		updates["name"] = name
	}
	if description != nil && router.Description != *description {
		router.Description = *description
		updates["description"] = *description
	}
	if len(updates) > 0 {
		if err = db.Model(router).Updates(updates).Error; err != nil {
			logger.Error("Failed to update router", err)
			err = NewCLError(ErrRouterUpdateFailed, "Failed to update router", err)
			return
		}
	}
	return
}

func (a *RouterAdmin) Delete(ctx context.Context, router *model.Router) (err error) {
	logger.Infof("ENTER RouterAdmin.Delete: routerID=%d, uuid=%s", router.ID, router.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgReader, router.Owner)
	if !permit {
		logger.Error("Not authorized to delete the router")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the router", nil)
		return
	}
	count := 0
	err = db.Model(&model.FloatingIp{}).Where("router_id = ?", router.ID).Count(&count).Error
	if err != nil {
		logger.Error("Failed to count floating ip")
		err = NewCLError(ErrDatabaseError, "Failed to count floating ip in the router", err)
		return
	}
	if count > 0 {
		logger.Error("There are floating ips")
		err = NewCLError(ErrRouterHasFloatingIPs, "There are associated floating ips", nil)
		return
	}
	count = 0
	err = db.Model(&model.Subnet{}).Where("router_id = ? and type <> 'vrrp'", router.ID).Count(&count).Error
	if err != nil {
		logger.Error("Failed to count subnet")
		err = NewCLError(ErrDatabaseError, "Failed to count subnet in the router", err)
		return
	}
	if count > 0 {
		logger.Error("There are associated subnets")
		err = NewCLError(ErrRouterHasSubnets, "There are associated subnets", nil)
		return
	}
	err = db.Model(&model.Portmap{}).Where("router_id = ?", router.ID).Count(&count).Error
	if err != nil {
		logger.Error("Failed to count portmap")
		err = NewCLError(ErrDatabaseError, "Failed to count portmap in the router", err)
		return
	}
	if count > 0 {
		logger.Error("There are associated portmaps")
		err = NewCLError(ErrRouterHasPortmaps, "There are associated portmaps", nil)
		return
	}
	err = db.Model(&model.LoadBalancer{}).Where("router_id = ?", router.ID).Count(&count).Error
	if err != nil {
		logger.Error("Failed to count load balancer")
		err = NewCLError(ErrDatabaseError, "Failed to count load balancer in the router", err)
		return
	}
	if count > 0 {
		logger.Error("There are associated load balancers")
		err = NewCLError(ErrRouterHasPortmaps, "There are associated load balancers", nil)
		return
	}
	control := "toall="
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_local_router.sh '%d'", router.ID)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Error("Delete master failed")
		return
	}
	if router.VrrpSubnetID > 0 {
		var vrrpSubnet *model.Subnet
		vrrpSubnet, err = subnetAdmin.Get(ctx, router.VrrpSubnetID)
		if err != nil {
			logger.Error("Failed to get vrrp subnet", err)
			return
		}
		err = subnetAdmin.Delete(ctx, vrrpSubnet)
		if err != nil {
			logger.Error("Failed to list floating ips", err)
			return
		}
	}
	router.Name = fmt.Sprintf("%s-%d", router.Name, router.CreatedAt.Unix())
	err = db.Model(router).Update("name", router.Name).Error
	if err != nil {
		logger.Error("DB failed to update router name", err)
		err = NewCLError(ErrRouterUpdateFailed, "Failed to update router name", err)
		return
	}
	if err = db.Delete(router).Error; err != nil {
		logger.Error("DB failed to delete router", err)
		err = NewCLError(ErrRouterDeleteFailed, "Failed to delete router", err)
		return
	}
	secgroups := []*model.SecurityGroup{}
	err = db.Where("router_id = ?", router.ID).Find(&secgroups).Error
	if err != nil {
		logger.Error("DB failed to query security groups", err)
		err = NewCLError(ErrDatabaseError, "Failed to query security groups in the router", err)
		return
	}
	for _, sg := range secgroups {
		err = secgroupAdmin.Delete(ctx, sg)
		if err != nil {
			logger.Error("Can not delete security group", err)
			err = NewCLError(ErrSecurityGroupDeleteFailed, "Failed to delete security group", err)
			return
		}
	}
	return
}

func (a *RouterAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, routers []*model.Router, err error) {
	logger.Infof("ENTER RouterAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT RouterAdmin.List: error=%v", err)
		} else {
			logger.Info("EXIT RouterAdmin.List: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgReader)
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

	if query != "" {
		query = fmt.Sprintf("name like '%%%s%%'", query)
	}
	queryBuilder, args := memberShip.GetOrgFilter()
	routers = []*model.Router{}
	if err = db.Model(&model.Router{}).Where(queryBuilder, args...).Where(query).Count(&total).Error; err != nil {
		logger.Error("DB failed to count router(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count router(s)", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Where(queryBuilder, args...).Where(query).Find(&routers).Error; err != nil {
		logger.Error("DB failed to query routers, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query routers", err)
		return
	}
	permit = memberShip.IsSystemAdmin()
	if permit {
		db = db.Offset(0).Limit(-1)
		for _, router := range routers {
			router.OwnerInfo = &model.Organization{Model: model.Model{ID: router.Owner}}
			if err = db.Take(router.OwnerInfo).Error; err != nil {
				logger.Error("Failed to query owner info", err)
				err = NewCLError(ErrOwnerNotFound, "Failed to query owner info", err)
				return
			}
		}
	}
	return
}
