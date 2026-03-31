package services

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"
	"api/src/common"
	"api/src/model"

	"github.com/google/uuid"
	"github.com/spf13/viper"

	"github.com/jinzhu/gorm"
)

const (
	RuleTypeCPU            = "cpu"
	RuleTypeMemory         = "memory"
	RuleTypeBW             = "bw"
	RuleTypeCompute        = "compute_node"
	RuleTypeControl        = "control_node"
	RuleTypeAvailable      = "node_available"
	RuleTypeHypervisorVCPU = "hypervisor_vcpu"
	RuleTypePacketDrop     = "packet_drop"
	RuleTypeIPBlock        = "ip_block"
	RulesEnabled           = "/etc/prometheus/rules_enabled"
	RulesGeneral           = "/etc/prometheus/general_rules"
	RulesSpecial           = "/etc/prometheus/special_rules"
	RulesNode              = "/etc/prometheus/node_rules"
	RuleTemplate           = "/etc/prometheus/node_templates"
)

var (
	alarmPrometheusIP      string
	alarmPrometheusPort    int
	alarmPrometheusSSHPort int
	isRemotePrometheus     bool
	sshKeyPath             string
	prometheusClient       *PrometheusClient
	alarmAdminInstance     = &AlarmAdmin{}
)

type PrometheusClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

type RuleFileRequest struct {
	Operation string `json:"operation"`
	FileUser  string `json:"file_user"`
	Content   string `json:"content"`
	FilePath  string `json:"file_path"`
	LinkPath  string `json:"link_path"`
}

