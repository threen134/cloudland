/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"gorm.io/gorm/clause"
)

func init() {
	Add("vpn_gateway", VpnGatewayState)
	Add("vpn_master", VpnMaster)
	Add("vpn_conn_status", VpnConnStatus)
	Add("vpn_client_status", VpnClientStatus)
	Add("vpn_bgp_status", VpnBgpStatus)
	Add("recover_vpn_gateway", RecoverVpnGateway)
	Add("vpn_backup", VpnBackup)
	Add("node_liveness", NodeLiveness)
}

// Callbacks from the node scripts (report_vpn_status.sh, create_vpn_gateway.sh). All of them are
// idempotent: cland retries a callback up to three times. Status payloads travel base64 encoded in one
// argument because the callback line is split on quotes and spaces.

func vpnCallbackArgs(ctx context.Context, args []string, n int) (gatewayID int64, hostid int32, err error) {
	if len(args) < n {
		err = fmt.Errorf("wrong params")
		return
	}
	gatewayID, err = strconv.ParseInt(args[1], 10, 64)
	if err != nil || gatewayID <= 0 {
		err = fmt.Errorf("invalid gateway id %q", args[1])
		return
	}
	// The executing node is the message extra, the argument is only a cross-check
	hostid, _ = ctx.Value("hostid").(int32)
	if reported, perr := strconv.Atoi(args[2]); perr == nil && hostid <= 0 {
		hostid = int32(reported)
	}
	return
}

