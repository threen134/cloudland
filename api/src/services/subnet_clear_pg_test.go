/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Deleting a subnet of a VPC clears its gateway port on every node, once the deletion is committed (PostgreSQL,
// CLAPI_TEST_DB_URI, see placement_pg_test.go). The command went to router.Hyper / router.Peer, which a VPC
// router never has any more (-1), so it was never sent and the port stayed on the nodes. A VNI another subnet
// still uses (subnets from before the check) only loses the address. A subnet of a VPC must not overlap another
// one of the VPC, a range its routers use nor a network its VPN gateway routes, and a VXLAN ID given for it must
// not belong to another subnet.

import (
	"fmt"
	"strings"
	"sync"
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

	// The VNI of a subnet of another VPC does not keep the port of this one (the node keeps a bridge another
	// VPC still uses)
	other := &model.Router{Owner: org, Name: p2Name("sc-vpc2")}
	must(t, db.Create(other).Error)
	t.Cleanup(func() { db.Unscoped().Delete(other) })
	mine := &model.Subnet{Owner: org, Name: p2Name("sc-mine"), Network: "192.168.238.0/24", Gateway: "192.168.238.1/24", Vlan: 9531008, Type: "internal", RouterID: router.ID}
	theirs := &model.Subnet{Owner: org, Name: p2Name("sc-theirs"), Network: "192.168.239.0/24", Gateway: "192.168.239.1/24", Vlan: 9531008, Type: "internal", RouterID: other.ID}
	must(t, db.Create(mine).Error)
	must(t, db.Create(theirs).Error)
	t.Cleanup(func() { db.Unscoped().Delete(mine); db.Unscoped().Delete(theirs) })
	cland.take()
	must(t, subnetAdmin.Delete(writer, mine))
	wantClear(t, cland, router, mine, "port")

	// Two subnets of the VPC on one VNI deleted at the same time: the second waits for the first and takes the port
	pair := []*model.Subnet{
		{Owner: org, Name: p2Name("sc-p1"), Network: "192.168.235.0/24", Gateway: "192.168.235.1/24", Vlan: 9531009, Type: "internal", RouterID: router.ID},
		{Owner: org, Name: p2Name("sc-p2"), Network: "192.168.236.0/24", Gateway: "192.168.236.1/24", Vlan: 9531009, Type: "internal", RouterID: router.ID},
	}
	for _, sn := range pair {
		must(t, db.Create(sn).Error)
		sn := sn
		t.Cleanup(func() { db.Unscoped().Delete(sn) })
	}
	cland.take()
	errs := make([]error, len(pair))
	var wg sync.WaitGroup
	for i := range pair {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = subnetAdmin.Delete(writer, pair[i])
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		must(t, err)
	}
	modes := map[string]int{}
	for _, cmd := range clearCommands(cland.take()) {
		for _, mode := range []string{"port", "address"} {
			if strings.Contains(cmd, "'"+mode+"'") {
				modes[mode]++
			}
		}
	}
	if modes["port"] != 1 || modes["address"] != 1 {
		t.Fatalf("concurrent deletions on one VNI sent %v, want one port and one address", modes)
	}

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
	for _, vpc := range []*model.Router{vpcA, vpcB} {
		sg := &model.SecurityGroup{Owner: org, Name: p2Name("sn-sg"), RouterID: vpc.ID}
		must(t, db.Create(sg).Error)
		must(t, db.Model(vpc).Update("default_sg", sg.ID).Error)
		t.Cleanup(func() {
			db.Unscoped().Where("secgroup = ?", sg.ID).Delete(&model.SecurityRule{})
			db.Unscoped().Delete(sg)
		})
	}
	inA := &model.Subnet{Owner: org, Name: p2Name("sn-a1"), Network: "192.168.241.0/24", Gateway: "192.168.241.1/24", Vlan: 9532001, Type: "internal", RouterID: vpcA.ID}
	inB := &model.Subnet{Owner: org, Name: p2Name("sn-b1"), Network: "192.168.242.0/24", Gateway: "192.168.242.1/24", Vlan: 9532002, Type: "internal", RouterID: vpcB.ID}
	must(t, db.Create(inA).Error)
	must(t, db.Create(inB).Error)
	t.Cleanup(func() { db.Unscoped().Delete(inA); db.Unscoped().Delete(inB) })
	// A VPN gateway that routes 10.251.0.0/16 into its tunnels. Its configuration can not be pushed (no public
	// address): that is logged once the subnet is committed and does not keep the subnet from being created
	gateway := &model.VpnGateway{Owner: org, Name: p2Name("sn-gw"), RouterID: vpcA.ID, Status: model.VpnGatewayStatusAvailable, MasterHyper: -1}
	must(t, db.Create(gateway).Error)
	must(t, db.Create(&model.VpnRemotePrefix{VpnGatewayID: gateway.ID, Cidr: "10.251.0.0/16", Source: model.VpnPrefixSourceConnectionStatic}).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("vpn_gateway_id = ?", gateway.ID).Delete(&model.VpnRemotePrefix{})
		db.Unscoped().Delete(gateway)
	})
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
		{0, "169.254.254.0/26", ErrSubnetCidrConflict},   // the transit gateway links
		{0, "169.1.2.0/24", ErrSubnetCidrConflict},       // the router links
		{0, "10.251.3.0/24", ErrSubnetCidrConflict},      // a network the VPN gateway routes
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

	// One VXLAN ID asked for by subnets of two VPCs at the same time: the VPC rows do not serialize them, the
	// lock on the VXLAN IDs does
	results := make([]error, 2)
	created := make([]*model.Subnet, 2)
	var wg sync.WaitGroup
	for i, c := range []struct {
		vpc     *model.Router
		network string
	}{{vpcA, "192.168.246.0/24"}, {vpcB, "192.168.247.0/24"}} {
		wg.Add(1)
		go func(i int, vpc *model.Router, network string) {
			defer wg.Done()
			created[i], results[i] = subnetAdmin.Create(writer, 9532004, p2Name("sn-race"), network, "", "", "", "internal", "", "", true, vpc, nil, 0)
		}(i, c.vpc, c.network)
	}
	wg.Wait()
	for i := range created {
		if results[i] == nil {
			s := created[i]
			t.Cleanup(func() {
				db.Unscoped().Where("subnet_id = ?", s.ID).Delete(&model.Address{})
				db.Unscoped().Delete(s)
			})
		}
	}
	ok, refused := 0, 0
	for _, err := range results {
		if err == nil {
			ok++
		} else if errCode(err) == ErrSubnetVniInUse {
			refused++
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("two subnets on VXLAN ID 9532004 at the same time: %v, want one created and one refused", results)
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