type RuleFileResponse struct {
	Success bool   `json:"success"`
	Exists  bool   `json:"exists"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
}

type ListRuleGroupsParams struct {
	RuleType  string
	Page      int
	PageSize  int
	GroupUUID string
	RuleID    string // Added: Support query by rule_id
}

// AdaptiveQueryParams Adaptive query parameters
type AdaptiveQueryParams struct {
	ID       string // Can be GroupUUID or RuleID
	RuleType string
	Page     int
	PageSize int
}

type (
	VMRuleLink struct {
		ID        uint      `gorm:"primaryKey;autoIncrement"`
		GroupUUID string    `gorm:"column:group_uuid;type:varchar(36);index;not null"`
		VMName    string    `gorm:"type:varchar(255);index;not null"`
		CreatedAt time.Time `gorm:"autoCreateTime"`
	}

	RuleGroupV2 struct {
		ID         string    `gorm:"primaryKey;type:varchar(36)"`
		Name       string    `gorm:"index;size:255"`
		Type       string    `gorm:"type:varchar(10);index"` // cpu/bw/memory/disk/network-in/network-out
		Enabled    bool      `gorm:"default:true"`
		Owner      string    `gorm:"type:varchar(255);index"`
		CreatedAt  time.Time `gorm:"autoCreateTime"`
		TriggerCnt int       `gorm:"default:0"`
		UpdatedAt  time.Time
	}

	CPURule struct {
		ID           int       `gorm:"primaryKey;autoIncrement"`
		GroupUUID    string    `gorm:"column:group_uuid;type:varchar(36);index"`
		Name         string    `json:"name" gorm:"size:255"`
		Limit        int       `json:"limit" gorm:"column:limit;check:limit >= 1"` // Threshold value
		Rule         string    `json:"rule" gorm:"type:varchar(8);column:rule"`    // Comparison operator: gt/lt
		Duration     int       `json:"duration" gorm:"check:duration >= 1"`        // Duration in minutes
		Over         int       `json:"over" gorm:"check:over >= 1"`
		DownTo       int       `json:"down_to" gorm:"check:down_to >= 0"`
		DownDuration int       `json:"down_duration" gorm:"check:down_duration >= 1"`
		Level        string    `json:"level" binding:"required,oneof=critical warning info"`
		CreatedAt    time.Time `gorm:"autoCreateTime"`
	}

	MemoryRule struct {
		ID           int       `gorm:"primaryKey;autoIncrement"`
		GroupUUID    string    `gorm:"column:group_uuid;type:varchar(36);index"`
		Name         string    `json:"name" gorm:"size:255"`
		Limit        int       `json:"limit" gorm:"column:limit;check:limit >= 1"` // Threshold value
		Rule         string    `json:"rule" gorm:"type:varchar(8);column:rule"`    // Comparison operator: gt/lt
		Duration     int       `json:"duration" gorm:"check:duration >= 1"`        // Duration in minutes
		Over         int       `json:"over" gorm:"check:over >= 1"`
		DownTo       int       `json:"down_to" gorm:"check:down_to >= 0"`
		DownDuration int       `json:"down_duration" gorm:"check:down_duration >= 1"`
		Level        string    `json:"level" binding:"required,oneof=critical warning info"`
		CreatedAt    time.Time `gorm:"autoCreateTime"`
	}

	BWRule struct {
		ID        uint   `gorm:"primaryKey;autoIncrement"`
		GroupUUID string `gorm:"column:group_uuid;type:varchar(36);index"`
		Name      string `gorm:"size:255"`

		InEnabled      bool   `gorm:"default:false"`
		InThreshold    int    `gorm:"check:in_threshold >= 0"`
		InDuration     int    `gorm:"check:in_duration >= 0"`
		InOverType     string `gorm:"type:varchar(20);default:'absolute'"`
		InDownTo       int    `gorm:"default:0"`
		InDownDuration int    `gorm:"default:0"`

		OutEnabled      bool   `gorm:"default:false"`
		OutThreshold    int    `gorm:"check:out_threshold >= 0"`
		OutDuration     int    `gorm:"check:out_duration >= 0"`
		OutOverType     string `gorm:"type:varchar(20);default:'absolute'"`
		OutDownTo       int    `gorm:"default:0"`
		OutDownDuration int    `gorm:"default:0"`

		CreatedAt time.Time `gorm:"autoCreateTime"`
	}
	Alert struct {
		ID            uint   `gorm:"primaryKey;autoIncrement"`
		Name          string `gorm:"size:255"`
		Status        string `gorm:"type:varchar(20)"`
		RuleGroupUUID string `json:"rule_group"`
		GlobalRuleID  string `gorm:"type:varchar(255)" json:"global_rule_id"` // Added: Global rule ID
		Severity      string `gorm:"type:varchar(20)"`
		Summary       string `gorm:"type:text"`
		Description   string `gorm:"type:text"`
		StartsAt      time.Time
		EndsAt        time.Time
		CreatedAt     time.Time `gorm:"autoCreateTime"`
		AlertType     string    `gorm:"type:varchar(20)" json:"alert_type"`
		TargetDevice  string    `gorm:"type:varchar(255)" json:"target_device"`
		RegionID      string    `gorm:"type:varchar(255)" json:"region_id"`   // Added: Region ID
		InstanceID    string    `gorm:"type:varchar(255)" json:"instance_id"` // Added: Instance ID
	}
)

// NodeAvailabilityConfig Complete configuration structure
type NodeAvailabilityConfig struct {
	NodeDownDuration     string `json:"node_down_duration"`
	AlertDurationMinutes int    `json:"alert_duration_minutes"`
}

type ManagementConfig struct {
	// CPU monitoring
	CPUUsageThreshold int    `json:"cpu_usage_threshold"`
	CPUAlertDuration  string `json:"cpu_alert_duration"`
	CPUAlertMinutes   int    `json:"cpu_alert_minutes"`

	// Memory monitoring
	MemoryUsageThreshold int    `json:"memory_usage_threshold"`
	MemoryAlertDuration  string `json:"memory_alert_duration"`
	MemoryAlertMinutes   int    `json:"memory_alert_minutes"`

	// Disk monitoring
	DiskSpaceThreshold int    `json:"disk_space_threshold"`
	DiskAlertDuration  string `json:"disk_alert_duration"`
	DiskAlertMinutes   int    `json:"disk_alert_minutes"`

	// Network monitoring - Added
	NetworkTrafficThresholdGB float64 `json:"network_traffic_threshold_gb"`
	NetworkAlertDuration      string  `json:"network_alert_duration"`
	NetworkAlertMinutes       int     `json:"network_alert_minutes"`
}

type ComputeConfig struct {
	// CPU monitoring
	CPUUsageThreshold int    `json:"cpu_usage_threshold"`
	CPUAlertDuration  string `json:"cpu_alert_duration"`
	CPUAlertMinutes   int    `json:"cpu_alert_minutes"`

	// Memory monitoring - Added
	MemoryUsageThreshold int    `json:"memory_usage_threshold"`
	MemoryAlertDuration  string `json:"memory_alert_duration"`
	MemoryAlertMinutes   int    `json:"memory_alert_minutes"`

	// Disk monitoring
	DiskSpaceThreshold int    `json:"disk_space_threshold"`
	DiskAlertDuration  string `json:"disk_alert_duration"`
	DiskAlertMinutes   int    `json:"disk_alert_minutes"`

	// Core network monitoring
	NetworkTrafficThresholdGB float64 `json:"network_traffic_threshold_gb"`
	NetworkAlertDuration      string  `json:"network_alert_duration"`
	NetworkAlertMinutes       int     `json:"network_alert_minutes"`

	// Multiple business type network monitoring
	NetworkTypes map[string]NetworkTypeConfig `json:"network_types"`
}

type NetworkTypeConfig struct {
	Threshold float64 `json:"threshold"`
	Pattern   string  `json:"pattern"`
	Duration  string  `json:"duration"`
}

type AlarmOperator struct {
	DB *gorm.DB
}

type AlarmAdmin struct{}

func init() {
	logger.Info("ENTER init: loading monitor config")
	defer logger.Info("EXIT init")
	viper.SetConfigFile("conf/config.toml")
	if err := viper.ReadInConfig(); err == nil {
		alarmPrometheusIP = viper.GetString("monitor.host")
		alarmPrometheusPort = viper.GetInt("monitor.port")
		alarmPrometheusSSHPort = viper.GetInt("monitor.sshport")
		sshKeyPath = viper.GetString("monitor.sshkey")
	}
	if alarmPrometheusPort == 0 {
		alarmPrometheusPort = 9090
	}
	if alarmPrometheusSSHPort == 0 {
		alarmPrometheusSSHPort = 22
	}
	if sshKeyPath == "" {
		sshKeyPath = "~/workspace/.ssh/cland.key"
	}
	isRemotePrometheus = !isLocalIP(alarmPrometheusIP)
	if !isRemotePrometheus || alarmPrometheusIP == "" {
		alarmPrometheusIP = "localhost"
	}
	if isRemotePrometheus {
		baseURL := fmt.Sprintf("https://%s:%d", alarmPrometheusIP, 8256)
		certFile := "/etc/ssl/certs/alarm_rules_manager.crt"
		client, err := AlertRUleClient(baseURL, certFile, "")
		if err != nil {
			logger.Errorf("Failed to initialize the Prometheus client.: %v", err)
		} else {
			prometheusClient = client
			logger.Infof("Prometheus client initialized successfully with URL: %s", baseURL)
		}
	}
	logger.Infof("Prometheus: IP=%s, port=%d, SSHport=%d, remote_mode=%v",
		alarmPrometheusIP, alarmPrometheusPort, alarmPrometheusSSHPort, isRemotePrometheus)
}

func GetPrometheusIP() string {
	logger.Info("ENTER GetPrometheusIP")
	defer func() {
		logger.Infof("EXIT GetPrometheusIP: ip=%s", alarmPrometheusIP)
	}()
	return alarmPrometheusIP
}

func GetPrometheusPort() int {
	logger.Info("ENTER GetPrometheusPort")
	defer func() {
		logger.Infof("EXIT GetPrometheusPort: port=%d", alarmPrometheusPort)
	}()
	return alarmPrometheusPort
}

func GetPrometheusSSHPort() int {
	logger.Info("ENTER GetPrometheusSSHPort")
	defer func() {
		logger.Infof("EXIT GetPrometheusSSHPort: port=%d", alarmPrometheusSSHPort)
	}()
	return alarmPrometheusSSHPort
}

func IsRemotePrometheus() bool {
	logger.Info("ENTER IsRemotePrometheus")
	defer func() {
		logger.Infof("EXIT IsRemotePrometheus: isRemote=%v", isRemotePrometheus)
	}()
	return isRemotePrometheus
}

func (a *AlarmOperator) GetCPURulesByGroupID(ctx context.Context, groupUUID string, rules *[]model.CPURuleDetail) (err error) {
	logger.Infof("ENTER AlarmOperator.GetCPURulesByGroupID: groupUUID=%s", groupUUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetCPURulesByGroupID: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetCPURulesByGroupID: rulesCount=%d", len(*rules))
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	return db.Where("group_uuid = ?", groupUUID).Find(rules).Error
}

func (a *AlarmOperator) GetRulesByGroupUUID(ctx context.Context, groupUUID string) (rg *model.RuleGroupV2, err error) {
	logger.Infof("ENTER AlarmOperator.GetRulesByGroupUUID: groupUUID=%s", groupUUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetRulesByGroupUUID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.GetRulesByGroupUUID: success")
		}
	}()
	ctx, _ = common.GetContextDB(ctx)
	groups, _, err := a.ListRuleGroups(ctx, ListRuleGroupsParams{
		GroupUUID: groupUUID,
		PageSize:  1,
	})
	if err != nil {
		logger.Errorf("rules query failed: groupID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("rules query failed: %v", err)
	}

	if len(groups) == 0 {
		logger.Errorf("rule not found: groupID=%s", groupUUID)
		return nil, gorm.ErrRecordNotFound
	}

	ruleType := groups[0].Type

	switch ruleType {
	case "cpu":
		details, err := a.GetCPURuleDetails(ctx, groupUUID)
		if err != nil {
			logger.Errorf("detail rules query failed: groupID=%s, error=%v", groupUUID, err)
			return nil, fmt.Errorf("detail rules query failed: %w", err)
		}
		type ResultGroup struct {
			model.RuleGroupV2
			Details []model.CPURuleDetail `gorm:"-"`
		}
		result := &ResultGroup{
			RuleGroupV2: groups[0],
			Details:     details,
		}
		return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
	case "bw":
		details, err := a.GetBWRuleDetails(ctx, groupUUID)
		if err != nil {
			logger.Errorf("detail rules query failed: groupID=%s, error=%v", groupUUID, err)
			return nil, fmt.Errorf("detail rules query failed: %w", err)
		}
		type ResultGroup struct {
			model.RuleGroupV2
			Details []model.BWRuleDetail `gorm:"-"`
		}
		result := &ResultGroup{
			RuleGroupV2: groups[0],
			Details:     details,
		}
		return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
	default:
		logger.Errorf("unsupported rule type groupID %s type %s", groupUUID, ruleType)
		return nil, fmt.Errorf("unsupported rule type: %s", ruleType)
	}
}

// GetRulesByRuleID 通过 rule_id 获取规则组（支持告警规则）
func (a *AlarmOperator) GetRulesByRuleID(ctx context.Context, ruleID string) (rg *model.RuleGroupV2, err error) {
	logger.Infof("ENTER AlarmOperator.GetRulesByRuleID: ruleID=%s", ruleID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetRulesByRuleID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.GetRulesByRuleID: success")
		}
	}()
	ctx, _ = common.GetContextDB(ctx)
	groups, _, err := a.ListRuleGroups(ctx, ListRuleGroupsParams{
		RuleID:   ruleID,
		PageSize: 1,
	})
	if err != nil {
		logger.Errorf("rules query failed: ruleID=%s, error=%v", ruleID, err)
		return nil, fmt.Errorf("rules query failed: %v", err)
	}

	if len(groups) == 0 {
		logger.Errorf("rule not found: ruleID=%s", ruleID)
		return nil, gorm.ErrRecordNotFound
	}

	ruleType := groups[0].Type

	switch ruleType {
	case "cpu":
		details, err := a.GetCPURuleDetails(ctx, groups[0].UUID)
		if err != nil {
			logger.Errorf("detail rules query failed: ruleID=%s, error=%v", ruleID, err)
			return nil, fmt.Errorf("detail rules query failed: %w", err)
		}
		type ResultGroup struct {
			model.RuleGroupV2
			Details []model.CPURuleDetail `gorm:"-"`
		}
		result := &ResultGroup{
			RuleGroupV2: groups[0],
			Details:     details,
		}
		return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
	case "bw":
		details, err := a.GetBWRuleDetails(ctx, groups[0].UUID)
		if err != nil {
			logger.Errorf("detail rules query failed: ruleID=%s, error=%v", ruleID, err)
			return nil, fmt.Errorf("detail rules query failed: %w", err)
		}
		type ResultGroup struct {
			model.RuleGroupV2
			Details []model.BWRuleDetail `gorm:"-"`
		}
		result := &ResultGroup{
			RuleGroupV2: groups[0],
			Details:     details,
		}
		return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
	case "memory":
		details, err := a.GetMemoryRuleDetails(ctx, groups[0].UUID)
		if err != nil {
			logger.Errorf("detail rules query failed: ruleID=%s, error=%v", ruleID, err)
			return nil, fmt.Errorf("detail rules query failed: %w", err)
		}
		type ResultGroup struct {
			model.RuleGroupV2
			Details []model.MemoryRuleDetail `gorm:"-"`
		}
		result := &ResultGroup{
			RuleGroupV2: groups[0],
			Details:     details,
		}
		return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
	default:
		logger.Errorf("unsupported rule type ruleID %s type %s", ruleID, ruleType)
		return nil, fmt.Errorf("unsupported rule type: %s", ruleType)
	}
}

func (a *AlarmOperator) GetCPURulesByGroupUUID(ctx context.Context, groupUUID string, ruleType string) (rg *model.RuleGroupV2, err error) {
	logger.Infof("ENTER AlarmOperator.GetCPURulesByGroupUUID: groupUUID=%s, ruleType=%s", groupUUID, ruleType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetCPURulesByGroupUUID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.GetCPURulesByGroupUUID: success")
		}
	}()
	groups, _, err := a.ListRuleGroups(ctx, ListRuleGroupsParams{
		RuleType:  ruleType,
		GroupUUID: groupUUID,
		PageSize:  1,
	})
	if err != nil || len(groups) == 0 {
		logger.Errorf("rules query failed: groupID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("rules query failed: %w", err)
	}

	details, err := a.GetCPURuleDetails(ctx, groupUUID)
	if err != nil {
		logger.Errorf("detail rules query failed: groupID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("detail rules query failed: %w", err)
	}
	type ResultGroup struct {
		model.RuleGroupV2
		Details []model.CPURuleDetail `gorm:"-"`
	}
	result := &ResultGroup{
		RuleGroupV2: groups[0],
		Details:     details,
	}
	return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
}
func (a *AlarmOperator) GetMemoryRulesByGroupUUID(ctx context.Context, groupUUID string, ruleType string) (rg *model.RuleGroupV2, err error) {
	logger.Infof("ENTER AlarmOperator.GetMemoryRulesByGroupUUID: groupUUID=%s, ruleType=%s", groupUUID, ruleType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetMemoryRulesByGroupUUID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.GetMemoryRulesByGroupUUID: success")
		}
	}()
	groups, _, err := a.ListRuleGroups(ctx, ListRuleGroupsParams{
		RuleType:  ruleType,
		GroupUUID: groupUUID,
		PageSize:  1,
	})
	if err != nil || len(groups) == 0 {
		logger.Errorf("rules query failed: groupID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("rules query failed: %w", err)
	}

	details, err := a.GetMemoryRuleDetails(ctx, groupUUID)
	if err != nil {
		logger.Errorf("detail rules query failed: groupID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("detail rules query failed: %w", err)
	}
	type ResultGroup struct {
		model.RuleGroupV2
		Details []model.MemoryRuleDetail `gorm:"-"`
	}
	result := &ResultGroup{
		RuleGroupV2: groups[0],
		Details:     details,
	}
	return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
}

func (a *AlarmOperator) GetBWRulesByGroupUUID(ctx context.Context, groupUUID string, ruleType string) (rg *model.RuleGroupV2, err error) {
	logger.Infof("ENTER AlarmOperator.GetBWRulesByGroupUUID: groupUUID=%s, ruleType=%s", groupUUID, ruleType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetBWRulesByGroupUUID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.GetBWRulesByGroupUUID: success")
		}
	}()
	groups, _, err := a.ListRuleGroups(ctx, ListRuleGroupsParams{
		RuleType:  ruleType,
		GroupUUID: groupUUID,
		PageSize:  1,
	})
	if err != nil || len(groups) == 0 {
		logger.Errorf("rules query failed: groupID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("rules query failed: %w", err)
	}

	details, err := a.GetBWRuleDetails(ctx, groupUUID)
	if err != nil {
		logger.Errorf("detail rules query failed: groupID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("detail rules query failed: %w", err)
	}
	type ResultGroup struct {
		model.RuleGroupV2
		Details []model.BWRuleDetail `gorm:"-"`
	}
	result := &ResultGroup{
		RuleGroupV2: groups[0],
		Details:     details,
	}
	return (*model.RuleGroupV2)(unsafe.Pointer(result)), nil
}

func (a *AlarmOperator) UpdateRuleGroupStatus(ctx context.Context, groupID string, enabled bool) (err error) {
	logger.Infof("ENTER AlarmOperator.UpdateRuleGroupStatus: groupID=%s, enabled=%v", groupID, enabled)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.UpdateRuleGroupStatus: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.UpdateRuleGroupStatus: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.RuleGroupV2{}).
			Where("uuid = ?", groupID).
			Update("enabled", enabled)
		if result.Error != nil {
			logger.Errorf("update satus failed groupID %s error %v", groupID, result.Error)
			return fmt.Errorf("update satus failed: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("group rules no found")
		}
		return nil
	})
}

// CheckVMLinkExists checks if a VM link already exists
func (a *AlarmOperator) CheckVMLinkExists(ctx context.Context, groupUUID, vmUUID, iface string) bool {
	ctx, db := common.GetContextDB(ctx)
	var count int64
	query := db.Model(&model.VMRuleLink{}).
		Where("group_uuid = ? AND vm_uuid = ?", groupUUID, vmUUID)

	if iface != "" {
		query = query.Where("interface = ?", iface)
	}

	query.Count(&count)
	return count > 0
}

// CreateVMLink creates a single VM link
func (a *AlarmOperator) CreateVMLink(ctx context.Context, groupUUID, vmUUID, iface string) (err error) {
	logger.Infof("ENTER AlarmOperator.CreateVMLink: groupUUID=%s, vmUUID=%s, iface=%s", groupUUID, vmUUID, iface)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.CreateVMLink: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.CreateVMLink: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	link := &model.VMRuleLink{
		GroupUUID: groupUUID,
		VMUUID:    vmUUID,
		Interface: iface,
	}
	if err = db.Create(link).Error; err != nil {
		logger.Errorf("create link failed: GroupUUID=%s, vmUUID=%s, interface=%s, error=%v",
			groupUUID, vmUUID, iface, err)
		return fmt.Errorf("create link failed: %w", err)
	}
	return nil
}

func (a *AlarmOperator) BatchLinkVMs(ctx context.Context, GroupUUID string, vmUUIDs []string, iface string) error {
	logger.Infof("ENTER AlarmOperator.BatchLinkVMs: GroupUUID=%s, vmUUIDs=%v, iface=%s", GroupUUID, vmUUIDs, iface)
	defer logger.Info("EXIT AlarmOperator.BatchLinkVMs")
	ctx, db := common.GetContextDB(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		for _, vmUUID := range vmUUIDs {
			var count int64
			tx.Model(&model.VMRuleLink{}).
				Where("group_uuid = ? AND vm_uuid = ? AND interface = ?", GroupUUID, vmUUID, iface).
				Count(&count)

			if count == 0 {
				link := &model.VMRuleLink{
					GroupUUID: GroupUUID,
					VMUUID:    vmUUID,
					Interface: iface,
				}
				if err := tx.Create(link).Error; err != nil {
					logger.Errorf("create link failed: GroupUUID=%s, vmUUID=%s, interface=%s, error=%v",
						GroupUUID, vmUUID, iface, err)
					return fmt.Errorf("create link failed: %w", err)
				}
			} else {
				logger.Infof("link already exists, skipping: GroupUUID=%s, vmUUID=%s, interface=%s",
					GroupUUID, vmUUID, iface)
			}
		}
		return nil
	})
}

func (a *AlarmOperator) DeleteRuleGroup(ctx context.Context, groupUUID, ruleType string) (err error) {
	logger.Infof("ENTER AlarmOperator.DeleteRuleGroup: groupUUID=%s, ruleType=%s", groupUUID, ruleType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.DeleteRuleGroup: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.DeleteRuleGroup: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	result := db.Where("uuid = ? AND type = ?", groupUUID, ruleType).
		Delete(&model.RuleGroupV2{})
	if result.Error != nil {
		logger.Errorf("delete rule failed: groupUUID=%s, type=%s, error=%v",
			groupUUID, ruleType, result.Error)
	}
	return result.Error
}

func (a *AlarmOperator) DeleteVMLink(ctx context.Context, groupUUID, vmUUID, iface string) (rowsAffected int64, err error) {
	logger.Infof("ENTER AlarmOperator.DeleteVMLink: groupUUID=%s, vmUUID=%s, iface=%s", groupUUID, vmUUID, iface)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.DeleteVMLink: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.DeleteVMLink: rowsAffected=%d", rowsAffected)
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	query := db.Where("group_uuid = ? AND vm_uuid = ?", groupUUID, vmUUID)

	if iface != "" {
		query = query.Where("interface = ?", iface)
	}

	result := query.Delete(&model.VMRuleLink{})
	if result.Error != nil {
		logger.Errorf("delete link failed groupUUID %s vmUUID %s interface %s error %v", groupUUID, vmUUID, iface, result.Error)
	}
	return result.RowsAffected, result.Error
}

func (a *AlarmOperator) GetLinkedVMs(ctx context.Context, groupUUID string) (links []model.VMRuleLink, err error) {
	logger.Infof("ENTER AlarmOperator.GetLinkedVMs: groupUUID=%s", groupUUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetLinkedVMs: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetLinkedVMs: found %d links", len(links))
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	query := db.Model(&model.VMRuleLink{})

	if groupUUID != "" {
		query = query.Where("group_uuid = ?", groupUUID)
	} else {
		logger.Info("query all group found, TBD")
	}

	if err := query.Find(&links).Error; err != nil {
		logger.Errorf("get link data failed: groupUUID=%s, error=%v", groupUUID, err)
		return nil, err
	}
	return links, nil
}

// GetRuleIDsByInstance retrieves all rule IDs associated with a single instance
// This includes both alarm rules (rule_group_v2) and adjust rules (adjust_rule_group)
// Input: instanceUUID - single instance UUID to query
// Output: []string - list of rule_id values
func (a *AlarmOperator) GetRuleIDsByInstance(ctx context.Context, instanceUUID string) (ruleIDs []string, err error) {
	logger.Infof("ENTER AlarmOperator.GetRuleIDsByInstance: instanceUUID=%s", instanceUUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetRuleIDsByInstance: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetRuleIDsByInstance: found %d rule IDs", len(ruleIDs))
		}
	}()

	if instanceUUID == "" {
		return []string{}, nil
	}

	ctx, db := common.GetContextDB(ctx)
	ruleIDs = make([]string, 0)

	// Query alarm rules from rule_group_v2
	type RuleIDResult struct {
		RuleID string
	}
	var alarmRuleIDs []RuleIDResult
	err = db.Table("vm_rule_links").
		Select("DISTINCT rule_group_v2.rule_id").
		Joins("JOIN rule_group_v2 ON vm_rule_links.group_uuid = rule_group_v2.uuid").
		Where("vm_rule_links.vm_uuid = ? AND vm_rule_links.deleted_at IS NULL", instanceUUID).
		Scan(&alarmRuleIDs).Error

	if err != nil {
		logger.Errorf("[GetRuleIDsByInstance] Failed to query alarm rules for instance %s: %v", instanceUUID, err)
		return nil, fmt.Errorf("failed to query alarm rules: %w", err)
	}

	for _, r := range alarmRuleIDs {
		ruleIDs = append(ruleIDs, r.RuleID)
	}

	// Query adjust rules from adjust_rule_group
	var adjustRuleIDs []RuleIDResult
	err = db.Table("vm_rule_links").
		Select("DISTINCT adjust_rule_group.rule_id").
		Joins("JOIN adjust_rule_group ON vm_rule_links.group_uuid = adjust_rule_group.uuid").
		Where("vm_rule_links.vm_uuid = ? AND vm_rule_links.deleted_at IS NULL", instanceUUID).
		Scan(&adjustRuleIDs).Error

	if err != nil {
		logger.Errorf("[GetRuleIDsByInstance] Failed to query adjust rules for instance %s: %v", instanceUUID, err)
		return nil, fmt.Errorf("failed to query adjust rules: %w", err)
	}

	for _, r := range adjustRuleIDs {
		ruleIDs = append(ruleIDs, r.RuleID)
	}

	return ruleIDs, nil
}

// GetCompleteRuleByRuleID retrieves complete rule information by rule_id
// This function automatically identifies if the rule is an alarm rule or adjust rule
// Input: ruleID - rule identifier
// Output: interface{} - either *RuleGroupV2 (alarm) or *AdjustRuleGroup (adjust)
func (a *AlarmOperator) GetCompleteRuleByRuleID(ctx context.Context, ruleID string) (result interface{}, err error) {
	logger.Infof("ENTER AlarmOperator.GetCompleteRuleByRuleID: ruleID=%s", ruleID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetCompleteRuleByRuleID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.GetCompleteRuleByRuleID: success")
		}
	}()

	if ruleID == "" {
		return nil, fmt.Errorf("ruleID cannot be empty")
	}

	// First, try to find in alarm rules (rule_group_v2)
	groups, _, err := a.ListRuleGroups(ctx, ListRuleGroupsParams{
		RuleID:   ruleID,
		PageSize: 1,
	})

	if err == nil && len(groups) > 0 {
		// Found in alarm rules, get complete info based on type
		group := groups[0]
		switch group.Type {
		case "cpu":
			return a.GetCPURulesByGroupUUID(ctx, group.UUID, group.Type)
		case "memory":
			return a.GetMemoryRulesByGroupUUID(ctx, group.UUID, group.Type)
		case "bandwidth":
			return a.GetBWRulesByGroupUUID(ctx, group.UUID, group.Type)
		default:
			logger.Errorf("[GetCompleteRuleByRuleID] Unknown alarm rule type: %s for rule_id: %s", group.Type, ruleID)
			return nil, fmt.Errorf("unknown alarm rule type: %s", group.Type)
		}
	}

	// Not found in alarm rules, try adjust rules
	adjustOperator := &AdjustOperator{}
	adjustGroups, _, err := adjustOperator.ListAdjustRuleGroups(ctx, ListAdjustRuleGroupsParams{
		RuleID:   ruleID,
		PageSize: 1,
	})

	if err == nil && len(adjustGroups) > 0 {
		// Found in adjust rules, get complete info based on type
		group := adjustGroups[0]
		switch group.Type {
		case "adjust_cpu":
			return adjustOperator.GetCPUAdjustRulesByGroupUUID(ctx, group.UUID, group.Type)
		case "adjust_in_bw", "adjust_out_bw":
			return adjustOperator.GetBWAdjustRulesByGroupUUID(ctx, group.UUID, group.Type)
		default:
			logger.Errorf("[GetCompleteRuleByRuleID] Unknown adjust rule type: %s for rule_id: %s", group.Type, ruleID)
			return nil, fmt.Errorf("unknown adjust rule type: %s", group.Type)
		}
	}

	// Rule not found in either table
	logger.Infof("[GetCompleteRuleByRuleID] Rule not found: %s", ruleID)
	return nil, fmt.Errorf("rule not found: %s", ruleID)
}

// GetInstanceRuleLinks retrieves all rule links for specific instances
// Input: instanceUUIDs - list of instance UUIDs to query
// Output: map[instanceUUID][]VMRuleLink - grouped by instance UUID
func (a *AlarmOperator) GetInstanceRuleLinks(ctx context.Context, instanceUUIDs []string) (linksMap map[string][]model.VMRuleLink, err error) {
	logger.Infof("ENTER AlarmOperator.GetInstanceRuleLinks: instanceUUIDs=%v", instanceUUIDs)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetInstanceRuleLinks: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetInstanceRuleLinks: found %d links for %d instances", len(linksMap), len(instanceUUIDs))
		}
	}()

	if len(instanceUUIDs) == 0 {
		return map[string][]model.VMRuleLink{}, nil
	}

	ctx, db := common.GetContextDB(ctx)
	var links []model.VMRuleLink

	if err := db.Where("vm_uuid IN (?)", instanceUUIDs).Find(&links).Error; err != nil {
		logger.Errorf("[GetInstanceRuleLinks] Query failed: %v", err)
		return nil, fmt.Errorf("failed to query rule links: %w", err)
	}

	// Group by instance UUID
	result := make(map[string][]model.VMRuleLink)
	for _, link := range links {
		result[link.VMUUID] = append(result[link.VMUUID], link)
	}

	return result, nil
}

// cleanRuleData removes internal database fields from rule data
func cleanRuleData(data interface{}) interface{} {
	// Fields to remove
	fieldsToRemove := []string{"ID", "CreatedAt", "UpdatedAt", "DeletedAt", "Creater", "OwnerInfo", "GroupUUID"}

	// First convert to map if it's not already
	var dataMap map[string]interface{}

	switch v := data.(type) {
	case map[string]interface{}:
		dataMap = v
	default:
		// Convert struct to map via JSON
		jsonBytes, err := json.Marshal(data)
		if err != nil {
			logger.Errorf("[cleanRuleData] Failed to marshal data: %v", err)
			return data
		}
		if err := json.Unmarshal(jsonBytes, &dataMap); err != nil {
			logger.Errorf("[cleanRuleData] Failed to unmarshal data: %v", err)
			return data
		}
	}

	// Clean the map
	for _, field := range fieldsToRemove {
		delete(dataMap, field)
	}

	// Clean nested details array
	if details, ok := dataMap["details"].([]interface{}); ok {
		cleanedDetails := make([]interface{}, len(details))
		for i, detail := range details {
			cleanedDetails[i] = cleanRuleData(detail)
		}
		dataMap["details"] = cleanedDetails
	}

	return dataMap
}

// GetInstanceRuleDetails retrieves complete rule group information for specific instances
// This is used by the API layer to return detailed rule information including rule details
// Input: instanceUUIDs - list of instance UUIDs to query
// Output: map with instance_id as key, containing complete rule information (both alarm and adjust rules)
// Each rule group includes an "interfaces" array field containing the network interfaces linked to that rule group
func (a *AlarmOperator) GetInstanceRuleDetails(ctx context.Context, instanceUUIDs []string) (result map[string]interface{}, err error) {
	logger.Infof("ENTER AlarmOperator.GetInstanceRuleDetails: instanceUUIDs=%v", instanceUUIDs)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetInstanceRuleDetails: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetInstanceRuleDetails: processed %d instances", len(result))
		}
	}()

	result = make(map[string]interface{})

	// Process each instance
	for _, instanceUUID := range instanceUUIDs {
		// 1. Get all rule links for this instance to build group_uuid -> interfaces mapping
		ctx, db := common.GetContextDB(ctx)
		var links []model.VMRuleLink
		if err := db.Where("vm_uuid = ? AND deleted_at IS NULL", instanceUUID).Find(&links).Error; err != nil {
			logger.Errorf("[GetInstanceRuleDetails] Failed to get rule links for instance %s: %v", instanceUUID, err)
		}

		// 2. Build map: group_uuid -> []interfaces (using map for deduplication)
		groupInterfacesMap := make(map[string]map[string]bool) // group_uuid -> map[interface]bool
		for _, link := range links {
			if link.Interface != "" && link.GroupUUID != "" {
				if groupInterfacesMap[link.GroupUUID] == nil {
					groupInterfacesMap[link.GroupUUID] = make(map[string]bool)
				}
				groupInterfacesMap[link.GroupUUID][link.Interface] = true
			}
		}

		// Convert to final format: group_uuid -> []interfaces
		groupInterfaces := make(map[string][]string)
		for groupUUID, ifaceMap := range groupInterfacesMap {
			interfaces := make([]string, 0, len(ifaceMap))
			for iface := range ifaceMap {
				interfaces = append(interfaces, iface)
			}
			groupInterfaces[groupUUID] = interfaces
		}

		// 3. Get all rule_ids for this instance
		ruleIDs, err := a.GetRuleIDsByInstance(ctx, instanceUUID)
		if err != nil {
			logger.Errorf("[GetInstanceRuleDetails] Failed to get rule IDs for instance %s: %v", instanceUUID, err)
			// Continue with next instance instead of failing completely
			result[instanceUUID] = map[string]interface{}{
				"instance_id": instanceUUID,
				"rule_count":  0,
				"rule_groups": []interface{}{},
				"error":       err.Error(),
			}
			continue
		}

		// 4. Get complete rule information for each rule_id - return raw database model
		rules := make([]interface{}, 0)
		for _, ruleID := range ruleIDs {
			rule, err := a.GetCompleteRuleByRuleID(ctx, ruleID)
			if err != nil {
				logger.Errorf("[GetInstanceRuleDetails] Failed to get rule %s for instance %s: %v", ruleID, instanceUUID, err)
				// Continue with next rule instead of failing
				continue
			}

			// Clean and append the rule data
			if rule != nil {
				cleanedRule := cleanRuleData(rule)

				// 5. Add interfaces array to the rule group
				// Extract UUID from the cleaned rule to match with groupInterfaces
				// Convert to map if not already
				var ruleMap map[string]interface{}
				if m, ok := cleanedRule.(map[string]interface{}); ok {
					ruleMap = m
				} else {
					// Convert struct to map via JSON
					jsonBytes, err := json.Marshal(cleanedRule)
					if err == nil {
						if err := json.Unmarshal(jsonBytes, &ruleMap); err != nil {
							logger.Errorf("[GetInstanceRuleDetails] Failed to unmarshal rule to map: %v", err)
							ruleMap = make(map[string]interface{})
						}
					} else {
						logger.Errorf("[GetInstanceRuleDetails] Failed to marshal rule: %v", err)
						ruleMap = make(map[string]interface{})
					}
				}

				// Try to get UUID from different possible field names (UUID, uuid, Uuid)
				var groupUUID string
				if uuid, exists := ruleMap["UUID"].(string); exists && uuid != "" {
					groupUUID = uuid
				} else if uuid, exists := ruleMap["uuid"].(string); exists && uuid != "" {
					groupUUID = uuid
				} else if uuid, exists := ruleMap["Uuid"].(string); exists && uuid != "" {
					groupUUID = uuid
				}

				// Get interfaces for this group_uuid
				var interfaces []string
				if groupUUID != "" {
					interfaces = groupInterfaces[groupUUID]
					if interfaces == nil {
						interfaces = []string{} // Return empty array if no interfaces
					}
				} else {
					// If UUID not found, set empty array
					interfaces = []string{}
					logger.Warningf("[GetInstanceRuleDetails] Warning: Could not find UUID in rule for rule_id %s", ruleID)
				}

				ruleMap["interfaces"] = interfaces
				cleanedRule = ruleMap

				rules = append(rules, cleanedRule)
			} else {
				logger.Warningf("[GetInstanceRuleDetails] Nil rule returned for rule_id %s", ruleID)
			}
		}

		// 6. Build result for this instance
		result[instanceUUID] = map[string]interface{}{
			"instance_id": instanceUUID,
			"rule_count":  len(rules),
			"rule_groups": rules,
		}
	}

	// Ensure all instances have a result entry
	for _, instanceUUID := range instanceUUIDs {
		if _, exists := result[instanceUUID]; !exists {
			result[instanceUUID] = map[string]interface{}{
				"instance_id": instanceUUID,
				"rule_count":  0,
				"rule_groups": []interface{}{},
			}
		}
	}

	return result, nil
}

func (a *AlarmOperator) DeleteRuleGroupWithDependencies(ctx context.Context, groupUUID, ruleType string) (err error) {
	logger.Infof("ENTER AlarmOperator.DeleteRuleGroupWithDependencies: groupUUID=%s, ruleType=%s", groupUUID, ruleType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.DeleteRuleGroupWithDependencies: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.DeleteRuleGroupWithDependencies: success")
		}
	}()

	ctx, db := common.GetContextDB(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		// delete detail db
		switch ruleType {
		case "cpu":
			if err := tx.Where("group_uuid = ?", groupUUID).
				Delete(&model.CPURuleDetail{}).Error; err != nil {
				logger.Errorf("CPU rules delete failed: group_uuid=%s, error=%v", groupUUID, err)
				return fmt.Errorf("CPU rules delete failed: %w", err)
			}
		case "memory":
			if err := tx.Where("group_uuid = ?", groupUUID).
				Delete(&model.MemoryRuleDetail{}).Error; err != nil {
				logger.Errorf("Memory rules delete failed: group_uuid=%s, error=%v", groupUUID, err)
				return fmt.Errorf("Memory rules delete failed: %w", err)
			}
		case "bw":
			if err := tx.Where("group_uuid = ?", groupUUID).
				Delete(&model.BWRuleDetail{}).Error; err != nil {
				logger.Errorf("bw rules delete failed: group_uuid=%s, error=%v", groupUUID, err)
				return fmt.Errorf("bw rules delete failed: %w", err)
			}
		default:
			return fmt.Errorf("unknow type: %s", ruleType)
		}
		// delete link db
		if err := tx.Where("group_uuid = ?", groupUUID).
			Delete(&model.VMRuleLink{}).Error; err != nil {
			logger.Errorf("failed to del vm link: groupUUID=%s, error=%v", groupUUID, err)
			return fmt.Errorf("failed to del vm link: %w", err)
		}
		// delete group rule db
		if err := tx.Where("uuid = ? AND type = ?", groupUUID, ruleType).
			Delete(&model.RuleGroupV2{}).Error; err != nil {
			logger.Errorf("group del failed: groupUUID=%s, error=%v", groupUUID, err)
			return fmt.Errorf("group del failed: %w", err)
		}

		return nil
	})
}

func Paginate(page, pageSize int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		offset := (page - 1) * pageSize
		return db.Offset(offset).Limit(pageSize)
	}
}

func (a *AlarmOperator) DeleteCPURulesByGroup(ctx context.Context, groupID string) (err error) {
	logger.Infof("ENTER AlarmOperator.DeleteCPURulesByGroup: groupID=%s", groupID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.DeleteCPURulesByGroup: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.DeleteCPURulesByGroup: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	if err = db.Where("group_uuid = ?", groupID).
		Delete(&CPURule{}).Error; err != nil {
		logger.Errorf("CPU rule delete failed: groupID=%s, error=%v", groupID, err)
		return err
	}
	return nil
}

func (a *AlarmOperator) ListRuleGroups(ctx context.Context, params ListRuleGroupsParams) (groups []model.RuleGroupV2, total int64, err error) {
	logger.Infof("ENTER AlarmOperator.ListRuleGroups: type=%s, groupUUID=%s, ruleID=%s", params.RuleType, params.GroupUUID, params.RuleID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.ListRuleGroups: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.ListRuleGroups: total=%d", total)
		}
	}()
	ctx, db := common.GetContextDB(ctx)

	query := db.Model(&model.RuleGroupV2{})
	if params.RuleType != "" {
		query = query.Where("type = ?", params.RuleType)
	}
	if params.GroupUUID != "" {
		query = query.Where("uuid = ?", params.GroupUUID)
	}
	// Added: Support query by rule_id
	if params.RuleID != "" {
		query = query.Where("rule_id = ?", params.RuleID)
	}

	if err = query.Count(&total).Error; err != nil {
		logger.Errorf("get rules count failed: ruleType=%s, error=%v", params.RuleType, err)
		return nil, 0, fmt.Errorf("get rules count failed: %w", err)
	}
	if err = query.Scopes(Paginate(params.Page, params.PageSize)).
		Find(&groups).Error; err != nil {
		logger.Errorf("page query failed: ruleType=%s, page=%d, pageSize=%d, error=%v",
			params.RuleType, params.Page, params.PageSize, err)
		return nil, 0, fmt.Errorf("page query failed: %w", err)
	}

	return groups, total, nil
}

func (a *AlarmOperator) GetCPURuleDetails(ctx context.Context, groupUUID string) (details []model.CPURuleDetail, err error) {
	logger.Infof("ENTER AlarmOperator.GetCPURuleDetails: groupUUID=%s", groupUUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetCPURuleDetails: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetCPURuleDetails: found %d details", len(details))
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	if err := db.Where("group_uuid = ?", groupUUID).Order("id ASC").Find(&details).Error; err != nil {
		logger.Errorf("query CPU rules detail failed: groupUUID=%s, error=%v", groupUUID, err)
	}
	return details, nil
}

func (a *AlarmOperator) GetMemoryRuleDetails(ctx context.Context, groupUUID string) (details []model.MemoryRuleDetail, err error) {
	logger.Infof("ENTER AlarmOperator.GetMemoryRuleDetails: groupUUID=%s", groupUUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetMemoryRuleDetails: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetMemoryRuleDetails: found %d details", len(details))
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	if err := db.Where("group_uuid = ?", groupUUID).Order("id ASC").Find(&details).Error; err != nil {
		logger.Errorf("query Memory rules detail failed: groupUUID=%s, error=%v", groupUUID, err)
	}
	return details, nil
}

func (a *AlarmOperator) IncrementTriggerCount(ctx context.Context, groupID string) (err error) {
	logger.Infof("ENTER AlarmOperator.IncrementTriggerCount: groupID=%s", groupID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.IncrementTriggerCount: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.IncrementTriggerCount: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	return db.Model(&model.RuleGroupV2{}).
		Where("uuid = ?", groupID).
		Update("trigger_cnt", gorm.Expr("trigger_cnt + 1")).Error
}

func (a *AlarmOperator) CreateCPURules(ctx context.Context, groupUUID string, rules []CPURule) (err error) {
	logger.Infof("ENTER AlarmOperator.CreateCPURules: groupUUID=%s, rulesCount=%d", groupUUID, len(rules))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.CreateCPURules: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.CreateCPURules: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		for i := range rules {
			rule := &CPURule{
				GroupUUID:    groupUUID,
				Name:         rules[i].Name,
				Duration:     rules[i].Duration,
				Over:         rules[i].Over,
				DownDuration: rules[i].DownDuration,
				DownTo:       rules[i].DownTo,
			}
			if err := tx.Create(rule).Error; err != nil {
				logger.Errorf("create cpu rule failed: groupUUID=%s, rule=%+v, error=%v", groupUUID, rules[i], err)
				return fmt.Errorf("create cpu rule failed: %w", err)
			}
		}
		return nil
	})
}

func (a *AlarmOperator) CreateBWRuleDetail(ctx context.Context, detail *model.BWRuleDetail) (err error) {
	logger.Infof("ENTER AlarmOperator.CreateBWRuleDetail: groupUUID=%s, name=%s", detail.GroupUUID, detail.Name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.CreateBWRuleDetail: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.CreateBWRuleDetail: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	if err := db.Create(detail).Error; err != nil {
		logger.Errorf("create bw rule detail failed: groupUUID=%s, name=%s, error=%v",
			detail.GroupUUID, detail.Name, err)
		return fmt.Errorf("create bw rule detail failed: %w", err)
	}
	return nil
}

func (a *AlarmOperator) GetBWRuleDetails(ctx context.Context, groupUUID string) (details []model.BWRuleDetail, err error) {
	logger.Infof("ENTER AlarmOperator.GetBWRuleDetails: groupUUID=%s", groupUUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetBWRuleDetails: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetBWRuleDetails: found %d details", len(details))
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	if err := db.Where("group_uuid = ?", groupUUID).Order("id ASC").Find(&details).Error; err != nil {
		logger.Errorf("query db BW rules detailed: groupUUID=%s, error=%v", groupUUID, err)
		return nil, fmt.Errorf("query db BW rules detailed: %w", err)
	}
	return details, nil
}

func (a *AlarmOperator) CreateRuleGroup(ctx context.Context, group *model.RuleGroupV2) (err error) {
	logger.Infof("ENTER AlarmOperator.CreateRuleGroup: name=%s, type=%s, uuid=%s", group.Name, group.Type, group.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.CreateRuleGroup: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.CreateRuleGroup: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	if err = db.Create(group).Error; err != nil {
		logger.Errorf("failed to create rule: UUID=%s, GroupUUID=%s, error=%v", group.UUID, group.UUID, err)
		return fmt.Errorf("failed to create rule: %w", err)
	}
	return nil
}

func (a *AlarmOperator) CreateCPURuleDetail(ctx context.Context, detail *model.CPURuleDetail) (err error) {
	logger.Infof("ENTER AlarmOperator.CreateCPURuleDetail: groupUUID=%s, ruleName=%s", detail.GroupUUID, detail.Name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.CreateCPURuleDetail: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.CreateCPURuleDetail: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	detail.UUID = uuid.NewString()
	if err := db.Create(detail).Error; err != nil {
		logger.Errorf("create cpu rule detail failed: groupUUID=%s, ruleName=%s, error=%v", detail.GroupUUID, detail.Name, err)
		return fmt.Errorf("create cpu rule detail failed: %w", err)
	}
	return nil
}

func (a *AlarmOperator) CreateMemoryRuleDetail(ctx context.Context, detail *model.MemoryRuleDetail) (err error) {
	logger.Infof("ENTER AlarmOperator.CreateMemoryRuleDetail: groupUUID=%s, ruleName=%s", detail.GroupUUID, detail.Name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.CreateMemoryRuleDetail: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.CreateMemoryRuleDetail: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	detail.UUID = uuid.NewString()
	if err := db.Create(detail).Error; err != nil {
		logger.Errorf("create memory rule detail failed: groupUUID=%s, ruleName=%s, error=%v", detail.GroupUUID, detail.Name, err)
		return fmt.Errorf("create memory rule detail failed: %w", err)
	}
	return nil
}

func isLocalIP(ip string) bool {
	logger.Infof("ENTER isLocalIP: ip=%s", ip)
	isLocal := false
	defer func() {
		logger.Infof("EXIT isLocalIP: isLocal=%v", isLocal)
	}()
	if ip == "localhost" || ip == "127.0.0.1" {
		isLocal = true
		return true
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		logger.Errorf("get local network configuration failed: %v", err)
		return false
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ipnet.IP.String() == ip {
				isLocal = true
				return true
			}
		}
	}
	return false
}

func AlertRUleClient(baseURL, certFile, keyFile string) (*PrometheusClient, error) {
	var client *http.Client
	if certFile != "" && keyFile == "" {
		caCert, err := os.ReadFile(certFile)
		if err != nil {
			return nil, fmt.Errorf("Read cert file failed: %v", err)
		}
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		tlsConfig := &tls.Config{
			RootCAs:            caCertPool,
			InsecureSkipVerify: false,
		}
		transport := &http.Transport{
			TLSClientConfig: tlsConfig,
		}

		client = &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		}
	} else if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("Load cert file failed: %v", err)
		}

		tlsConfig := &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: false,
		}

		caCertPath := filepath.Join(filepath.Dir(certFile), "ca.crt")
		if _, err := os.Stat(caCertPath); err == nil {
			caCert, err := os.ReadFile(caCertPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read CA certificate: %v", err)
			}
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			tlsConfig.RootCAs = caCertPool
		}

		transport := &http.Transport{
			TLSClientConfig: tlsConfig,
		}

		client = &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		}
	} else {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	return &PrometheusClient{
		BaseURL:    baseURL,
		HTTPClient: client,
	}, nil
}

func (c *PrometheusClient) sendRequest(endpoint string, req RuleFileRequest) (resp []byte, err error) {
	logger.Infof("ENTER PrometheusClient.sendRequest: endpoint=%s, operation=%s", endpoint, req.Operation)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.sendRequest: error=%v", err)
		} else {
			logger.Infof("EXIT PrometheusClient.sendRequest: success, respSize=%d", len(resp))
		}
	}()
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("The request serialization failed: %v", err)
	}

	url := c.BaseURL + endpoint
	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("Create HTTP request failed: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request failed: %v", err)
	}
	defer httpResp.Body.Close()

	resp, err = io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body failed: %v", err)
	}

	if httpResp.StatusCode != 200 {
		return nil, fmt.Errorf("request failed with status %d: %s", httpResp.StatusCode, string(resp))
	}

	return resp, nil
}

func (c *PrometheusClient) sendRequestNoResponse(endpoint string, req RuleFileRequest) (err error) {
	logger.Infof("ENTER PrometheusClient.sendRequestNoResponse: endpoint=%s, operation=%s", endpoint, req.Operation)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.sendRequestNoResponse: error=%v", err)
		} else {
			logger.Info("EXIT PrometheusClient.sendRequestNoResponse: success")
		}
	}()
	_, err = c.sendRequest(endpoint, req)
	return err
}

func (c *PrometheusClient) ClientReadRuleFile(path string) (content []byte, err error) {
	logger.Infof("ENTER PrometheusClient.ClientReadRuleFile: path=%s", path)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.ClientReadRuleFile: error=%v", err)
		} else {
			logger.Infof("EXIT PrometheusClient.ClientReadRuleFile: success, contentSize=%d", len(content))
		}
	}()
	req := RuleFileRequest{
		Operation: "read",
		FilePath:  path,
		FileUser:  "prometheus",
	}

	respBody, err := c.sendRequest("/api/v1/rules/file", req)
	if err != nil {
		logger.Errorf("prometheus server read file failed: %v", err)
		return nil, err
	}

	// Parse response
	var response RuleFileResponse
	if err = json.Unmarshal(respBody, &response); err != nil {
		logger.Errorf("parse response failed: %v", err)
		return nil, fmt.Errorf("parse response failed: %v", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("read file failed: %s", response.Message)
	}

	return []byte(response.Content), nil
}
func (c *PrometheusClient) ClientWriteRuleFile(path string, content []byte, perm os.FileMode) (err error) {
	logger.Infof("ENTER PrometheusClient.ClientWriteRuleFile: path=%s, contentSize=%d", path, len(content))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.ClientWriteRuleFile: error=%v", err)
		} else {
			logger.Info("EXIT PrometheusClient.ClientWriteRuleFile: success")
		}
	}()
	req := RuleFileRequest{
		Operation: "write",
		FilePath:  path,
		Content:   string(content),
		FileUser:  "prometheus",
	}
	err = c.sendRequestNoResponse("/api/v1/rules/file", req)
	if err != nil {
		logger.Errorf("prometheus server create file failed: %v", err)
	}
	return err
}

func (c *PrometheusClient) ClientCreateSymlink(target, link string) error {
	req := RuleFileRequest{
		Operation: "symlink",
		FilePath:  target,
		LinkPath:  link,
		FileUser:  "prometheus",
	}

	err := c.sendRequestNoResponse("/api/v1/rules/symlink", req)
	if err != nil {
		logger.Errorf("prometheus server create link failed: %v", err)
	}
	return err
}

func (c *PrometheusClient) ClientSetFileOwner(path string) error {
	req := RuleFileRequest{
		Operation: "chown",
		FilePath:  path,
		FileUser:  "prometheus",
	}

	err := c.sendRequestNoResponse("/api/v1/rules/chown", req)
	if err != nil {
		logger.Errorf("prometheus server create link failed: %v", err)
	}
	return err
}

func (c *PrometheusClient) ClientSetSymlinkOwner(path string) (err error) {
	logger.Infof("ENTER PrometheusClient.ClientSetSymlinkOwner: path=%s", path)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.ClientSetSymlinkOwner: error=%v", err)
		} else {
			logger.Info("EXIT PrometheusClient.ClientSetSymlinkOwner: success")
		}
	}()
	req := RuleFileRequest{
		Operation: "chown_symlink",
		FilePath:  path,
		FileUser:  "prometheus",
	}

	err = c.sendRequestNoResponse("/api/v1/rules/chown", req)
	if err != nil {
		logger.Errorf("prometheus server set link owner failed: %v", err)
	}
	return err
}

func (c *PrometheusClient) ClientRemoveRuleFile(path string) (err error) {
	logger.Infof("ENTER PrometheusClient.ClientRemoveRuleFile: path=%s", path)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.ClientRemoveRuleFile: error=%v", err)
		} else {
			logger.Info("EXIT PrometheusClient.ClientRemoveRuleFile: success")
		}
	}()
	req := RuleFileRequest{
		Operation: "delete",
		FilePath:  path,
	}

	err = c.sendRequestNoResponse("/api/v1/rules/file", req)
	if err != nil {
		logger.Errorf("prometheus server remove file failed: %v", err)
	}
	return err
}

func (c *PrometheusClient) ClientCheckFileExists(path string) (exists bool, err error) {
	logger.Infof("ENTER PrometheusClient.ClientCheckFileExists: path=%s", path)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.ClientCheckFileExists: error=%v", err)
		} else {
			logger.Infof("EXIT PrometheusClient.ClientCheckFileExists: exists=%v", exists)
		}
	}()
	req := RuleFileRequest{
		Operation: "check",
		FilePath:  path,
	}
	respBody, err := c.sendRequest("/api/v1/rules/file", req)
	if err != nil {
		logger.Errorf("server check file failed: %v", err)
		return false, err
	}
	var resp RuleFileResponse
	if err = json.Unmarshal(respBody, &resp); err != nil {
		return false, fmt.Errorf("failed to parse response: %w", err)
	}
	return resp.Exists, nil
}

func (c *PrometheusClient) ClientRemoveSymlink(linkPath string) (err error) {
	logger.Infof("ENTER PrometheusClient.ClientRemoveSymlink: linkPath=%s", linkPath)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT PrometheusClient.ClientRemoveSymlink: error=%v", err)
		} else {
			logger.Info("EXIT PrometheusClient.ClientRemoveSymlink: success")
		}
	}()
	req := RuleFileRequest{
		Operation: "delete",
		FilePath:  linkPath,
	}

	err = c.sendRequestNoResponse("/api/v1/rules/file", req)
	if err != nil {
		logger.Errorf("prometheus server remove symlink failed: %v", err)
	}
	return err
}

func (c *PrometheusClient) ClientGetUser(username string) (int, int, error) {
	req := RuleFileRequest{
		Operation: "getuser",
		FileUser:  username,
	}

	resp, err := c.sendRequest("/api/v1/rules/user", req)
	if err != nil {
		return 0, 0, err
	}

	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		UID     int    `json:"uid"`
		GID     int    `json:"gid"`
	}

	if err := json.Unmarshal(resp, &response); err != nil {
		return 0, 0, fmt.Errorf("failed to parse response: %v", err)
	}

	if !response.Success {
		return 0, 0, fmt.Errorf("failed to get user information: %s", response.Message)
	}

	return response.UID, response.GID, nil
}

func (c *PrometheusClient) ClientReloadPrometheus() error {
	logger.Info("ENTER PrometheusClient.ClientReloadPrometheus")
	defer func() {
		logger.Info("EXIT PrometheusClient.ClientReloadPrometheus")
	}()
	req := RuleFileRequest{
		Operation: "reload",
	}

	return c.sendRequestNoResponse("/api/v1/rules/reload", req)
}

func GetUser(username string) (uid, gid int, err error) {
	if isRemotePrometheus {
		// Get user ID from remote server
		uid, gid, err = prometheusClient.ClientGetUser("prometheus")
		if err != nil {
			return 0, 0, fmt.Errorf("failed to get remote group ID for %s: %v", username, err)
		}

		return uid, gid, nil
	} else {
		// Get user information locally
		u, err := user.Lookup(username)
		if err != nil {
			return 0, 0, err
		}
		uid, _ = strconv.Atoi(u.Uid)
		gid, _ = strconv.Atoi(u.Gid)
		return uid, gid, nil
	}
}

func ReadFile(path string) ([]byte, error) {
	logger.Infof("ENTER ReadFile: path=%s, isRemotePrometheus=%t", path, isRemotePrometheus)
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return nil, fmt.Errorf("The Prometheus client has not been initialized.")
		}
		return prometheusClient.ClientReadRuleFile(path)
	} else {
		return os.ReadFile(path)
	}
}

func WriteFile(path string, content []byte, perm os.FileMode) error {
	logger.Infof("ENTER WriteFile: path=%s, isRemotePrometheus=%t", path, isRemotePrometheus)
	defer logger.Info("EXIT WriteFile")
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return fmt.Errorf("The Prometheus client has not been initialized.")
		}
		return prometheusClient.ClientWriteRuleFile(path, content, perm)
	} else {
		if err := os.WriteFile(path, content, perm); err != nil {
			logger.Errorf("Failed to write file locally: path=%s, error=%v", path, err)
			return err
		}
		uid, gid, err := GetUser("prometheus")
		if err != nil {
			logger.Errorf("Failed to get prometheus user for chown: %v", err)
			return err
		}
		return SetFileOwner(path, uid, gid)
	}
}

func SetFileOwner(path string, uid, gid int) error {
	logger.Infof("ENTER SetFileOwner: path=%s, uid=%d, gid=%d, isRemotePrometheus=%t", path, uid, gid, isRemotePrometheus)
	defer logger.Info("EXIT SetFileOwner")
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return fmt.Errorf("The Prometheus client has not been initialized.")
		}

		return prometheusClient.ClientSetFileOwner(path)
	} else {
		return os.Chown(path, uid, gid)
	}
}

func SetSymlinkOwner(path string, uid, gid int) error {
	logger.Infof("ENTER SetSymlinkOwner: path=%s, uid=%d, gid=%d, isRemotePrometheus=%t", path, uid, gid, isRemotePrometheus)
	defer logger.Info("EXIT SetSymlinkOwner")
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return fmt.Errorf("The Prometheus client has not been initialized.")
		}

		return prometheusClient.ClientSetSymlinkOwner(path)
	} else {
		// GetUser is called inside this block, so uid, gid are not directly used from params
		// This is a potential bug if the intent was to use the passed uid, gid.
		// However, the original code also calls GetUser("prometheus") inside the else block.
		// Keeping the original logic for now, but logging the potential discrepancy.
		logger.Warningf("SetSymlinkOwner: Ignoring passed uid, gid (%d, %d) and calling GetUser('prometheus') locally.", uid, gid)
		localUid, localGid, err := GetUser("prometheus")
		if err != nil {
			logger.Errorf("Prometheus server set link owner failed with %s", err)
			return err
		}
		return os.Lchown(path, localUid, localGid)
	}
}

func CreateSymlink(target, link string) error {
	logger.Infof("ENTER CreateSymlink: target=%s, link=%s, isRemotePrometheus=%t", target, link, isRemotePrometheus)
	defer logger.Info("EXIT CreateSymlink")
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return fmt.Errorf("The Prometheus client has not been initialized.")
		}

		return prometheusClient.ClientCreateSymlink(target, link)
	} else {
		if _, err := os.Lstat(link); err == nil {
			logger.Infof("Removing existing symlink: %s", link)
			os.Remove(link)
		}
		if err := os.Symlink(target, link); err != nil {
			logger.Errorf("Failed to create symlink locally: target=%s, link=%s, error=%v", target, link, err)
			return err
		}
		uid, gid, err := GetUser("prometheus")
		if err != nil {
			logger.Errorf("Failed to get prometheus user for symlink chown: %v", err)
			return err
		}
		return SetSymlinkOwner(link, uid, gid)

	}
}

func RemoveSymlink(link string) error {
	logger.Infof("ENTER RemoveSymlink: link=%s, isRemotePrometheus=%t", link, isRemotePrometheus)
	defer logger.Info("EXIT RemoveSymlink")
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return fmt.Errorf("The Prometheus client has not been initialized.")
		}

		return prometheusClient.ClientRemoveSymlink(link)
	} else {
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			logger.Infof("Symlink does not exist, no need to remove: %s", link)
			return nil
		}
		if err := os.Remove(link); err != nil {
			logger.Errorf("Failed to remove symlink locally: link=%s, error=%v", link, err)
			return err
		}
		return nil
	}
}

func ReloadPrometheus() (err error) {
	logger.Info("ENTER ReloadPrometheus")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ReloadPrometheus: error=%v", err)
		} else {
			logger.Info("EXIT ReloadPrometheus: success")
		}
	}()
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return fmt.Errorf("The Prometheus client has not been initialized.")
		}

		return prometheusClient.ClientReloadPrometheus()
	} else {
		return ReloadPrometheusViaHTTP()
	}
}

// ReloadPrometheusViaHTTP reloads Prometheus configuration via HTTP API (requires --web.enable-lifecycle)
func ReloadPrometheusViaHTTP() (err error) {
	logger.Info("ENTER ReloadPrometheusViaHTTP")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ReloadPrometheusViaHTTP: error=%v", err)
		} else {
			logger.Info("EXIT ReloadPrometheusViaHTTP: success")
		}
	}()
	var reloadURL string

	// Step 1: Construct reload URL
	if isRemotePrometheus {
		// Remote scenario: use Prometheus IP and port
		pIP := GetPrometheusIP()
		pPort := GetPrometheusPort()
		reloadURL = fmt.Sprintf("http://%s:%d/-/reload", pIP, pPort)
		logger.Infof("Reloading remote Prometheus via HTTP: %s", reloadURL)
	} else {
		// Local scenario: use localhost
		reloadURL = "http://localhost:9090/-/reload"
		logger.Infof("Reloading local Prometheus via HTTP: %s", reloadURL)
	}

	// Step 2: Create HTTP client with timeout
	// Use a longer timeout to handle slow Prometheus reloads on large deployments
	client := &http.Client{
		Timeout: 2 * time.Minute,
	}

	// Step 3: Send POST request
	resp, err := client.Post(reloadURL, "", nil)
	if err != nil {
		logger.Errorf("Failed to send reload request to Prometheus: %v", err)
		return fmt.Errorf("failed to reload Prometheus via HTTP: %v", err)
	}
	defer resp.Body.Close()

	// Step 4: Check response status code
	if resp.StatusCode != 200 {
		// Read response body for detailed error information
		body, _ := io.ReadAll(resp.Body)
		logger.Errorf("Prometheus reload failed with status %d: %s", resp.StatusCode, string(body))

		// Handle 403 error specifically (lifecycle API not enabled)
		if resp.StatusCode == 403 {
			return fmt.Errorf("Prometheus lifecycle API is not enabled (HTTP 403). Please add --web.enable-lifecycle flag")
		}

		return fmt.Errorf("reload failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Step 5: Success log
	if isRemotePrometheus {
		logger.Infof("Successfully reloaded remote Prometheus configuration via HTTP API")
	} else {
		logger.Infof("Successfully reloaded local Prometheus configuration via HTTP API")
	}

	return nil
}

func RemoveFile(path string) error {
	logger.Infof("ENTER RemoveFile: path=%s, isRemotePrometheus=%t", path, isRemotePrometheus)
	defer logger.Info("EXIT RemoveFile")
	if isRemotePrometheus {
		if prometheusClient == nil {
			logger.Errorf("The Prometheus client has not been initialized.")
			return fmt.Errorf("The Prometheus client has not been initialized.")
		}

		return prometheusClient.ClientRemoveRuleFile(path)
	} else {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			logger.Infof("File does not exist, no need to remove: %s", path)
			return nil
		}
		if err := os.Remove(path); err != nil {
			logger.Errorf("Failed to remove file locally: path=%s, error=%v", path, err)
			return err
		}
		return nil
	}
}

func CheckFileExists(path string) (bool, error) {
	logger.Infof("ENTER CheckFileExists: path=%s, isRemotePrometheus=%t", path, isRemotePrometheus)
	defer logger.Info("EXIT CheckFileExists")
	if isRemotePrometheus {
		return prometheusClient.ClientCheckFileExists(path)
	} else {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}
}


func (a *AlarmOperator) GetNodeAlarmRules(ctx context.Context, uuid string) (rules []model.NodeAlarmRule, err error) {
	logger.Infof("ENTER AlarmOperator.GetNodeAlarmRules: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetNodeAlarmRules: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetNodeAlarmRules: found %d rules", len(rules))
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	if uuid != "" {
		if err := db.Where("uuid = ?", uuid).Find(&rules).Error; err != nil {
			logger.Errorf("query db node alarm rules: uuid=%s, error=%v", uuid, err)
			return nil, fmt.Errorf("query db node alarm rules: %w", err)
		}
	} else {
		if err := db.Find(&rules).Error; err != nil {
			logger.Errorf("query all node alarm rules: error=%v", err)
			return nil, fmt.Errorf("query all node alarm rules: %w", err)
		}
	}
	return rules, nil
}

// CreateNodeAlarmRules creates a new node alarm rule
func (a *AlarmOperator) CreateNodeAlarmRules(ctx context.Context, rule *model.NodeAlarmRule) (err error) {
	logger.Infof("ENTER AlarmOperator.CreateNodeAlarmRules: ruleType=%s, name=%s", rule.RuleType, rule.Name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.CreateNodeAlarmRules: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.CreateNodeAlarmRules: uuid=%s", rule.UUID)
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	rule.UUID = uuid.NewString()
	if err := db.Create(rule).Error; err != nil {
		logger.Errorf("Failed to create node alarm rule: ruleType=%s, name=%s, error=%v",
			rule.RuleType,
			rule.Name,
			err)
		return fmt.Errorf("failed to create node alarm rule: %w", err)
	}
	return nil
}

func (a *AlarmOperator) DeleteNodeAlarmRules(ctx context.Context, uuid string) (err error) {
	logger.Infof("ENTER AlarmOperator.DeleteNodeAlarmRules: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.DeleteNodeAlarmRules: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.DeleteNodeAlarmRules: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	result := db.Where("uuid = ?", uuid).Delete(&model.NodeAlarmRule{})
	if result.Error != nil {
		logger.Errorf("Failed to delete node alarm rule: uuid=%s, error=%v", uuid, result.Error)
		return fmt.Errorf("failed to delete node alarm rule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("node alarm rule not found: %s", uuid)
	}
	return nil
}

func (a *AlarmOperator) UpdateNodeAlarmRule(ctx context.Context, uuid string, updates map[string]interface{}) (err error) {
	logger.Infof("ENTER AlarmOperator.UpdateNodeAlarmRule: uuid=%s, updates=%v", uuid, updates)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.UpdateNodeAlarmRule: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.UpdateNodeAlarmRule: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	result := db.Model(&model.NodeAlarmRule{}).Where("uuid = ?", uuid).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update node alarm rule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("node alarm rule not found: %s", uuid)
	}
	return nil
}

func (a *AlarmOperator) DeleteNodeAlarmRuleByUUID(ctx context.Context, uuid string) (err error) {
	logger.Infof("ENTER AlarmOperator.DeleteNodeAlarmRuleByUUID: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.DeleteNodeAlarmRuleByUUID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.DeleteNodeAlarmRuleByUUID: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	return db.Where("uuid = ?", uuid).Delete(&model.NodeAlarmRule{}).Error
}

func (a *AlarmOperator) UpdateNodeAlarmRuleByUUID(ctx context.Context, uuid string, updates map[string]interface{}) (err error) {
	logger.Infof("ENTER AlarmOperator.UpdateNodeAlarmRuleByUUID: uuid=%s, updates=%v", uuid, updates)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.UpdateNodeAlarmRuleByUUID: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.UpdateNodeAlarmRuleByUUID: success")
		}
	}()
	ctx, db := common.GetContextDB(ctx)
	return db.Model(&model.NodeAlarmRule{}).Where("uuid = ?", uuid).Updates(updates).Error
}

// GetNodeAlarmRulesByType retrieves node alarm rules by rule type
func (a *AlarmOperator) GetNodeAlarmRulesByType(ctx context.Context, ruleType string) (rules []model.NodeAlarmRule, err error) {
	logger.Infof("ENTER AlarmOperator.GetNodeAlarmRulesByType: ruleType=%s", ruleType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.GetNodeAlarmRulesByType: error=%v", err)
		} else {
			logger.Infof("EXIT AlarmOperator.GetNodeAlarmRulesByType: found %d rules", len(rules))
		}
	}()
	ctx, db := common.GetContextDB(ctx)

	if err := db.Where("rule_type = ?", ruleType).Find(&rules).Error; err != nil {
		logger.Errorf("Failed to get node alarm rules by type: ruleType=%s, error=%v", ruleType, err)
		return nil, fmt.Errorf("failed to get node alarm rules by type: %w", err)
	}

	return rules, nil
}

func ProcessTemplate(templateFile, outputFile string, data map[string]interface{}) (err error) {
	logger.Infof("ENTER ProcessTemplate: templateFile=%s, outputFile=%s", templateFile, outputFile)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ProcessTemplate: error=%v", err)
		} else {
			logger.Info("EXIT ProcessTemplate: success")
		}
	}()

	templatePath := filepath.Join(RuleTemplate, templateFile)

	// All rule files are now stored in RulesGeneral directory (simplified from previous owner-based separation)
	outputPath := filepath.Join(RulesGeneral, outputFile)

	// Read template content
	templateContent, err := ReadFile(templatePath)
	if err != nil {
		logger.Errorf("Failed to read template file: path=%s, error=%v", templatePath, err)
		return fmt.Errorf("failed to read template file %s: %w", templatePath, err)
	}
	logger.Debugf("ProcessTemplate templateContent: %s,  templatePath: %s", templateContent, templatePath)

	var renderedContent string
	if strings.Contains(string(templateContent), "name: compute-network-resources") {
		renderedContent, err = renderNetworkResourcesTemplate(data)
	} else {
		renderedContent, err = renderTemplateContent(string(templateContent), data)
	}
	logger.Debugf("ProcessTemplate templateContent: %s,  err: %s", templateContent, err)
	if err != nil {
		logger.Errorf("Failed to render template: template=%s, error=%v", templateFile, err)
		return fmt.Errorf("failed to render template %s: %w", templateFile, err)
	}
	logger.Debugf("ProcessTemplate templateContent: %s,  outputPath: %s", templateContent, outputPath)
	// Write rendered content to output file
	if err := WriteFile(outputPath, []byte(renderedContent), 0640); err != nil {
		logger.Errorf("Failed to write output file: path=%s, error=%v", outputPath, err)
		return fmt.Errorf("failed to write output file %s: %w", outputPath, err)
	}

	// Create symlink to RulesEnabled directory
	enabledPath := filepath.Join(RulesEnabled, filepath.Base(outputPath))
	if err := CreateSymlink(outputPath, enabledPath); err != nil {
		logger.Errorf("Failed to create symlink: source=%s, target=%s, error=%v",
			outputPath, enabledPath, err)
		return fmt.Errorf("failed to create symlink: %w", err)
	}

	return nil
}

func renderNetworkResourcesTemplate(data map[string]interface{}) (renderedContent string, err error) {
	logger.Info("ENTER renderNetworkResourcesTemplate")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT renderNetworkResourcesTemplate: error=%v", err)
		} else {
			logger.Info("EXIT renderNetworkResourcesTemplate: success")
		}
	}()

	templatePath := filepath.Join(RuleTemplate, "compute-network-resources.yml.j2")
	templateContent, err := ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("failed to read network resources template: %w", err)
	}

	templateStr := string(templateContent)
	ruleTemplate := ""
	if strings.Contains(templateStr, "{% for net_type, params in network_types.items() %}") {
		parts := strings.Split(templateStr, "{% for net_type, params in network_types.items() %}")
		if len(parts) >= 2 {
			ruleTemplate = strings.Split(parts[1], "{% endfor %}")[0]
		}
	}

	if ruleTemplate == "" {
		return "", fmt.Errorf("invalid template format: missing for loop")
	}

	var rulesContent strings.Builder
	rulesContent.WriteString("groups:\n- name: compute-network-resources\n  rules:\n")

	networkTypes, ok := data["network_types"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("network_types not found in data")
	}

	for netType, params := range networkTypes {
		paramsMap, ok := params.(map[string]interface{})
		if !ok {
			logger.Warningf("Skipping non-map params for net_type %s: %v", netType, params)
			continue
		}

		// Create a copy of data for each iteration to avoid modifying the original map
		// and to ensure placeholders are correctly replaced for each net_type
		iterationData := make(map[string]interface{})
		for k, v := range data {
			iterationData[k] = v
		}

		iterationData["net_type"] = netType
		iterationData["net_type_cap"] = strings.ToUpper(netType[:1]) + netType[1:]

		for k, v := range paramsMap {
			iterationData[fmt.Sprintf("params.%s", k)] = v
		}

		ruleContent := ruleTemplate
		for key, value := range iterationData {
			if strings.HasPrefix(key, "$labels.") {
				continue
			}
			placeholder := fmt.Sprintf("{{ %s }}", key)
			strValue := fmt.Sprintf("%v", value)
			ruleContent = strings.ReplaceAll(ruleContent, placeholder, strValue)
		}

		defaultPattern := regexp.MustCompile(`{{ ([^}]+) \| default\(([^)]+)\) }}`)
		ruleContent = defaultPattern.ReplaceAllStringFunc(ruleContent, func(match string) string {
			matches := defaultPattern.FindStringSubmatch(match)
			if len(matches) != 3 {
				return match
			}
			key := strings.TrimSpace(matches[1])
			defaultValue := strings.TrimSpace(matches[2])
			if value, ok := iterationData[key]; ok {
				return fmt.Sprintf("%v", value)
			}
			return defaultValue
		})

		rulesContent.WriteString(ruleContent)
	}

	return rulesContent.String(), nil
}

func renderTemplateContent(templateContent string, data map[string]interface{}) (result string, err error) {
	logger.Info("ENTER renderTemplateContent")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT renderTemplateContent: error=%v", err)
		} else {
			logger.Info("EXIT renderTemplateContent: success")
		}
	}()

	if strings.Contains(templateContent, "name: compute-network-resources") {
		return "", fmt.Errorf("this template should be handled by renderNetworkResourcesTemplate")
	}

	result = templateContent

	// 1. Replace all variables in data (excluding $labels.)
	for key, value := range data {
		if strings.HasPrefix(key, "$labels.") {
			continue
		}
		placeholder := fmt.Sprintf("{{ %s }}", key)
		strValue := fmt.Sprintf("%v", value)
		result = strings.ReplaceAll(result, placeholder, strValue)

		// 1.1 Replace {{ key | default("xxx") }} format
		defaultPattern := regexp.MustCompile(fmt.Sprintf(`{{ %s \| default\("([^"]*)"\) }}`, key))
		result = defaultPattern.ReplaceAllString(result, strValue)

		// 1.2 Replace {{ key | default(xxx) }} format without quotes
		noQuotesPattern := regexp.MustCompile(fmt.Sprintf(`{{ *(?:\(?%s\)? *\| *default\(([^"\)]+)\)) *}}`, key))
		result = noQuotesPattern.ReplaceAllString(result, strValue)
	}

	// 2. Replace remaining default syntax variables (if no value provided in data)
	// 2.1 Default with quotes
	defaultPattern := regexp.MustCompile(`{{ ([a-zA-Z0-9_]+) \| default\("([^"]*)"\) }}`)
	result = defaultPattern.ReplaceAllStringFunc(result, func(match string) string {
		matches := defaultPattern.FindStringSubmatch(match)
		if len(matches) != 3 {
			return match
		}
		key := matches[1]
		if strings.HasPrefix(key, "$labels.") {
			return match
		}
		if val, ok := data[key]; ok {
			return fmt.Sprintf("%v", val)
		}
		return matches[2]
	})

	// 2.2 Default without quotes
	noQuotesPattern := regexp.MustCompile(`{{ *(?:\(?([a-zA-Z0-9_]+)\)? *\| *default\(([^"\)]+)\)) *}}`)
	result = noQuotesPattern.ReplaceAllStringFunc(result, func(match string) string {
		matches := noQuotesPattern.FindStringSubmatch(match)
		if len(matches) != 3 {
			return match
		}
		key := matches[1]
		if strings.HasPrefix(key, "$labels.") {
			return match
		}
		if val, ok := data[key]; ok {
			return fmt.Sprintf("%v", val)
		}
		return matches[2]
	})

	// 3. Unpack Prometheus template expressions, for example:
	// {{ "{{ if $labels.xxx }}a{{ else }}b{{ end }}" }} -> {{ if $labels.xxx }}a{{ else }}b{{ end }}
	promExprPattern := regexp.MustCompile(`{{ "{{ ([^{}]+) }}" }}`)
	result = promExprPattern.ReplaceAllString(result, "{{ $1 }}")

	return result, nil
}

