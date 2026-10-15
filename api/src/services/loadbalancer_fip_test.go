/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"testing"

	. "api/src/common"
	"api/src/model"
)

// Deleting a load balancer releases only the floating IPs the caller may release: an address a system admin
// attached from another organization is taken off and stays with its owner (1.1.0 regression run, code review)
func TestLBFloatingIpReleasable(t *testing.T) {
	own := &model.FloatingIp{Owner: 5}
	foreign := &model.FloatingIp{Owner: 6}
	writer := member(5, model.OrgWriter)
	admin := &MemberShip{UserID: 1, OrgID: 5, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}
	for name, c := range map[string]struct {
		m    *MemberShip
		fip  *model.FloatingIp
		want bool
	}{
		"writer, own address":     {writer, own, true},
		"writer, foreign address": {writer, foreign, false},
		"system admin, foreign":   {admin, foreign, true},
	} {
		if got := lbFloatingIpReleasable(c.m, c.fip); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
