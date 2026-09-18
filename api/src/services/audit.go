/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"time"

	. "api/src/common"
)

const (
	// 审计日志保留天数：默认值与取值范围须与 cpgateway 的系统设置 AUDIT_LOG_RETENTION_DAYS 保持一致。
	// 设下限是为了防止误把审计记录清空
	DefaultAuditLogRetentionDays = 365
	MinAuditLogRetentionDays     = 90
	MaxAuditLogRetentionDays     = 3650

	auditCleanupBatchSize = 5000
	// 批次间停顿，让出锁与 I/O，避免长时间占用数据库
	auditCleanupBatchPause = 200 * time.Millisecond
)

// AuditLogRetentionDays 读取系统设置镜像，缺失或越界时回退默认值（cpgateway 已校验，这里兜底）
func AuditLogRetentionDays() int {
	days := GetMirrorSettingInt("AUDIT_LOG_RETENTION_DAYS", DefaultAuditLogRetentionDays)
	if days < MinAuditLogRetentionDays || days > MaxAuditLogRetentionDays {
		return DefaultAuditLogRetentionDays
	}
	return days
}

// CleanupExpiredAuditLogs 物理删除早于保留期的审计记录。分批删除：首次启用或停机很久后，
// 一条 DELETE 删几十万行会长时间持锁并产生大量 WAL
func CleanupExpiredAuditLogs(ctx context.Context, retentionDays int) (deleted int64, err error) {
	_, db := GetContextDB(ctx)
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	for {
		result := db.Exec(
			`DELETE FROM audit_logs WHERE id IN (SELECT id FROM audit_logs WHERE created_at < ? ORDER BY created_at LIMIT ?)`,
			cutoff, auditCleanupBatchSize,
		)
		if result.Error != nil {
			return deleted, result.Error
		}
		deleted += result.RowsAffected
		if result.RowsAffected < auditCleanupBatchSize {
			return deleted, nil
		}
		time.Sleep(auditCleanupBatchPause)
	}
}
