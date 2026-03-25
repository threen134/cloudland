/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var FloatingIpAdmin = &FloatingIpAdminService{}

type FloatingIps struct {
	Instance  int64  `json:"instance"`
	PublicIp  string `json:"public_ip"`
	PrivateIp string `json:"private_ip"`
}

type FloatingIpAdminService struct{}

// AllocateFloatingIp allocates a floating IP from available subnets
func AllocateFloatingIp(ctx context.Context, fipID, orgID int64, subnets []*model.Subnet, publicIp string) (*model.Interface, error) {
	logger.Infof("ENTER AllocateFloatingIp: fipID=%d, orgID=%d, publicIp=%s", fipID, orgID, publicIp)
	defer func() {
		logger.Info("EXIT AllocateFloatingIp")
	}()

	var iface *model.Interface
	var err error

	// Try to allocate from each subnet in order
	for _, subnet := range subnets {
		iface, err = CreateInterface(ctx, subnet, fipID, orgID, 0, 0, 0, publicIp, "", "", "floating", nil, false)
		if err == nil {
			logger.Infof("Successfully allocated floating IP from subnet %s", subnet.Name)
			return iface, nil
		}
		logger.Debugf("Failed to allocate from subnet %s: %v", subnet.Name, err)
	}

	if err != nil {
		logger.Errorf("Failed to allocate floating IP from all subnets: %v", err)
		return nil, NewCLError(ErrFIPCreateFailed, "Failed to allocate floating IP", err)
	}

	return nil, NewCLError(ErrFIPCreateFailed, "No available subnets for floating IP allocation", nil)
}

// EnsureSubnetID ensures the floating IP has a valid subnet ID
func (a *FloatingIpAdminService) EnsureSubnetID(ctx context.Context, floatingIp *model.FloatingIp) error {
	if floatingIp.SubnetID != 0 {
		return nil
	}
	if floatingIp.Interface != nil && floatingIp.Interface.Address != nil {
		floatingIp.SubnetID = floatingIp.Interface.Address.SubnetID
	}
	return nil
}

// DeallocateFloatingIp deallocates a floating IP
func (a *FloatingIpAdminService) DeallocateFloatingIp(ctx context.Context, fipID int64) error {
	logger.Infof("ENTER DeallocateFloatingIp: fipID=%d", fipID)
	defer func() {
		logger.Info("EXIT DeallocateFloatingIp")
	}()

	ctx, db := GetContextDB(ctx)

	// Find the floating IP
	fip := &model.FloatingIp{Model: model.Model{ID: fipID}}
	err := db.Preload("Interface").Take(fip).Error
	if err != nil {
		logger.Errorf("Failed to query floating IP: %v", err)
		return NewCLError(ErrFIPNotFound, "Floating IP not found", err)
	}

	// Delete the interface if it exists
	if fip.Interface != nil {
		err = DeleteInterface(ctx, fip.Interface)
		if err != nil {
			logger.Errorf("Failed to delete interface for floating IP: %v", err)
			return NewCLError(ErrInterfaceDeleteFailed, "Failed to delete interface", err)
		}
	}

	// Delete the floating IP record
	err = db.Delete(fip).Error
	if err != nil {
		logger.Errorf("Failed to delete floating IP record: %v", err)
		return NewCLError(ErrFIPDeleteFailed, "Failed to delete floating IP", err)
	}

	logger.Infof("Successfully deallocated floating IP %d", fipID)
	return nil
}

