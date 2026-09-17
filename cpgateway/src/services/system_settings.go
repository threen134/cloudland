package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"gorm.io/gorm"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

const MaskedValue = "******"

// SettingMeta mirrors SETTINGS_METADATA: (value_type, category, description, is_secret, default_value_fn).
type SettingMeta struct {
	Key         string
	ValueType   string
	Category    string
	Description string
	IsSecret    bool
	Default     func() interface{}
}

func cfgString(key, def string) func() interface{} {
	return func() interface{} {
		if v := viper.GetString(key); v != "" {
			return v
		}
		return def
	}
}

func cfgNumber(key string, def float64) func() interface{} {
	return func() interface{} { return configFloat(key, def) }
}

func cfgInt(key string, def int) func() interface{} {
	return func() interface{} {
		if viper.IsSet(key) && viper.GetInt(key) != 0 {
			return viper.GetInt(key)
		}
		return def
	}
}

func constant(v interface{}) func() interface{} {
	return func() interface{} { return v }
}

var SettingsMetadata = []SettingMeta{
	{"FRONTEND_URL", "string", "general", "前端访问地址", false, cfgString("frontend.url", "")},
	{"ALARM_EVENT_RETENTION_DAYS", "number", "general", "VM 告警事件保留天数", false, constant(30)},
	{"AUDIT_LOG_RETENTION_DAYS", "number", "general", "操作审计日志保留天数", false, constant(DefaultAuditLogRetentionDays)},
	{"DNS_UPSTREAM", "string", "general", "内部 DNS 上游转发地址（计算节点 hostname 未匹配时转发至此）", false, cfgString("dns.upstream", "8.8.8.8")},
	{"DEFAULT_CPU_CORES", "number", "quota", "默认 CPU 配额（核）", false, cfgNumber("quota.defaults.cpu_cores", 4.0)},
	{"DEFAULT_RAM_GB", "number", "quota", "默认内存配额（GB）", false, cfgNumber("quota.defaults.ram_gb", 8.0)},
	{"DEFAULT_DISK_GB", "number", "quota", "默认磁盘配额（GB）", false, cfgNumber("quota.defaults.disk_gb", 50.0)},
	{"DEFAULT_PUBLIC_IPS", "number", "quota", "默认公网 IP 配额", false, cfgInt("quota.defaults.public_ips", 2)},
	{"DEFAULT_VPCS", "number", "quota", "默认 VPC 配额（个）", false, cfgInt("quota.defaults.vpcs", DefaultQuotaVPCs)},
	{"DEFAULT_LOAD_BALANCERS", "number", "quota", "默认负载均衡配额（个）", false, cfgInt("quota.defaults.load_balancers", DefaultQuotaLoadBalancers)},
	{"DEFAULT_IMAGES", "number", "quota", "默认镜像配额（个）", false, cfgInt("quota.defaults.images", DefaultQuotaImages)},
	{"NOTIFICATION_CHANNELS", "json", "notification", "启用的通知渠道列表", false, constant([]interface{}{"email"})},
	{"SMTP_HOST", "string", "notification", "SMTP 主机", false, cfgString("smtp.host", "")},
	{"SMTP_PORT", "number", "notification", "SMTP 端口", false, cfgInt("smtp.port", 587)},
	{"SMTP_TLS", "boolean", "notification", "启用 TLS", false, func() interface{} {
		if viper.IsSet("smtp.tls") {
			return viper.GetBool("smtp.tls")
		}
		return true
	}},
	{"SMTP_USER", "string", "notification", "SMTP 用户名", false, cfgString("smtp.user", "")},
	{"SMTP_PASSWORD", "secret", "notification", "SMTP 密码", true, cfgString("smtp.password", "")},
	{"SMTP_FROM", "string", "notification", "发件人邮箱地址", false, cfgString("smtp.from", "")},
	{"SMTP_FROM_NAME", "string", "notification", "发件人名称", false, cfgString("smtp.from_name", "CloudLand")},
	{"FEISHU_WEBHOOK_URL", "string", "notification", "飞书 Webhook 地址", false, cfgString("feishu.webhook_url", "")},
	{"FEISHU_SECRET", "secret", "notification", "飞书签名密钥", true, cfgString("feishu.secret", "")},
}

