package services

import "testing"

func TestValidateSettingAuditLogRetention(t *testing.T) {
	const key = "AUDIT_LOG_RETENTION_DAYS"
	// JSON 数字解码后是 float64
	for _, ok := range []interface{}{float64(90), float64(365), float64(3650)} {
		if err := ValidateSetting(key, ok); err != nil {
			t.Errorf("value %v should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []interface{}{float64(89), float64(3651), float64(0), float64(-1), 365.5, "365", nil, true} {
		if err := ValidateSetting(key, bad); err == nil {
			t.Errorf("value %#v should be rejected", bad)
		}
	}
	// 无范围约束的设置不做检查
	if err := ValidateSetting("PROJECT_NAME", "anything"); err != nil {
		t.Errorf("unconstrained setting should pass: %v", err)
	}
}
