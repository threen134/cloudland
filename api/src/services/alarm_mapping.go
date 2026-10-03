package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"sync"
	"time"

	"api/src/common"
	"api/src/model"
	"api/src/utils/tracing"
)

// matchedVMsFile is the file_sd target list of the Prometheus job "vm-group-metadata": one entry
// per (VM, rule group) with the rule_id the VM alarm / auto-adjust rule templates match on
// (`up{job="vm-group-metadata", rule_id=~"alarm-cpu-.*-<group>"}`). Prometheus re-reads it on its
// own (refresh_interval 30s), so writing it needs no reload.
const matchedVMsFile = "/etc/prometheus/lists/matched_vms.json"

// matchedVMsMu serializes every read-modify-write of matchedVMsFile in this process: the
// incremental updates done by the create / link / unlink handlers (UpdateMatchedVMsJSON) and the
// full reconcile below. Without it a reconcile that read the database just before a link was
// committed could overwrite the entry the link handler had just added.
var matchedVMsMu sync.Mutex

// vmRuleMappingKinds lists every rule group type that has VMs linked in vm_rule_links.
// prefix is the rule_id prefix the Prometheus templates match on.
var vmRuleMappingKinds = []struct {
	prefix    string
	groupType string
	adjust    bool // adjust_rule_groups instead of rule_group_v2
	iface     bool // bandwidth rules are per NIC and carry target_device
}{
	{"alarm-cpu", RuleTypeCPU, false, false},
	{"alarm-memory", RuleTypeMemory, false, false},
	{"alarm-bw", RuleTypeBW, false, true},
	{"adjust-cpu", model.RuleTypeAdjustCPU, true, false},
	{"adjust-bw", model.RuleTypeAdjustInBW, true, true},
	{"adjust-bw", model.RuleTypeAdjustOutBW, true, true},
}

// fileSDTarget is one entry of matchedVMsFile.
type fileSDTarget struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// isSameMapping reports whether an existing matched_vms.json entry is the one identified by
// (domain, rule_id, target_device). target_device is part of the key: a VM linked to a bandwidth
// rule on two NICs has one entry per NIC, as the reconcile writes it. Keying on (domain, rule_id)
// alone made the second NIC's "add" replace the first one's entry.
func isSameMapping(labels map[string]interface{}, domain, ruleID, targetDevice string) bool {
	d, _ := labels["domain"].(string)
	r, _ := labels["rule_id"].(string)
	t, _ := labels["target_device"].(string)
	return d == domain && r == ruleID && t == targetDevice
}

// vmRuleMappingSource is what a full rebuild reads. An interface so the tests can inject failures.
type vmRuleMappingSource interface {
	// GroupUUIDs returns every (not deleted) rule group of the given type, without paging limits.
	GroupUUIDs(ctx context.Context, groupType string, adjust bool) ([]string, error)
	// LinkedVMs returns the links of all the given groups in one query, keyed by group UUID.
	LinkedVMs(ctx context.Context, groupUUIDs []string) (map[string][]model.VMRuleLink, error)
	// InstanceDomains maps instance UUID -> libvirt domain ("inst-<id>"); deleted instances are absent.
	InstanceDomains(ctx context.Context, instanceUUIDs []string) (map[string]string, error)
}

type dbVMRuleMappingSource struct{}

func (dbVMRuleMappingSource) GroupUUIDs(ctx context.Context, groupType string, adjust bool) (uuids []string, err error) {
	_, db := common.GetContextDB(ctx)
	var table interface{} = &model.RuleGroupV2{}
	if adjust {
		table = &model.AdjustRuleGroup{}
	}
	err = db.Model(table).Where("type = ?", groupType).Order("id").Pluck("uuid", &uuids).Error
	return
}

