/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"fmt"
	"os"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var (
	hyperAdmin = &HyperAdmin{}
)

type HyperAdmin struct{}

func (a *HyperAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, hypers []*model.Hyper, err error) {
	logger.Infof("ENTER HyperAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT HyperAdmin.List: total=%d, count=%d", total, len(hypers))
		}
	}()
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "hostid"
	}
	if query != "" {
		query = fmt.Sprintf("hostname like '%%%s%%'", query)
	}

	hypers = []*model.Hyper{}
	if err = db.Model(&model.Hyper{}).Where("hostid >= 0").Where(query).Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count hypervisors", err)
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("Zone").Where("hostid >= 0").Where(query).Find(&hypers).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to retrieve hypervisors", err)
	}
	db = db.Offset(0).Limit(-1)
	for _, hyper := range hypers {
		hyper.Resource = &model.Resource{}
		if lerr := db.Where("hostid = ?", hyper.Hostid).Take(hyper.Resource).Error; lerr != nil {
			logger.Warningf("Hypervisor %s (hostid: %d) has no associated resource record: %+v", hyper.Hostname, hyper.Hostid, lerr)
		}
	}

	return
}

func (a *HyperAdmin) GetHyperByUUID(ctx context.Context, uuid string) (hyper *model.Hyper, err error) {
	logger.Infof("ENTER HyperAdmin.GetHyperByUUID: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.GetHyperByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT HyperAdmin.GetHyperByUUID: success, hostID=%d", hyper.Hostid)
		}
	}()
	_, db := GetContextDB(ctx)
	hyper = &model.Hyper{}
	if err = db.Preload("Zone").Where("uuid = ?", uuid).Take(hyper).Error; err != nil {
		logger.Error("Failed to query hypervisor by UUID", err)
		return nil, NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	hyper.Resource = &model.Resource{}
	if lerr := db.Where("hostid = ?", hyper.Hostid).Take(hyper.Resource).Error; lerr != nil {
		logger.Warningf("Hypervisor %s (hostid: %d, uuid: %s) has no associated resource record: %+v", hyper.Hostname, hyper.Hostid, hyper.UUID, lerr)
	}
	return
}

func (a *HyperAdmin) SetStatus(ctx context.Context, hostID int32, status int32) (err error) {
	logger.Infof("ENTER HyperAdmin.SetStatus: hostID=%d, status=%d", hostID, status)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.SetStatus: error=%v", err)
		} else {
			logger.Info("EXIT HyperAdmin.SetStatus: success")
		}
	}()
	hyper, err := a.GetHyperByHostid(ctx, hostID)
	if err != nil {
		return
	}
	if hyper.Status == status {
		return nil // No change needed
	}
	hyper.Status = status
	return a.Update(ctx, hyper)
}

