package apis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"api/src/model"
	"api/src/services"

	. "api/src/common"

	"github.com/gin-gonic/gin"
)

// NotificationAPI 通知相关 API
type NotificationAPI struct {
	admin    *services.NotificationAdmin
	notifier *services.AlarmNotifier
}

var notificationAPI = &NotificationAPI{
	admin:    &services.NotificationAdmin{},
	notifier: services.NewAlarmNotifier(),
}

// --- 内部通知渠道同步接口（CPGateway 推送，Authorize 中间件校验 X-Forwarded-Secret）---

// SyncChannel 处理 CPGateway 推送的渠道同步请求
// @Summary Sync notification channels
// @Description Internal endpoint for CPGateway to push notification channel changes (upsert, delete, bulk_sync)
// @Tags Notification
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "Sync successful"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /internal/notification-channels/sync [post]
func (a *NotificationAPI) SyncChannel(c *gin.Context) {
	var req struct {
		Action      string           `json:"action" binding:"required"` // upsert, delete, bulk_sync
		Channel     *channelPayload  `json:"channel"`
		ChannelUUID string           `json:"channel_uuid"`
		Channels    []channelPayload `json:"channels"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	switch req.Action {
	case "upsert":
		if req.Channel == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required for upsert"})
			return
		}
		configJSON, err := json.Marshal(req.Channel.Config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel config: " + err.Error()})
			return
		}
		ch := &model.NotificationChannel{
			OrgID:   req.Channel.OrgID,
			Name:    req.Channel.Name,
			Type:    req.Channel.Type,
			Config:  string(configJSON),
			Enabled: req.Channel.Enabled,
		}
		ch.UUID = req.Channel.UUID
		if err := a.admin.UpsertChannel(ctx, ch); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

	case "delete":
		if req.ChannelUUID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "channel_uuid is required for delete"})
			return
		}
		if err := a.admin.DeleteChannel(ctx, req.ChannelUUID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

	case "bulk_sync":
		channels := make([]model.NotificationChannel, len(req.Channels))
		for i, ch := range req.Channels {
			configJSON, err := json.Marshal(ch.Config)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel config: " + err.Error()})
				return
			}
			channels[i] = model.NotificationChannel{
				OrgID:   ch.OrgID,
				Name:    ch.Name,
				Type:    ch.Type,
				Config:  string(configJSON),
				Enabled: ch.Enabled,
			}
			channels[i].UUID = ch.UUID
		}
		if err := a.admin.BulkSyncChannels(ctx, channels); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported action: " + req.Action})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type channelPayload struct {
	UUID    string                 `json:"uuid"`
	OrgID   int64                  `json:"org_id"`
	Name    string                 `json:"name"`
	Type    string                 `json:"type"`
	Config  map[string]interface{} `json:"config"`
	Enabled bool                   `json:"enabled"`
}

// --- 告警规则绑定渠道 ---

// BindRuleChannels 为告警规则绑定通知渠道
// @Summary Bind notification channels to alarm rule
// @Description Bind one or more notification channels to an alarm rule group
// @Tags Notification
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "Binding successful"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Failure 403 {object} map[string]interface{} "Channel not owned"
// @Failure 409 {object} map[string]interface{} "Channel not synced"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /alarm/rule-channels [post]
func (a *NotificationAPI) BindRuleChannels(c *gin.Context) {
	var req struct {
		RuleGroupUUID string   `json:"rule_group_uuid" binding:"required"`
		ChannelUUIDs  []string `json:"channel_uuids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)

	// 校验渠道归属权（租户级）
	if err := a.admin.ValidateChannelOwnership(ctx, req.ChannelUUIDs, memberShip.OrgID); err != nil {
		if errors.Is(err, services.ErrChannelNotSynced) {
			c.JSON(http.StatusConflict, gin.H{
				"error":   "channel_not_synced",
				"message": "Some channels are not yet synced to this region. Please retry.",
			})
		} else if errors.Is(err, services.ErrChannelNotOwned) {
			c.JSON(http.StatusForbidden, gin.H{
				"error":   "channel_not_owned",
				"message": "Some channels do not belong to you.",
			})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	if err := a.admin.SetRuleBindings(ctx, req.RuleGroupUUID, req.ChannelUUIDs, memberShip.OrgID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetRuleChannels 获取告警规则绑定的通知渠道
// @Summary Get notification channels bound to alarm rule
// @Description Get the list of notification channels bound to a specific alarm rule group
// @Tags Notification
// @Accept json
// @Produce json
// @Param uuid path string true "Rule group UUID"
// @Success 200 {object} map[string]interface{} "Rule channel bindings"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /alarm/rule-channels/{uuid} [get]
func (a *NotificationAPI) GetRuleChannels(c *gin.Context) {
	ruleGroupUUID := c.Param("uuid")
	ctx := c.Request.Context()

	bindings, err := a.admin.GetRuleBindings(ctx, ruleGroupUUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"bindings": bindings})
}

// --- 告警事件查询 ---

// ListAlarmEvents 查询告警事件列表（按当前用户的组织过滤）
// @Summary List alarm events
// @Description List alarm events filtered by the current user's organization, supports pagination and status filter
// @Tags Notification
// @Accept json
// @Produce json
// @Param status query string false "Filter by status (firing, resolved)"
// @Param count_only query string false "If 'true', only return firing event count"
// @Param page query int false "Page number" default(1)
// @Param page_size query int false "Page size" default(20)
// @Success 200 {object} map[string]interface{} "Alarm events list"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /alarm/events [get]
func (a *NotificationAPI) ListAlarmEvents(c *gin.Context) {
	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)

	statusFilter := c.Query("status")
	countOnly := c.Query("count_only")

	// count_only 模式：只返回数量（按当前组织过滤）
	if countOnly == "true" {
		count, err := a.admin.CountFiringEvents(ctx, strconv.FormatInt(memberShip.OrgID, 10))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"count": count})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 按当前用户的组织 ID 过滤，实现租户隔离（owner 字段存储的是 OrgID 字符串）
	total, events, err := a.admin.ListAlarmEvents(ctx, strconv.FormatInt(memberShip.OrgID, 10), statusFilter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  total,
		"page":   page,
		"limit":  pageSize,
		"events": events,
	})
}

// GetAlarmDeliveryLogs 查询告警事件的发送流水（校验事件归属权）
// @Summary Get alarm delivery logs
// @Description Get notification delivery logs for a specific alarm event (validates event ownership)
// @Tags Notification
// @Accept json
// @Produce json
// @Param event_uuid path string true "Alarm event UUID"
// @Success 200 {object} map[string]interface{} "Delivery logs"
// @Failure 403 {object} map[string]interface{} "Access denied"
// @Failure 404 {object} map[string]interface{} "Event not found"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /alarm/events/{event_uuid}/delivery-logs [get]
func (a *NotificationAPI) GetAlarmDeliveryLogs(c *gin.Context) {
	eventUUID := c.Param("event_uuid")
	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)

	// 先校验事件归属当前用户的组织
	event, err := a.admin.GetAlarmEvent(ctx, eventUUID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "alarm event not found"})
		return
	}
	if event.Owner != strconv.FormatInt(memberShip.OrgID, 10) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	logs, err := a.admin.ListDeliveryLogs(ctx, eventUUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"delivery_logs": logs})
}