func validateNodeAlarmRule(rule *model.NodeAlarmRule) error {
	logger.Infof("ENTER validateNodeAlarmRule: ruleType=%s, name=%s", rule.RuleType, rule.Name)
	defer logger.Info("EXIT validateNodeAlarmRule")
	if rule.RuleType == "" {
		return fmt.Errorf("rule_type is required")
	}
	if rule.Name == "" {
		return fmt.Errorf("name is required")
	}
	if rule.Owner == "" {
		return fmt.Errorf("owner is required")
	}
	if len(rule.Config.RawMessage) == 0 {
		return fmt.Errorf("config is required")
	}

	// Validate if config is valid JSON
	var temp interface{}
	if err := json.Unmarshal(rule.Config.RawMessage, &temp); err != nil {
		return fmt.Errorf("config must be valid JSON: %w", err)
	}
	return nil
}

func createNodeAlarmRuleInternal(ctx context.Context, rule *model.NodeAlarmRule) (nr *model.NodeAlarmRule, err error) {
	logger.Infof("ENTER createNodeAlarmRuleInternal: name=%s, ruleType=%s", rule.Name, rule.RuleType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT createNodeAlarmRuleInternal: error=%v", err)
		} else {
			logger.Infof("EXIT createNodeAlarmRuleInternal: uuid=%s", nr.UUID)
		}
	}()
	if err = validateNodeAlarmRule(rule); err != nil {
		return nil, err
	}

	operator := &AlarmOperator{}
	existingRules, err := operator.GetNodeAlarmRulesByType(ctx, rule.RuleType)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing rules: %v", err)
	}
	if len(existingRules) > 0 {
		return nil, fmt.Errorf("rule type %s already exists, only one rule per type is allowed", rule.RuleType)
	}

	newRule := &model.NodeAlarmRule{
		RuleType:    rule.RuleType,
		Name:        rule.Name,
		Config:      rule.Config,
		Description: rule.Description,
		Owner:       rule.Owner,
		Enabled:     true,
	}
	err = operator.CreateNodeAlarmRules(ctx, newRule)
	if err != nil {
		return nil, fmt.Errorf("failed to save rule to database: %v", err)
	}

	var templateFiles []string
	switch rule.RuleType {
	case RuleTypeAvailable:
		templateFiles = []string{"node-availability.yml.j2"}
	case RuleTypeControl:
		templateFiles = []string{"management-resources.yml.j2"}
	case RuleTypeCompute:
		templateFiles = []string{"compute-core-resources.yml.j2", "compute-network-resources.yml.j2"}
	case RuleTypeHypervisorVCPU:
		templateFiles = []string{"compute-vcpu-resources.yml.j2"}
	case RuleTypePacketDrop:
		templateFiles = []string{"packet-drop-monitor.yml.j2"}
	case RuleTypeIPBlock:
		templateFiles = []string{"ip-block-monitor.yml.j2"}
	case "ipgroup_available_ip":
		templateFiles = []string{"ipgroup-available-ip-monitor.yml.j2"}
	default:
		operator.DeleteNodeAlarmRules(ctx, newRule.UUID)
		return nil, fmt.Errorf("unsupported rule type: %s", rule.RuleType)
	}

	for _, templateFile := range templateFiles {
		var configData map[string]interface{}
		if err = json.Unmarshal(rule.Config.RawMessage, &configData); err != nil {
			operator.DeleteNodeAlarmRules(ctx, newRule.UUID)
			return nil, fmt.Errorf("failed to parse config JSON: %v", err)
		}

		if rule.RuleType == RuleTypeAvailable {
			if nodeDownDuration, ok := configData["node_down_duration"].(string); ok {
				duration, err := time.ParseDuration(nodeDownDuration)
				if err != nil {
					operator.DeleteNodeAlarmRules(ctx, newRule.UUID)
					return nil, fmt.Errorf("invalid node_down_duration format: %v", err)
				}
				configData["node_down_duration_minutes"] = int(duration.Minutes())
			} else {
				configData["node_down_duration_minutes"] = 5
			}
		}

		outputFile := strings.TrimSuffix(templateFile, ".j2")

		err = ProcessTemplate(templateFile, outputFile, configData)
		if err != nil {
			operator.DeleteNodeAlarmRules(ctx, newRule.UUID)
			return nil, fmt.Errorf("failed to process template %s: %v", templateFile, err)
		}
	}

	if err := ReloadPrometheusViaHTTP(); err != nil {
		logger.Errorf("Failed to reload Prometheus: %v", err)
	}

	return newRule, nil
}

