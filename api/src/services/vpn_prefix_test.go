/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"testing"

	"api/src/model"
)

func TestValidateClientRoutes(t *testing.T) {
	for value, ok := range map[string]bool{
		"192.168.93.0/24":                true,
		"10.0.1.0/24, 10.0.2.0/24":       true,
		"192.168.0.0/16":                 true,
		"0.0.0.0/0":                      false,
		"10.0.1.0/24,0.0.0.0/0":          false,
		"8.8.8.8/0":                      false, // normalized to 0.0.0.0/0
		"not-a-cidr":                     false,
		"10.0.1.0/24, 2001:db8::/32":     false,
		" 192.168.93.0/24 , 10.0.0.0/8 ": true,
	} {
		if err := validateClientRoutes(value); (err == nil) != ok {
			t.Errorf("%q: err=%v, want ok=%v", value, err, ok)
		}
	}
}

func TestVpnGatewayPlaced(t *testing.T) {
	standby := &model.VpnGateway{HaMode: model.VpnHaModeActiveStandby}
	legacy := &model.VpnGateway{}
	active := &model.VpnGateway{HaMode: model.VpnHaModeActiveActive}
	for _, c := range []struct {
		name    string
		gateway *model.VpnGateway
		nodes   []int32
		want    bool
	}{
		{"active_standby, both nodes", standby, []int32{1, 2}, true},
		{"active_standby, single node", standby, []int32{1}, true},
		{"active_standby, none yet", standby, nil, false},
		{"row from before the modes, single node", legacy, []int32{3}, true},
		{"active_active, both nodes", active, []int32{1, 2}, true},
		{"active_active, one node", active, []int32{1}, false},
	} {
		if got := vpnGatewayPlaced(c.gateway, c.nodes); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