// --- 内部告警事件查询（CPGateway 全局汇总用）---

// InternalListAlarmEvents CPGateway 内部调用的告警事件接口
// @Summary Internal list alarm events
// @Description Internal endpoint for CPGateway to query alarm events without owner filtering
// @Tags Notification
// @Accept json
// @Produce json
// @Param count_only query string false "If 'true', only return firing event count"
// @Param status query string false "Filter by status"
// @Param page query int false "Page number" default(1)
// @Param page_size query int false "Page size" default(20)
// @Success 200 {object} map[string]interface{} "Alarm events"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /internal/alarm/events [get]
func (a *NotificationAPI) InternalListAlarmEvents(c *gin.Context) {
	ctx := c.Request.Context()
	countOnly := c.Query("count_only")

	if countOnly == "true" {
		// 内部接口：全局统计，不按 owner 过滤（CPGateway alarm_summary 使用）
		count, err := a.admin.CountFiringEvents(ctx, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"count": count})
		return
	}

	// 内部接口不过滤 owner，返回全量数据供 CPGateway 聚合
	statusFilter := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	total, events, err := a.admin.ListAlarmEvents(ctx, "", statusFilter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":  total,
		"page":   page,
		"limit":  pageSize,
		"events": events,
	})
}

// --- 升级后的 AlertManager 回调处理 ---