func vpnDecodePayload(arg string, v interface{}) error {
	data, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// VpnGatewayState marks the gateway available (or in error) once a VRRP node built it
// |:-COMMAND-:| vpn_gateway.sh '<gateway ID>' '<hostid>' 'ready|error'
func VpnGatewayState(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	_ = db
	gatewayID, hostid, err := vpnCallbackArgs(ctx, args, 4)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	if err = services.VpnGatewayReady(ctx, gatewayID, hostid, args[3]); err != nil {
		logger.Ctx(ctx).Errorf("Failed to record state of VPN gateway %d: %v", gatewayID, err)
	}
	return
}

// VpnMaster records which node holds the floating IP; reported by the node that has it
// |:-COMMAND-:| vpn_master.sh '<gateway ID>' '<hostid>'
func VpnMaster(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	_ = db
	gatewayID, hostid, err := vpnCallbackArgs(ctx, args, 3)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	if err = services.VpnGatewayMaster(ctx, gatewayID, hostid); err != nil {
		logger.Ctx(ctx).Errorf("Failed to record master of VPN gateway %d: %v", gatewayID, err)
	}
	return
}

// vpnLoadForReport loads the gateway a status report is about and checks that the node may send it: the
// floating IP holder (the master), so that a late report from the previous master cannot overwrite the new
// one; for the tunnels of an active_active gateway either VRRP node, each for the tunnels it runs
// (tunnelHosts tells which node runs which tunnel). clients: a WireGuard report, always the master's.
func vpnLoadForReport(ctx context.Context, args []string, clients bool) (gateway *model.VpnGateway, hostid int32, tunnelHosts map[int64]int32, err error) {
	gatewayID, hostid, err := vpnCallbackArgs(ctx, args, 4)
	if err != nil {
		return
	}
	ctx, db := GetContextDB(ctx)
	gateway = &model.VpnGateway{Model: model.Model{ID: gatewayID}}
	if err = db.Preload("Connections").Preload("Connections.Tunnels").Preload("Clients").Take(gateway).Error; err != nil {
		return
	}
	if gateway.Disabled {
		// A report sent just before the node learned about the pause: the connections stay "disabled"
		err = fmt.Errorf("VPN gateway %d is disabled, status report of node %d ignored", gatewayID, hostid)
		return
	}
	if services.VpnIsActiveActive(gateway) && !clients {
		if !services.VpnHostIsVrrpNode(ctx, gateway, hostid) {
			err = fmt.Errorf("VPN gateway %d status reported by node %d which is not one of its nodes", gatewayID, hostid)
			return
		}
		if tunnelHosts, err = services.VpnTunnelHosts(ctx, gateway); err != nil {
			return
		}
		services.VpnNodeReported(ctx, hostid)
		return
	}
	if gateway.MasterHyper != hostid {
		err = fmt.Errorf("VPN gateway %d status reported by node %d which is not the master (%d)", gatewayID, hostid, gateway.MasterHyper)
	}
	return
}

// vpnTunnelElsewhere tells whether a tunnel of an active_active gateway runs on another node than the
// reporting one: its state is that node's to report
func vpnTunnelElsewhere(tunnelHosts map[int64]int32, tunnel *model.VpnTunnel, hostid int32) bool {
	host, ok := tunnelHosts[tunnel.ID]
	return ok && host != hostid
}

type vpnConnReport struct {
	Name          string `json:"name"`
	State         string `json:"state"` // up or down
	EstablishedAt int64  `json:"established_at"`
	BytesIn       int64  `json:"bytes_in"`
	BytesOut      int64  `json:"bytes_out"`
	Error         string `json:"error"`
}

// vpnTunnelLastErrorMax is the size of vpn_tunnels.last_error (varchar(512), in characters)
const vpnTunnelLastErrorMax = 512

// vpnTunnelLastError is what a report leaves in last_error: the failure charon logged for a tunnel that is
// down (report_vpn_status.sh), nothing for one that is up. Cut to the column size, since a longer text
// would fail the update and with it the status of every tunnel of the report.
func vpnTunnelLastError(r *vpnConnReport) string {
	if r.State == "up" {
		return ""
	}
	text := strings.TrimSpace(r.Error)
	if runes := []rune(text); len(runes) > vpnTunnelLastErrorMax {
		text = string(runes[:vpnTunnelLastErrorMax])
	}
	return text
}

// VpnConnStatus stores the IKE SA state of every tunnel (one entry per tunnel name) and derives the
// status of each connection from its tunnels
// |:-COMMAND-:| vpn_conn_status.sh '<gateway ID>' '<hostid>' '<base64 json array>'
func VpnConnStatus(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	// The two nodes of an active_active gateway report at the same time (cland forwards callbacks
	// concurrently), and each report updates its own tunnels, then derives the connection status from all
	// of them. Without the gateway row lock both would read the other's tunnels before its update and write
	// a stale aggregate: both tunnels down, the connection left degraded, no alarm until the next report.
	// Locked before the gateway is loaded, so the second report reads what the first one committed.
	if len(args) > 1 {
		if gatewayID, perr := strconv.ParseInt(args[1], 10, 64); perr == nil {
			if lerr := db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", gatewayID).Take(&model.VpnGateway{}).Error; lerr != nil {
				logger.Ctx(ctx).Warningf("VPN gateway %d of a status report: %v", gatewayID, lerr)
				return
			}
		}
	}
	gateway, hostid, tunnelHosts, err := vpnLoadForReport(ctx, args, false)
	if err != nil {
		logger.Ctx(ctx).Warning(err)
		err = nil
		return
	}
	reports := []*vpnConnReport{}
	if err = vpnDecodePayload(args[3], &reports); err != nil {
		logger.Ctx(ctx).Error("Invalid connection status payload", err)
		err = nil
		return
	}
	byName := map[string]*vpnConnReport{}
	for _, r := range reports {
		byName[r.Name] = r
	}
	now := time.Now()
	before := services.VpnNextHopSignature(ctx, gateway)
	for _, conn := range gateway.Connections {
		type transition struct {
			tunnel   *model.VpnTunnel
			from, to string
		}
		transitions := []transition{}
		for _, tunnel := range conn.Tunnels {
			if vpnTunnelElsewhere(tunnelHosts, tunnel, hostid) {
				continue
			}
			r, ok := byName[services.VpnTunnelName(tunnel)]
			newStatus := model.VpnConnectionStatusDown
			updates := map[string]interface{}{}
			if ok {
				if r.State == "up" {
					newStatus = model.VpnConnectionStatusUp
				}
				updates["bytes_in"], updates["bytes_out"] = r.BytesIn, r.BytesOut
				if r.EstablishedAt > 0 {
					updates["established_at"] = time.Unix(r.EstablishedAt, 0)
				}
				if lastError := vpnTunnelLastError(r); lastError != tunnel.LastError {
					updates["last_error"] = lastError
				}
			}
			if newStatus != tunnel.Status {
				updates["status"] = newStatus
				transitions = append(transitions, transition{tunnel, tunnel.Status, newStatus})
				tunnel.Status = newStatus
			}
			if len(updates) == 0 {
				continue
			}
			if err = db.Model(&model.VpnTunnel{Model: model.Model{ID: tunnel.ID}}).Updates(updates).Error; err != nil {
				logger.Ctx(ctx).Errorf("Failed to update status of VPN tunnel %d: %v", tunnel.ID, err)
				return
			}
		}
		newStatus := services.VpnConnectionAggregateStatus(conn)
		// One tunnel of several lost while the connection still carries traffic: a warning of its own (the
		// connection alarm below covers the case where nothing is left)
		if len(conn.Tunnels) > 1 {
			carrying := newStatus == model.VpnConnectionStatusUp || newStatus == model.VpnConnectionStatusDegraded
			for _, tr := range transitions {
				if tr.from == model.VpnConnectionStatusUp && tr.to == model.VpnConnectionStatusDown && carrying {
					services.NotifyVpnTunnelState(ctx, gateway, conn, tr.tunnel, false, now)
				} else if tr.from == model.VpnConnectionStatusDown && tr.to == model.VpnConnectionStatusUp {
					services.NotifyVpnTunnelState(ctx, gateway, conn, tr.tunnel, true, now)
				}
			}
		}
		if newStatus == conn.Status {
			continue
		}
		// Only real transitions raise or resolve an alarm; the first report after creation does not. A
		// connection is interrupted when no tunnel is up: degraded still carries the traffic
		wasUp := conn.Status == model.VpnConnectionStatusUp || conn.Status == model.VpnConnectionStatusDegraded
		isUp := newStatus == model.VpnConnectionStatusUp || newStatus == model.VpnConnectionStatusDegraded
		if wasUp && newStatus == model.VpnConnectionStatusDown {
			services.NotifyVpnConnectionState(ctx, gateway, conn, false, now)
		} else if isUp && conn.Status == model.VpnConnectionStatusDown {
			services.NotifyVpnConnectionState(ctx, gateway, conn, true, now)
		}
		if newStatus != conn.Status {
			logger.Ctx(ctx).Infof("VPN connection %d of gateway %d: %s -> %s", conn.ID, gateway.ID, conn.Status, newStatus)
		}
		if err = db.Model(&model.VpnConnection{Model: model.Model{ID: conn.ID}}).Update("status", newStatus).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to update status of VPN connection %d: %v", conn.ID, err)
			return
		}
	}
	// active_active: the other nodes follow the node whose tunnel is up
	if services.VpnIsActiveActive(gateway) {
		if rerr := services.VpnRerouteIfMoved(ctx, gateway.ID, before); rerr != nil {
			logger.Ctx(ctx).Errorf("Failed to reroute VPN gateway %d: %v", gateway.ID, rerr)
		}
	}
	return
}

