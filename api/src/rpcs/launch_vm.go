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
	if argn < 5 {
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
	if errHndl != nil && services.FailEvacuationOf(ctx, instID, "no host that reaches its storage pools has the resources") {
		// The instance stays the down host's: nothing of it was created anywhere
		return
	}
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
	// Recovered on another host while this one was away: what the old copy reports is ignored until the host
	// removed it (reconcile, shared-storage-design.md §11.4); a boot would otherwise take the instance back
	if services.EvacuatedFrom(ctx, instID, int32(hyperID)) {
		logger.Ctx(ctx).Warningf("Host %d reported instance %d, which was recovered elsewhere: ignored until the host removes its copy", hyperID, instID)
		return
	}
	if reason == "evacuate" {
		return evacuationLaunched(ctx, instance, int32(hyperID), serverStatus, args)
	}
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
		// Same for the transit gateway of the VPC: the node routes to the other member VPCs by itself
		if terr := services.TgwResyncNode(ctx, instance.RouterID, int32(hyperID)); terr != nil {
			logger.Ctx(ctx).Warningf("Failed to sync the transit gateway to hyper %d, %v", hyperID, terr)
		}
	}
	return
}

// evacuationLaunched takes the launch of an instance recovered from a host that is down (shared-storage-design.md
// §11.3): the target owns it from now on, its floating IPs and routes are made there, and the gateway announced, as
// after a migration. The copy on the down host goes when that host comes back
func evacuationLaunched(ctx context.Context, instance *model.Instance, hyperID int32, state string, args []string) (status string, err error) {
	message := ""
	if len(args) > 5 {
		message = args[5]
	}
	m, err := services.EvacuationLaunched(ctx, instance.ID, hyperID, state, message)
	if err != nil || m == nil {
		return
	}
	instance.Hyper = hyperID
	if instance.RouterID > 0 {
		if ferr := syncFloatingIp(ctx, instance); ferr != nil {
			logger.Ctx(ctx).Error("Failed to make the floating IPs of the evacuated instance", ferr)
		}
		if verr := services.VpnResyncNode(ctx, instance.RouterID, hyperID); verr != nil {
			logger.Ctx(ctx).Warningf("Failed to sync VPN routes to hyper %d, %v", hyperID, verr)
		}
		if terr := services.TgwResyncNode(ctx, instance.RouterID, hyperID); terr != nil {
			logger.Ctx(ctx).Warningf("Failed to sync the transit gateway to hyper %d, %v", hyperID, terr)
		}
		// The guest still has the gateway MAC of the router of the down host
		if gerr := HyperExecute(ctx, fmt.Sprintf("inter=%d", hyperID), fmt.Sprintf("/opt/cloudland/scripts/backend/post_migration_net.sh '%d' 'garp'", instance.ID)); gerr != nil {
			logger.Ctx(ctx).Warningf("Failed to announce the gateway to evacuated instance %d: %v", instance.ID, gerr)
		}
		if terr := services.TgwNodeCheckLeave(ctx, instance.RouterID, m.SourceHyper); terr != nil {
			logger.Ctx(ctx).Warningf("Failed to check the transit gateway of host %d: %v", m.SourceHyper, terr)
		}
	}
	services.CleanEvacuatedSource(ctx, m, instance.RouterID)
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
