/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "api/src/common"
	"api/src/model"
	"api/src/utils/tracing"

	"gorm.io/gorm/clause"
)

// Fast master takeover (docs/architecture/plan/vpn-gateway-plan.md §8.2, F4). clapi normally refuses a new
// master while the old one reported within vpnMasterFlapWindow: from the claims alone a dead master and a
// split brain look the same. Two other signals tell them apart, and either one lets the claim through:
//   - the old master said it gave the floating IP up (vpn_backup, sent as soon as keepalived demoted it)
//   - cland lost the old master: its command stream closed, or it stopped sending its per-second liveness
//     message (node_liveness). A split brain between the two VRRP nodes leaves that stream alone.
// The state is in memory: it matters for seconds, and a restart only falls back to the window.

const (
	// A liveness report or a claim older than this is not acted upon
	vpnHAEvidenceTTL = time.Minute
	// How long a node cland lost stays out of the routes of an active_active gateway without further news.
	// After that the topology status (offline) takes over; any report from the node brings it back at once.
	vpnNodeAbsentTTL = 10 * time.Minute
	// The floating IP holder re-sends its master claim every 20 s (report_vpn_status.sh master_interval):
	// four missed claims and no node holds the address as far as clapi can tell
	vpnMasterStale = 90 * time.Second
	// A master claim of a node that released the address less than this ago is one it sent before the
	// release: ignored (VpnGatewayMaster). Short against the 90 s after which a master without claims is cleared
	vpnReleaseGrace = 10 * time.Second
)

type vpnClaim struct {
	hostid int32
	at     time.Time
}

var (
	vpnHAMu       sync.Mutex
	vpnClaims     = map[int64]vpnClaim{}  // gateway id -> latest rejected master claim
	vpnReleased   = map[int64]vpnClaim{}  // gateway id -> node that last reported it gave the address up
	vpnNodeAbsent = map[int32]time.Time{} // hostid -> since when cland considers it gone
)

func vpnRecordClaim(gatewayID int64, hostid int32) {
	vpnHAMu.Lock()
	defer vpnHAMu.Unlock()
	vpnClaims[gatewayID] = vpnClaim{hostid: hostid, at: time.Now()}
}

func vpnForgetClaim(gatewayID int64) {
	vpnHAMu.Lock()
	defer vpnHAMu.Unlock()
	delete(vpnClaims, gatewayID)
	delete(vpnReleased, gatewayID)
}

// vpnReleasedRecently tells whether the node released the gateway's address within vpnReleaseGrace
func vpnReleasedRecently(gatewayID int64, hostid int32) bool {
	vpnHAMu.Lock()
	defer vpnHAMu.Unlock()
	r, ok := vpnReleased[gatewayID]
	return ok && r.hostid == hostid && time.Since(r.at) < vpnReleaseGrace
}

// vpnPendingClaim returns the node that recently claimed the gateway, or -1
func vpnPendingClaim(gatewayID int64) int32 {
	vpnHAMu.Lock()
	defer vpnHAMu.Unlock()
	c, ok := vpnClaims[gatewayID]
	if !ok || time.Since(c.at) > vpnHAEvidenceTTL {
		return -1
	}
	return c.hostid
}

// vpnOldMasterGone tells why the recorded master can be replaced right now, or "" when it cannot
func vpnOldMasterGone(gatewayID int64, oldMaster int32, oldReportedAt time.Time) string {
	vpnHAMu.Lock()
	defer vpnHAMu.Unlock()
	if r, ok := vpnReleased[gatewayID]; ok && r.hostid == oldMaster && r.at.After(oldReportedAt) {
		return "the old master released the address"
	}
	if since, ok := vpnNodeAbsent[oldMaster]; ok && time.Since(since) < vpnHAEvidenceTTL {
		return "cland lost the old master"
	}
	return ""
}

// vpnTakeOverPending accepts the pending claim of a gateway when the evidence now allows it
func vpnTakeOverPending(ctx context.Context, gatewayID int64) (err error) {
	claimant := vpnPendingClaim(gatewayID)
	if claimant < 0 {
		return
	}
	gateway, err := loadVpnGateway(ctx, gatewayID)
	if err != nil || gateway.MasterHyper == claimant || gateway.MasterReportedAt == nil {
		return
	}
	reason := vpnOldMasterGone(gatewayID, gateway.MasterHyper, *gateway.MasterReportedAt)
	if reason == "" {
		return
	}
	logger.Ctx(ctx).Infof("VPN gateway %d: node %d takes over from node %d (%s)", gatewayID, claimant, gateway.MasterHyper, reason)
	return vpnAcceptMaster(ctx, gateway, claimant)
}

