package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"api/src/model"

	. "api/src/common"

	"github.com/google/uuid"
	"github.com/jinzhu/gorm"
)

// 通知渠道校验错误（sentinel errors，避免字符串比较）
var (
	ErrChannelNotSynced = errors.New("channel_not_synced")
	ErrChannelNotOwned  = errors.New("channel_not_owned")
)

// NotificationAdmin 通知管理服务
type NotificationAdmin struct{}

// --- 通知渠道镜像同步（CPGateway 推送） ---

// UpsertChannel 创建或更新本地通知渠道镜像
func (n *NotificationAdmin) UpsertChannel(ctx context.Context, ch *model.NotificationChannel) error {
	ctx, db := GetContextDB(ctx)

	var existing model.NotificationChannel
	err := db.Where("uuid = ?", ch.UUID).First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		return db.Create(ch).Error
	}
	if err != nil {
		return err
	}

	return db.Model(&existing).Updates(map[string]interface{}{
		"org_id":  ch.OrgID,
		"name":    ch.Name,
		"type":    ch.Type,
		"config":  ch.Config,
		"enabled": ch.Enabled,
	}).Error
}

// DeleteChannel 删除本地通知渠道镜像（同时清理相关 binding）
func (n *NotificationAdmin) DeleteChannel(ctx context.Context, channelUUID string) error {
	ctx, db := GetContextDB(ctx)

	return db.Transaction(func(tx *gorm.DB) error {
		// 先删除绑定关系
		if err := tx.Where("channel_uuid = ?", channelUUID).Delete(&model.AlarmNotificationBinding{}).Error; err != nil {
			return err
		}
		// 再删除渠道镜像
		return tx.Where("uuid = ?", channelUUID).Delete(&model.NotificationChannel{}).Error
	})
}

