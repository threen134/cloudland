/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"
)

func init() {
	Add("set_vrrp_ip", SetVrrpIp)
}

func UpdateLoadBalancerStatus(ctx context.Context, vrrpInstance *model.VrrpInstance) (err error) {
	ctx, db := GetContextDB(ctx)
	err = db.Model(&model.LoadBalancer{}).Where("vrrp_instance_id = ?", vrrpInstance.ID).Updates(map[string]interface{}{
		"status": "available"}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update load balancer status", err)
	}
	return
}

// vrrpDispatchArgs reads the VRRP instance and the role from the set_vrrp_ip.sh command clapi dispatched:
// '<router>' '<vrrp ID>' '<vlan>' '<mac>' '<ip>' '<peer mac>' '<peer ip>' '<role>' ['true'], after the
// script name in args[0]. cland hands it back as is when no node could take it (error=resource).
func vrrpDispatchArgs(args []string) (vrrpID int64, role string, ok bool) {
	if len(args) < 9 {
		return
	}
	vrrpID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil || vrrpID <= 0 || (args[8] != "MASTER" && args[8] != "BACKUP") {
		return 0, "", false
	}
	return vrrpID, args[8], true
}

// vrrpPlacementFailed handles a set_vrrp_ip.sh that cland could not give to any node: the candidates of
// the zone exist in the database but none is connected. A missing BACKUP leaves the instance on its
// MASTER node alone, as when the zone has no second node; a missing MASTER leaves it nowhere. Nothing is
// returned as an error: cland would only retry the same callback.
func vrrpPlacementFailed(ctx context.Context, args []string) {
	vrrpID, role, ok := vrrpDispatchArgs(args)
	if !ok {
		logger.Ctx(ctx).Errorf("set_vrrp_ip.sh found no node, and its arguments cannot be read: %v", args)
		return
	}
	logger.Ctx(ctx).Warningf("set_vrrp_ip.sh for the %s interface of VRRP instance %d found no connected node", role, vrrpID)
	if role == "BACKUP" {
		if err := UpdateLoadBalancerStatus(ctx, &model.VrrpInstance{Model: model.Model{ID: vrrpID}}); err != nil {
			return
		}
		if err := services.VpnGatewayBackupUnplaced(ctx, vrrpID, "no connected compute node could take the backup"); err != nil {
			logger.Ctx(ctx).Errorf("Failed to go on with VPN gateway of VRRP instance %d on one node: %v", vrrpID, err)
		}
		return
	}
	if err := services.VpnGatewayUnplaced(ctx, vrrpID, "no connected compute node in the zone could take the gateway"); err != nil {
		logger.Ctx(ctx).Errorf("Failed to mark the VPN gateway of VRRP instance %d: %v", vrrpID, err)
	}
}

// clearVrrpCopy removes from a node the copy of a VRRP interface that is recorded on another node, then
// re-sends the fdb entries of all the VRRP interfaces of the router: clear_vrrp_ip.sh deletes them by MAC,
// and the VRRP interfaces of a VPC on one node share their MAC (the same repair as when an instance is
// deleted). Commands to one node run in order, so the clearing is done before the fdb entries come back.
func clearVrrpCopy(ctx context.Context, vrrpInstance *model.VrrpInstance, role string, iface *model.Interface, hostid int32, mac string) (err error) {
	ctx, db := GetContextDB(ctx)
	peerRole := "MASTER"
	if role == "MASTER" {
		peerRole = "BACKUP"
	}
	peer := &model.Interface{}
	if err = db.Preload("Address").Where("type = 'vrrp' and name = ? and device = ?", peerRole, vrrpInstance.ID).Take(peer).Error; err != nil {
		return
	}
	if iface.Address == nil || peer.Address == nil || vrrpInstance.VrrpSubnet == nil {
		return fmt.Errorf("VRRP instance %d misses an address or its subnet", vrrpInstance.ID)
	}
	control := fmt.Sprintf("inter=%d", hostid)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_vrrp_ip.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s'", vrrpInstance.RouterID, vrrpInstance.ID, vrrpInstance.VrrpSubnet.Vlan,
		ShellEscape(iface.Address.Address), ShellEscape(mac), ShellEscape(peer.Address.Address), ShellEscape(peer.MacAddr))
	if err = HyperExecute(ctx, control, command); err != nil {
		return
	}
	ResyncRouterVrrpFdb(ctx, vrrpInstance.RouterID, 0)
	return
}

