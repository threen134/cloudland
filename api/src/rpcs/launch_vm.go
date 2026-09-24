/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"gorm.io/gorm"
)

var floatingIpAdmin = services.FloatingIpAdmin

func init() {
	Add("launch_vm", LaunchVM)
}

// sendFdbRules is kept as a thin wrapper: the implementation lives in common so the services package
// (load balancer and VPN gateway deletion) can resend entries too
func sendFdbRules(ctx context.Context, instance *model.Instance, vrrpInstance *model.VrrpInstance, instIface *model.Interface) (err error) {
	return SendFdbRules(ctx, instance, vrrpInstance, instIface)
}

func LaunchVM(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| launch_vm.sh '127' 'running' '3' 'reason'
	outerCtx := ctx
	ctx, db, newTransaction := StartTransaction(ctx)
	// Released after the transaction: a failure rolls it back, and the room held for a boot disk that was never
	// created would then stay reserved until the reservation expires a day later
	releaseBootVolume := int64(0)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
		if releaseBootVolume > 0 {
			services.ReleaseReservations(outerCtx, 0, releaseBootVolume, model.ReservationBoot)
		}
	}()
	argn := len(args)
	if argn < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	instID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	instance := &model.Instance{Model: model.Model{ID: instID}}
	reason := ""
	errHndl := ctx.Value("error")
	if errHndl != nil {
		reason = "Resource is not enough"
		err = db.Model(instance).Updates(map[string]interface{}{
			"status": "error",
			"reason": reason}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update instance", err)
		}
		// The boot disk was never created: give back the room held for it in its pool
		bootVolume := &model.Volume{}
		if db.Where("instance_id = ? AND booting = ?", instID, true).Take(bootVolume).Error == nil {
			releaseBootVolume = bootVolume.ID
		}
		return
	}
	err = db.Preload("Volumes").Take(instance).Error
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		reason = err.Error()
		return
	}
	err = db.Preload("SiteSubnets").Preload("SecurityGroups").Preload("Address").Preload("Address.Subnet").Preload("Address.Subnet.Router").Preload("SecondAddresses", func(db *gorm.DB) *gorm.DB {
		return db.Order("addresses.updated_at")
	}).Preload("SecondAddresses.Subnet").Where("instance = ?", instID).Find(&instance.Interfaces).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to get interfaces", err)
		reason = err.Error()
		return
	}
	serverStatus := args[2]
	hyperID, err := strconv.Atoi(args[3])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid hyper ID", err)
		reason = err.Error()
		return
	}
	reason = args[4]
	// "sync" is how the host reports an instance it found or started after a boot, not a reason to keep on the
	// instance: the column gets cleared instead, which also drops start_failed or storage_pending once it runs
	storedReason := reason
	if reason == "sync" {
		storedReason = ""
	}
	instance.Hyper = int32(hyperID)
	hyper := &model.Hyper{}
	err = db.Where("hostid = ?", hyperID).Take(hyper).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query hypervisor", err)
		return
	}
	instance.ZoneID = hyper.ZoneID
	if instance.Status != model.InstanceStatusMigrating {
		err = db.Model(&model.Instance{Model: model.Model{ID: instID}}).Updates(map[string]interface{}{
			"status": serverStatus,
			"hyper":  int32(hyperID),
			"zoneID": hyper.ZoneID,
			"reason": storedReason}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update instance", err)
			return
		}
		err = db.Model(&model.Interface{}).Where("instance = ?", instance.ID).Updates(map[string]interface{}{"hyper": int32(hyperID)}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update interface", err)
			return
		}
	}
	if reason == "sync" {
		err = syncMigration(ctx, instance)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to sync migration info", err)
		}
		err = syncNicInfo(ctx, instance)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to sync nic info", err)
		}
		if instance.RouterID > 0 {
			err = syncFloatingIp(ctx, instance)
			if err != nil {
				logger.Ctx(ctx).Error("Failed to sync floating ip", err)
			}
		}
	}
	// The node may host this VPC for the first time, or have rebuilt its router after a reboot (sync):
	// give it the VPN gateway routes. Idempotent and cheap, so it runs on every report.
	if instance.RouterID > 0 && instance.Status != model.InstanceStatusMigrating {
		if verr := services.VpnResyncNode(ctx, instance.RouterID, int32(hyperID)); verr != nil {
			logger.Ctx(ctx).Warningf("Failed to sync VPN routes to hyper %d, %v", hyperID, verr)
		}
	}
	return
}

