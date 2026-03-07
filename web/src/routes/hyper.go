/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package routes

import (
	"context"
	"fmt"
	"net/http"
	"os"

	. "web/src/common"
	"web/src/dbs"
	"web/src/model"

	"github.com/go-macaron/session"
	macaron "gopkg.in/macaron.v1"
)

var (
	hyperAdmin = &HyperAdmin{}
	hyperView  = &HyperView{}
)

type HyperAdmin struct{}
type HyperView struct{}

func (a *HyperAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, hypers []*model.Hyper, err error) {
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
	logger.Infof("Listing hypervisors: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)

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
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckPermission(model.Admin)
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
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckPermission(model.Admin)
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
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckPermission(model.Admin)
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

func (v *HyperView) List(c *macaron.Context, store session.Store) {
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckPermission(model.Admin)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	offset := c.QueryInt64("offset")
	limit := c.QueryInt64("limit")
	if limit == 0 {
		limit = 16
	}
	order := c.Query("order")
	if order == "" {
		order = "hostid"
	}
	query := c.QueryTrim("q")
	total, hypers, err := hyperAdmin.List(c.Req.Context(), offset, limit, order, query)
	if err != nil {
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "500")
		return
	}
	// transform hypers Memory from KB to MB and Disk from B to GB
	for _, hyper := range hypers {
		hyper.Resource.Memory /= 1024                  // Convert from KB to MB
		hyper.Resource.MemoryTotal /= 1024             // Convert from KB to MB
		hyper.Resource.Disk /= 1024 * 1024 * 1024      // Convert from B to GB
		hyper.Resource.DiskTotal /= 1024 * 1024 * 1024 // Convert from B to GB
	}
	pages := GetPages(total, limit)
	c.Data["Hypers"] = hypers
	c.Data["Total"] = total
	c.Data["Pages"] = pages
	c.Data["Query"] = query
	c.HTML(200, "hypers")
}

func (v *HyperView) Edit(c *macaron.Context, store session.Store) {
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckPermission(model.Admin)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	uuid := c.Params(":uuid")
	hyper, err := hyperAdmin.GetHyperByUUID(c.Req.Context(), uuid)
	if err != nil {
		c.Data["ErrorMsg"] = fmt.Sprintf("Specified hypervisor (%s) not found, %+v", uuid, err)
		c.HTML(500, "error")
		return
	}

	// Load zones for the dropdown
	_, zones, err := zoneAdmin.List(c.Req.Context(), 0, 1000, "name", "")
	if err != nil {
		logger.Error("Failed to load zones", err)
		c.Data["ErrorMsg"] = "Failed to load zones"
		c.HTML(500, "error")
		return
	}

	c.Data["Hyper"] = hyper
	c.Data["Zones"] = zones
	c.HTML(200, "hypers_patch")
}

func (v *HyperView) SetHyperStatus(c *macaron.Context, store session.Store) {
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckPermission(model.Admin)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	uuid := c.Params(":uuid")
	status := c.QueryInt("status")
	if uuid == "" || (status != 0 && status != 1) {
		c.Data["ErrorMsg"] = "Invalid UUID or status"
		c.HTML(400, "error")
		return
	}
	hyper, err := hyperAdmin.GetHyperByUUID(c.Req.Context(), uuid)
	if err != nil {
		c.Data["ErrorMsg"] = fmt.Sprintf("Specified hypervisor (%s) not found, %+v", uuid, err)
		c.HTML(500, "error")
		return
	}
	if err := hyperAdmin.SetStatus(c.Req.Context(), hyper.Hostid, int32(status)); err != nil {
		c.Data["ErrorMsg"] = fmt.Sprintf("Failed to set hypervisor status: %v", err)
		c.HTML(500, "error")
		return
	}
	c.Redirect("/hypers")
}

func (a *HyperAdmin) AllocateHostID(ctx context.Context) (hostID int32, err error) {
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

func (a *HyperAdmin) Deploy(ctx context.Context, ip, hostname, networkDevice, vlanDevice, dnsServer, domain, zoneName, virtType string) (hyper *model.Hyper, deployCmd string, err error) {
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckPermission(model.Admin)
	if !permit {
		return nil, "", NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)

	// Check if hostname already exists
	var hostID int32
	existing := &model.Hyper{}
	if err = db.Where("hostname = ?", hostname).Take(existing).Error; err == nil {
		// If exists, only allow retry if status is deploying or failed
		if existing.Status == 4 || existing.Status == 5 { // HYPER_DEPLOYING or HYPER_DEPLOY_FAILED
			logger.Infof("Retrying deployment for existing hypervisor: %s (HostID: %d, NewIP: %s)", hostname, existing.Hostid, ip)
			hostID = existing.Hostid
			hyper = existing
			hyper.HostIP = ip
			hyper.VirtType = virtType
			hyper.Status = 4 // Reset to deploying
			if err = db.Save(hyper).Error; err != nil {
				return nil, "", NewCLError(ErrSQLSyntaxError, "Failed to update existing hypervisor record", err)
			}
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
		"export CONTROLLER_IP=%s HOSTNAME=%s NETWORK_DEVICE=%s VLAN_DEVICE=%s DNS_SERVER=%s SCI_CLIENT_ID=%d DOMAIN=%s ZONE_NAME=%s VIRT_TYPE=%s; "+
			"curl -sSL %s | sudo -E bash",
		controllerIP, hostname, networkDevice, vlanDevice, dnsServer, hostID, domain, zoneName, virtType,
		deployScriptURL,
	)

	return hyper, deployCmd, nil
}

func (a *HyperAdmin) Maintain(ctx context.Context, hostID int32, migrate bool, targetHyper int32) (err error) {
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckPermission(model.Admin)
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
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckPermission(model.Admin)
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

	logger.Infof("Hypervisor %d deleted successfully from database (Hostname: %s)", hostID, hyper.Hostname)
	return nil
}

func (v *HyperView) Deploy(c *macaron.Context, store session.Store) {
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckPermission(model.Admin)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	ip := c.QueryTrim("ip")
	user := c.QueryTrim("user")
	password := c.QueryTrim("password")
	hostname := c.QueryTrim("hostname")
	networkDevice := c.QueryTrim("network_device")
	vlanDevice := c.QueryTrim("vlan_device")
	dnsServer := c.QueryTrim("dns_server")
	domain := c.QueryTrim("domain")
	zoneName := c.QueryTrim("zone_name")
	virtType := c.QueryTrim("virt_type")

	if ip == "" || user == "" || password == "" || hostname == "" {
		c.Data["ErrorMsg"] = "ip, user, password, and hostname are required"
		c.HTML(400, "error")
		return
	}
	if networkDevice == "" {
		networkDevice = "eth0"
	}
	if vlanDevice == "" {
		vlanDevice = networkDevice
	}
	if dnsServer == "" {
		dnsServer = "8.8.8.8"
	}
	if domain == "" {
		domain = "example.com"
	}
	if zoneName == "" {
		// zoneName = "zone0"
	}
	if virtType == "" {
		virtType = "kvm-x86_64"
	}

	hyper, deployCmd, err := hyperAdmin.Deploy(c.Req.Context(), ip, hostname, networkDevice, vlanDevice, dnsServer, domain, zoneName, virtType)
	if err != nil {
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "error")
		return
	}
	logger.Infof("Hypervisor record created/updated for %s. Manual deploy command: %s", hostname, deployCmd)
	c.Data["Hyper"] = hyper
	c.Redirect("/hypers")
}

func (v *HyperView) Maintain(c *macaron.Context, store session.Store) {
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckPermission(model.Admin)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	uuid := c.Params(":uuid")
	if uuid == "" {
		c.Data["ErrorMsg"] = "Invalid UUID"
		c.HTML(400, "error")
		return
	}
	migrate := c.QueryBool("migrate")
	targetHyper := int32(c.QueryInt("target_hyper"))
	hyper, err := hyperAdmin.GetHyperByUUID(c.Req.Context(), uuid)
	if err != nil {
		c.Data["ErrorMsg"] = fmt.Sprintf("Specified hypervisor (%s) not found, %+v", uuid, err)
		c.HTML(500, "error")
		return
	}
	if err := hyperAdmin.Maintain(c.Req.Context(), hyper.Hostid, migrate, targetHyper); err != nil {
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "error")
		return
	}
	c.Redirect("/hypers")
}

func (v *HyperView) Patch(c *macaron.Context, store session.Store) {
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckPermission(model.Admin)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	status := c.QueryInt("status")
	zoneID := c.QueryInt64("zone_id")
	cpuOverRate := c.QueryFloat64("cpu_over_rate")
	memOverRate := c.QueryFloat64("mem_over_rate")
	diskOverRate := c.QueryFloat64("disk_over_rate")
	remark := c.QueryTrim("remark")
	if status < 0 || status > 1 {
		c.Data["ErrorMsg"] = "Invalid status value"
		c.HTML(400, "error")
		return
	}
	if cpuOverRate < 1 || memOverRate < 1 || diskOverRate < 1 {
		c.Data["ErrorMsg"] = "Over commit rates must be greater than or equal to 1"
		c.HTML(400, "error")
		return
	}
	uuid := c.Params(":uuid")
	hyper, err := hyperAdmin.GetHyperByUUID(c.Req.Context(), uuid)
	if err != nil {
		c.Data["ErrorMsg"] = fmt.Sprintf("Specified hypervisor (%s) not found, %+v", uuid, err)
		c.HTML(500, "error")
		return
	}
	if err := hyperAdmin.SetStatus(c.Req.Context(), hyper.Hostid, int32(status)); err != nil {
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "error")
		return
	}
	zone, err := zoneAdmin.Get(c.Req.Context(), zoneID)
	if err != nil {
		c.Data["ErrorMsg"] = fmt.Sprintf("Failed to get zone(%d), %+v", zoneID, err)
		c.HTML(500, "error")
		return
	}
	hyper.ZoneID = zone.ID
	hyper.Zone = zone

	hyper.Status = int32(status)
	hyper.Remark = remark
	hyper.CpuOverRate = float32(cpuOverRate)
	hyper.MemOverRate = float32(memOverRate)
	hyper.DiskOverRate = float32(diskOverRate)
	if err := hyperAdmin.Update(c.Req.Context(), hyper); err != nil {
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "error")
		return
	}
	c.Redirect("/hypers")
}
