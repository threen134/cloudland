package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"api/src/dbs"
	"api/src/model"

	. "api/src/common"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 通知渠道校验错误（sentinel errors，避免字符串比较）
var (
	ErrChannelNotSynced = errors.New("channel_not_synced")
	ErrChannelNotOwned  = errors.New("channel_not_owned")
	// The rule group a binding names is neither a VM alarm rule group nor a node alarm rule
	ErrRuleGroupNotFound = errors.New("rule_group_not_found")
	// The rule group belongs to another organization, or is a node alarm rule and the caller no system admin
	ErrRuleGroupNotOwned = errors.New("rule_group_not_owned")
)

// NotificationAdmin 通知管理服务
type NotificationAdmin struct{}

// --- 通知渠道镜像同步（CPGateway 推送） ---

// UpsertChannel 创建或更新本地通知渠道镜像
// ch.OrgUUID names the owning organization; its local ID is resolved here (see resolveChannelOrg).
func (n *NotificationAdmin) UpsertChannel(ctx context.Context, ch *model.NotificationChannel) error {
	ctx, db := GetContextDB(ctx)
	ch.OrgID = resolveChannelOrg(ctx, ch.OrgUUID)

	var existing model.NotificationChannel
	// uuid 有唯一索引（告警绑定外键依赖），查找需包含软删除记录，重新下发时恢复原记录而非新建；
	// 优先取未删除、最新的一行，避免恢复旧的软删除行
	err := db.Unscoped().Where("uuid = ?", ch.UUID).Order("deleted_at IS NOT NULL, id DESC").Take(&existing).Error
	if err == gorm.ErrRecordNotFound {
		return db.Create(ch).Error
	}
	if err != nil {
		return err
	}

	return db.Unscoped().Model(&existing).Updates(channelMirrorColumns(ch)).Error
}

// channelMirrorColumns is what a sync writes over an existing mirror row (a map: false and 0 must be written)
func channelMirrorColumns(ch *model.NotificationChannel) map[string]interface{} {
	return map[string]interface{}{
		"org_id":     ch.OrgID,
		"org_uuid":   ch.OrgUUID,
		"name":       ch.Name,
		"type":       ch.Type,
		"config":     ch.Config,
		"enabled":    ch.Enabled,
		"deleted_at": nil,
	}
}

// resolveChannelOrg turns the organization UUID a channel arrives with into the organization's local ID.
// CPGateway and each region number their organizations independently, so the control plane's own ID
// means nothing here (it used to be stored as is: channels of every organization but the first were
// bound to whichever local organization happened to have that number). An organization not synced to
// this region yet resolves to 0: the channel is kept, owned by nobody, and bindPendingChannels gives it
// its owner once the organization is here. Rejecting it instead would lose the channel for good when
// the organization arrives after it (a full region sync pushes the channels before the organizations).
func resolveChannelOrg(ctx context.Context, orgUUID string) int64 {
	if orgUUID == "" {
		return 0
	}
	orgID, err := orgAdmin.GetOrgIDByUUID(ctx, orgUUID)
	if err != nil || orgID <= 0 {
		logger.Ctx(ctx).Warningf("Notification channel of organization %s kept unbound: organization not in this region yet (%v)", orgUUID, err)
		return 0
	}
	return orgID
}

// bindPendingChannels gives the channels that arrived before their organization (org_id 0) their local
// organization ID, when it is known by now. Called before every lookup of channels by organization; one
// indexed query when nothing is pending.
func bindPendingChannels(ctx context.Context) {
	ctx, db := GetContextDB(ctx)
	var orgUUIDs []string
	if err := db.Model(&model.NotificationChannel{}).Where("org_id = ? AND org_uuid <> ?", 0, "").
		Distinct("org_uuid").Pluck("org_uuid", &orgUUIDs).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to look up unbound notification channels: %v", err)
		return
	}
	for _, orgUUID := range orgUUIDs {
		// Still unknown is the normal case until the organization is synced: no log
		orgID, err := orgAdmin.GetOrgIDByUUID(ctx, orgUUID)
		if err != nil || orgID <= 0 {
			continue
		}
		if err := db.Model(&model.NotificationChannel{}).Where("org_uuid = ? AND org_id = ?", orgUUID, 0).
			Update("org_id", orgID).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to bind notification channels of organization %s: %v", orgUUID, err)
		}
	}
}

