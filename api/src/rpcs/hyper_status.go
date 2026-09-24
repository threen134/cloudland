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
	cpuModel := args[15]
	// end PET-769
	// PET-1218 fix hyper status
	logger.Ctx(ctx).Debugf("Updating hypervisor %s status to %d", hyperName, hyperStatus)
	updates := map[string]interface{}{
		"hostname":  hyperName,
		"cpu_model": cpuModel,
		"virt_type": "kvm-x86_64",
		"host_ip":   hostIP,
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
	// map 更新不会处理 Zone 关联，需直接写外键列；未上报可用区时保留原值
	if zoneName != "" {
		updates["zone_id"] = zone.ID
	}
	err = db.Model(&model.Hyper{}).Where("hostid = ?", hyperID).Updates(updates).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update hyper", err)
		return
	}
	// 同步 DNS：部署失败时清理条目；IP 或 hostname 变化时更新映射（避免每次心跳写文件）
	if hyperStatus == 5 { // HYPER_DEPLOY_FAILED
		services.RemoveHostFromDns(hyperName)
	} else if hostIP != "" && (hyper.HostIP != hostIP || hyper.Hostname != hyperName) {
		services.RegisterHostInDns(hyperName, hostIP)
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
