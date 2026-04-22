/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var (
	loadBalancerAdmin = &LoadBalancerAdmin{}
)

type LoadBalancerAdmin struct{}

func GetLBFloatingIpJson(ctx context.Context, loadBalancer *model.LoadBalancer) (jsonData []byte, err error) {
	logger.Infof("ENTER GetLBFloatingIpJson: lbID=%d", loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT GetLBFloatingIpJson: error=%v", err)
		} else {
			logger.Info("EXIT GetLBFloatingIpJson: success")
		}
	}()
	intQuery := fmt.Sprintf("load_balancer_id = %d", loadBalancer.ID)
	_, floatingIps, err := (&FloatingIpAdminService{}).List(ctx, 0, -1, "", "", intQuery)
	if err != nil {
		logger.Error("Failed to list floating ips", err)
		err = NewCLError(ErrFIPListFailed, "Failed to list floating ips", err)
		return
	}
	lbFloatingIpCfg := &LoadBalancerFloatingIpConfig{}
	for _, fip := range floatingIps {
		lbFloatingIpCfg.FloatingIps = append(lbFloatingIpCfg.FloatingIps, &LoadBalancerFloatingIp{
			Address:  fip.FipAddress,
			Vlan:     fip.Subnet.Vlan,
			Gateway:  fip.Subnet.Gateway,
			MarkID:   fip.ID,
			Inbound:  fip.Inbound,
			Outbound: fip.Outbound,
		})
	}
	for _, listener := range loadBalancer.Listeners {
		lbFloatingIpCfg.Ports = append(lbFloatingIpCfg.Ports, listener.Port)
	}
	jsonData, err = json.Marshal(lbFloatingIpCfg)
	if err != nil {
		logger.Errorf("Failed to marshal load balancer floating ip json data, %v", err)
		return
	}
	return
}

func CreateVrrpConf(ctx context.Context, loadBalancer *model.LoadBalancer) (err error) {
	logger.Infof("ENTER CreateVrrpConf: lbID=%d", loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT CreateVrrpConf: error=%v", err)
		} else {
			logger.Info("EXIT CreateVrrpConf: success")
		}
	}()
	if loadBalancer == nil || (loadBalancer.Status != "available") {
		logger.Error("Load balancer is not available")
		return
	}
	routerID := loadBalancer.RouterID
	vrrpID := loadBalancer.VrrpInstanceID
	vrrpVlan := loadBalancer.VrrpInstance.VrrpSubnet.Vlan
	jsonData, err := GetLBFloatingIpJson(ctx, loadBalancer)
	if err != nil {
		logger.Errorf("Failed to get load balancer floating ip json data, %v", err)
		return
	}
	vrrpIface1, vrrpIface2, err := GetVrrpInterfaces(ctx, loadBalancer.VrrpInstance.ID)
	if err != nil {
		logger.Error("No valid hypervisor", err)
		return
	}
	if vrrpIface1.Hyper >= 0 {
		control := fmt.Sprintf("inter=%d", vrrpIface1.Hyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_keepalived_conf.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s' 'MASTER'<<EOF\n%s\nEOF", routerID, vrrpID, vrrpVlan, vrrpIface1.Address.Address, vrrpIface1.MacAddr, vrrpIface2.Address.Address, vrrpIface2.MacAddr, jsonData)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Execute MASTER keepalived conf failed", err)
			return
		}
	}
	if vrrpIface2.Hyper >= 0 {
		control := fmt.Sprintf("inter=%d", vrrpIface2.Hyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_keepalived_conf.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s' 'BACKUP'<<EOF\n%s\nEOF", routerID, vrrpID, vrrpVlan, vrrpIface2.Address.Address, vrrpIface2.MacAddr, vrrpIface1.Address.Address, vrrpIface1.MacAddr, jsonData)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Execute BACKUP create keepalived conf failed", err)
			return
		}
	}
	return
}