// EnabledChannelsOfOrg returns the enabled notification channels of a local organization.
func (n *NotificationAdmin) EnabledChannelsOfOrg(ctx context.Context, orgID int64) ([]*model.NotificationChannel, error) {
	channels := []*model.NotificationChannel{}
	// 0 is the owner of channels whose organization is not resolved yet, never a real organization
	if orgID <= 0 {
		return channels, nil
	}
	bindPendingChannels(ctx)
	ctx, db := GetContextDB(ctx)
	if err := db.Where("org_id = ? AND enabled = ?", orgID, true).Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

// platformOrgIDCache holds the system organization's ID once read: it never changes
var platformOrgIDCache atomic.Int64

// platformOrgID returns the local ID of the system organization (org_type 2, created by adminInit), which owns
// the alarms about the platform itself, or 0 when it cannot be read.
func platformOrgID(ctx context.Context) int64 {
	if id := platformOrgIDCache.Load(); id > 0 {
		return id
	}
	_, db := GetContextDB(ctx)
	org := &model.Organization{}
	if err := db.Select("id").Where("org_type = ?", model.OrgTypeSystem).Order("id").Take(org).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to find the system organization: %v", err)
		return 0
	}
	platformOrgIDCache.Store(org.ID)
	return org.ID
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
// Each channel names its organization by UUID; the local ID is resolved as in UpsertChannel.
func (n *NotificationAdmin) BulkSyncChannels(ctx context.Context, channels []model.NotificationChannel) error {
	ctx, db := GetContextDB(ctx)
	for i := range channels {
		channels[i].OrgID = resolveChannelOrg(ctx, channels[i].OrgUUID)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// 收集本次同步的所有 UUID
		syncedUUIDs := make([]string, 0, len(channels))
		for _, ch := range channels {
			syncedUUIDs = append(syncedUUIDs, ch.UUID)

			var existing model.NotificationChannel
			err := tx.Unscoped().Where("uuid = ?", ch.UUID).Order("deleted_at IS NOT NULL, id DESC").Take(&existing).Error
			if err == gorm.ErrRecordNotFound {
				if err := tx.Create(&ch).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else {
				if err := tx.Unscoped().Model(&existing).Updates(channelMirrorColumns(&ch)).Error; err != nil {
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
	bindPendingChannels(ctx)
	ctx, db := GetContextDB(ctx)

	// 先检查渠道是否存在于本地镜像
	var existCount int64
	db.Model(&model.NotificationChannel{}).Where("uuid IN (?)", channelUUIDs).Count(&existCount)
	if int(existCount) != len(channelUUIDs) {
		return ErrChannelNotSynced
	}

	// Then the owner. 0 is the owner of channels whose organization is not resolved yet, never a
	// real organization
	if orgID <= 0 {
		return ErrChannelNotOwned
	}
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

// CheckRuleGroupAccess tells whether the caller may see or change the channels bound to a rule group: a VM
// alarm rule group of its own organization (any organization's for a system admin), or a node alarm rule
// (system admins only, like the node alarm rules themselves). Bindings used to be accepted for any rule group
// UUID, so anyone could replace, and so drop, the channels another organization had bound to its rules.
func (n *NotificationAdmin) CheckRuleGroupAccess(ctx context.Context, ruleGroupUUID string, orgID int64, systemAdmin bool) error {
	_, db := GetContextDB(ctx)
	group := &model.RuleGroupV2{}
	err := db.Select("owner").Where("uuid = ?", ruleGroupUUID).Take(group).Error
	if err == nil {
		if systemAdmin || (orgID > 0 && group.Owner == orgID) {
			return nil
		}
		return ErrRuleGroupNotOwned
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var nodeRules int64
	if err := db.Model(&model.NodeAlarmRule{}).Where("uuid = ?", ruleGroupUUID).Count(&nodeRules).Error; err != nil {
		return err
	}
	if nodeRules == 0 {
		return ErrRuleGroupNotFound
	}
	if !systemAdmin {
		return ErrRuleGroupNotOwned
	}
	return nil
}

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
// It returns the event and the notification it calls for: firing_trigger when the alert starts (or starts
// again), repeat_remind while it keeps firing, resolved when it ends, and "" when nothing changed (a resolved
// alert reported again), which must not be notified. Alertmanager fingerprints are a hash of the labels, so
// a later recurrence of the same alert comes back under the same fingerprint and reopens the event.
func (n *NotificationAdmin) UpsertAlarmEvent(ctx context.Context, fingerprint string, alertData map[string]string, alertStatus string, startsAt time.Time) (*model.AlarmEvent, string, error) {
	ctx, db := GetContextDB(ctx)

	var existing model.AlarmEvent
	err := db.Where("fingerprint = ?", fingerprint).First(&existing).Error

	now := time.Now()
	labelsJSON, err2 := json.Marshal(alertData)
	if err2 != nil {
		return nil, "", fmt.Errorf("failed to marshal alert labels: %w", err2)
	}
	resolved := alertStatus == "resolved"

	if err == gorm.ErrRecordNotFound {
		// 新告警
		vmUUID := alertData["vm_uuid"]
		if vmUUID == "" {
			vmUUID = alertData["instance_id"]
		}
		vmName := alertData["vm_name"]
		if vmName == "" {
			vmName = alertData["domain"]
		}
		event := &model.AlarmEvent{
			Model:         model.Model{UUID: uuid.New().String()},
			Fingerprint:   fingerprint,
			RuleGroupUUID: alertData["rule_group"],
			AlertName:     alertData["alertname"],
			Owner:         alarmEventOwner(ctx, alertData),
			VMUUID:        vmUUID,
			VMName:        vmName,
			Severity:      alertData["severity"],
			Status:        "firing",
			Summary:       alertData["summary"],
			Labels:        string(labelsJSON),
			FiredAt:       startsAt,
			LastFiredAt:   now,
		}
		notifyType := "firing_trigger"
		if resolved {
			// Seen for the first time already resolved (the firing report never reached us, e.g. clapi was
			// down): record it as what it is. It is still notified, being the only trace the alert fired.
			event.Status = "resolved"
			event.ResolvedAt = &now
			notifyType = "resolved"
		}
		if err := db.Create(event).Error; err != nil {
			return nil, "", err
		}
		return event, notifyType, nil
	}
	if err != nil {
		return nil, "", err
	}

	// 已存在的告警
	updates := map[string]interface{}{}
	// An event recorded without an owner (a node alert before its rules carried the owner label) is
	// nobody's: give it one, so the platform's operators see it
	if existing.Owner == "" {
		if owner := alarmEventOwner(ctx, alertData); owner != "" {
			updates["owner"] = owner
			existing.Owner = owner
		}
	}
	var notifyType string
	switch {
	case resolved && existing.Status == "resolved":
		// Reported again: nothing changes, and it was notified already
	case resolved:
		updates["status"] = "resolved"
		updates["resolved_at"] = now
		existing.Status = "resolved"
		existing.ResolvedAt = &now
		notifyType = "resolved"
	case existing.Status == "resolved":
		// The same alert fires again: reopen the event as a new occurrence
		updates["status"] = "firing"
		updates["resolved_at"] = nil
		updates["fired_at"] = startsAt
		updates["last_fired_at"] = now
		updates["summary"] = alertData["summary"]
		updates["labels"] = string(labelsJSON)
		existing.Status = "firing"
		existing.ResolvedAt = nil
		existing.FiredAt = startsAt
		existing.LastFiredAt = now
		existing.Summary = alertData["summary"]
		existing.Labels = string(labelsJSON)
		notifyType = "firing_trigger"
	default:
		// 仍在 firing —— 更新 LastFiredAt
		updates["last_fired_at"] = now
		existing.LastFiredAt = now
		notifyType = "repeat_remind"
	}
	if len(updates) > 0 {
		if err := db.Model(&existing).Updates(updates).Error; err != nil {
			return nil, "", err
		}
	}
	return &existing, notifyType, nil
}

// alarmEventOwner is the organization an alert's event belongs to: its owner label (the local organization
// ID). An alert without one is about the platform, not a tenant (node alerts from rule files rendered before
// the node templates carried the label): it goes to the system organization instead of to nobody, since the
// event list only shows an organization its own events.
func alarmEventOwner(ctx context.Context, alertData map[string]string) string {
	if owner := alertData["owner"]; owner != "" {
		return owner
	}
	if id := platformOrgID(ctx); id > 0 {
		return strconv.FormatInt(id, 10)
	}
	return ""
}

// ListAlarmEvents 分页查询告警事件（支持按 owner 过滤，实现租户隔离）
func (n *NotificationAdmin) ListAlarmEvents(ctx context.Context, owner string, status string, search string, page, pageSize int) (int64, []*model.AlarmEvent, error) {
	ctx, db := GetContextDB(ctx)

	query := db.Model(&model.AlarmEvent{})
	if owner != "" {
		query = query.Where("owner = ?", owner)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	// 按告警名与虚拟机名搜索。此前后端不支持搜索，前端只能在当前页的 20 条里过滤，
	// 翻到下一页搜的又是另外 20 条
	query = query.Scopes(dbs.Contains(search, "alert_name", "vm_name"))

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

// CleanupExpiredAlarmEvents 清理超过指定天数的告警事件及关联投递日志
func (n *NotificationAdmin) CleanupExpiredAlarmEvents(ctx context.Context, retentionDays int) (int64, error) {
	ctx, db := GetContextDB(ctx)
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	var deleted int64
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"DELETE FROM alarm_delivery_logs WHERE event_uuid IN (SELECT uuid FROM alarm_events WHERE last_fired_at < ?)",
			cutoff,
		).Error; err != nil {
			return fmt.Errorf("failed to delete expired delivery logs: %w", err)
		}
		result := tx.Where("last_fired_at < ?", cutoff).Delete(&model.AlarmEvent{})
		if result.Error != nil {
			return fmt.Errorf("failed to delete expired alarm events: %w", result.Error)
		}
		deleted = result.RowsAffected
		return nil
	})
	return deleted, err
}
