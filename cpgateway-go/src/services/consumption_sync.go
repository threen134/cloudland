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
func TriggerConsumptionSync(orgID, regionID int64, internalEndpoint, internalSecret string) {
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
		err := doConsumptionSync(ctx, orgID, regionID, internalEndpoint, internalSecret)
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

// fetchResourceList returns the list at url; a bare list or the first list value of an object.
func fetchResourceList(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.WithContext(ctx).Warnf("ConsumptionSync: GET %s error: %v", url, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.WithContext(ctx).Warnf("ConsumptionSync: GET %s -> %d", url, resp.StatusCode)
		return nil, fmt.Errorf("GET %s -> %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	switch v := data.(type) {
	case []interface{}:
		return v, nil
	case map[string]interface{}:
		for _, item := range v {
			if list, ok := item.([]interface{}); ok {
				return list, nil
			}
		}
	}
	return []interface{}{}, nil
}

func fieldFloat(item interface{}, key string) float64 {
	m, ok := item.(map[string]interface{})
	if !ok {
		return 0
	}
	f, _ := ToFloat(m[key])
	return f
}

func doConsumptionSync(ctx context.Context, orgID, regionID int64, internalEndpoint, internalSecret string) error {
	headers := map[string]string{
		"X-Org-ID":           fmt.Sprintf("%d", orgID),
		"X-Forwarded-Secret": internalSecret,
		"X-System-Role":      "1",
	}
	base := BuildBackendURL(internalEndpoint, "")
	client := InsecureClient(10 * time.Second)

	var cpu, ram, disk float64
	instances, err := fetchResourceList(ctx, client, base+"/instances", headers)
	if err != nil {
		return fmt.Errorf("failed to fetch instances for org=%d", orgID)
	}
	for _, inst := range instances {
		cpu += fieldFloat(inst, "cpu")
		ram += fieldFloat(inst, "memory") / 1024.0
	}

	volumes, err := fetchResourceList(ctx, client, base+"/volumes", headers)
	if err != nil {
		return fmt.Errorf("failed to fetch volumes for org=%d", orgID)
	}
	for _, vol := range volumes {
		disk += fieldFloat(vol, "size")
	}

	fips, err := fetchResourceList(ctx, client, base+"/floating_ips", headers)
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
