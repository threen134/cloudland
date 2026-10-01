/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// FIP-07: only an "instance" or "load_balancer" key moves or detaches a floating IP; a body that only sets the
// bandwidth used to detach it
func TestFloatingIpChangeKeepsAttachment(t *testing.T) {
	cases := []struct {
		body     string
		retarget bool
		inbound  int32
	}{
		{`{}`, false, 0},
		{`{"inbound":100}`, false, 100},
		{`{"inbound":100,"outbound":50,"group":{"id":"0b1c6e0e-6f4e-4b44-9d59-4d7bd6a3a2a1"}}`, false, 100},
		{`{"instance":null}`, true, 0},
		{`{"instance":{"id":"0b1c6e0e-6f4e-4b44-9d59-4d7bd6a3a2a2"},"inbound":100}`, true, 100},
		{`{"load_balancer":null}`, true, 0},
		{`{"load_balancer":{"id":"0b1c6e0e-6f4e-4b44-9d59-4d7bd6a3a2a3"}}`, true, 0},
	}
	for _, c := range cases {
		payload := &FloatingIpPatchPayload{}
		if err := binding.JSON.BindBody([]byte(c.body), payload); err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.body), &fields); err != nil {
			t.Fatal(err)
		}
		change := floatingIpChange(fields, payload)
		if change.Retarget != c.retarget {
			t.Errorf("%s: retarget %v, want %v", c.body, change.Retarget, c.retarget)
		}
		got := int32(0)
		if change.Inbound != nil {
			got = *change.Inbound
		}
		if got != c.inbound {
			t.Errorf("%s: inbound %d, want %d", c.body, got, c.inbound)
		}
	}
}

// FAIL-6: a rename took names of 1 or 33 characters, which the creation refuses
func TestInstanceHostnameLength(t *testing.T) {
	engine := binding.Validator.Engine().(*validator.Validate)
	tag := func(v interface{}) string {
		field, _ := reflect.TypeOf(v).FieldByName("Hostname")
		return field.Tag.Get("binding")
	}
	for _, rule := range []string{tag(InstancePatchPayload{}), tag(InstancePayload{})} {
		for _, name := range []string{"ab", "web-01", strings.Repeat("a", 32)} {
			if err := engine.Var(name, rule); err != nil {
				t.Errorf("%q refused by %q: %v", name, rule, err)
			}
		}
		for _, name := range []string{"a", strings.Repeat("a", 33), "wp_c_x", "wp-c-"} {
			if err := engine.Var(name, rule); err == nil {
				t.Errorf("%q accepted by %q", name, rule)
			}
		}
	}
	// Leaving the name out of a PATCH is still allowed
	if err := binding.Validator.ValidateStruct(&InstancePatchPayload{PowerAction: "stop"}); err != nil {
		t.Errorf("patch without a hostname refused: %v", err)
	}
	if err := binding.Validator.ValidateStruct(&InstancePatchPayload{Hostname: "a"}); err == nil {
		t.Error("patch with a one-letter hostname accepted")
	}
}

// A bandwidth limit set once must be removable: 0 lifts it (set_floating_bandwidth.sh), out-of-range values are refused
func TestFloatingIpPatchBandwidthRange(t *testing.T) {
	val := func(v int32) *int32 { return &v }
	for _, v := range []int32{0, 1, 20000} {
		if err := binding.Validator.ValidateStruct(&FloatingIpPatchPayload{Inbound: val(v), Outbound: val(v)}); err != nil {
			t.Errorf("bandwidth %d refused: %v", v, err)
		}
	}
	for _, v := range []int32{-1, 20001} {
		if err := binding.Validator.ValidateStruct(&FloatingIpPatchPayload{Inbound: val(v)}); err == nil {
			t.Errorf("inbound %d accepted", v)
		}
		if err := binding.Validator.ValidateStruct(&FloatingIpPatchPayload{Outbound: val(v)}); err == nil {
			t.Errorf("outbound %d accepted", v)
		}
	}
	if err := binding.Validator.ValidateStruct(&FloatingIpPatchPayload{}); err != nil {
		t.Errorf("patch without bandwidth refused: %v", err)
	}
}