// Update function is used to:
// 1. set the hypervisor status active or disabled
// 2. modify the hypervisor remark
// 3. modify the zone of the hypervisor
// 4. modify the over commit rates of hypervisor
func (a *HyperAdmin) Update(ctx context.Context, hyper *model.Hyper) (err error) {
	logger.Infof("ENTER HyperAdmin.Update: hostID=%d, status=%d, zoneID=%d", hyper.Hostid, hyper.Status, hyper.ZoneID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT HyperAdmin.Update: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		logger.Error("Not authorized for this operation", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	hyperInDB := &model.Hyper{}
	hyperInDB.ID = hyper.ID
	if err = db.Preload("Zone").Take(hyperInDB).Error; err != nil {
		logger.Error("Specified hypervisor not found", err)
		return NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	// Update the hypervisor status, remark, or zone
	callScript := false
	restartCloudlet := 0
	if hyper.Status != hyperInDB.Status {
		logger.Info("Updating hypervisor status from", hyperInDB.GetStatus(), "to", hyper.GetStatus())
		hyperInDB.Status = hyper.Status
		callScript = true
	}
	// update remark
	hyperInDB.Remark = hyper.Remark
	if hyper.ZoneID != hyperInDB.ZoneID {
		logger.Info("Updating hypervisor zone from", hyperInDB.ZoneID, "to", hyper.ZoneID)
		zone, err := zoneAdmin.Get(ctx, hyper.ZoneID)
		if err != nil {
			logger.Errorf("Failed to get zone(%d), %+v", hyper.ZoneID, err)
			return err
		}
		hyperInDB.Zone = zone
		hyperInDB.ZoneID = zone.ID
		callScript = true
		restartCloudlet = 1
	}
	// update over commit rates
	if hyper.CpuOverRate != hyperInDB.CpuOverRate {
		logger.Info("Updating hypervisor CPU over commit rate from", hyperInDB.CpuOverRate, "to", hyper.CpuOverRate)
		hyperInDB.CpuOverRate = hyper.CpuOverRate
		callScript = true
	}
	if hyper.MemOverRate != hyperInDB.MemOverRate {
		logger.Info("Updating hypervisor memory over commit rate from", hyperInDB.MemOverRate, "to", hyper.MemOverRate)
		hyperInDB.MemOverRate = hyper.MemOverRate
		callScript = true
	}
	if hyper.DiskOverRate != hyperInDB.DiskOverRate {
		logger.Info("Updating hypervisor disk over commit rate from", hyperInDB.DiskOverRate, "to", hyper.DiskOverRate)
		hyperInDB.DiskOverRate = hyper.DiskOverRate
		callScript = true
	}
	if err = db.Save(hyperInDB).Error; err != nil {
		logger.Error("Failed to update hypervisor", err)
		return
	}
	if callScript {
		logger.Info("Calling script to update hypervisor status")
		// Call the script to update hypervisor status
		control := fmt.Sprintf("inter=%d", hyperInDB.Hostid)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/update_hyper.sh '%d' '%s' '%d' '%f' '%f' '%f'",
			hyperInDB.Status, hyperInDB.Zone.Name, restartCloudlet, hyperInDB.CpuOverRate, hyperInDB.MemOverRate, hyperInDB.DiskOverRate)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Errorf("Failed to call script update hyper %+v", err)
			return
		}
		logger.Infof("Successfully updated hypervisor %d via script", hyperInDB.Hostid)
	}
	return
}

func (a *HyperAdmin) GetHyperByHostid(ctx context.Context, hostid int32) (hyper *model.Hyper, err error) {
	logger.Infof("ENTER HyperAdmin.GetHyperByHostid: hostid=%d", hostid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.GetHyperByHostid: error=%v", err)
		} else {
			logger.Infof("EXIT HyperAdmin.GetHyperByHostid: success, hostname=%s", hyper.Hostname)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		logger.Error("Not authorized for this operation", err)
		return
	}
	_, db := GetContextDB(ctx)
	hyper = &model.Hyper{}
	if err = db.Preload("Zone").Where("hostid = ?", hostid).Take(hyper).Error; err != nil {
		logger.Error("Failed to query hypervisor", err)
		return nil, NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}

	// Load resource information
	hyper.Resource = &model.Resource{}
	if lerr := db.Where("hostid = ?", hyper.Hostid).Take(hyper.Resource).Error; lerr != nil {
		logger.Warningf("Hypervisor %s (hostid: %d) has no associated resource record: %+v", hyper.Hostname, hyper.Hostid, lerr)
		// If no resource record, initialize with defaults
		hyper.Resource = &model.Resource{
			Hostid: hyper.Hostid,
		}
	}
	return
}

func (a *HyperAdmin) GetHyperByHostname(ctx context.Context, hostname string) (hyper *model.Hyper, err error) {
	logger.Infof("ENTER HyperAdmin.GetHyperByHostname: hostname=%s", hostname)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.GetHyperByHostname: error=%v", err)
		} else {
			logger.Infof("EXIT HyperAdmin.GetHyperByHostname: success, hostID=%d", hyper.Hostid)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		logger.Error("Not authorized for this operation", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	hyper = &model.Hyper{}
	if err = db.Where("hostname = ?", hostname).Take(hyper).Error; err != nil {
		logger.Error("Failed to query hypervisor", err)
		return nil, NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	return
}

func (a *HyperAdmin) AllocateHostID(ctx context.Context) (hostID int32, err error) {
	logger.Infof("ENTER HyperAdmin.AllocateHostID")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.AllocateHostID: error=%v", err)
		} else {
			logger.Infof("EXIT HyperAdmin.AllocateHostID: hostID=%d", hostID)
		}
	}()
	_, db := GetContextDB(ctx)
	var maxID int32
	if err = db.Model(&model.Hyper{}).Select("max(hostid)").Row().Scan(&maxID); err != nil {
		// If no records exist, Row().Scan might return an error or maxID will be 0.
		// We ensure it starts from 1 if it's currently unset/0.
		logger.Info("No existing hypervisors found, starting hostID from 1")
		return 1, nil // Start from 1 if no records or error
	}
	// If maxID is 0 (e.g., table is empty and max() returns 0), start from 1.
	// Otherwise, increment the maxID found.
	if maxID < 1 {
		hostID = 1
	} else {
		hostID = maxID + 1
	}
	return hostID, nil
}

func (a *HyperAdmin) Deploy(ctx context.Context, ip, hostname, networkDevice, vlanDevice, privateVlanDevice, dnsServer, domain, zoneName, virtType string) (hyper *model.Hyper, deployCmd string, err error) {
	logger.Infof("ENTER HyperAdmin.Deploy: ip=%s, hostname=%s, zone=%s", ip, hostname, zoneName)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.Deploy: error=%v", err)
		} else {
			logger.Infof("EXIT HyperAdmin.Deploy: success, hostID=%d", hyper.Hostid)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		return nil, "", NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)

	// Check if hostname already exists
	var hostID int32
	existing := &model.Hyper{}
	if err = db.Preload("Zone").Where("hostname = ?", hostname).Take(existing).Error; err == nil {
		// If exists, only allow retry if status is deploying or failed
		if existing.Status == 4 || existing.Status == 5 { // HYPER_DEPLOYING or HYPER_DEPLOY_FAILED
			logger.Infof("Retrying deployment for existing hypervisor: %s (HostID: %d, NewIP: %s)", hostname, existing.Hostid, ip)
			hostID = existing.Hostid
			hyper = existing

			if zoneName != "" {
				var zone *model.Zone
				zone, err = zoneAdmin.GetZoneByName(ctx, zoneName)
				if err != nil {
					return nil, "", NewCLError(ErrZoneNotFound, fmt.Sprintf("Zone '%s' not found, please check your zone information", zoneName), err)
				}
				hyper.ZoneID = zone.ID
				hyper.Zone = zone
			} else if hyper.Zone != nil {
				zoneName = hyper.Zone.Name
			}

			hyper.HostIP = ip
			hyper.VirtType = virtType
			hyper.Status = 4 // Reset to deploying
			if err = db.Save(hyper).Error; err != nil {
				return nil, "", NewCLError(ErrSQLSyntaxError, "Failed to update existing hypervisor record", err)
			}
			RegisterHostInDns(hostname, ip)
		} else {
			return nil, "", NewCLError(ErrHypervisorInvalidState, "Hypervisor with this hostname already exists and is not in a retryable state", nil)
		}
	} else {
		// Allocate unique host ID for new hypervisor
		hostID, err = a.AllocateHostID(ctx)
		if err != nil {
			return nil, "", err
		}

		// Create hyper record in deploying state
		var zone *model.Zone
		if zoneName != "" {
			zone, err = zoneAdmin.GetZoneByName(ctx, zoneName)
			if err != nil {
				return nil, "", NewCLError(ErrZoneNotFound, fmt.Sprintf("Zone '%s' not found, please check your zone information", zoneName), err)
			}
		} else {
			zone, err = zoneAdmin.GetDefaultZone(ctx)
			if err != nil {
				return nil, "", NewCLError(ErrZoneNotFound, "No zone specified and no default zone found in system", err)
			}
		}
		hyper = &model.Hyper{
			Hostid:   hostID,
			Hostname: hostname,
			HostIP:   ip,
			Status:   4, // HYPER_DEPLOYING
			VirtType: virtType,
			ZoneID:   zone.ID,
			Zone:     zone,
		}
		if err = db.Create(hyper).Error; err != nil {
			return nil, "", NewCLError(ErrSQLSyntaxError, "Failed to create hypervisor record", err)
		}
		logger.Infof("Created new hypervisor record for %s (HostID: %d, IP: %s, Zone: %s)", hostname, hostID, ip, zoneName)
		RegisterHostInDns(hostname, ip)
	}

	// Construct deploy command
	controllerIP := os.Getenv("MANAGEMENT_VIP")
	if controllerIP == "" {
		controllerIP = os.Getenv("PUBLIC_IP")
	}
	if controllerIP == "" {
		controllerIP = "127.0.0.1"
	}
	deployScriptURL := os.Getenv("DEPLOY_SCRIPT_URL")
	if deployScriptURL == "" {
		deployScriptURL = "https://raw.githubusercontent.com/threen134/cloudland/staging/deploy/docker/scripts/deploy-compute-node.sh"
	}

	deployCmd = fmt.Sprintf(
		"export CONTROLLER_IP=%s HOSTNAME=%s NETWORK_DEVICE=%s VLAN_DEVICE=%s PRIVATE_VLAN_DEVICE=%s DNS_SERVER=%s SCI_CLIENT_ID=%d DOMAIN=%s ZONE_NAME=%s VIRT_TYPE=%s; "+
			"curl -sSL %s | sudo -E bash",
		controllerIP, hostname, networkDevice, vlanDevice, privateVlanDevice, dnsServer, hostID, domain, zoneName, virtType,
		deployScriptURL,
	)

	return hyper, deployCmd, nil
}

