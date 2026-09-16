package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
)

// ResourceAmount maps consumption fields (cpu_cores, ram_gb, public_ips, disk_gb) to amounts.
// Key presence matters, mirroring the Python dicts.
type ResourceAmount map[string]float64

// quotaRules maps "METHOD route-template" (proxy_routes.go templates without the leading slash) to
// the quota action. Matching whole templates keeps sub-resource operations (e.g. deleting an
// instance interface) from releasing the parent's quota.
var quotaRules = map[string]string{
	"POST instances":             "consume",
	"POST volumes":               "consume",
	"POST floating_ips":          "consume",
	"DELETE instances/{id}":      "release",
	"DELETE volumes/{id}":        "release",
	"DELETE floating_ips/{id}":   "release",
	"POST instances/{id}/resize": "resize",
	"POST volumes/{id}/resize":   "resize",
}

// Order used when checking/reserving, matching the Python dict insertion order.
var resourceFieldOrder = []string{"cpu_cores", "ram_gb", "disk_gb", "public_ips"}

// MatchQuotaRule matches a proxy route template (e.g. "/instances/{id}/resize") against the quota rules.
func MatchQuotaRule(method, proxyTemplate string) string {
	return quotaRules[method+" "+strings.Trim(proxyTemplate, "/")]
}

// ExtractResourceID returns the second path segment: instances/abc-123[/resize] → abc-123.
func ExtractResourceID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func settingFloat(db *gorm.DB, key string, def float64) float64 {
	if f, ok := ToFloat(GetSetting(db, key)); ok {
		return f
	}
	return def
}

// DefaultQuotaValues reads default quotas from system settings (DB → config → hardcoded).
func DefaultQuotaValues(db *gorm.DB) (cpu, ram float64, ips int, disk float64) {
	cpu = settingFloat(db, "DEFAULT_CPU_CORES", configFloat("quota.defaults.cpu_cores", 4.0))
	ram = settingFloat(db, "DEFAULT_RAM_GB", configFloat("quota.defaults.ram_gb", 8.0))
	ips = int(settingFloat(db, "DEFAULT_PUBLIC_IPS", configFloat("quota.defaults.public_ips", 2)))
	disk = settingFloat(db, "DEFAULT_DISK_GB", configFloat("quota.defaults.disk_gb", 50.0))
	return
}

func createQuotaPair(tx *gorm.DB, orgID, regionID int64, cpu, ram float64, ips int, disk float64) error {
	if err := tx.Create(&model.OrgResourceQuota{
		OrgID: orgID, RegionID: regionID,
		MaxCPUCores: cpu, MaxRAMGB: ram, MaxPublicIPs: ips, MaxDiskGB: disk,
	}).Error; err != nil {
		return err
	}
	return tx.Create(&model.OrgResourceConsumption{OrgID: orgID, RegionID: regionID}).Error
}

// InitializeOrgQuotas creates quota + consumption rows for a new org in every region.
func InitializeOrgQuotas(tx *gorm.DB, orgID int64) error {
	cpu, ram, ips, disk := DefaultQuotaValues(tx)
	var regions []model.Region
	if err := tx.Find(&regions).Error; err != nil {
		return err
	}
	for _, r := range regions {
		if err := createQuotaPair(tx, orgID, r.ID, cpu, ram, ips, disk); err != nil {
			return err
		}
	}
	return nil
}

// InitializeRegionQuotas creates quota + consumption rows for a new region for every org.
func InitializeRegionQuotas(tx *gorm.DB, regionID int64) error {
	cpu, ram, ips, disk := DefaultQuotaValues(tx)
	var orgs []model.Organization
	if err := tx.Find(&orgs).Error; err != nil {
		return err
	}
	for _, o := range orgs {
		if err := createQuotaPair(tx, o.ID, regionID, cpu, ram, ips, disk); err != nil {
			return err
		}
	}
	return nil
}

