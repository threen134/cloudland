/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"fmt"

	. "api/src/common"
	"api/src/model"
)

type ResourceData struct {
	Title       string `json:"title"`
	CpuUsed     int64  `json:"cpu_used"`
	CpuAvail    int64  `json:"cpu_avail"`
	MemUsed     int64  `json:"mem_used"`
	MemAvail    int64  `json:"mem_avail"`
	DiskUsed    int64  `json:"disk_used"`
	DiskAvail   int64  `json:"disk_avail"`
	VolumeUsed  int64  `json:"volume_used"`
	VolumeAvail int64  `json:"volume_avail"`
	PubipUsed   int64  `json:"pubip_used"`
	PubipAvail  int64  `json:"pubip_avail"`
	PrvipUsed   int64  `json:"prvip_used"`
	PrvipAvail  int64  `json:"prvip_avail"`
}

type Dashboard struct{}

func (a *Dashboard) GetSystemIpUsage(ctx context.Context, ntype string) (ipTotal, ipUsed int, err error) {
	logger.Infof("ENTER GetSystemIpUsage: ntype=%s", ntype)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT GetSystemIpUsage: error=%v", err)
		} else {
			logger.Infof("EXIT GetSystemIpUsage: ipTotal=%d, ipUsed=%d", ipTotal, ipUsed)
		}
	}()
	ctx, db := GetContextDB(ctx)
	subnets := []*model.Subnet{}
	err = db.Where("type = ?", ntype).Find(&subnets).Error
	if err != nil {
		logger.Error("Failed to query subnets")
		return
	}
	where := "subnet_id in ("
	for i, sub := range subnets {
		if i == 0 {
			where = fmt.Sprintf("%s%d", where, sub.ID)
		} else {
			where = fmt.Sprintf("%s,%d", where, sub.ID)
		}
	}
	where = where + ")"
	err = db.Model(&model.Address{}).Where(where).Count(&ipTotal).Error
	if err != nil {
		logger.Error("Failed to count total public ips")
		return
	}
	err = db.Model(&model.Address{}).Where(where).Where("allocated = ?", true).Count(&ipUsed).Error
	if err != nil {
		logger.Error("Failed to count used public ips")
		return
	}
	return
}

func (a *Dashboard) GetOrgIpUsage(ctx context.Context, ntype string) (ipUsed int, err error) {
	logger.Infof("ENTER GetOrgIpUsage: ntype=%s", ntype)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT GetOrgIpUsage: error=%v", err)
		} else {
			logger.Infof("EXIT GetOrgIpUsage: ipUsed=%d", ipUsed)
		}
	}()
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	subnets := []*model.Subnet{}
	err = db.Where("type = ?", ntype).Find(&subnets).Error
	if err != nil {
		logger.Error("Failed to query subnets")
		return
	}
	where := "subnet in ("
	for i, sub := range subnets {
		if i == 0 {
			where = fmt.Sprintf("%s%d", where, sub.ID)
		} else {
			where = fmt.Sprintf("%s,%d", where, sub.ID)
		}
	}
	where = where + ")"
	err = db.Model(&model.Interface{}).Where(where).Where("owner = ?", memberShip.OrgID).Count(&ipUsed).Error
	if err != nil {
		logger.Error("Failed to count used ips")
		return
	}
	return
}

func (a *Dashboard) GetOrgUsage(ctx context.Context, quota *model.Quota, instanceAdmin interface{}) (rcData *ResourceData, err error) {
	logger.Infof("ENTER GetOrgUsage: quotaOrg=%d", quota.Owner)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT GetOrgUsage: error=%v", err)
		} else {
			logger.Infof("EXIT GetOrgUsage: rcDataSummary=%s", rcData.Title)
		}
	}()
	var cpu, memory, disk int32
	
	// Note: instanceAdmin.List needs to be called from routes layer
	// This is a placeholder - actual implementation should receive instances as parameter
	pubip, err := a.GetOrgIpUsage(ctx, "public")
	prvip, err := a.GetOrgIpUsage(ctx, "private")
	rcData = &ResourceData{
		Title:       "Organization Quota Usage Ratio",
		CpuUsed:     int64(cpu),
		CpuAvail:    int64(quota.Cpu - cpu),
		MemUsed:     int64(memory),
		MemAvail:    int64(quota.Memory*1024 - memory),
		DiskUsed:    int64(disk),
		DiskAvail:   int64(quota.Disk - disk),
		VolumeUsed:  0,
		VolumeAvail: int64(quota.Volume),
		PubipUsed:   int64(pubip),
		PubipAvail:  int64(quota.PublicIp - int32(pubip)),
		PrvipUsed:   int64(prvip),
		PrvipAvail:  int64(quota.PrivateIp - int32(prvip)),
	}
	return
}