func (a *HyperAdmin) Maintain(ctx context.Context, hostID int32, migrate bool, targetHyper int32) (err error) {
	logger.Infof("ENTER HyperAdmin.Maintain: hostID=%d, migrate=%v, targetHyper=%d", hostID, migrate, targetHyper)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.Maintain: error=%v", err)
		} else {
			logger.Info("EXIT HyperAdmin.Maintain: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		return NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)

	hyper := &model.Hyper{}
	if err = db.Where("hostid = ?", hostID).Take(hyper).Error; err != nil {
		return NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	if hyper.Status != 1 && hyper.Status != 0 {
		return NewCLError(ErrHypervisorInvalidState, "Hypervisor must be active or disabled to maintain", nil)
	}

	// Set status to maintaining
	if err = db.Model(hyper).Update("status", 2).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to update hypervisor status", err)
	}

	if migrate {
		// Find instances on this hyper
		instances := []*model.Instance{}
		if err = db.Where("hyper = ?", hostID).Find(&instances).Error; err != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to query instances", err)
		}

		if len(instances) > 0 {
			// Migrate all instances
			_, err = migrationAdmin.Create(ctx, fmt.Sprintf("maintenance-hyper-%d", hostID), instances, false, targetHyper)
			if err != nil {
				logger.Errorf("Failed to create migrations for maintenance: %v", err)
				return err
			}
		}
	}

	logger.Infof("Hypervisor %d entered maintenance mode (status=%d, migrate=%v, target=%d)", hostID, hyper.Status, migrate, targetHyper)
	return nil
}