func (a *FloatingIpAdminService) createAndAllocateFloatingIps(ctx context.Context, name string, inbound, outbound int32, count int, subnets []*model.Subnet, publicIp string, instance *model.Instance, isSite bool, group *model.IpGroup, loadBalancer *model.LoadBalancer) ([]*model.FloatingIp, error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	floatingIps := make([]*model.FloatingIp, 0)
	publicType := string(PublicFloating)
	routerID := int64(0)
	loadBalancerID := int64(0)
	if loadBalancer != nil {
		publicType = string(PublicLoadBalancer)
		loadBalancerID = loadBalancer.ID
		routerID = loadBalancer.RouterID
	}
	logger.Debugf("subnets: %v, publicIp: %s, instance: %v, count: %d, inbound: %d, outbound: %d", subnets, publicIp, instance, count, inbound, outbound)
	for i := 0; i < count; i++ {
		uniqueName := fmt.Sprintf("%s-%d-%d", name, i, time.Now().UnixNano())
		var groupID int64
		if group != nil && group.ID != 0 {
			groupID = group.ID
		}
		fip := &model.FloatingIp{Model: model.Model{Creater: memberShip.UserID}, Owner: memberShip.OrgID, Name: uniqueName, Inbound: inbound, Outbound: outbound, Type: publicType, GroupID: groupID, LoadBalancerID: loadBalancerID, RouterID: routerID}
		if err := db.Create(fip).Error; err != nil {
			logger.Error("DB failed to create floating ip", err)
			return nil, NewCLError(ErrFIPCreateFailed, "Failed to create floating ip", err)
		}
		logger.Debugf("fip: %v, subnets: %v, publicIp: %s", fip, subnets, publicIp)
		fipIface, err := AllocateFloatingIp(ctx, fip.ID, memberShip.OrgID, subnets, publicIp)
		if err != nil {
			logger.Error("DB failed to allocate floating ip", err)
			return nil, err
		}
		fip.FipAddress = fipIface.Address.Address
		fip.IPAddress = strings.Split(fip.FipAddress, "/")[0]
		fip.Interface = fipIface
		fip.SubnetID = fipIface.Address.Subnet.ID
		if instance != nil {
			if err := a.Attach(ctx, fip, instance); err != nil {
				logger.Error("Execute attaching floating ip failed", err)
				return nil, err
			}
		}
		if isSite {
			fip.Type = string(PublicSite)
			if i == 0 && instance != nil {
				var primaryInterfaceID int64
				for _, iface := range instance.Interfaces {
					if iface.PrimaryIf {
						primaryInterfaceID = iface.ID
						break
					}
				}
				for _, subnet := range subnets {
					if subnet.Interface != primaryInterfaceID {
						subnet.Interface = primaryInterfaceID
						if err := db.Model(subnet).Update("interface", primaryInterfaceID).Error; err != nil {
							logger.Error("Failed to update subnet interface", err)
							return nil, NewCLError(ErrSubnetUpdateFailed, "Failed to update subnet interface", err)
						}
					}
				}
			}
		}
		if err := db.Model(fip).Updates(fip).Error; err != nil {
			logger.Error("DB failed to update floating ip", err)
			return nil, NewCLError(ErrFIPUpdateFailed, "Failed to update floating ip", err)
		}
		floatingIps = append(floatingIps, fip)
	}
	return floatingIps, nil
}

func (a *FloatingIpAdminService) createDummyFloatingIp(ctx context.Context, instance *model.Instance, publicIp string) (floatingIp *model.FloatingIp, err error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	uniqueName := fmt.Sprintf("%s-%d", instance.Hostname, time.Now().UnixNano())
	fip := &model.FloatingIp{Model: model.Model{Creater: memberShip.UserID}, Owner: memberShip.OrgID, Name: uniqueName, Inbound: 0, Outbound: 0, FipAddress: publicIp, IntAddress: publicIp, InstanceID: instance.ID, Type: string(PublicNative)}
	if err = db.Create(fip).Error; err != nil {
		logger.Error("DB failed to create floating ip", err)
		return nil, NewCLError(ErrDummyFIPCreateFailed, "Failed to create floating ip", err)
	}
	return
}

