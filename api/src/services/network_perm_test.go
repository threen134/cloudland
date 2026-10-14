/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Alignment 03 (test-items/alignment/03-网络.md): writes on load balancers, listeners and floating IPs need the
// writer role of the organization that owns them, refused before any statement and any command.

import (
	"testing"

	. "api/src/common"
	"api/src/model"
)

// N1, N2: a reader deleted an empty listener and renamed a load balancer
func TestLoadBalancerWritesNeedWriter(t *testing.T) {
	cland := startFakeCland(t)
	const org = 5
	for _, m := range []*MemberShip{member(org, model.OrgReader), member(org+1, model.OrgWriter)} {
		lb := &model.LoadBalancer{Model: model.Model{ID: 51}, Owner: org, Name: "lb"}
		ctx, touched := noDBContext(t, m)
		_, err := loadBalancerAdmin.Update(ctx, lb, "renamed", "changed")
		refusedEarly(t, "update load balancer", err, touched, cland)
		if lb.Name != "lb" {
			t.Errorf("refused update changed the name to %q", lb.Name)
		}
		listener := &model.Listener{Model: model.Model{ID: 52}, Owner: org, Name: "l"}
		refusedEarly(t, "delete listener", listenerAdmin.Delete(ctx, listener, lb), touched, cland)
	}
}

// N4: the organization of the instance a floating IP is attached to may detach it, not delete it through
// DELETE /floating_ips/:id (DeleteOwned). Deletes cascading from a load balancer or VPN gateway call Delete,
// which keeps the detach rule (TestFloatingIpCascadeDeletePG)
func TestFloatingIpDeleteNeedsOwner(t *testing.T) {
	cland := startFakeCland(t)
	fip := &model.FloatingIp{Model: model.Model{ID: 53}, Owner: 6, Type: string(PublicFloating),
		InstanceID: 54, Instance: &model.Instance{Model: model.Model{ID: 54}, Owner: 5}}
	ctx, touched := noDBContext(t, member(5, model.OrgWriter))
	refusedEarly(t, "delete another organization's floating ip", FloatingIpAdmin.DeleteOwned(ctx, fip), touched, cland)
	ctx, touched = noDBContext(t, member(6, model.OrgReader))
	refusedEarly(t, "reader deletes", FloatingIpAdmin.DeleteOwned(ctx, fip), touched, cland)
}