func (a *HyperAdmin) Delete(ctx context.Context, hostID int32) (err error) {
	logger.Infof("ENTER HyperAdmin.Delete: hostID=%d", hostID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT HyperAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT HyperAdmin.Delete: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		return NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)

	hyper := &model.Hyper{}
	if err = db.Where("hostid = ?", hostID).Take(hyper).Error; err != nil {
		return NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}

	// Only allow deletion if there are no instances
	var count int64
	if err = db.Model(&model.Instance{}).Where("hyper = ?", hostID).Count(&count).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to count instances", err)
	}
	if count > 0 {
		return NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("Hypervisor still has %d instances, cannot delete", count), nil)
	}

	// Clean up related records
	db.Where("hostid = ?", hostID).Delete(&model.Resource{})

	// Release RouteIP (system interface)
	ifaces := []*model.Interface{}
	if err = db.Where("hyper = ? AND type = 'system'", hostID).Find(&ifaces).Error; err != nil {
		logger.Errorf("Failed to query system interfaces for hypervisor %d: %v", hostID, err)
	} else if len(ifaces) > 0 {
		if err = DeallocateAddress(ctx, ifaces); err != nil {
			logger.Errorf("Failed to deallocate system address for hypervisor %d: %v", hostID, err)
		}
		if err = db.Where("hyper = ? AND type = 'system'", hostID).Delete(&model.Interface{}).Error; err != nil {
			logger.Errorf("Failed to delete system interfaces for hypervisor %d: %v", hostID, err)
		}
	}

	// Remove from SCI if it was registered (Status != 4 is pre-active)
	if hyper.Status != 4 {
		if err = NodeRemove(hostID); err != nil {
			logger.Errorf("Failed to remove node %d from SCI: %v", hostID, err)
			// Decide if this should block DB deletion. Usually better to continue cleanup if SCI fails.
		}
	}

	if err = db.Delete(hyper).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to delete hypervisor record", err)
	}

	RemoveHostFromDns(hyper.Hostname)
	logger.Infof("Hypervisor %d deleted successfully from database (Hostname: %s)", hostID, hyper.Hostname)
	return nil
}
