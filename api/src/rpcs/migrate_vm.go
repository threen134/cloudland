/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/model"
	"api/src/services"
)

func init() {
	Add("migrate_vm", MigrateVM)
}

func execSourceMigrate(ctx context.Context, instance *model.Instance, migration *model.Migration, taskID int64, migrationScript, migrationType string) (err error) {
	ctx, db := GetContextDB(ctx)
	targetHyper := &model.Hyper{}
	err = db.Where("hostid = ?", migration.TargetHyper).Take(targetHyper).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query target hyper", err)
		return
	}
	sourceHyper := &model.Hyper{}
	err = db.Where("hostid = ?", migration.SourceHyper).Take(sourceHyper).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query source hyper", err)
		return
	}
	// The scripts work from the disk plan: which file goes where on the target
	planJson, err := json.Marshal(services.MigrationPlan(migration))
	if err != nil {
		logger.Ctx(ctx).Error("Failed to marshal the disk plan", err)
		return
	}
	if sourceHyper.Status != 10 {
		// source_migration.sh reaches the target over ssh / qemu+ssh by its internal IP: host names may not resolve,
		// and known_hosts usually records the IP only
		targetAddr := targetHyper.Hostname
		if targetHyper.HostIP != "" {
			targetAddr = targetHyper.HostIP
		}
		control := fmt.Sprintf("inter=%d", migration.SourceHyper)
		command := fmt.Sprintf("%s '%d' '%d' '%d' '%d' '%s' '%s' '%d' <<'EOF'\n%s\nEOF", migrationScript, migration.ID, taskID, instance.ID, instance.RouterID, ShellEscape(targetAddr), ShellEscape(migrationType), targetHyper.Hostid, planJson)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Ctx(ctx).Error("Source migration command execution failed", err)
			return
		}
	} else {
		err = fmt.Errorf("Source hyper is not in a valid state")
		return
	}
	return
}

// prewarmTargetFdb 在目标节点预先写入同 VPC 其他网卡的 VXLAN 转发 / 邻居条目（指向它们各自所在节点）。
// sendFdbRules 按网卡当前所在节点计算，迁移完成前目标节点拿不到这些条目，切换后虚拟机访问同 VPC 其他虚拟机要等 completed。
// 不包含正在迁移的虚拟机自己的网卡，也不向其他节点扩散，不会提前改变任何流量走向
func prewarmTargetFdb(ctx context.Context, instance *model.Instance, targetHyper *model.Hyper) (err error) {
	if instance.RouterID == 0 {
		return
	}
	ctx, db := GetContextDB(ctx)
	ifaces := []*model.Interface{}
	err = db.Preload("Address").Preload("Address.Subnet").Where("router_id = ? and type <> 'gateway' and hyper <> ? and instance <> ?", instance.RouterID, targetHyper.Hostid, instance.ID).Find(&ifaces).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query interfaces of the router", err)
		return
	}
	hostIPs := map[int32]string{}
	rules := []*FdbRule{}
	for _, iface := range ifaces {
		if iface.Address == nil || iface.Address.Subnet == nil || iface.Hyper < 0 {
			continue
		}
		subnet := iface.Address.Subnet
		if subnet.Type == string(Public) || subnet.Type == string(Private) {
			continue
		}
		hostIP, ok := hostIPs[iface.Hyper]
		if !ok {
			hyper := &model.Hyper{}
			if db.Where("hostid = ?", iface.Hyper).Take(hyper).Error != nil {
				continue
			}
			hostIP = hyper.HostIP
			hostIPs[iface.Hyper] = hostIP
		}
		rules = append(rules, &FdbRule{Instance: iface.Name, Vni: subnet.Vlan, InnerIP: iface.Address.Address, InnerMac: iface.MacAddr, OuterIP: hostIP, Gateway: subnet.Gateway, Router: subnet.RouterID})
	}
	if len(rules) == 0 {
		return
	}
	fdbJson, _ := json.Marshal(rules)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/add_fwrule.sh <<'EOF'\n%s\nEOF", fdbJson)
	err = HyperExecute(ctx, fmt.Sprintf("inter=%d", targetHyper.Hostid), command)
	if err != nil {
		logger.Ctx(ctx).Error("Prewarm target fdb execution failed", err)
	}
	return
}

