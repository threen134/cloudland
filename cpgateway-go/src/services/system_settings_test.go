package services

import "testing"

func TestValidateSettingAuditLogRetention(t *testing.T) {
	const key = "AUDIT_LOG_RETENTION_DAYS"
	// JSON numbers decode to float64
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
	// Settings without a range are not checked
	if err := ValidateSetting("PROJECT_NAME", "anything"); err != nil {
		t.Errorf("unconstrained setting should pass: %v", err)
	}
}

func TestValidateSettingDefaultQuotas(t *testing.T) {
	// Count quotas: non-negative whole numbers; 0 is valid (resource disabled by default)
	for _, key := range []string{"DEFAULT_PUBLIC_IPS", "DEFAULT_VPCS", "DEFAULT_LOAD_BALANCERS", "DEFAULT_IMAGES"} {
		for _, ok := range []interface{}{float64(0), float64(5)} {
			if err := ValidateSetting(key, ok); err != nil {
				t.Errorf("%s=%v should be accepted: %v", key, ok, err)
			}
		}
		for _, bad := range []interface{}{float64(-1), 2.5, "5", nil} {
			if err := ValidateSetting(key, bad); err == nil {
				t.Errorf("%s=%#v should be rejected", key, bad)
			}
		}
	}
	// Size quotas: non-negative, fractions allowed
	for _, key := range []string{"DEFAULT_CPU_CORES", "DEFAULT_RAM_GB", "DEFAULT_DISK_GB", "DEFAULT_TRAFFIC_GB"} {
		if err := ValidateSetting(key, 0.5); err != nil {
			t.Errorf("%s=0.5 should be accepted: %v", key, err)
		}
		if err := ValidateSetting(key, float64(-1)); err == nil {
			t.Errorf("%s=-1 should be rejected", key)
		}
	}
}