// pyNum formats like Python str(): ints for public_ips, floats always with a decimal point.
func pyNum(field string, v float64) string {
	if field == "public_ips" {
		return strconv.Itoa(int(v))
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func quotaExceeded(resource string, requested, available, limit float64, region string) *common.HTTPError {
	available = math.Max(0, available)
	return common.NewHTTPError(http.StatusTooManyRequests, map[string]interface{}{
		"error":     "quota_exceeded",
		"resource":  resource,
		"region":    region,
		"requested": requested,
		"available": available,
		"limit":     limit,
		"message": fmt.Sprintf("Org quota exceeded for %s in region %s: requested %s, available %s (limit: %s)",
			resource, region, pyNum(resource, requested), pyNum(resource, available), pyNum(resource, limit)),
	})
}

func consumptionValues(c *model.OrgResourceConsumption) map[string]float64 {
	return map[string]float64{"cpu_cores": c.CPUCores, "ram_gb": c.RAMGB, "public_ips": float64(c.PublicIPs), "disk_gb": c.DiskGB}
}

func consumptionUpdate(field string, v float64) interface{} {
	if field == "public_ips" {
		return int(v)
	}
	return v
}

// CheckAndReserve atomically checks the quota and reserves the amount, locking the consumption row.
func CheckAndReserve(orgID, regionID int64, amount ResourceAmount) *common.HTTPError {
	if len(amount) == 0 {
		return nil
	}
	tx := dbs.DB().Begin()

	var consumption model.OrgResourceConsumption
	if err := ForUpdate(tx).Where("org_id = ? AND region_id = ?", orgID, regionID).First(&consumption).Error; err != nil {
		tx.Rollback()
		return common.NewHTTPError(http.StatusNotFound, "Consumption record not found")
	}
	var quota model.OrgResourceQuota
	if err := tx.Where("org_id = ? AND region_id = ?", orgID, regionID).First(&quota).Error; err != nil {
		tx.Rollback()
		return common.NewHTTPError(http.StatusNotFound, "Quota record not found")
	}
	regionName := strconv.FormatInt(regionID, 10)
	var region model.Region
	if err := tx.Select("name").Where("id = ?", regionID).First(&region).Error; err == nil && region.Name != "" {
		regionName = region.Name
	}

	current := consumptionValues(&consumption)
	limits := map[string]float64{
		"cpu_cores": quota.MaxCPUCores, "ram_gb": quota.MaxRAMGB,
		"public_ips": float64(quota.MaxPublicIPs), "disk_gb": quota.MaxDiskGB,
	}
	updates := map[string]interface{}{}
	for _, field := range resourceFieldOrder {
		req, ok := amount[field]
		if !ok || req <= 0 {
			continue
		}
		cur, limit := current[field], limits[field]
		if cur+req > limit {
			tx.Rollback()
			return quotaExceeded(field, req, limit-cur, limit, regionName)
		}
		updates[field] = consumptionUpdate(field, cur+req)
	}
	if len(updates) > 0 {
		if err := tx.Model(&consumption).Updates(updates).Error; err != nil {
			tx.Rollback()
			return common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
		}
	}
	if err := tx.Commit().Error; err != nil {
		return common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}
	log.Infof("Quota reserved: org=%d, region=%d, amount=%v", orgID, regionID, amount)
	return nil
}

// Release decreases consumption (never below zero).
func Release(orgID, regionID int64, amount ResourceAmount) {
	if len(amount) == 0 {
		return
	}
	tx := dbs.DB().Begin()
	var consumption model.OrgResourceConsumption
	if err := ForUpdate(tx).Where("org_id = ? AND region_id = ?", orgID, regionID).First(&consumption).Error; err != nil {
		tx.Rollback()
		log.Warnf("Release failed: no consumption record for org=%d, region=%d", orgID, regionID)
		return
	}
	current := consumptionValues(&consumption)
	updates := map[string]interface{}{}
	for _, field := range resourceFieldOrder {
		v, ok := amount[field]
		if !ok || v <= 0 {
			continue
		}
		updates[field] = consumptionUpdate(field, math.Max(0, current[field]-v))
	}
	if len(updates) > 0 {
		if err := tx.Model(&consumption).Updates(updates).Error; err != nil {
			tx.Rollback()
			log.Errorf("Release failed: org=%d, region=%d: %v", orgID, regionID, err)
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		log.Errorf("Release commit failed: org=%d, region=%d: %v", orgID, regionID, err)
		return
	}
	log.Infof("Quota released: org=%d, region=%d, amount=%v", orgID, regionID, amount)
}

// getBackendJSON returns a transport error only when the request itself failed.
func getBackendJSON(ctx context.Context, url string, headers map[string]string) (map[string]interface{}, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := InsecureClient(BackendRequestTimeout()).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	data := map[string]interface{}{}
	json.Unmarshal(body, &data)
	return data, resp.StatusCode, nil
}

func floatOrZero(v interface{}) float64 {
	f, _ := ToFloat(v)
	return f
}

// QueryResourceAmount fetches a resource's current size before DELETE/RESIZE.
func QueryResourceAmount(ctx context.Context, region *model.Region, proxyPath, resourceID string, headers map[string]string) (ResourceAmount, *common.HTTPError) {
	var apiPath string
	switch {
	case strings.Contains(proxyPath, "/instances"):
		apiPath = "instances/" + resourceID
	case strings.Contains(proxyPath, "/volumes"):
		apiPath = "volumes/" + resourceID
	case strings.Contains(proxyPath, "/floating_ips"):
		return ResourceAmount{"public_ips": 1}, nil
	default:
		return ResourceAmount{}, nil
	}

	url := BuildBackendURL(region.InternalEndpoint, apiPath)
	data, status, err := getBackendJSON(ctx, url, headers)
	if err != nil {
		log.WithContext(ctx).Errorf("Failed to query resource amount: %s: %v", url, err)
		return nil, common.NewHTTPError(http.StatusBadGateway,
			fmt.Sprintf("Failed to query resource %s for quota tracking: %v", resourceID, err))
	}
	// 4xx 是请求本身的问题（资源不存在、ID 非法、无权访问），原样转给客户端；
	// 此前一律报 502，删除一个不存在的虚拟机会被当成网关故障
	if status >= 400 && status < 500 {
		log.WithContext(ctx).Infof("Resource query for quota tracking rejected by backend: %s -> %d", url, status)
		detail := fmt.Sprintf("Resource %s is not available (status=%d)", resourceID, status)
		if msg, ok := data["error_message"].(string); ok && msg != "" {
			detail = msg
		}
		return nil, common.NewHTTPError(status, detail)
	}
	if status != http.StatusOK {
		log.WithContext(ctx).Errorf("Failed to query resource amount: %s -> %d", url, status)
		return nil, common.NewHTTPError(http.StatusBadGateway,
			fmt.Sprintf("Failed to query resource %s for quota tracking (status=%d)", resourceID, status))
	}

	if strings.Contains(proxyPath, "/instances") {
		cpu, memory, disk := floatOrZero(data["cpu"]), floatOrZero(data["memory"]), floatOrZero(data["disk"])
		if cpu != 0 || memory != 0 || disk != 0 {
			return ResourceAmount{"cpu_cores": cpu, "ram_gb": memory / 1024.0, "disk_gb": disk}, nil
		}
		log.WithContext(ctx).Warnf("Instance %s has no cpu/memory/disk data, skipping quota tracking", resourceID)
		return ResourceAmount{}, nil
	}
	return ResourceAmount{"disk_gb": floatOrZero(data["size"])}, nil
}

func firstPresent(data map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := data[k]; ok {
			return v
		}
	}
	return 0
}

// QueryFlavorAmount fetches a flavor's cpu/ram/disk specs.
func QueryFlavorAmount(ctx context.Context, region *model.Region, flavorID interface{}, headers map[string]string) (ResourceAmount, *common.HTTPError) {
	id := fmt.Sprint(flavorID)
	url := BuildBackendURL(region.InternalEndpoint, "flavors/"+id)
	data, status, err := getBackendJSON(ctx, url, headers)
	if err != nil {
		log.WithContext(ctx).Errorf("Failed to query flavor: %s: %v", url, err)
		return nil, common.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("Failed to query flavor %s: %v", id, err))
	}
	if status != http.StatusOK {
		log.WithContext(ctx).Warnf("Failed to query flavor: %s -> %d", url, status)
		return nil, common.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("Failed to query flavor %s", id))
	}
	return ResourceAmount{
		"cpu_cores": floatOrZero(firstPresent(data, "cpu", "vcpus")),
		"ram_gb":    floatOrZero(firstPresent(data, "memory", "ram")) / 1024.0,
		"disk_gb":   floatOrZero(data["disk"]),
	}, nil
}