func (a *FloatingIpAdminService) Create(ctx context.Context, instance *model.Instance, pubSubnets []*model.Subnet, publicIp string, name string, inbound, outbound, activationCount int32, siteSubnets []*model.Subnet, group *model.IpGroup, loadBalancer *model.LoadBalancer) (floatingIps []*model.FloatingIp, err error) {
	logger.Infof("ENTER FloatingIpAdmin.Create: name=%s, publicIp=%s, instanceID=%v", name, publicIp, instance)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.Create: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	logger.Debugf("instance: %v, pubSubnets: %v, publicIp: %s, name: %s, inbound: %d, outbound: %d, activationCount: %d, siteSubnets: %v", instance, pubSubnets, publicIp, name, inbound, outbound, activationCount, siteSubnets)

	if publicIp != "" && (activationCount > 1 || len(siteSubnets) > 0) {
		logger.Error("Public ip and subnets cannot be specified at the same time")
		err = NewCLError(ErrInvalidParameter, "Public ip and subnets cannot be specified at the same time", nil)
		return
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	if len(pubSubnets) == 0 {
		err = db.Where("type = ?", "public").Order("priority ASC, id ASC").Find(&pubSubnets).Error
		if err != nil {
			logger.Error("Failed to query public subnets ", err)
			return nil, NewCLError(ErrSQLSyntaxError, "Failed to query public subnets", err)
		}
		if len(pubSubnets) == 0 {
			logger.Error("No public subnets available")
			return nil, NewCLError(ErrSubnetNotFound, "No public subnets available", nil)
		}
	}
	idleCountTotal := int64(0)
	for _, subnet := range pubSubnets {
		if subnet.Type != "public" {
			logger.Error("Subnet must be public", err)
			err = NewCLError(ErrSubnetShouldBePublic, "Subnet must be public", nil)
			return
		}
		var idleCount int64
		idleCount, err = subnetAdmin.CountIdleAddressesForSubnet(ctx, subnet)
		if err != nil {
			logger.Errorf("Failed to count idle addresses for subnet, err=%v", err)
			return
		}
		idleCountTotal += idleCount
	}
	if idleCountTotal < int64(activationCount) {
		logger.Errorf("Not enough idle addresses for public subnets, idleCountTotal: %d, activationCount: %d, pubSubnets: %v", idleCountTotal, activationCount, pubSubnets)
		return nil, NewCLError(ErrInsufficientAddress, "Not enough idle addresses for public subnets", nil)
	}

	// Sort public subnets by priority (lower value = higher priority)
	// If priority is same, sort by ID for stability
	if len(pubSubnets) > 0 {
		sort.Slice(pubSubnets, func(i, j int) bool {
			if pubSubnets[i].Priority == pubSubnets[j].Priority {
				return pubSubnets[i].ID < pubSubnets[j].ID
			}
			return pubSubnets[i].Priority < pubSubnets[j].Priority
		})
		logger.Debugf("Sorted pubSubnets by priority: %v", pubSubnets)
	}

	if len(siteSubnets) > 0 {
		for _, subnet := range siteSubnets {
			if subnet.Type != "site" {
				logger.Error("Subnet must be site", err)
				err = NewCLError(ErrSubnetShouldBeSite, "Subnet must be site", nil)
				return
			}
			var idleCount int64
			idleCount, err = subnetAdmin.CountIdleAddressesForSubnet(ctx, subnet)
			if err != nil {
				logger.Errorf("Failed to count idle addresses for subnet, err=%v", err)
				return
			}
			if idleCount == 0 {
				logger.Errorf("No idle addresses for site subnet %s", subnet.Name)
				err = NewCLError(ErrInsufficientAddress, "No idle addresses for site subnet", nil)
				return
			}
			subnet.IdleCount = idleCount
		}
	}

	floatingIps = make([]*model.FloatingIp, 0)
	logger.Debugf("pubSubnets: %v, publicIp: %s, instance: %v, activationCount: %d, inbound: %d, outbound: %d, siteSubnets: %v, group: %v", pubSubnets, publicIp, instance, activationCount, inbound, outbound, siteSubnets, group)
	var fips []*model.FloatingIp
	fips, err = a.createAndAllocateFloatingIps(ctx, name, inbound, outbound, int(activationCount), pubSubnets, publicIp, instance, false, group, loadBalancer)
	if err != nil {
		return
	}
	floatingIps = append(floatingIps, fips...)

	logger.Debugf("siteSubnets: %v", siteSubnets)
	for i := 0; i < len(siteSubnets); i++ {
		logger.Debugf("siteSubnets[%d]: %v, idleCount: %d, activationCount: %d, inbound: %d, outbound: %d, group: %v", i, siteSubnets[i], siteSubnets[i].IdleCount, siteSubnets[i].IdleCount, inbound, outbound, group)
		var siteFips []*model.FloatingIp
		siteFips, err = a.createAndAllocateFloatingIps(ctx, name, inbound, outbound, int(siteSubnets[i].IdleCount), []*model.Subnet{siteSubnets[i]}, "", instance, true, group, nil)
		if err != nil {
			return
		}
		floatingIps = append(floatingIps, siteFips...)
	}

	if loadBalancer != nil {
		loadBalancer.FloatingIps = append(loadBalancer.FloatingIps, floatingIps...)
		err = CreateVrrpConf(ctx, loadBalancer)
		if err != nil {
			err = NewCLError(ErrVrrpInstanceCreateFailed, "Recreate keepalived config failed", err)
			return
		}
		err = backendAdmin.CreateHaproxyConf(ctx, nil, loadBalancer)
		if err != nil {
			logger.Error("Failed to create haproxy conf ", err)
			err = NewCLError(ErrBackendCreateFailed, "Failed to create haproxy conf", err)
			return
		}
	}

	return floatingIps, nil
}

func (a *FloatingIpAdminService) Attach(ctx context.Context, floatingIp *model.FloatingIp, instance *model.Instance) (err error) {
	logger.Infof("ENTER FloatingIpAdmin.Attach: floatingIpID=%d, instanceID=%d", floatingIp.ID, instance.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.Attach: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.Attach: success")
		}
	}()
	if floatingIp.Type != string(PublicFloating) && floatingIp.Type != string(PublicSite) {
		logger.Infof("Cannot attach floating IP of type %s, only PublicFloating and PublicSite types are supported for attachment", floatingIp.Type)
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Cannot attach floating IP of type %s, only PublicFloating and PublicSite types are supported for attachment", floatingIp.Type), nil)
		return
	}
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if instance == nil || (instance.Status == model.InstanceStatusProvisioning) {
		logger.Error("Instance is not running")
		err = NewCLError(ErrInstanceInvalidState, "Instance is not running", nil)
		return
	}
	instID := instance.ID
	routerID := instance.RouterID
	if routerID == 0 {
		logger.Error("Instance has no router")
		err = NewCLError(ErrInstanceNoRouter, "Instance has no router", nil)
		return
	}
	router := &model.Router{Model: model.Model{ID: routerID}}
	err = db.Take(router).Error
	if err != nil {
		logger.Error("DB failed to query router", err)
		return NewCLError(ErrRouterNotFound, "DB failed to query router", err)
	}
	var primaryIface *model.Interface
	for i, iface := range instance.Interfaces {
		if iface.PrimaryIf {
			primaryIface = instance.Interfaces[i]
			break
		}
	}
	if primaryIface == nil {
		err = NewCLError(ErrInstanceNoPrimaryInterface, fmt.Sprintf("No primary interface for the instance, %d", instID), nil)
		return
	}
	floatingIp.IntAddress = primaryIface.Address.Address
	floatingIp.InstanceID = instance.ID
	floatingIp.RouterID = instance.RouterID
	err = db.Model(floatingIp).Updates(floatingIp).Error
	if err != nil {
		logger.Error("DB failed to update floating ip", err)
		return NewCLError(ErrFIPUpdateFailed, "DB failed to update floating ip", err)
	}

	err = a.EnsureSubnetID(ctx, floatingIp)
	if err != nil {
		logger.Error("Failed to ensure subnet_id", err)
		return
	}

	pubSubnet := floatingIp.Interface.Address.Subnet
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_floating.sh '%d' '%s' '%s' '%d' '%s' '%d' '%d' '%d' '%d'", router.ID, floatingIp.FipAddress, pubSubnet.Gateway, pubSubnet.Vlan, primaryIface.Address.Address, primaryIface.Address.Subnet.Vlan, floatingIp.ID, floatingIp.Inbound, floatingIp.Outbound)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Error("Execute floating ip failed", err)
		return
	}
	return
}

