package services

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"cpgateway/src/model"
	"cpgateway/src/tracing"
)

// Internal region endpoints are private addresses with self-signed certs (httpx verify=False).
var insecureTransport = &http.Transport{
	Proxy:           http.ProxyFromEnvironment,
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}

func InsecureClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: tracedInsecureTransport(), Timeout: timeout}
}

// tracedInsecureTransport 在请求 ctx 带上游 span 时注入 trace 上下文；后台同步请求不带 ctx，不受影响
var tracedInsecureTransport = sync.OnceValue(func() http.RoundTripper {
	return tracing.HTTPTransport(insecureTransport)
})

func configFloat(key string, def float64) float64 {
	if viper.IsSet(key) {
		return viper.GetFloat64(key)
	}
	return def
}

func BackendAPISuffix() string {
	if s := viper.GetString("proxy.backend_api_suffix"); s != "" {
		return s
	}
	return "/api/v1"
}

func BackendRequestTimeout() time.Duration {
	return time.Duration(configFloat("proxy.backend_request_timeout", 10) * float64(time.Second))
}

func ProxyTimeout() time.Duration {
	return time.Duration(configFloat("proxy.timeout_seconds", 30) * float64(time.Second))
}

// BuildBackendURL mirrors quota_service.build_backend_url.
func BuildBackendURL(internalEndpoint, apiPath string) string {
	base := strings.TrimRight(strings.TrimSpace(internalEndpoint), "/")
	if suffix := BackendAPISuffix(); !strings.HasSuffix(base, suffix) {
		base += suffix
	}
	if apiPath != "" {
		return base + "/" + apiPath
	}
	return base
}

// RegionInternalURL mirrors `internal_endpoint.rstrip("/") + "/api/v1" + path` used by sync/heartbeat calls.
func RegionInternalURL(region *model.Region, path string) string {
	return strings.TrimRight(region.InternalEndpoint, "/") + "/api/v1" + path
}

// SyncTargetRegions returns regions that are available and not in maintenance.
func SyncTargetRegions(db *gorm.DB) []model.Region {
	var regions []model.Region
	if err := db.Where("is_available = ? AND maintenance_mode = ?", true, false).Find(&regions).Error; err != nil {
		log.Errorf("Failed to list sync target regions: %v", err)
	}
	return regions
}

// ForUpdate applies SELECT ... FOR UPDATE on databases that support it.
func ForUpdate(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "postgres" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

// pushToRegion POSTs a JSON payload to a region's internal endpoint with
// up to 3 attempts (2s, 4s backoff), accepting only 200/201.
func pushToRegion(ctx context.Context, region *model.Region, path string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := RegionInternalURL(region, path)
	client := InsecureClient(10 * time.Second)

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		lastErr = func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Forwarded-Secret", region.InternalSecret)
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			respBody, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
				text := string(respBody)
				if len(text) > 200 {
					text = text[:200]
				}
				return fmt.Errorf("HTTP %d: %s", resp.StatusCode, text)
			}
			return nil
		}()
		if lastErr == nil {
			return nil
		}
		if attempt < maxRetries-1 {
			delay := time.Duration((attempt+1)*2) * time.Second
			log.WithContext(ctx).Warnf("Retry %d/%d pushing %s to region '%s': %v", attempt+1, maxRetries, path, region.Name, lastErr)
			time.Sleep(delay)
		}
	}
	return lastErr
}
