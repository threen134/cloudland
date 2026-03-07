/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"context"
	"encoding/json"
	"time"

	"web/src/common"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestID is a middleware that injects a request ID into every request.
// It checks the incoming header "X-Request-ID" first; if absent, a new UUID is generated.
// The request ID is stored in the gin context and echoed back in the response header.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader(common.RequestIDKey)
		if requestID == "" {
			requestID = uuid.New().String()
		}

		// 1. 保存到 Gin Context 中供 Logger 读取
		c.Set(common.RequestIDKey, requestID)

		// 2. 最关键：保存到原生的 HTTP Request Context，传给底层的业务代码
		ctx := context.WithValue(c.Request.Context(), common.RequestIDKey, requestID)
		c.Request = c.Request.WithContext(ctx)

		c.Header(common.RequestIDKey, requestID)
		c.Next()
	}
}

// Logger logs each request along with its request ID for end-to-end tracing.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Start timer
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// Process request
		c.Next()

		// Stop timer
		latency := time.Since(start)
		statusCode := c.Writer.Status()
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()

		clientIP := c.ClientIP()
		method := c.Request.Method
		if raw != "" {
			path = path + "?" + raw
		}

		requestIDValue, exists := c.Get(common.RequestIDKey)
		requestID := "-"
		if exists {
			if str, ok := requestIDValue.(string); ok {
				requestID = str
			}
		}

		// Output structured JSON for easier parsing in Loki/Grafana
		logData := map[string]interface{}{
			"tag":        "API_REQUEST",
			"method":     method,
			"path":       path,
			"status":     statusCode,
			"latency_ms": float64(latency.Nanoseconds()) / 1e6,
			"ip":         clientIP,
			"request_id": requestID,
		}
		if errorMessage != "" {
			logData["errors"] = errorMessage
		}

		jsonData, _ := json.Marshal(logData)
		logger.Info(string(jsonData))
	}
}