func (a *AlarmAdmin) CreateNodeAlarmRule(ctx context.Context, rule *model.NodeAlarmRule) (*model.NodeAlarmRule, error) {
	logger.Infof("ENTER AlarmAdmin.CreateNodeAlarmRule: ruleType=%s, name=%s", rule.RuleType, rule.Name)
	defer logger.Info("EXIT AlarmAdmin.CreateNodeAlarmRule")
	return createNodeAlarmRuleInternal(ctx, rule)
}

func getNodeAlarmRulesInternal(ctx context.Context, uuid, ruleType string) ([]model.NodeAlarmRule, error) {
	logger.Infof("ENTER getNodeAlarmRulesInternal: uuid=%s, ruleType=%s", uuid, ruleType)
	defer logger.Info("EXIT getNodeAlarmRulesInternal")
	operator := &AlarmOperator{}
	if uuid != "" {
		return operator.GetNodeAlarmRules(ctx, uuid)
	} else if ruleType != "" {
		return operator.GetNodeAlarmRulesByType(ctx, ruleType)
	} else {
		return operator.GetNodeAlarmRules(ctx, "")
	}
}

func (a *AlarmAdmin) GetNodeAlarmRules(ctx context.Context, uuid, ruleType string) ([]model.NodeAlarmRule, error) {
	logger.Infof("ENTER AlarmAdmin.GetNodeAlarmRules: uuid=%s, ruleType=%s", uuid, ruleType)
	defer logger.Info("EXIT AlarmAdmin.GetNodeAlarmRules")
	return getNodeAlarmRulesInternal(ctx, uuid, ruleType)
}

