/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"context"
	"encoding/json"
	"fmt"

	"api/src/model"
)

// FdbRule is one VXLAN forwarding / neighbor entry sent to add_fwrule.sh
type FdbRule struct {
	Instance string `json:"instance"`
	Vni      int64  `json:"vni"`
	InnerIP  string `json:"inner_ip"`
	InnerMac string `json:"inner_mac"`
	OuterIP  string `json:"outer_ip"`
	Gateway  string `json:"gateway"`
	Router   int64  `json:"router"`
}

// RouterHyperSet returns the nodes hosting a VPC router: every node with an interface of the router
// (instance NICs and VRRP NICs, not the gateway ports)
func RouterHyperSet(ctx context.Context, routerID int64) (hypers []int32, err error) {
	ctx, db := GetContextDB(ctx)
	rows := []struct{ Hyper int32 }{}
	err = db.Model(&model.Interface{}).Select("distinct hyper").Where("router_id = ? and type <> 'gateway' and hyper >= 0", routerID).Scan(&rows).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query router hypers", err)
		return
	}
	for _, row := range rows {
		hypers = append(hypers, row.Hyper)
	}
	return
}

// SendFdbRules pushes the forwarding entries of the given instance (or VRRP interface) to every other
// node hosting the same VPC, and the entries of all the other interfaces of the VPC to the node itself.
// The node set is built from the interfaces of the router, so a node only receives entries for VPCs it hosts.
func SendFdbRules(ctx context.Context, instance *model.Instance, vrrpInstance *model.VrrpInstance, instIface *model.Interface) (err error) {
	if instance != nil && instance.RouterID == 0 {
		// Classic network without a VPC router needs no fdb entries
		logger.Ctx(ctx).Debug("No need to send fdb for classic")
		return
	}
	ctx, db := GetContextDB(ctx)
	localRules := []*FdbRule{}
	spreadRules := []*FdbRule{}
	hyperNode := int32(-1)
	var interfaces []*model.Interface
	routerID := int64(0)
	if instance != nil {
		hyperNode = instance.Hyper
		interfaces = instance.Interfaces
		routerID = instance.RouterID
	} else if vrrpInstance != nil {
		routerID = vrrpInstance.RouterID
	}
	if instIface != nil {
		hyperNode = instIface.Hyper
		interfaces = []*model.Interface{instIface}
	}
	if hyperNode == -1 {
		logger.Ctx(ctx).Error("Invalid hyper node")
		return
	}
	hyper := &model.Hyper{}
	err = db.Where("hostid = ?", hyperNode).Take(hyper).Error
	if err != nil || hyper.Hostid < 0 {
		logger.Ctx(ctx).Error("Failed to query hypervisor")
		return
	}
	for _, iface := range interfaces {
		if iface.Address == nil || iface.Address.Subnet == nil {
			// Callers must preload Address.Subnet; a NIC without an address has nothing to spread
			logger.Ctx(ctx).Errorf("Interface %d has no address or subnet loaded, skipping its fdb rules", iface.ID)
			continue
		}
		subnetType := iface.Address.Subnet.Type
		if subnetType != string(Public) && subnetType != string(Private) {
			spreadRules = append(spreadRules, &FdbRule{Instance: iface.Name, Vni: iface.Address.Subnet.Vlan, InnerIP: iface.Address.Address, InnerMac: iface.MacAddr, OuterIP: hyper.HostIP, Gateway: iface.Address.Subnet.Gateway, Router: iface.Address.Subnet.RouterID})
		}
	}
	allIfaces := []*model.Interface{}
	hyperSet := make(map[int32]struct{})
	err = db.Preload("Address").Preload("Address.Subnet").Preload("Address.Subnet.Router").Where("router_id = ? and type <> 'gateway' and hyper <> ?", routerID, hyperNode).Find(&allIfaces).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query all interfaces", err)
		return
	}
	for _, iface := range allIfaces {
		if iface.Address == nil || iface.Address.Subnet == nil {
			continue
		}
		subnetType := iface.Address.Subnet.Type
		if subnetType == "public" || subnetType == "private" {
			continue
		}
		if iface.Hyper == -1 {
			continue
		}
		hyper := &model.Hyper{}
		hyperErr := db.Where("hostid = ? and hostid != ?", iface.Hyper, hyperNode).Take(hyper).Error
		if hyperErr != nil {
			logger.Ctx(ctx).Error("Failed to query hypervisor", hyperErr)
			continue
		}
		if iface.Hyper >= 0 {
			hyperSet[iface.Hyper] = struct{}{}
		}
		localRules = append(localRules, &FdbRule{Instance: iface.Name, Vni: iface.Address.Subnet.Vlan, InnerIP: iface.Address.Address, InnerMac: iface.MacAddr, OuterIP: hyper.HostIP, Gateway: iface.Address.Subnet.Gateway, Router: iface.Address.Subnet.RouterID})
	}
	if len(hyperSet) > 0 && len(spreadRules) > 0 {
		hyperList := fmt.Sprintf("group-fdb-%d", hyperNode)
		i := 0
		for key := range hyperSet {
			if i == 0 {
				hyperList = fmt.Sprintf("%s:%d", hyperList, key)
			} else {
				hyperList = fmt.Sprintf("%s,%d", hyperList, key)
			}
			i++
		}
		fdbJson, _ := json.Marshal(spreadRules)
		control := "toall=" + hyperList
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/add_fwrule.sh <<'EOF'\n%s\nEOF", fdbJson)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Ctx(ctx).Error("Add_fwrule execution failed", err)
			return
		}
	}
	if len(localRules) > 0 {
		fdbJson, _ := json.Marshal(localRules)
		control := fmt.Sprintf("inter=%d", hyperNode)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/add_fwrule.sh <<'EOF'\n%s\nEOF", fdbJson)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Ctx(ctx).Error("Add_fwrule execution failed", err)
			return
		}
	}
	return
}

// ResyncRouterVrrpFdb re-sends the forwarding entries of every remaining VRRP interface of a router.
// clear_vrrp_ip.sh deletes fdb entries by MAC, and all VRRP interfaces of one VPC on one node share a
// MAC, so removing one VRRP instance (a load balancer or a VPN gateway) also removes the entries the
// others rely on for their unicast heartbeats; without this the survivors end up with two masters until
// something else happens to resend the entries.
func ResyncRouterVrrpFdb(ctx context.Context, routerID, excludeVrrpID int64) {
	ctx, db := GetContextDB(ctx)
	instances := []*model.VrrpInstance{}
	if err := db.Where("router_id = ? and id <> ?", routerID, excludeVrrpID).Find(&instances).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query vrrp instances of router %d: %v", routerID, err)
		return
	}
	for _, instance := range instances {
		ifaces := []*model.Interface{}
		if err := db.Preload("Address").Preload("Address.Subnet").Where("type = 'vrrp' and device = ? and hyper >= 0", instance.ID).Find(&ifaces).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to query vrrp interfaces of instance %d: %v", instance.ID, err)
			continue
		}
		for _, iface := range ifaces {
			if err := SendFdbRules(ctx, nil, instance, iface); err != nil {
				logger.Ctx(ctx).Errorf("Failed to resend fdb rules for vrrp interface %d: %v", iface.ID, err)
			}
		}
	}
}
