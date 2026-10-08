/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"testing"
	"time"

	"api/src/model"
)

func vpnTestGateway(addresses ...string) *model.VpnGateway {
	gateway := &model.VpnGateway{}
	endpoints := []string{model.VpnEndpointVip1, model.VpnEndpointVip2}
	for i, a := range addresses {
		gateway.FloatingIps = append(gateway.FloatingIps, &model.FloatingIp{FipAddress: a, VpnEndpoint: endpoints[i]})
	}
	return gateway
}

func TestVpnConnectionAggregateStatus(t *testing.T) {
	tunnel := func(slot int32, priority, status string) *model.VpnTunnel {
		return &model.VpnTunnel{Slot: slot, Priority: priority, Status: status}
	}
	p, s := model.VpnTunnelPriorityPrimary, model.VpnTunnelPriorityStandby
	cases := []struct {
		name    string
		tunnels []*model.VpnTunnel
		want    string
	}{
		{"single up", []*model.VpnTunnel{tunnel(1, p, "up")}, "up"},
		{"single down", []*model.VpnTunnel{tunnel(1, p, "down")}, "down"},
		{"primary up", []*model.VpnTunnel{tunnel(1, p, "up"), tunnel(2, s, "down")}, "up"},
		{"only standby up", []*model.VpnTunnel{tunnel(1, p, "down"), tunnel(2, s, "up")}, "degraded"},
		{"primary in slot 2", []*model.VpnTunnel{tunnel(1, s, "down"), tunnel(2, p, "up")}, "up"},
		{"both pending", []*model.VpnTunnel{tunnel(1, p, "pending"), tunnel(2, s, "pending")}, "pending"},
		{"pending and down", []*model.VpnTunnel{tunnel(1, p, "pending"), tunnel(2, s, "down")}, "down"},
		{"paused", []*model.VpnTunnel{tunnel(1, p, "disabled"), tunnel(2, s, "disabled")}, "disabled"},
		{"standby 3 up", []*model.VpnTunnel{tunnel(1, p, "down"), tunnel(2, s, "down"), tunnel(3, s, "up")}, "degraded"},
	}
	for _, c := range cases {
		conn := &model.VpnConnection{Tunnels: c.tunnels}
		if got := VpnConnectionAggregateStatus(conn); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	ecmp := []struct {
		name    string
		tunnels []*model.VpnTunnel
		want    string
	}{
		{"ecmp all up", []*model.VpnTunnel{tunnel(1, p, "up"), tunnel(2, p, "up")}, "up"},
		{"ecmp one up", []*model.VpnTunnel{tunnel(1, p, "down"), tunnel(2, p, "up")}, "degraded"},
		{"ecmp none up", []*model.VpnTunnel{tunnel(1, p, "down"), tunnel(2, p, "down")}, "down"},
	}
	for _, c := range ecmp {
		conn := &model.VpnConnection{TrafficPolicy: model.VpnTrafficPolicyEcmp, Tunnels: c.tunnels}
		if got := VpnConnectionAggregateStatus(conn); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestNormalizeTunnels(t *testing.T) {
	two := vpnTestGateway("52.0.0.1/28", "52.0.0.2/28")
	one := vpnTestGateway("52.0.0.1/28")

	// Defaults: slot 1 vip1 primary, slot 2 vip2 standby
	params := &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "1.1.1.1"}}}
	if err := normalizeTunnels(two, params, 0); err != nil {
		t.Fatalf("two addresses, one peer: %v", err)
	}
	if params.Tunnels[0].Endpoint != "vip1" || params.Tunnels[1].Endpoint != "vip2" ||
		params.Tunnels[0].Priority != "primary" || params.Tunnels[1].Priority != "standby" {
		t.Errorf("defaults: got %+v %+v", params.Tunnels[0], params.Tunnels[1])
	}

	// One address, one peer address: the two tunnels would be the same pair
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "1.1.1.1"}}}
	if err := normalizeTunnels(one, params, 0); err == nil {
		t.Error("one address and one peer address for two tunnels must be refused")
	}

	// One address, two peer addresses: fine, both on vip1
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "2.2.2.2"}}}
	if err := normalizeTunnels(one, params, 0); err != nil || params.Tunnels[1].Endpoint != "vip1" {
		t.Errorf("one address, two peers: err=%v endpoint=%s", err, params.Tunnels[1].Endpoint)
	}

	// One address, two responder-only peers: told apart by their identity (validate), not refused here
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteID: "peer-a"}, {RemoteID: "peer-b"}}}
	if err := normalizeTunnels(one, params, 0); err != nil {
		t.Errorf("one address, two responder-only peers: %v", err)
	}

	// vip2 on a gateway with one address
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{Endpoint: "vip2", RemoteGateway: "1.1.1.1"}}}
	if err := normalizeTunnels(one, params, 0); err == nil {
		t.Error("vip2 on a single-address gateway must be refused")
	}

	// Primary in slot 2
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "2.2.2.2", Priority: "primary"}}}
	if err := normalizeTunnels(one, params, 0); err != nil || params.Tunnels[0].Priority != "standby" {
		t.Errorf("primary in slot 2: err=%v first=%s", err, params.Tunnels[0].Priority)
	}

	// Two primaries, none, three tunnels
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1", Priority: "primary"}, {RemoteGateway: "2.2.2.2", Priority: "primary"}}}
	if err := normalizeTunnels(one, params, 0); err == nil {
		t.Error("two primaries must be refused")
	}
	params = &VpnConnectionParams{}
	if err := normalizeTunnels(one, params, 0); err == nil {
		t.Error("no tunnel must be refused")
	}
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{}, {}, {}, {}, {}}}
	if err := normalizeTunnels(two, params, 0); err == nil {
		t.Error("five tunnels must be refused")
	}

	// Full mesh: two addresses times two peer addresses, the endpoints alternate
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "1.1.1.1"}, {RemoteGateway: "2.2.2.2"}, {RemoteGateway: "2.2.2.2"}}}
	if err := normalizeTunnels(two, params, 0); err != nil {
		t.Fatalf("full mesh: %v", err)
	}
	for i, want := range []string{"vip1", "vip2", "vip1", "vip2"} {
		if params.Tunnels[i].Endpoint != want {
			t.Errorf("full mesh tunnel %d: endpoint %s, want %s", i+1, params.Tunnels[i].Endpoint, want)
		}
	}
	if params.Tunnels[0].Priority != "primary" || params.Tunnels[3].Priority != "standby" {
		t.Errorf("full mesh priorities: %s %s", params.Tunnels[0].Priority, params.Tunnels[3].Priority)
	}

	// ecmp: every tunnel is primary
	params = &VpnConnectionParams{TrafficPolicy: "ecmp", Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "2.2.2.2", Priority: "standby"}}}
	if err := normalizeTunnels(two, params, 0); err != nil || params.Tunnels[0].Priority != "primary" || params.Tunnels[1].Priority != "primary" {
		t.Errorf("ecmp: err=%v priorities %s %s", err, params.Tunnels[0].Priority, params.Tunnels[1].Priority)
	}
	params = &VpnConnectionParams{TrafficPolicy: "roundrobin", Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}}}
	if err := normalizeTunnels(two, params, 0); err == nil {
		t.Error("an unknown traffic policy must be refused")
	}
}

