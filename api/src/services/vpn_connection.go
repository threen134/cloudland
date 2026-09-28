/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"net"
	"strings"

	. "api/src/common"
	"api/src/model"
)

var vpnConnectionAdmin = &VpnConnectionAdmin{}

type VpnConnectionAdmin struct{}

const (
	VpnDefaultIkeProposal = "aes256-sha256-modp2048"
	VpnDefaultEspProposal = "aes256-sha256-modp2048"
	VpnDefaultIkeLifetime = 86400
	VpnDefaultEspLifetime = 3600
	VpnDefaultDpdAction   = "restart"
	// An idle tunnel sends a liveness check after this many seconds; together with the IKE retransmission
	// settings of the gateway (strongswan.conf) a dead peer is declared in about 20 s
	VpnDefaultDpdDelay      = 10
	VpnDefaultMaxPrefixes   = 1000
	VpnDefaultBgpKeepalive  = 10
	VpnDefaultBgpHold       = 30
	VpnDefaultAsPrepend     = 2
	VpnDefaultBfdInterval   = 1000
	VpnDefaultBfdMultiplier = 3
	// An active_standby gateway runs every tunnel on its master: two local addresses times two peer
	// addresses at most. An active_active gateway runs one tunnel per node.
	vpnMaxTunnels             = 4
	vpnMaxTunnelsActiveActive = 2
)

// VpnIsActiveActive tells whether both VRRP nodes of the gateway run tunnels (VpnHaModeActiveActive)
func VpnIsActiveActive(gateway *model.VpnGateway) bool {
	return gateway.HaMode == model.VpnHaModeActiveActive
}

// VpnIsEcmp tells whether every tunnel of the connection that is up carries traffic
func VpnIsEcmp(conn *model.VpnConnection) bool {
	return conn.TrafficPolicy == model.VpnTrafficPolicyEcmp
}

// VpnTunnelParams is one tunnel of a connection as given by the API. Endpoint and Priority may be empty:
// the endpoints default to the gateway's tunnel addresses in turn (vip1, vip2 / node1, node2) and the
// first tunnel is the primary (on an active_active gateway, the one on the node carrying fewer primaries).
type VpnTunnelParams struct {
	Endpoint      string
	Priority      string
	RemoteGateway string
	RemoteID      string
	Psk           string
	PskSet        bool
	TunnelLocalIP string
	TunnelPeerIP  string
}

// VpnConnectionParams is the normalized input of create and update. Empty strings mean "not given" and
// take the defaults on create; on update, nil pointers in the API layer are turned into the current
// values before calling.
type VpnConnectionParams struct {
	Name               string
	Description        string
	LocalID            string
	RouteMode          string
	LocalCidrs         string
	RemoteCidrs        string
	RemoteSummaryCidrs string
	MaxPrefixes        int32
	LocalAsn           int64
	PeerAsn            int64
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
	AsPathPrepend      int32
	BfdEnabled         bool
	BfdInterval        int32
	BfdMultiplier      int32
	TrafficPolicy      string
	Tunnels            []*VpnTunnelParams
}

func vpnRequireGatewayReady(gateway *model.VpnGateway) error {
	if gateway.Status != model.VpnGatewayStatusAvailable {
		return NewCLError(ErrVpnGatewayNotReady, "VPN gateway is not available yet", nil)
	}
	return nil
}

// VpnTunnelName is the name of a tunnel on the nodes: the swanctl connection, the status reports, the BGP
// neighbor description. The traffic metrics carry the connection name and the slot separately.
func VpnTunnelName(tunnel *model.VpnTunnel) string {
	return fmt.Sprintf("c%d-t%d", tunnel.VpnConnectionID, tunnel.Slot)
}

// VpnPrimaryTunnel returns the primary tunnel of a connection (the first one when none is marked)
func VpnPrimaryTunnel(conn *model.VpnConnection) *model.VpnTunnel {
	for _, t := range conn.Tunnels {
		if t.Priority == model.VpnTunnelPriorityPrimary {
			return t
		}
	}
	if len(conn.Tunnels) > 0 {
		return conn.Tunnels[0]
	}
	return nil
}

