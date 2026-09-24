/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"net"
	"strings"

	. "api/src/common"
	"api/src/model"
)

var vpnConnectionAdmin = &VpnConnectionAdmin{}

type VpnConnectionAdmin struct{}

const (
	VpnDefaultIkeProposal  = "aes256-sha256-modp2048"
	VpnDefaultEspProposal  = "aes256-sha256-modp2048"
	VpnDefaultIkeLifetime  = 86400
	VpnDefaultEspLifetime  = 3600
	VpnDefaultDpdAction    = "restart"
	VpnDefaultDpdDelay     = 30
	VpnDefaultMaxPrefixes  = 1000
	VpnDefaultBgpKeepalive = 10
	VpnDefaultBgpHold      = 30
)

// VpnConnectionParams is the normalized input of create and update. Empty strings mean "not given" and
// take the defaults on create; on update, nil pointers in the API layer are turned into the current
// values before calling.
type VpnConnectionParams struct {
	Name               string
	Description        string
	RemoteGateway      string
	RemoteID           string
	LocalID            string
	RouteMode          string
	LocalCidrs         string
	RemoteCidrs        string
	RemoteSummaryCidrs string
	MaxPrefixes        int32
	LocalAsn           int64
	PeerAsn            int64
	TunnelLocalIP      string
	TunnelPeerIP       string
	BgpPassword        string
	BgpPasswordSet     bool
	BgpKeepalive       int32
	BgpHold            int32
	Psk                string
	PskSet             bool
	IkeProposal        string
	EspProposal        string
	IkeLifetime        int32
	EspLifetime        int32
	DpdAction          string
	DpdDelay           int32
	Initiator          bool
}

func vpnRequireGatewayReady(gateway *model.VpnGateway) error {
	if gateway.Status != model.VpnGatewayStatusAvailable {
		return NewCLError(ErrVpnGatewayNotReady, "VPN gateway is not available yet", nil)
	}
	return nil
}

func (a *VpnConnectionAdmin) Get(ctx context.Context, gateway *model.VpnGateway, uuID string) (conn *model.VpnConnection, err error) {
	for _, c := range gateway.Connections {
		if c.UUID == uuID {
			return c, nil
		}
	}
	return nil, NewCLError(ErrVpnConnectionNotFound, "VPN connection not found", nil)
}

