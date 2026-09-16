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
	"strings"
	"time"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	Add("inst_status", InstanceStatus)
}

func InstanceStatus(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| inst_status.sh '3' '5 running 7 running 9 shut_off'
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	argn := len(args)
	if argn < 3 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	hyperID, err := strconv.Atoi(args[1])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid hypervisor ID", err)
		return
	}
	hyper := &model.Hyper{Hostid: int32(hyperID)}
	err = db.Where(hyper).Take(hyper).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query hyper", err)
		return
	}
	statusList := strings.Split(args[2], " ")
	for i := 0; i < len(statusList); i += 2 {
		instID, err := strconv.ParseInt(statusList[i], 10, 64)
		if err != nil {
			logger.Ctx(ctx).Error("Invalid instance ID", err)
			continue
		}
		status := statusList[i+1]
		instance := &model.Instance{Model: model.Model{ID: instID}}
		err = db.Unscoped().Take(instance).Error
		if err != nil {
			logger.Ctx(ctx).Error("Invalid instance ID", err)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				instance.Hostname = "unknown"
				instance.Status = model.InstanceStatus(status)
				instance.Hyper = int32(hyperID)
				err = db.Create(instance).Error
				if err != nil {
					logger.Ctx(ctx).Error("Failed to create unknown instance", err)
				}
			}
			continue
		}
		if instance.Status == "rescuing" {
			continue
		}
		if instance.Status == model.InstanceStatusMigrating {
			// 迁移进行中（本地存储热迁移复制磁盘可能很久，期间目标节点也会上报该虚拟机）：不改状态和所在节点。
			// 判据是迁移记录 10 分钟内有更新：source_migration.sh 每 3 秒上报一次进度，正常迁移无论多久都算活跃；
			// 迁移记录已结束、或真的卡死不再上报时，退回 6 分钟兜底由心跳纠正
			var active int64
			if cerr := db.Model(&model.Migration{}).Where("instance_id = ? and status in ? and updated_at > ?", instance.ID,
				[]string{"in_progress", "target_prepared", "source_prepared"}, time.Now().Add(-10*time.Minute)).Count(&active).Error; cerr == nil && active > 0 {
				continue
			}
			if time.Since(instance.UpdatedAt) < 6*time.Minute {
				continue
			}
		}
		if instance.Status.String() != status {
			err = db.Model(instance).Updates(map[string]interface{}{
				"status": status,
			}).Error
			if err != nil {
				logger.Ctx(ctx).Error("Failed to update status", err)
			}
		}
		if instance.DeletedAt.Valid {
			err = db.Unscoped().Model(instance).Updates(map[string]interface{}{
				"status":     status,
				"deleted_at": nil,
			}).Error
			if err != nil {
				logger.Ctx(ctx).Error("Failed to update status", err)
			}
		}
		if instance.Hyper != int32(hyperID) {
			instance.Hyper = int32(hyperID)
			if instance.Hyper >= 0 {
				err = syncMigration(ctx, instance)
				if err != nil {
					logger.Ctx(ctx).Error("Failed to sync migration info", err)
				}
			}
			err = db.Unscoped().Model(instance).Updates(map[string]interface{}{
				"hyper": int32(hyperID),
			}).Error
			if err != nil {
				logger.Ctx(ctx).Error("Failed to update hypervisor", err)
			}
			err = db.Unscoped().Model(&model.Interface{}).Where("instance = ?", instance.ID).Updates(map[string]interface{}{
				"hyper": int32(hyperID),
			}).Error
			if err != nil {
				logger.Ctx(ctx).Error("Failed to update interface", err)
			}
		}
	}
	return
}
