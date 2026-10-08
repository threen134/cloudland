/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"fmt"
	"testing"

	. "api/src/common"
	"api/src/model"
)

// slot builds a host with room for cpu vCPUs, mem GiB of memory and disk GiB of disk
func slot(hostid int32, cpu, memGiB, diskGiB int64) *HostSlot {
	return &HostSlot{Hostid: hostid, FreeCpu: cpu, FreeMemKiB: memGiB << 20, FreeDiskBytes: diskGiB * gib}
}

func demand(cpu, memGiB, diskGiB int64) Demand {
	return Demand{Cpu: cpu, MemKiB: memGiB << 20, DiskBytes: diskGiB * gib}
}

func group(policy string, strict bool) *model.PlacementGroup {
	return &model.PlacementGroup{Name: "g", Policy: policy, Strict: strict}
}

func errCode(err error) ErrCode {
	if clErr, ok := err.(*CLError); ok {
		return clErr.Code
	}
	return 0
}

// placeBatch places count instances one after the other like a creation request does
func placeBatch(t *testing.T, g *model.PlacementGroup, slots []*HostSlot, occ map[int32]int, pending map[int32]Demand, need Demand, count int) (hosts []int32, err error) {
	t.Helper()
	for left := count; left > 0; left-- {
		var host int32
		if host, err = Place(g, slots, occ, pending, need, need.Times(int64(left))); err != nil {
			return
		}
		occ[host]++
		pending[host] = pending[host].Add(need)
		hosts = append(hosts, host)
	}
	return
}

func TestPlaceSpreadStrict(t *testing.T) {
	need := demand(2, 4, 20)
	slots := []*HostSlot{slot(1, 8, 16, 100), slot(2, 32, 64, 500), slot(3, 16, 32, 200)}
	// The roomiest free host first, and pending keeps the next one away from it
	hosts, err := placeBatch(t, group(model.PlacementPolicySpread, true), slots, map[int32]int{}, map[int32]Demand{}, need, 3)
	if err != nil || fmt.Sprint(hosts) != "[2 3 1]" {
		t.Fatalf("hosts %v, err %v; want [2 3 1]", hosts, err)
	}
	// Every host holds a member: no room for a fourth
	_, err = Place(group(model.PlacementPolicySpread, true), slots, map[int32]int{1: 1, 2: 1, 3: 1}, map[int32]Demand{}, need, need)
	if errCode(err) != ErrPlacementGroupNoHost {
		t.Fatalf("err %v, want ErrPlacementGroupNoHost", err)
	}
	// A free host without room does not count as free
	_, err = Place(group(model.PlacementPolicySpread, true), []*HostSlot{slot(1, 8, 16, 100), slot(2, 1, 64, 500)}, map[int32]int{1: 1}, map[int32]Demand{}, need, need)
	if errCode(err) != ErrPlacementGroupNoHost {
		t.Fatalf("err %v, want ErrPlacementGroupNoHost", err)
	}
}

func TestPlaceSpreadSoft(t *testing.T) {
	need := demand(1, 1, 10)
	slots := []*HostSlot{slot(1, 64, 64, 1000), slot(2, 64, 64, 1000), slot(3, 64, 64, 1000)}
	occ := map[int32]int{}
	hosts, err := placeBatch(t, group(model.PlacementPolicySpread, false), slots, occ, map[int32]Demand{}, need, 5)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int32]int{}
	for _, h := range hosts {
		counts[h]++
	}
	if len(counts) != 3 || counts[1]+counts[2]+counts[3] != 5 || counts[1] > 2 || counts[2] > 2 || counts[3] > 2 {
		t.Fatalf("hosts %v, want a 2/2/1 spread", hosts)
	}
	// A host without room is skipped even when it holds the fewest members
	host, err := Place(group(model.PlacementPolicySpread, false), []*HostSlot{slot(1, 0, 64, 1000), slot(2, 64, 64, 1000)},
		map[int32]int{2: 3}, map[int32]Demand{}, need, need)
	if err != nil || host != 2 {
		t.Fatalf("host %d, err %v; want 2", host, err)
	}
	_, err = Place(group(model.PlacementPolicySpread, false), []*HostSlot{slot(1, 0, 64, 1000)}, map[int32]int{}, map[int32]Demand{}, need, need)
	if errCode(err) != ErrNoQualifiedHypervisor {
		t.Fatalf("err %v, want ErrNoQualifiedHypervisor", err)
	}
}