// validate checks the semantic rules of one connection against the gateway and its other connections
func (a *VpnConnectionAdmin) validate(ctx context.Context, gateway *model.VpnGateway, params *VpnConnectionParams, excludeID int64) (err error) {
	if !gateway.IpsecEnabled {
		return NewCLError(ErrInvalidParameter, "Site-to-site is disabled on this gateway", nil)
	}
	if params.RouteMode != model.VpnRouteModeStatic && params.RouteMode != model.VpnRouteModeBgp {
		return NewCLError(ErrInvalidParameter, "route_mode must be static or bgp", nil)
	}
	if params.RemoteGateway != "" {
		ip := net.ParseIP(params.RemoteGateway)
		if ip == nil || ip.To4() == nil {
			return NewCLError(ErrInvalidParameter, "remote_gateway must be an IPv4 address", nil)
		}
	} else {
		if params.Initiator {
			return NewCLError(ErrInvalidParameter, "A responder-only connection (no remote_gateway) cannot be the initiator", nil)
		}
		if strings.TrimSpace(params.RemoteID) == "" {
			return NewCLError(ErrInvalidParameter, "remote_id is required when remote_gateway is empty", nil)
		}
	}
	for _, other := range gateway.Connections {
		if other.ID == excludeID {
			continue
		}
		if other.Name == params.Name {
			return NewCLError(ErrInvalidParameter, "A connection with this name already exists", nil)
		}
		if params.RemoteGateway == "" && other.RemoteGateway == "" && other.RemoteID == params.RemoteID {
			return NewCLError(ErrInvalidParameter, "remote_id must be unique among responder-only connections", nil)
		}
	}
	if params.LocalCidrs != "" {
		if _, err = ParseCidrList(params.LocalCidrs); err != nil {
			return
		}
	} else {
		var vpc []string
		if vpc, err = vpcInternalCidrs(ctx, gateway.RouterID); err != nil {
			return
		}
		if len(vpc) == 0 {
			return NewCLError(ErrInvalidParameter, "The VPC has no subnet yet; local networks would be empty", nil)
		}
	}
	if params.RouteMode == model.VpnRouteModeStatic {
		cidrs, perr := ParseCidrList(params.RemoteCidrs)
		if perr != nil {
			return perr
		}
		if len(cidrs) == 0 {
			return NewCLError(ErrInvalidParameter, "remote_cidrs is required in static mode", nil)
		}
		return validateVpnPrefixes(ctx, gateway.RouterID, gateway.ID, cidrs, excludeID, model.VpnPrefixSourceConnectionStatic, model.VpnPrefixSourceConnectionSummary)
	}
	// BGP mode
	cidrs, perr := ParseCidrList(params.RemoteSummaryCidrs)
	if perr != nil {
		return perr
	}
	if len(cidrs) == 0 {
		return NewCLError(ErrInvalidParameter, "remote_summary_cidrs is required in bgp mode", nil)
	}
	if err = validateVpnPrefixes(ctx, gateway.RouterID, gateway.ID, cidrs, excludeID, model.VpnPrefixSourceConnectionStatic, model.VpnPrefixSourceConnectionSummary); err != nil {
		return
	}
	if params.LocalAsn < 1 || params.LocalAsn > 4294967295 || params.PeerAsn < 1 || params.PeerAsn > 4294967295 {
		return NewCLError(ErrInvalidParameter, "local_asn and peer_asn are required in bgp mode", nil)
	}
	if params.LocalAsn == params.PeerAsn {
		return NewCLError(ErrInvalidParameter, "local_asn and peer_asn must differ (eBGP)", nil)
	}
	if err = validateTunnelLink(ctx, gateway.RouterID, params.TunnelLocalIP, params.TunnelPeerIP); err != nil {
		return
	}
	for _, other := range gateway.Connections {
		if other.ID == excludeID || other.RouteMode != model.VpnRouteModeBgp {
			continue
		}
		if other.LocalAsn != params.LocalAsn {
			return NewCLError(ErrInvalidParameter, "All BGP connections of a gateway must use the same local_asn", nil)
		}
		if other.TunnelLocalIP == params.TunnelLocalIP || other.TunnelPeerIP == params.TunnelPeerIP {
			return NewCLError(ErrInvalidParameter, "Tunnel link addresses are already used by another connection", nil)
		}
	}
	if params.BgpHold < 3 || params.BgpKeepalive < 1 || params.BgpKeepalive*3 > params.BgpHold {
		return NewCLError(ErrInvalidParameter, "bgp_hold must be at least 3 times bgp_keepalive", nil)
	}
	if params.MaxPrefixes < 1 || params.MaxPrefixes > 100000 {
		return NewCLError(ErrInvalidParameter, "max_prefixes must be 1-100000", nil)
	}
	return
}

func applyVpnConnectionDefaults(params *VpnConnectionParams) {
	if params.IkeProposal == "" {
		params.IkeProposal = VpnDefaultIkeProposal
	}
	if params.EspProposal == "" {
		params.EspProposal = VpnDefaultEspProposal
	}
	if params.IkeLifetime <= 0 {
		params.IkeLifetime = VpnDefaultIkeLifetime
	}
	if params.EspLifetime <= 0 {
		params.EspLifetime = VpnDefaultEspLifetime
	}
	if params.DpdAction == "" {
		params.DpdAction = VpnDefaultDpdAction
	}
	if params.DpdDelay <= 0 {
		params.DpdDelay = VpnDefaultDpdDelay
	}
	if params.MaxPrefixes <= 0 {
		params.MaxPrefixes = VpnDefaultMaxPrefixes
	}
	if params.BgpKeepalive <= 0 {
		params.BgpKeepalive = VpnDefaultBgpKeepalive
	}
	if params.BgpHold <= 0 {
		params.BgpHold = VpnDefaultBgpHold
	}
	if params.RouteMode == "" {
		params.RouteMode = model.VpnRouteModeStatic
	}
	// remote_id stays empty unless the user set one: the dispatcher derives it from remote_gateway every
	// time, so a later change of the gateway address is not stuck with the old identity
}

func normalizeCidrField(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	cidrs, err := ParseCidrList(value)
	if err != nil {
		return "", err
	}
	return joinCidrs(cidrs), nil
}