func (dbVMRuleMappingSource) LinkedVMs(ctx context.Context, groupUUIDs []string) (map[string][]model.VMRuleLink, error) {
	byGroup := make(map[string][]model.VMRuleLink, len(groupUUIDs))
	if len(groupUUIDs) == 0 {
		return byGroup, nil
	}
	_, db := common.GetContextDB(ctx)
	var links []model.VMRuleLink
	if err := db.Where("group_uuid IN ?", groupUUIDs).Order("id").Find(&links).Error; err != nil {
		return nil, err
	}
	for _, l := range links {
		byGroup[l.GroupUUID] = append(byGroup[l.GroupUUID], l)
	}
	return byGroup, nil
}

func (dbVMRuleMappingSource) InstanceDomains(ctx context.Context, instanceUUIDs []string) (map[string]string, error) {
	domains := make(map[string]string, len(instanceUUIDs))
	if len(instanceUUIDs) == 0 {
		return domains, nil
	}
	_, db := common.GetContextDB(ctx)
	var rows []struct {
		ID   int64
		UUID string
	}
	if err := db.Model(&model.Instance{}).Select("id, uuid").Where("uuid IN ?", instanceUUIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		// Same format as GetDomainByInstanceUUID
		domains[r.UUID] = fmt.Sprintf("inst-%d", r.ID)
	}
	return domains, nil
}

// buildVMRuleMappings computes the full content of matchedVMsFile from the database, with three
// queries per rule type whatever the number of groups (it runs under matchedVMsMu, which blocks
// the incremental updates meanwhile).
// Any query error aborts the whole build: writing a partial list would drop every mapping of
// the rule types that failed and silently stop their alarms.
func buildVMRuleMappings(ctx context.Context, src vmRuleMappingSource) (targets []fileSDTarget, stats map[string]int, err error) {
	stats = make(map[string]int)
	targets = []fileSDTarget{}
	for _, kind := range vmRuleMappingKinds {
		groups, err := src.GroupUUIDs(ctx, kind.groupType, kind.adjust)
		if err != nil {
			return nil, nil, fmt.Errorf("list %s rule groups: %w", kind.groupType, err)
		}
		stats[kind.prefix] += len(groups)
		linksByGroup, err := src.LinkedVMs(ctx, groups)
		if err != nil {
			return nil, nil, fmt.Errorf("list VMs linked to %s rule groups: %w", kind.groupType, err)
		}
		var uuids []string
		for _, links := range linksByGroup {
			for _, l := range links {
				uuids = append(uuids, l.VMUUID)
			}
		}
		domains, err := src.InstanceDomains(ctx, uuids)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve domains for %s rule groups: %w", kind.groupType, err)
		}
		for _, group := range groups {
			for _, l := range linksByGroup[group] {
				domain, ok := domains[l.VMUUID]
				if !ok {
					// The instance was deleted: its mapping is dropped (the link row is left behind)
					continue
				}
				labels := map[string]string{
					"domain":      domain,
					"rule_id":     fmt.Sprintf("%s-%s-%s", kind.prefix, domain, group),
					"instance_id": l.VMUUID,
				}
				if kind.iface && l.Interface != "" {
					labels["target_device"] = l.Interface
				}
				targets = append(targets, fileSDTarget{Targets: []string{"localhost:9090"}, Labels: labels})
			}
		}
	}
	sortFileSDTargets(targets)
	return targets, stats, nil
}

// sortFileSDTargets gives a stable order so identical content serializes identically
// (and HA replicas reconciling at the same time write the same bytes).
func sortFileSDTargets(targets []fileSDTarget) {
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i].Labels, targets[j].Labels
		if a["rule_id"] != b["rule_id"] {
			return a["rule_id"] < b["rule_id"]
		}
		if a["target_device"] != b["target_device"] {
			return a["target_device"] < b["target_device"]
		}
		return a["instance_id"] < b["instance_id"]
	})
}