// clearSourceAddresses 删除源节点路由器上的浮动 IP 和辅助 IP，须在目标节点重建之后调用
func clearSourceAddresses(ctx context.Context, instance *model.Instance, migration *model.Migration) (err error) {
	ctx, db := GetContextDB(ctx)
	err = db.Preload("SiteSubnets").Preload("Address").Preload("Address.Subnet").Preload("SecondAddresses").Preload("SecondAddresses.Subnet").Preload("Address.Subnet.Router").Where("instance = ?", instance.ID).Find(&instance.Interfaces).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to get interfaces", err)
		return
	}
	var primaryIface *model.Interface
	for i, iface := range instance.Interfaces {
		if iface.PrimaryIf {
			primaryIface = instance.Interfaces[i]
			break
		}
	}
	if primaryIface == nil {
		return
	}
	err = db.Where("instance_id = ? and type = ?", instance.ID, PublicFloating).Find(&instance.FloatingIps).Error
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to query floating ip(s), %v", err)
		return
	}
	control := fmt.Sprintf("inter=%d", migration.SourceHyper)
	if instance.RouterID > 0 {
		for _, fip := range instance.FloatingIps {
			command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_floating.sh '%d' '%s' '%s' '%d' '%d'", fip.RouterID, ShellEscape(fip.FipAddress), ShellEscape(fip.IntAddress), primaryIface.Address.Subnet.Vlan, fip.ID)
			err = HyperExecute(ctx, control, command)
			if err != nil {
				logger.Ctx(ctx).Error("Execute clear floating ip failed", err)
				return
			}
		}
	}
	if len(primaryIface.SiteSubnets) > 0 || len(primaryIface.SecondAddresses) > 0 {
		var moreAddresses []string
		_, moreAddresses, err = GetInstanceNetworks(ctx, instance, []*model.Interface{primaryIface})
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to get instance networks, %v", err)
			return
		}
		var oldAddrsJson []byte
		oldAddrsJson, err = json.Marshal(moreAddresses)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to marshal second addresses json data, %v", err)
			return
		}
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_second_ips.sh '%d' '%s' '%s'<<'EOF'\n%s\nEOF", instance.ID, ShellEscape(primaryIface.MacAddr), ShellEscape(GetImageOSCode(ctx, instance)), oldAddrsJson)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Ctx(ctx).Error("Execute clear second ips failed", err)
			return
		}
	}
	return
}

