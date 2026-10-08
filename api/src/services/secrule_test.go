/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import "testing"

func TestNormalizeRulePorts(t *testing.T) {
	cases := []struct {
		name             string
		protocol         string
		min, max         int32
		wantMin, wantMax int32
		wantErr          bool
	}{
		{name: "tcp without ports means all", protocol: "tcp", min: -1, max: -1, wantMin: 1, wantMax: 65535},
		{name: "tcp single port from min", protocol: "tcp", min: 22, max: -1, wantMin: 22, wantMax: 22},
		{name: "udp single port from max", protocol: "udp", min: -1, max: 53, wantMin: 53, wantMax: 53},
		{name: "tcp range", protocol: "tcp", min: 8000, max: 8080, wantMin: 8000, wantMax: 8080},
		{name: "tcp min greater than max", protocol: "tcp", min: 100, max: 10, wantErr: true},
		{name: "tcp out of range", protocol: "tcp", min: 1, max: 70000, wantErr: true},
		{name: "icmp any type and code", protocol: "icmp", min: -1, max: -1, wantMin: -1, wantMax: -1},
		{name: "icmp echo request type only", protocol: "icmp", min: 8, max: -1, wantMin: 8, wantMax: -1},
		{name: "icmp type and code", protocol: "icmp", min: 3, max: 4, wantMin: 3, wantMax: 4},
		{name: "icmp echo reply type 0 code 0", protocol: "icmp", min: 0, max: 0, wantMin: 0, wantMax: 0},
		{name: "icmp code without type", protocol: "icmp", min: -1, max: 0, wantErr: true},
		{name: "icmp type 255 reserved as any", protocol: "icmp", min: 255, max: -1, wantErr: true},
		{name: "icmp code out of range", protocol: "icmp", min: 3, max: 256, wantErr: true},
		{name: "icmp type below -1", protocol: "icmp", min: -2, max: -1, wantErr: true},
		{name: "gre ignores ports", protocol: "gre", min: 1, max: 2, wantMin: -1, wantMax: -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMin, gotMax, err := normalizeRulePorts(c.protocol, c.min, c.max)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %d/%d", gotMin, gotMax)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotMin != c.wantMin || gotMax != c.wantMax {
				t.Fatalf("got %d/%d, want %d/%d", gotMin, gotMax, c.wantMin, c.wantMax)
			}
		})
	}
}

func ptr(v int32) *int32 { return &v }

func TestRulePortsFromPayload(t *testing.T) {
	cases := []struct {
		name             string
		protocol         string
		min, max         *int32
		wantMin, wantMax int32
		wantErr          bool
	}{
		{name: "tcp not given", protocol: "tcp", wantMin: -1, wantMax: -1},
		{name: "tcp explicit range", protocol: "tcp", min: ptr(80), max: ptr(443), wantMin: 80, wantMax: 443},
		{name: "tcp explicit zero rejected", protocol: "tcp", min: ptr(0), max: ptr(1024), wantErr: true},
		{name: "udp explicit -1 rejected", protocol: "udp", min: ptr(-1), max: ptr(-1), wantErr: true},
		{name: "icmp type 0 kept", protocol: "icmp", min: ptr(0), wantMin: 0, wantMax: -1},
		{name: "icmp not given means any", protocol: "icmp", wantMin: -1, wantMax: -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMin, gotMax, err := RulePortsFromPayload(c.protocol, c.min, c.max)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %d/%d", gotMin, gotMax)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotMin != c.wantMin || gotMax != c.wantMax {
				t.Fatalf("got %d/%d, want %d/%d", gotMin, gotMax, c.wantMin, c.wantMax)
			}
		})
	}
}

func TestResolveRulePorts(t *testing.T) {
	cases := []struct {
		name                     string
		oldProtocol, newProtocol string
		oldMin, oldMax           int32
		min, max                 *int32
		wantMin, wantMax         int32
		wantErr                  bool
	}{
		{name: "keep ports when not given", oldProtocol: "tcp", newProtocol: "tcp", oldMin: 22, oldMax: 22, wantMin: 22, wantMax: 22},
		{name: "tcp to udp keeps ports", oldProtocol: "tcp", newProtocol: "udp", oldMin: 22, oldMax: 22, wantMin: 22, wantMax: 22},
		{name: "update max only", oldProtocol: "tcp", newProtocol: "tcp", oldMin: 8000, oldMax: 8000, max: ptr(8080), wantMin: 8000, wantMax: 8080},
		{name: "explicit zero port rejected", oldProtocol: "tcp", newProtocol: "tcp", oldMin: 1000, oldMax: 2000, min: ptr(0), wantErr: true},
		{name: "tcp to icmp resets to any", oldProtocol: "tcp", newProtocol: "icmp", oldMin: 22, oldMax: 22, wantMin: -1, wantMax: -1},
		{name: "icmp to tcp without ports rejected", oldProtocol: "icmp", newProtocol: "tcp", oldMin: 8, oldMax: 0, wantErr: true},
		{name: "icmp to tcp with port", oldProtocol: "icmp", newProtocol: "tcp", oldMin: 8, oldMax: 0, min: ptr(443), wantMin: 443, wantMax: -1},
		{name: "icmp set code 0 explicitly", oldProtocol: "icmp", newProtocol: "icmp", oldMin: 3, oldMax: -1, max: ptr(0), wantMin: 3, wantMax: 0},
		{name: "tcp to icmp with explicit type code", oldProtocol: "tcp", newProtocol: "icmp", oldMin: 22, oldMax: 22, min: ptr(8), max: ptr(0), wantMin: 8, wantMax: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMin, gotMax, err := resolveRulePorts(c.oldProtocol, c.newProtocol, c.oldMin, c.oldMax, c.min, c.max)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %d/%d", gotMin, gotMax)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotMin != c.wantMin || gotMax != c.wantMax {
				t.Fatalf("got %d/%d, want %d/%d", gotMin, gotMax, c.wantMin, c.wantMax)
			}
		})
	}
}
