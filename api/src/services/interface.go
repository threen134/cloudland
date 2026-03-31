/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"encoding/json"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/google/uuid"
	"github.com/jinzhu/gorm"
)

var InterfaceAdmin = &InterfaceAdminService{}

type InterfaceInfo struct {
	PublicIps      []*model.FloatingIp
	Subnets        []*model.Subnet
	MacAddress     string
	IpAddress      string
	Count          int
	SiteSubnets    []*model.Subnet
	Inbound        int32
	Outbound       int32
	AllowSpoofing  bool
	SecurityGroups []*model.SecurityGroup
}

type InterfaceAdminService struct{}

func (a *InterfaceAdminService) Get(ctx context.Context, id int64) (iface *model.Interface, err error) {
	logger.Infof("ENTER InterfaceAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT InterfaceAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, "Invalid interface ID", nil)
		logger.Error(err)
		return
	}
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	iface = &model.Interface{Model: model.Model{ID: id}}
	err = db.Preload("SiteSubnets").Preload("SecurityGroups").Preload("Address").Preload("Address.Subnet").Preload("SecondAddresses", func(db *gorm.DB) *gorm.DB {
		return db.Order("addresses.updated_at")
	}).Preload("SecondAddresses.Subnet").Take(iface).Error
	if err != nil {
		logger.Debug("DB failed to query interface, %v", err)
		err = NewCLError(ErrInterfaceNotFound, "Interface not found", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, iface.Owner)
	if !permit {
		logger.Debug("Not authorized to read the interface")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the interface", nil)
		return
	}
	return
}

func (a *InterfaceAdminService) GetInterfaceByUUID(ctx context.Context, uuID string) (iface *model.Interface, err error) {
	logger.Infof("ENTER InterfaceAdmin.GetInterfaceByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.GetInterfaceByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT InterfaceAdmin.GetInterfaceByUUID: success, id=%d", iface.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	ctx, db := GetContextDB(ctx)
	iface = &model.Interface{}
	err = db.Preload("SiteSubnets").Preload("SecurityGroups").Preload("Address").Preload("Address.Subnet").Preload("SecondAddresses", func(db *gorm.DB) *gorm.DB {
		return db.Order("addresses.updated_at")
	}).Preload("SecondAddresses.Subnet").Where(query, args...).Where("uuid = ?", uuID).Take(iface).Error
	if err != nil {
		logger.Debug("DB failed to query interface, %v", err)
		err = NewCLError(ErrInterfaceNotFound, "Interface not found", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, iface.Owner)
	if !permit {
		logger.Debug("Not authorized to read the subnet")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the interface", nil)
		return
	}
	return
}

// GetInterfacesByUUIDs batch-fetches interfaces by UUID list. Only returns interfaces
// the caller is authorized to read; unrecognized UUIDs are silently omitted.
func (a *InterfaceAdminService) GetInterfacesByUUIDs(ctx context.Context, uuIDs []string) (ifaces []*model.Interface, err error) {
	logger.Infof("ENTER InterfaceAdmin.GetInterfacesByUUIDs: count=%d", len(uuIDs))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.GetInterfacesByUUIDs: error=%v", err)
		} else {
			logger.Infof("EXIT InterfaceAdmin.GetInterfacesByUUIDs: found=%d", len(ifaces))
		}
	}()
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	_, db := GetContextDB(ctx)
	err = db.Where(query, args...).Where("uuid IN (?)", uuIDs).Find(&ifaces).Error
	if err != nil {
		return nil, err
	}
	// Only returns interfaces the caller has OrgReader access to.
	// Note: no Preload needed here — callers only use MacAddr and Instance fields.
	var authorized []*model.Interface
	for _, iface := range ifaces {
		if memberShip.CheckResourceOrg(model.OrgReader, iface.Owner) {
			authorized = append(authorized, iface)
		}
	}
	return authorized, nil
}

func (a *InterfaceAdminService) Delete(ctx context.Context, instance *model.Instance, iface *model.Interface) (err error) {
	logger.Infof("ENTER InterfaceAdmin.Delete: instanceID=%d, ifaceID=%d", instance.ID, iface.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT InterfaceAdmin.Delete: success")
		}
	}()
	if iface.PrimaryIf {
		err = NewCLError(ErrCannotDeletePrimaryInterface, "Primary interface can not be deleted", nil)
		return
	}
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, iface.Owner)
	if !permit {
		logger.Error("Not authorized to delete the interface")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the interface", nil)
		return
	}
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/detach_vm_nic.sh '%d' '%d' '%d' '%s' '%s'", instance.ID, iface.ID, iface.Address.Subnet.Vlan, iface.Address.Address, iface.MacAddr)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Error("Detach vm nic command execution failed", err)
		return
	}
	return
}