func CreateVrrpInstance(ctx context.Context, name string, router *model.Router, zone *model.Zone) (vrrpInstance *model.VrrpInstance, err error) {
	logger.Infof("ENTER CreateVrrpInstance: name=%s, routerID=%d", name, router.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT CreateVrrpInstance: error=%v", err)
		} else {
			logger.Infof("EXIT CreateVrrpInstance: success, vrrpInstanceID=%d", vrrpInstance.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	name = fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
	var vrrpSubnet *model.Subnet
	if router.VrrpSubnetID > 0 {
		vrrpSubnet, err = subnetAdmin.Get(ctx, router.VrrpSubnetID)
		if err != nil {
			logger.Error("Failed to create vrrp subnet")
			return
		}
	} else {
		vrrpSubnet, err = subnetAdmin.Create(ctx, 0, name, "192.168.196.0/24", "", "", "", "vrrp", "", "", false, router, nil, 0)
		if err != nil {
			logger.Error("Failed to create vrrp subnet")
			return
		}
		router.VrrpSubnetID = vrrpSubnet.ID
		err = db.Model(router).Updates(map[string]interface{}{"vrrp_subnet_id": vrrpSubnet.ID}).Error
		if err != nil {
			logger.Error("DB failed to update router vrrp subnet", err)
			return
		}
	}
	memberShip := GetMemberShip(ctx)
	zoneID := int64(0)
	if zone != nil {
		zoneID = zone.ID
	}
	vrrpInstance = &model.VrrpInstance{Model: model.Model{Creater: memberShip.UserID}, Owner: memberShip.OrgID, VrrpSubnetID: vrrpSubnet.ID, ZoneID: zoneID, RouterID: router.ID}
	err = db.Create(vrrpInstance).Error
	if err != nil {
		logger.Error("DB failed to create vrrp instance ", err)
		return
	}
	vrrpIface1, err := CreateInterface(ctx, vrrpSubnet, vrrpInstance.ID, memberShip.OrgID, -1, 0, 0, "", "", "MASTER", "vrrp", nil, false)
	if err != nil {
		logger.Error("Failed to create vrrp interface 1", err)
		return
	}
	vrrpIface2, err := CreateInterface(ctx, vrrpSubnet, vrrpInstance.ID, memberShip.OrgID, -1, 0, 0, "", "", "BACKUP", "vrrp", nil, false)
	if err != nil {
		logger.Error("Failed to create vrrp interface 1", err)
		return
	}
	control := "inter="
	hyperGroup := ""
	if zone != nil {
		hyperGroup, err = GetHyperGroup(ctx, zone.ID, -1)
		if err != nil {
			logger.Error("Failed to get hyper group", err)
			return
		}
		control = "select=" + hyperGroup
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/set_vrrp_ip.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s' 'MASTER' 'true'", router.ID, vrrpInstance.ID, vrrpSubnet.Vlan, vrrpIface1.MacAddr, vrrpIface1.Address.Address, vrrpIface2.MacAddr, vrrpIface2.Address.Address)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Error("Set vrrp ip command execution failed ", err)
		return
	}
	return
}

func (a *LoadBalancerAdmin) Create(ctx context.Context, name, description string, router *model.Router, zone *model.Zone) (loadBalancer *model.LoadBalancer, err error) {
	logger.Infof("ENTER LoadBalancerAdmin.Create: name=%s, routerID=%d", name, router.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT LoadBalancerAdmin.Create: success, lbID=%d", loadBalancer.ID)
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
	vrrpInstance, err := CreateVrrpInstance(ctx, name, router, zone)
	if err != nil {
		logger.Error("Failed to create vrrp instance", err)
		err = NewCLError(ErrVrrpInstanceCreateFailed, "Failed to create vrrp instance", err)
		return
	}
	loadBalancer = &model.LoadBalancer{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, Name: name, Description: description, RouterID: router.ID, VrrpInstanceID: vrrpInstance.ID, Status: "pending"}
	err = db.Create(loadBalancer).Error
	if err != nil {
		logger.Error("DB failed to create load balancer ", err)
		err = NewCLError(ErrLoadBalancerCreateFailed, "Failed to create load balancer", err)
		return
	}
	return
}

func (a *LoadBalancerAdmin) Get(ctx context.Context, id int64) (loadBalancer *model.LoadBalancer, err error) {
	logger.Infof("ENTER LoadBalancerAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT LoadBalancerAdmin.Get: success")
		}
	}()
	if id <= 0 {
		logger.Error("returning nil router")
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	loadBalancer = &model.LoadBalancer{Model: model.Model{ID: id}}
	err = db.Preload("FloatingIps").Preload("Router").Preload("VrrpInstance").Preload("VrrpInstance.VrrpSubnet").Preload("Listeners").Preload("Listeners.Backends").Where(where, args...).Take(loadBalancer).Error
	if err != nil {
		logger.Error("Failed to query load balancer", err)
		err = NewCLError(ErrLoadBalancerNotFound, "Failed to find load balancer", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, loadBalancer.Owner)
	if !permit {
		logger.Error("Not authorized to read the load balancer")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the load balancer", nil)
		return
	}
	return
}

func (a *LoadBalancerAdmin) GetLoadBalancerByUUID(ctx context.Context, uuID string) (loadBalancer *model.LoadBalancer, err error) {
	logger.Infof("ENTER LoadBalancerAdmin.GetLoadBalancerByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.GetLoadBalancerByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT LoadBalancerAdmin.GetLoadBalancerByUUID: success, lbID=%d", loadBalancer.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	loadBalancer = &model.LoadBalancer{}
	err = db.Preload("FloatingIps").Preload("Router").Preload("VrrpInstance").Preload("VrrpInstance.VrrpSubnet").Preload("Listeners").Preload("Listeners.Backends").Where(where, args...).Where("uuid = ?", uuID).Take(loadBalancer).Error
	if err != nil {
		logger.Error("Failed to query load balancer, %v", err)
		err = NewCLError(ErrRouterNotFound, "Failed to find load balancer", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, loadBalancer.Owner)
	if !permit {
		logger.Error("Not authorized to read the load balancer")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the load balancer", nil)
		return
	}
	return
}

func (a *LoadBalancerAdmin) GetLoadBalancerByName(ctx context.Context, name string) (loadBalancer *model.LoadBalancer, err error) {
	logger.Infof("ENTER LoadBalancerAdmin.GetLoadBalancerByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.GetLoadBalancerByName: error=%v", err)
		} else {
			logger.Infof("EXIT LoadBalancerAdmin.GetLoadBalancerByName: success, lbID=%d", loadBalancer.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	loadBalancer = &model.LoadBalancer{}
	err = db.Preload("Router").Where(where, args...).Where("name = ?", name).Take(loadBalancer).Error
	if err != nil {
		logger.Error("Failed to query load balancer, %v", err)
		err = NewCLError(ErrRouterNotFound, "Failed to find load balancer", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, loadBalancer.Owner)
	if !permit {
		logger.Error("Not authorized to read the load balancer")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the load balancer", nil)
		return
	}
	return
}

func (a *LoadBalancerAdmin) GetLoadBalancer(ctx context.Context, reference *BaseReference) (loadBalancer *model.LoadBalancer, err error) {
	logger.Infof("ENTER LoadBalancerAdmin.GetLoadBalancer: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.GetLoadBalancer: error=%v", err)
		} else {
			logger.Info("EXIT LoadBalancerAdmin.GetLoadBalancer: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "Router base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		loadBalancer, err = a.GetLoadBalancerByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		loadBalancer, err = a.GetLoadBalancerByName(ctx, reference.Name)
		return
	}
	return
}

func (a *LoadBalancerAdmin) Update(ctx context.Context, loadBalancer *model.LoadBalancer, name, description string) (lb *model.LoadBalancer, err error) {
	logger.Infof("ENTER LoadBalancerAdmin.Update: lbID=%d, name=%s", loadBalancer.ID, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT LoadBalancerAdmin.Update: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	updates := map[string]interface{}{}
	if loadBalancer.Name != name {
		loadBalancer.Name = name
		updates["name"] = name
	}
	if loadBalancer.Description != description {
		loadBalancer.Description = description
		updates["description"] = description
	}
	if len(updates) > 0 {
		if err = db.Model(loadBalancer).Updates(updates).Error; err != nil {
			logger.Error("Failed to save load balancer", err)
			err = NewCLError(ErrRouterUpdateFailed, "Failed to update load balancer", err)
			return
		}
	}
	lb = loadBalancer
	return
}

func (a *LoadBalancerAdmin) Delete(ctx context.Context, loadBalancer *model.LoadBalancer) (err error) {
	logger.Infof("ENTER LoadBalancerAdmin.Delete: lbID=%d, name=%s", loadBalancer.ID, loadBalancer.Name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT LoadBalancerAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, loadBalancer.Owner)
	if !permit {
		logger.Error("Not authorized to delete the load balancer")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the router", nil)
		return
	}
	intQuery := fmt.Sprintf("load_balancer_id = %d", loadBalancer.ID)
	_, floatingIps, err := (&FloatingIpAdminService{}).List(ctx, 0, -1, "", "", intQuery)
	if err != nil {
		logger.Error("Failed to list floating ips", err)
		err = NewCLError(ErrFIPListFailed, "Failed to list floating ips", err)
		return
	}
	for _, floatingIp := range floatingIps {
		err = (&FloatingIpAdminService{}).Delete(ctx, floatingIp)
		if err != nil {
			logger.Error("Failed to delete floating ip", err)
			err = NewCLError(ErrFIPListFailed, "Failed to delete floating ip", err)
			return
		}
	}
	loadBalancer.FloatingIps = nil
	_, listeners, err := listenerAdmin.List(ctx, 0, -1, "", loadBalancer)
	if err != nil {
		logger.Error("Failed to list listeners", err)
		err = NewCLError(ErrListenerListFailed, "Failed to list listeners", err)
		return
	}
	for _, listener := range listeners {
		err = listenerAdmin.Delete(ctx, listener, loadBalancer)
		if err != nil {
			logger.Error("Failed to delete listener", err)
			err = NewCLError(ErrListenerDeleteFailed, "Failed to delete listener", err)
			return
		}
	}
	loadBalancer.Listeners = nil
	loadBalancer.Name = fmt.Sprintf("%s-%d", loadBalancer.Name, loadBalancer.CreatedAt.Unix())
	err = db.Model(&model.LoadBalancer{Model: model.Model{ID: loadBalancer.ID}}).Update("name", loadBalancer.Name).Error
	if err != nil {
		logger.Error("DB failed to update loadBalancer name", err)
		err = NewCLError(ErrLoadBalancerUpdateFailed, "Failed to update loadBalancer name", err)
		return
	}
	vrrpInstance := loadBalancer.VrrpInstance
	vrrpSubnet := vrrpInstance.VrrpSubnet
	routerID := loadBalancer.RouterID
	vrrpIface1, vrrpIface2, err := GetVrrpInterfaces(ctx, vrrpInstance.ID)
	if err != nil {
		logger.Error("Failed to get vrrp interfaces", err)
		err = NewCLError(ErrInterfaceDeleteFailed, "Failed to delete vrrp interface 2", err)
		return
	}
	if vrrpIface1.Hyper >= 0 {
		control := fmt.Sprintf("inter=%d", vrrpIface1.Hyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_vrrp_ip.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s'", routerID, vrrpInstance.ID, vrrpSubnet.Vlan, vrrpIface1.Address.Address, vrrpIface1.MacAddr, vrrpIface2.Address.Address, vrrpIface2.MacAddr)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Set vrrp ip command execution failed ", err)
			return
		}
	}
	if vrrpIface2.Hyper >= 0 {
		control := fmt.Sprintf("inter=%d", vrrpIface2.Hyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_vrrp_ip.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s'", routerID, vrrpInstance.ID, vrrpSubnet.Vlan, vrrpIface2.Address.Address, vrrpIface2.MacAddr, vrrpIface1.Address.Address, vrrpIface1.MacAddr)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Set vrrp ip command execution failed ", err)
			return
		}
	}
	err = DeleteInterface(ctx, vrrpIface1)
	if err != nil {
		logger.Error("DB failed to delete vrrp interface 1", err)
		err = NewCLError(ErrInterfaceDeleteFailed, "Failed to delete vrrp interface 1", err)
		return
	}
	err = DeleteInterface(ctx, vrrpIface2)
	if err != nil {
		logger.Error("DB failed to delete vrrp interface 2", err)
		err = NewCLError(ErrInterfaceDeleteFailed, "Failed to delete vrrp interface 2", err)
		return
	}
	if err = db.Delete(loadBalancer.VrrpInstance).Error; err != nil {
		logger.Error("DB failed to delete vrrp instance", err)
		err = NewCLError(ErrLoadBalancerDeleteFailed, "Failed to delete vrrp instance", err)
		return
	}
	if err = db.Delete(loadBalancer).Error; err != nil {
		logger.Error("DB failed to delete load balancer", err)
		err = NewCLError(ErrLoadBalancerDeleteFailed, "Failed to delete load balancer", err)
		return
	}
	loadBalancer.Name = fmt.Sprintf("%s-%d", loadBalancer.Name, loadBalancer.CreatedAt.Unix())
	err = db.Model(&model.LoadBalancer{}).Unscoped().Where("id = ?", loadBalancer.ID).Update("name", loadBalancer.Name).Error
	if err != nil {
		logger.Error("DB failed to update loadBalancer name", err)
		err = NewCLError(ErrLoadBalancerUpdateFailed, "Failed to update loadBalancer name", err)
		return
	}
	count := 0
	err = db.Model(&model.LoadBalancer{}).Where("router_id = ?", loadBalancer.RouterID).Count(&count).Error
	if err != nil {
		logger.Error("Failed to count load balancer")
		err = NewCLError(ErrDatabaseError, "Failed to count load balancer in the router", err)
		return
	}
	if count == 0 {
		err = subnetAdmin.Delete(ctx, loadBalancer.VrrpInstance.VrrpSubnet)
		if err != nil {
			logger.Error("Failed to delete vrrp subnet", err)
			err = NewCLError(ErrSubnetDeleteFailed, "Failed to delete vrrp subnet", err)
			return
		}
	}
	return
}

func (a *LoadBalancerAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, loadBalancers []*model.LoadBalancer, err error) {
	logger.Infof("ENTER LoadBalancerAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT LoadBalancerAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT LoadBalancerAdmin.List: total=%d, count=%d", total, len(loadBalancers))
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
	loadBalancers = []*model.LoadBalancer{}
	if err = db.Model(&model.LoadBalancer{}).Where(queryBuilder, args...).Where(query).Count(&total).Error; err != nil {
		logger.Error("DB failed to count load balancer, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count load balancer", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("FloatingIps").Preload("VrrpInstance").Preload("VrrpInstance.VrrpSubnet").Preload("Listeners").Preload("Listeners.Backends").Preload("Router").Where(queryBuilder, args...).Where(query).Find(&loadBalancers).Error; err != nil {
		logger.Error("DB failed to query load balancers, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query load balancers", err)
		return
	}
	permit = memberShip.IsSystemAdmin()
	if permit {
		db = db.Offset(0).Limit(-1)
		for _, loadBalancer := range loadBalancers {
			loadBalancer.OwnerInfo = &model.Organization{Model: model.Model{ID: loadBalancer.Owner}}
			if err = db.Take(loadBalancer.OwnerInfo).Error; err != nil {
				logger.Error("Failed to query owner info", err)
				err = NewCLError(ErrOwnerNotFound, "Failed to query owner info", err)
				return
			}
		}
	}
	return
}
