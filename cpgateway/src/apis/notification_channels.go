package apis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
	"cpgateway/src/services"
)

// nonPublicPrefixes are ranges a channel must not point to, besides what netip already classifies
// as private, loopback, link-local, multicast or unspecified.
// Keep in sync with nonPublicPrefixes in api/src/services/notifier_dial.go: clapi refuses the same
// ranges on the resolved address when it sends, so a channel saved here must not fail there.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, includes broadcast
	netip.MustParsePrefix("64:ff9b::/96"),   // well-known NAT64, maps to any IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64 (RFC 8215)
	netip.MustParsePrefix("2002::/16"),      // 6to4, embeds an IPv4 address
}

// isPublicAddr reports whether ip is a public unicast address a channel may point to.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// validateWebhookURL requires http(s) and rejects non-public IP literals (SSRF guard). Host names
// are checked by clapi on the resolved address when it sends.
func validateWebhookURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must use http or https scheme", field)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%s has no valid hostname", field)
	}
	if ip, err := netip.ParseAddr(host); err == nil && !isPublicAddr(ip) {
		return fmt.Errorf("%s must not point to a private/internal address", field)
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

// channelOrderColumns are the fields GET /notification-channels can be sorted by (the order parameter)
var channelOrderColumns = map[string]string{
	"name":       "name",
	"type":       "type",
	"enabled":    "enabled",
	"created_at": "created_at",
}

// channelListOrder is the ORDER BY of the channel list: the requested field when it is one of
// channelOrderColumns, otherwise the default, newest first
func channelListOrder(order string) string {
	if _, ok := channelOrderColumns[strings.TrimPrefix(order, "-")]; ok {
		return orderBy(order, channelOrderColumns, "id")
	}
	return "created_at DESC, id DESC"
}

// GET /notification-channels?offset=&limit=&query=&order=
func ListChannels(c *gin.Context) {
	p, ok := parseListParams(c)
	if !ok {
		return
	}
	org := currentOrg(c)
	var channels []model.NotificationChannel
	q := dbs.DBContext(c.Request.Context()).Model(&model.NotificationChannel{}).
		Where("org_id = ?", org.ID).
		Scopes(searchScope(p.Query, "name", "type"))
	// total 是过滤后的总数，原先返回的是当前页条数（当时也没有分页，所以看不出区别）
	total, ok := countAndPage(c, q.Order(channelListOrder(p.Order)), p, &channels)
	if !ok {
		return
	}
	out := make([]channelOut, 0, len(channels))
	for i := range channels {
		out = append(out, toChannelOut(&channels[i]))
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "channels": out})
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
		if *in.Name == "" {
			common.AbortValidation(c, "body", errors.New("name must not be empty"))
			return
		}
		updates["name"] = *in.Name
	}
	if in.Config != nil {
		// Same checks as on create: an update must not point the channel at an internal address
		if err := validateChannel(ch.Type, in.Config); err != nil {
			common.AbortValidation(c, "body", err)
			return
		}
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
