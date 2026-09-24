package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"api/src/model"
)

type fakeMappingSource struct {
	groups  map[string][]string // group type -> group UUIDs
	links   map[string][]model.VMRuleLink
	domains map[string]string // instance UUID -> domain; missing = deleted instance
	failOn  string            // "groups:<type>", "links:<group>" or "domains"
}

func (f *fakeMappingSource) GroupUUIDs(_ context.Context, groupType string, _ bool) ([]string, error) {
	if f.failOn == "groups:"+groupType {
		return nil, errors.New("db down")
	}
	return f.groups[groupType], nil
}

func (f *fakeMappingSource) LinkedVMs(_ context.Context, groups []string) (map[string][]model.VMRuleLink, error) {
	out := map[string][]model.VMRuleLink{}
	for _, g := range groups {
		if f.failOn == "links:"+g {
			return nil, errors.New("db down")
		}
		out[g] = f.links[g]
	}
	return out, nil
}

func (f *fakeMappingSource) InstanceDomains(_ context.Context, uuids []string) (map[string]string, error) {
	if f.failOn == "domains" {
		return nil, errors.New("db down")
	}
	out := map[string]string{}
	for _, u := range uuids {
		if d, ok := f.domains[u]; ok {
			out[u] = d
		}
	}
	return out, nil
}

func newFakeMappingSource() *fakeMappingSource {
	return &fakeMappingSource{
		groups: map[string][]string{
			RuleTypeCPU:               {"g-cpu"},
			RuleTypeBW:                {"g-bw"},
			model.RuleTypeAdjustOutBW: {"g-adj"},
		},
		links: map[string][]model.VMRuleLink{
			"g-cpu": {{VMUUID: "vm-b"}, {VMUUID: "vm-a"}, {VMUUID: "vm-deleted"}},
			"g-bw":  {{VMUUID: "vm-a", Interface: "tap1"}, {VMUUID: "vm-a", Interface: "tap0"}},
			"g-adj": {{VMUUID: "vm-b", Interface: "tap2"}},
		},
		domains: map[string]string{"vm-a": "inst-1", "vm-b": "inst-2"},
	}
}

type fakeMappingFile struct {
	content []byte
	readErr error
	writes  int
}

func (f *fakeMappingFile) read(context.Context, string) ([]byte, error) { return f.content, f.readErr }
func (f *fakeMappingFile) write(_ context.Context, _ string, data []byte, _ os.FileMode) error {
	f.writes++
	f.content = data
	return nil
}

func TestBuildVMRuleMappings(t *testing.T) {
	targets, stats, err := buildVMRuleMappings(context.Background(), newFakeMappingSource())
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, tg := range targets {
		got = append(got, tg.Labels["rule_id"]+"|"+tg.Labels["target_device"]+"|"+tg.Labels["instance_id"])
	}
	// Sorted by rule_id then NIC; the deleted instance is dropped; target_device only on bandwidth
	// rules, one entry per NIC when a VM is linked on two
	want := []string{
		"adjust-bw-inst-2-g-adj|tap2|vm-b",
		"alarm-bw-inst-1-g-bw|tap0|vm-a",
		"alarm-bw-inst-1-g-bw|tap1|vm-a",
		"alarm-cpu-inst-1-g-cpu||vm-a",
		"alarm-cpu-inst-2-g-cpu||vm-b",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: got %s, want %s", i, got[i], want[i])
		}
	}
	if _, ok := targets[3].Labels["target_device"]; ok {
		t.Error("CPU rule entry must not carry target_device")
	}
	if stats["alarm-cpu"] != 1 || stats["adjust-bw"] != 1 || stats["alarm-memory"] != 0 {
		t.Errorf("unexpected stats %v", stats)
	}
}

