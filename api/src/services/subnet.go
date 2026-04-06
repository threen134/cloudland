/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"math/rand"
	"net"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/apparentlymart/go-cidr/cidr"
)

var (
	subnetAdmin = &SubnetAdmin{}
	vniMax      = 16777215
	vniMin      = 4096
)

type SubnetAdmin struct{}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func ipToInt(ip net.IP) (*big.Int, int) {
	val := &big.Int{}
	val.SetBytes([]byte(ip))
	if len(ip) == net.IPv4len {
		return val, 32
	} else if len(ip) == net.IPv6len {
		return val, 128
	} else {
		panic(fmt.Errorf("Unsupported address length %d", len(ip)))
	}
}

func getValidVni(ctx context.Context) (vni int, err error) {
	logger.Infof("ENTER getValidVni")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT getValidVni: error=%v", err)
		} else {
			logger.Infof("EXIT getValidVni: vni=%d", vni)
		}
	}()
	ctx, db := GetContextDB(ctx)
	count := 1
	for count > 0 {
		vni = rand.Intn(vniMax-vniMin) + vniMin
		if err = db.Model(&model.Subnet{}).Where("vlan = ?", vni).Count(&count).Error; err != nil {
			logger.Error("Failed to query existing vlan, %v", err)
			return
		}
	}
	return
}

func checkIfExistVni(ctx context.Context, vni int64) (result bool, err error) {
	logger.Infof("ENTER checkIfExistVni: vni=%d", vni)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT checkIfExistVni: error=%v", err)
		} else {
			logger.Infof("EXIT checkIfExistVni: result=%t", result)
		}
	}()
	ctx, db := GetContextDB(ctx)
	count := 0
	if err = db.Model(&model.Subnet{}).Where("vlan = ?", vni).Count(&count).Error; err != nil {
		logger.Error("Failed to query existing vlan, %v", err)
		return
	}
	if count > 0 {
		return true, nil
	} else {
		return false, nil
	}
}

