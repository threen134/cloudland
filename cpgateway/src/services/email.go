package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/tracing"
)

type emailConfig struct {
	SMTP        common.SMTPConfig
	From        string
	FromName    string
	FrontendURL string
}

// LoadEmailConfig reads SMTP settings with DB → config → default fallback.
func LoadEmailConfig(db *gorm.DB) emailConfig {
	return emailConfig{
		SMTP: common.SMTPConfig{
			Host:     GetSettingString(db, "SMTP_HOST", ""),
			Port:     GetSettingInt(db, "SMTP_PORT", 587),
			StartTLS: Truthy(GetSetting(db, "SMTP_TLS")),
			User:     GetSettingString(db, "SMTP_USER", ""),
			Password: GetSettingString(db, "SMTP_PASSWORD", ""),
		},
		From:        GetSettingString(db, "SMTP_FROM", ""),
		FromName:    GetSettingString(db, "SMTP_FROM_NAME", "CloudLand"),
		FrontendURL: GetSettingString(db, "FRONTEND_URL", ""),
	}
}

type notifyConfig struct {
	Channels         []interface{}
	FeishuWebhookURL string
	FeishuSecret     string
	FrontendURL      string
}

func loadNotifyConfig(db *gorm.DB) notifyConfig {
	channels, ok := GetSetting(db, "NOTIFICATION_CHANNELS").([]interface{})
	if !ok {
		channels = []interface{}{"email"}
	}
	return notifyConfig{
		Channels:         channels,
		FeishuWebhookURL: GetSettingString(db, "FEISHU_WEBHOOK_URL", ""),
		FeishuSecret:     GetSettingString(db, "FEISHU_SECRET", ""),
		FrontendURL:      GetSettingString(db, "FRONTEND_URL", ""),
	}
}

func (c notifyConfig) has(channel string) bool {
	for _, v := range c.Channels {
		if s, ok := v.(string); ok && s == channel {
			return true
		}
	}
	return false
}

