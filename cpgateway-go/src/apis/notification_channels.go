package apis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/services"
)

var reservedIPv4 = &net.IPNet{IP: net.IPv4(240, 0, 0, 0), Mask: net.CIDRMask(4, 32)}

// validateWebhookURL requires http(s) and rejects private/internal IP literals (SSRF guard).
func validateWebhookURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must use http or https scheme", field)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%s has no valid hostname", field)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || reservedIPv4.Contains(ip) {
			return fmt.Errorf("%s must not point to a private/internal address", field)
		}
	}
	return nil
}

func validateChannel(channelType string, config map[string]interface{}) error {
	var field string
	switch channelType {
	case "feishu":
		field = "webhook_url"
	case "webhook":
		field = "url"
	default:
		return errors.New("type must be 'feishu' or 'webhook'")
	}
	u, ok := config[field].(string)
	if !ok || u == "" {
		return fmt.Errorf("%s channel requires '%s' in config", channelType, field)
	}
	return validateWebhookURL(u, field)
}

func getOrgChannelOr404(c *gin.Context, orgID int64) (*model.NotificationChannel, bool) {
	var ch model.NotificationChannel
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ? AND org_id = ?", c.Param("channel_uuid"), orgID).First(&ch).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Notification channel not found")
		return nil, false
	}
	return &ch, true
}

// GET /notification-channels
func ListChannels(c *gin.Context) {
	org := currentOrg(c)
	var channels []model.NotificationChannel
	dbs.DBContext(c.Request.Context()).Where("org_id = ?", org.ID).Order("created_at DESC").Find(&channels)
	out := make([]channelOut, 0, len(channels))
	for i := range channels {
		out = append(out, toChannelOut(&channels[i]))
	}
	c.JSON(http.StatusOK, gin.H{"total": len(out), "channels": out})
}

// POST /notification-channels
func CreateChannel(c *gin.Context) {
	var in struct {
		Name    string                 `json:"name" binding:"required"`
		Type    string                 `json:"type" binding:"required"`
		Config  map[string]interface{} `json:"config" binding:"required"`
		Enabled *bool                  `json:"enabled"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if err := validateChannel(in.Type, in.Config); err != nil {
		common.AbortValidation(c, "body", err)
		return
	}
	cfg, _ := json.Marshal(in.Config)
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	org := currentOrg(c)
	ch := model.NotificationChannel{OrgID: org.ID, Name: in.Name, Type: in.Type, Config: string(cfg), Enabled: enabled}
	if err := dbs.DBContext(c.Request.Context()).Create(&ch).Error; err != nil {
		internalServerError(c, err)
		return
	}
	log.WithContext(c).Infof("Org %d created notification channel '%s' (%s)", org.ID, ch.Name, ch.UUID)
	go services.PushChannelUpsertToAllRegions(context.WithoutCancel(c.Request.Context()), ch.UUID)
	c.JSON(http.StatusCreated, toChannelOut(&ch))
}

// GET /notification-channels/:channel_uuid
func GetChannel(c *gin.Context) {
	ch, ok := getOrgChannelOr404(c, currentOrg(c).ID)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toChannelOut(ch))
}

// PUT /notification-channels/:channel_uuid — partial update of name/config/enabled.
func UpdateChannel(c *gin.Context) {
	var in struct {
		Name    *string                `json:"name"`
		Config  map[string]interface{} `json:"config"`
		Enabled *bool                  `json:"enabled"`
	}
	if !bindJSON(c, &in) {
		return
	}
	org := currentOrg(c)
	ch, ok := getOrgChannelOr404(c, org.ID)
	if !ok {
		return
	}
	updates := map[string]interface{}{}
	if in.Name != nil {
		updates["name"] = *in.Name
	}
	if in.Config != nil {
		cfg, _ := json.Marshal(in.Config)
		updates["config"] = string(cfg)
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	db := dbs.DBContext(c.Request.Context())
	if len(updates) > 0 {
		if err := db.Model(ch).Updates(updates).Error; err != nil {
			internalServerError(c, err)
			return
		}
	}
	db.Where("id = ?", ch.ID).First(ch)
	log.WithContext(c).Infof("Org %d updated notification channel '%s' (%s)", org.ID, ch.Name, ch.UUID)
	go services.PushChannelUpsertToAllRegions(context.WithoutCancel(c.Request.Context()), ch.UUID)
	c.JSON(http.StatusOK, toChannelOut(ch))
}

// DELETE /notification-channels/:channel_uuid
func DeleteChannel(c *gin.Context) {
	org := currentOrg(c)
	ch, ok := getOrgChannelOr404(c, org.ID)
	if !ok {
		return
	}
	if err := dbs.DBContext(c.Request.Context()).Delete(ch).Error; err != nil {
		internalServerError(c, err)
		return
	}
	log.WithContext(c).Infof("Org %d deleted notification channel (%s)", org.ID, ch.UUID)
	go services.PushChannelDeleteToAllRegions(context.WithoutCancel(c.Request.Context()), ch.UUID)
	c.Status(http.StatusNoContent)
}
