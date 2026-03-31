package apis

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"api/src/services"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
	"github.com/spf13/viper"
)

type MonitorAPI struct{}

var monitorAPI = &MonitorAPI{}

type cacheItem struct {
	uuid       string
	instanceID int
}

var (
	cacheSize   = 10
	cacheExpire = 30 * time.Minute
	cacheList   = make([]cacheItem, 0, cacheSize)
	lastAccess  time.Time
	// Prometheus API path
	PrometheusQueryPath      = "/api/v1/query"
	PrometheusQueryRangePath = "/api/v1/query_range"

	// build full Prometheus URL
	PrometheusURL      string
	PrometheusRangeURL string

	prometheusIP   string
	prometheusPort int

	volemonitorIP    string
	volemonitorIPort int

	WdsPrometheusURL      string
	WdsPrometheusRangeURL string

	volemonitorUser    string
	volemonitorPasswd  string
	WDSLoginPath       string
	WDSAuthURL         string
	WDSVolumeDetailURL string
	// query range metrics
	rangeQueries = map[string]string{
		"cpu":              `100 * rate(libvirt_domain_info_cpu_time_seconds_total{domain=~"%s"}[2m]) / on(domain) libvirt_domain_info_virtual_cpus{domain=~"%s"}`,
		"memory_unused":    `libvirt_domain_memory_stats_unused_bytes{domain=~"%s"} / 1024`,
		"memory_total":     `libvirt_domain_info_maximum_memory_bytes{domain=~"%s"} / 1024`,
		"disk_read":        `rate(libvirt_domain_block_stats_read_bytes_total{domain=~"%s",target_device=~"%s"}[2m]) / 1024`,
		"disk_write":       `rate(libvirt_domain_block_stats_write_bytes_total{domain=~"%s",target_device=~"%s"}[2m]) / 1024`,
		"network_receive":  `rate(libvirt_domain_interface_stats_receive_bytes_total{domain=~"%s",target_device=~"%s"}[1m]) / 1024`,
		"network_transmit": `rate(libvirt_domain_interface_stats_transmit_bytes_total{domain=~"%s",target_device=~"%s"}[1m]) / 1024`,
		"traffic":          `increase(libvirt_domain_interface_stats_receive_bytes_total{domain=~"%s",target_device=~"%s"}[1h]) / 1024`, // ingress only, KB per hour
		"volume_read":      `expontech_tianshu_vol_op_bytes_persecond{mode='read',volName='%s'}`,
		"volume_write":     `expontech_tianshu_vol_op_bytes_persecond{mode='write',volName='%s'}`,
		"host_cpu":         `100 - (avg by (hostname) (rate(node_cpu_seconds_total{mode="idle",hostname=~"%s"}[2m])) * 100)`,
		"host_mem_total":   `node_memory_MemTotal_bytes{hostname=~"%s"} / 1024`,
		"host_mem_free":    `(node_memory_MemFree_bytes{hostname=~"%[1]s"} + node_memory_Buffers_bytes{hostname=~"%[1]s"} + node_memory_Cached_bytes{hostname=~"%[1]s"}) / 1024`,
	}
)

var (
	SwitchAPIEndpoint string
	SwitchAPIHouse    string
)

func init() {
	viper.SetConfigFile("conf/config.toml")
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("Failed to load configuration file %+v", err)
		prometheusIP = "localhost"
		prometheusPort = 9090
	} else {
		prometheusIP = viper.GetString("monitor.host")
		prometheusPort = viper.GetInt("monitor.port")
		logger.Info("prometheusIP: %s,  prometheusPort: %d", prometheusIP, prometheusPort)

		fmt.Printf("wngzhe prometheusIP: %s,  prometheusPort: %d", prometheusIP, prometheusPort)
		volemonitorIP = viper.GetString("WDS.host")
		volemonitorIPort = viper.GetInt("WDS.port")
		volemonitorUser = viper.GetString("WDS.admin")
		volemonitorPasswd = viper.GetString("WDS.password")
		logger.Info("volemonitorIP: %s,  volemonitorIPort: %d volemonitorUser: %s, volemonitorPasswd: %s",
			volemonitorIP, volemonitorIPort, volemonitorUser, volemonitorPasswd)

		SwitchAPIEndpoint = viper.GetString("switch_api.endpoint")
		SwitchAPIHouse = viper.GetString("switch_api.house")
		logger.Info("Switch API Endpoint: %s, House: %s", SwitchAPIEndpoint, SwitchAPIHouse)
	}
	if prometheusIP == "" {
		prometheusIP = "localhost"
	}
	if prometheusPort == 0 {
		prometheusPort = 9090
	}

	// init Prometheus URL
	PrometheusURL = fmt.Sprintf("http://%s:%d%s", prometheusIP, prometheusPort, PrometheusQueryPath)
	PrometheusRangeURL = fmt.Sprintf("http://%s:%d%s", prometheusIP, prometheusPort, PrometheusQueryRangePath)
	WdsPrometheusURL = fmt.Sprintf("http://%s:%d%s", volemonitorIP, volemonitorIPort, PrometheusQueryPath)
	WdsPrometheusRangeURL = fmt.Sprintf("http://%s:%d%s", volemonitorIP, volemonitorIPort, PrometheusQueryRangePath)
	WDSLoginPath = "/api/v1/login"
	WDSAuthURL = fmt.Sprintf("https://%s%s", volemonitorIP, WDSLoginPath)
	WDSVolumeDetailURL = fmt.Sprintf("https://%s/api/v2/block/volumes/%%s", volemonitorIP)
}

var (
	wdsToken    string
	wdsTokenExp time.Time
	tokenMutex  sync.Mutex
)

type PrometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]interface{}   `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

type MetricsRequest struct {
	Start    string   `json:"start" binding:"required"`
	End      string   `json:"end" binding:"required"`
	Step     string   `json:"step" binding:"required"`
	ID       []string `json:"id"`
	Hostname []string `json:"hostname"`
	Disk     []string `json:"disk"`
	Network  []string `json:"network"`
	VolName  []string `json:"volName"`
}

type NetworkMetricsRequest struct {
	Start        string   `json:"start" binding:"required"`
	End          string   `json:"end" binding:"required"`
	Step         string   `json:"step" binding:"required"`
	InterfaceIDs []string `json:"interface_ids" binding:"required,min=1"`
}

type WDSVolumeResponse struct {
	RetCode      string `json:"ret_code"`
	Message      string `json:"message"`
	VolumeDetail struct {
		VolumeName string `json:"volume_name"`
	} `json:"volume_detail"`
}

// 1. CPU monitor - single metric
type CPUResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Label      string `json:"label"` // "CPU Utilization Rate (%)"
		Unit       string `json:"unit"`  // "%"
		Result     []struct {
			Metric struct {
				Domain   string `json:"domain"`
				Instance string `json:"instance"`
				Job      string `json:"job"`
				UUID     string `json:"uuid,omitempty"`
			} `json:"metric"`
			Values []struct { // one-dimensional array
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// 2. memory monitor - double metrics (total and used)
type MemoryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string   `json:"resultType"`
		ChartType  string   `json:"chart_type"` // "bar"
		Label      []string `json:"label"`      // ["total(KB)", "used(KB)"]
		Unit       string   `json:"unit"`       // "KB"
		Result     []struct {
			Metric struct {
				Domain   string `json:"domain"`
				Instance string `json:"instance"`
				Job      string `json:"job"`
				UUID     string `json:"uuid,omitempty"`
			} `json:"metric"`
			Values [][]struct { // two-dimensional array [total, used]
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// 3. disk monitor - double metrics (read and write speed)
type DiskResponse struct {
	Status string `json:"status"`
	Data   struct {
		ChartType string   `json:"chart_type"` // "line"
		Label     []string `json:"label"`      // ["read(KB/s)", "write(KB/s)"]
		Unit      string   `json:"unit"`       // "KB/s"
		Result    []struct {
			Metric struct {
				Domain       string `json:"domain"`
				Instance     string `json:"instance"`
				Job          string `json:"job"`
				TargetDevice string `json:"target_device"`
			} `json:"metric"`
			Values [][]struct { // two-dimensional array [read speed, write speed]
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// 4. network monitor - double metrics (inbound and outbound bandwidth)
type NetworkResponse struct {
	Status string `json:"status"`
	Data   struct {
		ChartType  string   `json:"chart_type"` // "line"
		Label      []string `json:"label"`      // ["receive(KB/s)", "transmit(KB/s)"]
		Unit       string   `json:"unit"`       // "KB/s"
		ResultType string   `json:"resultType"`
		Result     []struct {
			Metric struct {
				Domain       string `json:"domain"`
				Instance     string `json:"instance"`
				Job          string `json:"job"`
				TargetDevice string `json:"target_device"`
				InterfaceID  string `json:"interface_id"`
			} `json:"metric"`
			Values [][]struct { // two-dimensional array [receive speed, transmit speed]
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// 5. traffic monitor - single metric
type TrafficResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Unit       string `json:"unit"` // "KB/hour"
		Result     []struct {
			Metric struct {
				Domain       string `json:"domain"`
				Instance     string `json:"instance"`
				Job          string `json:"job"`
				TargetDevice string `json:"target_device"`
			} `json:"metric"`
			Values []struct { // one-dimensional array
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// 6. volume monitor - single metric
type VolumeMonResponse struct {
	Status string `json:"status"`
	Data   struct {
		ChartType string   `json:"chart_type"`
		Label     []string `json:"label"`
		Unit      string   `json:"unit"`
		Result    []struct {
			Metric struct {
				VolName string `json:"volName"`
				Job     string `json:"job"`
			} `json:"metric"`
			Values [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func getInstanceIDFromCache(uuid string) (int, bool) {
	if time.Since(lastAccess) > cacheExpire {
		cacheList = cacheList[:0]
		return 0, false
	}

	for _, item := range cacheList {
		if item.uuid == uuid {
			lastAccess = time.Now()
			return item.instanceID, true
		}
	}
	return 0, false
}
func addToCache(uuid string, instanceID int) {
	if len(cacheList) >= cacheSize {
		cacheList = cacheList[1:]
	}

	cacheList = append(cacheList, cacheItem{uuid, instanceID})
	lastAccess = time.Now()
}

func parseUnixTime(timeStr string) (int64, error) {
	if timestamp, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		return timestamp, nil
	}
	return 0, fmt.Errorf("invalid time format: %s. Please use Unix timestamp (e.g., 1740382609)", timeStr)
}

const (
	QueryTypeInstant = "instant"
	QueryTypeRange   = "range"
)

func (api *MonitorAPI) getRangeQuery(metricType string, instanceIDs []string, deviceIDs []string) string {
	logger.Info("Building range query - metric type: %s, instance IDs: %v, device IDs: %v",
		metricType, instanceIDs, deviceIDs)

	if len(instanceIDs) == 0 {
		logger.Warning("No instance IDs provided")
		return ""
	}

	uuidFilter := strings.Join(instanceIDs, "|")
	logger.Info("UUID filter: %s", uuidFilter)

	query, ok := rangeQueries[metricType]
	if !ok {
		logger.Error("Unknown range metric type: %s", metricType)
		return ""
	}

	var finalQuery string
	if metricType == "disk_read" || metricType == "disk_write" ||
		metricType == "network_receive" || metricType == "network_transmit" || metricType == "traffic" || metricType == "cpu" {
		if (metricType != "cpu") && len(deviceIDs) == 0 {
			logger.Warning("No device IDs provided for %s metrics", metricType)
			return ""
		}
		var deviceFilter string
		if metricType == "cpu" {
			deviceFilter = uuidFilter
		} else {
			deviceFilter = strings.Join(deviceIDs, "|")
		}
		logger.Info("Device filter: %s", deviceFilter)
		finalQuery = fmt.Sprintf(query, uuidFilter, deviceFilter)
	} else {
		finalQuery = fmt.Sprintf(query, uuidFilter)
	}

	logger.Info("Generated query: %s", finalQuery)
	return finalQuery
}

// @Summary Get instance traffic metrics
// @Description Query traffic historical data for instances from Prometheus
// @Tags Monitoring
// @Accept json
// @Produce json
// @Param message body MetricsRequest true "Metrics query request"
// @Success 200 {array} map[string]interface{} "Traffic metrics data"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Router /metrics/instances/traffic/his_data [post]
func (api *MonitorAPI) GetTraffic(c *gin.Context) {
	var request MetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if len(request.ID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Instance ID is required"})
		return
	}

	if len(request.Network) == 0 {
		logger.Warning("Network interface not provided in request")
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Network interface is required"})
		return
	}

	if len(request.ID) != len(request.Network) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "The number of instance IDs and network interfaces must be the same"})
		return
	}

	var instanceIDs []string
	for _, uuid := range request.ID {
		logger.Debug("Attempting to convert UUID: %s\n", uuid)
		if instanceID, ok := getInstanceIDFromCache(uuid); ok {
			instanceIDs = append(instanceIDs, "inst-"+strconv.Itoa(instanceID))
			continue
		}
		instanceID, err := services.GetDBIndexByInstanceUUID(c, uuid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			logger.Errorf("failed to get instance: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			return
		}
		logger.Debug("Successfully converted UUID %s to instanceID %d\n", uuid, instanceID)
		instanceIDs = append(instanceIDs, "inst-"+strconv.Itoa(instanceID))
		addToCache(uuid, instanceID)
	}

	// validate time params
	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	var allResults []interface{}
	for i := range instanceIDs {
		convertedID := instanceIDs[i]
		networkDevice := request.Network[i]

		// generate query for the instance
		query := api.getRangeQuery("traffic", []string{convertedID}, []string{networkDevice})
		if query == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid metric type"})
			return
		}

		// execute Prometheus query
		result, err := queryPrometheus(PrometheusRangeURL, query, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
		if err != nil {
			logger.Error("Failed to query traffic for %s (%s): %v", convertedID, networkDevice, err)
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
			return
		}

		// format return result
		formattedResult := formatResponse(result, "traffic")
		if trafficResult, ok := formattedResult.(*TrafficResponse); ok {
			for i := range trafficResult.Data.Result {
				trafficResult.Data.Result[i].Metric = struct {
					Domain       string `json:"domain"`
					Instance     string `json:"instance"`
					Job          string `json:"job"`
					TargetDevice string `json:"target_device"`
				}{
					Domain:       trafficResult.Data.Result[i].Metric.Domain,
					Instance:     trafficResult.Data.Result[i].Metric.Instance,
					Job:          trafficResult.Data.Result[i].Metric.Job,
					TargetDevice: trafficResult.Data.Result[i].Metric.TargetDevice,
				}
			}
		}
		allResults = append(allResults, formattedResult)
	}
	c.JSON(http.StatusOK, allResults)
}

// @Summary Get instance CPU metrics
// @Description Query CPU historical data for instances from Prometheus
// @Tags Monitoring
// @Accept json
// @Produce json
// @Param message body MetricsRequest true "Metrics query request"
// @Success 200 {object} map[string]interface{} "CPU metrics data"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Router /metrics/instances/cpu/his_data [post]
func (api *MonitorAPI) GetCPU(c *gin.Context) {
	var request MetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if len(request.ID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Instance ID is required"})
		return
	}

	// convert UUID to index ID, build reverse domain→uuid map
	var instanceIDs []string
	domainToUUID := make(map[string]string)
	for _, uuid := range request.ID {
		logger.Debug("Attempting to convert UUID: %s\n", uuid)
		if instanceID, ok := getInstanceIDFromCache(uuid); ok {
			domain := "inst-" + strconv.Itoa(instanceID)
			instanceIDs = append(instanceIDs, domain)
			domainToUUID[domain] = uuid
			continue
		}
		instanceID, err := services.GetDBIndexByInstanceUUID(c, uuid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			logger.Errorf("failed to get instance: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			return
		}
		logger.Debug("Successfully converted UUID %s to instanceID %d\n", uuid, instanceID)
		domain := "inst-" + strconv.Itoa(instanceID)
		instanceIDs = append(instanceIDs, domain)
		domainToUUID[domain] = uuid
		addToCache(uuid, instanceID)
	}

	// validate time params
	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	// build query
	query := api.getRangeQuery("cpu", instanceIDs, nil)
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid metric type"})
		return
	}

	// execute query
	result, err := queryPrometheus(PrometheusRangeURL, query, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		logger.Error("Failed to query CPU: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}

	// format result and fill uuid
	formattedResult := formatResponse(result, "cpu")
	if formattedResult != nil {
		if cpuResp, ok := formattedResult.(*CPUResponse); ok {
			for i := range cpuResp.Data.Result {
				domain := cpuResp.Data.Result[i].Metric.Domain
				cpuResp.Data.Result[i].Metric = struct {
					Domain   string `json:"domain"`
					Instance string `json:"instance"`
					Job      string `json:"job"`
					UUID     string `json:"uuid,omitempty"`
				}{
					Domain:   domain,
					Instance: cpuResp.Data.Result[i].Metric.Instance,
					Job:      cpuResp.Data.Result[i].Metric.Job,
					UUID:     domainToUUID[domain],
				}
			}
		}
	}
	c.JSON(http.StatusOK, formattedResult)
}

// @Summary Get instance memory metrics
// @Description Query memory historical data for instances from Prometheus
// @Tags Monitoring
// @Accept json
// @Produce json
// @Param message body MetricsRequest true "Metrics query request"
// @Success 200 {object} map[string]interface{} "Memory metrics data"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Router /metrics/instances/memory/his_data [post]
func (api *MonitorAPI) GetMemory(c *gin.Context) {
	var request MetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if len(request.ID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Instance ID is required"})
		return
	}

	var instanceIDs []string
	domainToUUID := make(map[string]string)
	for _, uuid := range request.ID {
		logger.Debug("Attempting to convert UUID: %s\n", uuid)
		if instanceID, ok := getInstanceIDFromCache(uuid); ok {
			domain := "inst-" + strconv.Itoa(instanceID)
			instanceIDs = append(instanceIDs, domain)
			domainToUUID[domain] = uuid
			continue
		}
		instanceID, err := services.GetDBIndexByInstanceUUID(c, uuid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			logger.Errorf("failed to get instance: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			return
		}
		logger.Debug("Successfully converted UUID %s to instanceID %d\n", uuid, instanceID)
		domain := "inst-" + strconv.Itoa(instanceID)
		instanceIDs = append(instanceIDs, domain)
		domainToUUID[domain] = uuid
		addToCache(uuid, instanceID)
	}

	// validate time params
	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	// build memory unused and total query
	unusedQuery := api.getRangeQuery("memory_unused", instanceIDs, nil)
	totalQuery := api.getRangeQuery("memory_total", instanceIDs, nil)
	if unusedQuery == "" || totalQuery == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid metric type"})
		return
	}

	// execute query
	unusedResult, err := queryPrometheus(PrometheusRangeURL, unusedQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		logger.Error("Failed to query memory unused: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}

	totalResult, err := queryPrometheus(PrometheusRangeURL, totalQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		logger.Error("Failed to query memory total: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}

	// merge results and fill uuid
	result := mergeMemoryResults(unusedResult, totalResult)
	for i := range result.Data.Result {
		domain := result.Data.Result[i].Metric.Domain
		result.Data.Result[i].Metric = struct {
			Domain   string `json:"domain"`
			Instance string `json:"instance"`
			Job      string `json:"job"`
			UUID     string `json:"uuid,omitempty"`
		}{
			Domain:   domain,
			Instance: result.Data.Result[i].Metric.Instance,
			Job:      result.Data.Result[i].Metric.Job,
			UUID:     domainToUUID[domain],
		}
	}
	c.JSON(http.StatusOK, result)
}

// @Summary Get instance disk metrics
// @Description Query disk read/write historical data for instances from Prometheus
// @Tags Monitoring
// @Accept json
// @Produce json
// @Param message body MetricsRequest true "Metrics query request"
// @Success 200 {object} map[string]interface{} "Disk metrics data"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Router /metrics/instances/disk/his_data [post]
func (api *MonitorAPI) GetDisk(c *gin.Context) {
	var request MetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if len(request.ID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Instance ID is required"})
		return
	}

	if len(request.Disk) == 0 {
		logger.Warning("Disk ID not provided in request")
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Disk ID is required"})
		return
	}

	var instanceIDs []string
	for _, uuid := range request.ID {
		logger.Debug("Attempting to convert UUID: %s\n", uuid)
		if instanceID, ok := getInstanceIDFromCache(uuid); ok {
			instanceIDs = append(instanceIDs, "inst-"+strconv.Itoa(instanceID))
			continue
		}
		instanceID, err := services.GetDBIndexByInstanceUUID(c, uuid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			logger.Errorf("failed to get instance: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			return
		}
		logger.Debug("Successfully converted UUID %s to instanceID %d\n", uuid, instanceID)
		instanceIDs = append(instanceIDs, "inst-"+strconv.Itoa(instanceID))
		addToCache(uuid, instanceID)
	}

	// validate time params
	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	// build read and write query
	readQuery := api.getRangeQuery("disk_read", instanceIDs, request.Disk)
	writeQuery := api.getRangeQuery("disk_write", instanceIDs, request.Disk)
	logger.Debug("Read Query:", readQuery)
	logger.Debug("Write Query:", writeQuery)
	if readQuery == "" || writeQuery == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid metric type"})
		return
	}

	// execute query
	readResult, err := queryPrometheus(PrometheusRangeURL, readQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		logger.Error("Failed to query disk read metrics: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}

	writeResult, err := queryPrometheus(PrometheusRangeURL, writeQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		logger.Error("Failed to query disk write metrics: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}

	// merge results
	result := mergeDiskResults(readResult, writeResult)
	for i := range result.Data.Result {
		result.Data.Result[i].Metric = struct {
			Domain       string `json:"domain"`
			Instance     string `json:"instance"`
			Job          string `json:"job"`
			TargetDevice string `json:"target_device"`
		}{
			Domain:       result.Data.Result[i].Metric.Domain,
			Instance:     result.Data.Result[i].Metric.Instance,
			Job:          result.Data.Result[i].Metric.Job,
			TargetDevice: result.Data.Result[i].Metric.TargetDevice,
		}
	}
	c.JSON(http.StatusOK, result)
}

// GetHyperCPU returns host CPU metrics
func (api *MonitorAPI) GetHyperCPU(c *gin.Context) {
	var request MetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if len(request.Hostname) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Hostname is required"})
		return
	}

	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	hostnameFilter := strings.Join(request.Hostname, "|")
	query := fmt.Sprintf(rangeQueries["host_cpu"], hostnameFilter)

	result, err := queryPrometheus(PrometheusRangeURL, query, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}

	c.JSON(http.StatusOK, formatResponse(result, "cpu"))
}

// GetHyperMemory returns host memory metrics
func (api *MonitorAPI) GetHyperMemory(c *gin.Context) {
	var request MetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if len(request.Hostname) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Hostname is required"})
		return
	}

	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	hostnameFilter := strings.Join(request.Hostname, "|")
	totalQuery := fmt.Sprintf(rangeQueries["host_mem_total"], hostnameFilter)
	freeQuery := fmt.Sprintf(rangeQueries["host_mem_free"], hostnameFilter)

	totalResult, err := queryPrometheus(PrometheusRangeURL, totalQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil || totalResult.Status != "success" {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query total memory"})
		return
	}

	freeResult, err := queryPrometheus(PrometheusRangeURL, freeQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil || freeResult.Status != "success" {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query free memory"})
		return
	}

	c.JSON(http.StatusOK, mergeMemoryResults(freeResult, totalResult))
}

func getWDSToken() (string, error) {
	tokenMutex.Lock()
	defer tokenMutex.Unlock()
	if time.Now().Before(wdsTokenExp) && wdsToken != "" {
		logger.Info("Using cached token")
		return wdsToken, nil
	}

	authBody := fmt.Sprintf(`{"name":"%s","password":"%s"}`, volemonitorUser, volemonitorPasswd)
	req, err := http.NewRequest("POST", WDSAuthURL, strings.NewReader(authBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("WDS auth request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		wdsToken = ""
		wdsTokenExp = time.Now().Add(-1 * time.Hour)
		return "", fmt.Errorf("token expired, will retry")
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("WDS auth failed with status: %d", resp.StatusCode)
	}

	var authResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResponse); err != nil {
		return "", fmt.Errorf("failed to decode auth response: %v", err)
	}
	if authResponse.AccessToken == "" {
		return "", errors.New("empty access_token in auth response")
	}
	wdsToken = authResponse.AccessToken
	if authResponse.ExpiresIn > 0 {
		wdsTokenExp = time.Now().Add(time.Duration(authResponse.ExpiresIn) * time.Second)
	} else {
		wdsTokenExp = time.Now().Add(5 * time.Minute)
	}
	logger.Debug("token expires at: %s\n", wdsTokenExp.Format(time.RFC3339))
	return authResponse.AccessToken, nil
}

func convertVolNames(volIDs []string, token string) []string {
	var names []string

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Skip certificate verification
			},
		},
	}

	for _, volID := range volIDs {
		detailURL := fmt.Sprintf(WDSVolumeDetailURL, volID)
		req, _ := http.NewRequest("GET", detailURL, nil)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		resp, err := client.Do(req)
		if err != nil {
			logger.Error("Failed to get volume detail for %s: %v", volID, err)
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			logger.Error("Received 401 for volume %s, invalidating token", volID)
			wdsToken = ""
			wdsTokenExp = time.Now().Add(-1 * time.Hour)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var wdsResp WDSVolumeResponse
		if err := json.Unmarshal(body, &wdsResp); err != nil {
			logger.Warning("Failed to decode JSON for %s: %v\nRaw JSON: %s", volID, err, string(body))
			continue
		}

		if wdsResp.RetCode == "0" && wdsResp.VolumeDetail.VolumeName != "" {
			names = append(names, wdsResp.VolumeDetail.VolumeName)
		} else {
			logger.Error("Invalid response for volume %s: ret_code=%s, message=%s",
				volID, wdsResp.RetCode, wdsResp.Message)
		}
	}
	return names
}

func GetLastUUIDFromVolumeUUID(ctx context.Context, volumeUUID string) (string, error) {
	volumeAdmin := &services.VolumeAdmin{}
	volume, err := volumeAdmin.GetVolumeByUUID(ctx, volumeUUID)
	if err != nil {
		return "", err
	}

	return volume.GetOriginVolumeID(), nil
}

// @Summary Get volume metrics
// @Description Query volume read/write historical data from WDS storage monitoring
// @Tags Monitoring
// @Accept json
// @Produce json
// @Param message body MetricsRequest true "Metrics query request"
// @Success 200 {object} map[string]interface{} "Volume metrics data"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /metrics/instances/volume/his_data [post]
func (api *MonitorAPI) GetVolume(c *gin.Context) {
	// check volemonitorIP and volemonitorIPort
	if volemonitorIP == "" || volemonitorIPort == 0 || volemonitorUser == "" || volemonitorPasswd == "" {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Incomplete WDS configuration",
			"details": map[string]interface{}{
				"volemonitorIP":     volemonitorIP,
				"volemonitorIPort":  volemonitorIPort,
				"volemonitorUser":   volemonitorUser,
				"volemonitorPasswd": strings.Repeat("*", len(volemonitorPasswd)),
			},
		})
		return
	}
	var request MetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if len(request.VolName) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Volume name is required"})
		return
	}

	// validate time params
	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	var lastUUIDs []string
	for _, volUUID := range request.VolName {
		lastUUID, err := GetLastUUIDFromVolumeUUID(c.Request.Context(), volUUID)
		if err != nil {
			logger.Errorf("Failed to convert volume UUID: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"status":  "error",
				"message": "Invalid volume UUID format",
			})
			return
		}
		lastUUIDs = append(lastUUIDs, lastUUID)
	}
	token, err := getWDSToken()
	if err != nil {
		logger.Errorf("WDS authentication failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "get wds token failed",
		})
		return
	}
	actualVolNames := convertVolNames(lastUUIDs, token)
	if len(actualVolNames) == 0 {
		logger.Error("All volume conversions failed")
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "no valid volume founded"})
		return
	}
	// build read and write query
	readQuery := fmt.Sprintf(rangeQueries["volume_read"], strings.Join(actualVolNames, "|"))
	writeQuery := fmt.Sprintf(rangeQueries["volume_write"], strings.Join(actualVolNames, "|"))
	if readQuery == "" || writeQuery == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid metric type"})
		return
	}

	// execute query
	readResult, err := queryPrometheus(WdsPrometheusRangeURL, readQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		logger.Error("Failed to query disk read metrics: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}

	writeResult, err := queryPrometheus(WdsPrometheusRangeURL, writeQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
	if err != nil {
		logger.Error("Failed to query disk write metrics: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to query metrics"})
		return
	}
	// merge results
	result := mergeVolumeResults(readResult, writeResult, request.VolName, actualVolNames)
	c.JSON(http.StatusOK, result)
}

func mergeVolumeResults(readRes, writeRes *PrometheusResponse, originalVolNames []string, convertedVolNames []string) *VolumeMonResponse {
	response := &VolumeMonResponse{
		Status: "success",
		Data: struct {
			ChartType string   `json:"chart_type"`
			Label     []string `json:"label"`
			Unit      string   `json:"unit"`
			Result    []struct {
				Metric struct {
					VolName string `json:"volName"`
					Job     string `json:"job"`
				} `json:"metric"`
				Values [][]struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			} `json:"result"`
		}{
			ChartType: "line",
			Label:     []string{"read (KB/s)", "write (KB/s)"},
			Unit:      "KB/s",
		},
	}

	volMapping := make(map[string]string)
	for i := range originalVolNames {
		volMapping[originalVolNames[i]] = convertedVolNames[i]
		volMapping[convertedVolNames[i]] = originalVolNames[i]
	}

	for _, convertedVol := range convertedVolNames {
		originalVol := volMapping[convertedVol]
		entry := struct {
			Metric struct {
				VolName string `json:"volName"`
				Job     string `json:"job"`
			} `json:"metric"`
			Values [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		}{
			Metric: struct {
				VolName string `json:"volName"`
				Job     string `json:"job"`
			}{
				VolName: originalVol, // Preserve original volume identifier format
				Job:     "tianshu",
			},
		}

		var readValues, writeValues []struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}
		for _, series := range readRes.Data.Result {
			if vol, ok := series.Metric["volName"]; ok && vol == convertedVol {
				for _, point := range series.Values {
					if len(point) != 2 {
						continue
					}
					timestamp, _ := strconv.ParseFloat(fmt.Sprintf("%v", point[0]), 64)
					value, _ := strconv.ParseFloat(fmt.Sprintf("%v", point[1]), 64)
					readValues = append(readValues, struct {
						Time  string `json:"time"`
						Value string `json:"value"`
					}{
						Time:  fmt.Sprintf("%d", int64(timestamp)),
						Value: fmt.Sprintf("%.2f", value/1024),
					})
				}
			}
		}

		for _, series := range writeRes.Data.Result {
			if vol, ok := series.Metric["volName"]; ok && vol == convertedVol {
				for _, point := range series.Values {
					if len(point) != 2 {
						continue
					}
					timestamp, _ := strconv.ParseFloat(fmt.Sprintf("%v", point[0]), 64)
					value, _ := strconv.ParseFloat(fmt.Sprintf("%v", point[1]), 64)
					writeValues = append(writeValues, struct {
						Time  string `json:"time"`
						Value string `json:"value"`
					}{
						Time:  fmt.Sprintf("%d", int64(timestamp)),
						Value: fmt.Sprintf("%.2f", value/1024),
					})
				}
			}
		}

		mergedValues := make([][]struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}, 2)

		sort.Slice(readValues, func(i, j int) bool {
			ti, _ := strconv.ParseInt(readValues[i].Time, 10, 64)
			tj, _ := strconv.ParseInt(readValues[j].Time, 10, 64)
			return ti < tj
		})

		sort.Slice(writeValues, func(i, j int) bool {
			ti, _ := strconv.ParseInt(writeValues[i].Time, 10, 64)
			tj, _ := strconv.ParseInt(writeValues[j].Time, 10, 64)
			return ti < tj
		})

		mergedValues[0] = readValues
		mergedValues[1] = writeValues

		entry.Values = mergedValues
		response.Data.Result = append(response.Data.Result, entry)
	}

	return response
}

// @Summary Get instance network metrics
// @Description Query network receive/transmit historical data for instances from Prometheus
// @Tags Monitoring
// @Accept json
// @Produce json
// @Param message body MetricsRequest true "Metrics query request"
// @Success 200 {object} map[string]interface{} "Network metrics data"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Router /metrics/instances/network/his_data [post]
func (api *MonitorAPI) GetNetwork(c *gin.Context) {
	var request NetworkMetricsRequest
	if err := c.BindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	start, end, err := validateAndParseTimeParams(request.Start, request.End, request.Step)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	// batch fetch all interfaces, skip any not found (don't fail the whole request)
	ifaces, err := services.InterfaceAdmin.GetInterfacesByUUIDs(c, request.InterfaceIDs)
	if err != nil {
		logger.Errorf("failed to batch fetch interfaces: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to fetch interfaces"})
		return
	}

	// build tap→ifaceUUID map, group tap names by instance domain
	type ifaceInfo struct {
		tapName     string
		ifaceUUID   string
		domainName  string
	}
	var validIfaces []ifaceInfo

	for _, iface := range ifaces {
		if iface.Instance == 0 {
			logger.Warningf("skipping interface %s: not attached to an instance", iface.UUID)
			continue
		}
		mac := strings.ReplaceAll(iface.MacAddr, ":", "")
		if len(mac) < 6 {
			logger.Warningf("skipping interface %s: invalid MAC %s", iface.UUID, iface.MacAddr)
			continue
		}
		tapName := "tap" + mac[len(mac)-6:]
		domainName := "inst-" + strconv.Itoa(int(iface.Instance))

		validIfaces = append(validIfaces, ifaceInfo{
			tapName:    tapName,
			ifaceUUID:  iface.UUID,
			domainName: domainName,
		})
	}

	if len(validIfaces) == 0 {
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	// group by domain name so each instance only needs 2 Prometheus queries
	type instanceGroup struct {
		domainName string
		ifaces     []ifaceInfo
	}
	domainMap := map[string]*instanceGroup{}
	domainOrder := []string{}
	for _, fi := range validIfaces {
		if _, exists := domainMap[fi.domainName]; !exists {
			domainMap[fi.domainName] = &instanceGroup{domainName: fi.domainName}
			domainOrder = append(domainOrder, fi.domainName)
		}
		domainMap[fi.domainName].ifaces = append(domainMap[fi.domainName].ifaces, fi)
	}

	// tap→ifaceUUID reverse map for result enrichment
	tapToIfaceUUID := map[string]string{}
	for _, fi := range validIfaces {
		tapToIfaceUUID[fi.tapName] = fi.ifaceUUID
	}

	type queryResult struct {
		domainName     string
		receiveResult  *PrometheusResponse
		transmitResult *PrometheusResponse
		err            error
	}

	resultCh := make(chan queryResult, len(domainOrder))

	// concurrently query Prometheus per instance (2 queries each)
	var wg sync.WaitGroup
	for _, dn := range domainOrder {
		grp := domainMap[dn]
		wg.Add(1)
		go func(grp *instanceGroup) {
			defer wg.Done()
			tapNames := make([]string, len(grp.ifaces))
			for i, fi := range grp.ifaces {
				tapNames[i] = fi.tapName
			}
			receiveQuery := api.getRangeQuery("network_receive", []string{grp.domainName}, tapNames)
			transmitQuery := api.getRangeQuery("network_transmit", []string{grp.domainName}, tapNames)

			recv, err := queryPrometheus(PrometheusRangeURL, receiveQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
			if err != nil {
				resultCh <- queryResult{domainName: grp.domainName, err: err}
				return
			}
			trans, err := queryPrometheus(PrometheusRangeURL, transmitQuery, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end), request.Step)
			if err != nil {
				resultCh <- queryResult{domainName: grp.domainName, err: err}
				return
			}
			resultCh <- queryResult{domainName: grp.domainName, receiveResult: recv, transmitResult: trans}
		}(grp)
	}
	// close channel in a separate goroutine so range can drain concurrently,
	// avoiding deadlock if goroutines block on a full channel before wg.Wait returns
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// collect results, split per interface
	// key: ifaceUUID → NetworkResponse
	ifaceResults := map[string]*NetworkResponse{}
	for qr := range resultCh {
		if qr.err != nil {
			logger.Errorf("prometheus query failed for %s: %v", qr.domainName, qr.err)
			continue // skip failed instances, don't abort
		}
		merged := mergeNetworkResults(qr.receiveResult, qr.transmitResult)
		for i := range merged.Data.Result {
			td := merged.Data.Result[i].Metric.TargetDevice
			ifaceUUID, ok := tapToIfaceUUID[td]
			if !ok {
				continue
			}
			merged.Data.Result[i].Metric.InterfaceID = ifaceUUID
			// wrap each interface as its own NetworkResponse
			single := &NetworkResponse{Status: merged.Status}
			single.Data.ChartType = merged.Data.ChartType
			single.Data.Label = merged.Data.Label
			single.Data.Unit = merged.Data.Unit
			single.Data.ResultType = merged.Data.ResultType
			single.Data.Result = append(single.Data.Result, merged.Data.Result[i])
			ifaceResults[ifaceUUID] = single
		}
	}

	// return in the same order as requested interface_ids so frontend can rely on it
	allResults := make([]interface{}, 0, len(request.InterfaceIDs))
	for _, uuid := range request.InterfaceIDs {
		if r, ok := ifaceResults[uuid]; ok {
			allResults = append(allResults, r)
		}
		// interfaces with no data are simply omitted
	}

	c.JSON(http.StatusOK, allResults)
}

func validateTimeParams(start, end int64, step string) error {
	if start >= end {
		return fmt.Errorf("start time must be less than end time")
	}
	duration, err := time.ParseDuration(step)
	if err != nil {
		return fmt.Errorf("invalid step format: %s", err)
	}
	if duration < time.Second {
		return fmt.Errorf("step must be at least 1 second")
	}
	return nil
}

func validateAndParseTimeParams(startStr, endStr, step string) (int64, int64, error) {
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start time: %s", err)
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end time: %s", err)
	}
	if err := validateTimeParams(start, end, step); err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func queryPrometheus(baseURL, query string, start, end, step string) (*PrometheusResponse, error) {
	// record query params
	logger.Info("Prometheus url: %s query: %s, start: %s, end: %s, step: %s", baseURL, query, start, end, step)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("query", query)
	q.Add("start", start)
	q.Add("end", end)
	q.Add("step", step)
	req.URL.RawQuery = q.Encode()

	// record full request URL
	logger.Info("Prometheus request URL: %s", req.URL.String())

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var prometheusResp PrometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&prometheusResp); err != nil {
		return nil, err
	}

	return &prometheusResp, nil
}

// merge memory query results
func mergeMemoryResults(unused, total *PrometheusResponse) *MemoryResponse {
	var memResp MemoryResponse
	memResp.Status = "success"
	memResp.Data.ResultType = "matrix"
	memResp.Data.ChartType = "bar"
	memResp.Data.Label = []string{"Total(KB)", "Used(KB)"}
	memResp.Data.Unit = "KB"

	// Use map to match results with the same domain
	resultMap := make(map[string]struct {
		metric struct {
			Domain   string
			Instance string
			Job      string
		}
		unusedValues [][]interface{}
		totalValues  [][]interface{}
	})

	// Collect unused data
	for _, r := range unused.Data.Result {
		domain, ok := r.Metric["domain"]
		if !ok || domain == "" {
			domain = r.Metric["hostname"]
		}
		resultMap[domain] = struct {
			metric struct {
				Domain   string
				Instance string
				Job      string
			}
			unusedValues [][]interface{}
			totalValues  [][]interface{}
		}{
			metric: struct {
				Domain   string
				Instance string
				Job      string
			}{
				Domain:   domain,
				Instance: r.Metric["instance"],
				Job:      r.Metric["job"],
			},
			unusedValues: r.Values,
		}
	}

	// Merge total data
	for _, r := range total.Data.Result {
		domain, ok := r.Metric["domain"]
		if !ok || domain == "" {
			domain = r.Metric["hostname"]
		}
		if item, exists := resultMap[domain]; exists {
			item.totalValues = r.Values
			resultMap[domain] = item
		}
	}

	// Build final result
	for _, item := range resultMap {
		var result struct {
			Metric struct {
				Domain   string `json:"domain"`
				Instance string `json:"instance"`
				Job      string `json:"job"`
				UUID     string `json:"uuid,omitempty"`
			} `json:"metric"`
			Values [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		}

		result.Metric = struct {
			Domain   string `json:"domain"`
			Instance string `json:"instance"`
			Job      string `json:"job"`
			UUID     string `json:"uuid,omitempty"`
		}{
			Domain:   item.metric.Domain,
			Instance: item.metric.Instance,
			Job:      item.metric.Job,
		}

		// Process total and unused values to calculate used memory
		var totalValues, usedValues []struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}

		for i := 0; i < len(item.totalValues) && i < len(item.unusedValues); i++ {
			totalTime := item.totalValues[i][0].(float64)
			totalVal, _ := strconv.ParseFloat(item.totalValues[i][1].(string), 64)
			unusedVal, _ := strconv.ParseFloat(item.unusedValues[i][1].(string), 64)
			usedVal := totalVal - unusedVal

			totalValues = append(totalValues, struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{
				Time:  fmt.Sprintf("%d", int64(totalTime)),
				Value: fmt.Sprintf("%.2f", totalVal),
			})

			usedValues = append(usedValues, struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{
				Time:  fmt.Sprintf("%d", int64(totalTime)),
				Value: fmt.Sprintf("%.2f", usedVal),
			})
		}

		result.Values = [][]struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}{totalValues, usedValues}

		memResp.Data.Result = append(memResp.Data.Result, result)
	}

	return &memResp
}

func mergeDiskResults(readRes, writeRes *PrometheusResponse) *DiskResponse {
	response := &DiskResponse{
		Status: "success",
		Data: struct {
			ChartType string   `json:"chart_type"`
			Label     []string `json:"label"`
			Unit      string   `json:"unit"`
			Result    []struct {
				Metric struct {
					Domain       string `json:"domain"`
					Instance     string `json:"instance"`
					Job          string `json:"job"`
					TargetDevice string `json:"target_device"`
				} `json:"metric"`
				Values [][]struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			} `json:"result"`
		}{
			ChartType: "line",
			Label:     []string{"read (KB/s)", "write (KB/s)"},
			Unit:      "KB/s",
		},
	}

	// 修复点1：遍历所有结果项
	for i := 0; i < len(readRes.Data.Result); i++ {
		entry := struct {
			Metric struct {
				Domain       string `json:"domain"`
				Instance     string `json:"instance"`
				Job          string `json:"job"`
				TargetDevice string `json:"target_device"`
			} `json:"metric"`
			Values [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		}{
			Metric: struct {
				Domain       string `json:"domain"`
				Instance     string `json:"instance"`
				Job          string `json:"job"`
				TargetDevice string `json:"target_device"`
			}{
				Domain:       readRes.Data.Result[i].Metric["domain"],
				Instance:     readRes.Data.Result[i].Metric["instance"],
				Job:          readRes.Data.Result[i].Metric["job"],
				TargetDevice: readRes.Data.Result[i].Metric["target_device"],
			},
		}

		for _, v := range readRes.Data.Result[i].Values {
			if len(v) < 2 {
				continue
			}
			value, _ := strconv.ParseFloat(v[1].(string), 64)
			entry.Values = append(entry.Values, []struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{
				{
					Time:  fmt.Sprintf("%d", int64(v[0].(float64))),
					Value: fmt.Sprintf("%.2f", value/1024),
				},
			})
		}

		if len(writeRes.Data.Result) > i {
			for j, v := range writeRes.Data.Result[i].Values {
				if j >= len(entry.Values) {
					entry.Values = append(entry.Values, make([]struct {
						Time  string `json:"time"`
						Value string `json:"value"`
					}, 0))
				}
				value, _ := strconv.ParseFloat(v[1].(string), 64)
				if len(entry.Values[j]) == 0 {
					entry.Values[j] = append(entry.Values[j], struct {
						Time  string `json:"time"`
						Value string `json:"value"`
					}{
						Time: fmt.Sprintf("%d", int64(v[0].(float64))),
					})
				}
				entry.Values[j] = append(entry.Values[j], struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				}{
					Value: fmt.Sprintf("%.2f", value/1024),
				})
			}
		}
		response.Data.Result = append(response.Data.Result, entry)
	}

	return response
}

// merge network query results
func mergeNetworkResults(receive, transmit *PrometheusResponse) *NetworkResponse {
	var netResp NetworkResponse
	netResp.Status = "success"
	netResp.Data.ChartType = "line"
	netResp.Data.Label = []string{"Receive Speed (KB/s)", "Transmit Speed (KB/s)"}
	netResp.Data.Unit = "KB/s"
	netResp.Data.ResultType = receive.Data.ResultType

	// use map to match results with the same UUID
	resultMap := make(map[string]struct {
		metric struct {
			Domain       string
			Instance     string
			Job          string
			TargetDevice string
		}
		receiveValues  [][]interface{}
		transmitValues [][]interface{}
	})

	// collect receive data
	for _, r := range receive.Data.Result {
		key := r.Metric["target_device"]
		resultMap[key] = struct {
			metric struct {
				Domain       string
				Instance     string
				Job          string
				TargetDevice string
			}
			receiveValues  [][]interface{}
			transmitValues [][]interface{}
		}{
			metric: struct {
				Domain       string
				Instance     string
				Job          string
				TargetDevice string
			}{
				Domain:       r.Metric["domain"],
				Instance:     r.Metric["instance"],
				Job:          r.Metric["job"],
				TargetDevice: r.Metric["target_device"],
			},
			receiveValues: r.Values,
		}
	}

	// merge transmit data
	for _, r := range transmit.Data.Result {
		key := r.Metric["target_device"]
		if item, exists := resultMap[key]; exists {
			item.transmitValues = r.Values
			resultMap[key] = item
		}
	}

	// build final result
	for _, item := range resultMap {
		var result struct {
			Metric struct {
				Domain       string `json:"domain"`
				Instance     string `json:"instance"`
				Job          string `json:"job"`
				TargetDevice string `json:"target_device"`
				InterfaceID  string `json:"interface_id"`
			} `json:"metric"`
			Values [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			} `json:"values"`
		}

		result.Metric = struct {
			Domain       string `json:"domain"`
			Instance     string `json:"instance"`
			Job          string `json:"job"`
			TargetDevice string `json:"target_device"`
			InterfaceID  string `json:"interface_id"`
		}{
			Domain:       item.metric.Domain,
			Instance:     item.metric.Instance,
			Job:          item.metric.Job,
			TargetDevice: item.metric.TargetDevice,
		}

		// process receive data
		var receiveValues []struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}
		for _, v := range item.receiveValues {
			timestamp := v[0].(float64)
			value := v[1].(string)
			timeStr := fmt.Sprintf("%d", int64(timestamp))

			receiveValues = append(receiveValues, struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{
				Time:  timeStr,
				Value: value,
			})
		}

		// process transmit data
		var transmitValues []struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}
		for _, v := range item.transmitValues {
			timestamp := v[0].(float64)
			value := v[1].(string)
			timeStr := fmt.Sprintf("%d", int64(timestamp))

			transmitValues = append(transmitValues, struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{
				Time:  timeStr,
				Value: value,
			})
		}

		result.Values = [][]struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}{receiveValues, transmitValues}

		netResp.Data.Result = append(netResp.Data.Result, result)
	}

	return &netResp
}

// add response format conversion function
func formatResponse(resp *PrometheusResponse, metricType string) interface{} {
	switch metricType {
	case "cpu":
		// CPU monitoring - single metric, use one-dimensional array
		var cpuResp CPUResponse
		cpuResp.Status = resp.Status
		cpuResp.Data.ResultType = resp.Data.ResultType
		cpuResp.Data.Label = "CPU Utilization Rate (%)"
		cpuResp.Data.Unit = "%"

		for _, r := range resp.Data.Result {
			var result struct {
				Metric struct {
					Domain   string `json:"domain"`
					Instance string `json:"instance"`
					Job      string `json:"job"`
					UUID     string `json:"uuid,omitempty"`
				} `json:"metric"`
				Values []struct { // one-dimensional array
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			}

			result.Metric.Domain = r.Metric["domain"]
			if result.Metric.Domain == "" {
				result.Metric.Domain = r.Metric["hostname"]
			}
			result.Metric.Instance = r.Metric["instance"]
			result.Metric.Job = r.Metric["job"]

			var values []struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}

			for _, v := range r.Values {
				timestamp := v[0].(float64)
				value := v[1].(string)
				timeStr := fmt.Sprintf("%d", int64(timestamp))
				values = append(values, struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				}{
					Time:  timeStr,
					Value: value,
				})
			}

			result.Values = values // directly assign one-dimensional array
			cpuResp.Data.Result = append(cpuResp.Data.Result, result)
		}
		return &cpuResp

	case "disk_read":
		var diskResp DiskResponse
		diskResp.Status = resp.Status
		diskResp.Data.ChartType = "line"
		diskResp.Data.Label = []string{"Read Speed (KB/s)", "Write Speed (KB/s)"}
		diskResp.Data.Unit = "KB/s"

		for _, r := range resp.Data.Result {
			var result struct {
				Metric struct {
					Domain       string `json:"domain"`
					Instance     string `json:"instance"`
					Job          string `json:"job"`
					TargetDevice string `json:"target_device"`
				} `json:"metric"`
				Values [][]struct { // two-dimensional array
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			}

			result.Metric.Domain = r.Metric["domain"]
			result.Metric.Instance = r.Metric["instance"]
			result.Metric.Job = r.Metric["job"]
			result.Metric.TargetDevice = r.Metric["target_device"]

			var readValues []struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}

			for _, v := range r.Values {
				timestamp := v[0].(float64)
				value := v[1].(string)
				timeStr := fmt.Sprintf("%d", int64(timestamp))
				readValues = append(readValues, struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				}{
					Time:  timeStr,
					Value: value,
				})
			}

			result.Values = [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{readValues} // wrap as two-dimensional array
			diskResp.Data.Result = append(diskResp.Data.Result, result)
		}
		return diskResp

	case "disk_write":
		var diskResp DiskResponse
		diskResp.Status = resp.Status
		diskResp.Data.ChartType = "line"
		diskResp.Data.Label = []string{"read (KB/s)", "write (KB/s)"}
		diskResp.Data.Unit = "KB/s"

		for _, r := range resp.Data.Result {
			var result struct {
				Metric struct {
					Domain       string `json:"domain"`
					Instance     string `json:"instance"`
					Job          string `json:"job"`
					TargetDevice string `json:"target_device"`
				} `json:"metric"`
				Values [][]struct { // two-dimensional array
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			}

			result.Metric.Domain = r.Metric["domain"]
			result.Metric.Instance = r.Metric["instance"]
			result.Metric.Job = r.Metric["job"]
			result.Metric.TargetDevice = r.Metric["target_device"]

			var writeValues []struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}

			for _, v := range r.Values {
				timestamp := v[0].(float64)
				value := v[1].(string)
				timeStr := fmt.Sprintf("%d", int64(timestamp))
				writeValues = append(writeValues, struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				}{
					Time:  timeStr,
					Value: value,
				})
			}

			result.Values = [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{writeValues} // wrap as two-dimensional array
			diskResp.Data.Result = append(diskResp.Data.Result, result)
		}
		return diskResp

	case "network_receive":
		var networkResp NetworkResponse
		networkResp.Status = resp.Status
		networkResp.Data.ChartType = "line"
		networkResp.Data.Label = []string{"receive (KB/s)", "transmit (KB/s)"}
		networkResp.Data.Unit = "KB/s"
		networkResp.Data.ResultType = resp.Data.ResultType

		for _, r := range resp.Data.Result {
			var result struct {
				Metric struct {
					Domain       string `json:"domain"`
					Instance     string `json:"instance"`
					Job          string `json:"job"`
					TargetDevice string `json:"target_device"`
					InterfaceID  string `json:"interface_id"`
				} `json:"metric"`
				Values [][]struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			}

			result.Metric.Domain = r.Metric["domain"]
			result.Metric.Instance = r.Metric["instance"]
			result.Metric.Job = r.Metric["job"]
			result.Metric.TargetDevice = r.Metric["target_device"]

			var receiveValues []struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}

			for _, v := range r.Values {
				timestamp := v[0].(float64)
				value := v[1].(string)
				timeStr := fmt.Sprintf("%d", int64(timestamp))
				receiveValues = append(receiveValues, struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				}{
					Time:  timeStr,
					Value: value,
				})
			}

			result.Values = [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{receiveValues}
			networkResp.Data.Result = append(networkResp.Data.Result, result)
		}
		return networkResp

	case "network_transmit":
		var networkResp NetworkResponse
		networkResp.Status = resp.Status
		networkResp.Data.ChartType = "line"
		networkResp.Data.Label = []string{"receive (KB/s)", "transmit (KB/s)"}
		networkResp.Data.Unit = "KB/s"
		networkResp.Data.ResultType = resp.Data.ResultType

		for _, r := range resp.Data.Result {
			var result struct {
				Metric struct {
					Domain       string `json:"domain"`
					Instance     string `json:"instance"`
					Job          string `json:"job"`
					TargetDevice string `json:"target_device"`
					InterfaceID  string `json:"interface_id"`
				} `json:"metric"`
				Values [][]struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			}

			result.Metric.Domain = r.Metric["domain"]
			result.Metric.Instance = r.Metric["instance"]
			result.Metric.Job = r.Metric["job"]
			result.Metric.TargetDevice = r.Metric["target_device"]

			var transmitValues []struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}

			for _, v := range r.Values {
				timestamp := v[0].(float64)
				value := v[1].(string)
				timeStr := fmt.Sprintf("%d", int64(timestamp))
				transmitValues = append(transmitValues, struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				}{
					Time:  timeStr,
					Value: value,
				})
			}

			result.Values = [][]struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}{transmitValues}
			networkResp.Data.Result = append(networkResp.Data.Result, result)
		}
		return networkResp

	case "traffic":
		// traffic monitoring - single metric, use one-dimensional array
		var trafficResp TrafficResponse
		trafficResp.Status = resp.Status
		trafficResp.Data.ResultType = resp.Data.ResultType
		trafficResp.Data.Unit = "KB/hour"

		for _, r := range resp.Data.Result {
			var result struct {
				Metric struct {
					Domain       string `json:"domain"`
					Instance     string `json:"instance"`
					Job          string `json:"job"`
					TargetDevice string `json:"target_device"`
				} `json:"metric"`
				Values []struct { // one-dimensional array
					Time  string `json:"time"`
					Value string `json:"value"`
				} `json:"values"`
			}

			result.Metric.Domain = r.Metric["domain"]
			result.Metric.Instance = r.Metric["instance"]
			result.Metric.Job = r.Metric["job"]
			result.Metric.TargetDevice = r.Metric["target_device"]

			var values []struct {
				Time  string `json:"time"`
				Value string `json:"value"`
			}

			for _, v := range r.Values {
				timestamp := v[0].(float64)
				value := v[1].(string)
				timeStr := fmt.Sprintf("%d", int64(timestamp))
				values = append(values, struct {
					Time  string `json:"time"`
					Value string `json:"value"`
				}{
					Time:  timeStr,
					Value: value,
				})
			}

			result.Values = values // directly assign one-dimensional array
			trafficResp.Data.Result = append(trafficResp.Data.Result, result)
		}
		return trafficResp

	default:
		logger.Error("Unknown metric type: %s", metricType)
		return nil
	}
}
