package services

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"api/src/common"
	"api/src/model"
	"api/src/utils/tracing"
)

// Auto-adjust rule templates (apis/adjust.go refers to these as well)
const (
	CPUAdjustRuleTemplate        = "VM-cpu-adjust-rule.yml.j2"
	ResourceAdjustAlertsTemplate = "resource-adjustment-alerts.yml.j2"
	InBWAdjustRuleTemplate       = "VM-in-bw-adjust-rule.yml.j2"
	OutBWAdjustRuleTemplate      = "VM-out-bw-adjust-rule.yml.j2"
)

// startupRebuildAttempts bounds the retries of the startup rule rebuild (one round a minute).
const startupRebuildAttempts = 10

// RebuildAlarmRulesOnStartup re-renders the rule files of every enabled VM alarm rule group and,
// since 2026-09-21, every enabled resource auto-adjust rule group, so they survive a clapi or
// Prometheus restart and a reset Prometheus volume (the VM <-> rule group mapping file is kept up
// to date separately by StartVMRuleMappingReconciler).
// It runs in the background and retries: clapi does not wait for alarm-rules-manager, which itself
// waits for Prometheus to be healthy, so on a full stack start the first round usually cannot write
// anything. A round with any failure (query, render / write, reload) is retried after a minute.
func RebuildAlarmRulesOnStartup() {
	go func() {
		for attempt := 1; ; attempt++ {
			failed := rebuildAllRuleFiles()
			if failed == 0 {
				return
			}
			if attempt >= startupRebuildAttempts {
				logger.Errorf("Startup rule rebuild gave up after %d rounds, %d rule groups still failing", attempt, failed)
				return
			}
			logger.Warningf("Startup rule rebuild: %d failures, retrying in a minute (round %d/%d)", failed, attempt, startupRebuildAttempts)
			time.Sleep(time.Minute)
		}
	}()
}

// rebuildAllRuleFiles runs one rebuild round and returns the number of failures.
func rebuildAllRuleFiles() int {
	ctx, span := tracing.StartBackground(context.Background(), "alarm.rebuild_rule_files")
	defer span.End()
	logger.Ctx(ctx).Info("Rebuilding alarm and auto-adjust rule files...")

	alarmRebuilt, alarmFailed := rebuildAlarmRuleFiles(ctx)
	adjustRebuilt, adjustFailed := rebuildAdjustRuleFiles(ctx)
	failed := alarmFailed + adjustFailed

	// Rule files only take effect on a reload: one reload once everything is written
	if alarmRebuilt+adjustRebuilt > 0 {
		if err := ReloadPrometheusViaHTTP(ctx); err != nil {
			logger.Ctx(ctx).Errorf("Failed to reload Prometheus after rule rebuild: %v", err)
			failed++
		}
	}
	return failed
}

// rebuildAlarmRuleFiles re-renders every enabled VM alarm rule group (CPU / memory / bandwidth).
func rebuildAlarmRuleFiles(ctx context.Context) (rebuilt, failed int) {
	_, db := common.GetContextDB(ctx)

	// Every enabled, not deleted rule group
	var groups []model.RuleGroupV2
	if err := db.Where("enabled = ? AND deleted_at IS NULL", true).Find(&groups).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query enabled rule groups on startup: %v", err)
		return 0, 1
	}

	if len(groups) == 0 {
		logger.Ctx(ctx).Info("No enabled alarm rules found, skipping rebuild")
		return 0, 0
	}

	logger.Ctx(ctx).Infof("Found %d enabled alarm rule groups, rebuilding...", len(groups))
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
			logger.Ctx(ctx).Warningf("Unknown rule type '%s' for group %s, skipping", group.Type, group.UUID)
			continue
		}

		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to rebuild rule %s (type=%s): %v", group.UUID, group.Type, err)
			failed++
			continue
		}
		rebuiltCount++
	}

	logger.Ctx(ctx).Infof("Alarm rules rebuild complete: %d/%d rules rebuilt", rebuiltCount, len(groups))
	return rebuiltCount, failed
}

// rebuildAdjustRuleFiles re-renders every enabled resource auto-adjust rule group. These files
// were only ever written by the create / update handlers, so a reset Prometheus volume used to
// leave the auto-adjust rules silently gone until someone edited them.
func rebuildAdjustRuleFiles(ctx context.Context) (rebuilt, failed int) {
	_, db := common.GetContextDB(ctx)
	var groups []model.AdjustRuleGroup
	if err := db.Where("enabled = ?", true).Order("id").Find(&groups).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query enabled adjust rule groups on startup: %v", err)
		return 0, 1
	}
	for i := range groups {
		var err error
		switch groups[i].Type {
		case model.RuleTypeAdjustCPU:
			err = rebuildCPUAdjustRule(ctx, &groups[i])
		case model.RuleTypeAdjustInBW, model.RuleTypeAdjustOutBW:
			err = rebuildBWAdjustRule(ctx, &groups[i])
		default:
			logger.Ctx(ctx).Warningf("Unknown adjust rule type '%s' for group %s, skipping", groups[i].Type, groups[i].UUID)
			continue
		}
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to rebuild adjust rule %s (type=%s): %v", groups[i].UUID, groups[i].Type, err)
			failed++
			continue
		}
		rebuilt++
	}
	logger.Ctx(ctx).Infof("Adjust rules rebuild complete: %d/%d rule groups rebuilt", rebuilt, len(groups))
	return rebuilt, failed
}

