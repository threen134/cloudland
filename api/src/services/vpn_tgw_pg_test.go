/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A change of the VPN networks of a member VPC of a transit gateway moves the gateway to a new generation and the
// nodes get it once the change is committed: the member routers throw the VPN networks out of table 252
// (PostgreSQL, CLAPI_TEST_DB_URI, see placement_pg_test.go)

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestVpnPrefixesMoveTransitGatewayPG(t *testing.T) {
	org := int64(900000 + time.Now().Unix()%90000)
	ctx, db := pgContext(t, org)
	cland := startFakeCland(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	f := &tgwPGFixture{t: t, ctx: ctx, org: org, tag: tag, vlan: 7100000 + time.Now().Unix()%1000000*3}
	must(t, db.Unscoped().Where("hostid = ?", 9111).Delete(&model.Hyper{}).Error)
	hyper := &model.Hyper{Hostid: 9111, Hostname: "vt-h1", Status: 1, HostIP: "10.9.9.11"}
	must(t, db.Create(hyper).Error)
	t.Cleanup(func() { db.Unscoped().Delete(hyper) })
	ra := f.router("vt-a")
	f.nic(f.subnet(ra, "172.30.1.0/24", "internal"), "172.30.1.10", 9111)
	tgw, err := TransitGatewayAdmin.Create(ctx, "vt-"+tag[len(tag)-6:], "")
	must(t, err)
	t.Cleanup(func() {
		for _, m := range []interface{}{&model.TgwAttachment{}, &model.TgwPropagation{}, &model.TgwRoute{}, &model.TgwRouteTable{}, &model.TgwNodeState{}} {
			db.Unscoped().Where("tgw_id = ?", tgw.ID).Delete(m)
		}
		db.Unscoped().Delete(tgw)
	})
	_, err = TransitGatewayAdmin.Attach(ctx, tgw, ra, nil, true)
	must(t, err)
	gateway := &model.VpnGateway{Owner: org, Name: "vt-gw-" + tag[len(tag)-6:], RouterID: ra.ID, Status: model.VpnGatewayStatusAvailable, MasterHyper: -1}
	must(t, db.Create(gateway).Error)
	conn := &model.VpnConnection{Owner: org, Name: "vt-c", VpnGatewayID: gateway.ID, RouteMode: model.VpnRouteModeStatic, RemoteCidrs: "10.99.70.0/24"}
	must(t, db.Create(conn).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("vpn_gateway_id = ?", gateway.ID).Delete(&model.VpnRemotePrefix{})
		db.Unscoped().Delete(conn)
		db.Unscoped().Delete(gateway)
	})
	cland.take()

	// A new VPN network: the generation moves on in the transaction, the push waits for the commit
	g0 := f.gen(tgw)
	tctx, _, _ := StartTransaction(ctx)
	_, err = rebuildRemotePrefixes(tctx, gateway)
	must(t, err)
	if sent := cland.take(); len(sent) != 0 {
		t.Fatalf("sent before the commit: %v", sent)
	}
	EndTransaction(tctx, nil)
	if g := f.gen(tgw); g != g0+1 {
		t.Fatalf("generation %d after a new VPN network, want %d", g, g0+1)
	}
	applies, _ := parseSent(t, cland.take())
	if len(applies) != 1 {
		t.Fatalf("pushes after the commit: %d, want 1", len(applies))
	}
	throws := applies[0].state.Attachments[0].Throw
	found := false
	for _, c := range throws {
		if c == "10.99.70.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the VPN network is not thrown: %v", throws)
	}

	// The same networks again: nothing moves
	tctx, _, _ = StartTransaction(ctx)
	_, err = rebuildRemotePrefixes(tctx, gateway)
	must(t, err)
	EndTransaction(tctx, nil)
	if g := f.gen(tgw); g != g0+1 {
		t.Fatalf("generation %d without a change, want %d", g, g0+1)
	}
	if sent := cland.take(); len(sent) != 0 {
		t.Fatalf("sent without a change: %v", sent)
	}

	// A change rolled back: neither the generation nor a push
	must(t, db.Model(conn).Update("remote_cidrs", "10.99.71.0/24").Error)
	tctx, _, _ = StartTransaction(ctx)
	_, err = rebuildRemotePrefixes(tctx, gateway)
	must(t, err)
	EndTransaction(tctx, fmt.Errorf("rolled back"))
	if g := f.gen(tgw); g != g0+1 {
		t.Fatalf("generation %d after a rollback, want %d", g, g0+1)
	}
	if sent := cland.take(); len(sent) != 0 {
		t.Fatalf("sent after a rollback: %v", sent)
	}

	// Without a transaction (a context that only carries a plain connection) the push happens at once, and
	// it already has the rewritten rows
	must(t, db.Model(conn).Update("remote_cidrs", "10.99.72.0/24").Error)
	plain, _ := GetContextDB(ctx)
	_, err = rebuildRemotePrefixes(plain, gateway)
	must(t, err)
	applies, _ = parseSent(t, cland.take())
	if len(applies) != 1 {
		t.Fatalf("pushes without a transaction: %d, want 1", len(applies))
	}
	found = false
	for _, c := range applies[0].state.Attachments[0].Throw {
		found = found || c == "10.99.72.0/24"
	}
	if !found {
		t.Fatalf("the push without a transaction misses the new VPN network: %v", applies[0].state.Attachments[0].Throw)
	}
}

// AfterCommit outside a transaction runs at once; inside one, only once it is committed
func TestAfterCommitPG(t *testing.T) {
	ctx, _ := pgContext(t, 1)
	ran := 0
	AfterCommit(ctx, func(context.Context) { ran++ })
	if ran != 1 {
		t.Fatalf("outside a transaction: ran %d times, want 1", ran)
	}
	tctx, _, started := StartTransaction(ctx)
	if !started {
		t.Fatal("no transaction started")
	}
	inner, _, nested := StartTransaction(tctx)
	AfterCommit(inner, func(c context.Context) {
		ran++
		// the context the dispatch gets is outside the finished transaction, and has no pending list of its own
		next, _, again := StartTransaction(c)
		if !again {
			t.Error("the dispatch context still carries the committed transaction")
		}
		EndTransaction(next, fmt.Errorf("only checked"))
		AfterCommit(c, func(context.Context) { ran += 10 })
	})
	if nested || ran != 1 {
		t.Fatalf("nested %v, ran %d inside the transaction", nested, ran)
	}
	EndTransaction(tctx, nil)
	if ran != 12 {
		t.Fatalf("after the commit ran is %d, want 12", ran)
	}
	tctx, _, _ = StartTransaction(ctx)
	AfterCommit(tctx, func(context.Context) { ran += 100 })
	EndTransaction(tctx, fmt.Errorf("rolled back"))
	if ran != 12 {
		t.Fatalf("ran after a rollback: %d", ran)
	}
}