// QuotaPlan records what the proxy reserved before forwarding and what to settle afterwards.
type QuotaPlan struct {
	Action   string
	Reserved bool
	Amount   ResourceAmount
	Shrink   ResourceAmount
	// Multi-instance create: Amount = Unit × Count.
	Unit  ResourceAmount
	Count int
}

// instanceCount mirrors clapi's create handler: count defaults to 1 and values below 1 count as 1.
func instanceCount(body map[string]interface{}) int {
	n, ok := ToFloat(body["count"])
	if !ok || n < 1 {
		return 1
	}
	return int(n)
}

func scaleAmount(amount ResourceAmount, n int) ResourceAmount {
	out := make(ResourceAmount, len(amount))
	for k, v := range amount {
		out[k] = v * float64(n)
	}
	return out
}

func parseBodyObject(raw []byte) (map[string]interface{}, *common.HTTPError) {
	body := map[string]interface{}{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}
	return body, nil
}

func explicitInstanceSpec(body map[string]interface{}) (ResourceAmount, bool) {
	if body["cpu"] == nil || body["memory"] == nil {
		return nil, false
	}
	disk := body["disk"]
	if !Truthy(disk) {
		disk = 0
	}
	return ResourceAmount{
		"cpu_cores": floatOrZero(body["cpu"]),
		"ram_gb":    floatOrZero(body["memory"]) / 1024.0,
		"disk_gb":   floatOrZero(disk),
	}, true
}

