/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"fmt"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"
)

func init() {
	Add("hyper_status", HyperStatus)
}

func HyperStatus(ctx context.Context, args []string) (status string, err error) {
	//"|:-COMMAND-:| hyper_status.sh '$NODE_ID' '$HOSTNAME' '$cpu' '$total_cpu' '$memory' '$total_memory' '$disk' '$total_disk' '$state' '$vtep_ip' '$ZONE_NAME' '$cpu_over_rate' '$mem_over_rate' '$disk_over_rate' '$cpu_model'"
	logger.Ctx(ctx).Debugf("HyperStatus updates %+v", args)
	// Host names whose hyper-hosts lines follow this report. Deferred ahead of the transaction so that it runs after
	// the commit: SyncHostsInDns reads committed rows, and a clapi start rebuilding the file meanwhile can not put the
	// old address back for good
	var dnsNames []string
	defer func() {
		if err == nil && len(dnsNames) > 0 {
			services.SyncHostsInDns(dnsNames...)
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	argn := len(args)
	if argn < 15 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	hyperID, err := strconv.Atoi(args[1])
	if err != nil || hyperID < 0 {
		logger.Ctx(ctx).Error("Invalid hypervisor ID", err)
		return
	}
	hyperName := args[2]
	availCpu, err := strconv.Atoi(args[3])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid available cpu", err)
		availCpu = 0
	}
	totalCpu, err := strconv.Atoi(args[4])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid total cpu", err)
		totalCpu = 0
	}
	availMem, err := strconv.Atoi(args[5])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid available memory", err)
		availMem = 0
	}
	totalMem, err := strconv.Atoi(args[6])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid total memory", err)
		totalMem = 0
	}
	availDisk, err := strconv.Atoi(args[7])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid available disk", err)
		availDisk = 0
	}
	totalDisk, err := strconv.Atoi(args[8])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid total disk", err)
		totalDisk = 0
	}
	hyperStatus, err := strconv.Atoi(args[9])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid hypervisor status", err)
		hyperStatus = 1
	}
	hostIP := args[10]
	zoneName := args[11]
	zone := &model.Zone{Name: zoneName}
	if zoneName != "" {
		err = db.Where("name = ?", zoneName).FirstOrCreate(zone).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create zone", err)
			return
		}
	}
	hyper := &model.Hyper{Hostid: int32(hyperID)}
	err = db.Where("hostid = ?", hyperID).Take(hyper).Error
	if err != nil {
		// Do NOT auto-create. If admin explicitly DELETE'd this hyper, any orphan
		// heartbeat from a still-running cloudlet must be ignored, not used to
		// resurrect the row. New nodes are created up-front by POST /api/v1/hypers
		// (services.HyperAdmin.Deploy) before the compute node ever heartbeats,
		// so a missing row here always means "deleted, ignore me".
		logger.Ctx(ctx).Warningf("hyper_status RPC for unknown hostid=%d (name=%s); ignoring orphan heartbeat", hyperID, hyperName)
		err = nil
		return
	}
	// PET-769 should maintain the hypervisor's over commit rates in admin console and admin API
	// args 12 cpu_over_rate, args 13 mem_over_rate, args 14 disk_over_rate are float values
	cpuOverRate, err := strconv.ParseFloat(args[12], 32)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid cpu over rate", err)
	} else {
		hyper.CpuOverRate = float32(cpuOverRate)
	}
	memOverRate, err := strconv.ParseFloat(args[13], 32)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid memory over rate", err)
	} else {
		hyper.MemOverRate = float32(memOverRate)
	}
	diskOverRate, err := strconv.ParseFloat(args[14], 32)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid disk over rate", err)
	} else {
		hyper.DiskOverRate = float32(diskOverRate)
	}
	// An empty cpu model (lscpu without "Model name:") is the last argument, which the decoding drops
	cpuModel := ""
	if argn > 15 {
		cpuModel = args[15]
	}
	// end PET-769
	// PET-1218 fix hyper status
	logger.Ctx(ctx).Debugf("Updating hypervisor %s status to %d", hyperName, hyperStatus)
	updates := map[string]interface{}{
		"hostname":  hyperName,
		"cpu_model": cpuModel,
		"virt_type": "kvm-x86_64",
	}
	// The VXLAN interface has no address for a moment while booting or switching the bond: keep the known one
	if hostIP != "" {
		updates["host_ip"] = hostIP
	}
	// 心跳只反映节点自身的运行状态，不能覆盖管理员置位的状态：disabled(0) 与 maintaining(2)
	// 是控制面单方面设置的，计算节点并不知情，每次心跳都上报 active(1)。这里若无条件写回
	// （map 更新不会跳过任何键），节点进入维护模式后几十秒就会被心跳改回 active 而自动退出
	if hyper.Status != model.HyperStatusNames[model.HYPER_DISABLED] &&
		hyper.Status != model.HyperStatusNames[model.HYPER_MAINTAINING] {
		updates["status"] = hyperStatus
	} else {
		logger.Ctx(ctx).Debugf("Hypervisor %d kept in administrative status %d (reported %d)", hyperID, hyper.Status, hyperStatus)
	}
	// Back from offline: the status an admin set before it went away (disabled, maintaining), or while it was away
	// (PATCH /hypers records offline_prior then, 0 or 1), comes back with it. The host reports what its disabled
	// marker says; when an admin changed it while the host was away the marker is stale, so it is rewritten
	var syncMarker *int32
	if hyper.Status == model.HyperStatusOffline {
		updates["offline_at"] = nil
		updates["offline_prior"] = nil
		if status, sync := offlineReturnStatus(hyper.OfflinePrior, hyperStatus); status != nil {
			updates["status"] = *status
			if sync {
				syncMarker = status
			}
		}
	}
	// map 更新不会处理 Zone 关联，需直接写外键列；未上报可用区时保留原值
	if zoneName != "" {
		updates["zone_id"] = zone.ID
	}
	err = db.Model(&model.Hyper{}).Where("hostid = ?", hyperID).Updates(updates).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update hyper", err)
		return
	}
	if syncMarker != nil {
		// update_hyper.sh only creates or removes the disabled marker here (restart_clet 0); zone and over-commit
		// rates are the ones the host just reported
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/update_hyper.sh '%d' '%s' '0' '%f' '%f' '%f'",
			*syncMarker, ShellEscape(zoneName), hyper.CpuOverRate, hyper.MemOverRate, hyper.DiskOverRate)
		if xerr := HyperExecute(ctx, fmt.Sprintf("inter=%d", hyperID), command); xerr != nil {
			logger.Ctx(ctx).Errorf("Failed to sync the disabled marker of hypervisor %d: %v", hyperID, xerr)
		}
	}
	// hyper-hosts follows the name and the address, rewritten only when one of them changed (after the commit, see
	// above). A renamed host takes its old name along, unless another host still has that name
	if hyper.Hostname != hyperName {
		dnsNames = []string{hyper.Hostname, hyperName}
	} else if hostIP != "" && hyper.HostIP != hostIP {
		dnsNames = []string{hyperName}
	}
	resource := &model.Resource{
		Hostid:      int32(hyperID),
		Cpu:         int64(availCpu),
		CpuTotal:    int64(totalCpu),
		Memory:      int64(availMem),
		MemoryTotal: int64(totalMem),
		Disk:        int64(availDisk),
		DiskTotal:   int64(totalDisk),
	}
	err = db.Where("hostid = ?", hyperID).Assign(resource).FirstOrCreate(&model.Resource{}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create or update hyper resource", err)
		return
	}
	if availCpu == 0 || availMem == 0 || availDisk == 0 {
		err = db.Model(&model.Resource{}).Where("hostid = ?", hyperID).Updates(map[string]interface{}{
			"cpu":    availCpu,
			"memory": availMem,
			"disk":   availDisk}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update hypervisor resource", err)
		}
	}
	if hyper.RouteIP == "" {
		_, err = SystemRouter(ctx, []string{args[0], args[1], args[2]})
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create system router", err)
		}
	}
	return
}

// offlineReturnStatus decides the status of a host coming back from offline: the status recorded in offline_prior
// (disabled or maintaining before it went away, or what an admin chose while it was away) wins over the reported
// one. sync tells that the host's disabled marker, which is what it reports, disagrees and must be rewritten
func offlineReturnStatus(prior *int32, reported int) (status *int32, sync bool) {
	if prior == nil {
		return nil, false
	}
	switch *prior {
	case model.HyperStatusNames[model.HYPER_DISABLED], model.HyperStatusNames[model.HYPER_ACTIVE], model.HyperStatusNames[model.HYPER_MAINTAINING]:
		disabled := *prior == model.HyperStatusNames[model.HYPER_DISABLED]
		return prior, disabled != (reported == 0)
	}
	return nil, false
}