const DefaultAuditLogRetentionDays = 365

// settingRange is the allowed value range (inclusive) of a numeric setting.
type settingRange struct {
	Min, Max float64
	Integer  bool
}

// settingRanges lists numeric settings with an allowed range. Audit log retention has a lower bound so the audit
// trail cannot be wiped by mistake (clapi applies the same range when reading it). Default quotas must be
// non-negative, and count quotas whole numbers, matching the per-org quota update API.
var settingRanges = map[string]settingRange{
	"AUDIT_LOG_RETENTION_DAYS": {90, 3650, true},
	"DEFAULT_CPU_CORES":        {0, 1e6, false},
	"DEFAULT_RAM_GB":           {0, 1e7, false},
	"DEFAULT_DISK_GB":          {0, 1e9, false},
	"DEFAULT_PUBLIC_IPS":       {0, 1e5, true},
	"DEFAULT_VPCS":             {0, 1e5, true},
	"DEFAULT_LOAD_BALANCERS":   {0, 1e5, true},
	"DEFAULT_IMAGES":           {0, 1e5, true},
}

// ValidateSetting validates a single setting value; only numeric settings with a range are checked
func ValidateSetting(key string, value interface{}) error {
	r, ok := settingRanges[key]
	if !ok {
		return nil
	}
	n, isNum := value.(float64)
	if !isNum {
		return fmt.Errorf("%s must be a number", key)
	}
	if r.Integer && n != float64(int64(n)) {
		return fmt.Errorf("%s must be an integer", key)
	}
	if n < r.Min || n > r.Max {
		return fmt.Errorf("%s must be between %s and %s", key,
			strconv.FormatFloat(r.Min, 'f', -1, 64), strconv.FormatFloat(r.Max, 'f', -1, 64))
	}
	return nil
}

func FindSettingMeta(key string) (*SettingMeta, bool) {
	for i := range SettingsMetadata {
		if SettingsMetadata[i].Key == key {
			return &SettingsMetadata[i], true
		}
	}
	return nil, false
}

// SerializeSetting mirrors json.dumps(value, ensure_ascii=False).
func SerializeSetting(v interface{}) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// DeserializeSetting mirrors _deserialize: JSON decode, falling back to the raw string.
func DeserializeSetting(raw *string) interface{} {
	if raw == nil {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal([]byte(*raw), &v); err != nil {
		return *raw
	}
	return v
}

// Truthy mirrors Python truthiness for JSON-like values.
func Truthy(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case []interface{}:
		return len(t) > 0
	case map[string]interface{}:
		return len(t) > 0
	}
	return true
}

func ToFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// GetSetting returns a setting value with DB → config → default fallback (no masking).
func GetSetting(db *gorm.DB, key string) interface{} {
	var row model.SystemSetting
	if err := db.Where("key = ?", key).Limit(1).Find(&row).Error; err == nil && row.Key != "" && row.Value != nil {
		return DeserializeSetting(row.Value)
	}
	if meta, ok := FindSettingMeta(key); ok {
		return meta.Default()
	}
	return nil
}

