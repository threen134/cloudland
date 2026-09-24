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

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

// ResourceAmount maps consumption fields (cpu_cores, ram_gb, disk_gb, public_ips, vpcs,
// load_balancers, images) to amounts. Key presence matters, mirroring the Python dicts.
type ResourceAmount map[string]float64

// quotaRule is the quota action for a proxy route and the kind of resource it acts on.
type quotaRule struct {
	Action   string // consume, release or resize
	Resource string // instance, volume, floating_ip, lb_floating_ip, vpc, load_balancer or image
}

// quotaRules maps "METHOD route-template" (proxy_routes.go templates without the leading slash) to the
// quota rule. Matching whole templates keeps sub-resource operations (e.g. deleting an instance interface)
// from releasing the parent's quota, and states the resource explicitly instead of guessing it from the path.
var quotaRules = map[string]quotaRule{
	"POST instances":                        {"consume", "instance"},
	"POST volumes":                          {"consume", "volume"},
	"POST floating_ips":                     {"consume", "floating_ip"},
	"POST load_balancers/{id}/floating_ips": {"consume", "lb_floating_ip"},
	"POST vpcs":                             {"consume", "vpc"},
	"POST load_balancers":                   {"consume", "load_balancer"},
	"POST vpn_gateways":                     {"consume", "vpn_gateway"},
	"POST images":                           {"consume", "image"},
	"DELETE instances/{id}":                 {"release", "instance"},
	"DELETE volumes/{id}":                   {"release", "volume"},
	"DELETE floating_ips/{id}":              {"release", "floating_ip"},
	"DELETE load_balancers/{id}/floating_ips/{floating_ip_id}": {"release", "lb_floating_ip"},
	"DELETE vpcs/{id}":           {"release", "vpc"},
	"DELETE load_balancers/{id}": {"release", "load_balancer"},
	"DELETE vpn_gateways/{id}":   {"release", "vpn_gateway"},
	"DELETE images/{id}":         {"release", "image"},
	"POST instances/{id}/resize": {"resize", "instance"},
	"POST volumes/{id}/resize":   {"resize", "volume"},
}

// QuotaResourceFields lists the consumption fields in check order; the quota column of each is "max_" + field.
var QuotaResourceFields = []string{"cpu_cores", "ram_gb", "disk_gb", "public_ips", "vpcs", "load_balancers", "images", "vpn_gateways"}

// integerQuotaFields are counted per object and stored as integers.
var integerQuotaFields = map[string]bool{"public_ips": true, "vpcs": true, "load_balancers": true, "images": true, "vpn_gateways": true}

// IsIntegerQuotaField reports whether a consumption field is an integer count.
func IsIntegerQuotaField(field string) bool {
	return integerQuotaFields[field]
}

// systemAdminRole is the X-System-Role header value of a system admin.
const systemAdminRole = "1"

func lookupQuotaRule(method, proxyTemplate string) (quotaRule, bool) {
	rule, ok := quotaRules[method+" "+strings.Trim(proxyTemplate, "/")]
	return rule, ok
}

