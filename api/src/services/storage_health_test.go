/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"testing"
	"time"
)

// The alarm state of a cluster (shared-storage-design.md §14.2): a condition raises its alarm once it lasted the
// delay (at once when immediate), a severity that changes resolves and raises again, a condition gone resolves, and a
// round only speaks for its own part of the keys
func TestStorageAlarmTransitions(t *testing.T) {
	info := ParseStorageHealthInfo("")
	t0 := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	delay := 2 * time.Minute
	node := storageCondition{Key: "node:2", Name: StorageAlarmNodeDown, Severity: "warning", Summary: "work-02 is down"}
	health := []string{"cluster", "flag", "node", "disk"}
	if ch := storageAlarmTransitions(info, health, []storageCondition{node}, nil, t0, delay); len(ch) != 0 {
		t.Fatalf("an alarm before the delay: %+v", ch)
	}
	if ch := storageAlarmTransitions(info, health, []storageCondition{node}, nil, t0.Add(90*time.Second), delay); len(ch) != 0 {
		t.Fatalf("an alarm before the delay: %+v", ch)
	}
	ch := storageAlarmTransitions(info, health, []storageCondition{node}, nil, t0.Add(2*time.Minute), delay)
	if len(ch) != 1 || ch[0].Resolve || ch[0].State.Name != StorageAlarmNodeDown || !ch[0].State.Since.Equal(t0) {
		t.Fatalf("raised after the delay: %+v", ch)
	}
	if ch := storageAlarmTransitions(info, health, []storageCondition{node}, nil, t0.Add(3*time.Minute), delay); len(ch) != 0 {
		t.Fatalf("raised again: %+v", ch)
	}
	// The pool round leaves the node alarm alone
	if ch := storageAlarmTransitions(info, []string{"pool", "mount"}, nil, nil, t0.Add(3*time.Minute), delay); len(ch) != 0 {
		t.Fatalf("a round touched what it does not speak for: %+v", ch)
	}
	// Immediate, with a severity that changes
	warn := storageCondition{Key: "pool:p1", Name: StorageAlarmPoolUsageHigh, Severity: "warning", Immediate: true}
	ch = storageAlarmTransitions(info, []string{"pool", "mount"}, []storageCondition{warn}, nil, t0.Add(4*time.Minute), delay)
	if len(ch) != 1 || ch[0].Resolve {
		t.Fatalf("immediate alarm: %+v", ch)
	}
	crit := warn
	crit.Severity = "critical"
	ch = storageAlarmTransitions(info, []string{"pool", "mount"}, []storageCondition{crit}, nil, t0.Add(5*time.Minute), delay)
	if len(ch) != 2 || !ch[0].Resolve || ch[0].State.Severity != "warning" || ch[1].Resolve || ch[1].State.Severity != "critical" {
		t.Fatalf("severity change: %+v", ch)
	}
	// Back to warning and critical again within the outage: each firing has its own time (the fingerprint of the
	// event), the time of the condition stays
	firstCritical := ch[1].State.Fired
	storageAlarmTransitions(info, []string{"pool", "mount"}, []storageCondition{warn}, nil, t0.Add(5*time.Minute+30*time.Second), delay)
	ch = storageAlarmTransitions(info, []string{"pool", "mount"}, []storageCondition{crit}, nil, t0.Add(5*time.Minute+50*time.Second), delay)
	if len(ch) != 2 || ch[1].State.Severity != "critical" || ch[1].State.Fired.Equal(firstCritical) || !ch[1].State.Since.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("critical again: %+v", ch)
	}
	// Gone: resolved, its time cleared
	ch = storageAlarmTransitions(info, health, nil, nil, t0.Add(6*time.Minute), delay)
	if len(ch) != 1 || !ch[0].Resolve || ch[0].Key != "node:2" {
		t.Fatalf("resolved: %+v", ch)
	}
	if _, ok := info.Since["node:2"]; ok {
		t.Fatal("the time of a condition gone stays")
	}
	// A condition that came and went before the delay leaves nothing behind
	storageAlarmTransitions(info, health, []storageCondition{{Key: "disk:d1", Name: StorageAlarmDiskDown, Severity: "warning"}}, nil, t0.Add(7*time.Minute), delay)
	if ch := storageAlarmTransitions(info, health, nil, nil, t0.Add(8*time.Minute), delay); len(ch) != 0 || len(info.Since) != 1 {
		t.Fatalf("a short condition: %+v, since %v", ch, info.Since)
	}
	// A key the round did not look at stays as it is: not resolved, its time kept
	disk := storageCondition{Key: "disk:d2", Name: StorageAlarmDiskDown, Severity: "warning", Summary: "d2 is down"}
	storageAlarmTransitions(info, health, []storageCondition{disk}, nil, t0.Add(10*time.Minute), delay)
	if ch := storageAlarmTransitions(info, health, []storageCondition{disk}, nil, t0.Add(12*time.Minute), delay); len(ch) != 1 {
		t.Fatalf("d2 not raised: %+v", ch)
	}
	if ch := storageAlarmTransitions(info, health, nil, map[string]bool{"disk:d2": true}, t0.Add(13*time.Minute), delay); len(ch) != 0 || info.Firing["disk:d2"] == nil {
		t.Fatalf("a disk left out of a report was resolved: %+v", ch)
	}
	if ch := storageAlarmTransitions(info, health, nil, nil, t0.Add(14*time.Minute), delay); len(ch) != 1 || !ch[0].Resolve {
		t.Fatalf("a disk reported fine again was not resolved: %+v", ch)
	}
	// "flag" and "flagged" are different prefixes
	info.Since["flagged:x"] = t0
	storageAlarmTransitions(info, []string{"flag"}, nil, nil, t0.Add(9*time.Minute), delay)
	if _, ok := info.Since["flagged:x"]; !ok {
		t.Fatal("a key of another prefix was cleared")
	}
}

func TestStorageShortName(t *testing.T) {
	for in, want := range map[string]string{"work-01": "work-01", "Work-01.cloudland.local": "work-01", " work-02 ": "work-02"} {
		if got := storageShortName(in); got != want {
			t.Errorf("storageShortName(%q) = %q, want %q", in, got, want)
		}
	}
}