type vpnClientReport struct {
	PublicKey     string `json:"public_key"`
	LastHandshake int64  `json:"last_handshake"`
	BytesIn       int64  `json:"bytes_in"`
	BytesOut      int64  `json:"bytes_out"`
}

// VpnClientStatus stores the WireGuard peer counters
// |:-COMMAND-:| vpn_client_status.sh '<gateway ID>' '<hostid>' '<base64 json array>'
func VpnClientStatus(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	gateway, _, _, err := vpnLoadForReport(ctx, args, true)
	if err != nil {
		logger.Ctx(ctx).Warning(err)
		err = nil
		return
	}
	reports := []*vpnClientReport{}
	if err = vpnDecodePayload(args[3], &reports); err != nil {
		logger.Ctx(ctx).Error("Invalid client status payload", err)
		err = nil
		return
	}
	byKey := map[string]*vpnClientReport{}
	for _, r := range reports {
		byKey[r.PublicKey] = r
	}
	for _, client := range gateway.Clients {
		r, ok := byKey[client.PublicKey]
		if !ok {
			continue
		}
		updates := map[string]interface{}{"bytes_in": r.BytesIn, "bytes_out": r.BytesOut}
		if r.LastHandshake > 0 {
			updates["last_handshake_at"] = time.Unix(r.LastHandshake, 0)
		}
		if err = db.Model(&model.VpnClient{Model: model.Model{ID: client.ID}}).Updates(updates).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to update status of VPN client %d: %v", client.ID, err)
			return
		}
	}
	return
}