func vpnTestAAGateway(withClient bool) *model.VpnGateway {
	gateway := &model.VpnGateway{HaMode: model.VpnHaModeActiveActive}
	gateway.FloatingIps = append(gateway.FloatingIps,
		&model.FloatingIp{FipAddress: "52.0.0.1/28", VpnEndpoint: model.VpnEndpointNode1},
		&model.FloatingIp{FipAddress: "52.0.0.2/28", VpnEndpoint: model.VpnEndpointNode2})
	if withClient {
		gateway.FloatingIps = append(gateway.FloatingIps, &model.FloatingIp{FipAddress: "52.0.0.3/28", VpnEndpoint: model.VpnEndpointVip1})
	}
	return gateway
}

func TestNormalizeTunnelsActiveActive(t *testing.T) {
	gw := vpnTestAAGateway(true)
	params := &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "1.1.1.1"}}}
	if err := normalizeTunnels(gw, params, 0); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if params.Tunnels[0].Endpoint != "node1" || params.Tunnels[1].Endpoint != "node2" || params.Tunnels[0].Priority != "primary" {
		t.Errorf("defaults: %+v %+v", params.Tunnels[0], params.Tunnels[1])
	}
	// A second connection gets its primary on the other node
	gw.Connections = append(gw.Connections, &model.VpnConnection{Model: model.Model{ID: 7}, Tunnels: []*model.VpnTunnel{
		{Endpoint: "node1", Priority: "primary"}, {Endpoint: "node2", Priority: "standby"}}})
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "2.2.2.2"}}}
	if err := normalizeTunnels(gw, params, 0); err != nil || params.Tunnels[1].Priority != "primary" || params.Tunnels[0].Priority != "standby" {
		t.Errorf("alternation: err=%v %s %s", err, params.Tunnels[0].Priority, params.Tunnels[1].Priority)
	}
	// ... unless that other connection is the one being edited
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "2.2.2.2"}}}
	if err := normalizeTunnels(gw, params, 7); err != nil || params.Tunnels[0].Priority != "primary" {
		t.Errorf("alternation excluding itself: err=%v %s", err, params.Tunnels[0].Priority)
	}
	for name, tunnels := range map[string][]*VpnTunnelParams{
		"three tunnels":      {{RemoteGateway: "1.1.1.1"}, {RemoteGateway: "2.2.2.2"}, {RemoteGateway: "3.3.3.3"}},
		"both on one node":   {{Endpoint: "node1", RemoteGateway: "1.1.1.1"}, {Endpoint: "node1", RemoteGateway: "2.2.2.2"}},
		"client address":     {{Endpoint: "vip1", RemoteGateway: "1.1.1.1"}},
		"floating address 2": {{Endpoint: "vip2", RemoteGateway: "1.1.1.1"}},
	} {
		params = &VpnConnectionParams{Tunnels: tunnels}
		if err := normalizeTunnels(gw, params, 0); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	// node addresses are not tunnel endpoints of an active_standby gateway
	params = &VpnConnectionParams{Tunnels: []*VpnTunnelParams{{Endpoint: "node1", RemoteGateway: "1.1.1.1"}}}
	if err := normalizeTunnels(vpnTestGateway("52.0.0.1/28"), params, 0); err == nil {
		t.Error("node1 on an active_standby gateway must be refused")
	}
}