// A failed query must never lead to a write: a partial list would drop the mappings of the
// rule types that failed and silently stop their alarms.
func TestSyncVMRuleMappingsWritesNothingOnQueryError(t *testing.T) {
	for _, failOn := range []string{"groups:" + RuleTypeMemory, "groups:" + model.RuleTypeAdjustCPU, "links:g-bw", "domains"} {
		src := newFakeMappingSource()
		src.failOn = failOn
		file := &fakeMappingFile{content: []byte(`[{"targets":["localhost:9090"],"labels":{"rule_id":"keep"}}]`)}
		if _, err := syncVMRuleMappings(context.Background(), src, file.read, file.write, true); err == nil {
			t.Errorf("%s: expected an error", failOn)
		}
		if file.writes != 0 {
			t.Errorf("%s: file was written despite the error", failOn)
		}
	}
}

func TestSyncVMRuleMappingsSkipsUnchangedFile(t *testing.T) {
	src := newFakeMappingSource()
	file := &fakeMappingFile{}
	res, err := syncVMRuleMappings(context.Background(), src, file.read, file.write, false)
	if err != nil || !res.Written || file.writes != 1 || res.Count != 5 {
		t.Fatalf("first sync: res=%+v err=%v writes=%d", res, err, file.writes)
	}

	// The incremental updates write another key order and an empty target_device: same content
	var entries []map[string]interface{}
	if err := json.Unmarshal(file.content, &entries); err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	for _, e := range entries {
		labels := e["labels"].(map[string]interface{})
		if _, ok := labels["target_device"]; !ok {
			labels["target_device"] = ""
		}
	}
	file.content, _ = json.Marshal(entries)
	res, err = syncVMRuleMappings(context.Background(), src, file.read, file.write, false)
	if err != nil || res.Written || file.writes != 1 {
		t.Fatalf("unchanged content was rewritten: res=%+v err=%v writes=%d", res, err, file.writes)
	}

	// force always writes (the manual endpoint)
	if res, _ = syncVMRuleMappings(context.Background(), src, file.read, file.write, true); !res.Written || file.writes != 2 {
		t.Fatalf("force did not write: res=%+v writes=%d", res, file.writes)
	}

	// Drift: an entry lost (e.g. an incremental update started from an empty file) is restored
	file.content = []byte(`[]`)
	if res, _ = syncVMRuleMappings(context.Background(), src, file.read, file.write, false); !res.Written || file.writes != 3 {
		t.Fatalf("emptied file was not restored: res=%+v writes=%d", res, file.writes)
	}

	// Unreadable file (missing, alarm-rules-manager down): rewrite rather than skip
	file.readErr = errors.New("unavailable")
	if res, _ = syncVMRuleMappings(context.Background(), src, file.read, file.write, false); !res.Written || file.writes != 4 {
		t.Fatalf("unreadable file was not rewritten: res=%+v writes=%d", res, file.writes)
	}
}

// The incremental "add" must treat each NIC of a bandwidth rule as its own entry, as the
// reconcile does; keyed on (domain, rule_id) alone the second NIC replaced the first.
func TestIsSameMappingIncludesTargetDevice(t *testing.T) {
	tap1 := map[string]interface{}{"domain": "inst-1", "rule_id": "alarm-bw-inst-1-g", "target_device": "tap1"}
	cpu := map[string]interface{}{"domain": "inst-1", "rule_id": "alarm-cpu-inst-1-g", "target_device": ""}
	cpuFromReconcile := map[string]interface{}{"domain": "inst-1", "rule_id": "alarm-cpu-inst-1-g"}

	if !isSameMapping(tap1, "inst-1", "alarm-bw-inst-1-g", "tap1") {
		t.Error("same VM, rule and NIC must match")
	}
	if isSameMapping(tap1, "inst-1", "alarm-bw-inst-1-g", "tap2") {
		t.Error("another NIC of the same VM must be a separate entry")
	}
	if !isSameMapping(cpu, "inst-1", "alarm-cpu-inst-1-g", "") || !isSameMapping(cpuFromReconcile, "inst-1", "alarm-cpu-inst-1-g", "") {
		t.Error("an empty and a missing target_device must both match a rule without NIC")
	}
}