// FeishuSign generates the Feishu webhook HMAC-SHA256 signature.
func FeishuSign(secret string, timestamp int64) string {
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

const emailCSS = `
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; line-height: 1.6; color: #333; max-width: 600px; margin: 0 auto; padding: 20px; }
        .container { background: #ffffff; border-radius: 8px; padding: 40px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .header { text-align: center; margin-bottom: 30px; }
        .header h1 { color: #2563eb; margin: 0; font-size: 28px; }
        .content { margin: 30px 0; }
        .button { display: inline-block; padding: 14px 32px; background: #2563eb; color: #ffffff !important; text-decoration: none; border-radius: 6px; font-weight: 600; margin: 20px 0; }
        .button:hover { background: #1d4ed8; }
        .footer { margin-top: 40px; padding-top: 20px; border-top: 1px solid #e5e7eb; font-size: 14px; color: #6b7280; text-align: center; }
        .link { color: #2563eb; word-break: break-all; }`

type emailBody struct {
	Title, Hello, Main, Link, Button, LinkText string
	Extra                                      []string
	FooterIgnore, FooterRights                 string
}

func renderEmailHTML(b emailBody) string {
	var extra bytes.Buffer
	for _, p := range b.Extra {
		fmt.Fprintf(&extra, "\n            <p>%s</p>", p)
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>%s
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>%s</h1>
        </div>
        <div class="content">
            <p>%s</p>
            <p>%s</p>
            <div style="text-align: center;">
                <a href="%s" class="button">%s</a>
            </div>
            <p>%s</p>
            <p><a href="%s" class="link">%s</a></p>%s
        </div>
        <div class="footer">
            <p>%s</p>
            <p>%s</p>
        </div>
    </div>
</body>
</html>`, emailCSS, b.Title, b.Hello, b.Main, b.Link, b.Button, b.LinkText, b.Link, b.Link, extra.String(), b.FooterIgnore, b.FooterRights)
}

func sendActivationEmail(ctx context.Context, db *gorm.DB, email, username, token, language string) bool {
	cfg := LoadEmailConfig(db)
	link := fmt.Sprintf("%s/activate?token=%s", cfg.FrontendURL, token)
	if cfg.SMTP.Host == "" {
		log.WithContext(ctx).Warnf("SMTP_HOST not configured, skipping email to %s", email)
		log.WithContext(ctx).Infof("Activation link for frontend would be: %s", link)
		return true
	}

	name := html.EscapeString(username)
	var subject, text string
	var body emailBody
	if language == "zh" {
		subject = "欢迎来到 Cloudland - 请激活您的账户"
		body = emailBody{
			Title: "🎉 欢迎来到 Cloudland!", Hello: fmt.Sprintf("您好 <strong>%s</strong>,", name),
			Main:   "欢迎加入 Cloudland！我们很高兴您的加入。请点击下方按钮验证您的邮箱并激活账户：",
			Button: "激活账户", LinkText: "或者将以下链接复制到浏览器中访问：",
			Extra:        []string{fmt.Sprintf("验证令牌: <code>%s</code>", token), "<strong>注意:</strong> 此链接在 24 小时内有效。"},
			FooterIgnore: "如果您没有注册过此账户，请忽略此邮件。", FooterRights: "© 2026 Cloudland Platform. 保留所有权利。",
		}
		text = fmt.Sprintf("欢迎来到 Cloudland!\n\n您好 %s,\n\n欢迎加入 Cloudland！请访问以下链接激活您的账户：\n\n%s\n\n验证令牌: %s\n\n注意: 此链接在 24 小时内有效。\n\n如果您没有注册过此账户，请忽略此邮件。\n\n© 2026 Cloudland Platform. 保留所有权利。\n", username, link, token)
	} else {
		subject = "Welcome to Cloudland - Please Activate Your Account"
		body = emailBody{
			Title: "🎉 Welcome to Cloudland!", Hello: fmt.Sprintf("Hello <strong>%s</strong>,", name),
			Main:   "Welcome to Cloudland! We're excited to have you on board. Please click the button below to verify your email and activate your account:",
			Button: "Activate Account", LinkText: "Or copy and paste this link into your browser:",
			Extra:        []string{fmt.Sprintf("Verification Token: <code>%s</code>", token), "<strong>Note:</strong> This link will expire in 24 hours."},
			FooterIgnore: "If you did not sign up for this account, please ignore this email.", FooterRights: "© 2026 Cloudland Platform. All rights reserved.",
		}
		text = fmt.Sprintf("Welcome to Cloudland!\n\nHello %s,\n\nWelcome to Cloudland! Please visit the link below to activate your account:\n\n%s\n\nVerification Token: %s\n\nNote: This link will expire in 24 hours.\n\nIf you did not sign up for this account, please ignore this email.\n\n© 2026 Cloudland Platform. All rights reserved.\n", username, link, token)
	}
	body.Link = link

	msg := common.BuildAlternativeMessage(subject, common.FormatAddress(cfg.FromName, cfg.From), email, text, renderEmailHTML(body))
	// SMTP 为外部服务：只建 span
	_, mailSpan := tracing.StartChild(ctx, "notify.email", trace.WithSpanKind(trace.SpanKindClient))
	mailErr := common.SendMail(cfg.SMTP, cfg.From, email, msg)
	tracing.EndSpan(mailSpan, mailErr)
	if err := mailErr; err != nil {
		log.WithContext(ctx).Errorf("Failed to send email: %v", err)
		return false
	}
	log.WithContext(ctx).Infof("Activation email sent successfully to %s", email)
	return true
}

func sendInvitationEmail(ctx context.Context, db *gorm.DB, email, orgName, inviterName, token string, isExisting bool, language string) bool {
	cfg := LoadEmailConfig(db)
	link := fmt.Sprintf("%s/invite/accept?token=%s", cfg.FrontendURL, token)
	if cfg.SMTP.Host == "" {
		log.WithContext(ctx).Warnf("SMTP_HOST not configured, skipping invitation email to %s", email)
		log.WithContext(ctx).Infof("Invitation link: %s", link)
		return true
	}

	org, inviter := html.EscapeString(orgName), html.EscapeString(inviterName)
	var subject, text string
	var body emailBody
	if language == "zh" {
		subject = fmt.Sprintf("CloudLand - 您被邀请加入组织 %s", orgName)
		action := "点击下方按钮创建账户并加入："
		if isExisting {
			action = "点击下方按钮接受邀请："
		}
		body = emailBody{
			Title: "您被邀请加入一个组织", Hello: "您好，",
			Main:   fmt.Sprintf("<strong>%s</strong> 邀请您加入组织 <strong>%s</strong>。%s", inviter, org, action),
			Button: "接受邀请", LinkText: "或者将以下链接复制到浏览器中访问：",
			Extra:        []string{"<strong>注意:</strong> 此邀请链接在 24 小时内有效。"},
			FooterIgnore: "如果您不认识邀请人，请忽略此邮件。", FooterRights: "© 2026 Cloudland Platform. 保留所有权利。",
		}
		text = fmt.Sprintf("您被邀请加入组织 %s。请访问: %s", orgName, link)
	} else {
		subject = fmt.Sprintf("CloudLand - You're invited to join %s", orgName)
		action := "Click the button below to create your account and join:"
		if isExisting {
			action = "Click the button below to accept:"
		}
		body = emailBody{
			Title: "You're Invited!", Hello: "Hello,",
			Main:   fmt.Sprintf("<strong>%s</strong> has invited you to join the organization <strong>%s</strong>. %s", inviter, org, action),
			Button: "Accept Invitation", LinkText: "Or copy and paste this link into your browser:",
			Extra:        []string{"<strong>Note:</strong> This invitation expires in 24 hours."},
			FooterIgnore: "If you don't recognize the sender, please ignore this email.", FooterRights: "© 2026 Cloudland Platform. All rights reserved.",
		}
		text = fmt.Sprintf("You've been invited to join %s. Visit: %s", orgName, link)
	}
	body.Link = link

	msg := common.BuildAlternativeMessage(subject, common.FormatAddress(cfg.FromName, cfg.From), email, text, renderEmailHTML(body))
	// SMTP 为外部服务：只建 span
	_, mailSpan := tracing.StartChild(ctx, "notify.email", trace.WithSpanKind(trace.SpanKindClient))
	mailErr := common.SendMail(cfg.SMTP, cfg.From, email, msg)
	tracing.EndSpan(mailSpan, mailErr)
	if err := mailErr; err != nil {
		log.WithContext(ctx).Errorf("Failed to send email: %v", err)
		return false
	}
	log.WithContext(ctx).Infof("Invitation email sent to %s for org %s", email, orgName)
	return true
}

func postFeishu(ctx context.Context, cfg notifyConfig, title, langKey string, lines [][]map[string]string) bool {
	// 飞书为外部服务：只建 span，不注入 trace 头
	ctx, span := tracing.StartChild(ctx, "notify.feishu", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	ok := sendFeishuPost(ctx, cfg, title, langKey, lines)
	if !ok {
		span.SetStatus(codes.Error, "feishu notification failed")
	}
	return ok
}

func sendFeishuPost(ctx context.Context, cfg notifyConfig, title, langKey string, lines [][]map[string]string) bool {
	payload := map[string]interface{}{
		"msg_type": "post",
		"content": map[string]interface{}{
			"post": map[string]interface{}{
				langKey: map[string]interface{}{"title": title, "content": lines},
			},
		},
	}
	if cfg.FeishuSecret != "" {
		ts := time.Now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = FeishuSign(cfg.FeishuSecret, ts)
	}
	body, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.FeishuWebhookURL, bytes.NewReader(body))
	if err != nil {
		log.WithContext(ctx).Errorf("Failed to send feishu notification: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.WithContext(ctx).Errorf("Failed to send feishu notification: %v", err)
		return false
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.WithContext(ctx).Errorf("Failed to send feishu notification: %v", err)
		return false
	}
	if code, ok := ToFloat(result["code"]); ok && result["code"] != nil && code == 0 {
		return true
	}
	if code, ok := ToFloat(result["StatusCode"]); ok && result["StatusCode"] != nil && code == 0 {
		return true
	}
	log.WithContext(ctx).Errorf("Feishu API error: %v", result)
	return false
}

func textLine(s string) []map[string]string { return []map[string]string{{"tag": "text", "text": s}} }
func linkLine(text, href string) []map[string]string {
	return []map[string]string{{"tag": "a", "text": text, "href": href}}
}

func sendFeishuActivation(ctx context.Context, cfg notifyConfig, email, username, token, language string) bool {
	if cfg.FeishuWebhookURL == "" {
		log.WithContext(ctx).Warn("FEISHU_WEBHOOK_URL not configured, skipping feishu notification")
		return false
	}
	link := fmt.Sprintf("%s/activate?token=%s", cfg.FrontendURL, token)
	if language == "zh" {
		return postFeishu(ctx, cfg, "CloudLand - 新用户注册激活", "zh_cn", [][]map[string]string{
			textLine(fmt.Sprintf("用户 %s (%s) 已注册，请激活账户：", username, email)),
			linkLine("点击激活账户", link),
			textLine("激活令牌: " + token),
			textLine("此链接 24 小时内有效。"),
		})
	}
	return postFeishu(ctx, cfg, "CloudLand - New User Activation", "en_us", [][]map[string]string{
		textLine(fmt.Sprintf("User %s (%s) has registered. Please activate:", username, email)),
		linkLine("Click to Activate Account", link),
		textLine("Activation Token: " + token),
		textLine("This link expires in 24 hours."),
	})
}

func sendFeishuInvitation(ctx context.Context, cfg notifyConfig, email, orgName, inviterName, token, language string) bool {
	if cfg.FeishuWebhookURL == "" {
		log.WithContext(ctx).Warn("FEISHU_WEBHOOK_URL not configured, skipping feishu invitation notification")
		return false
	}
	link := fmt.Sprintf("%s/invite/accept?token=%s", cfg.FrontendURL, token)
	if language == "zh" {
		return postFeishu(ctx, cfg, "CloudLand - 组织邀请: "+orgName, "zh_cn", [][]map[string]string{
			textLine(fmt.Sprintf("%s 邀请 %s 加入组织 %s", inviterName, email, orgName)),
			linkLine("接受邀请", link),
			textLine("此链接 24 小时内有效。"),
		})
	}
	return postFeishu(ctx, cfg, "CloudLand - Org Invitation: "+orgName, "en_us", [][]map[string]string{
		textLine(fmt.Sprintf("%s invited %s to join %s", inviterName, email, orgName)),
		linkLine("Accept Invitation", link),
		textLine("This link expires in 24 hours."),
	})
}

// SendActivationNotification sends via the channels enabled in NOTIFICATION_CHANNELS.
func SendActivationNotification(ctx context.Context, email, username, token, language string) bool {
	db := dbs.DBContext(ctx)
	cfg := loadNotifyConfig(db)
	var results []bool
	if cfg.has("email") {
		results = append(results, sendActivationEmail(ctx, db, email, username, token, language))
	}
	if cfg.has("feishu") {
		results = append(results, sendFeishuActivation(ctx, cfg, email, username, token, language))
	}
	if len(results) == 0 {
		log.WithContext(ctx).Warn("No valid notification channels configured, falling back to email")
		results = append(results, sendActivationEmail(ctx, db, email, username, token, language))
	}
	return anyTrue(results)
}

func SendInvitationNotification(ctx context.Context, email, orgName, inviterName, token string, isExisting bool) bool {
	const language = "en"
	db := dbs.DBContext(ctx)
	cfg := loadNotifyConfig(db)
	var results []bool
	if cfg.has("email") {
		results = append(results, sendInvitationEmail(ctx, db, email, orgName, inviterName, token, isExisting, language))
	}
	if cfg.has("feishu") {
		results = append(results, sendFeishuInvitation(ctx, cfg, email, orgName, inviterName, token, language))
	}
	if len(results) == 0 {
		log.WithContext(ctx).Warn("No valid notification channels configured, falling back to email")
		results = append(results, sendInvitationEmail(ctx, db, email, orgName, inviterName, token, isExisting, language))
	}
	return anyTrue(results)
}

func anyTrue(values []bool) bool {
	for _, v := range values {
		if v {
			return true
		}
	}
	return false
}
