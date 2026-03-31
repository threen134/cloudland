package services

import (
	"context"
	"fmt"
	"path/filepath"

	"api/src/common"
	"api/src/model"
)

// RebuildAlarmRulesOnStartup 在 clapi 启动时重建所有启用的告警规则文件到 Prometheus
// 确保 clapi 或 Prometheus 重启后规则不丢失
func RebuildAlarmRulesOnStartup() {
	logger.Info("Starting alarm rules rebuild on startup...")

	ctx := context.Background()
	ctx, db := common.GetContextDB(ctx)

	// 查询所有启用且未软删除的规则组
	var groups []model.RuleGroupV2
	if err := db.Where("enabled = ? AND deleted_at IS NULL", true).Find(&groups).Error; err != nil {
		logger.Errorf("Failed to query enabled rule groups on startup: %v", err)
		return
	}

	if len(groups) == 0 {
		logger.Info("No enabled alarm rules found, skipping rebuild")
		return
	}

	logger.Infof("Found %d enabled alarm rule groups, rebuilding...", len(groups))
	rebuiltCount := 0

	for _, group := range groups {
		var err error
		switch group.Type {
		case RuleTypeCPU:
			err = rebuildCPURule(ctx, &group)
		case RuleTypeMemory:
			err = rebuildMemoryRule(ctx, &group)
		case RuleTypeBW:
			err = rebuildBWRule(ctx, &group)
		default:
			logger.Warningf("Unknown rule type '%s' for group %s, skipping", group.Type, group.UUID)
			continue
		}

		if err != nil {
			logger.Errorf("Failed to rebuild rule %s (type=%s): %v", group.UUID, group.Type, err)
			continue
		}
		rebuiltCount++
	}

	logger.Infof("Alarm rules rebuild complete: %d/%d rules rebuilt", rebuiltCount, len(groups))

	// 全部写完后调用一次 reload
	if rebuiltCount > 0 {
		if err := ReloadPrometheusViaHTTP(); err != nil {
			logger.Errorf("Failed to reload Prometheus after rule rebuild: %v", err)
		}
	}
}

func rebuildCPURule(ctx context.Context, group *model.RuleGroupV2) error {
	operator := &AlarmOperator{}
	details, err := operator.GetCPURuleDetails(ctx, group.UUID)
	if err != nil || len(details) == 0 {
		return fmt.Errorf("no CPU rule details for group %s", group.UUID)
	}

	safeOwner := filepath.Base(group.Owner)
	safeUUID := filepath.Base(group.UUID)

	for i, rule := range details {
		ruleOperator := ">"
		if rule.Rule == "lt" {
			ruleOperator = "<"
		}

		ruleData := map[string]interface{}{
			"owner":            safeOwner,
			"rule_group":       safeUUID,
			"name":             rule.Name,
			"rule_operator":    ruleOperator,
			"limit_value":      rule.Limit,
			"duration_minutes": rule.Duration,
			"rule_id":          fmt.Sprintf("alarm-cpu-%s-%s", safeOwner, safeUUID),
			"global_rule_id":   group.RuleID,
			"region_id":        group.RegionID,
			"level":            rule.Level,
			"detail_index":     i,
			"over":             rule.Over,
			"duration":         rule.Duration,
			"down_to":          rule.DownTo,
			"down_duration":    rule.DownDuration,
		}

		templateFile := "VM-cpu-rule.yml.j2"
		outputFile := fmt.Sprintf("cpu-%s-%s-%d.yml", safeOwner, safeUUID, i)
		if err := ProcessTemplate(templateFile, outputFile, ruleData); err != nil {
			return err
		}
	}
	return nil
}

func rebuildMemoryRule(ctx context.Context, group *model.RuleGroupV2) error {
	operator := &AlarmOperator{}
	details, err := operator.GetMemoryRuleDetails(ctx, group.UUID)
	if err != nil || len(details) == 0 {
		return fmt.Errorf("no Memory rule details for group %s", group.UUID)
	}

	safeOwner := filepath.Base(group.Owner)
	safeUUID := filepath.Base(group.UUID)

	for i, rule := range details {
		ruleOperator := ">"
		if rule.Rule == "lt" {
			ruleOperator = "<"
		}

		ruleData := map[string]interface{}{
			"owner":            safeOwner,
			"rule_group":       safeUUID,
			"name":             rule.Name,
			"rule_operator":    ruleOperator,
			"limit_value":      rule.Limit,
			"duration_minutes": rule.Duration,
			"rule_id":          fmt.Sprintf("alarm-memory-%s-%s", safeOwner, safeUUID),
			"global_rule_id":   group.RuleID,
			"region_id":        group.RegionID,
			"level":            rule.Level,
			"detail_index":     i,
			"over":             rule.Over,
			"duration":         rule.Duration,
			"down_to":          rule.DownTo,
			"down_duration":    rule.DownDuration,
		}

		templateFile := "VM-memory-rule.yml.j2"
		outputFile := fmt.Sprintf("memory-%s-%s-%d.yml", safeOwner, safeUUID, i)
		if err := ProcessTemplate(templateFile, outputFile, ruleData); err != nil {
			return err
		}
	}
	return nil
}

func rebuildBWRule(ctx context.Context, group *model.RuleGroupV2) error {
	operator := &AlarmOperator{}
	details, err := operator.GetBWRuleDetails(ctx, group.UUID)
	if err != nil || len(details) == 0 {
		return fmt.Errorf("no BW rule details for group %s", group.UUID)
	}

	safeOwner := filepath.Base(group.Owner)
	safeUUID := filepath.Base(group.UUID)

	for i, rule := range details {
		data := map[string]interface{}{
			"owner":          safeOwner,
			"rule_group":     safeUUID,
			"global_rule_id": group.RuleID,
			"region_id":      group.RegionID,
			"level":          rule.Level,
			"detail_index":   i,
		}

		var templateFile, outputFile string
		switch rule.Direction {
		case "in":
			data["rule_id"] = fmt.Sprintf("alarm-bw-in-%s-%s", safeOwner, safeUUID)
			data["in_threshold"] = rule.Limit
			data["in_duration"] = rule.Duration
			templateFile = "VM-in-bw-rule.yml.j2"
			outputFile = fmt.Sprintf("bw-in-%s-%s-%d.yml", safeOwner, safeUUID, i)
		case "out":
			data["rule_id"] = fmt.Sprintf("alarm-bw-out-%s-%s", safeOwner, safeUUID)
			data["out_threshold"] = rule.Limit
			data["out_duration"] = rule.Duration
			templateFile = "VM-out-bw-rule.yml.j2"
			outputFile = fmt.Sprintf("bw-out-%s-%s-%d.yml", safeOwner, safeUUID, i)
		default:
			continue
		}

		if err := ProcessTemplate(templateFile, outputFile, data); err != nil {
			return err
		}
	}
	return nil
}