// ProcessAlertWebhookV2 处理 AlertManager 回调（fingerprint 幂等 + 通知推送）
// @Summary Process AlertManager webhook
// @Description Handle AlertManager callback with fingerprint-based idempotent event upsert and notification dispatch
// @Tags Notification
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "Processed result"
// @Failure 400 {object} map[string]interface{} "Invalid alert payload"
// @Router /alerts/process [post]
func (a *NotificationAPI) ProcessAlertWebhookV2(c *gin.Context) {
	var notification struct {
		Status string `json:"status"`
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			StartsAt    time.Time         `json:"startsAt"`
			EndsAt      time.Time         `json:"endsAt"`
			Fingerprint string            `json:"fingerprint"`
		} `json:"alerts"`
	}

	if err := c.ShouldBindJSON(&notification); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid alert payload"})
		return
	}

	ctx := c.Request.Context()
	processedCount := 0

	for _, alert := range notification.Alerts {
		if alert.Fingerprint == "" {
			logger.Warningf("Alert missing fingerprint, skipping")
			continue
		}

		// 合并 labels + annotations 作为 alertData
		alertData := make(map[string]string)
		for k, v := range alert.Labels {
			alertData[k] = v
		}
		if summary, ok := alert.Annotations["summary"]; ok {
			alertData["summary"] = summary
		}

		// 以 fingerprint 为幂等键 UPSERT 事件
		event, notifyType, err := a.admin.UpsertAlarmEvent(
			ctx, alert.Fingerprint, alertData, alert.Status, alert.StartsAt,
		)
		if err != nil {
			logger.Errorf("Failed to upsert alarm event (fingerprint=%s): %v", alert.Fingerprint, err)
			continue
		}

		// 查找绑定的渠道并发送通知
		if event.RuleGroupUUID != "" {
			go a.sendNotifications(event, notifyType)
		}
		processedCount++
	}

	// 保持对旧 ip_blocked 告警的兼容处理
	for _, alert := range notification.Alerts {
		if alert.Status == "firing" && alert.Labels["alert_type"] == "ip_blocked" && SwitchAPIEndpoint != "" {
			reqBody := map[string]interface{}{
				"mode":       "simple",
				"ip_address": alert.Labels["ip"],
				"house":      SwitchAPIHouse,
				"reason":     "Block IP for detect attack",
				"comments":   alert.Annotations["summary"],
			}
			go alarmAPI.sendSwitchAPIRequest(reqBody)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "processed",
		"processed": processedCount,
	})
}

// sendNotifications 异步发送通知到绑定的渠道
func (a *NotificationAPI) sendNotifications(event *model.AlarmEvent, notifyType string) {
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("Panic in sendNotifications for event %s: %v", event.UUID, r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = SetContextDB(ctx, DB())
	channels, err := a.admin.GetBoundChannels(ctx, event.RuleGroupUUID)
	if err != nil {
		logger.Errorf("Failed to get bound channels for rule %s: %v", event.RuleGroupUUID, err)
		return
	}
	if len(channels) == 0 {
		return
	}

	for _, channel := range channels {
		result := a.notifier.SendNotification(channel, event, notifyType)

		// 记录发送流水
		deliveryLog := &model.AlarmDeliveryLog{
			EventUUID:    event.UUID,
			ChannelUUID:  result.ChannelUUID,
			ChannelName:  result.ChannelName,
			ChannelType:  result.ChannelType,
			NotifyType:   notifyType,
			Status:       result.Status,
			ErrorMessage: result.Error,
			SentAt:       time.Now(),
		}
		if err := a.admin.CreateDeliveryLog(ctx, deliveryLog); err != nil {
			logger.Errorf("Failed to create delivery log: %v", err)
		}
	}
}