func TestVpnTunnelRanks(t *testing.T) {
	conn := &model.VpnConnection{AsPathPrepend: 2, Tunnels: []*model.VpnTunnel{
		{Model: model.Model{ID: 1}, Slot: 1, Priority: "standby"},
		{Model: model.Model{ID: 2}, Slot: 2, Priority: "primary"},
		{Model: model.Model{ID: 3}, Slot: 3, Priority: "standby"},
		{Model: model.Model{ID: 4}, Slot: 4, Priority: "standby"},
	}}
	ranks := vpnTunnelRanks(conn)
	if ranks[2] != 0 || ranks[1] != 1 || ranks[3] != 2 || ranks[4] != 3 {
		t.Fatalf("ranks: %v", ranks)
	}
	if vpnRankLocalPref(0) != 200 || vpnRankLocalPref(1) != 100 || vpnRankLocalPref(2) != 90 || vpnRankLocalPref(3) != 80 {
		t.Errorf("local preferences: %d %d %d %d", vpnRankLocalPref(0), vpnRankLocalPref(1), vpnRankLocalPref(2), vpnRankLocalPref(3))
	}
	if vpnRankPrepend(conn, 0) != 0 || vpnRankPrepend(conn, 1) != 2 || vpnRankPrepend(conn, 3) != 4 {
		t.Errorf("prepends: %d %d %d", vpnRankPrepend(conn, 0), vpnRankPrepend(conn, 1), vpnRankPrepend(conn, 3))
	}
	conn.AsPathPrepend = 0
	if vpnRankPrepend(conn, 3) != 0 {
		t.Error("no prepend configured must mean none on any tunnel")
	}
	conn.TrafficPolicy = model.VpnTrafficPolicyEcmp
	for id, rank := range vpnTunnelRanks(conn) {
		if rank != 0 {
			t.Errorf("ecmp tunnel %d has rank %d", id, rank)
		}
	}
}

func TestVpnOldMasterGone(t *testing.T) {
	gw := int64(900001)
	reported := time.Now().Add(-5 * time.Second)
	if r := vpnOldMasterGone(gw, 7, reported); r != "" {
		t.Fatalf("no evidence yet, got %q", r)
	}
	// A release older than the last master report does not count
	vpnHAMu.Lock()
	vpnReleased[gw] = vpnClaim{hostid: 7, at: reported.Add(-time.Second)}
	vpnHAMu.Unlock()
	if r := vpnOldMasterGone(gw, 7, reported); r != "" {
		t.Errorf("stale release accepted: %q", r)
	}
	vpnHAMu.Lock()
	vpnReleased[gw] = vpnClaim{hostid: 7, at: time.Now()}
	vpnHAMu.Unlock()
	if r := vpnOldMasterGone(gw, 7, reported); r == "" {
		t.Error("a fresh release must let the claim through")
	}
	vpnForgetClaim(gw)
	// cland lost the node
	vpnHAMu.Lock()
	vpnNodeAbsent[8] = time.Now()
	vpnHAMu.Unlock()
	if r := vpnOldMasterGone(gw, 8, reported); r == "" {
		t.Error("a node cland lost must let the claim through")
	}
	vpnHAMu.Lock()
	vpnNodeAbsent[8] = time.Now().Add(-2 * vpnHAEvidenceTTL)
	vpnHAMu.Unlock()
	if r := vpnOldMasterGone(gw, 8, reported); r != "" {
		t.Errorf("expired liveness evidence accepted: %q", r)
	}
	vpnHAMu.Lock()
	delete(vpnNodeAbsent, 8)
	vpnHAMu.Unlock()
}

func TestVpnReleasedRecently(t *testing.T) {
	gw := int64(900002)
	if vpnReleasedRecently(gw, 7) {
		t.Fatal("no release recorded yet")
	}
	vpnHAMu.Lock()
	vpnReleased[gw] = vpnClaim{hostid: 7, at: time.Now()}
	vpnHAMu.Unlock()
	if !vpnReleasedRecently(gw, 7) {
		t.Error("a claim of the node that just released must count as sent before the release")
	}
	if vpnReleasedRecently(gw, 8) {
		t.Error("another node's claim is not concerned")
	}
	vpnHAMu.Lock()
	vpnReleased[gw] = vpnClaim{hostid: 7, at: time.Now().Add(-2 * vpnReleaseGrace)}
	vpnHAMu.Unlock()
	if vpnReleasedRecently(gw, 7) {
		t.Error("an old release no longer holds the node's claims back")
	}
	vpnForgetClaim(gw)
}