// VpnGatewayBackup records that a VRRP node no longer holds the gateway's address
// |:-COMMAND-:| vpn_backup.sh '<gateway ID>' '<hostid>'
func VpnGatewayBackup(ctx context.Context, gatewayID int64, hostid int32) (err error) {
	gateway, err := loadVpnGateway(ctx, gatewayID)
	if err != nil {
		return
	}
	if !vpnHostIsVrrpNode(ctx, gateway, hostid) || gateway.MasterHyper != hostid {
		return
	}
	vpnHAMu.Lock()
	vpnReleased[gatewayID] = vpnClaim{hostid: hostid, at: time.Now()}
	vpnHAMu.Unlock()
	return vpnTakeOverPending(ctx, gatewayID)
}

// vpnNodeGone tells whether the other nodes should stop routing through a gateway node: cland lost it a
// short while ago, or the topology marks it offline
func vpnNodeGone(ctx context.Context, hostid int32) bool {
	vpnHAMu.Lock()
	since, ok := vpnNodeAbsent[hostid]
	vpnHAMu.Unlock()
	if ok && time.Since(since) < vpnNodeAbsentTTL {
		return true
	}
	_, db := GetContextDB(ctx)
	hyper := &model.Hyper{}
	return db.Select("status").Where("hostid = ?", hostid).Take(hyper).Error == nil && hyper.Status == 10
}

// vpnNodeTunnelsDown marks the tunnels an active_active gateway runs on a node cland lost as down, with the
// alarms a report would raise: that node cannot report them any more, and the other node only reports its own
func vpnNodeTunnelsDown(ctx context.Context, gateway *model.VpnGateway, hostid int32) *model.VpnGateway {
	hosts, err := VpnTunnelHosts(ctx, gateway)
	if err != nil {
		return nil
	}
	return vpnTunnelsDown(ctx, gateway.ID, func(t *model.VpnTunnel) bool {
		host, ok := hosts[t.ID]
		return ok && host == hostid
	}, fmt.Sprintf("node %d lost", hostid))
}

// vpnTunnelsDown marks the picked tunnels of a gateway down, with the connection status and the alarms a
// status report would have produced, and returns the gateway as it is now (nil when that failed). The
// gateway row is locked and the gateway read again first, as the status reports do (rpcs.VpnConnStatus):
// the connection status is derived from all the tunnels, and a report handled at the same time would
// otherwise leave a stale aggregate behind.
func vpnTunnelsDown(ctx context.Context, gatewayID int64, pick func(*model.VpnTunnel) bool, why string) *model.VpnGateway {
	ctx, db, newTransaction := StartTransaction(ctx)
	var err error
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", gatewayID).Take(&model.VpnGateway{}).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to lock VPN gateway %d: %v", gatewayID, err)
		return nil
	}
	gateway, err := loadVpnGateway(ctx, gatewayID)
	if err != nil {
		return nil
	}
	now := time.Now()
	for _, conn := range gateway.Connections {
		changed := []*model.VpnTunnel{}
		for _, t := range conn.Tunnels {
			if t.Status != model.VpnConnectionStatusUp || !pick(t) {
				continue
			}
			if err = db.Model(&model.VpnTunnel{Model: model.Model{ID: t.ID}}).Update("status", model.VpnConnectionStatusDown).Error; err != nil {
				logger.Ctx(ctx).Errorf("Failed to mark VPN tunnel %d down: %v", t.ID, err)
				return nil
			}
			t.Status = model.VpnConnectionStatusDown
			changed = append(changed, t)
		}
		if len(changed) == 0 {
			continue
		}
		newStatus := VpnConnectionAggregateStatus(conn)
		carrying := newStatus == model.VpnConnectionStatusUp || newStatus == model.VpnConnectionStatusDegraded
		if len(conn.Tunnels) > 1 && carrying {
			for _, t := range changed {
				NotifyVpnTunnelState(ctx, gateway, conn, t, false, now)
			}
		}
		if newStatus == conn.Status {
			continue
		}
		if (conn.Status == model.VpnConnectionStatusUp || conn.Status == model.VpnConnectionStatusDegraded) && newStatus == model.VpnConnectionStatusDown {
			NotifyVpnConnectionState(ctx, gateway, conn, false, now)
		}
		logger.Ctx(ctx).Infof("VPN connection %d of gateway %d: %s -> %s (%s)", conn.ID, gateway.ID, conn.Status, newStatus, why)
		if err = db.Model(&model.VpnConnection{Model: model.Model{ID: conn.ID}}).Update("status", newStatus).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to update status of VPN connection %d: %v", conn.ID, err)
			return nil
		}
		conn.Status = newStatus
	}
	return gateway
}