// sameFileSDTargets compares the current file with the rebuilt list semantically: the
// incremental updates write keys in another order and an empty target_device on non-bandwidth
// rules, which must not count as a difference.
func sameFileSDTargets(current []byte, want []fileSDTarget) bool {
	var have []fileSDTarget
	if err := json.Unmarshal(current, &have); err != nil {
		return false
	}
	for i := range have {
		for k, v := range have[i].Labels {
			if v == "" {
				delete(have[i].Labels, k)
			}
		}
	}
	sortFileSDTargets(have)
	if len(have) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(have, want)
}

// VMRuleMappingSyncResult is what SyncVMRuleMappings did.
type VMRuleMappingSyncResult struct {
	Count   int            // mapping entries in the file
	Stats   map[string]int // rule groups per rule_id prefix
	Written bool           // false when the file already matched the database
}

// SyncVMRuleMappings rebuilds matchedVMsFile from the database and writes it when it differs
// from what is there (always when force). Nothing is written if the rebuild fails.
func SyncVMRuleMappings(ctx context.Context, force bool) (result VMRuleMappingSyncResult, err error) {
	return syncVMRuleMappings(ctx, dbVMRuleMappingSource{}, ReadFile, WriteFile, force)
}

func syncVMRuleMappings(
	ctx context.Context,
	src vmRuleMappingSource,
	read func(context.Context, string) ([]byte, error),
	write func(context.Context, string, []byte, os.FileMode) error,
	force bool,
) (result VMRuleMappingSyncResult, err error) {
	matchedVMsMu.Lock()
	defer matchedVMsMu.Unlock()

	targets, stats, err := buildVMRuleMappings(ctx, src)
	if err != nil {
		return result, err
	}
	result.Count, result.Stats = len(targets), stats
	if !force {
		// A read error (file missing, alarm-rules-manager briefly down) just means "rewrite it"
		if current, rerr := read(ctx, matchedVMsFile); rerr == nil && sameFileSDTargets(current, targets) {
			return result, nil
		}
	}
	data, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return result, err
	}
	if err = write(ctx, matchedVMsFile, data, 0644); err != nil {
		return result, fmt.Errorf("write %s: %w", matchedVMsFile, err)
	}
	result.Written = true
	return result, nil
}

// vmRuleMappingSyncInterval is how often the background reconcile runs; VM_RULE_MAPPING_SYNC_INTERVAL
// (a Go duration, e.g. "1m") overrides it, mainly for testing.
func vmRuleMappingSyncInterval() time.Duration {
	if v := os.Getenv("VM_RULE_MAPPING_SYNC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 10*time.Second {
			return d
		}
		logger.Warningf("Ignoring invalid VM_RULE_MAPPING_SYNC_INTERVAL=%q", v)
	}
	return 10 * time.Minute
}

// StartVMRuleMappingReconciler keeps matchedVMsFile in line with the database. The create / link
// / unlink handlers update it incrementally; this catches whatever they missed: the file lost with
// the Prometheus volume, an incremental update that failed (a failed read makes it start from an
// empty list), mappings of deleted VMs. It replaces the manual "sync mappings" button.
// First run shortly after start (Prometheus and alarm-rules-manager come up with clapi), then every
// interval; a failed run is retried after a minute.
func StartVMRuleMappingReconciler() {
	interval := vmRuleMappingSyncInterval()
	go func() {
		delay := 30 * time.Second
		for {
			time.Sleep(delay)
			delay = interval
			if err := reconcileVMRuleMappingsOnce(); err != nil {
				delay = time.Minute
			}
		}
	}()
}

func reconcileVMRuleMappingsOnce() error {
	ctx, span := tracing.StartBackground(context.Background(), "alarm.reconcile_vm_rule_mappings")
	defer span.End()
	result, err := SyncVMRuleMappings(ctx, false)
	if err != nil {
		logger.Ctx(ctx).Errorf("VM rule mapping reconcile failed, file left unchanged: %v", err)
		return err
	}
	if result.Written {
		logger.Ctx(ctx).Infof("VM rule mappings were out of date, rewrote %s: %d entries, groups %v", matchedVMsFile, result.Count, result.Stats)
	}
	return nil
}
