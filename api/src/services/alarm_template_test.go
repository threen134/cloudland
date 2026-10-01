/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"api/src/model"

	"gopkg.in/yaml.v3"
)

// repoRuleTemplates points the rule templates at the repository copy for one test
func repoRuleTemplates(t *testing.T) {
	t.Helper()
	t.Cleanup(UseLocalRuleTemplates(filepath.Join("..", "..", "..", "deploy", "roles", "monitor", "templates")))
}

// nodeRuleConfigs are the configs the node alarm rule page pre-fills per rule type (AlarmList.vue CONFIG_TEMPLATES)
var nodeRuleConfigs = map[string]string{
	RuleTypeAvailable: `{"node_down_duration":"5m"}`,
	RuleTypeControl: `{"cpu_usage_threshold":80,"cpu_alert_duration":"10m","memory_usage_threshold":80,"memory_alert_duration":"10m",
		"disk_space_threshold":20,"disk_alert_duration":"10m","network_traffic_threshold_gb":5,"network_alert_duration":"10m"}`,
	RuleTypeCompute: `{"cpu_usage_threshold":80,"cpu_alert_duration":"10m","memory_usage_threshold":80,"memory_alert_duration":"10m",
		"disk_space_threshold":10,"disk_alert_duration":"20m","network_traffic_threshold_gb":25,"network_alert_duration":"5m",
		"network_types":{"public":{"pattern":"=~\"bond1\"","threshold":20,"duration":"5m"},"private":{"pattern":"=~\"bond0\"","threshold":20,"duration":"5m"}}}`,
	RuleTypeHypervisorVCPU: `{"vcpu_usage_threshold":85,"for_duration":"10m"}`,
	RuleTypePacketDrop:     `{"packet_drop_threshold":0,"for_duration":"1m","severity":"warning"}`,
	RuleTypeIPBlock:        `{"for_duration":"30s","severity":"warning"}`,
	"ipgroup_available_ip": `{"threshold":100,"for_duration":"5m","severity":"warning"}`,
	RuleTypeLocalPool:      `{"usage_threshold":85,"for_duration":"5m","severity":"warning"}`,
}

func nodeRule(ruleType, config string) *model.NodeAlarmRule {
	rule := &model.NodeAlarmRule{RuleType: ruleType, Name: "r", Owner: "3", Enabled: true,
		Config: model.ConfigWrapper{RawMessage: json.RawMessage(config)}}
	rule.UUID = "11111111-2222-3333-4444-" + strings.Repeat("5", 12)
	return rule
}

// Node alerts reached clapi without an owner label, so the alarm event list (filtered by owner) never showed
// them, and without a rule_group label, so no bound channel was ever notified. Every alert of every node rule
// type must carry both, with the values clapi passes (owner) and the rule's UUID (rule_group).
func TestNodeAlarmRulesLabelOwnerAndRuleGroup(t *testing.T) {
	repoRuleTemplates(t)
	ctx := context.Background()
	for ruleType, config := range nodeRuleConfigs {
		t.Run(ruleType, func(t *testing.T) {
			rule := nodeRule(ruleType, config)
			files, err := renderNodeAlarmRuleFiles(ctx, rule, "7")
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if len(files) != len(nodeAlarmTemplates(ruleType)) {
				t.Fatalf("got %d files, want %d", len(files), len(nodeAlarmTemplates(ruleType)))
			}
			for name, content := range files {
				if missing := unresolvedPlaceholders(content); len(missing) > 0 {
					t.Fatalf("%s: unresolved placeholders %v", name, missing)
				}
				var parsed struct {
					Groups []struct {
						Rules []struct {
							Alert  string            `yaml:"alert"`
							Labels map[string]string `yaml:"labels"`
						} `yaml:"rules"`
					} `yaml:"groups"`
				}
				if err := yaml.Unmarshal([]byte(content), &parsed); err != nil {
					t.Fatalf("%s is not valid YAML: %v\n%s", name, err, content)
				}
				alerts := 0
				for _, g := range parsed.Groups {
					for _, r := range g.Rules {
						alerts++
						if r.Labels["owner"] != "7" || r.Labels["rule_group"] != rule.UUID {
							t.Errorf("%s alert %s: owner=%q rule_group=%q", name, r.Alert, r.Labels["owner"], r.Labels["rule_group"])
						}
					}
				}
				if alerts == 0 {
					t.Fatalf("%s: no alerts rendered", name)
				}
			}
		})
	}
}

