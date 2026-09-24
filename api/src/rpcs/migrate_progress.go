/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"strconv"

	. "api/src/common"
	"api/src/model"
)

func init() {
	Add("migrate_progress", MigrateProgress)
}

// MigrateProgress 记录迁移进度，由 source_migration.sh 在迁移期间每几秒上报一次（virsh domjobinfo）
// |:-COMMAND-:| migrate_progress.sh '<migration ID>' '<instance ID>' '<percent>' '<processed bytes>' '<total bytes>'
func MigrateProgress(ctx context.Context, args []string) (status string, err error) {
	ctx, db := GetContextDB(ctx)
	if len(args) < 6 {
		logger.Ctx(ctx).Error("Invalid args for migration progress", args)
		return
	}
	migrationID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid migration ID", err)
		return
	}
	percent, err := strconv.Atoi(args[3])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid progress percent", err)
		return
	}
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	transferred, _ := strconv.ParseInt(args[4], 10, 64)
	total, _ := strconv.ParseInt(args[5], 10, 64)
	// 进度不回退：libvirt 先只统计磁盘镜像，开始传内存后 data_total 变大，百分比会掉下去
	migration := &model.Migration{}
	if db.Where("id = ?", migrationID).Take(migration).Error == nil && int32(percent) < migration.Progress {
		percent = int(migration.Progress)
	}
	// 只更新进行中的迁移，避免迟到的上报覆盖已结束的记录
	err = db.Model(&model.Migration{}).Where("id = ? and status in ?", migrationID,
		[]string{"in_progress", "target_prepared", "source_prepared"}).Updates(map[string]interface{}{
		"progress":    int32(percent),
		"transferred": transferred,
		"total":       total,
	}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update migration progress", err)
	}
	return
}
