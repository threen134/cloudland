/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"testing"

	"api/src/model"
)

func TestClaimsSubnetGateway(t *testing.T) {
	public := &model.Subnet{Name: "public-756", Gateway: "52.117.101.145/28"}
	internal := &model.Subnet{Name: "sn", Gateway: "192.168.30.1/24"}
	cases := []struct {
		name    string
		subnet  *model.Subnet
		address string
		ifType  string
		want    bool
	}{
		{"floating ip asks for the public gateway", public, "52.117.101.145", "floating", true},
		{"instance asks for the gateway with a prefix", public, "52.117.101.145/28", "instance", true},
		{"gateway stored without a prefix", &model.Subnet{Gateway: "10.0.0.1"}, "10.0.0.1/24", "instance", true},
		{"another address", public, "52.117.101.146", "floating", false},
		{"automatic allocation", public, "", "floating", false},
		{"the subnet's own gateway port", internal, "192.168.30.1/24", "gateway", false},
		{"any gateway interface type is exempt", public, "52.117.101.145", "gateway_public", false},
		{"subnet without a gateway", &model.Subnet{}, "10.0.0.1", "instance", false},
	}
	for _, c := range cases {
		if got := ClaimsSubnetGateway(c.subnet, c.address, c.ifType); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