// vpnRerouteActiveActive handles a node of active_active gateways that cland lost (gone) or got back: its
// tunnels are marked down while it is gone, and once it is back its status caches are reset (the IPsec
// config push drops them) so that it reports at the next heartbeat instead of after up to five minutes;
// either way the routes of the other nodes are pushed again, the prefixes of its tunnels move to the other
// node while it is gone and back afterwards
func vpnRerouteActiveActive(ctx context.Context, hostid int32, gone bool) {
	ctx, db := GetContextDB(ctx)
	ifaces := []*model.Interface{}
	if err := db.Where("hyper = ? and type = 'vrrp'", hostid).Find(&ifaces).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query the VRRP interfaces of node %d: %v", hostid, err)
		return
	}
	for _, iface := range ifaces {
		gateway := &model.VpnGateway{}
		if db.Where("vrrp_instance_id = ? and ha_mode = ?", iface.Device, model.VpnHaModeActiveActive).Take(gateway).Error != nil {
			continue
		}
		gateway, err := loadVpnGateway(ctx, gateway.ID)
		if err != nil || gateway.Status != model.VpnGatewayStatusAvailable || gateway.Disabled {
			continue
		}
		if gone {
			// The routes below follow the tunnel states just written
			if updated := vpnNodeTunnelsDown(ctx, gateway, hostid); updated != nil {
				gateway = updated
			}
		} else if err = dispatchVpnIpsec(ctx, gateway, hostid); err != nil {
			logger.Ctx(ctx).Errorf("VPN gateway %d: failed to refresh node %d: %v", gateway.ID, hostid, err)
		}
		if err = dispatchVpnRoutes(ctx, gateway, -1); err != nil {
			logger.Ctx(ctx).Errorf("VPN gateway %d: failed to reroute after node %d changed: %v", gateway.ID, hostid, err)
		}
	}
}

// VpnHostIsVrrpNode tells whether the node is one of the two VRRP nodes of the gateway
func VpnHostIsVrrpNode(ctx context.Context, gateway *model.VpnGateway, hostid int32) bool {
	return vpnHostIsVrrpNode(ctx, gateway, hostid)
}

// VpnTunnelHosts maps each tunnel of an active_active gateway to the node that runs it (empty otherwise)
func VpnTunnelHosts(ctx context.Context, gateway *model.VpnGateway) (hosts map[int64]int32, err error) {
	hosts = map[int64]int32{}
	endpointHosts, err := vpnEndpointHosts(ctx, gateway)
	if err != nil || len(endpointHosts) == 0 {
		return
	}
	for _, conn := range gateway.Connections {
		for _, t := range conn.Tunnels {
			hosts[t.ID] = vpnTunnelHost(endpointHosts, t)
		}
	}
	return
}

// VpnNextHopSignature summarizes where the other nodes send the traffic of an active_active gateway
func VpnNextHopSignature(ctx context.Context, gateway *model.VpnGateway) string {
	return vpnNextHopSignature(ctx, gateway)
}

// VpnRerouteIfMoved re-pushes the routes of the other nodes when a status change moved where they should
// send the traffic of an active_active gateway (before: VpnNextHopSignature ahead of the change)
func VpnRerouteIfMoved(ctx context.Context, gatewayID int64, before string) (err error) {
	gateway, err := loadVpnGateway(ctx, gatewayID)
	if err != nil || !VpnIsActiveActive(gateway) || gateway.Status != model.VpnGatewayStatusAvailable || gateway.Disabled {
		return
	}
	if vpnNextHopSignature(ctx, gateway) == before {
		return
	}
	logger.Ctx(ctx).Infof("VPN gateway %d: the preferred gateway node of a connection changed, rerouting the other nodes", gatewayID)
	return dispatchVpnRoutes(ctx, gateway, -1)
}

// VpnNodeReported is called when a gateway node sent a status report: a node that reports is alive, so a
// node cland had lost (a missed "ok", a cland restart) is routed through again
func VpnNodeReported(ctx context.Context, hostid int32) {
	vpnHAMu.Lock()
	_, absent := vpnNodeAbsent[hostid]
	delete(vpnNodeAbsent, hostid)
	vpnHAMu.Unlock()
	if absent {
		logger.Ctx(ctx).Infof("Node %d reports again, routing through it", hostid)
		vpnRerouteActiveActive(ctx, hostid, false)
	}
}