func MigrateVM(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| migrate_vm.sh '12' '2' '127' '3' 'state'
	outerCtx := ctx
	ctx, db, newTransaction := StartTransaction(ctx)
	// The reservations of a migration that ended are dropped after the transaction: a failure rolls the transaction
	// back, which would undo the release and leave the migration in a non-terminal state, so the periodic cleanup
	// (which matches the terminal ones) would not catch them either and the space would stay reserved until the
	// reservations expire a day later
	releaseMigration := int64(0)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
		if releaseMigration > 0 {
			services.ReleaseReservations(outerCtx, releaseMigration, 0, model.ReservationMigration)
		}
	}()
	argn := len(args)
	if argn < 5 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	migrationID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid migration ID", err)
		return
	}
	taskID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid task ID", err)
		return
	}
	instID, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	hyperID, err := strconv.Atoi(args[4])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid hyper ID", err)
		return
	}
	status = args[5]
	taskStatus := status
	message := ""
	if len(args) > 6 {
		message = args[6]
	}
	migration := &model.Migration{Model: model.Model{ID: migrationID}}
	err = db.Model(migration).Take(migration).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to get migration record", err)
		return
	}
	instance := &model.Instance{Model: model.Model{ID: instID}}
	err = db.Preload("Volumes").Take(instance).Error
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	errHndl := ctx.Value("error")
	if errHndl != nil {
		reason := "Resource is not enough"
		err = db.Model(instance).Updates(map[string]interface{}{
			"status": "rollback",
			"reason": reason}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update instance", err)
		}
		err = db.Model(migration).Updates(map[string]interface{}{"status": "failed"}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update migration", err)
		}
		releaseMigration = migration.ID
		return
	}

	// Use defer to handle status updates, ensuring both migration and task status
	// are set to "failed" if any error occurs during the function execution.
	defer func() {
		switch status {
		case "failed", "not_supported", "source_rollback", "rollback", "timeout":
			taskStatus = "failed"
		default:
			taskStatus = "completed"
		}
		migration.Status = status
		// 本 defer 自身的数据库错误只记日志，不能写回命名返回值 err：
		// 覆盖掉分支里的失败会让 EndTransaction 误判为成功，提交半完成的迁移且不会重试
		var updateErr error
		if err != nil {
			taskStatus = "failed"
			updateErr = db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]interface{}{"status": taskStatus, "message": err.Error()}).Error
		} else {
			updateErr = db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]interface{}{"status": taskStatus, "message": message}).Error
		}
		if updateErr != nil {
			logger.Ctx(ctx).Error("Failed to update task", updateErr)
		}
		if updateErr = db.Model(migration).Updates(map[string]interface{}{"status": status}).Error; updateErr != nil {
			logger.Ctx(ctx).Error("Failed to update migration", updateErr)
		}
		if taskStatus == "failed" {
			releaseMigration = migration.ID
		}
	}()

	if status == "completed" {
		// complete_migration.sh 回传目标节点上虚拟机的实际状态；迁移期间心跳上报被跳过，状态不变时不会再上报
		vmStatus := model.InstanceStatusMigrated
		if s, ok := strings.CutPrefix(message, "vm_state="); ok {
			switch model.InstanceStatus(s) {
			case model.InstanceStatusRunning, model.InstanceStatusShutoff, model.InstanceStatusPaused:
				vmStatus = model.InstanceStatus(s)
			}
		}
		err = db.Model(&model.Instance{Model: model.Model{ID: instID}}).Updates(map[string]interface{}{"status": vmStatus}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update instance status to unknown, %v", err)
			return
		}
		// 迁移完成，进度置满：上报循环在 virsh migrate 返回时就停了，最后一次上报通常停在中途
		err = db.Model(&model.Migration{}).Where("id = ?", migration.ID).Update("progress", 100).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update migration progress", err)
			return
		}
		// The disks were copied to the target: they take the pool, path and host of the plan
		if err = services.ApplyMigrationPlan(ctx, migration, instID, int32(hyperID)); err != nil {
			logger.Ctx(ctx).Error("Failed to update the volumes of the migrated instance", err)
			return
		}
		// LaunchVM sync 在目标节点重建网卡、浮动 IP 后，再清理源节点上的浮动 IP 和辅助 IP
		_, err = LaunchVM(ctx, []string{args[0], args[3], string(vmStatus), args[4], "sync"})
		if err != nil {
			logger.Ctx(ctx).Error("Failed to sync vm info", err)
			return
		}
		// 目标节点路由器宣告网关（各节点路由器网关 MAC 不同，虚拟机 ARP 缓存仍指向源节点）：
		// 排在目标节点重建网卡、浮动 IP 的命令之后，源节点删除路由器之前
		err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hyperID), fmt.Sprintf("/opt/cloudland/scripts/backend/post_migration_net.sh '%d' 'garp'", instID))
		if err != nil {
			logger.Ctx(ctx).Error("Execute post migration network switch failed", err)
			return
		}
		err = clearSourceAddresses(ctx, instance, migration)
		if err != nil {
			return
		}
		err = execSourceMigrate(ctx, instance, migration, taskID, "/opt/cloudland/scripts/backend/finish_source_migration.sh", migration.Type)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to exec finish source migration", err)
			return
		}
	} else if status == "rollback" {
		err = execSourceMigrate(ctx, instance, migration, taskID, "/opt/cloudland/scripts/backend/rollback_source_migration.sh", migration.Type)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to exec finish source migration", err)
			return
		}
	} else if status == "source_rollback" {
		if err = rollbackTarget(ctx, migration, instance); err != nil {
			return
		}
	} else if status == "not_supported" {
		// 目标节点未做任何改动，虚拟机仍在源节点：退出 migrating，由下次心跳按实际状态更新
		err = db.Model(&model.Instance{Model: model.Model{ID: instID}}).Updates(map[string]interface{}{"status": model.InstanceStatusRollback}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update instance status to rollback, %v", err)
			return
		}
	} else if status == "failed" || status == "timeout" {
		err = db.Model(&model.Instance{Model: model.Model{ID: instID}}).Updates(map[string]interface{}{"status": model.InstanceStatusUnknown}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update instance status to unknown, %v", err)
			return
		}
	} else if status == "target_prepared" {
		// 目标节点等于源节点时必须立即中止：继续下去 source_migration.sh 会把虚拟机迁往
		// 它已经所在的那台机器，libvirt 拒绝后触发回滚，而回滚会在"目标节点"执行
		// clear_target_migration.sh —— 那正是虚拟机正在运行的节点，destroy + undefine + 删盘
		// 会把活虚拟机连数据一起销毁。调度候选集算错时确实发生过（见 services/migration.go
		// 拼接 select= 控制串的注释）
		if int32(hyperID) == migration.SourceHyper {
			logger.Ctx(ctx).Errorf("Target hypervisor %d equals source hypervisor for migration %d, aborting", hyperID, migration.ID)
			if uerr := db.Model(&model.Instance{Model: model.Model{ID: instID}}).Updates(map[string]interface{}{
				"status": model.InstanceStatusRollback}).Error; uerr != nil {
				logger.Ctx(ctx).Error("Failed to update instance status to rollback", uerr)
			}
			status = "failed"
			err = fmt.Errorf("target hypervisor %d is the same as source", hyperID)
			return
		}
		// 目标节点由调度器选出时（创建迁移未指定目标，target_hyper 存的是 -1），实际节点只有这个回调知道，必须落库：
		// 后续 complete_migration.sh、clear_target_migration.sh 都用它作 inter= 下发，-1 会被 cland 当作无目标节点丢弃，
		// 迁移会永远停在 source_prepared（虚拟机已在目标节点跑起来，但账本不收尾）
		migration.TargetHyper = int32(hyperID)
		err = db.Model(migration).Updates(map[string]interface{}{"target_hyper": int32(hyperID)}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update migration target hyper", err)
			return
		}
		targetHyper := &model.Hyper{}
		err = db.Where("hostid = ?", hyperID).Take(targetHyper).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query hyper", err)
			return
		}
		// 迁移发起之后目标节点可能被置为维护/禁用：自动选目标时，候选集是在那之前算出的，
		// 创建迁移时的目标校验和「有虚拟机迁入就拒绝进入维护」的检查都覆盖不到这个窗口。
		// 此刻虚拟机还没切换过去，中止的代价远小于让它落到一台即将断电的节点上
		if targetHyper.Status != 1 {
			logger.Ctx(ctx).Errorf("Target hypervisor %d is in status %d, aborting migration %d", hyperID, targetHyper.Status, migration.ID)
			if uerr := db.Model(&model.Instance{Model: model.Model{ID: instID}}).Updates(map[string]interface{}{
				"status": model.InstanceStatusRollback}).Error; uerr != nil {
				logger.Ctx(ctx).Error("Failed to update instance status to rollback", uerr)
			}
			status = "failed"
			err = fmt.Errorf("target hypervisor %d is not active (status %d)", hyperID, targetHyper.Status)
			return
		}
		// The target is known now: plan where the disks go, check the space and reserve it before copying anything.
		// If that fails, undo what target_migration.sh prepared.
		if _, perr := services.PlanMigrationTarget(ctx, migration, instance, int32(hyperID)); perr != nil {
			logger.Ctx(ctx).Errorf("Disk plan of migration %d on host %d failed: %v", migration.ID, hyperID, perr)
			status = "source_rollback"
			message = perr.Error()
			if err = rollbackTarget(ctx, migration, instance); err != nil {
				return
			}
			return
		}
		task2 := &model.Task{
			Name:    "Prepare_Source",
			Mission: migration.ID,
			Summary: "Prepare resources on source hypervisor",
			Status:  "in_progress",
		}
		err = db.Model(task2).Create(task2).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create task2", err)
			return
		}
		// 预热失败不影响迁移，只是切换后同 VPC 互访要等 completed
		if perr := prewarmTargetFdb(ctx, instance, targetHyper); perr != nil {
			logger.Ctx(ctx).Warningf("Failed to prewarm target fdb, %v", perr)
		}
		// The target may host this VPC for the first time: give its router the VPN gateway routes
		if verr := services.VpnResyncNode(ctx, instance.RouterID, targetHyper.Hostid); verr != nil {
			logger.Ctx(ctx).Warningf("Failed to sync VPN routes to migration target, %v", verr)
		}
		err = execSourceMigrate(ctx, instance, migration, task2.ID, "/opt/cloudland/scripts/backend/source_migration.sh", migration.Type)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to exec source migration", err)
			// 先留存原始失败原因：冷迁移分支的 LaunchVM 会覆盖 err，成功时 err 为 nil，
			// 再调 err.Error() 会让整个回调 handler panic
			migrateErr := err
			if migration.Type == "cold" {
				_, err = LaunchVM(ctx, []string{args[0], args[3], "migrated", args[4], "sync"})
				if err != nil {
					logger.Ctx(ctx).Error("Failed to sync vm info", err)
					return
				}
			}
			err = db.Model(&model.Task{}).Where("id = ?", task2.ID).Updates(map[string]interface{}{"status": "failed", "message": migrateErr.Error()}).Error
			return
		}
	} else if status == "source_prepared" {
		// 源节点上的浮动 IP、辅助 IP 保留到 completed：目标节点重建之前仍由源节点路由器转发
		control := fmt.Sprintf("inter=%d", migration.TargetHyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/complete_migration.sh '%d' '%d' '%d' '%s'", migration.ID, taskID, instance.ID, ShellEscape(migration.Type))
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Ctx(ctx).Error("Execute clear target failed", err)
			return
		}
	}
	logger.Ctx(ctx).Infof("Migration condition: %s, new status: %s", migration.Status, status)
	return
}

