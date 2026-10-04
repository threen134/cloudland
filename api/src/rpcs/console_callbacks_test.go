/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"testing"

	"api/src/model"
)

func TestParseVncPasswd(t *testing.T) {
	ok, err := parseVncPasswd([]string{"set_vnc_passwd", "12", "5906", "10.191.202.40"})
	if err != nil || ok.failed || ok.instanceID != 12 || ok.port != 5906 || ok.address != "10.191.202.40" {
		t.Errorf("address callback: %+v, %v", ok, err)
	}
	failed, err := parseVncPasswd([]string{"set_vnc_passwd", "12", "error", "VNC password authentication is off"})
	if err != nil || !failed.failed || failed.address != "" || failed.port != 0 || failed.reason == "" {
		t.Errorf("failure callback: %+v, %v", failed, err)
	}
	if failed, err = parseVncPasswd([]string{"set_vnc_passwd", "12", "error"}); err != nil || !failed.failed {
		t.Errorf("failure callback without reason: %+v, %v", failed, err)
	}
	for _, args := range [][]string{
		{"set_vnc_passwd", "12", "5906"},           // no address: the old handler read args[3] out of range
		{"set_vnc_passwd", "12", "-1", "10.0.0.1"}, // domain not running: no port
		{"set_vnc_passwd", "12", "5906", "not-an-ip"},
		{"set_vnc_passwd", "x", "5906", "10.0.0.1"},
		{"set_vnc_passwd", "12"},
	} {
		if r, err := parseVncPasswd(args); err == nil {
			t.Errorf("%q accepted: %+v", args, r)
		}
	}
}

func TestRescueStatus(t *testing.T) {
	cases := []struct {
		state, stage string
		want         model.InstanceStatus
	}{
		{"rescuing", "sync", ""},                            // rescue domain runs
		{"error", "sync", ""},                               // defined but not started: end_rescue cleans it up
		{"error", "failed", model.InstanceStatusShutoff},    // no rescue image: nothing to end, the instance is off
		{"running", "refused", model.InstanceStatusRunning}, // shared boot disk not usable: refused before the stop
		{"paused", "refused", model.InstanceStatusPaused},
		{"shut_off", "refused", model.InstanceStatusShutoff},
		{"", "refused", model.InstanceStatusShutoff}, // no domain state read
	}
	for _, c := range cases {
		if got := rescueStatus(c.state, c.stage); got != c.want {
			t.Errorf("rescueStatus(%q, %q) = %q, want %q", c.state, c.stage, got, c.want)
		}
	}
}

func TestPasswordReason(t *testing.T) {
	cases := []struct {
		result, current, want string
		keep                  bool
	}{
		{"error", "", InstanceReasonPasswordFailed, false},
		{"error", "init", InstanceReasonPasswordFailed, false},
		{"error", InstanceReasonPasswordFailed, InstanceReasonPasswordFailed, true},
		{"success", InstanceReasonPasswordFailed, "", false},
		{"success", "init", "init", true},
		{"success", "", "", true},
	}
	for _, c := range cases {
		reason, keep := passwordReason(c.result, c.current)
		if reason != c.want || keep != c.keep {
			t.Errorf("passwordReason(%q, %q) = %q, %v; want %q, %v", c.result, c.current, reason, keep, c.want, c.keep)
		}
	}
}