// GetSettingString mirrors `await settings_service.get(db, key) or fallback`.
func GetSettingString(db *gorm.DB, key, fallback string) string {
	v := GetSetting(db, key)
	if !Truthy(v) {
		return fallback
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// GetSettingInt mirrors `int(await settings_service.get(db, key) or fallback)`.
func GetSettingInt(db *gorm.DB, key string, fallback int) int {
	v := GetSetting(db, key)
	if !Truthy(v) {
		return fallback
	}
	if f, ok := ToFloat(v); ok {
		return int(f)
	}
	return fallback
}

// GetAllSettings returns stored rows, filling unstored metadata keys with virtual default rows.
func GetAllSettings(db *gorm.DB) []model.SystemSetting {
	var stored []model.SystemSetting
	db.Find(&stored)
	storedMap := make(map[string]model.SystemSetting, len(stored))
	for _, s := range stored {
		storedMap[s.Key] = s
	}

	result := make([]model.SystemSetting, 0, len(SettingsMetadata))
	for _, meta := range SettingsMetadata {
		if row, ok := storedMap[meta.Key]; ok {
			result = append(result, row)
			continue
		}
		value := SerializeSetting(meta.Default())
		desc := meta.Description
		result = append(result, model.SystemSetting{
			Key:         meta.Key,
			Value:       &value,
			ValueType:   meta.ValueType,
			Category:    meta.Category,
			Description: &desc,
			IsSecret:    meta.IsSecret,
		})
	}
	return result
}

func getConfigVersion(db *gorm.DB) int64 {
	var row model.SystemConfigVersion
	if err := db.Where("id = ?", 1).Limit(1).Find(&row).Error; err != nil || row.ID == 0 {
		return 0
	}
	return row.Version
}

func incrementConfigVersion(tx *gorm.DB) (int64, error) {
	res := tx.Model(&model.SystemConfigVersion{}).Where("id = ?", 1).
		Update("version", gorm.Expr("version + 1"))
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		if err := tx.Create(&model.SystemConfigVersion{ID: 1, Version: 1}).Error; err != nil {
			return 0, err
		}
	}
	return getConfigVersion(tx), nil
}

// BulkUpdateSettings updates known keys (skipping masked secrets) and bumps the config version once.
// Callers must pass only keys that exist in SettingsMetadata.
func BulkUpdateSettings(db *gorm.DB, updates map[string]interface{}) (int64, error) {
	tx := db.Begin()
	for key, newValue := range updates {
		meta, ok := FindSettingMeta(key)
		if !ok {
			continue
		}
		if s, isStr := newValue.(string); isStr && meta.IsSecret && s == MaskedValue {
			continue
		}
		serialized := SerializeSetting(newValue)

		var existing model.SystemSetting
		if err := tx.Where("key = ?", key).Limit(1).Find(&existing).Error; err != nil {
			tx.Rollback()
			return 0, err
		}
		if existing.Key != "" {
			if err := tx.Model(&model.SystemSetting{}).Where("key = ?", key).
				Update("value", serialized).Error; err != nil {
				tx.Rollback()
				return 0, err
			}
			continue
		}
		desc := meta.Description
		if err := tx.Create(&model.SystemSetting{
			Key:         key,
			Value:       &serialized,
			ValueType:   meta.ValueType,
			Category:    meta.Category,
			Description: &desc,
			IsSecret:    meta.IsSecret,
		}).Error; err != nil {
			tx.Rollback()
			return 0, err
		}
	}

	version, err := incrementConfigVersion(tx)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	return version, tx.Commit().Error
}

// GetSettingsSyncPayload builds the clapi sync payload (secrets in plaintext, values JSON-serialized).
func GetSettingsSyncPayload(db *gorm.DB, force bool) map[string]interface{} {
	rows := GetAllSettings(db)
	settings := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		settings = append(settings, map[string]interface{}{
			"key":        r.Key,
			"value":      r.Value,
			"value_type": r.ValueType,
			"category":   r.Category,
			"is_secret":  r.IsSecret,
		})
	}
	return map[string]interface{}{
		"config_version": getConfigVersion(db),
		"settings":       settings,
		"force":          force,
	}
}

// PushSettingsToAllRegions fans out the full settings snapshot to all sync target regions.
func PushSettingsToAllRegions(ctx context.Context) {
	db := dbs.DBContext(ctx)
	regions := SyncTargetRegions(db)
	if len(regions) == 0 {
		return
	}
	payload := GetSettingsSyncPayload(db, false)
	forEachRegion(regions, func(r *model.Region) {
		if err := pushToRegion(ctx, r, "/internal/system-settings/sync", payload); err != nil {
			log.WithContext(ctx).Errorf("Failed to sync system settings to region '%s': %v", r.Name, err)
		}
	})
}

// PushSettingsToRegion force-pushes all settings to one region (provisioning / recovery).
func PushSettingsToRegion(ctx context.Context, db *gorm.DB, region *model.Region) {
	payload := GetSettingsSyncPayload(db, true)
	if err := pushToRegion(ctx, region, "/internal/system-settings/sync", payload); err != nil {
		log.WithContext(ctx).Errorf("Full system settings sync to region '%s' failed: %v", region.Name, err)
		return
	}
	log.WithContext(ctx).Infof("Full system settings sync to region '%s' completed (version=%v)", region.Name, payload["config_version"])
}
