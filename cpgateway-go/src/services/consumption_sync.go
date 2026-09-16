package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/tracing"
)

const consumptionSyncCooldown = time.Hour

var (
	consumptionSyncMu sync.Mutex
	syncingPairs      = map[[2]int64]bool{}
	lastSyncTime      = map[[2]int64]time.Time{}
)

// TriggerConsumptionSync starts a background reconciliation on login unless the
// org-region pair is already syncing or was synced within the last hour.
func TriggerConsumptionSync(orgID int64, orgUUID string, regionID int64, internalEndpoint, internalSecret string) {
	key := [2]int64{orgID, regionID}

	consumptionSyncMu.Lock()
	if syncingPairs[key] {
		consumptionSyncMu.Unlock()
		log.Debugf("ConsumptionSync: already in progress for org=%d, region=%d, skipping", orgID, regionID)
		return
	}
	if last, ok := lastSyncTime[key]; ok && time.Since(last) < consumptionSyncCooldown {
		consumptionSyncMu.Unlock()
		log.Debugf("ConsumptionSync: skipped (last sync was %.0fs ago)", time.Since(last).Seconds())
		return
	}
	syncingPairs[key] = true
	consumptionSyncMu.Unlock()

	go func() {
		// 登录触发的后台对账：独立 trace（按 TRACING_BACKGROUND_SAMPLE_RATIO 采样）
		ctx, span := tracing.StartBackground(context.Background(), "consumption.sync",
			trace.WithAttributes(attribute.Int64("cloudland.org_id", orgID), attribute.Int64("cloudland.region_id", regionID)))
		err := doConsumptionSync(ctx, orgID, orgUUID, regionID, internalEndpoint, internalSecret)
		tracing.EndSpan(span, err)

		consumptionSyncMu.Lock()
		defer consumptionSyncMu.Unlock()
		delete(syncingPairs, key)
		if err != nil {
			log.WithContext(ctx).Errorf("ConsumptionSync failed: org=%d, region=%d: %v", orgID, regionID, err)
			return
		}
		lastSyncTime[key] = time.Now()
	}()
}

const (
	consumptionSyncPageSize = 100
	// 翻页上限：防止后端 total 异常时无限循环（100 × 1000 = 10 万条，远超单组织单区域的资源量）
	consumptionSyncMaxPages = 1000
)

// fetchAllResources 按 offset 翻页取完整列表。clapi 列表接口默认每页 50 条，只取一页时
// 资源超过 50 个的组织会被少算，而对账结果会覆盖逐次记账的用量，导致配额限制失效
func fetchAllResources(ctx context.Context, client *http.Client, url, query string, headers map[string]string) ([]interface{}, error) {
	all := []interface{}{}
	for page := 0; page < consumptionSyncMaxPages; page++ {
		pageURL := fmt.Sprintf("%s?limit=%d&offset=%d", url, consumptionSyncPageSize, len(all))
		if query != "" {
			pageURL += "&" + query
		}
		items, total, err := fetchResourcePage(ctx, client, pageURL, headers)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) == 0 || len(all) >= total {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GET %s: exceeded %d pages", url, consumptionSyncMaxPages)
}

// fetchResourcePage 返回一页列表及其 total。响应为对象时取其中的列表字段；没有 total 时视为只有这一页
func fetchResourcePage(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]interface{}, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.WithContext(ctx).Warnf("ConsumptionSync: GET %s error: %v", url, err)
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.WithContext(ctx).Warnf("ConsumptionSync: GET %s -> %d", url, resp.StatusCode)
		return nil, 0, fmt.Errorf("GET %s -> %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, 0, err
	}
	switch v := data.(type) {
	case []interface{}:
		return v, len(v), nil
	case map[string]interface{}:
		var items []interface{}
		for _, item := range v {
			if list, ok := item.([]interface{}); ok {
				items = list
				break
			}
		}
		total := len(items)
		if t, ok := ToFloat(v["total"]); ok {
			total = int(t)
		}
		return items, total, nil
	}
	return []interface{}{}, 0, nil
}

func fieldFloat(item interface{}, key string) float64 {
	m, ok := item.(map[string]interface{})
	if !ok {
		return 0
	}
	f, _ := ToFloat(m[key])
	return f
}

func doConsumptionSync(ctx context.Context, orgID int64, orgUUID string, regionID int64, internalEndpoint, internalSecret string) error {
	// clapi 只按 X-Org-UUID 解析组织（两侧自增 ID 各自独立）。缺了它 clapi 会把请求当作
	// 不属于任何组织，列表全部为空，下面就会把用量覆盖成 0，配额限制随之失效
	if orgUUID == "" {
		return fmt.Errorf("missing org uuid for org=%d", orgID)
	}
	headers := map[string]string{
		"X-Org-UUID":         orgUUID,
		"X-Forwarded-Secret": internalSecret,
		"X-System-Role":      "1",
	}
	base := BuildBackendURL(internalEndpoint, "")
	client := InsecureClient(10 * time.Second)

	var cpu, ram, disk float64
	instances, err := fetchAllResources(ctx, client, base+"/instances", "", headers)
	if err != nil {
		return fmt.Errorf("failed to fetch instances for org=%d", orgID)
	}
	for _, inst := range instances {
		cpu += fieldFloat(inst, "cpu")
		ram += fieldFloat(inst, "memory") / 1024.0
	}

	volumes, err := fetchAllResources(ctx, client, base+"/volumes", "type=all", headers)
	if err != nil {
		return fmt.Errorf("failed to fetch volumes for org=%d", orgID)
	}
	for _, vol := range volumes {
		disk += fieldFloat(vol, "size")
	}

	fips, err := fetchAllResources(ctx, client, base+"/floating_ips", "", headers)
	if err != nil {
		return fmt.Errorf("failed to fetch floating_ips for org=%d", orgID)
	}

	tx := dbs.DBContext(ctx).Begin()
	var consumption model.OrgResourceConsumption
	if err := ForUpdate(tx).Where("org_id = ? AND region_id = ?", orgID, regionID).First(&consumption).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("no consumption record for org=%d, region=%d", orgID, regionID)
	}
	if err := tx.Model(&consumption).Updates(map[string]interface{}{
		"cpu_cores":  cpu,
		"ram_gb":     ram,
		"disk_gb":    disk,
		"public_ips": len(fips),
	}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}

	log.WithContext(ctx).Infof("ConsumptionSync: org=%d, region=%d -> cpu=%v, ram=%.2fGB, disk=%vGB, public_ips=%d",
		orgID, regionID, cpu, ram, disk, len(fips))
	return nil
}