func (a *InterfaceAdminService) List(ctx context.Context, offset, limit int64, order string, instance *model.Instance) (total int64, interfaces []*model.Interface, err error) {
	logger.Infof("ENTER InterfaceAdmin.List: instanceID=%d, offset=%d, limit=%d, order=%s", instance.ID, offset, limit, order)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT InterfaceAdmin.List: total=%d, count=%d", total, len(interfaces))
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgReader, instance.Owner)
	if !permit {
		logger.Debug("Not authorized for this operation")
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

	where := fmt.Sprintf("instance = %d", instance.ID)
	query, args := memberShip.GetOrgFilter()
	interfaces = []*model.Interface{}
	if err = db.Model(&model.Interface{}).Where(where).Where(query, args...).Count(&total).Error; err != nil {
		logger.Debug("DB failed to count security rule(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count interfaces", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("SiteSubnets").Preload("SecurityGroups").Preload("Address").Preload("Address.Subnet").Preload("SecondAddresses", func(db *gorm.DB) *gorm.DB {
		return db.Order("addresses.updated_at")
	}).Preload("SecondAddresses.Subnet").Where(where).Where(query, args...).Find(&interfaces).Error; err != nil {
		logger.Debug("DB failed to query interface(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query interfaces", err)
		return
	}

	return
}

func (a *InterfaceAdminService) checkAddresses(_ context.Context, iface *model.Interface, ifaceSubnets, siteSubnets []*model.Subnet, secondAddrsCount int, publicIps []*model.FloatingIp) (valid, changed bool) {
	logger.Infof("ENTER InterfaceAdmin.checkAddresses: ifaceID=%d", iface.ID)
	defer func() {
		logger.Infof("EXIT InterfaceAdmin.checkAddresses: valid=%v, changed=%v", valid, changed)
	}()
	vlan := iface.Address.Subnet.Vlan
	publicIpsLength := len(publicIps)
	secondIpsLength := len(iface.SecondAddresses)
	if publicIpsLength > 0 {
		if publicIpsLength != secondIpsLength+1 {
			changed = true
		}
		for i, pubIp := range publicIps {
			if vlan != pubIp.Interface.Address.Subnet.Vlan {
				changed = true
				return
			}
			if i == 0 {
				if pubIp.FipAddress != iface.Address.Address {
					changed = true
					logger.Errorf("pubIp.FipAddress: %s, iface.Address.Address: %s, %d", pubIp.FipAddress, iface.Address.Address, i)
					return
				}
			} else {
				if (i - 1) < secondIpsLength {
					secondAddr := iface.SecondAddresses[i-1].Address
					if pubIp.FipAddress != secondAddr {
						changed = true
						logger.Errorf("pubIp.FipAddress: %s, iface.Address.Address: %s, %d", pubIp.FipAddress, secondAddr, i)
						return
					}
				}
			}
		}
	} else {
		if secondAddrsCount != secondIpsLength {
			changed = true
		}
		for _, subnet := range ifaceSubnets {
			if vlan != subnet.Vlan {
				changed = true
				return
			}
		}
	}
	if len(siteSubnets) != len(iface.SiteSubnets) {
		changed = true
	}
	for _, site := range siteSubnets {
		if vlan != site.Vlan {
			changed = true
			return
		}
		found := false
		for _, ifaceSite := range iface.SiteSubnets {
			if site.ID == ifaceSite.ID {
				found = true
				break
			}
		}
		if !found {
			changed = true
			break
		}
	}
	valid = true

	return
}

func (a *InterfaceAdminService) allocateSecondAddresses(ctx context.Context, instance *model.Instance, iface *model.Interface, ifaceSubnets []*model.Subnet, secondAddrsCount int) (err error) {
	logger.Infof("ENTER InterfaceAdmin.allocateSecondAddresses: instanceID=%d, ifaceID=%d, count=%d", instance.ID, iface.ID, secondAddrsCount)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.allocateSecondAddresses: error=%v", err)
		} else {
			logger.Info("EXIT InterfaceAdmin.allocateSecondAddresses: success")
		}
	}()
	cnt := 0
	for _, subnet := range ifaceSubnets {
		for i := 0; i < secondAddrsCount; i++ {
			var addr *model.Address
			addr, err = AllocateAddress(ctx, subnet, iface.ID, "", "second")
			if err == nil {
				iface.SecondAddresses = append(iface.SecondAddresses, addr)
				if subnet.Type == string(Public) {
					_, err = (&FloatingIpAdminService{}).createDummyFloatingIp(ctx, instance, addr.Address)
					if err != nil {
						logger.Error("DB failed to create dummy floating ip", err)
						return
					}
				}
				cnt++
				if cnt >= secondAddrsCount {
					return
				}
			} else {
				logger.Errorf("Allocate address interface from subnet %s--%s/%s failed, %v", subnet.Name, subnet.Network, subnet.Netmask, err)
			}
		}
	}
	if cnt < secondAddrsCount {
		err = NewCLError(ErrInsufficientAddress, fmt.Sprintf("Only %d addresses can be allocated", cnt), nil)
		return
	}
	return
}

