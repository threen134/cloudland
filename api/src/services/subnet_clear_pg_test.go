/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Deleting a subnet of a VPC clears its gateway port on every node, once the deletion is committed (PostgreSQL,
// CLAPI_TEST_DB_URI, see placement_pg_test.go). The command went to router.Hyper / router.Peer, which a VPC
// router never has any more (-1), so it was never sent and the port stayed on the nodes. A VNI another subnet
// still uses (subnets from before the check) only loses the address. A subnet of a VPC must not overlap another
// one of the VPC, and a VXLAN ID given for it must not belong to another subnet.

import (
	"fmt"
	"strings"
	"testing"

	. "api/src/common"
	"api/src/model"
)

func TestSubnetDeleteClearsGatewayPortPG(t *testing.T) {
	cland := startFakeCland(t)
	ctx, db := pgContext(t, 1)
	org := p2Org(9531)
	writer := asMember(org, model.OrgWriter)
	router := &model.Router{Owner: org, Name: p2Name("sc-vpc")}
	must(t, db.Create(router).Error)
	t.Cleanup(func() { db.Unscoped().Delete(router) })
	for _, typ := range []string{"internal", "vrrp"} {
		subnet := &model.Subnet{Owner: org, Name: p2Name("sc-" + typ), Network: "192.168.196.0/24", Gateway: "192.168.196.1/24",
			Vlan: 9531001, Type: typ, RouterID: router.ID}
		if typ == "internal" {
			subnet.Network, subnet.Gateway, subnet.Vlan = "192.168.231.0/24", "192.168.231.1/24", 9531002
		}
		must(t, db.Create(subnet).Error)
		t.Cleanup(func() { db.Unscoped().Delete(subnet) })
		cland.take()
		must(t, subnetAdmin.Delete(writer, subnet))
		wantClear(t, cland, router, subnet, "port")
	}

	// Two subnets on one VNI: the first deleted only loses its address, the last takes the port
	a := &model.Subnet{Owner: org, Name: p2Name("sc-va"), Network: "192.168.233.0/24", Gateway: "192.168.233.1/24", Vlan: 9531004, Type: "internal", RouterID: router.ID}
	b := &model.Subnet{Owner: org, Name: p2Name("sc-vb"), Network: "192.168.234.0/24", Gateway: "192.168.234.1/24", Vlan: 9531004, Type: "internal", RouterID: router.ID}
	must(t, db.Create(a).Error)
	must(t, db.Create(b).Error)
	t.Cleanup(func() { db.Unscoped().Delete(a); db.Unscoped().Delete(b) })
	cland.take()
	must(t, subnetAdmin.Delete(writer, a))
	wantClear(t, cland, router, a, "address")
	must(t, subnetAdmin.Delete(writer, b))
	wantClear(t, cland, router, b, "port")

	// Part of a larger deletion: nothing is sent until the outer transaction commits, nothing when it rolls back
	c := &model.Subnet{Owner: org, Name: p2Name("sc-nest"), Network: "192.168.237.0/24", Gateway: "192.168.237.1/24", Vlan: 9531007, Type: "vrrp", RouterID: router.ID}
	must(t, db.Create(c).Error)
	t.Cleanup(func() { db.Unscoped().Delete(c) })
	cland.take()
	outer, _, _ := StartTransaction(writer)
	must(t, subnetAdmin.Delete(outer, c))
	if sent := clearCommands(cland.take()); len(sent) != 0 {
		t.Fatalf("sent inside the outer transaction: %v", sent)
	}
	EndTransaction(outer, fmt.Errorf("rolled back"))
	if sent := clearCommands(cland.take()); len(sent) != 0 {
		t.Fatalf("sent although the outer transaction rolled back: %v", sent)
	}
	var live int64
	must(t, db.Model(&model.Subnet{}).Where("id = ?", c.ID).Count(&live).Error)
	if live != 1 {
		t.Fatalf("the subnet is gone after the rollback")
	}
	outer, _, _ = StartTransaction(writer)
	must(t, subnetAdmin.Delete(outer, c))
	EndTransaction(outer, nil)
	wantClear(t, cland, router, c, "port")

	// A deletion that fails sends nothing: an address still in use
	subnet := &model.Subnet{Owner: org, Name: p2Name("sc-used"), Network: "192.168.232.0/24", Gateway: "192.168.232.1/24",
		Vlan: 9531003, Type: "internal", RouterID: router.ID}
	must(t, db.Create(subnet).Error)
	iface := &model.Interface{Name: "eth0", Subnet: subnet.ID, Type: "instance", Owner: org}
	must(t, db.Create(iface).Error)
	t.Cleanup(func() { db.Unscoped().Delete(iface); db.Unscoped().Delete(subnet) })
	cland.take()
	if err := subnetAdmin.Delete(writer, subnet); errCode(err) != ErrAddressInUse {
		t.Fatalf("subnet in use: got %v, want address in use", err)
	}
	if sent := clearCommands(cland.take()); len(sent) != 0 {
		t.Fatalf("command sent although the deletion failed: %v", sent)
	}
	_ = ctx
}

func TestSubnetCreateInVpcPG(t *testing.T) {
	startFakeCland(t)
	_, db := pgContext(t, 1)
	org := p2Org(9532)
	writer := asMember(org, model.OrgWriter)
	vpcA := &model.Router{Owner: org, Name: p2Name("sn-a")}
	vpcB := &model.Router{Owner: org, Name: p2Name("sn-b")}
	must(t, db.Create(vpcA).Error)
	must(t, db.Create(vpcB).Error)
	t.Cleanup(func() { db.Unscoped().Delete(vpcA); db.Unscoped().Delete(vpcB) })
	// A created subnet gets a rule in the default security group of its VPC
	sg := &model.SecurityGroup{Owner: org, Name: p2Name("sn-sg"), RouterID: vpcA.ID}
	must(t, db.Create(sg).Error)
	must(t, db.Model(vpcA).Update("default_sg", sg.ID).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("secgroup = ?", sg.ID).Delete(&model.SecurityRule{})
		db.Unscoped().Delete(sg)
	})
	inA :=&model.Subnet{Owner: org, Name: p2Name("sn-a1"), Network: "192.168.241.0/24", Gateway: "192.168.241.1/24", Vlan: 9532001, Type: "internal", RouterID: vpcA.ID}
	inB := &model.Subnet{Owner: org, Name: p2Name("sn-b1"), Network: "192.168.242.0/24", Gateway: "192.168.242.1/24", Vlan: 9532002, Type: "internal", RouterID: vpcB.ID}
	must(t, db.Create(inA).Error)
	must(t, db.Create(inB).Error)
	t.Cleanup(func() { db.Unscoped().Delete(inA); db.Unscoped().Delete(inB) })
	create := func(vlan int, network string) (*model.Subnet, error) {
		return subnetAdmin.Create(writer, vlan, p2Name("sn-new"), network, "", "", "", "internal", "", "", true, vpcA, nil, 0)
	}
	for _, c := range []struct {
		vlan    int
		network string
		want    ErrCode
	}{
		{0, "192.168.241.128/25", ErrSubnetCidrConflict}, // inside the subnet of the VPC
		{0, "192.168.240.0/23", ErrSubnetCidrConflict},   // around it
		{0, "192.168.196.0/26", ErrSubnetCidrConflict},   // the VRRP subnet range
		{9532002, "192.168.243.0/24", ErrSubnetVniInUse}, // the VNI of the other VPC
		{9532001, "192.168.243.0/24", ErrSubnetVniInUse}, // the VNI of the VPC itself: no gateway on its nodes
		{756, "192.168.243.0/24", ErrInvalidParameter},   // a VLAN ID
	} {
		if s, err := create(c.vlan, c.network); errCode(err) != c.want {
			t.Fatalf("vlan %d %s: got %v (%v), want %d", c.vlan, c.network, err, s, c.want)
		}
	}
	var count int64
	must(t, db.Model(&model.Subnet{}).Where("router_id = ?", vpcA.ID).Count(&count).Error)
	if count != 1 {
		t.Fatalf("%d subnets in the VPC after the refusals, want 1", count)
	}
	// A VNI nobody uses, and one picked by clapi
	for _, c := range []struct {
		vlan    int
		network string
	}{{9532003, "192.168.244.0/24"}, {0, "192.168.245.0/24"}} {
		s, err := create(c.vlan, c.network)
		must(t, err)
		t.Cleanup(func() {
			db.Unscoped().Where("subnet_id = ?", s.ID).Delete(&model.Address{})
			db.Unscoped().Delete(s)
		})
		if c.vlan > 0 && s.Vlan != int64(c.vlan) || s.Vlan < int64(vniMin) {
			t.Fatalf("created with VNI %d, want %d", s.Vlan, c.vlan)
		}
	}
}

// clearCommands keeps the clear_subnet_gw.sh commands of what the fake cland received
func clearCommands(sent []string) (cmds []string) {
	for _, cmd := range sent {
		if strings.Contains(cmd, "clear_subnet_gw.sh") {
			cmds = append(cmds, cmd)
		}
	}
	return
}

func wantClear(t *testing.T, cland *fakeCland, router *model.Router, subnet *model.Subnet, mode string) {
	t.Helper()
	want := fmt.Sprintf("toall= | /opt/cloudland/scripts/backend/clear_subnet_gw.sh '%d' '%d' '%s' '%s' '%s'", router.ID, subnet.Vlan, subnet.Network, subnet.Gateway, mode)
	sent := clearCommands(cland.take())
	if len(sent) != 1 || !strings.HasPrefix(sent[0], want) {
		t.Fatalf("%s: sent %v, want %q", subnet.Name, sent, want)
	}
}