// VpnConnectionAggregateStatus derives the status of a connection from its tunnels: up while the primary
// is up (with ecmp: while every tunnel is), degraded while only some other tunnel is, pending until any
// tunnel reported, down otherwise
func VpnConnectionAggregateStatus(conn *model.VpnConnection) string {
	if len(conn.Tunnels) == 0 {
		return conn.Status
	}
	primary := VpnPrimaryTunnel(conn)
	anyUp, allUp, allPending, allDisabled := false, true, true, true
	for _, t := range conn.Tunnels {
		if t.Status == model.VpnConnectionStatusUp {
			anyUp = true
		} else {
			allUp = false
		}
		if t.Status != model.VpnConnectionStatusPending {
			allPending = false
		}
		if t.Status != model.VpnConnectionStatusDisabled {
			allDisabled = false
		}
	}
	switch {
	case allDisabled:
		return model.VpnConnectionStatusDisabled
	case VpnIsEcmp(conn) && allUp:
		return model.VpnConnectionStatusUp
	case !VpnIsEcmp(conn) && primary != nil && primary.Status == model.VpnConnectionStatusUp:
		return model.VpnConnectionStatusUp
	case anyUp:
		return model.VpnConnectionStatusDegraded
	case allPending:
		return model.VpnConnectionStatusPending
	}
	return model.VpnConnectionStatusDown
}

func (a *VpnConnectionAdmin) Get(ctx context.Context, gateway *model.VpnGateway, uuID string) (conn *model.VpnConnection, err error) {
	for _, c := range gateway.Connections {
		if c.UUID == uuID {
			return c, nil
		}
	}
	return nil, NewCLError(ErrVpnConnectionNotFound, "VPN connection not found", nil)
}

