package services

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"api/src/model"
)

// NotifyResult 通知发送结果
type NotifyResult struct {
	ChannelUUID string
	ChannelName string
	ChannelType string
	Status      string // sent, failed
	Error       string
}

// AlarmNotifier 告警通知发送器（复用 http.Client 以利用连接池）
type AlarmNotifier struct {
	client *http.Client
}

func NewAlarmNotifier() *AlarmNotifier {
	return &AlarmNotifier{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// SendNotification 向指定渠道发送通知（同步执行，由调用方决定是否用 goroutine）
func (n *AlarmNotifier) SendNotification(
	channel *model.NotificationChannel,
	event *model.AlarmEvent,
	notifyType string, // firing_trigger, repeat_remind, resolved
) *NotifyResult {
	result := &NotifyResult{
		ChannelUUID: channel.UUID,
		ChannelName: channel.Name,
		ChannelType: channel.Type,
	}

	defer func() {
		if r := recover(); r != nil {
			result.Status = "failed"
			result.Error = fmt.Sprintf("panic: %v", r)
		}
	}()

	var err error
	switch channel.Type {
	case "feishu":
		err = n.sendFeishu(channel, event, notifyType)
	case "webhook":
		err = n.sendWebhook(channel, event, notifyType)
	case "slack":
		err = n.sendSlack(channel, event, notifyType)
	case "email":
		err = n.sendEmail(channel, event, notifyType)
	default:
		err = fmt.Errorf("unsupported channel type: %s", channel.Type)
	}

	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	} else {
		result.Status = "sent"
	}
	return result
}

// sendFeishu 发送飞书 Webhook 通知（支持 HMAC-SHA256 签名）
func (n *AlarmNotifier) sendFeishu(
	channel *model.NotificationChannel,
	event *model.AlarmEvent,
	notifyType string,
) error {
	var config struct {
		WebhookURL string `json:"webhook_url"`
		Secret     string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(channel.Config), &config); err != nil {
		return fmt.Errorf("parse feishu config: %w", err)
	}
	// channel.Config 为空时，从系统设置镜像中读取全局配置作为 fallback
	if config.WebhookURL == "" {
		config.WebhookURL = GetMirrorSetting("FEISHU_WEBHOOK_URL")
	}
	if config.Secret == "" {
		config.Secret = GetMirrorSetting("FEISHU_SECRET")
	}
	if config.WebhookURL == "" {
		return fmt.Errorf("feishu webhook_url is empty (not configured in channel or system settings)")
	}

	// 构建飞书消息体
	title := n.buildTitle(event, notifyType)
	content := n.buildContent(event, notifyType)

	payload := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"header": map[string]interface{}{
				"title": map[string]interface{}{
					"tag":     "plain_text",
					"content": title,
				},
				"template": n.getFeishuHeaderColor(event, notifyType),
			},
			"elements": []interface{}{
				map[string]interface{}{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}

	// 如果配置了签名密钥，添加签名
	if config.Secret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		sign, err := n.genFeishuSign(config.Secret, timestamp)
		if err != nil {
			return fmt.Errorf("generate feishu sign: %w", err)
		}
		payload["timestamp"] = timestamp
		payload["sign"] = sign
	}

	return n.doHTTPPost(config.WebhookURL, payload)
}

// sendWebhook 发送自定义 Webhook 通知
func (n *AlarmNotifier) sendWebhook(
	channel *model.NotificationChannel,
	event *model.AlarmEvent,
	notifyType string,
) error {
	var config struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`  // HTTP 方法，默认 POST
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(channel.Config), &config); err != nil {
		return fmt.Errorf("parse webhook config: %w", err)
	}
	if config.URL == "" {
		// fallback 到系统设置镜像中的全局 Webhook 配置
		config.URL = GetMirrorSetting("CUSTOM_WEBHOOK_URL")
	}
	if config.URL == "" {
		return fmt.Errorf("webhook url is empty (not configured in channel or system settings)")
	}
	if config.Method == "" {
		// 优先使用 channel 配置中的 Method，其次从镜像读取，最后默认 POST
		config.Method = GetMirrorSetting("CUSTOM_WEBHOOK_METHOD")
		if config.Method == "" {
			config.Method = "POST"
		}
	}

	payload := map[string]interface{}{
		"event_uuid":      event.UUID,
		"alert_name":      event.AlertName,
		"vm_uuid":         event.VMUUID,
		"vm_name":         event.VMName,
		"severity":        event.Severity,
		"status":          event.Status,
		"summary":         event.Summary,
		"notify_type":     notifyType,
		"fired_at":        event.FiredAt.Format(time.RFC3339),
		"last_fired_at":   event.LastFiredAt.Format(time.RFC3339),
		"rule_group_uuid": event.RuleGroupUUID,
		"labels":          event.Labels,
	}
	if event.ResolvedAt != nil {
		payload["resolved_at"] = event.ResolvedAt.Format(time.RFC3339)
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequest(config.Method, config.URL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}
	return nil
}

// genFeishuSign 生成飞书签名
// 飞书签名算法：以 "timestamp\nsecret" 为 HMAC-SHA256 密钥，签名空串，再 base64 编码
func (n *AlarmNotifier) genFeishuSign(secret string, timestamp string) (string, error) {
	stringToSign := timestamp + "\n" + secret
	h := hmac.New(sha256.New, []byte(stringToSign))
	// 飞书要求签名空串（key 即为全部签名素材），此处 Write(nil) 是正确行为
	_, err := h.Write(nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

func (n *AlarmNotifier) buildTitle(event *model.AlarmEvent, notifyType string) string {
	switch notifyType {
	case "firing_trigger":
		return fmt.Sprintf("[告警触发] %s - %s", event.AlertName, event.VMName)
	case "repeat_remind":
		return fmt.Sprintf("[告警持续] %s - %s", event.AlertName, event.VMName)
	case "resolved":
		return fmt.Sprintf("[告警恢复] %s - %s", event.AlertName, event.VMName)
	default:
		return fmt.Sprintf("[告警] %s - %s", event.AlertName, event.VMName)
	}
}

func (n *AlarmNotifier) buildContent(event *model.AlarmEvent, notifyType string) string {
	statusEmoji := "🔴"
	if notifyType == "resolved" {
		statusEmoji = "✅"
	}
	return fmt.Sprintf(
		"%s **%s**\n"+
			"**VM**: %s (%s)\n"+
			"**级别**: %s\n"+
			"**摘要**: %s\n"+
			"**触发时间**: %s\n"+
			"**最后触发**: %s",
		statusEmoji, event.Status,
		event.VMName, event.VMUUID,
		event.Severity,
		event.Summary,
		event.FiredAt.Format("2006-01-02 15:04:05"),
		event.LastFiredAt.Format("2006-01-02 15:04:05"),
	)
}

func (n *AlarmNotifier) getFeishuHeaderColor(event *model.AlarmEvent, notifyType string) string {
	if notifyType == "resolved" {
		return "green"
	}
	switch event.Severity {
	case "critical":
		return "red"
	case "warning":
		return "orange"
	default:
		return "yellow"
	}
}

// sendSlack 发送 Slack Incoming Webhook 通知
func (n *AlarmNotifier) sendSlack(
	channel *model.NotificationChannel,
	event *model.AlarmEvent,
	notifyType string,
) error {
	var config struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal([]byte(channel.Config), &config); err != nil {
		return fmt.Errorf("parse slack config: %w", err)
	}
	// channel.Config 为空时，从系统设置镜像中读取全局 Slack Webhook URL
	if config.WebhookURL == "" {
		config.WebhookURL = GetMirrorSetting("SLACK_WEBHOOK_URL")
	}
	if config.WebhookURL == "" {
		return fmt.Errorf("slack webhook_url is empty (not configured in channel or system settings)")
	}

	title := n.buildTitle(event, notifyType)
	content := n.buildContent(event, notifyType)

	// Slack Incoming Webhook payload：text 作为纯文本 fallback，blocks 提供富文本
	payload := map[string]interface{}{
		"text": title,
		"blocks": []interface{}{
			map[string]interface{}{
				"type": "header",
				"text": map[string]interface{}{
					"type": "plain_text",
					"text": title,
				},
			},
			map[string]interface{}{
				"type": "section",
				"text": map[string]interface{}{
					"type": "mrkdwn",
					"text": content,
				},
			},
		},
	}

	return n.doHTTPPost(config.WebhookURL, payload)
}

// sendEmail 发送告警邮件通知
func (n *AlarmNotifier) sendEmail(
	channel *model.NotificationChannel,
	event *model.AlarmEvent,
	notifyType string,
) error {
	var config struct {
		To []string `json:"to"` // 收件人列表
	}
	if err := json.Unmarshal([]byte(channel.Config), &config); err != nil {
		return fmt.Errorf("parse email config: %w", err)
	}
	if len(config.To) == 0 {
		return fmt.Errorf("email channel has no recipients configured")
	}

	// 从系统设置镜像读取 SMTP 配置
	host := GetMirrorSetting("SMTP_HOST")
	if host == "" {
		return fmt.Errorf("SMTP_HOST not configured in system settings")
	}
	port := GetMirrorSetting("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	user := GetMirrorSetting("SMTP_USER")
	password := GetMirrorSetting("SMTP_PASSWORD")
	from := GetMirrorSetting("SMTP_FROM")
	fromName := GetMirrorSetting("SMTP_FROM_NAME")
	if fromName == "" {
		fromName = "CloudLand"
	}

	title := n.buildTitle(event, notifyType)
	content := n.buildContent(event, notifyType)
	// 将 markdown 粗体转为纯文本
	plainContent := strings.ReplaceAll(content, "**", "")

	addr := host + ":" + port
	toHeader := strings.Join(config.To, ", ")

	msg := fmt.Sprintf("From: %s <%s>\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		fromName, from, toHeader, title, plainContent)

	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, password, host)
	}

	portNum, _ := strconv.Atoi(port)
	if portNum == 465 {
		// 隐式 SSL（SMTPS）
		tlsConfig := &tls.Config{ServerName: host}
		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("tls dial: %w", err)
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp new client: %w", err)
		}
		defer client.Close()
		if auth != nil {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
		if err := client.Mail(from); err != nil {
			return fmt.Errorf("smtp mail: %w", err)
		}
		for _, to := range config.To {
			if err := client.Rcpt(to); err != nil {
				return fmt.Errorf("smtp rcpt %s: %w", to, err)
			}
		}
		w, err := client.Data()
		if err != nil {
			return fmt.Errorf("smtp data: %w", err)
		}
		if _, err := w.Write([]byte(msg)); err != nil {
			return fmt.Errorf("smtp write: %w", err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("smtp close data: %w", err)
		}
		return client.Quit()
	}

	// 端口 587 或其他：使用 STARTTLS 或明文
	return smtp.SendMail(addr, auth, from, config.To, []byte(msg))
}

func (n *AlarmNotifier) doHTTPPost(url string, payload interface{}) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	resp, err := n.client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}
	return nil
}