func TestPlacePackStrict(t *testing.T) {
	g := group(model.PlacementPolicyPack, true)
	need := demand(2, 2, 10)
	slots := []*HostSlot{slot(1, 4, 64, 1000), slot(2, 16, 64, 1000), slot(3, 8, 64, 1000)}
	// A new group: the host able to take the whole batch, all members on it
	hosts, err := placeBatch(t, g, slots, map[int32]int{}, map[int32]Demand{}, need, 3)
	if err != nil || fmt.Sprint(hosts) != "[2 2 2]" {
		t.Fatalf("hosts %v, err %v; want [2 2 2]", hosts, err)
	}
	// Only host 2 takes 4 x 2 vCPUs, host 3 would take 4 but not 5
	if _, err = placeBatch(t, g, slots, map[int32]int{}, map[int32]Demand{}, demand(4, 1, 1), 5); errCode(err) != ErrPlacementGroupHostFull {
		t.Fatalf("err %v, want ErrPlacementGroupHostFull for a batch no host takes whole", err)
	}
	// With an anchor, only the anchor
	host, err := Place(g, slots, map[int32]int{3: 2}, map[int32]Demand{}, need, need)
	if err != nil || host != 3 {
		t.Fatalf("host %d, err %v; want the anchor 3", host, err)
	}
	// The anchor can not take the rest of the batch: refused before anything goes there
	if _, err = Place(g, slots, map[int32]int{3: 2}, map[int32]Demand{}, need, need.Times(5)); errCode(err) != ErrPlacementGroupHostFull {
		t.Fatalf("err %v, want ErrPlacementGroupHostFull", err)
	}
	// The anchor is not a candidate (maintenance)
	if _, err = Place(g, slots, map[int32]int{7: 1}, map[int32]Demand{}, need, need); errCode(err) != ErrPlacementGroupHostFull {
		t.Fatalf("err %v, want ErrPlacementGroupHostFull", err)
	}
	// A group split before: the host with the most members
	host, err = Place(g, slots, map[int32]int{1: 1, 3: 2}, map[int32]Demand{}, need, need)
	if err != nil || host != 3 {
		t.Fatalf("host %d, err %v; want 3", host, err)
	}
	// rest adds up different flavors: 2 + 2 + 8 vCPUs do not fit the 8 of host 3
	rest := demand(2, 1, 1).Add(demand(2, 1, 1)).Add(demand(8, 1, 1))
	if _, err = Place(g, slots, map[int32]int{3: 1}, map[int32]Demand{}, demand(2, 1, 1), rest); errCode(err) != ErrPlacementGroupHostFull {
		t.Fatalf("err %v, want ErrPlacementGroupHostFull for 12 vCPUs on 8", err)
	}
}

func TestPlacePackSoft(t *testing.T) {
	g := group(model.PlacementPolicyPack, false)
	need := demand(4, 1, 1)
	slots := []*HostSlot{slot(1, 8, 64, 1000), slot(2, 16, 64, 1000), slot(3, 12, 64, 1000)}
	// The anchor is full: the next one goes to the roomiest host, and the following ones join it
	occ := map[int32]int{1: 2}
	hosts, err := placeBatch(t, g, slots, occ, map[int32]Demand{1: demand(8, 2, 2)}, need, 3)
	if err != nil || fmt.Sprint(hosts) != "[2 2 2]" {
		t.Fatalf("hosts %v, err %v; want [2 2 2]", hosts, err)
	}
	// A new group whose batch fits no host whole spills over: first the roomiest, then the others join it while it has room
	hosts, err = placeBatch(t, g, slots, map[int32]int{}, map[int32]Demand{}, need, 8)
	if err != nil || fmt.Sprint(hosts) != "[2 2 2 2 3 3 3 1]" {
		t.Fatalf("hosts %v, err %v; want [2 2 2 2 3 3 3 1]", hosts, err)
	}
}

func TestPlaceTieAndPending(t *testing.T) {
	need := demand(1, 1, 1)
	slots := []*HostSlot{slot(3, 8, 8, 100), slot(1, 8, 8, 100), slot(2, 8, 8, 100)}
	host, err := Place(group(model.PlacementPolicySpread, true), slots, map[int32]int{}, map[int32]Demand{}, need, need)
	if err != nil || host != 1 {
		t.Fatalf("host %d, err %v; want the lowest host id on a tie", host, err)
	}
	// Members of the group still being created weigh on their host: the pack group spills over, a strict one refuses
	pending := map[int32]Demand{1: demand(6, 0, 0)}
	host, err = Place(group(model.PlacementPolicyPack, false), slots, map[int32]int{1: 3}, pending, demand(4, 1, 1), demand(4, 1, 1))
	if err != nil || host == 1 {
		t.Fatalf("host %d, err %v; want another host than the full anchor", host, err)
	}
	if _, err = Place(group(model.PlacementPolicyPack, true), slots, map[int32]int{1: 3}, pending, demand(4, 1, 1), demand(4, 1, 1)); errCode(err) != ErrPlacementGroupHostFull {
		t.Fatalf("err %v, want ErrPlacementGroupHostFull", err)
	}
}