func (a *FloatingIpAdminService) Get(ctx context.Context, id int64) (floatingIp *model.FloatingIp, err error) {
	logger.Infof("ENTER FloatingIpAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid floatingIp ID: %d", id), nil)
		logger.Error(err)
		return
	}
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	where, args := memberShip.GetOrgFilter()
	floatingIp = &model.FloatingIp{Model: model.Model{ID: id}}
	err = db.Preload("Interface").Preload("Interface.SecurityGroups").Preload("Interface.Address").Preload("Interface.Address.Subnet").Preload("Subnet").Preload("Group").Where(where, args...).Take(floatingIp).Error
	if err != nil {
		logger.Error("DB failed to query floatingIp ", err)
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query floatingIp", err)
	}
	if floatingIp.InstanceID > 0 {
		floatingIp.Instance = &model.Instance{Model: model.Model{ID: floatingIp.InstanceID}}
		err = db.Take(floatingIp.Instance).Error
		if err != nil {
			msg := fmt.Sprintf("Failed to query instance: %d", floatingIp.InstanceID)
			logger.Error(msg, err)
			return nil, NewCLError(ErrInstanceNotFound, msg, err)
		}
		instance := floatingIp.Instance
		err = db.Preload("Address").Preload("Address.Subnet").Where("instance = ? and primary_if = true", instance.ID).Find(&instance.Interfaces).Error
		if err != nil {
			logger.Error("Failed to query interfaces %v", err)
			return nil, NewCLError(ErrSQLSyntaxError, "Failed to query interfaces", err)
		}
	} else if floatingIp.LoadBalancerID > 0 {
		floatingIp.LoadBalancer, err = loadBalancerAdmin.Get(ctx, floatingIp.LoadBalancerID)
		if err != nil {
			logger.Error("Failed to get ip load balancer ", err)
			return
		}
	}
	if floatingIp.RouterID > 0 {
		floatingIp.Router = &model.Router{Model: model.Model{ID: floatingIp.RouterID}}
		err = db.Take(floatingIp.Router).Error
		if err != nil {
			msg := fmt.Sprintf("Failed to query router: %d", floatingIp.RouterID)
			logger.Error(msg, err)
			return nil, NewCLError(ErrRouterNotFound, msg, err)
		}
	}

	err = a.EnsureSubnetID(ctx, floatingIp)
	if err != nil {
		logger.Error("Failed to ensure subnet_id", err)
		return
	}

	return
}