// PrepareQuota runs the pre-forward quota step: reserve for create/expand, measure for delete.
func PrepareQuota(ctx context.Context, orgID int64, region *model.Region, method, proxyTemplate, resolvedPath string, rawBody []byte, headers map[string]string) (*QuotaPlan, *common.HTTPError) {
	plan := &QuotaPlan{Action: MatchQuotaRule(method, proxyTemplate)}

	switch plan.Action {
	case "consume":
		body, herr := parseBodyObject(rawBody)
		if herr != nil {
			return nil, herr
		}
		amount := ResourceAmount{}
		switch {
		case strings.Contains(proxyTemplate, "instances"):
			if spec, ok := explicitInstanceSpec(body); ok {
				amount = spec
			} else if Truthy(body["flavor"]) {
				if amount, herr = QueryFlavorAmount(ctx, region, body["flavor"], headers); herr != nil {
					return nil, herr
				}
			}
			if len(amount) > 0 {
				plan.Unit, plan.Count = amount, instanceCount(body)
				amount = scaleAmount(amount, plan.Count)
			}
		case strings.Contains(proxyTemplate, "volumes"):
			if body["size"] != nil {
				amount = ResourceAmount{"disk_gb": floatOrZero(body["size"])}
			}
		case strings.Contains(proxyTemplate, "floating_ips"):
			amount = ResourceAmount{"public_ips": 1}
		}
		if len(amount) > 0 {
			if herr := CheckAndReserve(orgID, region.ID, amount); herr != nil {
				return nil, herr
			}
			plan.Reserved, plan.Amount = true, amount
		}

	case "release":
		amount, herr := QueryResourceAmount(ctx, region, resolvedPath, ExtractResourceID(resolvedPath), headers)
		if herr != nil {
			return nil, herr
		}
		plan.Amount = amount

	case "resize":
		body, herr := parseBodyObject(rawBody)
		if herr != nil {
			return nil, herr
		}
		resourceID := ExtractResourceID(resolvedPath)
		diff := ResourceAmount{}
		switch {
		case strings.Contains(proxyTemplate, "instances"):
			// clapi InstanceResizePayload: cpu (cores) and memory (MB), each optional; an omitted
			// value keeps the current size and the disk is never resized.
			cpu, memory := floatOrZero(body["cpu"]), floatOrZero(body["memory"])
			if cpu <= 0 && memory <= 0 {
				break
			}
			oldAmount, herr := QueryResourceAmount(ctx, region, resolvedPath, resourceID, headers)
			if herr != nil {
				return nil, herr
			}
			if len(oldAmount) == 0 {
				break // legacy instance without cpu/memory data: not tracked
			}
			if cpu > 0 {
				diff["cpu_cores"] = cpu - oldAmount["cpu_cores"]
			}
			if memory > 0 {
				diff["ram_gb"] = memory/1024.0 - oldAmount["ram_gb"]
			}
		case strings.Contains(proxyTemplate, "volumes"):
			// clapi VolumeResizePayload: size (GB), required.
			size := floatOrZero(body["size"])
			if size <= 0 {
				break
			}
			oldAmount, herr := QueryResourceAmount(ctx, region, resolvedPath, resourceID, headers)
			if herr != nil {
				return nil, herr
			}
			diff["disk_gb"] = size - oldAmount["disk_gb"]
		}

		reserve, shrink := ResourceAmount{}, ResourceAmount{}
		for k, v := range diff {
			if v > 0 {
				reserve[k] = v
			} else if v < 0 {
				shrink[k] = -v
			}
		}
		plan.Shrink = shrink
		if len(reserve) > 0 {
			if herr := CheckAndReserve(orgID, region.ID, reserve); herr != nil {
				return nil, herr
			}
			plan.Reserved, plan.Amount = true, reserve
		}
	}
	return plan, nil
}

// FinishQuota settles the plan after forwarding, based only on the backend's answer. status is 0
// when no response was received; respBody is nil when it could not be read.
func FinishQuota(orgID, regionID int64, plan *QuotaPlan, status int, respBody []byte) {
	if plan == nil {
		return
	}
	if status == http.StatusOK || status == http.StatusCreated || status == http.StatusNoContent {
		switch {
		case plan.Action == "release" && len(plan.Amount) > 0:
			Release(orgID, regionID, plan.Amount)
		case plan.Action == "resize" && len(plan.Shrink) > 0:
			Release(orgID, regionID, plan.Shrink)
		case plan.Action == "consume" && plan.Reserved && plan.Count > 1:
			// clapi returns the created instances; give back the reservation of any that were not created.
			var created []json.RawMessage
			if respBody != nil && json.Unmarshal(respBody, &created) == nil && len(created) < plan.Count {
				Release(orgID, regionID, scaleAmount(plan.Unit, plan.Count-len(created)))
			}
		}
		return
	}
	if plan.Reserved {
		Release(orgID, regionID, plan.Amount)
	}
}