func deleteNodeAlarmRuleInternal(ctx context.Context, uuid string) (deletedFiles []string, err error) {
	logger.Infof("ENTER deleteNodeAlarmRuleInternal: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT deleteNodeAlarmRuleInternal: error=%v", err)
		} else {
			logger.Infof("EXIT deleteNodeAlarmRuleInternal: deleted %d files", len(deletedFiles))
		}
	}()
	operator := &AlarmOperator{}

	rules, err := operator.GetNodeAlarmRules(ctx, uuid)
	if err != nil {
		return nil, fmt.Errorf("failed to get rule information: %v", err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("node alarm rule not found")
	}

	rule := rules[0]
	if err := operator.DeleteNodeAlarmRules(ctx, uuid); err != nil {
		return nil, fmt.Errorf("failed to delete rule from database: %v", err)
	}

	var templateFiles []string
	switch rule.RuleType {
	case RuleTypeAvailable:
		templateFiles = []string{"node-availability.yml"}
	case RuleTypeControl:
		templateFiles = []string{"management-resources.yml"}
	case RuleTypeCompute:
		templateFiles = []string{"compute-core-resources.yml", "compute-network-resources.yml"}
	case RuleTypeHypervisorVCPU:
		templateFiles = []string{"compute-vcpu-resources.yml"}
	case RuleTypePacketDrop:
		templateFiles = []string{"packet-drop-monitor.yml"}
	case RuleTypeIPBlock:
		templateFiles = []string{"ip-block-monitor.yml"}
	case "ipgroup_available_ip":
		templateFiles = []string{"ipgroup-available-ip-monitor.yml"}
	case "service_monitoring":
		templateFiles = []string{"service_monitoring.yml"}
	}

	deletedFiles = []string{}
	for _, templateFile := range templateFiles {
		outputPath := filepath.Join(RulesGeneral, templateFile)
		enabledPath := filepath.Join(RulesEnabled, templateFile)
		if err := RemoveFile(enabledPath); err != nil {
			logger.Errorf("Failed to remove symlink: path=%s, error=%v", enabledPath, err)
		} else {
			deletedFiles = append(deletedFiles, enabledPath)
		}
		if err := RemoveFile(outputPath); err != nil {
			logger.Errorf("Failed to remove rule file: path=%s, error=%v", outputPath, err)
		} else {
			deletedFiles = append(deletedFiles, outputPath)
		}
	}

	if err := ReloadPrometheusViaHTTP(); err != nil {
		logger.Errorf("Failed to reload Prometheus configuration: error=%v", err)
	}
	return deletedFiles, nil
}