// vpnTunnelEndpoints lists the public addresses tunnels of the gateway can start from, in default order:
// vip1, vip2 on an active_standby gateway, node1, node2 on an active_active one (its vip1 is only for
// the client VPN)
func vpnTunnelEndpoints(gateway *model.VpnGateway) []string {
	fips := vpnEndpointFips(gateway)
	candidates := []string{model.VpnEndpointVip1, model.VpnEndpointVip2}
	if VpnIsActiveActive(gateway) {
		candidates = []string{model.VpnEndpointNode1, model.VpnEndpointNode2}
	}
	endpoints := []string{}
	for _, endpoint := range candidates {
		if fips[endpoint] != nil {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

// vpnLighterNode picks the node of an active_active gateway that is primary for fewer connections (node1
// on a tie), so that the primaries of successive connections alternate and both nodes carry traffic
func vpnLighterNode(gateway *model.VpnGateway, excludeID int64) string {
	load := map[string]int{}
	for _, conn := range gateway.Connections {
		if conn.ID == excludeID || VpnIsEcmp(conn) {
			continue
		}
		if primary := VpnPrimaryTunnel(conn); primary != nil {
			load[primary.Endpoint]++
		}
	}
	if load[model.VpnEndpointNode2] < load[model.VpnEndpointNode1] {
		return model.VpnEndpointNode2
	}
	return model.VpnEndpointNode1
}

// normalizeTunnels fills the endpoint and priority defaults and checks the shape of the tunnel list
func normalizeTunnels(gateway *model.VpnGateway, params *VpnConnectionParams, excludeID int64) error {
	aa := VpnIsActiveActive(gateway)
	maxTunnels := vpnMaxTunnels
	if aa {
		maxTunnels = vpnMaxTunnelsActiveActive
	}
	if len(params.Tunnels) == 0 {
		return NewCLError(ErrInvalidParameter, "A connection needs at least one tunnel", nil)
	}
	if len(params.Tunnels) > maxTunnels {
		return NewCLError(ErrInvalidParameter, fmt.Sprintf("A connection of this gateway has at most %d tunnels", maxTunnels), nil)
	}
	if params.TrafficPolicy == "" {
		params.TrafficPolicy = model.VpnTrafficPolicyPreferred
	}
	if params.TrafficPolicy != model.VpnTrafficPolicyPreferred && params.TrafficPolicy != model.VpnTrafficPolicyEcmp {
		return NewCLError(ErrInvalidParameter, "traffic_policy must be preferred or ecmp", nil)
	}
	allowed := vpnTunnelEndpoints(gateway)
	if len(allowed) == 0 {
		return NewCLError(ErrInvalidParameter, "The gateway has no public address for tunnels", nil)
	}
	primaries := 0
	for i, t := range params.Tunnels {
		if t.Endpoint == "" {
			t.Endpoint = allowed[i%len(allowed)]
		}
		known := false
		for _, e := range allowed {
			known = known || e == t.Endpoint
		}
		if !known {
			if aa {
				return NewCLError(ErrInvalidParameter, "Tunnels of an active-active gateway start from node1 or node2", nil)
			}
			return NewCLError(ErrInvalidParameter, fmt.Sprintf("The gateway has no public address %s", t.Endpoint), nil)
		}
		if t.Priority != "" && t.Priority != model.VpnTunnelPriorityPrimary && t.Priority != model.VpnTunnelPriorityStandby {
			return NewCLError(ErrInvalidParameter, "priority must be primary or standby", nil)
		}
		if t.Priority == model.VpnTunnelPriorityPrimary {
			primaries++
		}
	}
	seen := map[string]bool{}
	for _, t := range params.Tunnels {
		// A responder-only tunnel has no peer address: it is told apart by the peer identity (validate)
		if t.RemoteGateway == "" {
			continue
		}
		key := t.Endpoint + "|" + t.RemoteGateway
		if seen[key] {
			return NewCLError(ErrInvalidParameter, "Two tunnels need a different public address on at least one side", nil)
		}
		seen[key] = true
	}
	if aa && len(params.Tunnels) == 2 && params.Tunnels[0].Endpoint == params.Tunnels[1].Endpoint {
		return NewCLError(ErrInvalidParameter, "The two tunnels of an active-active connection run on different nodes", nil)
	}
	// With ecmp every tunnel carries traffic: there is no standby
	if params.TrafficPolicy == model.VpnTrafficPolicyEcmp {
		for _, t := range params.Tunnels {
			t.Priority = model.VpnTunnelPriorityPrimary
		}
		return nil
	}
	switch {
	case primaries == 0:
		primary := 0
		if aa && len(params.Tunnels) == 2 {
			want := vpnLighterNode(gateway, excludeID)
			for i, t := range params.Tunnels {
				if t.Endpoint == want {
					primary = i
				}
			}
		}
		for i, t := range params.Tunnels {
			t.Priority = model.VpnTunnelPriorityStandby
			if i == primary {
				t.Priority = model.VpnTunnelPriorityPrimary
			}
		}
	case primaries == 1:
		for _, t := range params.Tunnels {
			if t.Priority == "" {
				t.Priority = model.VpnTunnelPriorityStandby
			}
		}
	default:
		return NewCLError(ErrInvalidParameter, "Exactly one tunnel can be the primary", nil)
	}
	return nil
}

// validate checks the semantic rules of one connection against the gateway and its other connections
func (a *VpnConnectionAdmin) validate(ctx context.Context, gateway *model.VpnGateway, params *VpnConnectionParams, excludeID int64) (err error) {
	if !gateway.IpsecEnabled {
		return NewCLError(ErrInvalidParameter, "Site-to-site is disabled on this gateway", nil)
	}
	if params.RouteMode != model.VpnRouteModeStatic && params.RouteMode != model.VpnRouteModeBgp {
		return NewCLError(ErrInvalidParameter, "route_mode must be static or bgp", nil)
	}
	if err = normalizeTunnels(gateway, params, excludeID); err != nil {
		return
	}
	for _, t := range params.Tunnels {
		if t.RemoteGateway != "" {
			ip := net.ParseIP(t.RemoteGateway)
			if ip == nil || ip.To4() == nil {
				return NewCLError(ErrInvalidParameter, "remote_gateway must be an IPv4 address", nil)
			}
			continue
		}
		if params.Initiator {
			return NewCLError(ErrInvalidParameter, "A responder-only tunnel (no remote_gateway) cannot be initiated by the gateway", nil)
		}
		if strings.TrimSpace(t.RemoteID) == "" {
			return NewCLError(ErrInvalidParameter, "remote_id is required when remote_gateway is empty", nil)
		}
	}
	// Tunnels of the other connections: a responder-only tunnel is found by (local address, peer identity)
	others := []*model.VpnTunnel{}
	for _, other := range gateway.Connections {
		if other.ID == excludeID {
			continue
		}
		if other.Name == params.Name {
			return NewCLError(ErrInvalidParameter, "A connection with this name already exists", nil)
		}
		others = append(others, other.Tunnels...)
	}
	for i, t := range params.Tunnels {
		if t.RemoteGateway != "" {
			continue
		}
		for _, o := range others {
			if o.RemoteGateway == "" && o.Endpoint == t.Endpoint && o.RemoteID == t.RemoteID {
				return NewCLError(ErrInvalidParameter, "remote_id must be unique among responder-only tunnels of the same public address", nil)
			}
		}
		for _, prev := range params.Tunnels[:i] {
			if prev.RemoteGateway == "" && prev.Endpoint == t.Endpoint && prev.RemoteID == t.RemoteID {
				return NewCLError(ErrInvalidParameter, "Responder-only tunnels of the same public address need different identities", nil)
			}
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
	for i, t := range params.Tunnels {
		if err = validateTunnelLink(ctx, gateway.RouterID, t.TunnelLocalIP, t.TunnelPeerIP); err != nil {
			return
		}
		for _, o := range others {
			if o.TunnelLocalIP != "" && (o.TunnelLocalIP == t.TunnelLocalIP || o.TunnelPeerIP == t.TunnelPeerIP) {
				return NewCLError(ErrInvalidParameter, "Tunnel link addresses are already used by another connection", nil)
			}
		}
		for _, prev := range params.Tunnels[:i] {
			if prev.TunnelLocalIP == t.TunnelLocalIP || prev.TunnelPeerIP == t.TunnelPeerIP || cidrsOverlap(prev.TunnelLocalIP+"/30", t.TunnelLocalIP+"/30") {
				return NewCLError(ErrInvalidParameter, "The tunnels of a connection need different link networks", nil)
			}
		}
	}
	for _, other := range gateway.Connections {
		if other.ID == excludeID || other.RouteMode != model.VpnRouteModeBgp {
			continue
		}
		if other.LocalAsn != params.LocalAsn {
			return NewCLError(ErrInvalidParameter, "All BGP connections of a gateway must use the same local_asn", nil)
		}
	}
	if params.BgpHold < 3 || params.BgpKeepalive < 1 || params.BgpKeepalive*3 > params.BgpHold {
		return NewCLError(ErrInvalidParameter, "bgp_hold must be at least 3 times bgp_keepalive", nil)
	}
	if params.MaxPrefixes < 1 || params.MaxPrefixes > 100000 {
		return NewCLError(ErrInvalidParameter, "max_prefixes must be 1-100000", nil)
	}
	if params.AsPathPrepend < 0 || params.AsPathPrepend > 10 {
		return NewCLError(ErrInvalidParameter, "as_path_prepend must be 0-10", nil)
	}
	if params.BfdInterval < 300 || params.BfdInterval > 60000 {
		return NewCLError(ErrInvalidParameter, "bfd_interval must be 300-60000 milliseconds", nil)
	}
	if params.BfdMultiplier < 2 || params.BfdMultiplier > 50 {
		return NewCLError(ErrInvalidParameter, "bfd_multiplier must be 2-50", nil)
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
	if params.BfdInterval <= 0 {
		params.BfdInterval = VpnDefaultBfdInterval
	}
	if params.BfdMultiplier <= 0 {
		params.BfdMultiplier = VpnDefaultBfdMultiplier
	}
	if params.RouteMode == "" {
		params.RouteMode = model.VpnRouteModeStatic
	}
	if params.TrafficPolicy == "" {
		params.TrafficPolicy = model.VpnTrafficPolicyPreferred
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

// tunnelColumns is the stored form of one tunnel of params; psk is encrypted, empty when not given
func tunnelColumns(params *VpnConnectionParams, t *VpnTunnelParams) (cols map[string]interface{}, err error) {
	cols = map[string]interface{}{
		"endpoint": t.Endpoint, "priority": t.Priority, "remote_gateway": t.RemoteGateway, "remote_id": t.RemoteID,
		"tunnel_local_ip": t.TunnelLocalIP, "tunnel_peer_ip": t.TunnelPeerIP,
	}
	if params.RouteMode == model.VpnRouteModeStatic {
		cols["tunnel_local_ip"], cols["tunnel_peer_ip"] = "", ""
	}
	if t.PskSet {
		psk := ""
		if strings.TrimSpace(t.Psk) != "" {
			if psk, err = EncryptSecret(t.Psk); err != nil {
				return
			}
		}
		cols["psk"] = psk
	}
	return
}

// createTunnel stores one tunnel; its interface id is its own id, unique for ever
func createTunnel(ctx context.Context, conn *model.VpnConnection, slot int32, params *VpnConnectionParams, t *VpnTunnelParams, status string) (tunnel *model.VpnTunnel, err error) {
	ctx, db := GetContextDB(ctx)
	cols, err := tunnelColumns(params, t)
	if err != nil {
		return
	}
	psk, _ := cols["psk"].(string)
	tunnel = &model.VpnTunnel{
		Model: model.Model{Creater: conn.Creater}, Owner: conn.Owner, VpnGatewayID: conn.VpnGatewayID, VpnConnectionID: conn.ID, Slot: slot,
		Endpoint: t.Endpoint, Priority: t.Priority, RemoteGateway: t.RemoteGateway, RemoteID: t.RemoteID, Psk: psk,
		TunnelLocalIP: cols["tunnel_local_ip"].(string), TunnelPeerIP: cols["tunnel_peer_ip"].(string), Status: status,
	}
	if err = db.Create(tunnel).Error; err != nil {
		err = NewCLError(ErrVpnConnectionCreateFailed, "Failed to create VPN tunnel", err)
		return
	}
	if err = db.Model(tunnel).Update("if_id", int32(tunnel.ID)).Error; err != nil {
		err = NewCLError(ErrVpnConnectionCreateFailed, "Failed to assign the interface id", err)
		return
	}
	tunnel.IfID = int32(tunnel.ID)
	return
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
		VpnGatewayID: gateway.ID, Status: initialStatus, LocalID: params.LocalID, RouteMode: params.RouteMode,
		LocalCidrs: localCidrs, RemoteCidrs: remoteCidrs, RemoteSummaryCidrs: summary,
		MaxPrefixes: params.MaxPrefixes, LocalAsn: params.LocalAsn, PeerAsn: params.PeerAsn, BgpPassword: bgpPassword,
		BgpKeepalive: params.BgpKeepalive, BgpHold: params.BgpHold, AuthMethod: "psk", Psk: psk, IkeVersion: 2,
		IkeProposal: params.IkeProposal, EspProposal: params.EspProposal, IkeLifetime: params.IkeLifetime, EspLifetime: params.EspLifetime,
		DpdAction: params.DpdAction, DpdDelay: params.DpdDelay, Initiator: params.Initiator,
		AsPathPrepend: params.AsPathPrepend, BfdEnabled: params.BfdEnabled, BfdInterval: params.BfdInterval, BfdMultiplier: params.BfdMultiplier,
		TrafficPolicy: params.TrafficPolicy,
	}
	if params.RouteMode == model.VpnRouteModeStatic {
		conn.LocalAsn, conn.PeerAsn, conn.RemoteSummaryCidrs, conn.BgpPassword, conn.BfdEnabled = 0, 0, "", "", false
	} else {
		conn.RemoteCidrs = ""
	}
	if err = db.Create(conn).Error; err != nil {
		err = NewCLError(ErrVpnConnectionCreateFailed, "Failed to create VPN connection", err)
		return
	}
	for i, t := range params.Tunnels {
		var tunnel *model.VpnTunnel
		if tunnel, err = createTunnel(ctx, conn, int32(i+1), params, t, initialStatus); err != nil {
			return
		}
		conn.Tunnels = append(conn.Tunnels, tunnel)
	}
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
		params.LocalAsn, params.PeerAsn, params.BfdEnabled = 0, 0, false
	} else {
		remoteCidrs = ""
	}
	set("name", conn.Name, params.Name)
	set("description", conn.Description, params.Description)
	set("local_id", conn.LocalID, params.LocalID)
	set("route_mode", conn.RouteMode, params.RouteMode)
	set("local_cidrs", conn.LocalCidrs, localCidrs)
	set("remote_cidrs", conn.RemoteCidrs, remoteCidrs)
	set("remote_summary_cidrs", conn.RemoteSummaryCidrs, summary)
	set("max_prefixes", conn.MaxPrefixes, params.MaxPrefixes)
	set("local_asn", conn.LocalAsn, params.LocalAsn)
	set("peer_asn", conn.PeerAsn, params.PeerAsn)
	set("bgp_keepalive", conn.BgpKeepalive, params.BgpKeepalive)
	set("bgp_hold", conn.BgpHold, params.BgpHold)
	set("ike_proposal", conn.IkeProposal, params.IkeProposal)
	set("esp_proposal", conn.EspProposal, params.EspProposal)
	set("ike_lifetime", conn.IkeLifetime, params.IkeLifetime)
	set("esp_lifetime", conn.EspLifetime, params.EspLifetime)
	set("dpd_action", conn.DpdAction, params.DpdAction)
	set("dpd_delay", conn.DpdDelay, params.DpdDelay)
	set("initiator", conn.Initiator, params.Initiator)
	set("as_path_prepend", conn.AsPathPrepend, params.AsPathPrepend)
	set("bfd_enabled", conn.BfdEnabled, params.BfdEnabled)
	set("bfd_interval", conn.BfdInterval, params.BfdInterval)
	set("bfd_multiplier", conn.BfdMultiplier, params.BfdMultiplier)
	if conn.TrafficPolicy != params.TrafficPolicy && !(conn.TrafficPolicy == "" && params.TrafficPolicy == model.VpnTrafficPolicyPreferred) {
		updates["traffic_policy"] = params.TrafficPolicy
	}
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
	changed := len(updates) > 0
	if changed {
		if err = db.Model(&model.VpnConnection{Model: model.Model{ID: conn.ID}}).Updates(updates).Error; err != nil {
			err = NewCLError(ErrVpnConnectionUpdateFailed, "Failed to update VPN connection", err)
			return
		}
	}
	// A connection that left BGP keeps no session: drop the last BGP / BFD snapshot of its tunnels
	if conn.RouteMode == model.VpnRouteModeBgp && params.RouteMode == model.VpnRouteModeStatic {
		if err = db.Model(&model.VpnTunnel{}).Where("vpn_connection_id = ?", conn.ID).Updates(map[string]interface{}{
			"bgp_state": "", "bgp_status": "", "bgp_reported_at": nil, "bfd_state": ""}).Error; err != nil {
			err = NewCLError(ErrVpnConnectionUpdateFailed, "Failed to update VPN tunnels", err)
			return
		}
	}
	// Tunnels by slot: the list position is the slot
	bySlot := map[int32]*model.VpnTunnel{}
	for _, t := range conn.Tunnels {
		bySlot[t.Slot] = t
	}
	status := model.VpnConnectionStatusPending
	if gateway.Disabled {
		status = model.VpnConnectionStatusDisabled
	}
	// Tunnels whose alarm must be resolved once the change is done: the removed ones, and every one when a
	// single tunnel is left of several (the tunnel alarm only exists for connections with several)
	resolved := []*model.VpnTunnel{}
	if len(params.Tunnels) <= 1 && len(conn.Tunnels) > 1 {
		resolved = conn.Tunnels
	}
	for slot := int32(1); slot <= vpnMaxTunnels; slot++ {
		old := bySlot[slot]
		if int(slot) > len(params.Tunnels) {
			if old != nil {
				if err = db.Delete(&model.VpnTunnel{Model: model.Model{ID: old.ID}}).Error; err != nil {
					err = NewCLError(ErrVpnConnectionUpdateFailed, "Failed to delete VPN tunnel", err)
					return
				}
				changed = true
				if len(params.Tunnels) > 1 {
					resolved = append(resolved, old)
				}
			}
			continue
		}
		t := params.Tunnels[slot-1]
		if old == nil {
			if _, err = createTunnel(ctx, conn, slot, params, t, status); err != nil {
				return
			}
			changed = true
			continue
		}
		var cols map[string]interface{}
		if cols, err = tunnelColumns(params, t); err != nil {
			return
		}
		tunnelUpdates := map[string]interface{}{}
		current := map[string]interface{}{
			"endpoint": old.Endpoint, "priority": old.Priority, "remote_gateway": old.RemoteGateway, "remote_id": old.RemoteID,
			"tunnel_local_ip": old.TunnelLocalIP, "tunnel_peer_ip": old.TunnelPeerIP, "psk": old.Psk,
		}
		for col, val := range cols {
			if col == "psk" || current[col] != val {
				tunnelUpdates[col] = val
			}
		}
		if len(tunnelUpdates) > 0 {
			if err = db.Model(&model.VpnTunnel{Model: model.Model{ID: old.ID}}).Updates(tunnelUpdates).Error; err != nil {
				err = NewCLError(ErrVpnConnectionUpdateFailed, "Failed to update VPN tunnel", err)
				return
			}
			changed = true
		}
	}
	if changed {
		if err = a.dispatchAfterChange(ctx, gateway.ID); err != nil {
			return
		}
	}
	resolveVpnAlarmsOf(ctx, gateway, conn, resolved, false)
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
	if err = db.Where("vpn_connection_id = ?", conn.ID).Delete(&model.VpnTunnel{}).Error; err != nil {
		err = NewCLError(ErrVpnConnectionUpdateFailed, "Failed to delete VPN tunnels", err)
		return
	}
	if err = softDeleteRenamed(ctx, &model.VpnConnection{}, conn.ID, conn.Name, conn.CreatedAt); err != nil {
		return
	}
	if gateway.Status == model.VpnGatewayStatusAvailable {
		if err = a.dispatchAfterChange(ctx, gateway.ID); err != nil {
			return
		}
	}
	resolveVpnAlarmsOf(ctx, gateway, conn, conn.Tunnels, true)
	return
}

// Restart tears the IKE SA of every tunnel of a connection down, or only the one in slot, and initiates
// them again on the node holding the floating IP
func (a *VpnConnectionAdmin) Restart(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, slot int32) (err error) {
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
	names := []string{}
	for _, t := range conn.Tunnels {
		if slot == 0 || t.Slot == slot {
			names = append(names, VpnTunnelName(t))
		}
	}
	if len(names) == 0 {
		return NewCLError(ErrInvalidParameter, "The connection has no such tunnel", nil)
	}
	return dispatchVpnRestart(ctx, gateway, conn, names)
}