// VpnBgpStatus stores the BGP neighbor state and prefix lists as a display-only snapshot
// |:-COMMAND-:| vpn_bgp_status.sh '<gateway ID>' '<hostid>' '<base64 json array>'
func VpnBgpStatus(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	gateway, hostid, tunnelHosts, err := vpnLoadForReport(ctx, args, false)
	if err != nil {
		logger.Ctx(ctx).Warning(err)
		err = nil
		return
	}
	reports := []*services.VpnBgpReport{}
	if err = vpnDecodePayload(args[3], &reports); err != nil {
		logger.Ctx(ctx).Error("Invalid bgp status payload", err)
		err = nil
		return
	}
	byName := map[string]*services.VpnBgpReport{}
	for _, r := range reports {
		byName[r.Name] = r
	}
	now := time.Now()
	for _, conn := range gateway.Connections {
		if conn.RouteMode != model.VpnRouteModeBgp {
			continue
		}
		for _, tunnel := range conn.Tunnels {
			r, ok := byName[services.VpnTunnelName(tunnel)]
			if !ok || vpnTunnelElsewhere(tunnelHosts, tunnel, hostid) {
				continue
			}
			snapshot, jerr := json.Marshal(r)
			if jerr != nil {
				continue
			}
			// The route mode is checked again by the statement itself: a switch to static committed after the
			// gateway was loaded clears these columns, and this report must not write them back
			if err = db.Model(&model.VpnTunnel{}).Where("id = ? AND vpn_connection_id IN (?)", tunnel.ID,
				db.Model(&model.VpnConnection{}).Select("id").Where("route_mode = ?", model.VpnRouteModeBgp)).
				Updates(map[string]interface{}{
					"bgp_state": r.State, "bgp_status": string(snapshot), "bgp_reported_at": now, "bfd_state": r.Bfd}).Error; err != nil {
				logger.Ctx(ctx).Errorf("Failed to update bgp status of VPN tunnel %d: %v", tunnel.ID, err)
				return
			}
		}
	}
	return
}

// VpnBackup records that a node gave the gateway's floating IP up (keepalived demoted it), which lets
// the other node's master claim through without waiting for the split-brain window
// |:-COMMAND-:| vpn_backup.sh '<gateway ID>' '<hostid>'
func VpnBackup(ctx context.Context, args []string) (status string, err error) {
	gatewayID, hostid, err := vpnCallbackArgs(ctx, args, 3)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	if err = services.VpnGatewayBackup(ctx, gatewayID, hostid); err != nil {
		logger.Ctx(ctx).Errorf("Failed to record backup of VPN gateway %d: %v", gatewayID, err)
		err = nil
	}
	return
}

// NodeLiveness is sent by cland, not by a node: '<hostid>' 'gone' once the node's command stream closed
// or its per-second liveness messages stopped, 'ok' when they are back
// |:-COMMAND-:| node_liveness.sh '<hostid>' '<gone|ok>'
func NodeLiveness(ctx context.Context, args []string) (status string, err error) {
	if len(args) < 3 {
		logger.Ctx(ctx).Error("Invalid node_liveness args", args)
		return
	}
	hostid, perr := strconv.Atoi(args[1])
	if perr != nil || hostid < 0 {
		logger.Ctx(ctx).Errorf("Invalid hypervisor ID in node_liveness: %s", args[1])
		return
	}
	// cland reports a node with the message extra set to that very node (cland/liveness.go), while a line a
	// node printed itself carries the printing node there, which the node cannot choose. Without this check
	// any compute node could print node_liveness for another one and have it taken for gone.
	if extra, ok := ctx.Value("hostid").(int32); !ok || extra != int32(hostid) {
		logger.Ctx(ctx).Warningf("Ignoring node_liveness about node %d that came from node %v", hostid, ctx.Value("hostid"))
		return
	}
	logger.Ctx(ctx).Infof("cland reports node %d as %s", hostid, args[2])
	if err = services.VpnNodeLiveness(ctx, int32(hostid), args[2]); err != nil {
		logger.Ctx(ctx).Errorf("Failed to apply liveness of node %d: %v", hostid, err)
		err = nil
	}
	return
}

// RecoverVpnGateway rebuilds the gateways of which the node is a VRRP member after it rebooted
// |:-COMMAND-:| recover_vpn_gateway.sh '<hostid>'
func RecoverVpnGateway(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	_ = db
	if len(args) < 2 {
		err = fmt.Errorf("Wrong params")
		return
	}
	hyperID, err := strconv.Atoi(args[1])
	if err != nil || hyperID < 0 {
		logger.Ctx(ctx).Errorf("Invalid hypervisor ID: %s, err: %v", args[1], err)
		return
	}
	recovered, err := services.VpnRecoverNode(ctx, int32(hyperID))
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to recover VPN gateways on hyper %d: %v", hyperID, err)
		return
	}
	status = fmt.Sprintf("Recovered %d VPN gateway(s) on hypervisor %d", recovered, hyperID)
	return
}