func (a *FloatingIpAdminService) GetFloatingIpByUUID(ctx context.Context, uuID string) (floatingIp *model.FloatingIp, err error) {
	logger.Infof("ENTER FloatingIpAdmin.GetFloatingIpByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.GetFloatingIpByUUID: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.GetFloatingIpByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	floatingIp = &model.FloatingIp{}
	err = db.Preload("Interface").Preload("Interface.Address").Preload("Interface.Address.Subnet").Preload("Subnet").Preload("Group").Where(where, args...).Where("uuid = ?", uuID).Take(floatingIp).Error
	if err != nil {
		logger.Error("Failed to query floatingIp, %v", err)
		return nil, NewCLError(ErrDatabaseError, "Failed to query floatingIp", err)
	}
	if floatingIp.InstanceID > 0 {
		floatingIp.Instance = &model.Instance{Model: model.Model{ID: floatingIp.InstanceID}}
		err = db.Take(floatingIp.Instance).Error
		if err != nil {
			msg := fmt.Sprintf("Failed to query instance: %d", floatingIp.InstanceID)
			logger.Error(msg, err)
			return nil, NewCLError(ErrInstanceNotFound, msg, err)
		}
		instance := floatingIp.Instance
		err = db.Preload("Address").Preload("Address.Subnet").Where("instance = ? and primary_if = true", instance.ID).Find(&instance.Interfaces).Error
		if err != nil {
			msg := fmt.Sprintf("Failed to query interfaces for instance: %d", instance.ID)
			logger.Error(msg, err)
			return nil, NewCLError(ErrSQLSyntaxError, msg, err)
		}
	} else if floatingIp.LoadBalancerID > 0 {
		floatingIp.LoadBalancer, err = loadBalancerAdmin.Get(ctx, floatingIp.LoadBalancerID)
		if err != nil {
			logger.Error("DB failed to query load balancer ", err)
			return nil, NewCLError(ErrSQLSyntaxError, "Failed to query load balancer", err)
		}
	}
	if floatingIp.RouterID > 0 {
		floatingIp.Router = &model.Router{Model: model.Model{ID: floatingIp.RouterID}}
		err = db.Take(floatingIp.Router).Error
		if err != nil {
			msg := fmt.Sprintf("Failed to query router: %d", floatingIp.RouterID)
			logger.Error(msg, err)
			return nil, NewCLError(ErrRouterNotFound, msg, err)
		}
	}

	err = a.EnsureSubnetID(ctx, floatingIp)
	if err != nil {
		logger.Error("Failed to ensure subnet_id", err)
		return
	}

	return
}

func (a *FloatingIpAdminService) Detach(ctx context.Context, floatingIp *model.FloatingIp) (err error) {
	logger.Infof("ENTER FloatingIpAdmin.Detach: floatingIpID=%d", floatingIp.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.Detach: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.Detach: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if floatingIp.Type == string(PublicNative) {
		if err = db.Delete(floatingIp).Error; err != nil {
			logger.Error("DB: delete native fip failed", err)
			return NewCLError(ErrDeleteNativeFIPFailed, "Failed to delete native floating IP", err)
		}
		floatingIp.Instance = nil
		return
	}
	if floatingIp.Type == string(PublicReserved) {
		floatingIp.Instance = nil
		floatingIp.Interface = nil
		floatingIp.Router = nil
		floatingIp.InstanceID = 0
		floatingIp.RouterID = 0
		floatingIp.IntAddress = ""
		floatingIp.Type = string(PublicFloating)

		updateFields := make(map[string]interface{})
		updateFields["instance_id"] = 0
		updateFields["router_id"] = 0
		updateFields["int_address"] = ""
		updateFields["type"] = string(PublicFloating)

		err = db.Model(floatingIp).Updates(updateFields).Error
		if err != nil {
			logger.Errorf("Failed to update public ip, %v", err)
			return NewCLError(ErrUpdatePublicIPFailed, "Failed to update public ip", err)
		}
		return
	}
	loadBalancer := floatingIp.LoadBalancer
	if floatingIp.Instance != nil {
		var primaryIface *model.Interface
		instance := floatingIp.Instance
		for i, iface := range instance.Interfaces {
			if iface.PrimaryIf {
				primaryIface = instance.Interfaces[i]
				break
			}
		}
		control := fmt.Sprintf("inter=%d", floatingIp.Instance.Hyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_floating.sh '%d' '%s' '%s' '%d' '%d'", floatingIp.RouterID, floatingIp.FipAddress, floatingIp.IntAddress, primaryIface.Address.Subnet.Vlan, floatingIp.ID)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Detach floating ip failed", err)
			return
		}
	} else if loadBalancer != nil {
		hyperGroup := ""
		hyperGroup, _, _, err = GetVrrpHyperGroup(ctx, loadBalancer.VrrpInstance)
		if err != nil {
			logger.Error("Failed to query vrrp hyper group and interfaces", err)
			return
		}
		control := "toall=" + hyperGroup
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_lb_floating.sh '%d' '%s' '%d' '%d'", floatingIp.RouterID, floatingIp.FipAddress, floatingIp.Subnet.Vlan, floatingIp.ID)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Clear lb floating ip execution failed ", err)
			err = NewCLError(ErrExecuteOnHyperFailed, "Clear lb floating execution failed", err)
			return
		}
	}
	logger.Debugf("Floating ip: %v\n", floatingIp)
	floatingIp.InstanceID = 0
	floatingIp.Instance = nil
	err = db.Model(&model.FloatingIp{Model: model.Model{ID: floatingIp.ID}}).Update(map[string]interface{}{
		"instance_id":      0,
		"load_balancer_id": 0,
		"router_id":        0,
		"int_address":      "",
		"type":             PublicFloating,
	}).Error
	if err != nil {
		logger.Error("Failed to update instance ID for floating ip", err)
		return NewCLError(ErrUpdateInstIDOfFIPFailed, "Failed to update instance ID for floating ip", err)
	}
	if loadBalancer != nil {
		intQuery := fmt.Sprintf("load_balancer_id = %d", loadBalancer.ID)
		_, loadBalancer.FloatingIps, err = (&FloatingIpAdminService{}).List(ctx, 0, -1, "", "", intQuery)
		if err != nil {
			logger.Error("Failed to list floating ip(s), %v", err)
			return
		}
		err = CreateVrrpConf(ctx, loadBalancer)
		if err != nil {
			logger.Error("Recreate keepalived config failed", err)
			err = NewCLError(ErrVrrpInstanceCreateFailed, "Recreate keepalived config failed", err)
			return
		}
		err = backendAdmin.CreateHaproxyConf(ctx, nil, loadBalancer)
		if err != nil {
			logger.Error("Failed to create haproxy conf ", err)
			err = NewCLError(ErrBackendCreateFailed, "Failed to create haproxy conf", err)
			return
		}
	}
	return
}