func (a *InterfaceAdminService) changeAddresses(ctx context.Context, instance *model.Instance, iface *model.Interface, ifaceSubnets, siteSubnets []*model.Subnet, secondAddrsCount int, publicIps []*model.FloatingIp, secgroups []*model.SecurityGroup) (iface2 *model.Interface, err error) {
	logger.Infof("ENTER InterfaceAdmin.changeAddresses: instanceID=%d, ifaceID=%d", instance.ID, iface.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.changeAddresses: error=%v", err)
		} else {
			logger.Info("EXIT InterfaceAdmin.changeAddresses: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	for _, site := range iface.SiteSubnets {
		err = db.Model(site).Updates(map[string]interface{}{"interface": 0}).Error
		if err != nil {
			logger.Error("Failed to update site subnets", err)
			err = NewCLError(ErrSiteSubnetUpdateFailed, "Failed to update site subnets", err)
			return
		}
	}
	iface.SiteSubnets = nil

	if len(publicIps) > 0 {
		primaryMac := iface.MacAddr
		primaryUUID := iface.UUID
		if iface.FloatingIp != publicIps[0].ID {
			var floatingIp *model.FloatingIp
			floatingIp, err = (&FloatingIpAdminService{}).Get(ctx, iface.FloatingIp)
			if err != nil {
				logger.Errorf("Failed to get floating ip, %v", err)
				return
			}
			err = (&FloatingIpAdminService{}).Detach(ctx, floatingIp)
			if err != nil {
				logger.Errorf("Failed to detach floating ip, %v", err)
				return
			}
			if err = db.Model(iface).Association("Security_Groups").Replace([]*model.SecurityGroup{}).Error; err != nil {
				logger.Debug("Failed to save interface", err)
				return
			}
			mac := ""
			mac, err = GenerateMacaddr()
			if err != nil {
				logger.Error("Failed to generate random Mac address, %v", err)
				return
			}
			err = db.Model(iface).Update(map[string]interface{}{"instance": 0, "uuid": uuid.New().String(), "primary_if": false, "name": "fip", "inbound": 0, "outbound": 0, "allow_spoofing": false, "mac_addr": mac}).Error
			if err != nil {
				logger.Error("Failed to Update addresses, %v", err)
				return
			}
			iface = nil
		}
		iface, _, err = DerivePublicInterface(ctx, instance, iface, publicIps, primaryMac, primaryUUID)
		if err != nil {
			logger.Error("Failed to derive primary interface", err)
			return
		}
		if len(secgroups) > 0 {
			if err = db.Model(iface).Association("Security_Groups").Replace(secgroups).Error; err != nil {
				logger.Debug("Failed to save interface", err)
				return
			}
			iface.SecurityGroups = secgroups
		}
	} else {
		cnt := secondAddrsCount - len(iface.SecondAddresses)
		if cnt > 0 {
			err = a.allocateSecondAddresses(ctx, instance, iface, ifaceSubnets, cnt)
			if err != nil {
				return
			}
		} else if cnt < 0 {
			for i := 0; i < -cnt; i++ {
				err = db.Model(&iface.SecondAddresses[i]).Updates(map[string]interface{}{"second_interface": 0, "allocated": false}).Error
				if err != nil {
					logger.Errorf("Failed to update second address of interface %d, %+v", iface.ID, err)
					err = NewCLError(ErrAddressUpdateFailed, "Failed to update second address of interface", err)
					return
				}
			}
		}
	}
	for _, site := range siteSubnets {
		err = db.Model(site).Updates(map[string]interface{}{"interface": iface.ID}).Error
		if err != nil {
			logger.Error("Failed to update interface", err)
			err = NewCLError(ErrSiteSubnetUpdateFailed, "Failed to update interface", err)
			return
		}
		iface.SiteSubnets = append(iface.SiteSubnets, site)
	}
	iface.SecondAddresses = nil
	err = db.Preload("Subnet").Where("second_interface = ?", iface.ID).Find(&iface.SecondAddresses).Error
	if err != nil {
		logger.Error("Second addresses query failed", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query second addresses of interface", err)
		return
	}
	iface2 = iface

	return
}

func (a *InterfaceAdminService) checkSubnets(_ context.Context, subnets []*model.Subnet, vlan int64) (err error) {
	if len(subnets) == 0 {
		err = fmt.Errorf("At least one subnet must be specified")
		return
	}
	for _, subnet := range subnets {
		if vlan == 0 {
			vlan = subnet.Vlan
		} else if vlan != subnet.Vlan {
			err = fmt.Errorf("Subnets are not all in the same vlan")
			return
		}
	}
	return
}

func (a *InterfaceAdminService) CheckIfaceSubnets(ctx context.Context, primaryIface *InterfaceInfo, secondaryIfaces []*InterfaceInfo) (err error) {
	logger.Infof("ENTER InterfaceAdmin.CheckIfaceSubnets")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.CheckIfaceSubnets: error=%v", err)
		} else {
			logger.Info("EXIT InterfaceAdmin.CheckIfaceSubnets: success")
		}
	}()
	checkVlan := int64(0)
	if len(primaryIface.Subnets) > 0 {
		err = a.checkSubnets(ctx, primaryIface.Subnets, 0)
		if err != nil {
			logger.Error("Failed to check primary subnets", err)
			return
		}
		checkVlan = primaryIface.Subnets[0].Vlan
	}
	if len(primaryIface.SiteSubnets) > 0 {
		err = a.checkSubnets(ctx, primaryIface.SiteSubnets, checkVlan)
		if err != nil {
			logger.Error("Failed to check site subnets", err)
			return
		}
	}
	for _, iface := range secondaryIfaces {
		err = a.checkSubnets(ctx, iface.Subnets, 0)
		if err != nil {
			logger.Error("Failed to check site subnets", err)
			return
		}
		if iface.Subnets[0].Vlan == checkVlan {
			err = fmt.Errorf("Second interfaces can not use same vlan with primary")
			return
		}
	}
	for i, iface := range secondaryIfaces {
		checkVlan = iface.Subnets[0].Vlan
		for _, rest := range secondaryIfaces[i+1:] {
			if rest.Subnets[0].Vlan == checkVlan {
				err = fmt.Errorf("Different interfaces can not use same vlan")
				return
			}
		}
	}
	return
}

