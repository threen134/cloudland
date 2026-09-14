package apis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/services"
	"cpgateway-go/src/tracing"
)

func settingsListResponse(db *gorm.DB) gin.H {
	rows := services.GetAllSettings(db)
	out := make([]settingOut, 0, len(rows))
	for i := range rows {
		out = append(out, toSettingOut(&rows[i]))
	}
	return gin.H{"settings": out}
}

// GET /system/settings (superuser)
func ListSystemSettings(c *gin.Context) {
	c.JSON(http.StatusOK, settingsListResponse(dbs.DBContext(c.Request.Context())))
}

// PUT /system/settings (superuser) — body is {KEY: value, ...}; unknown keys are ignored and
// masked secrets ("******") are not written back.
func UpdateSystemSettings(c *gin.Context) {
	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		common.AbortValidation(c, "body", err)
		return
	}
	valid := map[string]interface{}{}
	keys := make([]string, 0, len(payload))
	for k, v := range payload {
		if _, ok := services.FindSettingMeta(k); ok {
			valid[k] = v
			keys = append(keys, k)
		}
	}
	if len(valid) == 0 {
		common.AbortWithDetail(c, http.StatusBadRequest, "No valid settings keys provided")
		return
	}

	db := dbs.DBContext(c.Request.Context())
	version, err := services.BulkUpdateSettings(db, valid)
	if err != nil {
		internalServerError(c, err)
		return
	}
	log.WithContext(c).Infof("System settings updated: %v, config_version=%d", keys, version)
	go services.PushSettingsToAllRegions(context.WithoutCancel(c.Request.Context()))
	c.JSON(http.StatusOK, settingsListResponse(db))
}

// POST /system/settings/test-notification (superuser)
func TestNotification(c *gin.Context) {
	var in struct {
		Channel string `json:"channel" binding:"required"`
	}
	if !bindJSON(c, &in) {
		return
	}
	db := dbs.DBContext(c.Request.Context())
	var success bool
	var message string
	switch in.Channel {
	case "email":
		success, message = testEmailChannel(c.Request.Context(), db)
	case "feishu":
		success, message = testFeishuChannel(c.Request.Context(), db)
	case "slack":
		success, message = testSlackChannel(c.Request.Context(), db)
	case "webhook":
		success, message = testWebhookChannel(c.Request.Context(), db)
	default:
		common.AbortWithDetail(c, http.StatusBadRequest, fmt.Sprintf("Unsupported channel: %s", in.Channel))
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel": in.Channel, "success": success, "message": message})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func testEmailChannel(ctx context.Context, db *gorm.DB) (bool, string) {
	cfg := services.LoadEmailConfig(db)
	if cfg.SMTP.Host == "" {
		return false, "SMTP_HOST not configured"
	}
	msg := common.BuildPlainMessage(fmt.Sprintf("[%s] Test Notification", cfg.FromName),
		common.FormatAddress(cfg.FromName, cfg.From), cfg.From, "This is a test notification from CloudLand.")
	// SMTP 为外部服务：只建 span
	_, mailSpan := tracing.StartChild(ctx, "notify.email", trace.WithSpanKind(trace.SpanKindClient))
	mailErr := common.SendMail(cfg.SMTP, cfg.From, cfg.From, msg)
	tracing.EndSpan(mailSpan, mailErr)
	if err := mailErr; err != nil {
		return false, err.Error()
	}
	return true, "Test email sent successfully"
}

func postJSON(ctx context.Context, method, url string, payload interface{}, headers map[string]string) (int, []byte, error) {
	// 通知测试目标均为外部服务：只建 span，不注入 trace 头
	ctx, span := tracing.StartChild(ctx, "notify.test", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, nil
}

func testFeishuChannel(ctx context.Context, db *gorm.DB) (bool, string) {
	webhookURL := services.GetSettingString(db, "FEISHU_WEBHOOK_URL", "")
	secret := services.GetSettingString(db, "FEISHU_SECRET", "")
	if webhookURL == "" {
		return false, "FEISHU_WEBHOOK_URL not configured"
	}
	payload := map[string]interface{}{
		"msg_type": "text",
		"content":  map[string]string{"text": "[CloudLand] Test notification — 飞书渠道连通性测试成功"},
	}
	if secret != "" {
		ts := time.Now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = services.FeishuSign(secret, ts)
	}
	status, body, err := postJSON(ctx, http.MethodPost, webhookURL, payload, nil)
	if err != nil {
		return false, err.Error()
	}
	if status != http.StatusOK {
		return false, fmt.Sprintf("HTTP %d", status)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return false, err.Error()
	}
	for _, key := range []string{"code", "StatusCode"} {
		if v, ok := data[key]; ok && v != nil {
			if f, isNum := services.ToFloat(v); isNum && f == 0 {
				return true, "Feishu webhook OK"
			}
		}
	}
	return false, fmt.Sprintf("Feishu returned: %v", data)
}

func testSlackChannel(ctx context.Context, db *gorm.DB) (bool, string) {
	webhookURL := services.GetSettingString(db, "SLACK_WEBHOOK_URL", "")
	if webhookURL == "" {
		return false, "SLACK_WEBHOOK_URL not configured"
	}
	payload := map[string]string{"text": "[CloudLand] Test notification — Slack channel connectivity test successful"}
	status, body, err := postJSON(ctx, http.MethodPost, webhookURL, payload, nil)
	if err != nil {
		return false, err.Error()
	}
	if status == http.StatusOK {
		return true, "Slack webhook OK"
	}
	return false, fmt.Sprintf("HTTP %d: %s", status, truncate(string(body), 200))
}

func testWebhookChannel(ctx context.Context, db *gorm.DB) (bool, string) {
	webhookURL := services.GetSettingString(db, "CUSTOM_WEBHOOK_URL", "")
	method := strings.ToUpper(services.GetSettingString(db, "CUSTOM_WEBHOOK_METHOD", "POST"))

	headers := map[string]string{}
	rawHeaders := services.GetSetting(db, "CUSTOM_WEBHOOK_HEADERS")
	if s, ok := rawHeaders.(string); ok {
		var parsed interface{}
		if json.Unmarshal([]byte(s), &parsed) == nil {
			rawHeaders = parsed
		}
	}
	if m, ok := rawHeaders.(map[string]interface{}); ok {
		for k, v := range m {
			headers[k] = fmt.Sprint(v)
		}
	}

	if webhookURL == "" {
		return false, "CUSTOM_WEBHOOK_URL not configured"
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return false, fmt.Sprintf("Unsupported HTTP method: %s", method)
	}

	payload := map[string]string{"event": "test", "message": "CloudLand webhook connectivity test"}
	status, body, err := postJSON(ctx, method, webhookURL, payload, headers)
	if err != nil {
		return false, err.Error()
	}
	if status < 300 {
		return true, fmt.Sprintf("Webhook OK (HTTP %d)", status)
	}
	return false, fmt.Sprintf("HTTP %d: %s", status, truncate(string(body), 200))
}