// rollbackTarget undoes on the target what target_migration.sh and source_migration.sh prepared there:
// network resources, and the disk files and placeholders of the plan
func rollbackTarget(ctx context.Context, migration *model.Migration, instance *model.Instance) (err error) {
	ctx, db := GetContextDB(ctx)
	err = db.Model(&model.Instance{Model: model.Model{ID: instance.ID}}).Updates(map[string]interface{}{"status": model.InstanceStatusRollback}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update instance status to rollback, %v", err)
		return
	}
	task3 := &model.Task{
		Name:    "Source_Rollback",
		Mission: migration.ID,
		Summary: "Clean up target hypervisor",
		Status:  "in_progress",
	}
	if err = db.Model(task3).Create(task3).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to create task3", err)
		return
	}
	// target_migration.sh built security group chains and the VPC router for the interfaces
	var ifaces []*model.Interface
	if err = db.Where("instance = ?", instance.ID).Find(&ifaces).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to get interfaces", err)
		return
	}
	macs := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		macs = append(macs, iface.MacAddr)
	}
	planJson, _ := json.Marshal(services.MigrationPlan(migration))
	control := fmt.Sprintf("inter=%d", migration.TargetHyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_target_migration.sh '%d' '%d' '%d' '%d' '%s'<<'EOF'\n%s\nEOF", migration.ID, task3.ID, instance.ID, instance.RouterID, ShellEscape(strings.Join(macs, " ")), planJson)
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Error("Execute clear target failed", err)
	}
	return
}