func SetVrrpIp(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| set_vrrp_ip.sh '1' '0' 'MASTER'
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	// These are the arguments of the dispatched command, not of a node's callback
	if ctx.Value("error") == "resource" {
		vrrpPlacementFailed(ctx, args)
		return
	}
	argn := len(args)
	if argn < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	vrrpID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || vrrpID < 0 {
		logger.Ctx(ctx).Error("Invalid vrrp ID", err)
		return
	}
	hyperID, err := strconv.Atoi(args[2])
	if err != nil || hyperID < 0 {
		logger.Ctx(ctx).Error("Invalid hypervisor ID", err)
		return
	}
	hyper := &model.Hyper{}
	err = db.Where("hostid = ?", hyperID).Take(hyper).Error
	if err != nil || hyper.Hostid < 0 {
		logger.Ctx(ctx).Error("Failed to query hypervisor")
		return
	}
	role := args[3]
	vrrpInstance := &model.VrrpInstance{Model: model.Model{ID: vrrpID}}
	err = db.Preload("VrrpSubnet").Take(vrrpInstance).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query vrrp instance", err)
		return
	}
	macAddr := args[4]
	vrrpIface := &model.Interface{}
	err = db.Preload("Address").Preload("Address.Subnet").Where("type = 'vrrp' and name = ? and device = ?", role, vrrpID).Take(vrrpIface).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query vrrp interface", err)
		return
	}
	// Checked before recording the node: the same interface built on a second node. It happens when its
	// set_vrrp_ip.sh was sent twice, e.g. cland retried a MASTER callback whose transaction failed after it
	// had sent the BACKUP's, and the scheduler picked another node the second time; both nodes would then
	// hold the same address and MAC. The node recorded first keeps it (the fdb entries, the peer and the VPN
	// push may already follow it) and the late one is cleared. This used to compare after the update, which
	// never matched.
	if vrrpIface.Hyper >= 0 && vrrpIface.Hyper != int32(hyperID) {
		logger.Ctx(ctx).Errorf("VRRP interface %d is on node %d already, clearing the copy node %d built", vrrpIface.ID, vrrpIface.Hyper, hyperID)
		if err = clearVrrpCopy(ctx, vrrpInstance, role, vrrpIface, int32(hyperID), macAddr); err != nil {
			logger.Ctx(ctx).Error("Failed to clear the duplicated vrrp interface", err)
		}
		// Nothing is recorded, and cland has no reason to retry
		err = nil
		return
	}
	err = db.Model(&model.Interface{}).Where("id = ?", vrrpIface.ID).Updates(map[string]interface{}{
		"hyper":    hyperID,
		"mac_addr": macAddr,
	}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update interface", err)
		return
	}
	vrrpIface.Hyper, vrrpIface.MacAddr = int32(hyperID), macAddr
	err = sendFdbRules(ctx, nil, vrrpInstance, vrrpIface)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to send fdb rules for interface", err)
		return
	}
	if role == "MASTER" {
		err = db.Model(&model.VrrpInstance{Model: model.Model{ID: vrrpID}}).Updates(map[string]interface{}{
			"hyper": hyperID}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update vrrp ", err)
		}
		vrrpIface2 := &model.Interface{}
		err = db.Preload("Address").Preload("Address.Subnet").Where("type = 'vrrp' and name = 'BACKUP' and device = ?", vrrpID).Take(vrrpIface2).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query vrrp interface 2", err)
			return
		}
		err = services.PlaceVrrpBackup(ctx, vrrpInstance, vrrpIface, vrrpIface2)
		var clErr *CLError
		if errors.As(err, &clErr) && clErr.Code == ErrNoQualifiedHypervisor {
			// The zone has no other available node: a load balancer runs on this one alone, and so does an
			// active_standby VPN gateway until the watchdog finds it a second node
			logger.Ctx(ctx).Warningf("VRRP instance %d has no node for its BACKUP interface: %v", vrrpID, err)
			if err = UpdateLoadBalancerStatus(ctx, vrrpInstance); err != nil {
				logger.Ctx(ctx).Error("Failed to update load balancer", err)
				return
			}
			// Logged, not returned: an error here would roll back the MASTER placement recorded above, and
			// cland would only retry the same callback (vrrpPlacementFailed does the same)
			if vpnErr := services.VpnGatewayBackupUnplaced(ctx, vrrpID, "the zone has no other available compute node"); vpnErr != nil {
				logger.Ctx(ctx).Error("Failed to go on with the VPN gateway on one node", vpnErr)
			}
			return
		}
		if err != nil {
			logger.Ctx(ctx).Error("set vrrp ip execution failed", err)
			return
		}
	} else {
		err = db.Model(&model.VrrpInstance{Model: model.Model{ID: vrrpID}}).Updates(map[string]interface{}{
			"peer": hyperID}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update vrrp ", err)
		}
		err = UpdateLoadBalancerStatus(ctx, vrrpInstance)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update load balancer", err)
			return
		}
		// A BACKUP node that came late (the watchdog found the zone a second node): a load balancer that
		// already has floating IPs runs keepalived and haproxy on its MASTER alone so far. Logged, not
		// returned, as a failure would roll back the BACKUP placement recorded above
		if lbErr := services.LoadBalancerBackupPlaced(ctx, vrrpID); lbErr != nil {
			logger.Ctx(ctx).Error("Failed to push the load balancer to its BACKUP node", lbErr)
		}
		// A VPN gateway sharing this VRRP instance can only be pushed to its pair now that both
		// interfaces know their node
		if err = services.VpnGatewayVrrpReady(ctx, vrrpInstance.ID); err != nil {
			logger.Ctx(ctx).Error("Failed to dispatch VPN gateway", err)
			return
		}
	}
	return
}