// MatchQuotaRule returns the quota action for a proxy route template (e.g. "/instances/{id}/resize"), or "".
func MatchQuotaRule(method, proxyTemplate string) string {
	rule, _ := lookupQuotaRule(method, proxyTemplate)
	return rule.Action
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

// Built-in default quotas, used when neither the system setting nor the config file sets a value.
// Keep in sync with conf/config.toml [quota.defaults] and the DEFAULT_* system settings.
const (
	DefaultQuotaVPCs          = 5
	DefaultQuotaLoadBalancers = 5
	DefaultQuotaImages        = 10
	DefaultQuotaVpnGateways   = 1
)

// DefaultQuotaValues reads default quotas from system settings (DB → config → hardcoded).
func DefaultQuotaValues(db *gorm.DB) model.OrgResourceQuota {
	return model.OrgResourceQuota{
		MaxCPUCores:      settingFloat(db, "DEFAULT_CPU_CORES", configFloat("quota.defaults.cpu_cores", 4.0)),
		MaxRAMGB:         settingFloat(db, "DEFAULT_RAM_GB", configFloat("quota.defaults.ram_gb", 8.0)),
		MaxPublicIPs:     int(settingFloat(db, "DEFAULT_PUBLIC_IPS", configFloat("quota.defaults.public_ips", 2))),
		MaxDiskGB:        settingFloat(db, "DEFAULT_DISK_GB", configFloat("quota.defaults.disk_gb", 50.0)),
		MaxVPCs:          int(settingFloat(db, "DEFAULT_VPCS", configFloat("quota.defaults.vpcs", DefaultQuotaVPCs))),
		MaxLoadBalancers: int(settingFloat(db, "DEFAULT_LOAD_BALANCERS", configFloat("quota.defaults.load_balancers", DefaultQuotaLoadBalancers))),
		MaxImages:        int(settingFloat(db, "DEFAULT_IMAGES", configFloat("quota.defaults.images", DefaultQuotaImages))),
		MaxVpnGateways:   int(settingFloat(db, "DEFAULT_VPN_GATEWAYS", configFloat("quota.defaults.vpn_gateways", DefaultQuotaVpnGateways))),
	}
}

func createQuotaPair(tx *gorm.DB, orgID, regionID int64, defaults model.OrgResourceQuota) error {
	quota := defaults
	quota.ID, quota.OrgID, quota.RegionID = 0, orgID, regionID
	if err := tx.Create(&quota).Error; err != nil {
		return err
	}
	return tx.Create(&model.OrgResourceConsumption{OrgID: orgID, RegionID: regionID}).Error
}

// InitializeOrgQuotas creates quota + consumption rows for a new org in every region.
func InitializeOrgQuotas(tx *gorm.DB, orgID int64) error {
	defaults := DefaultQuotaValues(tx)
	var regions []model.Region
	if err := tx.Find(&regions).Error; err != nil {
		return err
	}
	for _, r := range regions {
		if err := createQuotaPair(tx, orgID, r.ID, defaults); err != nil {
			return err
		}
	}
	return nil
}

// InitializeRegionQuotas creates quota + consumption rows for a new region for every org.
func InitializeRegionQuotas(tx *gorm.DB, regionID int64) error {
	defaults := DefaultQuotaValues(tx)
	var orgs []model.Organization
	if err := tx.Find(&orgs).Error; err != nil {
		return err
	}
	for _, o := range orgs {
		if err := createQuotaPair(tx, o.ID, regionID, defaults); err != nil {
			return err
		}
	}
	return nil
}

// pyNum formats like Python str(): ints for count fields, floats always with a decimal point.
func pyNum(field string, v float64) string {
	if IsIntegerQuotaField(field) {
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
	return map[string]float64{
		"cpu_cores": c.CPUCores, "ram_gb": c.RAMGB, "public_ips": float64(c.PublicIPs), "disk_gb": c.DiskGB,
		"vpcs": float64(c.VPCs), "load_balancers": float64(c.LoadBalancers), "images": float64(c.Images), "vpn_gateways": float64(c.VpnGateways),
	}
}

func quotaLimits(q *model.OrgResourceQuota) map[string]float64 {
	return map[string]float64{
		"cpu_cores": q.MaxCPUCores, "ram_gb": q.MaxRAMGB, "public_ips": float64(q.MaxPublicIPs), "disk_gb": q.MaxDiskGB,
		"vpcs": float64(q.MaxVPCs), "load_balancers": float64(q.MaxLoadBalancers), "images": float64(q.MaxImages), "vpn_gateways": float64(q.MaxVpnGateways),
	}
}

func consumptionUpdate(field string, v float64) interface{} {
	if IsIntegerQuotaField(field) {
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
	limits := quotaLimits(&quota)
	updates := map[string]interface{}{}
	for _, field := range QuotaResourceFields {
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
	for _, field := range QuotaResourceFields {
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

// fetchResource GETs a resource from the region backend with the caller's headers.
func fetchResource(ctx context.Context, region *model.Region, apiPath string, headers map[string]string) (map[string]interface{}, *common.HTTPError) {
	url := BuildBackendURL(region.InternalEndpoint, apiPath)
	data, status, err := getBackendJSON(ctx, url, headers)
	if err != nil {
		log.WithContext(ctx).Errorf("Failed to query resource amount: %s: %v", url, err)
		return nil, common.NewHTTPError(http.StatusBadGateway,
			fmt.Sprintf("Failed to query resource %s for quota tracking: %v", apiPath, err))
	}
	// A 4xx is a problem with the request itself (missing resource, invalid ID, no access): pass it through.
	// Reporting 502 would make deleting a nonexistent instance look like a gateway failure.
	if status >= 400 && status < 500 {
		log.WithContext(ctx).Infof("Resource query for quota tracking rejected by backend: %s -> %d", url, status)
		detail := fmt.Sprintf("Resource %s is not available (status=%d)", apiPath, status)
		if msg, ok := data["error_message"].(string); ok && msg != "" {
			detail = msg
		}
		return nil, common.NewHTTPError(status, detail)
	}
	if status != http.StatusOK {
		log.WithContext(ctx).Errorf("Failed to query resource amount: %s -> %d", url, status)
		return nil, common.NewHTTPError(http.StatusBadGateway,
			fmt.Sprintf("Failed to query resource %s for quota tracking (status=%d)", apiPath, status))
	}
	return data, nil
}

// QueryResourceAmount fetches a resource (apiPath such as "vpcs/{uuid}") before DELETE/RESIZE and returns the
// quota it holds for the caller's org. It returns an empty amount when the resource belongs to another org:
// a system admin can delete any org's resource, but the quota is the owner's, not the caller's. That org's
// next consumption sync corrects its usage.
func QueryResourceAmount(ctx context.Context, region *model.Region, resource, apiPath string, headers map[string]string) (ResourceAmount, *common.HTTPError) {
	data, herr := fetchResource(ctx, region, apiPath, headers)
	if herr != nil {
		return nil, herr
	}
	if owner, _ := data["owner_uuid"].(string); owner != "" && owner != headers["X-Org-UUID"] {
		log.WithContext(ctx).Infof("Resource %s is owned by org %s, not the caller's org: quota not released", apiPath, owner)
		return ResourceAmount{}, nil
	}

	switch resource {
	case "instance":
		cpu, memory, disk := floatOrZero(data["cpu"]), floatOrZero(data["memory"]), floatOrZero(data["disk"])
		if cpu != 0 || memory != 0 || disk != 0 {
			return ResourceAmount{"cpu_cores": cpu, "ram_gb": memory / 1024.0, "disk_gb": disk}, nil
		}
		log.WithContext(ctx).Warnf("Instance %s has no cpu/memory/disk data, skipping quota tracking", apiPath)
		return ResourceAmount{}, nil
	case "volume":
		return ResourceAmount{"disk_gb": floatOrZero(data["size"])}, nil
	case "floating_ip", "lb_floating_ip":
		return ResourceAmount{"public_ips": 1}, nil
	case "vpc":
		return ResourceAmount{"vpcs": 1}, nil
	case "load_balancer":
		// Deleting a load balancer also deletes its floating IPs
		amount := ResourceAmount{"load_balancers": 1}
		if fips, ok := data["floating_ips"].([]interface{}); ok && len(fips) > 0 {
			amount["public_ips"] = float64(len(fips))
		}
		return amount, nil
	case "vpn_gateway":
		// A VPN gateway always owns exactly one public address, released with it
		amount := ResourceAmount{"vpn_gateways": 1}
		if fips, ok := data["floating_ips"].([]interface{}); ok && len(fips) > 0 {
			amount["public_ips"] = float64(len(fips))
		}
		return amount, nil
	case "image":
		// Only private images count against the org quota; public platform images are not charged
		if public, _ := data["public"].(bool); public {
			return ResourceAmount{}, nil
		}
		return ResourceAmount{"images": 1}, nil
	}
	return ResourceAmount{}, nil
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
	// Creates that may produce several objects (instances, floating IPs): Amount = Unit × Count, and the
	// reservation for objects the backend did not create is given back.
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

// floatingIPCount is an upper bound of the floating IPs one create request produces. clapi allocates
// activation_count addresses (0 counts as 1 without site subnets) plus one per site subnet.
func floatingIPCount(body map[string]interface{}) int {
	n := 0
	if f, ok := ToFloat(body["activation_count"]); ok && f > 0 {
		n = int(f)
	}
	if sites, ok := body["site_subnets"].([]interface{}); ok {
		n += len(sites)
	}
	if n < 1 {
		n = 1
	}
	return n
}

func scaleAmount(amount ResourceAmount, n int) ResourceAmount {
	out := make(ResourceAmount, len(amount))
	for k, v := range amount {
		out[k] = v * float64(n)
	}
	return out
}

// parseBodyObject returns the request body as a JSON object, or nil when it is not one. Such a request
// is rejected by clapi with 400 anyway, so nothing needs to be reserved for it.
func parseBodyObject(raw []byte) map[string]interface{} {
	body := map[string]interface{}{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	return body
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
	rule, _ := lookupQuotaRule(method, proxyTemplate)
	plan := &QuotaPlan{Action: rule.Action}

	switch rule.Action {
	case "consume":
		body := parseBodyObject(rawBody)
		if body == nil {
			break
		}
		amount := ResourceAmount{}
		switch rule.Resource {
		case "instance":
			if spec, ok := explicitInstanceSpec(body); ok {
				amount = spec
			} else if Truthy(body["flavor"]) {
				var herr *common.HTTPError
				if amount, herr = QueryFlavorAmount(ctx, region, body["flavor"], headers); herr != nil {
					return nil, herr
				}
			}
			if len(amount) > 0 {
				plan.Unit, plan.Count = amount, instanceCount(body)
				amount = scaleAmount(amount, plan.Count)
			}
		case "volume":
			if body["size"] != nil {
				amount = ResourceAmount{"disk_gb": floatOrZero(body["size"])}
			}
		case "floating_ip", "lb_floating_ip":
			plan.Unit, plan.Count = ResourceAmount{"public_ips": 1}, floatingIPCount(body)
			amount = scaleAmount(plan.Unit, plan.Count)
		case "vpc":
			amount = ResourceAmount{"vpcs": 1}
		case "load_balancer":
			amount = ResourceAmount{"load_balancers": 1}
		case "vpn_gateway":
			// The gateway and the public address it is created with
			amount = ResourceAmount{"vpn_gateways": 1, "public_ips": 1}
		case "image":
			// clapi makes every image a system admin creates public; public platform images are not charged
			if headers["X-System-Role"] != systemAdminRole {
				amount = ResourceAmount{"images": 1}
			}
		}
		if len(amount) > 0 {
			if herr := CheckAndReserve(orgID, region.ID, amount); herr != nil {
				return nil, herr
			}
			plan.Reserved, plan.Amount = true, amount
		}

	case "release":
		amount, herr := QueryResourceAmount(ctx, region, rule.Resource, strings.Trim(resolvedPath, "/"), headers)
		if herr != nil {
			return nil, herr
		}
		plan.Amount = amount

	case "resize":
		body := parseBodyObject(rawBody)
		if body == nil {
			break
		}
		apiPath := strings.TrimSuffix(strings.Trim(resolvedPath, "/"), "/resize")
		diff := ResourceAmount{}
		switch rule.Resource {
		case "instance":
			// clapi InstanceResizePayload: cpu (cores) and memory (MB), each optional; an omitted
			// value keeps the current size and the disk is never resized.
			cpu, memory := floatOrZero(body["cpu"]), floatOrZero(body["memory"])
			if cpu <= 0 && memory <= 0 {
				break
			}
			oldAmount, herr := QueryResourceAmount(ctx, region, rule.Resource, apiPath, headers)
			if herr != nil {
				return nil, herr
			}
			if len(oldAmount) == 0 {
				break // legacy instance without cpu/memory data, or another org's instance: not tracked
			}
			if cpu > 0 {
				diff["cpu_cores"] = cpu - oldAmount["cpu_cores"]
			}
			if memory > 0 {
				diff["ram_gb"] = memory/1024.0 - oldAmount["ram_gb"]
			}
		case "volume":
			// clapi VolumeResizePayload: size (GB), required.
			size := floatOrZero(body["size"])
			if size <= 0 {
				break
			}
			oldAmount, herr := QueryResourceAmount(ctx, region, rule.Resource, apiPath, headers)
			if herr != nil {
				return nil, herr
			}
			if len(oldAmount) == 0 {
				break // another org's volume: not tracked
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
	// 202: clapi accepted a deletion the node finishes later (a volume on its host); the few that fail there are
	// corrected by the next consumption sync
	if status == http.StatusOK || status == http.StatusCreated || status == http.StatusAccepted || status == http.StatusNoContent {
		switch {
		case plan.Action == "release" && len(plan.Amount) > 0:
			Release(orgID, regionID, plan.Amount)
		case plan.Action == "resize" && len(plan.Shrink) > 0:
			Release(orgID, regionID, plan.Shrink)
		case plan.Action == "consume" && plan.Reserved && len(plan.Unit) > 0:
			// clapi returns the created objects as an array; give back the reservation of any that were not created.
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