// adjustRuleBaseData holds the template variables every auto-adjust rule and its alerts file use,
// the same values the create / update handlers in apis/adjust.go pass.
func adjustRuleBaseData(group *model.AdjustRuleGroup, smoothWindow, triggerDuration, limitDuration int) map[string]interface{} {
	return map[string]interface{}{
		"rule_group":          strings.ReplaceAll(group.UUID, "-", "_"),
		"rule_group_original": group.UUID,
		"global_rule_id":      group.RuleID,
		"smooth_window":       smoothWindow,
		"trigger_duration":    triggerDuration,
		"limit_duration":      limitDuration,
		"owner":               group.Owner,
		"notify_url":          group.NotifyURL,
		"region_id":           group.RegionID,
	}
}

func rebuildCPUAdjustRule(ctx context.Context, group *model.AdjustRuleGroup) error {
	details, err := (&AdjustOperator{}).GetCPUAdjustRuleDetails(ctx, group.UUID)
	if err != nil {
		return fmt.Errorf("load CPU adjust rule details of group %s: %w", group.UUID, err)
	}
	if len(details) == 0 {
		return fmt.Errorf("no CPU adjust rule details for group %s", group.UUID)
	}
	sortByID(details, func(d model.CPUAdjustRuleDetail) int64 { return d.ID })
	d := details[0]
	data := adjustRuleBaseData(group, d.SmoothWindow, d.TriggerDuration, d.LimitDuration)
	data["high_threshold"] = d.HighThreshold
	data["limit_percent"] = d.LimitPercent
	uuid := filepath.Base(group.UUID)
	if err := ProcessTemplate(ctx, CPUAdjustRuleTemplate, fmt.Sprintf("cpu-adjust-%s-%s.yml", group.Owner, uuid), data); err != nil {
		return err
	}
	return ProcessTemplate(ctx, ResourceAdjustAlertsTemplate, fmt.Sprintf("resource-adjust-alerts-%s-%s.yml", group.Owner, uuid), data)
}

// rebuildBWAdjustRule renders one recording rule file per direction the group has, and the alerts
// file once with the first detail's parameters (the create handler uses its first submitted rule; this
// uses the first stored one, by ID).
func rebuildBWAdjustRule(ctx context.Context, group *model.AdjustRuleGroup) error {
	details, err := (&AdjustOperator{}).GetBWAdjustRuleDetails(ctx, group.UUID)
	if err != nil {
		return fmt.Errorf("load bandwidth adjust rule details of group %s: %w", group.UUID, err)
	}
	if len(details) == 0 {
		return fmt.Errorf("no bandwidth adjust rule details for group %s", group.UUID)
	}
	// The query has no ORDER BY: without this "the first detail" (which the alerts file is built
	// from) could change between restarts
	sortByID(details, func(d model.BWAdjustRuleDetail) int64 { return d.ID })
	uuid := filepath.Base(group.UUID)
	for _, d := range details {
		data := adjustRuleBaseData(group, d.SmoothWindow, d.TriggerDuration, d.LimitDuration)
		data["high_threshold_pct"] = d.HighThresholdPct
		data["limit_value_pct"] = d.LimitValuePct
		data["rule_id"] = fmt.Sprintf("adjust-bw-%s-%s-%s", d.Direction, group.Owner, group.UUID)
		data[d.Direction+"_high_threshold_pct"] = d.HighThresholdPct
		data[d.Direction+"_limit_value_pct"] = d.LimitValuePct
		data["in_enabled"] = d.Direction == "in"
		data["out_enabled"] = d.Direction == "out"
		var template string
		switch d.Direction {
		case "in":
			template = InBWAdjustRuleTemplate
		case "out":
			template = OutBWAdjustRuleTemplate
		default:
			return fmt.Errorf("invalid direction %q in bandwidth adjust rule group %s", d.Direction, group.UUID)
		}
		if err := ProcessTemplate(ctx, template, fmt.Sprintf("bw-%s-adjust-%s-%s.yml", d.Direction, group.Owner, uuid), data); err != nil {
			return err
		}
	}
	first := details[0]
	alerts := adjustRuleBaseData(group, first.SmoothWindow, first.TriggerDuration, first.LimitDuration)
	return ProcessTemplate(ctx, ResourceAdjustAlertsTemplate, fmt.Sprintf("resource-adjust-alerts-%s-%s.yml", group.Owner, uuid), alerts)
}

func rebuildCPURule(ctx context.Context, group *model.RuleGroupV2) error {
	operator := &AlarmOperator{}
	details, err := operator.GetCPURuleDetails(ctx, group.UUID)
	if err != nil || len(details) == 0 {
		return fmt.Errorf("no CPU rule details for group %s", group.UUID)
	}

	safeOwner := strconv.FormatInt(group.Owner, 10)
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
			"duration":         rule.Duration,
		}

		templateFile := "VM-cpu-rule.yml.j2"
		outputFile := fmt.Sprintf("cpu-%s-%s-%d.yml", safeOwner, safeUUID, i)
		if err := ProcessTemplate(ctx, templateFile, outputFile, ruleData); err != nil {
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

	safeOwner := strconv.FormatInt(group.Owner, 10)
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
			"duration":         rule.Duration,
		}

		templateFile := "VM-memory-rule.yml.j2"
		outputFile := fmt.Sprintf("memory-%s-%s-%d.yml", safeOwner, safeUUID, i)
		if err := ProcessTemplate(ctx, templateFile, outputFile, ruleData); err != nil {
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

	safeOwner := strconv.FormatInt(group.Owner, 10)
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

		if err := ProcessTemplate(ctx, templateFile, outputFile, data); err != nil {
			return err
		}
	}
	return nil
}

// sortByID orders rule details by primary key, i.e. creation order.
func sortByID[T any](items []T, id func(T) int64) {
	sort.SliceStable(items, func(i, j int) bool { return id(items[i]) < id(items[j]) })
}