func (a *InterfaceAdminService) Create(ctx context.Context, instance *model.Instance, address, mac string, inbound, outbound int32, allowSpoofing bool, secgroups []*model.SecurityGroup, subnets []*model.Subnet, secondAddrsCount int) (iface *model.Interface, err error) {
	logger.Infof("ENTER InterfaceAdmin.Create: instanceID=%d, address=%s, mac=%s", instance.ID, address, mac)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT InterfaceAdmin.Create: success, ifaceID=%d", iface.ID)
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	ifaceLen := len(instance.Interfaces)
	if ifaceLen >= 8 {
		err = NewCLError(ErrTooManyInterfaces, "Can not create interfaces more than 8", nil)
		return
	}
	routerID := instance.RouterID
	ifname := fmt.Sprintf("eth%d", ifaceLen)
	err = a.checkSubnets(ctx, subnets, 0)
	if err != nil {
		logger.Error("Failed to check subnets", err)
		return
	}
	for _, instIface := range instance.Interfaces {
		if instIface.Address.Subnet.Vlan == subnets[0].Vlan {
			logger.Error("New interface can not use the same vlan of existing interfaces")
			err = NewCLError(ErrInterfaceInvalidSubnet, "Invalid or duplicate subnets for interfaces", nil)
			return
		}
	}
	for _, subnet := range subnets {
		if subnet.Type == "site" {
			logger.Error("Not allowed to create interface in site subnet")
			err = NewCLError(ErrNotAllowInterfaceInSiteSubnet, "Not allowed to create interface in site subnet", nil)
			return
		}
		if routerID > 0 && subnet.RouterID != routerID {
			logger.Error("Subnets can not belong to different router")
			err = NewCLError(ErrSubnetsCrossVPCInOneInstance, "Subnets can not belong to different router", nil)
			return
		}
		if iface == nil {
			iface, err = CreateInterface(ctx, subnet, instance.ID, memberShip.OrgID, instance.Hyper, inbound, outbound, address, mac, ifname, "instance", secgroups, allowSpoofing)
			if err == nil {
				if subnet.Type == "public" {
					_, err = (&FloatingIpAdminService{}).createDummyFloatingIp(ctx, instance, iface.Address.Address)
					if err != nil {
						logger.Error("DB failed to create dummy floating ip", err)
						return
					}
				}
				break
			} else {
				logger.Errorf("Allocate address interface from subnet %s--%s/%s failed, %v", subnet.Name, subnet.Network, subnet.Netmask, err)
			}
		}
	}
	if iface == nil {
		if err == nil {
			err = NewCLError(ErrInterfaceCreateFailed, "Failed to create interface", nil)
		}
		return
	}
	if routerID == 0 {
		instance.RouterID = iface.Address.Subnet.RouterID
		err = db.Model(&model.Instance{Model: model.Model{ID: int64(instance.ID)}}).Update(map[string]interface{}{
			"router_id": instance.RouterID}).Error
		if err != nil {
			logger.Debug("Failed to update instance", err)
			err = NewCLError(ErrInstanceUpdateFailed, "Failed to update instance", err)
			return
		}
	}
	err = ApplyInterface(ctx, instance, iface, false)
	if err != nil {
		return
	}
	return
}

