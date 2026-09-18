package apis

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	"cpgateway/src/common"
)

// maxTelemetryBody 限制浏览器单次上报的 span 数据大小
const maxTelemetryBody = 512 << 10

var telemetryClient = &http.Client{Timeout: 5 * time.Second}

// POST /telemetry/traces — 将已登录用户浏览器上报的 OTLP/JSON span 转发到链路追踪后端。
// 不直接暴露 Tempo 的 OTLP 端口，避免任何人都能写入链路数据；后端不可用时静默丢弃，不影响前端
func IngestBrowserTraces(c *gin.Context) {
	endpoint := strings.TrimRight(viper.GetString("tracing.browser_otlp_endpoint"), "/")
	if endpoint == "" {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	if c.ContentType() != "application/json" {
		common.AbortWithDetail(c, http.StatusUnsupportedMediaType, "Telemetry payload must be application/json")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxTelemetryBody+1))
	if err != nil {
		common.AbortWithDetail(c, http.StatusBadRequest, "Invalid telemetry payload")
		return
	}
	if len(body) > maxTelemetryBody {
		common.AbortWithDetail(c, http.StatusRequestEntityTooLarge, "Telemetry payload too large")
		return
	}

	req, err := http.NewRequest(http.MethodPost, endpoint+"/v1/traces", bytes.NewReader(body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		var resp *http.Response
		if resp, err = telemetryClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	if err != nil {
		log.WithContext(c).Warnf("Forward browser traces failed: %v", err)
	}
	c.AbortWithStatus(http.StatusAccepted)
}
