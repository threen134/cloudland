/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"api/src/model"
)

func tgwTestMember(attID, routerID, tableID int64, slot int32, subnets ...string) *tgwMember {
	return &tgwMember{Att: &model.TgwAttachment{Model: model.Model{ID: attID}, RouterID: routerID, RouteTableID: tableID, Slot: slot}, Subnets: subnets}
}

func tgwTestMembers(ms ...*tgwMember) (map[int64]*tgwMember, []*tgwMember) {
	byID := map[int64]*tgwMember{}
	for _, m := range ms {
		byID[m.Att.ID] = m
	}
	return byID, ms
}

func tgwEntryStrings(entries []*TgwRouteEntry) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Prefix+" "+e.Source+" "+string(rune('0'+e.AttachmentID)))
	}
	return out
}

// Every member propagates into the default table and is associated with it: full mesh
func TestTgwFullMesh(t *testing.T) {
	a := tgwTestMember(1, 11, 100, 0, "10.1.0.0/24", "10.1.1.0/24")
	b := tgwTestMember(2, 12, 100, 1, "10.2.0.0/24")
	c := tgwTestMember(3, 13, 100, 2, "10.3.0.0/24")
	byID, ordered := tgwTestMembers(a, b, c)
	props := []*model.TgwPropagation{
		{RouteTableID: 100, AttachmentID: 1}, {RouteTableID: 100, AttachmentID: 2}, {RouteTableID: 100, AttachmentID: 3},
	}
	entries := computeTgwTableRoutes(100, byID, props, nil)
	want := []string{"10.1.0.0/24 propagated 1", "10.1.1.0/24 propagated 1", "10.2.0.0/24 propagated 2", "10.3.0.0/24 propagated 3"}
	if got := tgwEntryStrings(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	reachable, blackhole, throw := tgwRouterPrefixes(a, entries, ordered)
	if !reflect.DeepEqual(reachable, []string{"10.2.0.0/24", "10.3.0.0/24"}) {
		t.Errorf("reachable = %v", reachable)
	}
	if len(blackhole) != 0 {
		t.Errorf("blackhole = %v, want none", blackhole)
	}
	if !reflect.DeepEqual(throw, []string{"10.1.0.0/24", "10.1.1.0/24", "192.168.196.0/24"}) {
		t.Errorf("throw = %v", throw)
	}
}

// hub-spoke: the spokes are associated with a table holding only the hub, the hub with the default table
// holding the spokes; a spoke drops the other spoke instead of routing it out by the default route
func TestTgwHubSpoke(t *testing.T) {
	hub := tgwTestMember(1, 11, 100, 0, "10.0.0.0/24")
	a := tgwTestMember(2, 12, 200, 1, "10.2.0.0/24")
	b := tgwTestMember(3, 13, 200, 2, "10.3.0.0/24")
	byID, ordered := tgwTestMembers(hub, a, b)
	props := []*model.TgwPropagation{
		{RouteTableID: 100, AttachmentID: 2}, {RouteTableID: 100, AttachmentID: 3}, // spokes into default
		{RouteTableID: 200, AttachmentID: 1}, // hub into the spoke table
	}
	spokeTable := computeTgwTableRoutes(200, byID, props, nil)
	reachable, blackhole, _ := tgwRouterPrefixes(a, spokeTable, ordered)
	if !reflect.DeepEqual(reachable, []string{"10.0.0.0/24"}) || !reflect.DeepEqual(blackhole, []string{"10.3.0.0/24"}) {
		t.Errorf("spoke a: reachable %v blackhole %v", reachable, blackhole)
	}
	hubTable := computeTgwTableRoutes(100, byID, props, nil)
	reachable, blackhole, _ = tgwRouterPrefixes(hub, hubTable, ordered)
	if !reflect.DeepEqual(reachable, []string{"10.2.0.0/24", "10.3.0.0/24"}) || len(blackhole) != 0 {
		t.Errorf("hub: reachable %v blackhole %v", reachable, blackhole)
	}
}

// An allow list propagates only the parts of the subnets inside it; the rest of the member's subnet is dropped
func TestTgwPrefixFilter(t *testing.T) {
	a := tgwTestMember(1, 11, 100, 0, "10.1.0.0/24")
	b := tgwTestMember(2, 12, 100, 1, "10.2.0.0/16", "10.9.0.0/24")
	byID, ordered := tgwTestMembers(a, b)
	props := []*model.TgwPropagation{{RouteTableID: 100, AttachmentID: 1}, {RouteTableID: 100, AttachmentID: 2, Prefixes: "10.2.1.0/24"}}
	entries := computeTgwTableRoutes(100, byID, props, nil)
	want := []string{"10.1.0.0/24 propagated 1", "10.2.1.0/24 propagated 2"}
	if got := tgwEntryStrings(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	reachable, blackhole, _ := tgwRouterPrefixes(a, entries, ordered)
	if !reflect.DeepEqual(reachable, []string{"10.2.1.0/24"}) || !reflect.DeepEqual(blackhole, []string{"10.2.0.0/16", "10.9.0.0/24"}) {
		t.Errorf("reachable %v blackhole %v", reachable, blackhole)
	}
	// An allowed network wider than the subnet propagates the whole subnet
	props[1].Prefixes = "10.0.0.0/8"
	entries = computeTgwTableRoutes(100, byID, props, nil)
	want = []string{"10.1.0.0/24 propagated 1", "10.2.0.0/16 propagated 2", "10.9.0.0/24 propagated 2"}
	if got := tgwEntryStrings(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

// A static route replaces the propagated one with the same destination; a blackhole drops a part; routes towards
// a member that is not active, and a route the table would send back into the VPC itself, are left out
func TestTgwStaticRoutes(t *testing.T) {
	a := tgwTestMember(1, 11, 100, 0, "10.1.0.0/24")
	b := tgwTestMember(2, 12, 100, 1, "10.2.0.0/24")
	c := tgwTestMember(3, 13, 100, 2, "10.3.0.0/24")
	byID, ordered := tgwTestMembers(a, b, c)
	props := []*model.TgwPropagation{{RouteTableID: 100, AttachmentID: 1}, {RouteTableID: 100, AttachmentID: 2}, {RouteTableID: 100, AttachmentID: 3}}
	statics := []*model.TgwRoute{
		{RouteTableID: 100, Destination: "10.2.0.0/24", AttachmentID: 3, Type: model.TgwRouteStatic}, // B's subnet through C
		{RouteTableID: 100, Destination: "10.3.0.128/25", Type: model.TgwRouteBlackhole},
		{RouteTableID: 100, Destination: "172.16.0.0/16", AttachmentID: 9, Type: model.TgwRouteStatic}, // detached member
		{RouteTableID: 100, Destination: "10.0.0.0/8", AttachmentID: 2, Type: model.TgwRouteStatic},    // covers A itself
		{RouteTableID: 200, Destination: "10.4.0.0/24", AttachmentID: 2, Type: model.TgwRouteStatic},   // other table
	}
	entries := computeTgwTableRoutes(100, byID, props, statics)
	want := []string{"10.0.0.0/8 static 2", "10.1.0.0/24 propagated 1", "10.2.0.0/24 static 3", "10.3.0.0/24 propagated 3", "10.3.0.128/25 blackhole 0"}
	if got := tgwEntryStrings(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	reachable, blackhole, throw := tgwRouterPrefixes(a, entries, ordered)
	// 10.0.0.0/8 stays: the VPC's own 10.1.0.0/24 is thrown back to the main table by the more specific throw
	if !reflect.DeepEqual(reachable, []string{"10.0.0.0/8", "10.2.0.0/24", "10.3.0.0/24"}) {
		t.Errorf("reachable = %v", reachable)
	}
	if !reflect.DeepEqual(blackhole, []string{"10.3.0.128/25"}) {
		t.Errorf("blackhole = %v", blackhole)
	}
	if !reflect.DeepEqual(throw, []string{"10.1.0.0/24", "192.168.196.0/24"}) {
		t.Errorf("throw = %v", throw)
	}
	// Seen from C, the static route sending B's subnet to C itself is not installed (it would loop back)
	reachable, _, _ = tgwRouterPrefixes(c, entries, ordered)
	if !reflect.DeepEqual(reachable, []string{"10.0.0.0/8", "10.1.0.0/24"}) {
		t.Errorf("c reachable = %v", reachable)
	}
}

func TestTgwLinkIPs(t *testing.T) {
	r, g := tgwLinkIPs(0)
	if r != "169.254.254.1" || g != "169.254.254.0" {
		t.Errorf("slot 0: %s %s", r, g)
	}
	r, g = tgwLinkIPs(9)
	if r != "169.254.254.19" || g != "169.254.254.18" {
		t.Errorf("slot 9: %s %s", r, g)
	}
}

func TestValidateTgwDestination(t *testing.T) {
	for _, bad := range []string{"0.0.0.0/0", "169.254.254.0/28", "x", "fe80::/64"} {
		if _, err := validateTgwDestination(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if cidr, err := validateTgwDestination(" 10.5.3.7/16 "); err != nil || cidr != "10.5.0.0/16" {
		t.Errorf("normalized %q, %v", cidr, err)
	}
}

func TestTgwReservedConflict(t *testing.T) {
	if tgwReservedConflict("192.168.196.128/25") == nil || tgwReservedConflict("169.254.0.0/16") == nil {
		t.Error("reserved ranges accepted")
	}
	if tgwReservedConflict("192.168.197.0/24") != nil {
		t.Error("an ordinary network refused")
	}
}

// TestTgwSimulationStates writes the node states of a few scenarios as JSON for a netns simulation of
// apply_tgw.sh (only when TGW_SIM_DIR is set). Routers 11/12/13 are VPCs A/B/C, attachments 101/102/103.
func TestTgwSimulationStates(t *testing.T) {
	dir := os.Getenv("TGW_SIM_DIR")
	if dir == "" {
		t.Skip("TGW_SIM_DIR not set")
	}
	a := tgwTestMember(101, 11, 1, 0, "192.168.1.0/24", "192.168.11.0/24")
	b := tgwTestMember(102, 12, 1, 1, "192.168.2.0/24", "192.168.12.0/24")
	c := tgwTestMember(103, 13, 1, 2, "192.168.3.0/24")
	tables := []*model.TgwRouteTable{{Model: model.Model{ID: 1}, IsDefault: true, Slot: 0}, {Model: model.Model{ID: 2}, Slot: 1}}
	all := []*model.TgwPropagation{{RouteTableID: 1, AttachmentID: 101}, {RouteTableID: 1, AttachmentID: 102}, {RouteTableID: 1, AttachmentID: 103}}
	write := func(name string, generation int64, ms []*tgwMember, props []*model.TgwPropagation, routes []*model.TgwRoute) {
		byID, ordered := tgwTestMembers(ms...)
		state := assembleTgwState(7, generation, byID, ordered, tables, props, routes)
		data, _ := json.Marshal(state)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("g1-mesh.json", 1, []*tgwMember{a, b, c}, all, nil)
	// hub-spoke: A is the hub in the default table, B and C are associated with the spoke table holding only A
	hb := *b.Att
	hb.RouteTableID = 2
	hc := *c.Att
	hc.RouteTableID = 2
	write("g2-hubspoke.json", 2, []*tgwMember{a, {Att: &hb, Subnets: b.Subnets}, {Att: &hc, Subnets: c.Subnets}},
		[]*model.TgwPropagation{{RouteTableID: 1, AttachmentID: 102}, {RouteTableID: 1, AttachmentID: 103}, {RouteTableID: 2, AttachmentID: 101}}, nil)
	write("g3-static.json", 3, []*tgwMember{a, b, c}, all, []*model.TgwRoute{
		{RouteTableID: 1, Destination: "192.168.2.0/25", Type: model.TgwRouteBlackhole},
		{RouteTableID: 1, Destination: "192.168.2.10/32", AttachmentID: 102, Type: model.TgwRouteStatic},
	})
	write("g4-prefix.json", 4, []*tgwMember{a, b, c}, []*model.TgwPropagation{{RouteTableID: 1, AttachmentID: 101},
		{RouteTableID: 1, AttachmentID: 102, Prefixes: "192.168.2.0/24"}, {RouteTableID: 1, AttachmentID: 103}}, nil)
	write("g5-detachB.json", 5, []*tgwMember{a, c}, []*model.TgwPropagation{{RouteTableID: 1, AttachmentID: 101}, {RouteTableID: 1, AttachmentID: 103}}, nil)
	write("g6-empty.json", 6, nil, nil, nil)
	write("g0-old.json", 0, []*tgwMember{a, b, c}, all, nil)
}

// The networks of a member's VPN gateway stay with the VPN in its router: a wide blackhole of the table keeps the
// rest dropped, a route of the table into the VPN range is not installed
func TestTgwVpnPrefixesStayWithTheVpn(t *testing.T) {
	a := tgwTestMember(1, 11, 100, 0, "10.1.0.0/24")
	a.VpnPrefixes = []string{"10.50.0.0/16"}
	b := tgwTestMember(2, 12, 100, 1, "10.2.0.0/24")
	byID, ordered := tgwTestMembers(a, b)
	props := []*model.TgwPropagation{{RouteTableID: 100, AttachmentID: 1}, {RouteTableID: 100, AttachmentID: 2}}
	statics := []*model.TgwRoute{
		{RouteTableID: 100, Destination: "10.0.0.0/8", Type: model.TgwRouteBlackhole},
		{RouteTableID: 100, Destination: "10.50.1.0/24", AttachmentID: 2, Type: model.TgwRouteStatic},
	}
	entries := computeTgwTableRoutes(100, byID, props, statics)
	reachable, blackhole, throw := tgwRouterPrefixes(a, entries, ordered)
	if !reflect.DeepEqual(throw, []string{"10.1.0.0/24", "10.50.0.0/16", "192.168.196.0/24"}) {
		t.Errorf("throw = %v", throw)
	}
	if !reflect.DeepEqual(reachable, []string{"10.2.0.0/24"}) || !reflect.DeepEqual(blackhole, []string{"10.0.0.0/8"}) {
		t.Errorf("reachable %v blackhole %v", reachable, blackhole)
	}
}