// dispatchAfterChange re-pushes everything a connection change touches: the derived prefixes, the
// swanctl config, the FRR config and the routes on every node
func (a *VpnConnectionAdmin) dispatchAfterChange(ctx context.Context, gatewayID int64) (err error) {
	gateway, err := loadVpnGateway(ctx, gatewayID)
	if err != nil {
		return
	}
	if _, err = rebuildRemotePrefixes(ctx, gateway); err != nil {
		return
	}
	if err = dispatchVpnIpsec(ctx, gateway, -1); err != nil {
		return
	}
	if err = dispatchVpnBgp(ctx, gateway, -1); err != nil {
		return
	}
	return dispatchVpnRoutes(ctx, gateway, -1)
}

func (a *VpnConnectionAdmin) Create(ctx context.Context, gateway *model.VpnGateway, params *VpnConnectionParams) (conn *model.VpnConnection, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to change the VPN gateway", nil)
		return
	}
	if err = vpnRequireGatewayReady(gateway); err != nil {
		return
	}
	if err = SecretStoreReady(); err != nil {
		return
	}
	applyVpnConnectionDefaults(params)
	if strings.TrimSpace(params.Psk) == "" {
		err = NewCLError(ErrInvalidParameter, "psk is required", nil)
		return
	}
	if err = a.validate(ctx, gateway, params, 0); err != nil {
		return
	}
	psk, err := EncryptSecret(params.Psk)
	if err != nil {
		return
	}
	bgpPassword, err := EncryptSecret(params.BgpPassword)
	if err != nil {
		return
	}
	localCidrs, err := normalizeCidrField(params.LocalCidrs)
	if err != nil {
		return
	}
	remoteCidrs, err := normalizeCidrField(params.RemoteCidrs)
	if err != nil {
		return
	}
	summary, err := normalizeCidrField(params.RemoteSummaryCidrs)
	if err != nil {
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	initialStatus := model.VpnConnectionStatusPending
	if gateway.Disabled {
		initialStatus = model.VpnConnectionStatusDisabled
	}
	conn = &model.VpnConnection{
		Model: model.Model{Creater: memberShip.UserID}, Owner: gateway.Owner, Name: params.Name, Description: params.Description,
		VpnGatewayID: gateway.ID, Status: initialStatus,
		RemoteGateway: params.RemoteGateway, RemoteID: params.RemoteID, LocalID: params.LocalID, RouteMode: params.RouteMode,
		LocalCidrs: localCidrs, RemoteCidrs: remoteCidrs, RemoteSummaryCidrs: summary,
		MaxPrefixes: params.MaxPrefixes, LocalAsn: params.LocalAsn, PeerAsn: params.PeerAsn,
		TunnelLocalIP: params.TunnelLocalIP, TunnelPeerIP: params.TunnelPeerIP, BgpPassword: bgpPassword,
		BgpKeepalive: params.BgpKeepalive, BgpHold: params.BgpHold, AuthMethod: "psk", Psk: psk, IkeVersion: 2,
		IkeProposal: params.IkeProposal, EspProposal: params.EspProposal, IkeLifetime: params.IkeLifetime, EspLifetime: params.EspLifetime,
		DpdAction: params.DpdAction, DpdDelay: params.DpdDelay, Initiator: params.Initiator,
	}
	if params.RouteMode == model.VpnRouteModeStatic {
		conn.LocalAsn, conn.PeerAsn, conn.TunnelLocalIP, conn.TunnelPeerIP, conn.RemoteSummaryCidrs, conn.BgpPassword = 0, 0, "", "", "", ""
	} else {
		conn.RemoteCidrs = ""
	}
	if err = db.Create(conn).Error; err != nil {
		err = NewCLError(ErrVpnConnectionCreateFailed, "Failed to create VPN connection", err)
		return
	}
	// The XFRM interface id is the connection id: unique for ever, no allocation needed
	if err = db.Model(conn).Update("if_id", int32(conn.ID)).Error; err != nil {
		err = NewCLError(ErrVpnConnectionCreateFailed, "Failed to assign the interface id", err)
		return
	}
	conn.IfID = int32(conn.ID)
	if err = a.dispatchAfterChange(ctx, gateway.ID); err != nil {
		return
	}
	return
}