func generateIPAddresses(ctx context.Context, subnet *model.Subnet, start net.IP, end net.IP, preSize int) (err error) {
	logger.Infof("ENTER generateIPAddresses: subnetID=%d, start=%s, end=%s, preSize=%d", subnet.ID, start.String(), end.String(), preSize)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT generateIPAddresses: error=%v", err)
		} else {
			logger.Info("EXIT generateIPAddresses: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	ip := start
	for {
		ipstr := fmt.Sprintf("%s/%d", ip.String(), preSize)
		if ipstr == subnet.Gateway {
			if ip.String() == end.String() {
				break
			} else {
				ip = cidr.Inc(ip)
				ipstr = fmt.Sprintf("%s/%d", ip.String(), preSize)
			}
		}
		address := &model.Address{
			Model:    model.Model{Creater: subnet.Creater},
			Owner:    subnet.Owner,
			Address:  ipstr,
			Netmask:  subnet.Netmask,
			Type:     "ipv4",
			SubnetID: subnet.ID,
		}
		err = db.Create(address).Error
		if err != nil {
			logger.Error("Database create IP address failed, %v", err)
			err = NewCLError(ErrAddressCreateFailed, "Failed to create IP address", err)
			return err
		}
		if ip.String() == end.String() {
			break
		}
		ip = cidr.Inc(ip)
	}
	return nil
}

func (a *SubnetAdmin) Get(ctx context.Context, id int64) (subnet *model.Subnet, err error) {
	logger.Infof("ENTER SubnetAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = fmt.Errorf("Invalid subnet ID: %d", id)
		return
	}
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	subnet = &model.Subnet{Model: model.Model{ID: id}}
	err = db.Preload("Router").Preload("Group").Take(subnet).Error
	if err != nil {
		logger.Error("DB failed to query subnet ", err)
		err = NewCLError(ErrSubnetNotFound, "Subnet not found", err)
		return
	}
	if subnet.RouterID > 0 {
		subnet.Router = &model.Router{Model: model.Model{ID: subnet.RouterID}}
		err = db.Take(subnet.Router).Error
		if err != nil {
			logger.Error("Failed to query router ", err)
			err = NewCLError(ErrRouterNotFound, "Router not found", err)
			return
		}
	}
	if subnet.Type == "internal" {
		permit := memberShip.CheckResourceOrg(model.OrgReader, subnet.Owner)
		if !permit {
			logger.Error("Not authorized to read the subnet")
			err = NewCLError(ErrPermissionDenied, "Not authorized to read the subnet", nil)
			return
		}
	}
	return
}

func (a *SubnetAdmin) GetSubnetByUUID(ctx context.Context, uuID string) (subnet *model.Subnet, err error) {
	logger.Infof("ENTER SubnetAdmin.GetSubnetByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.GetSubnetByUUID: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.GetSubnetByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	subnet = &model.Subnet{}
	err = db.Preload("Router").Preload("Group").Where("uuid = ?", uuID).Take(subnet).Error
	if err != nil {
		err = NewCLError(ErrSubnetNotFound, "Subnet not found", err)
		return
	}
	if subnet.RouterID > 0 {
		subnet.Router = &model.Router{Model: model.Model{ID: subnet.RouterID}}
		err = db.Take(subnet.Router).Error
		if err != nil {
			logger.Error("Failed to query router ", err)
			err = NewCLError(ErrRouterNotFound, "Router not found", err)
			return
		}
	}
	if subnet.Type == "internal" {
		permit := memberShip.CheckResourceOrg(model.OrgReader, subnet.Owner)
		if !permit {
			logger.Error("Not authorized to read the subnet")
			err = NewCLError(ErrPermissionDenied, "Not authorized to read the subnet", nil)
			return
		}
	}
	return
}

func (a *SubnetAdmin) GetSubnetByName(ctx context.Context, name string) (subnet *model.Subnet, err error) {
	logger.Infof("ENTER SubnetAdmin.GetSubnetByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.GetSubnetByName: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.GetSubnetByName: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	subnet = &model.Subnet{}
	err = db.Preload("Router").Preload("Group").Where("name = ?", name).Take(subnet).Error
	if err != nil {
		err = NewCLError(ErrSubnetNotFound, "Subnet not found", err)
		return
	}
	if subnet.RouterID > 0 {
		subnet.Router = &model.Router{Model: model.Model{ID: subnet.RouterID}}
		err = db.Take(subnet.Router).Error
		if err != nil {
			logger.Error("Failed to query router ", err)
			err = NewCLError(ErrRouterNotFound, "Router not found", err)
			return
		}
	}
	if subnet.Type == "internal" {
		permit := memberShip.CheckResourceOrg(model.OrgReader, subnet.Owner)
		if !permit {
			logger.Error("Not authorized to read the subnet")
			err = NewCLError(ErrPermissionDenied, "Not authorized to read the subnet", nil)
			return
		}
	}
	return
}

func (a *SubnetAdmin) GetSubnet(ctx context.Context, reference *BaseReference) (subnet *model.Subnet, err error) {
	logger.Infof("ENTER SubnetAdmin.GetSubnet: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.GetSubnet: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.GetSubnet: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = fmt.Errorf("Subnet base reference must be provided with either uuid or name")
		return
	}
	if reference.ID != "" {
		subnet, err = a.GetSubnetByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		subnet, err = a.GetSubnetByName(ctx, reference.Name)
		return
	}
	return
}

func (a *SubnetAdmin) Update(ctx context.Context, id int64, name, subnetType string, ipGroup *model.IpGroup, priority int32, dhcp bool) (err error) {
	logger.Infof("ENTER SubnetAdmin.Update: id=%d, name=%s, subnetType=%s, ipGroup=%+v, priority=%d, dhcp=%t", id, name, subnetType, ipGroup, priority, dhcp)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	updates := map[string]interface{}{
		"name":     name,
		"type":     subnetType,
		"priority": priority,
		"dhcp":     dhcp,
	}

	if ipGroup != nil {
		updates["group_id"] = ipGroup.ID
	} else {
		updates["group_id"] = 0
	}

	err = db.Model(&model.Subnet{}).Where("id = ?", id).Updates(updates).Error
	if err != nil {
		logger.Errorf("Failed to save subnet, err=%v", err)
		err = NewCLError(ErrSubnetUpdateFailed, "Failed to update subnet", err)
		return
	}
	return
}

func clearRouting(ctx context.Context, routerID int64, subnet *model.Subnet) (err error) {
	logger.Infof("ENTER clearRouting: routerID=%d, subnetID=%d", routerID, subnet.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT clearRouting: error=%v", err)
		} else {
			logger.Info("EXIT clearRouting: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	router := &model.Router{Model: model.Model{ID: routerID}}
	err = db.Take(router).Error
	if err != nil {
		logger.Error("DB failed to query router", err)
		err = NewCLError(ErrRouterNotFound, "Router not found", err)
		return
	}
	if router.Hyper >= 0 {
		control := fmt.Sprintf("toall=router-%d:%d", router.ID, router.Hyper)
		if router.Peer >= 0 {
			control = fmt.Sprintf("%s,%d", control, router.Peer)
		}
		if router.Hyper == router.Peer {
			control = fmt.Sprintf("inter=%d", router.Hyper)
		}
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_gateway.sh '%d' '%s' '%d'", router.ID, subnet.Gateway, subnet.Vlan)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Set gateway failed")
			return
		}
	}
	return
}

func setRouting(ctx context.Context, subnet *model.Subnet, _ bool) (err error) {
	logger.Infof("ENTER setRouting: subnetID=%d", subnet.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT setRouting: error=%v", err)
		} else {
			logger.Info("EXIT setRouting: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	router := &model.Router{Model: model.Model{ID: subnet.RouterID}}
	err = db.Take(router).Error
	if err != nil {
		logger.Error("DB failed to query router", err)
		err = NewCLError(ErrRouterNotFound, "Router not found", err)
		return
	}
	secgroup := &model.SecurityGroup{Model: model.Model{ID: router.DefaultSG}}
	err = db.Take(secgroup).Error
	if err != nil {
		logger.Error("DB failed to query router", err)
		err = NewCLError(ErrSecurityGroupNotFound, "Security group not found", err)
		return
	}
	_, err = (&SecruleAdminService{}).Create(ctx, fmt.Sprintf("subnet-%s-ingress-tcp", strings.ReplaceAll(subnet.Network, "/", "-")), subnet.Network, "ingress", "tcp", 1, 65535, secgroup)
	if err != nil {
		logger.Error("Failed to create security rule", err)
		return
	}
	_, err = (&SecruleAdminService{}).Create(ctx, fmt.Sprintf("subnet-%s-ingress-udp", strings.ReplaceAll(subnet.Network, "/", "-")), subnet.Network, "ingress", "udp", 1, 65535, secgroup)
	if err != nil {
		logger.Error("Failed to create security rule", err)
		return
	}
	_, err = CreateInterface(ctx, subnet, router.ID, router.Owner, router.Hyper, 0, 0, subnet.Gateway, "", "subnet-gw", "gateway", nil, false)
	if err != nil {
		logger.Error("Failed to create gateway subnet interface", err)
		return
	}
	return
}

func (a *SubnetAdmin) Create(ctx context.Context, vlan int, name, network, gateway, start, end, rtype, dns, domain string, dhcp bool, router *model.Router, ipGroup *model.IpGroup, priority int32) (subnet *model.Subnet, err error) {
	logger.Infof("ENTER SubnetAdmin.Create: vlan=%d, name=%s, network=%s, gateway=%s, rtype=%s, dhcp=%t, priority=%d", vlan, name, network, gateway, rtype, dhcp, priority)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.Create: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	if rtype == "public" || rtype == "private" {
		permit = memberShip.IsSystemAdmin()
		if !permit {
			logger.Error("Not authorized for this operation")
			err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
			return
		}
		if router != nil {
			logger.Errorf("%s subnet can not be created in a vpc", rtype)
			err = NewCLError(ErrPublicSubnetCannotInVPC, fmt.Sprintf("Not able to create %s subnet in a vpc", rtype), nil)
			return
		}
	}
	if vlan <= 0 {
		vlan, err = getValidVni(ctx)
		if err != nil {
			logger.Error("Failed to get valid vlan %s, %v", vlan, err)
			return
		}
	}
	owner := memberShip.OrgID
	count := 0
	err = db.Model(&model.Subnet{}).Where("vlan = ?", vlan).Count(&count).Error
	if err != nil {
		logger.Error("Database failed to count network", err)
		err = NewCLError(ErrDatabaseError, "Database failed to count network", err)
		return
	}
	var routerID int64
	if router != nil {
		routerID = router.ID
	}
	var groupID int64
	if ipGroup != nil {
		groupID = ipGroup.ID
	}
	_, ipNet, err := net.ParseCIDR(network)
	if err != nil {
		logger.Error("CIDR parsing failed, %v", err)
		err = NewCLError(ErrInvalidCIDR, "Invalid CIDR", err)
		return
	}
	addrCount := cidr.AddressCount(ipNet)
	if addrCount < 5 || addrCount > 1000 {
		err = NewCLError(ErrCIDRTooBig, "Network/mask must have more than 5 but less than 1000 addresses", nil)
		logger.Error("Invalid address count", err)
		return
	}
	if rtype == "" {
		rtype = "internal"
	}
	first, last := cidr.AddressRange(ipNet)
	preSize, _ := ipNet.Mask.Size()
	if gateway == "" {
		gateway = cidr.Inc(first).String()
	}
	if start == "" {
		start = cidr.Inc(first).String()
	}
	if start == gateway {
		start = cidr.Inc(net.ParseIP(start)).String()
	}
	if end == "" {
		end = cidr.Dec(last).String()
	}
	if end == gateway {
		end = cidr.Dec(net.ParseIP(end)).String()
	}
	gateway = fmt.Sprintf("%s/%d", gateway, preSize)
	netmask := net.IP(net.CIDRMask(preSize, 32)).String()
	subnet = &model.Subnet{
		Model:        model.Model{Creater: memberShip.UserID},
		Owner:        owner,
		Name:         name,
		Network:      network,
		Netmask:      netmask,
		Gateway:      gateway,
		Start:        start,
		End:          end,
		NameServer:   dns,
		DomainSearch: domain,
		Dhcp:         dhcp,
		Vlan:         int64(vlan),
		Type:         rtype,
		RouterID:     routerID,
		GroupID:      groupID,
		Priority:     priority,
	}
	err = db.Create(subnet).Error
	if err != nil {
		logger.Error("Database create subnet failed, %v", err)
		err = NewCLError(ErrSubnetCreateFailed, "Failed to create subnet", err)
		return
	}
	err = db.Preload("Router").Preload("Group").Where("id = ?", subnet.ID).First(&subnet).Error
	if err != nil {
		logger.Error("Error loading subnet details after creation:", err)
		err = NewCLError(ErrDatabaseError, "Error loading subnet details after creation", err)
		return nil, err
	}

	ip := net.ParseIP(start)
	for {
		ipstr := fmt.Sprintf("%s/%d", ip.String(), preSize)
		address := &model.Address{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, Address: ipstr, Netmask: netmask, Type: "ipv4", SubnetID: subnet.ID}
		err = db.Create(address).Error
		if err != nil {
			logger.Error("Database create address failed, %v", err)
			return
		}
		if ip.String() == end {
			break
		}
		ip = cidr.Inc(ip)
		if ipstr == gateway {
			ip = cidr.Inc(ip)
		}
	}
	// Create record for gateway
	address := &model.Address{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, Address: gateway, Netmask: netmask, Type: "ipv4", SubnetID: subnet.ID}
	err = db.Create(address).Error
	if err != nil {
		logger.Error("Database create address for gateway failed, %v", err)
	}
	if subnet.RouterID > 0 {
		err = setRouting(ctx, subnet, false)
		if err != nil {
			logger.Error("Failed to set routing for subnet")
			return
		}
	}
	return
}

func (a *SubnetAdmin) Delete(ctx context.Context, subnet *model.Subnet) (err error) {
	logger.Infof("ENTER SubnetAdmin.Delete: subnetID=%d, uuid=%s", subnet.ID, subnet.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	var permit bool
	if subnet.Type == "public" || subnet.Type == "private" {
		permit = memberShip.IsSystemAdmin()
	} else {
		permit = memberShip.CheckResourceOrg(model.OrgWriter, subnet.Owner)
	}
	if !permit {
		logger.Error("Not authorized to delete the subnet")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the subnet", nil)
		return
	}
	count := 0
	if subnet.Type != "site" {
		count := 0
		err = db.Model(&model.Interface{}).Where("subnet = ? and type <> 'dhcp' and type <> 'gateway'", subnet.ID).Count(&count).Error
		if err != nil {
			logger.Error("Failed to query interfaces", err)
			err = NewCLError(ErrDatabaseError, "Failed to query interfaces", err)
			return
		}
		if count > 0 {
			err = NewCLError(ErrAddressInUse, "Some addresses of this subnet are still in use", nil)
			logger.Error("Some addresses of this subnet are still in use")
			return
		}
	}
	err = db.Model(&model.Subnet{}).Where("vlan = ?", subnet.Vlan).Count(&count).Error
	if err != nil {
		logger.Error("Database failed to count subnet", err)
		err = NewCLError(ErrDatabaseError, "Database failed to count subnet", err)
		return
	}
	subnet.Name = fmt.Sprintf("%s-%d", subnet.Name, subnet.CreatedAt.Unix())
	err = db.Model(subnet).Update("name", subnet.Name).Error
	if err != nil {
		logger.Error("DB failed to update subnet name", err)
		err = NewCLError(ErrSubnetUpdateFailed, "DB failed to update subnet name", err)
		return
	}
	err = db.Delete(subnet).Error
	if err != nil {
		logger.Error("Database delete subnet failed, %v", err)
		err = NewCLError(ErrSubnetDeleteFailed, "Database delete subnet failed", err)
		return
	}
	// delete ip address
	err = db.Where("subnet_id = ?", subnet.ID).Delete(model.Address{}).Error
	if err != nil {
		logger.Error("Database delete ip address failed, %v", err)
		err = NewCLError(ErrAddressDeleteFailed, "Database delete ip address failed", err)
		return
	}
	if subnet.Type == "vrrp" && subnet.RouterID > 0 {
		err = db.Model(&model.Router{Model: model.Model{ID: subnet.RouterID}}).Updates(map[string]interface{}{"vrrp_subnet_id": 0}).Error
		if err != nil {
			logger.Error("DB failed to update router vrrp subnet", err)
			err = NewCLError(ErrDatabaseError, "Database failed to update router vrrp subnet", err)
			return
		}
	}
	// delete floatingip
	var floatingIps []*model.FloatingIp
	err = db.Where("subnet_id = ?", subnet.ID).Find(&floatingIps).Error
	if err != nil {
		logger.Error("Database query floatingip failed, %v", err)
		err = NewCLError(ErrDatabaseError, "Database query floatingip failed", err)
		return
	}
	for _, floatingIp := range floatingIps {
		err = (&FloatingIpAdminService{}).DeallocateFloatingIp(ctx, floatingIp.ID)
		if err != nil {
			logger.Error("Failed to deallocate floatingip %d, %v", floatingIp.ID, err)
			return
		}
	}
	// delete interfaces if any
	err = db.Where("subnet = ?", subnet.ID).Delete(model.Interface{}).Error
	if err != nil {
		logger.Error("Database delete interface failed, %v", err)
		err = NewCLError(ErrAddressDeleteFailed, "Database delete interface failed", err)
		return
	}
	if subnet.RouterID > 0 {
		err = clearRouting(ctx, subnet.RouterID, subnet)
		if err != nil {
			logger.Error("Failed to set routing for subnet")
			return
		}
	}
	return
}

func (a *SubnetAdmin) CountIdleAddressesForSubnet(ctx context.Context, subnet *model.Subnet) (idleCount int64, err error) {
	logger.Infof("ENTER SubnetAdmin.CountIdleAddressesForSubnet: subnetID=%d", subnet.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.CountIdleAddressesForSubnet: error=%v", err)
		} else {
			logger.Infof("EXIT SubnetAdmin.CountIdleAddressesForSubnet: idleCount=%d", idleCount)
		}
	}()
	ctx, db := GetContextDB(ctx)

	err = db.Model(&model.Address{}).
		Where("subnet_id = ?", subnet.ID).
		Where("allocated = ?", "f").
		Where("reserved = ?", "f").
		Where("address != ?", subnet.Gateway).
		Count(&idleCount).Error

	if err != nil {
		if err.Error() != "record not found" {
			err = NewCLError(ErrDatabaseError, fmt.Sprintf("Failed to count idle addresses for subnet %s", subnet.UUID), err)
			return 0, err
		}
	}

	return
}

func (a *SubnetAdmin) CountAddressStatistics(ctx context.Context, subnet *model.Subnet) (total, allocated, reserved, available int64, err error) {
	logger.Infof("ENTER SubnetAdmin.CountAddressStatistics: subnetID=%d", subnet.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.CountAddressStatistics: error=%v", err)
		} else {
			logger.Infof("EXIT SubnetAdmin.CountAddressStatistics: total=%d, available=%d", total, available)
		}
	}()
	ctx, db := GetContextDB(ctx)

	err = db.Model(&model.Address{}).
		Where("subnet_id = ?", subnet.ID).
		Count(&total).Error
	if err != nil {
		err = NewCLError(ErrDatabaseError, fmt.Sprintf("Failed to count total addresses for subnet %s", subnet.UUID), err)
		return
	}

	err = db.Model(&model.Address{}).
		Where("subnet_id = ? AND allocated = ?", subnet.ID, "t").
		Count(&allocated).Error
	if err != nil {
		err = NewCLError(ErrDatabaseError, fmt.Sprintf("Failed to count allocated addresses for subnet %s", subnet.UUID), err)
		return
	}

	err = db.Model(&model.Address{}).
		Where("subnet_id = ? AND reserved = ?", subnet.ID, "t").
		Count(&reserved).Error
	if err != nil {
		err = NewCLError(ErrDatabaseError, fmt.Sprintf("Failed to count reserved addresses for subnet %s", subnet.UUID), err)
		return
	}

	err = db.Model(&model.Address{}).
		Where("subnet_id = ?", subnet.ID).
		Where("allocated = ?", "f").
		Where("reserved = ?", "f").
		Where("address != ?", subnet.Gateway).
		Count(&available).Error
	if err != nil {
		err = NewCLError(ErrDatabaseError, fmt.Sprintf("Failed to count available addresses for subnet %s", subnet.UUID), err)
		return
	}

	return
}

func (a *SubnetAdmin) List(ctx context.Context, offset, limit int64, order, query, intQuery string) (total int64, subnets []*model.Subnet, err error) {
	logger.Infof("ENTER SubnetAdmin.List: offset=%d, limit=%d, order=%s, query=%s, intQuery=%s", offset, limit, order, query, intQuery)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT SubnetAdmin.List: error=%v", err)
		} else {
			logger.Info("EXIT SubnetAdmin.List: success")
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

	// Build owner/visibility filter:
	// SystemAdmin with AllOrgs: no filter (see everything)
	// Regular users: own org subnets + all public/private subnets (shared infrastructure)
	var ownerFilter string
	var ownerArgs []interface{}
	if memberShip.AllOrgs && memberShip.IsSystemAdmin() {
		ownerFilter = ""
		ownerArgs = nil
	} else {
		ownerFilter = "owner = ? OR type IN ('public', 'private')"
		ownerArgs = []interface{}{memberShip.OrgID}
	}
	subnets = []*model.Subnet{}
	if err = db.Model(&model.Subnet{}).Where(ownerFilter, ownerArgs...).Where(query).Where(intQuery).Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count subnets", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("Group").Preload("Router").Where(ownerFilter, ownerArgs...).Where(query).Where(intQuery).Find(&subnets).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Database failed to query subnets", err)
		return
	}
	permit = memberShip.CheckOrgPermission(model.OrgWriter)
	if permit {
		db = db.Offset(0).Limit(-1)
		for _, subnet := range subnets {
			subnet.OwnerInfo = &model.Organization{Model: model.Model{ID: subnet.Owner}}
			if err = db.Take(subnet.OwnerInfo).Error; err != nil {
				logger.Error("Failed to query owner info", err)
				err = NewCLError(ErrUserNotFound, "Failed to query owner info", err)
				return
			}
		}
	}

	return
}