// The labels are clapi's: a rule config cannot set them, and they are not offered as config keys
func TestNodeAlarmReservedKeysNotConfigurable(t *testing.T) {
	repoRuleTemplates(t)
	ctx := context.Background()
	allowed, err := nodeAlarmAllowedKeys(ctx, RuleTypeIPBlock)
	if err != nil {
		t.Fatal(err)
	}
	if allowed["owner"] || allowed["rule_group"] || !allowed["for_duration"] {
		t.Fatalf("allowed keys %v", allowed)
	}
	for _, key := range []string{"owner", "rule_group"} {
		err := validateNodeAlarmConfig(ctx, nodeRule(RuleTypeIPBlock, `{"`+key+`":"1"}`))
		if !IsAlarmRuleInputError(err) {
			t.Errorf("config key %s: want an input error, got %v", key, err)
		}
	}
}

// POST /node-alarm-rules answered 500 for every mistake in the request (wrong config keys, missing values,
// unknown rule type); those are input errors (400). A template file missing on the server is not.
func TestNodeAlarmRuleInputErrors(t *testing.T) {
	repoRuleTemplates(t)
	ctx := context.Background()
	cases := []struct {
		name, ruleType, config, want string
	}{
		{"unknown rule type", "foo", `{}`, "unsupported rule type"},
		{"config key the template does not use", RuleTypeIPBlock, `{"foo":1}`, "config keys not used"},
		{"value the template needs but has no default for", RuleTypeHypervisorVCPU, `{"for_duration":"10m"}`, "config is missing values"},
		{"network types missing", RuleTypeCompute, `{"cpu_usage_threshold":80}`, "network_types"},
		{"bad duration", RuleTypeAvailable, `{"node_down_duration":"five"}`, "node_down_duration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := renderNodeAlarmRuleFiles(ctx, nodeRule(c.ruleType, c.config), "3")
			if !IsAlarmRuleInputError(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an input error containing %q, got %v", c.want, err)
			}
		})
	}
	for _, c := range []string{"", `{"a":`} {
		if err := validateNodeAlarmRule(nodeRule(RuleTypeIPBlock, c)); !IsAlarmRuleInputError(err) {
			t.Errorf("config %q: want an input error, got %v", c, err)
		}
	}

	// A template missing on this side is a server problem
	RuleTemplateDir = t.TempDir()
	_, err := renderNodeAlarmRuleFiles(ctx, nodeRule(RuleTypeIPBlock, `{"for_duration":"30s"}`), "3")
	if err == nil || IsAlarmRuleInputError(err) {
		t.Fatalf("missing template: want a non-input error, got %v", err)
	}
}

// A rule file that still holds a `{{ name }}` placeholder makes Prometheus reject its whole
// rule set, so ProcessTemplate refuses to write one. Prometheus' own templating must not be
// mistaken for an unresolved placeholder.
func TestUnresolvedPlaceholders(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "config key did not match the template",
			content: "expr: usage > {{ vcpu_usage_threshold }}\nfor: {{ for_duration }}\n",
			want:    []string{"vcpu_usage_threshold", "for_duration"},
		},
		{
			name:    "reported once per name",
			content: "a {{ threshold }} b {{ threshold }}",
			want:    []string{"threshold"},
		},
		{
			name:    "prometheus templating is left alone",
			content: `summary: "{{ $labels.instance }} {{ $value | humanize }} {{ if $labels.zone }}z{{ end }}"`,
			want:    nil,
		},
		{
			name:    "fully rendered",
			content: "expr: usage > 85\nfor: 10m\n",
			want:    nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := unresolvedPlaceholders(c.content)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}