// VpnNodeLiveness takes cland's view of a node: "gone" once its stream closed or it went silent, "ok"
// when it is back. A gone node that is the master of a gateway lets a pending claim through, and the other
// nodes stop routing through it for its active_active gateways.
// |:-COMMAND-:| node_liveness.sh '<hostid>' '<gone|ok>'
func VpnNodeLiveness(ctx context.Context, hostid int32, state string) (err error) {
	vpnHAMu.Lock()
	if state == "ok" {
		_, absent := vpnNodeAbsent[hostid]
		delete(vpnNodeAbsent, hostid)
		vpnHAMu.Unlock()
		if absent {
			vpnRerouteActiveActive(ctx, hostid, false)
		}
		return
	}
	_, known := vpnNodeAbsent[hostid]
	if !known {
		vpnNodeAbsent[hostid] = time.Now()
	}
	vpnHAMu.Unlock()
	if !known {
		vpnRerouteActiveActive(ctx, hostid, true)
	}
	ctx, db := GetContextDB(ctx)
	gateways := []*model.VpnGateway{}
	if err = db.Where("master_hyper = ?", hostid).Find(&gateways).Error; err != nil {
		return
	}
	for _, gateway := range gateways {
		if terr := vpnTakeOverPending(ctx, gateway.ID); terr != nil {
			logger.Ctx(ctx).Errorf("VPN gateway %d: takeover after node %d was lost failed: %v", gateway.ID, hostid, terr)
		}
	}
	return
}

// StartVpnMasterWatchdog catches the gateways whose floating IP no node claims any more. Only the holder
// reports (the backup stays silent by design), so without this a gateway whose keepalived faulted on both
// nodes, or whose two nodes both went away, keeps showing its last reports: master set, tunnels up.
func StartVpnMasterWatchdog() {
	go func() {
		// Claims come every 20 s: after a restart give the nodes the time to refresh them first
		time.Sleep(time.Minute)
		for {
			ctx, span := tracing.StartBackground(context.Background(), "vpn.master_watchdog")
			vpnCheckMasters(ctx)
			PlaceMissingVrrpBackups(ctx)
			span.End()
			time.Sleep(20 * time.Second)
		}
	}()
}

// vpnCheckMasters clears the master of every gateway whose claim is stale. An active_standby gateway runs
// its tunnels on the holder only, so they are down as well (with their alarms); an active_active gateway
// runs them on both nodes, reported by each, and only its client VPN is lost. The next claim sets the master
// again (a master of -1 is replaced at once) and the node re-sends its status within a minute.
func vpnCheckMasters(ctx context.Context) {
	ctx, db := GetContextDB(ctx)
	stale := []*model.VpnGateway{}
	if err := db.Select("id").Where("status = ? AND disabled = ? AND master_hyper >= 0 AND master_reported_at < ?",
		model.VpnGatewayStatusAvailable, false, time.Now().Add(-vpnMasterStale)).Find(&stale).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query the VPN gateways for stale masters: %v", err)
		return
	}
	for _, g := range stale {
		gateway, err := loadVpnGateway(ctx, g.ID)
		if err != nil {
			continue
		}
		// Updated only while the claim is still the stale one: a claim that just came in wins
		result := db.Model(&model.VpnGateway{}).Where("id = ? AND master_hyper = ? AND master_reported_at = ?",
			gateway.ID, gateway.MasterHyper, gateway.MasterReportedAt).Update("master_hyper", -1)
		if result.Error != nil || result.RowsAffected == 0 {
			continue
		}
		if VpnIsActiveActive(gateway) {
			logger.Ctx(ctx).Infof("VPN gateway %d: node %d stopped claiming the client VPN address %s ago, clearing the master",
				gateway.ID, gateway.MasterHyper, time.Since(*gateway.MasterReportedAt).Round(time.Second))
			continue
		}
		logger.Ctx(ctx).Warningf("VPN gateway %d: no node claimed its public address for %s (last master node %d), marking its tunnels down",
			gateway.ID, time.Since(*gateway.MasterReportedAt).Round(time.Second), gateway.MasterHyper)
		vpnTunnelsDown(ctx, gateway.ID, func(*model.VpnTunnel) bool { return true }, "no gateway node holds the public address")
	}
}