func (a *FloatingIpAdminService) Update(ctx context.Context, floatingIp *model.FloatingIp, instance *model.Instance, group *model.IpGroup, loadBalancer *model.LoadBalancer) (floatingIpTemp *model.FloatingIp, err error) {
	logger.Infof("ENTER FloatingIpAdmin.Update: floatingIpID=%d, name=%s", floatingIp.ID, floatingIp.Name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	err = a.Detach(ctx, floatingIp)
	if err != nil {
		logger.Errorf("Failed to detach floating ip %+v", err)
		return
	}

	if instance != nil {
		err = a.Attach(ctx, floatingIp, instance)
		if err != nil {
			logger.Errorf("Failed to attach floating ip %+v", err)
			return
		}
	} else if loadBalancer != nil {
		err = db.Model(&model.FloatingIp{Model: model.Model{ID: floatingIp.ID}}).Update(map[string]interface{}{
			"load_balancer_id": loadBalancer.ID,
			"router_id":        loadBalancer.RouterID,
			"type":             PublicLoadBalancer,
		}).Error
		err = CreateVrrpConf(ctx, loadBalancer)
		if err != nil {
			err = NewCLError(ErrVrrpInstanceCreateFailed, "Recreate keepalived config failed", err)
			return
		}
		err = backendAdmin.CreateHaproxyConf(ctx, nil, loadBalancer)
		if err != nil {
			logger.Error("Failed to create haproxy conf ", err)
			err = NewCLError(ErrBackendCreateFailed, "Failed to create haproxy conf", err)
			return
		}
	}

	if group != nil {
		groupID := int64(0)

		err = db.Model(&model.FloatingIp{Model: model.Model{ID: floatingIp.ID}}).Update("group_id", groupID).Error
		if err != nil {
			logger.Error("Failed to update floating ip group_id", err)
			return nil, NewCLError(ErrUpdateGroupIDFailed, "Failed to update floating ip group_id", err)
		}
	}

	floatingIpTemp, err = a.Get(ctx, floatingIp.ID)
	if err != nil {
		logger.Error("Failed to get updated floating ip", err)
		return
	}

	return floatingIpTemp, nil
}

func (a *FloatingIpAdminService) Delete(ctx context.Context, floatingIp *model.FloatingIp) (err error) {
	logger.Infof("ENTER FloatingIpAdmin.Delete: floatingIpID=%d, uuid=%s", floatingIp.ID, floatingIp.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.Delete: success")
		}
	}()
	if floatingIp.Type != string(PublicFloating) && floatingIp.Type != string(PublicLoadBalancer) {
		errorStr := fmt.Sprintf("Cannot delete floating IP of type %s, only PublicFloating or PublicLoadBalancer type is supported for deletion", floatingIp.Type)
		logger.Info(errorStr)
		err = NewCLError(ErrInvalidParameter, errorStr, nil)
		return
	}
	ctx, _, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if floatingIp.Instance != nil || floatingIp.LoadBalancer != nil {
		err = a.Detach(ctx, floatingIp)
		if err != nil {
			logger.Error("Failed to detach floating ip", err)
			return
		}
	}
	err = a.DeallocateFloatingIp(ctx, floatingIp.ID)
	if err != nil {
		logger.Error("DB failed to deallocate floating ip", err)
		return
	}
	return
}