func (a *AlarmAdmin) DeleteNodeAlarmRule(ctx context.Context, uuid string) ([]string, error) {
	logger.Infof("ENTER AlarmAdmin.DeleteNodeAlarmRule: uuid=%s", uuid)
	defer logger.Info("EXIT AlarmAdmin.DeleteNodeAlarmRule")
	return deleteNodeAlarmRuleInternal(ctx, uuid)
}

type NotifyParams struct {
	Alerts []struct {
		State       string            `json:"state"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    time.Time         `json:"startsAt"`
		EndsAt      time.Time         `json:"endsAt"`
	} `json:"alerts"`
}

func (a *AlarmOperator) SendNotification(ctx context.Context, notifyURL string, params NotifyParams) (err error) {
	logger.Infof("ENTER AlarmOperator.SendNotification: notifyURL=%s, alertsCount=%d", notifyURL, len(params.Alerts))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT AlarmOperator.SendNotification: error=%v", err)
		} else {
			logger.Info("EXIT AlarmOperator.SendNotification: success")
		}
	}()
	if notifyURL == "" {
		return nil
	}
	jsonData, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("failed to marshal params: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", notifyURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		logger.Errorf("Request failed with status %d, response: %s", resp.StatusCode, string(body))
		return fmt.Errorf("notification service returned status %d", resp.StatusCode)
	}
	logger.Infof("Successfully sent notification to %s", notifyURL)
	return nil
}

func UpdateMatchedVMsJSON(ctx context.Context, vmUUIDs []string, groupUUID, operation, ruleType string, targetDevice ...string) (err error) {
	logger.Infof("ENTER UpdateMatchedVMsJSON: vmUUIDsCount=%d, groupUUID=%s, operation=%s, ruleType=%s, targetDevice=%v",
		len(vmUUIDs), groupUUID, operation, ruleType, targetDevice)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UpdateMatchedVMsJSON: error=%v", err)
		} else {
			logger.Info("EXIT UpdateMatchedVMsJSON: success")
		}
	}()
	matchedVMsFile := "/etc/prometheus/lists/matched_vms.json"

	var matchedVMs []map[string]interface{}
	existingData, err := ReadFile(matchedVMsFile)
	if err == nil && len(existingData) > 0 {
		if err := json.Unmarshal(existingData, &matchedVMs); err != nil {
			logger.Errorf("Failed to parse existing matched_vms.json: %v", err)
			matchedVMs = []map[string]interface{}{}
		}
	} else {
		matchedVMs = []map[string]interface{}{}
		logger.Infof("Creating new matched_vms.json file")
	}

	if operation == "add" {
		logger.Infof("Adding/updating VM mappings for rule group %s, VM count: %d", groupUUID, len(vmUUIDs))
		var notFoundVMs []string
		for _, instanceid := range vmUUIDs {
			domain, err := GetDomainByInstanceUUID(ctx, instanceid)
			if err != nil {
				logger.Errorf("Failed to get domain for instanceid=%s: %v", instanceid, err)
				notFoundVMs = append(notFoundVMs, instanceid)
				continue
			}

			ruleID := fmt.Sprintf("%s-%s-%s", ruleType, domain, groupUUID)

			var targetDeviceValue string
			if len(targetDevice) > 0 {
				targetDeviceValue = targetDevice[0]
			}

			newEntry := map[string]interface{}{
				"targets": []string{"localhost:9109"},
				"labels": map[string]interface{}{
					"domain":        domain,
					"rule_id":       ruleID,
					"instance_id":   instanceid,
					"target_device": targetDeviceValue,
				},
			}

			entryExists := false
			for i, vm := range matchedVMs {
				labels, ok := vm["labels"].(map[string]interface{})
				if !ok {
					continue
				}
				domainVal, hasDomain := labels["domain"].(string)
				existingRuleID, hasRuleID := labels["rule_id"].(string)
				if hasDomain && hasRuleID && domainVal == domain && existingRuleID == ruleID {
					entryExists = true
					matchedVMs[i] = newEntry
					logger.Infof("Updating existing mapping: domain=%s, rule_id=%s, instance_id=%s", domain, ruleID, instanceid)
					break
				}
			}

			if !entryExists {
				matchedVMs = append(matchedVMs, newEntry)
				logger.Infof("Adding new mapping: domain=%s, rule_id=%s-%s, instance_id=%s", domain, domain, groupUUID, instanceid)
			}
		}

		if len(notFoundVMs) > 0 {
			return fmt.Errorf("instances not found: %v", notFoundVMs)
		}
	} else if operation == "remove" {
		nonEmptyDevices := []string{}
		for _, d := range targetDevice {
			if d != "" {
				nonEmptyDevices = append(nonEmptyDevices, d)
			}
		}
		hasDeviceFilter := len(nonEmptyDevices) > 0

		filteredVMs := []map[string]interface{}{}
		removedCount := 0

		for _, vm := range matchedVMs {
			labels, ok := vm["labels"].(map[string]interface{})
			if !ok {
				filteredVMs = append(filteredVMs, vm)
				continue
			}
			ruleID, ok := labels["rule_id"].(string)
			if !ok {
				filteredVMs = append(filteredVMs, vm)
				continue
			}

			if !hasDeviceFilter {
				if strings.HasSuffix(ruleID, "-"+groupUUID) {
					if len(vmUUIDs) == 0 {
						domain, _ := labels["domain"].(string)
						instanceID, _ := labels["instance_id"].(string)
						logger.Infof("Removing mapping by group(all): domain=%s, rule_id=%s, instance_id=%s", domain, ruleID, instanceID)
						removedCount++
						continue
					}
					instanceID, _ := labels["instance_id"].(string)
					inVM := false
					for _, id := range vmUUIDs {
						if id == instanceID {
							inVM = true
							break
						}
					}
					if inVM {
						domain, _ := labels["domain"].(string)
						logger.Infof("Removing mapping by group: domain=%s, rule_id=%s, instance_id=%s", domain, ruleID, instanceID)
						removedCount++
						continue
					}
				}
				filteredVMs = append(filteredVMs, vm)
				continue
			}

			if !strings.HasSuffix(ruleID, "-"+groupUUID) {
				filteredVMs = append(filteredVMs, vm)
				continue
			}
			instanceID, _ := labels["instance_id"].(string)
			inVM := false
			for _, id := range vmUUIDs {
				if id == instanceID {
					inVM = true
					break
				}
			}
			inDev := false
			for _, d := range nonEmptyDevices {
				if d == labels["target_device"] {
					inDev = true
					break
				}
			}
			if inVM && inDev {
				domain, _ := labels["domain"].(string)
				logger.Infof("Removing mapping by triple: domain=%s, rule_id=%s, instance_id=%s, target_device=%s",
					domain, ruleID, instanceID, labels["target_device"])
				removedCount++
				continue
			}
			filteredVMs = append(filteredVMs, vm)
		}

		matchedVMs = filteredVMs
		logger.Infof("Removed %d mappings for rule group %s", removedCount, groupUUID)
	}

	matchedVMsData, err := json.MarshalIndent(matchedVMs, "", "  ")
	if err != nil {
		logger.Errorf("Failed to marshal matched_vms.json: %v", err)
		return err
	}

	err = WriteFile(matchedVMsFile, matchedVMsData, 0644)
	if err != nil {
		logger.Errorf("Failed to write matched_vms.json: %v", err)
		return err
	}

	if err := ReloadPrometheusViaHTTP(); err != nil {
		logger.Warningf("Warning: Failed to reload Prometheus after updating matched_vms.json: %v", err)
	} else {
		logger.Infof("Successfully reloaded Prometheus configuration after updating matched_vms.json")
	}

	return nil
}

