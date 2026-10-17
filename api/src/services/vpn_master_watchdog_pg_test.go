/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The master watchdog clears a stale claim, and marks the tunnels of an active_standby gateway down, in one
// transaction holding the gateway row and checking under it that the claim is still stale; a claim takes the same
// lock before it reads the gateway (2026-10-17: after a reboot of work-01 the watchdog cleared a claim that had just
// come in and the tunnels of r2-gwc went down for a second with VpnConnectionDown alarms, R2-19)
// (PostgreSQL, CLAPI_TEST_DB_URI, see placement_pg_test.go)

import (
	"fmt"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestVpnMasterWatchdogPG(t *testing.T) {
	org := int64(900000 + time.Now().Unix()%90000)
	ctx, db := pgContext(t, org)
	old := time.Now().Add(-5 * time.Minute)
	tag := fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	gateway := &model.VpnGateway{Owner: org, Name: "vw-" + tag, Status: model.VpnGatewayStatusAvailable,
		HaMode: model.VpnHaModeActiveStandby, MasterHyper: 1, MasterReportedAt: &old}
	must(t, db.Create(gateway).Error)
	conn := &model.VpnConnection{Owner: org, Name: "vw-c", VpnGatewayID: gateway.ID, RouteMode: model.VpnRouteModeStatic,
		RemoteCidrs: "10.99.80.0/24", Status: model.VpnConnectionStatusUp}
	must(t, db.Create(conn).Error)
	tunnel := &model.VpnTunnel{Owner: org, VpnGatewayID: gateway.ID, VpnConnectionID: conn.ID, Slot: 1, Status: model.VpnConnectionStatusUp}
	must(t, db.Create(tunnel).Error)
	t.Cleanup(func() {
		db.Unscoped().Delete(tunnel)
		db.Unscoped().Delete(conn)
		db.Unscoped().Delete(gateway)
	})
	set := func(master int32, at time.Time) {
		t.Helper()
		must(t, db.Model(&model.VpnGateway{}).Where("id = ?", gateway.ID).Updates(map[string]interface{}{"master_hyper": master, "master_reported_at": at}).Error)
		must(t, db.Model(&model.VpnTunnel{}).Where("id = ?", tunnel.ID).Update("status", model.VpnConnectionStatusUp).Error)
		must(t, db.Model(&model.VpnConnection{}).Where("id = ?", conn.ID).Update("status", model.VpnConnectionStatusUp).Error)
	}
	state := func() string {
		g, tn := &model.VpnGateway{}, &model.VpnTunnel{}
		must(t, db.Select("master_hyper").Where("id = ?", gateway.ID).Take(g).Error)
		must(t, db.Select("status").Where("id = ?", tunnel.ID).Take(tn).Error)
		return fmt.Sprintf("master=%d tunnel=%s", g.MasterHyper, tn.Status)
	}
	// begin opens a transaction that is rolled back however the test ends
	begin := func() *gorm.DB {
		tx := db.Begin()
		t.Cleanup(func() { tx.Rollback() })
		return tx
	}

	// Still the stale claim: cleared, the tunnel marked down
	if !vpnClearStaleMaster(ctx, gateway.ID) || state() != "master=-1 tunnel=down" {
		t.Fatalf("a stale claim: %s, want it cleared and the tunnel down", state())
	}

	// A claim committed after vpnCheckMasters' query read the stale one: kept, the tunnel left up
	set(1, old)
	set(3, time.Now())
	if vpnClearStaleMaster(ctx, gateway.ID) || state() != "master=3 tunnel=up" {
		t.Fatalf("a claim committed after the query: %s, want it kept and the tunnel up", state())
	}

	// A claim holding the row (VpnGatewayMaster locks it before reading): the watchdog waits, then finds it fresh
	set(1, old)
	tx := begin()
	must(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", gateway.ID).Take(&model.VpnGateway{}).Error)
	done := make(chan bool, 1)
	go func() { done <- vpnClearStaleMaster(ctx, gateway.ID) }()
	select {
	case <-done:
		t.Fatalf("the watchdog did not wait for the claim holding the gateway")
	case <-time.After(300 * time.Millisecond):
	}
	must(t, tx.Model(&model.VpnGateway{}).Where("id = ?", gateway.ID).Updates(map[string]interface{}{"master_hyper": 3, "master_reported_at": time.Now()}).Error)
	must(t, tx.Commit().Error)
	select {
	case cleared := <-done:
		if cleared || state() != "master=3 tunnel=up" {
			t.Fatalf("a claim committing while the watchdog waited: cleared=%v %s, want it kept", cleared, state())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the watchdog is still waiting after the claim committed")
	}

	// The watchdog holds the row until its tunnels are marked down: a claim waits for all of it, then reads the
	// master cleared and the tunnel down (it sets the master again; the node's next report brings the tunnel up)
	set(1, old)
	wctx, wdb, _ := StartTransaction(ctx)
	t.Cleanup(func() { wdb.Rollback() })
	if !vpnClearStaleMaster(wctx, gateway.ID) {
		t.Fatalf("a stale claim inside a transaction was not cleared")
	}
	read := make(chan string, 1)
	go func() {
		// the claim reads and ends: it must not keep the row
		c := db.Begin()
		defer c.Rollback()
		g := &model.VpnGateway{}
		if err := c.Clauses(clause.Locking{Strength: "UPDATE"}).Select("master_hyper").Where("id = ?", gateway.ID).Take(g).Error; err != nil {
			read <- err.Error()
			return
		}
		tn := &model.VpnTunnel{}
		c.Select("status").Where("id = ?", tunnel.ID).Take(tn)
		read <- fmt.Sprintf("master=%d tunnel=%s", g.MasterHyper, tn.Status)
	}()
	select {
	case s := <-read:
		t.Fatalf("a claim read the gateway while the watchdog held it: %s", s)
	case <-time.After(300 * time.Millisecond):
	}
	EndTransaction(wctx, nil)
	select {
	case s := <-read:
		if s != "master=-1 tunnel=down" {
			t.Fatalf("the claim read %s after the watchdog committed, want master=-1 tunnel=down", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the claim is still waiting after the watchdog committed")
	}

	// A claim refreshed by the master itself in time: not stale, nothing happens
	set(1, time.Now().Add(-vpnMasterStale+20*time.Second))
	if vpnClearStaleMaster(ctx, gateway.ID) || state() != "master=1 tunnel=up" {
		t.Fatalf("a claim refreshed in time: %s, want it kept", state())
	}
}