func (a *FloatingIpAdminService) List(ctx context.Context, offset, limit int64, order, query string, intQuery string) (total int64, floatingIps []*model.FloatingIp, err error) {
	logger.Infof("ENTER FloatingIpAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT FloatingIpAdmin.List: error=%v", err)
		} else {
			logger.Info("EXIT FloatingIpAdmin.List: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgReader)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}
	if query != "" {
		query = fmt.Sprintf("fip_address like '%%%s%%' or int_address like '%%%s%%' or name like '%%%s%%'", query, query, query)
	}

	_, db := GetContextDB(ctx)
	where, args := memberShip.GetOrgFilter()
	floatingIps = []*model.FloatingIp{}
	if err = db.Model(&model.FloatingIp{}).Where(where, args...).Where(query).Where(intQuery).Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count floatingIps", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("Group").Preload("Interface").Preload("Interface.Address").Preload("Interface.Address.Subnet").Preload("Subnet").Where(where, args...).Where(query).Where(intQuery).Find(&floatingIps).Error; err != nil {
		logger.Error("DB failed to query floating ip(s), %v", err)
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to query floating IPs", err)
	}
	db = db.Offset(0).Limit(-1)
	for _, fip := range floatingIps {
		err = a.EnsureSubnetID(ctx, fip)
		if err != nil {
			logger.Error("Failed to ensure subnet_id", err)
			continue
		}

		if fip.InstanceID > 0 {
			fip.Instance = &model.Instance{Model: model.Model{ID: fip.InstanceID}}
			err = db.Preload("Zone").Take(fip.Instance).Error
			if err != nil {
				logger.Error("DB failed to query instance ", err)
				err = nil
				continue
			}
			instance := fip.Instance
			err = db.Preload("Address").Where("instance = ? and primary_if = true", instance.ID).Find(&instance.Interfaces).Error
			if err != nil {
				logger.Error("Failed to query interfaces ", err)
				err = nil
				continue
			}
		} else if fip.LoadBalancerID > 0 {
			fip.LoadBalancer, err = loadBalancerAdmin.Get(ctx, fip.LoadBalancerID)
			if err != nil {
				logger.Error("DB failed to query load balancer ", err)
				err = nil
				continue
			}
		}

		if fip.RouterID > 0 {
			fip.Router = &model.Router{Model: model.Model{ID: fip.RouterID}}
			err = db.Take(fip.Router).Error
			if err != nil {
				logger.Error("DB failed to query router ", err)
				err = nil
				continue
			}
		}
	}
	permit = memberShip.IsSystemAdmin()
	if permit {
		for _, fip := range floatingIps {
			fip.OwnerInfo = &model.Organization{Model: model.Model{ID: fip.Owner}}
			if err = db.Take(fip.OwnerInfo).Error; err != nil {
				logger.Error("Failed to query owner info", err)
				return 0, nil, NewCLError(ErrOwnerNotFound, "Failed to query owner info", err)
			}
		}
	}

	return
}