func syncMigration(ctx context.Context, instance *model.Instance) (err error) {
	migration := &model.Migration{}
	ctx, db := GetContextDB(ctx)
	err = db.Preload("Phases", "name = 'Prepare_Source' and status != 'completed'").Where("instance_id = ? and source_hyper = ?", instance.ID, instance.Hyper).Last(migration).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = nil
			return
		}
		logger.Ctx(ctx).Error("Failed to get migrations", err)
		return
	}
	if len(migration.Phases) > 0 {
		for _, task := range migration.Phases {
			err = execSourceMigrate(ctx, instance, migration, task.ID, "/opt/cloudland/scripts/backend/source_migration.sh", "cold")
			if err != nil {
				logger.Ctx(ctx).Error("Failed to exec source migration", err)
				return
			}
		}
		return
	}
	if instance.Status == "shutoff" {
		err = db.Preload("Phases", "name = 'Prepare_Source' and status != 'completed'").Where("instance_id = ? and target_hyper = ? and status != 'completed'", instance.ID, instance.Hyper).Last(migration).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to exec source migration", err)
			return
		}
	}
	return
}

func syncNicInfo(ctx context.Context, instance *model.Instance) (err error) {
	vlans := []*VlanInfo{}
	for _, iface := range instance.Interfaces {
		var vlanInfo *VlanInfo
		vlanInfo, err = GetInterfaceInfo(ctx, instance, iface)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to get interface info", err)
			return
		}
		vlans = append(vlans, vlanInfo)
	}
	jsonData, err := json.Marshal(vlans)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to marshal instance json data", err)
		return
	}
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/sync_nic_info.sh '%d' '%s' '%s' <<'EOF'\n%s\nEOF", instance.ID, ShellEscape(instance.Hostname), ShellEscape(GetImageOSCode(ctx, instance)), jsonData)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Execute floating ip failed", err)
		return
	}
	return
}

func syncFloatingIp(ctx context.Context, instance *model.Instance) (err error) {
	ctx, db := GetContextDB(ctx)
	var primaryIface *model.Interface
	for i, iface := range instance.Interfaces {
		if iface.PrimaryIf {
			primaryIface = instance.Interfaces[i]
			break
		}
	}
	if primaryIface != nil {
		floatingIps := []*model.FloatingIp{}
		err = db.Preload("Interface").Preload("Interface.Address").Preload("Interface.Address.Subnet").Where("instance_id = ? and type = ?", instance.ID, PublicFloating).Find(&floatingIps).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to get floating ip", err)
			return
		}
		for _, floatingIp := range floatingIps {
			err = floatingIpAdmin.EnsureSubnetID(ctx, floatingIp)
			if err != nil {
				logger.Ctx(ctx).Error("Failed to ensure subnet_id", err)
				continue
			}

			pubSubnet := floatingIp.Interface.Address.Subnet
			control := fmt.Sprintf("inter=%d", instance.Hyper)
			command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_floating.sh '%d' '%s' '%s' '%d' '%s' '%d' '%d' '%d' '%d'", floatingIp.RouterID, ShellEscape(floatingIp.FipAddress), ShellEscape(pubSubnet.Gateway), pubSubnet.Vlan, ShellEscape(primaryIface.Address.Address), primaryIface.Address.Subnet.Vlan, floatingIp.ID, floatingIp.Inbound, floatingIp.Outbound)
			err = HyperExecute(ctx, control, command)
			if err != nil {
				logger.Ctx(ctx).Error("Execute floating ip failed", err)
				return
			}
		}
	}
	return
}
