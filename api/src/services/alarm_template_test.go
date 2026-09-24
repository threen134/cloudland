/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import "testing"

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