// BulkSyncChannels 全量同步渠道（UPSERT + 清理过期，整体事务保护）
func (n *NotificationAdmin) BulkSyncChannels(ctx context.Context, channels []model.NotificationChannel) error {
	ctx, db := GetContextDB(ctx)

	return db.Transaction(func(tx *gorm.DB) error {
		// 收集本次同步的所有 UUID
		syncedUUIDs := make([]string, 0, len(channels))
		for _, ch := range channels {
			syncedUUIDs = append(syncedUUIDs, ch.UUID)

			var existing model.NotificationChannel
			err := tx.Where("uuid = ?", ch.UUID).First(&existing).Error
			if err == gorm.ErrRecordNotFound {
				if err := tx.Create(&ch).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else {
				if err := tx.Model(&existing).Updates(map[string]interface{}{
					"org_id":  ch.OrgID,
					"name":    ch.Name,
					"type":    ch.Type,
					"config":  ch.Config,
					"enabled": ch.Enabled,
				}).Error; err != nil {
					return err
				}
			}
		}

		// 清理本地有但上游已不存在的过期渠道，同时清理关联的绑定记录
		if len(syncedUUIDs) > 0 {
			// 先清理即将被删除渠道的绑定关系，防止产生孤立 Binding
			if err := tx.Where("channel_uuid NOT IN (?)", syncedUUIDs).Delete(&model.AlarmNotificationBinding{}).Error; err != nil {
				return err
			}
			if err := tx.Where("uuid NOT IN (?)", syncedUUIDs).Delete(&model.NotificationChannel{}).Error; err != nil {
				return err
			}
		} else {
			// 全量为空 = 上游没有任何渠道，清空本地渠道及所有绑定
			if err := tx.Where("1 = 1").Delete(&model.AlarmNotificationBinding{}).Error; err != nil {
				return err
			}
			if err := tx.Where("1 = 1").Delete(&model.NotificationChannel{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetChannelsByUUIDs 根据 UUID 列表查询本地渠道
func (n *NotificationAdmin) GetChannelsByUUIDs(ctx context.Context, uuids []string) ([]*model.NotificationChannel, error) {
	ctx, db := GetContextDB(ctx)
	var channels []*model.NotificationChannel
	if err := db.Where("uuid IN (?)", uuids).Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

// GetChannelByUUID 查询单个渠道
func (n *NotificationAdmin) GetChannelByUUID(ctx context.Context, channelUUID string) (*model.NotificationChannel, error) {
	ctx, db := GetContextDB(ctx)
	var ch model.NotificationChannel
	if err := db.Where("uuid = ?", channelUUID).First(&ch).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

// ValidateChannelOwnership 校验渠道归属权（租户级隔离）
// 区分两种错误：渠道未同步到本地（channel_not_synced）和渠道不属于当前组织（channel_not_owned）
func (n *NotificationAdmin) ValidateChannelOwnership(ctx context.Context, channelUUIDs []string, orgID int64) error {
	ctx, db := GetContextDB(ctx)

	// 先检查渠道是否存在于本地镜像
	var existCount int64
	db.Model(&model.NotificationChannel{}).Where("uuid IN (?)", channelUUIDs).Count(&existCount)
	if int(existCount) != len(channelUUIDs) {
		return ErrChannelNotSynced
	}

	// 再检查归属权（按组织）
	var ownedCount int64
	db.Model(&model.NotificationChannel{}).Where(
		"uuid IN (?) AND org_id = ?", channelUUIDs, orgID,
	).Count(&ownedCount)
	if int(ownedCount) != len(channelUUIDs) {
		return ErrChannelNotOwned
	}

	return nil
}

// --- 告警绑定管理 ---

// SetRuleBindings 设置规则的渠道绑定（全量替换，事务保证原子性）
func (n *NotificationAdmin) SetRuleBindings(ctx context.Context, ruleGroupUUID string, channelUUIDs []string, orgID int64) error {
	ctx, db := GetContextDB(ctx)

	return db.Transaction(func(tx *gorm.DB) error {
		// 先删除旧绑定
		if err := tx.Where("rule_group_uuid = ?", ruleGroupUUID).Delete(&model.AlarmNotificationBinding{}).Error; err != nil {
			return err
		}

		// 创建新绑定
		for _, chUUID := range channelUUIDs {
			binding := &model.AlarmNotificationBinding{
				RuleGroupUUID: ruleGroupUUID,
				ChannelUUID:   chUUID,
				OrgID:         orgID,
			}
			if err := tx.Create(binding).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetRuleBindings 获取规则的所有渠道绑定
func (n *NotificationAdmin) GetRuleBindings(ctx context.Context, ruleGroupUUID string) ([]*model.AlarmNotificationBinding, error) {
	ctx, db := GetContextDB(ctx)
	var bindings []*model.AlarmNotificationBinding
	if err := db.Where("rule_group_uuid = ?", ruleGroupUUID).Find(&bindings).Error; err != nil {
		return nil, err
	}
	return bindings, nil
}

// GetBoundChannels 获取规则绑定的所有渠道
func (n *NotificationAdmin) GetBoundChannels(ctx context.Context, ruleGroupUUID string) ([]*model.NotificationChannel, error) {
	ctx, db := GetContextDB(ctx)

	var bindings []*model.AlarmNotificationBinding
	if err := db.Where("rule_group_uuid = ?", ruleGroupUUID).Find(&bindings).Error; err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}

	uuids := make([]string, len(bindings))
	for i, b := range bindings {
		uuids[i] = b.ChannelUUID
	}

	var channels []*model.NotificationChannel
	if err := db.Where("uuid IN (?) AND enabled = ?", uuids, true).Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

// DeleteRuleBindings 删除规则的所有绑定（规则删除时调用）
func (n *NotificationAdmin) DeleteRuleBindings(ctx context.Context, ruleGroupUUID string) error {
	ctx, db := GetContextDB(ctx)
	return db.Where("rule_group_uuid = ?", ruleGroupUUID).Delete(&model.AlarmNotificationBinding{}).Error
}

// --- 告警事件管理 ---

// UpsertAlarmEvent 以 fingerprint 为幂等键更新告警事件
func (n *NotificationAdmin) UpsertAlarmEvent(ctx context.Context, fingerprint string, alertData map[string]string, alertStatus string, startsAt time.Time) (*model.AlarmEvent, string, error) {
	ctx, db := GetContextDB(ctx)

	var existing model.AlarmEvent
	err := db.Where("fingerprint = ?", fingerprint).First(&existing).Error

	now := time.Now()
	labelsJSON, err2 := json.Marshal(alertData)
	if err2 != nil {
		return nil, "", fmt.Errorf("failed to marshal alert labels: %w", err2)
	}

	if err == gorm.ErrRecordNotFound {
		// 新告警
		event := &model.AlarmEvent{
			Model:         model.Model{UUID: uuid.New().String()},
			Fingerprint:   fingerprint,
			RuleGroupUUID: alertData["rule_group"],
			AlertName:     alertData["alertname"],
			Owner:         alertData["owner"],
			VMUUID:        alertData["vm_uuid"],
			VMName:        alertData["vm_name"],
			Severity:      alertData["severity"],
			Status:        "firing",
			Summary:       alertData["summary"],
			Labels:        string(labelsJSON),
			FiredAt:       startsAt,
			LastFiredAt:   now,
		}
		if err := db.Create(event).Error; err != nil {
			return nil, "", err
		}
		return event, "firing_trigger", nil
	}
	if err != nil {
		return nil, "", err
	}

	// 已存在的告警
	if alertStatus == "resolved" {
		existing.Status = "resolved"
		existing.ResolvedAt = &now
		if err := db.Model(&existing).Updates(map[string]interface{}{
			"status":      "resolved",
			"resolved_at": now,
		}).Error; err != nil {
			return nil, "", err
		}
		return &existing, "resolved", nil
	}

	// 仍在 firing —— 更新 LastFiredAt
	if err := db.Model(&existing).Update("last_fired_at", now).Error; err != nil {
		return nil, "", err
	}
	existing.LastFiredAt = now
	return &existing, "repeat_remind", nil
}

// ListAlarmEvents 分页查询告警事件（支持按 owner 过滤，实现租户隔离）
func (n *NotificationAdmin) ListAlarmEvents(ctx context.Context, owner string, status string, page, pageSize int) (int64, []*model.AlarmEvent, error) {
	ctx, db := GetContextDB(ctx)

	query := db.Model(&model.AlarmEvent{})
	if owner != "" {
		query = query.Where("owner = ?", owner)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}

	var events []*model.AlarmEvent
	offset := (page - 1) * pageSize
	if err := query.Order("last_fired_at DESC").Offset(offset).Limit(pageSize).Find(&events).Error; err != nil {
		return 0, nil, err
	}

	return total, events, nil
}

// CountFiringEvents 统计 firing 状态的告警数，owner 为空时统计全局（内部接口用）
func (n *NotificationAdmin) CountFiringEvents(ctx context.Context, owner string) (int64, error) {
	ctx, db := GetContextDB(ctx)
	query := db.Model(&model.AlarmEvent{}).Where("status = ?", "firing")
	if owner != "" {
		query = query.Where("owner = ?", owner)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// GetAlarmEvent 获取单个告警事件
func (n *NotificationAdmin) GetAlarmEvent(ctx context.Context, eventUUID string) (*model.AlarmEvent, error) {
	ctx, db := GetContextDB(ctx)
	var event model.AlarmEvent
	if err := db.Where("uuid = ?", eventUUID).First(&event).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

// --- 发送流水管理 ---

// CreateDeliveryLog 创建发送流水记录
func (n *NotificationAdmin) CreateDeliveryLog(ctx context.Context, log *model.AlarmDeliveryLog) error {
	ctx, db := GetContextDB(ctx)
	log.Model = model.Model{UUID: uuid.New().String()}
	return db.Create(log).Error
}

// ListDeliveryLogs 查询某个事件的发送流水
func (n *NotificationAdmin) ListDeliveryLogs(ctx context.Context, eventUUID string) ([]*model.AlarmDeliveryLog, error) {
	ctx, db := GetContextDB(ctx)
	var logs []*model.AlarmDeliveryLog
	if err := db.Where("event_uuid = ?", eventUUID).Order("sent_at DESC").Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}