func (a *InterfaceAdminService) Update(ctx context.Context, instance *model.Instance, iface *model.Interface, name string, inbound, outbound int32, allowSpoofing bool, secgroups []*model.SecurityGroup, ifaceSubnets []*model.Subnet, siteSubnets []*model.Subnet, secondAddrsCount int, publicIps []*model.FloatingIp) (iface2 *model.Interface, err error) {
	logger.Infof("ENTER InterfaceAdmin.Update: instanceID=%d, ifaceID=%d, name=%s", instance.ID, iface.ID, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT InterfaceAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT InterfaceAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	needUpdate := false
	needRemoteUpdate := false
	if iface.Name != name {
		iface.Name = name
		needUpdate = true
	}
	if iface.Inbound != inbound {
		iface.Inbound = inbound
		needUpdate = true
		needRemoteUpdate = true
	}
	if iface.Outbound != outbound {
		iface.Outbound = outbound
		needUpdate = true
		needRemoteUpdate = true
	}
	if iface.AllowSpoofing != allowSpoofing {
		iface.AllowSpoofing = allowSpoofing
		needUpdate = true
		needRemoteUpdate = true
	}
	if len(secgroups) > 0 {
		if err = db.Model(iface).Association("Security_Groups").Replace(secgroups).Error; err != nil {
			logger.Debug("Failed to save interface", err)
			err = NewCLError(ErrInterfaceUpdateFailed, "Failed to update interface security groups", err)
			return
		}
		iface.SecurityGroups = secgroups
		needRemoteUpdate = true
	} else {
		err = NewCLError(ErrAtLeastOneSGRequired, "At least one security group is needed", nil)
		return
	}
	if needUpdate || needRemoteUpdate {
		err = db.Model(&model.Interface{Model: model.Model{ID: int64(iface.ID)}}).Update(map[string]interface{}{
			"inbound":        iface.Inbound,
			"outbound":       iface.Outbound,
			"allow_spoofing": iface.AllowSpoofing,
			"name":           iface.Name}).Error
		if err != nil {
			logger.Debug("Failed to save interface", err)
			err = NewCLError(ErrInterfaceUpdateFailed, "Failed to update interface", err)
			return
		}
	}
	changed := false
	if iface.PrimaryIf && iface.Address.Subnet.RouterID == 0 {
		// valid := true
		_, changed = a.checkAddresses(ctx, iface, ifaceSubnets, siteSubnets, secondAddrsCount, publicIps)
		// if !valid {
		// 	logger.Errorf("Failed to check addresses, %v", err)
		// 	err = fmt.Errorf("Failed to check addresses")
		// 	return
		// }

		if changed {
			var oldAddresses []string
			_, oldAddresses, err = GetInstanceNetworks(ctx, instance, []*model.Interface{iface})
			if err != nil {
				logger.Errorf("Failed to get instance networks, %v", err)
				return
			}
			var oldAddrsJson []byte
			oldAddrsJson, err = json.Marshal(oldAddresses)
			if err != nil {
				logger.Errorf("Failed to marshal instance json data, %v", err)
				err = NewCLError(ErrJSONMarshalFailed, "Failed to marshal instance json data", err)
				return
			}
			// 1. Get old addresses 2. Change addresses 3. Remote execute
			iface, err = a.changeAddresses(ctx, instance, iface, ifaceSubnets, siteSubnets, secondAddrsCount, publicIps, secgroups)
			if err != nil {
				logger.Errorf("Failed to get instance networks, %v", err)
				return
			}
			osCode := GetImageOSCode(ctx, instance)
			if osCode == "windows" {
				control := fmt.Sprintf("inter=%d", instance.Hyper)
				command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_second_ips.sh '%d' '%s' '%s'<<EOF\n%s\nEOF", instance.ID, iface.MacAddr, GetImageOSCode(ctx, instance), oldAddrsJson)
				err = HyperExecute(ctx, control, command)
				if err != nil {
					logger.Error("clear_second_ips command execution failed", err)
					return
				}
			}
		}

	}
	if needRemoteUpdate || changed {
		err = ApplyInterface(ctx, instance, iface, changed)
		if err != nil {
			logger.Error("Update vm nic command execution failed", err)
			return
		}
	}
	iface2 = iface
	return
}

