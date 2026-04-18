/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"api/src/services"

	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

type RuntimeConfigAPI struct{}

var runtimeConfigAPI = &RuntimeConfigAPI{}

const maskedValue = "******"

// GetRuntimeConfig 返回当前 clapi 进程加载的基础设施配置（只读 + secret 脱敏）。
// 用途：系统设置页 Infrastructure tab 展示当前生效值，不支持修改。
//
// Route: GET /api/v1/internal/runtime-config
func (r *RuntimeConfigAPI) GetRuntimeConfig(c *gin.Context) {
	endpoint := viper.GetString("s3.endpoint")
	minioHost := viper.GetString("minio.hostname")
	secretKey := viper.GetString("s3.secret_key")
	sciSecret := viper.GetString("sci.shared_secret")

	resp := gin.H{
		"mode":                      detectMode(endpoint, minioHost),
		"s3_enabled":                services.S3Enabled(),
		"s3_endpoint":               endpoint,
		"s3_access_key":             viper.GetString("s3.access_key"),
		"s3_secret_key":             maskIfSet(secretKey),
		"s3_secret_key_set":         secretKey != "",
		"s3_bucket":                 viper.GetString("s3.bucket"),
		"s3_region":                 viper.GetString("s3.region"),
		"s3_use_ssl":                viper.GetBool("s3.use_ssl"),
		"s3_upload_timeout_minutes": int(services.S3UploadTimeout().Minutes()),
		"minio_hostname":            minioHost,
		"clapi_hostname":            viper.GetString("clapi.hostname"),
		"clapi_internal_url":        viper.GetString("clapi.internal_url"),
		"sci_shared_secret":         maskIfSet(sciSecret),
		"sci_shared_secret_set":     sciSecret != "",
	}
	c.JSON(http.StatusOK, resp)
}

// TestS3 对当前运行时配置做一次 BucketExists 探测，验证 endpoint/凭据/bucket 可用性。
// 不修改任何运行状态。
//
// Route: POST /api/v1/internal/runtime-config/test-s3
func (r *RuntimeConfigAPI) TestS3(c *gin.Context) {
	endpoint := viper.GetString("s3.endpoint")
	if endpoint == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "S3_ENDPOINT not set (legacy mode)",
		})
		return
	}
	bucket := viper.GetString("s3.bucket")
	if bucket == "" {
		bucket = "images"
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(viper.GetString("s3.access_key"), viper.GetString("s3.secret_key"), ""),
		Secure: viper.GetBool("s3.use_ssl"),
		Region: viper.GetString("s3.region"),
	})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "init client failed: " + err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	start := time.Now()
	exists, err := client.BucketExists(ctx, bucket)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"message":    "BucketExists failed: " + err.Error(),
			"latency_ms": latency,
		})
		return
	}
	if !exists {
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"message":    "bucket '" + bucket + "' does not exist on " + endpoint,
			"latency_ms": latency,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"message":    "bucket '" + bucket + "' reachable",
		"latency_ms": latency,
	})
}

// detectMode 根据 S3_ENDPOINT 和 MINIO_HOSTNAME 推断运行模式
func detectMode(endpoint, minioHost string) string {
	if endpoint == "" {
		return "legacy"
	}
	ep := endpoint
	if !strings.Contains(ep, "://") {
		ep = "http://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return "external_s3"
	}
	host := u.Hostname()
	if minioHost != "" && strings.EqualFold(host, minioHost) {
		return "minio"
	}
	// 内置 MinIO 默认域名
	if strings.HasSuffix(host, ".cloudland.internal") {
		return "minio"
	}
	return "external_s3"
}

func maskIfSet(v string) string {
	if v == "" {
		return ""
	}
	return maskedValue
}
