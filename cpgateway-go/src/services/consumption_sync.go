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
		// Login-triggered background reconciliation: its own trace (sampled by TRACING_BACKGROUND_SAMPLE_RATIO)
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
	// Page limit guards against an endless loop on a bogus total (100 x 1000 = 100k items, far beyond one org in one region)
	consumptionSyncMaxPages = 1000
)

// fetchAllResources pages through the full list by offset. clapi lists return 50 items per page by default; reading only
// one page undercounts orgs with more resources, and the sync result overwrites the tracked usage, breaking quota limits
func fetchAllResources(ctx context.Context, client *http.Client, url, query string, headers map[string]string) ([]interface{}, error) {
	all := []interface{}{}
	for page := 0; page < consumptionSyncMaxPages; page++ {
		pageURL := fmt.Sprintf("%s?limit=%d&offset=%d", url, consumptionSyncPageSize, len(all))
		if query != "" {
			pageURL += "&" + query
		}
		items, total, _, err := fetchResourcePage(ctx, client, pageURL, headers)
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

// fetchResourcePage returns one page, its total and whether the response carried a total. For an object response
// it takes the list field; without a total the page is treated as the whole list
func fetchResourcePage(ctx context.Context, client *http.Client, url string, headers map[string]string) (items []interface{}, total int, hasTotal bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, false, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.WithContext(ctx).Warnf("ConsumptionSync: GET %s error: %v", url, err)
		return nil, 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.WithContext(ctx).Warnf("ConsumptionSync: GET %s -> %d", url, resp.StatusCode)
		return nil, 0, false, fmt.Errorf("GET %s -> %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, false, err
	}
	var data interface{}
	if err = json.Unmarshal(body, &data); err != nil {
		return nil, 0, false, err
	}
	switch v := data.(type) {
	case []interface{}:
		return v, len(v), false, nil
	case map[string]interface{}:
		for _, item := range v {
			if list, ok := item.([]interface{}); ok {
				items = list
				break
			}
		}
		if t, ok := ToFloat(v["total"]); ok {
			return items, int(t), true, nil
		}
		return items, len(items), false, nil
	}
	return []interface{}{}, 0, false, nil
}

// fetchResourceCount returns the size of a list from its total while requesting a single item. A list
// without a total falls back to paging through every item.
func fetchResourceCount(ctx context.Context, client *http.Client, url, query string, headers map[string]string) (int, error) {
	pageURL := url + "?limit=1&offset=0"
	if query != "" {
		pageURL += "&" + query
	}
	_, total, hasTotal, err := fetchResourcePage(ctx, client, pageURL, headers)
	if err != nil {
		return 0, err
	}
	if hasTotal {
		return total, nil
	}
	all, err := fetchAllResources(ctx, client, url, query, headers)
	return len(all), err
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
	// clapi resolves the org only from X-Org-UUID (auto-increment IDs differ between the services). Without it clapi treats
	// the request as belonging to no org, every list is empty, usage is overwritten with 0 and quota limits stop working
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

	// Count-only resources read just the list total instead of paging through full objects (clapi builds
	// nested subnets, listeners and backends for each), which also keeps the sync short
	fips, err := fetchResourceCount(ctx, client, base+"/floating_ips", "", headers)
	if err != nil {
		return fmt.Errorf("failed to count floating_ips for org=%d", orgID)
	}
	vpcs, err := fetchResourceCount(ctx, client, base+"/vpcs", "", headers)
	if err != nil {
		return fmt.Errorf("failed to count vpcs for org=%d", orgID)
	}
	lbs, err := fetchResourceCount(ctx, client, base+"/load_balancers", "", headers)
	if err != nil {
		return fmt.Errorf("failed to count load_balancers for org=%d", orgID)
	}
	// Private images owned by the org. The plain list also returns other orgs' public images (and every org's
	// images for a system admin, the role used here); public platform images are not charged to any org.
	images, err := fetchResourceCount(ctx, client, base+"/images", "owned=true&visibility=private", headers)
	if err != nil {
		return fmt.Errorf("failed to count images for org=%d", orgID)
	}

	tx := dbs.DBContext(ctx).Begin()
	var consumption model.OrgResourceConsumption
	if err := ForUpdate(tx).Where("org_id = ? AND region_id = ?", orgID, regionID).First(&consumption).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("no consumption record for org=%d, region=%d", orgID, regionID)
	}
	if err := tx.Model(&consumption).Updates(map[string]interface{}{
		"cpu_cores":      cpu,
		"ram_gb":         ram,
		"disk_gb":        disk,
		"public_ips":     fips,
		"vpcs":           vpcs,
		"load_balancers": lbs,
		"images":         images,
	}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}

	log.WithContext(ctx).Infof("ConsumptionSync: org=%d, region=%d -> cpu=%v, ram=%.2fGB, disk=%vGB, public_ips=%d, vpcs=%d, load_balancers=%d, images=%d",
		orgID, regionID, cpu, ram, disk, fips, vpcs, lbs, images)
	return nil
}