func TestPlaceDiskDimension(t *testing.T) {
	// Boot disk in a pool: the pool of host 2 can not take the batch of three 40 GiB disks
	slots := []*HostSlot{slot(1, 64, 64, 200), slot(2, 64, 64, 100)}
	host, err := Place(group(model.PlacementPolicyPack, true), slots, map[int32]int{}, map[int32]Demand{}, demand(1, 1, 40), demand(3, 3, 120))
	if err != nil || host != 1 {
		t.Fatalf("host %d, err %v; want 1, the only pool that takes 120 GiB", host, err)
	}
}

func TestPackAnchorAndViolation(t *testing.T) {
	if a := packAnchor(map[int32]int{}); a != -1 {
		t.Fatalf("anchor %d, want -1", a)
	}
	if a := packAnchor(map[int32]int{5: 2, 3: 2, 1: 1, 9: 0}); a != 3 {
		t.Fatalf("anchor %d, want 3", a)
	}
	label := func(h int32) string { return fmt.Sprint(h) }
	spread := group(model.PlacementPolicySpread, true)
	if placementViolation(spread, map[int32]int{1: 1}, 2, 1, label) != "" {
		t.Fatal("a free host breaks no spread rule")
	}
	if placementViolation(spread, map[int32]int{1: 1}, 1, 1, label) == "" || placementViolation(spread, map[int32]int{}, 1, 2, label) == "" {
		t.Fatal("a taken host, or two on one host, break the spread rule")
	}
	pack := group(model.PlacementPolicyPack, true)
	if placementViolation(pack, map[int32]int{}, 4, 3, label) != "" || placementViolation(pack, map[int32]int{4: 2}, 4, 1, label) != "" {
		t.Fatal("the anchor, or any host of an empty group, is fine for pack")
	}
	if placementViolation(pack, map[int32]int{4: 2}, 5, 1, label) == "" {
		t.Fatal("another host than the anchor breaks the pack rule")
	}
}

func TestPlacementCompliant(t *testing.T) {
	cases := []struct {
		policy  string
		perHost map[int32]int
		want    bool
	}{
		{model.PlacementPolicySpread, map[int32]int{}, true},
		{model.PlacementPolicySpread, map[int32]int{1: 1, 2: 1}, true},
		{model.PlacementPolicySpread, map[int32]int{1: 2, 2: 1}, false},
		{model.PlacementPolicyPack, map[int32]int{1: 3}, true},
		{model.PlacementPolicyPack, map[int32]int{1: 2, 2: 1}, false},
	}
	for _, c := range cases {
		if got := placementCompliant(c.policy, c.perHost); got != c.want {
			t.Errorf("%s %v: compliant %t, want %t", c.policy, c.perHost, got, c.want)
		}
	}
}

func TestStrictPackBlocker(t *testing.T) {
	// 6 is in the request, still running, but its own migration failed earlier in the call (maintenance goes on)
	call := &migrationCall{ids: map[int64]bool{1: true, 2: true, 3: true, 4: true, 6: true}, started: map[int64]bool{}, failed: map[int64]bool{6: true}}
	member := func(id int64, status model.InstanceStatus, reason string, landed bool) *placementMember {
		return &placementMember{Instance: &model.Instance{Model: model.Model{ID: id}, Hostname: fmt.Sprintf("m%d", id), Status: status, Reason: reason}, Landed: landed}
	}
	cases := []struct {
		m     *placementMember
		moves bool
	}{
		{member(1, model.InstanceStatusRunning, "", true), true},
		{member(2, model.InstanceStatusPaused, "", true), true},
		{member(3, model.InstanceStatusPaused, InstanceReasonStorageFull, true), false},
		{member(4, model.InstanceStatusRescuing, "", true), false},
		{member(4, model.InstanceStatusProvisioning, "", false), false},
		{member(5, model.InstanceStatusRunning, "", true), false}, // not in the request
		{member(6, model.InstanceStatusRunning, "", true), false}, // its migration failed in this call
	}
	for _, c := range cases {
		reason := strictPackBlocker(c.m, call)
		if (reason == "") != c.moves {
			t.Errorf("%s (%s): blocker %q, moves %t", c.m.Instance.Hostname, c.m.Instance.Status, reason, c.moves)
		}
	}
}
