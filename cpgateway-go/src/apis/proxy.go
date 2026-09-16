package apis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/services"
	"cpgateway-go/src/tracing"
)

var hopByHopHeaders = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true,
	"proxy-authorization": true, "te": true, "trailers": true,
	"transfer-encoding": true, "upgrade": true, "host": true,
	"content-length": true, "authorization": true,
}

var templateParam = regexp.MustCompile(`\{(\w+)\}`)

func resolveTemplate(c *gin.Context, template string) string {
	return templateParam.ReplaceAllStringFunc(template, func(m string) string {
		return url.PathEscape(c.Param(m[1 : len(m)-1]))
	})
}

// proxyHandler builds the handler for one whitelisted backend route.
func proxyHandler(route proxyRoute) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			internalServerError(c, err)
			return
		}
		if route.Method == http.MethodPost && route.Template == "/instances" && len(body) > 0 {
			var payload map[string]interface{}
			if json.Unmarshal(body, &payload) == nil && payload["hypervisor"] != nil && !currentUser(c).IsAdmin() {
				common.AbortWithDetail(c, http.StatusForbidden, "Only system admin can pin an instance to a specific hypervisor")
				return
			}
		}
		forwardToRegion(c, route.Template, body)
	}
}

// forwardToRegion resolves the token's region, enforces org status and quota, forwards the
// request with X-* identity headers and settles the quota based on the backend response.
func forwardToRegion(c *gin.Context, template string, body []byte) {
	claims := currentClaims(c)
	user := currentUser(c)
	method := c.Request.Method
	db := dbs.DBContext(c.Request.Context())

	var region model.Region
	if err := db.Where("uuid = ?", claims.Region).First(&region).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, fmt.Sprintf("Region '%s' not found", claims.Region))
		return
	}
	if !region.IsAvailable {
		common.AbortWithDetail(c, http.StatusServiceUnavailable, fmt.Sprintf("Region '%s' is currently unavailable", region.Name))
		return
	}

	var org *model.Organization
	var orgID int64
	var orgUUID string
	if claims.OrgID != "" {
		// Orgs are soft-deleted: a token issued before deletion finds nothing and is rejected
		// instead of being forwarded without org identity and quota tracking.
		var o model.Organization
		if db.Where("uuid = ?", claims.OrgID).Limit(1).Find(&o).RowsAffected == 0 {
			common.AbortWithDetail(c, http.StatusForbidden, "Organization is not accessible")
			return
		}
		org, orgID, orgUUID = &o, o.ID, o.UUID
	}

	forwarded := map[string]string{
		"X-User-ID": strconv.FormatInt(user.ID, 10),
		// 只传用户名，不传邮箱：用户名创建后不可更改、注销后不可复用，是跨服务引用账号最稳的标识；
		// 邮箱可改、注销后还能被本人复用，作为标识不可靠，clapi 侧也没有任何地方需要它
		"X-User-Name": user.Username,
		// 用户 UUID：与组织一致，跨服务标识统一用全局唯一 ID。X-User-ID 是本服务的自增主键，
		// 到了区域侧无法解析，仅作为各资源 creater 列的历史字段保留
		"X-User-UUID": user.UUID,
		// 传 UUID 而非自增主键：两侧组织表的 ID 各自独立，此前靠同步时强行用同一个 ID
		// 作主键来维持一致，一旦错位，资源会静默挂到别的组织名下且毫无报错
		"X-Org-UUID":         orgUUID,
		"X-Org-Name":         claims.OrgName,
		"X-Org-Role":         strconv.Itoa(claims.OR),
		"X-Is-Owner":         strconv.FormatBool(claims.IsOwner),
		"X-System-Role":      strconv.Itoa(claims.SR),
		"X-Forwarded-Secret": region.InternalSecret,
	}

	if org != nil {
		if org.Status == model.OrgPending || org.Status == model.OrgDisabled {
			common.AbortWithDetail(c, http.StatusForbidden, "Organization is not accessible")
			return
		}
		if org.Status == model.OrgSuspended && method != http.MethodGet {
			common.AbortWithDetail(c, http.StatusForbidden, "Organization is suspended")
			return
		}
	}

	resolvedPath := resolveTemplate(c, template)
	var plan *services.QuotaPlan
	if orgID != 0 {
		var herr *common.HTTPError
		if plan, herr = services.PrepareQuota(c.Request.Context(), orgID, &region, method, template, resolvedPath, body, forwarded); herr != nil {
			common.AbortWithError(c, herr)
			return
		}
	}

	backendURL := services.BuildBackendURL(region.InternalEndpoint, strings.TrimLeft(resolvedPath, "/"))
	query := c.Request.URL.Query()
	query.Del("region")
	if encoded := query.Encode(); encoded != "" {
		backendURL += "?" + encoded
	}
	log.WithContext(c).Infof("Proxy: %s -> %s (user=%s, org=%s)", method, backendURL, claims.Subject, claims.OrgID)

	var reqBody io.Reader
	switch method {
	case http.MethodGet, http.MethodDelete, http.MethodHead, http.MethodOptions:
	default:
		reqBody = bytes.NewReader(body)
	}

	backendFailed := func(err error) {
		services.FinishQuota(orgID, region.ID, plan, 0, nil)
		log.WithContext(c).Errorf("Backend request failed: %s: %v", backendURL, err)
		common.AbortWithDetail(c, http.StatusBadGateway, fmt.Sprintf("Backend request failed: %v", err))
	}

	// Once forwarded, clapi may carry out the operation even if the client goes away, so the backend
	// call must not be cancelled by a client disconnect (which would also release the reserved quota).
	// WithoutCancel keeps the trace; ProxyTimeout still bounds the call.
	backendCtx := context.WithoutCancel(c.Request.Context())
	req, err := http.NewRequestWithContext(backendCtx, method, backendURL, reqBody)
	if err != nil {
		backendFailed(err)
		return
	}
	for k, values := range c.Request.Header {
		if hopByHopHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range values {
			req.Header.Add(k, v)
		}
	}
	for k, v := range forwarded {
		req.Header.Set(k, v)
	}

	resp, err := services.InsecureClient(services.ProxyTimeout()).Do(req)
	if err != nil {
		backendFailed(err)
		return
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		log.WithContext(c).Warnf("Backend %d: %s", resp.StatusCode, backendURL)
	}
	// The status line has arrived, so the backend's verdict is known even if the body read failed;
	// settle on it before (and independently of) writing back to the client.
	if readErr != nil {
		respBody = nil
	}
	services.FinishQuota(orgID, region.ID, plan, resp.StatusCode, respBody)
	if readErr != nil {
		log.WithContext(c).Errorf("Backend response read failed: %s: %v", backendURL, readErr)
		common.AbortWithDetail(c, http.StatusBadGateway, fmt.Sprintf("Backend request failed: %v", readErr))
		return
	}

	for k, values := range resp.Header {
		// 网关已写入同一 trace 的 X-Trace-ID，跳过后端的同名响应头避免重复
		if hopByHopHeaders[strings.ToLower(k)] || k == http.CanonicalHeaderKey(tracing.TraceIDHeader) {
			continue
		}
		for _, v := range values {
			c.Writer.Header().Add(k, v)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)
	c.Writer.Write(respBody)
}
