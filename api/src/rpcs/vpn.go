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
	"time"

	. "api/src/common"
	"api/src/model"
	"api/src/services"
)

func init() {
	Add("vpn_gateway", VpnGatewayState)
	Add("vpn_master", VpnMaster)
	Add("vpn_conn_status", VpnConnStatus)
	Add("vpn_client_status", VpnClientStatus)
	Add("vpn_bgp_status", VpnBgpStatus)
	Add("recover_vpn_gateway", RecoverVpnGateway)
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

func vpnLoadForReport(ctx context.Context, args []string) (gateway *model.VpnGateway, hostid int32, err error) {
	gatewayID, hostid, err := vpnCallbackArgs(ctx, args, 4)
	if err != nil {
		return
	}
	ctx, db := GetContextDB(ctx)
	gateway = &model.VpnGateway{Model: model.Model{ID: gatewayID}}
	if err = db.Preload("Connections").Preload("Clients").Take(gateway).Error; err != nil {
		return
	}
	// Only the node that currently holds the floating IP may report tunnel state, so a late report from
	// the previous master cannot overwrite the new one
	if gateway.MasterHyper != hostid {
		err = fmt.Errorf("VPN gateway %d status reported by node %d which is not the master (%d)", gatewayID, hostid, gateway.MasterHyper)
	} else if gateway.Disabled {
		// A report sent just before the node learned about the pause: the connections stay "disabled"
		err = fmt.Errorf("VPN gateway %d is disabled, status report of node %d ignored", gatewayID, hostid)
	}
	return
}

type vpnConnReport struct {
	Name          string `json:"name"`
	State         string `json:"state"` // up or down
	EstablishedAt int64  `json:"established_at"`
	BytesIn       int64  `json:"bytes_in"`
	BytesOut      int64  `json:"bytes_out"`
	Error         string `json:"error"`
}

// VpnConnStatus stores the IKE SA state of every connection
// |:-COMMAND-:| vpn_conn_status.sh '<gateway ID>' '<hostid>' '<base64 json array>'
func VpnConnStatus(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	gateway, _, err := vpnLoadForReport(ctx, args)
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
	for _, conn := range gateway.Connections {
		r, ok := byName[fmt.Sprintf("c%d", conn.ID)]
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
			if r.Error != "" || conn.LastError != "" {
				updates["last_error"] = r.Error
			}
		}
		if newStatus != conn.Status {
			updates["status"] = newStatus
			// Only real transitions raise or resolve an alarm; the first report after creation does not
			if newStatus == model.VpnConnectionStatusDown && conn.Status == model.VpnConnectionStatusUp {
				services.NotifyVpnConnectionState(ctx, gateway, conn, false, now)
			} else if newStatus == model.VpnConnectionStatusUp && conn.Status == model.VpnConnectionStatusDown {
				services.NotifyVpnConnectionState(ctx, gateway, conn, true, now)
			}
		}
		if len(updates) == 0 {
			continue
		}
		if err = db.Model(&model.VpnConnection{Model: model.Model{ID: conn.ID}}).Updates(updates).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to update status of VPN connection %d: %v", conn.ID, err)
			return
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
	gateway, _, err := vpnLoadForReport(ctx, args)
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
	gateway, _, err := vpnLoadForReport(ctx, args)
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
		r, ok := byName[fmt.Sprintf("c%d", conn.ID)]
		if !ok {
			continue
		}
		snapshot, jerr := json.Marshal(r)
		if jerr != nil {
			continue
		}
		if err = db.Model(&model.VpnConnection{Model: model.Model{ID: conn.ID}}).Updates(map[string]interface{}{
			"bgp_state": r.State, "bgp_status": string(snapshot), "bgp_reported_at": now}).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to update bgp status of VPN connection %d: %v", conn.ID, err)
			return
		}
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