func (a *VpnConnectionAdmin) Update(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, params *VpnConnectionParams) (updated *model.VpnConnection, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to change the VPN gateway", nil)
		return
	}
	if err = vpnRequireGatewayReady(gateway); err != nil {
		return
	}
	applyVpnConnectionDefaults(params)
	if err = a.validate(ctx, gateway, params, conn.ID); err != nil {
		return
	}
	updates := map[string]interface{}{}
	set := func(col string, old, val interface{}) {
		if old != val {
			updates[col] = val
		}
	}
	localCidrs, err := normalizeCidrField(params.LocalCidrs)
	if err != nil {
		return
	}
	remoteCidrs, err := normalizeCidrField(params.RemoteCidrs)
	if err != nil {
		return
	}
	summary, err := normalizeCidrField(params.RemoteSummaryCidrs)
	if err != nil {
		return
	}
	if params.RouteMode == model.VpnRouteModeStatic {
		summary = ""
		params.LocalAsn, params.PeerAsn, params.TunnelLocalIP, params.TunnelPeerIP = 0, 0, "", ""
	} else {
		remoteCidrs = ""
	}
	set("name", conn.Name, params.Name)
	set("description", conn.Description, params.Description)
	set("remote_gateway", conn.RemoteGateway, params.RemoteGateway)
	set("remote_id", conn.RemoteID, params.RemoteID)
	set("local_id", conn.LocalID, params.LocalID)
	set("route_mode", conn.RouteMode, params.RouteMode)
	set("local_cidrs", conn.LocalCidrs, localCidrs)
	set("remote_cidrs", conn.RemoteCidrs, remoteCidrs)
	set("remote_summary_cidrs", conn.RemoteSummaryCidrs, summary)
	set("max_prefixes", conn.MaxPrefixes, params.MaxPrefixes)
	set("local_asn", conn.LocalAsn, params.LocalAsn)
	set("peer_asn", conn.PeerAsn, params.PeerAsn)
	set("tunnel_local_ip", conn.TunnelLocalIP, params.TunnelLocalIP)
	set("tunnel_peer_ip", conn.TunnelPeerIP, params.TunnelPeerIP)
	set("bgp_keepalive", conn.BgpKeepalive, params.BgpKeepalive)
	set("bgp_hold", conn.BgpHold, params.BgpHold)
	set("ike_proposal", conn.IkeProposal, params.IkeProposal)
	set("esp_proposal", conn.EspProposal, params.EspProposal)
	set("ike_lifetime", conn.IkeLifetime, params.IkeLifetime)
	set("esp_lifetime", conn.EspLifetime, params.EspLifetime)
	set("dpd_action", conn.DpdAction, params.DpdAction)
	set("dpd_delay", conn.DpdDelay, params.DpdDelay)
	set("initiator", conn.Initiator, params.Initiator)
	if params.PskSet {
		if strings.TrimSpace(params.Psk) == "" {
			err = NewCLError(ErrInvalidParameter, "psk cannot be empty", nil)
			return
		}
		if updates["psk"], err = EncryptSecret(params.Psk); err != nil {
			return
		}
	}
	if params.BgpPasswordSet {
		if updates["bgp_password"], err = EncryptSecret(params.BgpPassword); err != nil {
			return
		}
	} else if params.RouteMode == model.VpnRouteModeStatic && conn.BgpPassword != "" {
		updates["bgp_password"] = ""
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if len(updates) > 0 {
		if err = db.Model(&model.VpnConnection{Model: model.Model{ID: conn.ID}}).Updates(updates).Error; err != nil {
			err = NewCLError(ErrVpnConnectionUpdateFailed, "Failed to update VPN connection", err)
			return
		}
		if err = a.dispatchAfterChange(ctx, gateway.ID); err != nil {
			return
		}
	}
	if gateway, err = loadVpnGateway(ctx, gateway.ID); err != nil {
		return
	}
	updated, err = a.Get(ctx, gateway, conn.UUID)
	return
}

func (a *VpnConnectionAdmin) Delete(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to change the VPN gateway", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	_ = db
	if err = softDeleteRenamed(ctx, &model.VpnConnection{}, conn.ID, conn.Name, conn.CreatedAt); err != nil {
		return
	}
	if gateway.Status == model.VpnGatewayStatusAvailable {
		err = a.dispatchAfterChange(ctx, gateway.ID)
	}
	return
}

// Restart tears the IKE SA down and re-initiates it on the node holding the floating IP
func (a *VpnConnectionAdmin) Restart(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the VPN gateway", nil)
	}
	if err = vpnRequireGatewayReady(gateway); err != nil {
		return
	}
	if gateway.Disabled {
		return NewCLError(ErrVpnGatewayDisabled, "VPN gateway is disabled", nil)
	}
	return dispatchVpnRestart(ctx, gateway, conn)
}
