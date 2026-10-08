/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"golang.org/x/crypto/curve25519"
	"gorm.io/gorm"
)

var (
	vpnGatewayAdmin = &VpnGatewayAdmin{}
)

// VPN gateway: a VRRP pair in the VPC router netns, built like a load balancer (CreateVrrpInstance,
// keepalived holding the public floating IP) with strongSwan, WireGuard and FRR on top. See
// docs/architecture/plan/vpn-gateway-plan.md.
type VpnGatewayAdmin struct{}

const (
	VpnDefaultClientPort = 51820
	// A node holding the floating IP reports itself every 20 s (report_vpn_status.sh). Two nodes reporting
	// within this window is a split brain; a real failover is accepted once the old master has been silent
	// for the window, so the worst-case delay for re-pointing the other nodes is about the window itself.
	vpnMasterFlapWindow = 30 * time.Second
)

type VpnGatewayParams struct {
	Name          string
	Description   string
	IpsecEnabled  bool
	ClientEnabled bool
	ClientCidr    string
	ClientPort    int32
	ClientDns     string
	ClientRoutes  string
	// VpnHaModeActiveStandby (default) or VpnHaModeActiveActive
	HaMode string
	// active_standby: one or two floating IPs (vip1, vip2), both held by keepalived on the master; the second
	// lets a connection run two tunnels towards a single peer address (the peer sees two gateways).
	// active_active: the fixed address of each node (node1, node2), then the client VPN floating IP (vip1)
	// when client access is enabled; missing entries are allocated from the subnets of the first one.
	PublicIps []*VpnPublicIpParams
	Inbound   int32
	Outbound  int32
}

// VpnPublicIpParams picks one public address: from these subnets (any public subnet when empty), the
// given address when not empty
type VpnPublicIpParams struct {
	Subnets []*model.Subnet
	Address string
}

// VpnGatewayPatch carries the updatable fields; nil means unchanged
type VpnGatewayPatch struct {
	Name          *string
	Description   *string
	IpsecEnabled  *bool
	ClientEnabled *bool
	ClientCidr    *string
	ClientPort    *int32
	ClientDns     *string
	ClientRoutes  *string
	Enabled       *bool
}

func generateWgKeyPair() (privateKey, publicKey string, err error) {
	priv := make([]byte, 32)
	if _, err = rand.Read(priv); err != nil {
		return
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return
	}
	return base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(pub), nil
}

func generateWgPresharedKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func vpnPreload(db *gorm.DB) *gorm.DB {
	return db.Preload("Router").Preload("VrrpInstance").Preload("VrrpInstance.VrrpSubnet").
		Preload("FloatingIps").Preload("FloatingIps.Subnet").
		Preload("Connections", dbs.OrderByID).Preload("Connections.Tunnels", func(db *gorm.DB) *gorm.DB { return db.Order("slot") }).
		Preload("Clients", dbs.OrderByID)
}

// loadVpnGateway reloads a gateway with everything the dispatch code needs, without an org filter
// (used from callbacks that carry no membership)
func loadVpnGateway(ctx context.Context, id int64) (gateway *model.VpnGateway, err error) {
	ctx, db := GetContextDB(ctx)
	gateway = &model.VpnGateway{Model: model.Model{ID: id}}
	if err = vpnPreload(db).Take(gateway).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to load VPN gateway %d: %v", id, err)
		err = NewCLError(ErrVpnGatewayNotFound, "VPN gateway not found", err)
	}
	return
}

// vpnGatewayOfRouter returns the live gateway of a VPC, or nil when it has none
func vpnGatewayOfRouter(ctx context.Context, routerID int64) (gateway *model.VpnGateway, err error) {
	ctx, db := GetContextDB(ctx)
	gateway = &model.VpnGateway{}
	err = db.Where("router_id = ?", routerID).Take(gateway).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		logger.Ctx(ctx).Errorf("Failed to query VPN gateway of router %d: %v", routerID, err)
		return nil, NewCLError(ErrDatabaseError, "Failed to query VPN gateway", err)
	}
	return loadVpnGateway(ctx, gateway.ID)
}

func (a *VpnGatewayAdmin) Get(ctx context.Context, id int64) (gateway *model.VpnGateway, err error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	gateway = &model.VpnGateway{Model: model.Model{ID: id}}
	if err = vpnPreload(db).Where(where, args...).Take(gateway).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query VPN gateway %d: %v", id, err)
		err = NewCLError(ErrVpnGatewayNotFound, "VPN gateway not found", err)
		return
	}
	if !memberShip.CheckResourceOrg(model.OrgReader, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the VPN gateway", nil)
	}
	return
}

func (a *VpnGatewayAdmin) GetByUUID(ctx context.Context, uuID string) (gateway *model.VpnGateway, err error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	gateway = &model.VpnGateway{}
	if err = vpnPreload(db).Where(where, args...).Where("uuid = ?", uuID).Take(gateway).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query VPN gateway %s: %v", uuID, err)
		err = NewCLError(ErrVpnGatewayNotFound, "VPN gateway not found", err)
		return
	}
	if !memberShip.CheckResourceOrg(model.OrgReader, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the VPN gateway", nil)
	}
	return
}

func (a *VpnGatewayAdmin) List(ctx context.Context, offset, limit int64, order, name string, routerID int64) (total int64, gateways []*model.VpnGateway, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgReader) {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}
	if order == "" {
		order = "-created_at"
	}
	where, args := memberShip.GetOrgFilter()
	filter := func(tx *gorm.DB) *gorm.DB {
		tx = tx.Where(where, args...)
		if name != "" {
			tx = tx.Scopes(dbs.Contains(name, "name"))
		}
		if routerID > 0 {
			tx = tx.Where("router_id = ?", routerID)
		}
		return tx
	}
	gateways = []*model.VpnGateway{}
	if err = db.Model(&model.VpnGateway{}).Scopes(filter).Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count VPN gateways", err)
		return
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = vpnPreload(db).Scopes(filter).Find(&gateways).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to query VPN gateways", err)
		return
	}
	if memberShip.IsSystemAdmin() {
		_, db = GetContextDB(ctx)
		for _, gateway := range gateways {
			gateway.OwnerInfo = &model.Organization{Model: model.Model{ID: gateway.Owner}}
			if err = db.Take(gateway.OwnerInfo).Error; err != nil {
				err = NewCLError(ErrOwnerNotFound, "Failed to query owner info", err)
				return
			}
		}
	}
	return
}

func vpnClientParamsValid(ctx context.Context, routerID, gatewayID int64, clientCidr string, port int32) (normalized string, err error) {
	cidrs, err := ParseCidrList(clientCidr)
	if err != nil {
		return
	}
	if len(cidrs) != 1 {
		return "", NewCLError(ErrInvalidParameter, "Client pool must be exactly one network", nil)
	}
	if port <= 0 || port > 65535 {
		return "", NewCLError(ErrInvalidParameter, "Client port must be 1-65535", nil)
	}
	if err = validateVpnPrefixes(ctx, routerID, gatewayID, cidrs, gatewayID, model.VpnPrefixSourceClientPool); err != nil {
		return
	}
	return cidrs[0], nil
}

// Create builds the VRRP pair and the public address and stores the gateway as pending. The node side is
// pushed from the set_vrrp_ip callback once both VRRP interfaces know their node (VpnGatewayVrrpReady):
// at this point their hyper is still -1 and a dispatch would have no target.
func (a *VpnGatewayAdmin) Create(ctx context.Context, params *VpnGatewayParams, router *model.Router, zone *model.Zone) (gateway *model.VpnGateway, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgWriter) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to create VPN gateways", nil)
		return
	}
	if !memberShip.CheckResourceOrg(model.OrgWriter, router.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to use this VPC", nil)
		return
	}
	if err = SecretStoreReady(); err != nil {
		return
	}
	if !params.IpsecEnabled && !params.ClientEnabled {
		err = NewCLError(ErrInvalidParameter, "Enable site-to-site, client access or both", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var count int64
	if err = db.Model(&model.VpnGateway{}).Where("router_id = ?", router.ID).Count(&count).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to count VPN gateways", err)
		return
	}
	if count > 0 {
		err = NewCLError(ErrVpnGatewayExists, "This VPC already has a VPN gateway", nil)
		return
	}
	clientCidr, clientPort := "", int32(VpnDefaultClientPort)
	if params.ClientPort > 0 {
		clientPort = params.ClientPort
	}
	privateKey, publicKey := "", ""
	if params.ClientEnabled {
		if clientCidr, err = vpnClientParamsValid(ctx, router.ID, 0, params.ClientCidr, clientPort); err != nil {
			return
		}
		var priv string
		if priv, publicKey, err = generateWgKeyPair(); err != nil {
			return
		}
		if privateKey, err = EncryptSecret(priv); err != nil {
			return
		}
	}
	if params.ClientRoutes != "" {
		if err = validateClientRoutes(params.ClientRoutes); err != nil {
			return
		}
	}
	haMode := params.HaMode
	if haMode == "" {
		haMode = model.VpnHaModeActiveStandby
	}
	// The VRRP nodes come from the zone. An active_active gateway needs two; an active_standby one runs on
	// a single node when the zone has no second, and gets the BACKUP node once it has (PlaceMissingVrrpBackups)
	nodeCount, err := vpnZoneNodeCount(ctx, zone.ID, -1)
	if err != nil {
		return
	}
	need := int64(1)
	if haMode == model.VpnHaModeActiveActive {
		need = 2
	}
	if nodeCount < need {
		err = NewCLError(ErrVpnGatewayNeedsNodes, fmt.Sprintf("Zone %s has %d available compute node(s), a gateway in this mode needs %d", zone.Name, nodeCount, need), nil)
		return
	}
	// The public addresses first: allocating one can fail (no public subnet, pool exhausted, address in use),
	// and CreateVrrpInstance already pushes set_vrrp_ip.sh to a node, which a rollback would not undo
	publicIps := params.PublicIps
	endpoints := []string{model.VpnEndpointVip1, model.VpnEndpointVip2}
	switch haMode {
	case model.VpnHaModeActiveStandby:
		if len(publicIps) == 0 {
			publicIps = []*VpnPublicIpParams{{}}
		}
		if len(publicIps) > 2 {
			err = NewCLError(ErrInvalidParameter, "An active-standby VPN gateway has at most two public addresses", nil)
			return
		}
	case model.VpnHaModeActiveActive:
		endpoints = []string{model.VpnEndpointNode1, model.VpnEndpointNode2, model.VpnEndpointVip1}
		if len(publicIps) == 0 {
			publicIps = []*VpnPublicIpParams{{}}
		}
		want := 2
		if params.ClientEnabled {
			want = 3
		}
		if len(publicIps) > want {
			err = NewCLError(ErrInvalidParameter, "An active-active VPN gateway has one address per node, and a third one only for its client VPN", nil)
			return
		}
		for len(publicIps) < want {
			publicIps = append(publicIps, &VpnPublicIpParams{Subnets: publicIps[0].Subnets})
		}
	default:
		err = NewCLError(ErrInvalidParameter, "ha_mode must be active_standby or active_active", nil)
		return
	}
	endpointFips := map[string][]*model.FloatingIp{}
	for i, p := range publicIps {
		endpoint := endpoints[i]
		var allocated []*model.FloatingIp
		if allocated, err = (&FloatingIpAdminService{}).Create(ctx, nil, p.Subnets, p.Address, params.Name, params.Inbound, params.Outbound, 1, nil, nil, nil); err != nil {
			return
		}
		endpointFips[endpoint] = allocated
	}
	vrrpInstance, err := CreateVrrpInstance(ctx, params.Name, router, zone)
	if err != nil {
		err = NewCLError(ErrVrrpInstanceCreateFailed, "Failed to create vrrp instance", err)
		return
	}
	gateway = &model.VpnGateway{
		Model: model.Model{Creater: memberShip.UserID}, Owner: memberShip.OrgID,
		Name: params.Name, Description: params.Description, Status: model.VpnGatewayStatusPending,
		RouterID: router.ID, VrrpInstanceID: vrrpInstance.ID, ZoneID: zone.ID,
		IpsecEnabled: params.IpsecEnabled, ClientEnabled: params.ClientEnabled, ClientProtocol: model.VpnClientProtocolWireguard,
		ClientCidr: clientCidr, ClientPort: clientPort, ClientPrivateKey: privateKey, ClientPublicKey: publicKey,
		ClientDns: params.ClientDns, ClientRoutes: params.ClientRoutes, MasterHyper: -1, HaMode: haMode,
	}
	if err = db.Create(gateway).Error; err != nil {
		err = NewCLError(ErrVpnGatewayCreateFailed, "Failed to create VPN gateway", err)
		return
	}
	// Tie the public addresses to the gateway
	for endpoint, fips := range endpointFips {
		for _, fip := range fips {
			if err = db.Model(&model.FloatingIp{Model: model.Model{ID: fip.ID}}).Updates(map[string]interface{}{
				"vpn_gateway_id": gateway.ID, "router_id": router.ID, "type": string(PublicVpnGateway), "vpn_endpoint": endpoint}).Error; err != nil {
				err = NewCLError(ErrVpnGatewayCreateFailed, "Failed to attach the public address", err)
				return
			}
		}
	}
	if _, err = rebuildRemotePrefixes(ctx, gateway); err != nil {
		return
	}
	gateway, err = loadVpnGateway(ctx, gateway.ID)
	return
}

func (a *VpnGatewayAdmin) Update(ctx context.Context, gateway *model.VpnGateway, patch *VpnGatewayPatch) (updated *model.VpnGateway, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the VPN gateway", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	updates := map[string]interface{}{}
	redispatch, reroute := false, false
	if patch.Name != nil && *patch.Name != gateway.Name {
		updates["name"] = *patch.Name
	}
	if patch.Description != nil && *patch.Description != gateway.Description {
		updates["description"] = *patch.Description
	}
	if patch.ClientDns != nil && *patch.ClientDns != gateway.ClientDns {
		updates["client_dns"] = *patch.ClientDns
	}
	if patch.ClientRoutes != nil && *patch.ClientRoutes != gateway.ClientRoutes {
		if *patch.ClientRoutes != "" {
			if err = validateClientRoutes(*patch.ClientRoutes); err != nil {
				return
			}
		}
		updates["client_routes"] = *patch.ClientRoutes
	}
	if patch.IpsecEnabled != nil && *patch.IpsecEnabled != gateway.IpsecEnabled {
		if !*patch.IpsecEnabled && len(gateway.Connections) > 0 {
			err = NewCLError(ErrVpnGatewayInUse, "Delete the site connections before disabling site-to-site", nil)
			return
		}
		updates["ipsec_enabled"] = *patch.IpsecEnabled
		gateway.IpsecEnabled = *patch.IpsecEnabled
		redispatch = true
	}
	clientEnabled := gateway.ClientEnabled
	if patch.ClientEnabled != nil {
		clientEnabled = *patch.ClientEnabled
	}
	// The client VPN of an active_active gateway needs its own floating IP: the node addresses do not move
	if clientEnabled && !gateway.ClientEnabled && VpnIsActiveActive(gateway) && vpnEndpointFips(gateway)[model.VpnEndpointVip1] == nil {
		err = NewCLError(ErrInvalidParameter, "Add a public address for the client VPN first", nil)
		return
	}
	clientCidr := gateway.ClientCidr
	if patch.ClientCidr != nil && *patch.ClientCidr != gateway.ClientCidr {
		if len(gateway.Clients) > 0 {
			err = NewCLError(ErrVpnGatewayInUse, "Delete the clients before changing the client pool", nil)
			return
		}
		clientCidr = *patch.ClientCidr
	}
	clientPort := gateway.ClientPort
	if patch.ClientPort != nil && *patch.ClientPort > 0 {
		clientPort = *patch.ClientPort
	}
	if clientEnabled {
		if err = SecretStoreReady(); err != nil {
			return
		}
		if clientCidr, err = vpnClientParamsValid(ctx, gateway.RouterID, gateway.ID, clientCidr, clientPort); err != nil {
			return
		}
		if gateway.ClientPrivateKey == "" {
			priv, pub, kerr := generateWgKeyPair()
			if kerr != nil {
				return nil, kerr
			}
			enc, eerr := EncryptSecret(priv)
			if eerr != nil {
				return nil, eerr
			}
			updates["client_private_key"], updates["client_public_key"] = enc, pub
			gateway.ClientPrivateKey, gateway.ClientPublicKey = enc, pub
		}
	} else if len(gateway.Clients) > 0 && gateway.ClientEnabled {
		err = NewCLError(ErrVpnGatewayInUse, "Delete the clients before disabling client access", nil)
		return
	}
	if clientEnabled != gateway.ClientEnabled || clientCidr != gateway.ClientCidr || clientPort != gateway.ClientPort {
		updates["client_enabled"], updates["client_cidr"], updates["client_port"] = clientEnabled, clientCidr, clientPort
		gateway.ClientEnabled, gateway.ClientCidr, gateway.ClientPort = clientEnabled, clientCidr, clientPort
		redispatch, reroute = true, true
	}
	if !gateway.IpsecEnabled && !gateway.ClientEnabled {
		err = NewCLError(ErrInvalidParameter, "Enable site-to-site, client access or both", nil)
		return
	}
	if patch.Enabled != nil && *patch.Enabled == gateway.Disabled {
		if err = vpnSetGatewayDisabled(ctx, gateway, !*patch.Enabled); err != nil {
			return
		}
		updates["disabled"] = gateway.Disabled
		redispatch = true
	}
	if len(updates) > 0 {
		if err = db.Model(&model.VpnGateway{Model: model.Model{ID: gateway.ID}}).Updates(updates).Error; err != nil {
			err = NewCLError(ErrVpnGatewayUpdateFailed, "Failed to update VPN gateway", err)
			return
		}
	}
	if reroute {
		if _, err = rebuildRemotePrefixes(ctx, gateway); err != nil {
			return
		}
	}
	if updated, err = loadVpnGateway(ctx, gateway.ID); err != nil {
		return
	}
	// A gateway in error is re-pushed by any PATCH: it goes back to pending and the ready callbacks settle
	// it again, which is the way out of error. Pushing needs the VRRP nodes (vpnGatewayPlaced): before the
	// set_vrrp_ip callbacks chose them, VpnGatewayVrrpReady pushes whatever is stored by then
	if gateway.Status == model.VpnGatewayStatusError {
		redispatch = true
	}
	if redispatch && updated.Status != model.VpnGatewayStatusDeleting {
		nodes, nerr := vpnGatewayVrrpNodes(ctx, updated)
		if nerr == nil && vpnGatewayPlaced(updated, nodes) {
			if updated.Status == model.VpnGatewayStatusError {
				if err = vpnSetStatus(ctx, updated.ID, model.VpnGatewayStatusPending, ""); err != nil {
					return
				}
				// The response is built from updated: it must show what was just stored, the reason included
				vpnApplyStatus(updated, model.VpnGatewayStatusPending, "")
			}
			if err = dispatchVpnAll(ctx, updated, -1); err != nil {
				return
			}
		}
	}
	return
}

// vpnAddableEndpoint is the public address a gateway can still take: the second floating IP of an
// active_standby gateway, the client VPN floating IP of an active_active one
func vpnAddableEndpoint(gateway *model.VpnGateway) string {
	if VpnIsActiveActive(gateway) {
		return model.VpnEndpointVip1
	}
	return model.VpnEndpointVip2
}

// AddPublicIp allocates the address vpnAddableEndpoint names and pushes the gateway again, so that
// keepalived holds it (or, for the client VPN of an active_active gateway, starts holding it)
func (a *VpnGatewayAdmin) AddPublicIp(ctx context.Context, gateway *model.VpnGateway, params *VpnPublicIpParams) (updated *model.VpnGateway, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the VPN gateway", nil)
		return
	}
	if err = vpnAddressChangeAllowed(gateway); err != nil {
		return
	}
	endpoint := vpnAddableEndpoint(gateway)
	fips := vpnEndpointFips(gateway)
	if fips[endpoint] != nil {
		err = NewCLError(ErrInvalidParameter, "The gateway already has all the public addresses it can use", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	inbound, outbound := int32(0), int32(0)
	for _, f := range gateway.FloatingIps {
		inbound, outbound = f.Inbound, f.Outbound
		break
	}
	allocated, err := (&FloatingIpAdminService{}).Create(ctx, nil, params.Subnets, params.Address, gateway.Name, inbound, outbound, 1, nil, nil, nil)
	if err != nil {
		return
	}
	for _, fip := range allocated {
		if err = db.Model(&model.FloatingIp{Model: model.Model{ID: fip.ID}}).Updates(map[string]interface{}{
			"vpn_gateway_id": gateway.ID, "router_id": gateway.RouterID, "type": string(PublicVpnGateway), "vpn_endpoint": endpoint}).Error; err != nil {
			err = NewCLError(ErrVpnGatewayUpdateFailed, "Failed to attach the public address", err)
			return
		}
	}
	return vpnRepushAfterAddressChange(ctx, gateway.ID)
}

// RemovePublicIp releases the address vpnAddableEndpoint names, once nothing uses it: no tunnel on vip2, no
// client VPN on the vip1 of an active_active gateway. The first address of a gateway cannot be removed.
func (a *VpnGatewayAdmin) RemovePublicIp(ctx context.Context, gateway *model.VpnGateway, endpoint string) (updated *model.VpnGateway, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the VPN gateway", nil)
		return
	}
	if err = vpnAddressChangeAllowed(gateway); err != nil {
		return
	}
	if endpoint != vpnAddableEndpoint(gateway) {
		err = NewCLError(ErrInvalidParameter, "This public address cannot be removed from the gateway", nil)
		return
	}
	fip := vpnEndpointFips(gateway)[endpoint]
	if fip == nil {
		err = NewCLError(ErrInvalidParameter, "The gateway has no such public address", nil)
		return
	}
	for _, conn := range gateway.Connections {
		for _, t := range conn.Tunnels {
			if t.Endpoint == endpoint {
				err = NewCLError(ErrVpnGatewayInUse, fmt.Sprintf("Connection %s still has a tunnel on this address", conn.Name), nil)
				return
			}
		}
	}
	if VpnIsActiveActive(gateway) && gateway.ClientEnabled {
		err = NewCLError(ErrVpnGatewayInUse, "Disable the client VPN before removing its address", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if err = db.Model(&model.FloatingIp{Model: model.Model{ID: fip.ID}}).Updates(map[string]interface{}{
		"vpn_gateway_id": 0, "router_id": 0, "type": string(PublicFloating), "vpn_endpoint": ""}).Error; err != nil {
		err = NewCLError(ErrVpnGatewayUpdateFailed, "Failed to detach the public address", err)
		return
	}
	fip.VpnGatewayID, fip.RouterID, fip.Type = 0, 0, string(PublicFloating)
	if err = (&FloatingIpAdminService{}).Delete(ctx, fip); err != nil {
		return
	}
	return vpnRepushAfterAddressChange(ctx, gateway.ID)
}

// vpnAddressChangeAllowed refuses to add or remove a public address unless the gateway is available or in
// error: the change is pushed to the nodes right away (vpnRepushAfterAddressChange), which is skipped while
// the gateway is being built or deleted, and nothing would push it later. The nodes would keep the old
// addresses while the database has the new ones, and a released address could be handed out elsewhere.
func vpnAddressChangeAllowed(gateway *model.VpnGateway) error {
	if gateway.Status == model.VpnGatewayStatusAvailable || gateway.Status == model.VpnGatewayStatusError {
		return nil
	}
	return NewCLError(ErrVpnGatewayNotReady, "Change the public addresses once the gateway is available", nil)
}

// vpnRepushAfterAddressChange reloads a gateway whose public addresses changed and pushes it to its nodes
// (create_vpn_gateway.sh drops an address that is gone together with its rules)
func vpnRepushAfterAddressChange(ctx context.Context, gatewayID int64) (updated *model.VpnGateway, err error) {
	if updated, err = loadVpnGateway(ctx, gatewayID); err != nil {
		return
	}
	if updated.Status != model.VpnGatewayStatusAvailable && updated.Status != model.VpnGatewayStatusError {
		return
	}
	if nodes, nerr := vpnGatewayVrrpNodes(ctx, updated); nerr != nil || !vpnGatewayPlaced(updated, nodes) {
		return
	}
	err = dispatchVpnAll(ctx, updated, -1)
	return
}

// vpnSetGatewayDisabled flips the pause switch of a gateway and resets what the nodes report. A disabled
// gateway runs no tunnel and its master sends no tunnel state, so its connections show "disabled"; on
// enabling they go back to pending until the first report. Alarms still firing for a connection that was
// down are resolved on disabling: after a pause the first report is not a down/up transition and would
// never resolve them.
func vpnSetGatewayDisabled(ctx context.Context, gateway *model.VpnGateway, disabled bool) (err error) {
	ctx, db := GetContextDB(ctx)
	status := model.VpnConnectionStatusPending
	if disabled {
		status = model.VpnConnectionStatusDisabled
	}
	if err = db.Model(&model.VpnConnection{}).Where("vpn_gateway_id = ?", gateway.ID).Update("status", status).Error; err != nil {
		return NewCLError(ErrVpnGatewayUpdateFailed, "Failed to update the connections of the VPN gateway", err)
	}
	// The BGP snapshot goes too: the page reads the neighbor state from it
	updates := map[string]interface{}{"status": status, "established_at": nil, "bgp_state": "", "bgp_status": "", "bgp_reported_at": nil, "bfd_state": ""}
	if err = db.Model(&model.VpnTunnel{}).Where("vpn_gateway_id = ?", gateway.ID).Updates(updates).Error; err != nil {
		return NewCLError(ErrVpnGatewayUpdateFailed, "Failed to update the tunnels of the VPN gateway", err)
	}
	if disabled {
		now := time.Now()
		for _, conn := range gateway.Connections {
			if conn.Status == model.VpnConnectionStatusDown {
				NotifyVpnConnectionState(ctx, gateway, conn, true, now)
			}
			for _, t := range conn.Tunnels {
				if len(conn.Tunnels) > 1 && t.Status == model.VpnConnectionStatusDown {
					NotifyVpnTunnelState(ctx, gateway, conn, t, true, now)
				}
			}
		}
	}
	for _, conn := range gateway.Connections {
		conn.Status = status
		for _, t := range conn.Tunnels {
			t.Status, t.EstablishedAt, t.BgpState, t.BgpStatus, t.BgpReportedAt, t.BfdState = status, nil, "", "", nil, ""
		}
	}
	gateway.Disabled = disabled
	return
}

// deleteVrrpInstance clears the VRRP interfaces on their nodes, removes the instance and, when it was
// the last one of the router, the VRRP subnet. Shared by load balancer and VPN gateway deletion: the
// subnet is counted by VRRP instances, not by load balancers, so a gateway keeps it alive.
func deleteVrrpInstance(ctx context.Context, routerID int64, vrrpInstance *model.VrrpInstance) (err error) {
	ctx, db := GetContextDB(ctx)
	if vrrpInstance.VrrpSubnet == nil {
		vrrpInstance.VrrpSubnet = &model.Subnet{Model: model.Model{ID: vrrpInstance.VrrpSubnetID}}
		if err = db.Take(vrrpInstance.VrrpSubnet).Error; err != nil {
			err = NewCLError(ErrSubnetNotFound, "VRRP subnet not found", err)
			return
		}
	}
	vrrpSubnet := vrrpInstance.VrrpSubnet
	vrrpIface1, vrrpIface2, err := GetVrrpInterfaces(ctx, vrrpInstance.ID)
	if err != nil {
		err = NewCLError(ErrInterfaceDeleteFailed, "Failed to find vrrp interfaces", err)
		return
	}
	pairs := []struct{ me, peer *model.Interface }{{vrrpIface1, vrrpIface2}, {vrrpIface2, vrrpIface1}}
	for _, p := range pairs {
		if p.me.Hyper < 0 {
			continue
		}
		control := fmt.Sprintf("inter=%d", p.me.Hyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_vrrp_ip.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s'", routerID, vrrpInstance.ID, vrrpSubnet.Vlan, ShellEscape(p.me.Address.Address), ShellEscape(p.me.MacAddr), ShellEscape(p.peer.Address.Address), ShellEscape(p.peer.MacAddr))
		if err = HyperExecute(ctx, control, command); err != nil {
			logger.Ctx(ctx).Error("Clear vrrp ip command execution failed ", err)
			return
		}
	}
	if err = DeleteInterface(ctx, vrrpIface1); err != nil {
		err = NewCLError(ErrInterfaceDeleteFailed, "Failed to delete vrrp interface 1", err)
		return
	}
	if err = DeleteInterface(ctx, vrrpIface2); err != nil {
		err = NewCLError(ErrInterfaceDeleteFailed, "Failed to delete vrrp interface 2", err)
		return
	}
	if err = db.Delete(&model.VrrpInstance{Model: model.Model{ID: vrrpInstance.ID}}).Error; err != nil {
		err = NewCLError(ErrVrrpInstanceDeleteFailed, "Failed to delete vrrp instance", err)
		return
	}
	// clear_vrrp_ip.sh deletes fdb entries by MAC, which the other VRRP instances of the VPC share
	ResyncRouterVrrpFdb(ctx, routerID, vrrpInstance.ID)
	var count int64
	if err = db.Model(&model.VrrpInstance{}).Where("router_id = ?", routerID).Count(&count).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to count vrrp instances in the router", err)
		return
	}
	if count == 0 {
		if err = subnetAdmin.Delete(ctx, vrrpSubnet); err != nil {
			err = NewCLError(ErrSubnetDeleteFailed, "Failed to delete vrrp subnet", err)
			return
		}
	}
	return
}

// Delete removes the gateway with its connections and clients, tears it down on every node, releases
// the public address and the VRRP pair
func (a *VpnGatewayAdmin) Delete(ctx context.Context, gateway *model.VpnGateway) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the VPN gateway", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if err = db.Model(&model.VpnGateway{Model: model.Model{ID: gateway.ID}}).Update("status", model.VpnGatewayStatusDeleting).Error; err != nil {
		err = NewCLError(ErrVpnGatewayDeleteFailed, "Failed to mark VPN gateway deleting", err)
		return
	}
	for _, conn := range gateway.Connections {
		if err = softDeleteRenamed(ctx, &model.VpnConnection{}, conn.ID, conn.Name, conn.CreatedAt); err != nil {
			return
		}
	}
	for _, client := range gateway.Clients {
		if err = db.Model(&model.VpnClient{Model: model.Model{ID: client.ID}}).Update("ip_address", "").Error; err != nil {
			err = NewCLError(ErrVpnClientDeleteFailed, "Failed to release client address", err)
			return
		}
		if err = softDeleteRenamed(ctx, &model.VpnClient{}, client.ID, client.Name, client.CreatedAt); err != nil {
			return
		}
	}
	// The tunnels of the connections go with them, as when a single connection is deleted
	if err = db.Where("vpn_gateway_id = ?", gateway.ID).Delete(&model.VpnTunnel{}).Error; err != nil {
		err = NewCLError(ErrVpnGatewayDeleteFailed, "Failed to delete VPN tunnels", err)
		return
	}
	if err = db.Where("vpn_gateway_id = ?", gateway.ID).Delete(&model.VpnRemotePrefix{}).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to clear VPN prefixes", err)
		return
	}
	if gateway.VrrpInstance != nil {
		if err = dispatchVpnClear(ctx, gateway); err != nil {
			return
		}
	}
	for _, fip := range gateway.FloatingIps {
		if err = db.Model(&model.FloatingIp{Model: model.Model{ID: fip.ID}}).Updates(map[string]interface{}{
			"vpn_gateway_id": 0, "router_id": 0, "type": string(PublicFloating)}).Error; err != nil {
			err = NewCLError(ErrVpnGatewayDeleteFailed, "Failed to detach the public address", err)
			return
		}
		fip.VpnGatewayID, fip.RouterID, fip.Type = 0, 0, string(PublicFloating)
		if err = (&FloatingIpAdminService{}).Delete(ctx, fip); err != nil {
			return
		}
	}
	if gateway.VrrpInstance != nil {
		if err = deleteVrrpInstance(ctx, gateway.RouterID, gateway.VrrpInstance); err != nil {
			return
		}
	}
	if err = softDeleteRenamed(ctx, &model.VpnGateway{}, gateway.ID, gateway.Name, gateway.CreatedAt); err != nil {
		return
	}
	for _, conn := range gateway.Connections {
		resolveVpnAlarmsOf(ctx, gateway, conn, conn.Tunnels, true)
	}
	return
}

// softDeleteRenamed soft-deletes a row and gives it a unique name so the (name, parent) unique index
// does not block recreating the same name (the repository convention, PET-1228)
func softDeleteRenamed(ctx context.Context, m interface{}, id int64, name string, createdAt time.Time) (err error) {
	ctx, db := GetContextDB(ctx)
	if err = db.Where("id = ?", id).Delete(m).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to delete %T %d: %v", m, id, err)
		return NewCLError(ErrDatabaseError, "Failed to delete record", err)
	}
	if err = db.Model(m).Unscoped().Where("id = ?", id).Update("name", fmt.Sprintf("%s-%d", name, createdAt.Unix())).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to rename deleted %T %d: %v", m, id, err)
		return NewCLError(ErrDatabaseError, "Failed to rename deleted record", err)
	}
	return
}

// VpnGatewayVrrpReady is called from the set_vrrp_ip callback once both VRRP interfaces have a node:
// this is the earliest moment the gateway can be pushed to the pair
func VpnGatewayVrrpReady(ctx context.Context, vrrpInstanceID int64) (err error) {
	ctx, db := GetContextDB(ctx)
	gateway := &model.VpnGateway{}
	if err = db.Where("vrrp_instance_id = ?", vrrpInstanceID).Take(gateway).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return
	}
	if gateway, err = loadVpnGateway(ctx, gateway.ID); err != nil {
		return
	}
	if err = dispatchVpnAll(ctx, gateway, -1); err != nil {
		// Recorded on the gateway and swallowed: returning it would roll the caller's transaction back,
		// together with the BACKUP interface's node and this very marker, leaving the gateway pending forever
		logger.Ctx(ctx).Errorf("Failed to dispatch VPN gateway %d after vrrp ready: %v", gateway.ID, err)
		err = vpnSetStatus(ctx, gateway.ID, model.VpnGatewayStatusError, fmt.Sprintf("Failed to push the gateway to its nodes: %v", err))
	}
	return
}

// vpnSetStatus changes the status of a gateway together with the reason shown next to it: set for
// error, empty (cleared) for every other status
func vpnSetStatus(ctx context.Context, gatewayID int64, status, reason string) error {
	_, db := GetContextDB(ctx)
	reason = vpnStatusReason(status, reason)
	return db.Model(&model.VpnGateway{Model: model.Model{ID: gatewayID}}).Updates(map[string]interface{}{
		"status": status, "status_reason": reason}).Error
}

// vpnStatusReason is the reason stored with a status: only an error keeps one, cut to the size of the column
func vpnStatusReason(status, reason string) string {
	if status != model.VpnGatewayStatusError {
		return ""
	}
	if r := []rune(reason); len(r) > 255 {
		return string(r[:255])
	}
	return reason
}

// vpnApplyStatus sets on a loaded gateway what vpnSetStatus stored
func vpnApplyStatus(gateway *model.VpnGateway, status, reason string) {
	gateway.Status, gateway.StatusReason = status, vpnStatusReason(status, reason)
}

// vpnGatewayOfVrrp returns the gateway built on a VRRP instance, nil when the instance belongs to a load balancer
func vpnGatewayOfVrrp(ctx context.Context, vrrpInstanceID int64) (gateway *model.VpnGateway, err error) {
	_, db := GetContextDB(ctx)
	gateway = &model.VpnGateway{}
	if err = db.Where("vrrp_instance_id = ?", vrrpInstanceID).Take(gateway).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return
}

// VpnGatewayBackupUnplaced is called when the BACKUP interface of a VRRP instance found no node: its zone
// has no other available node, or none of them is connected. An active_standby gateway still being built
// goes on with its MASTER node alone, without high availability; the watchdog gives it the BACKUP node once
// the zone has one (PlaceMissingVrrpBackups). An active_active gateway cannot run on one node.
func VpnGatewayBackupUnplaced(ctx context.Context, vrrpInstanceID int64, why string) (err error) {
	gateway, err := vpnGatewayOfVrrp(ctx, vrrpInstanceID)
	if err != nil || gateway == nil {
		return
	}
	if VpnIsActiveActive(gateway) {
		return vpnSetStatus(ctx, gateway.ID, model.VpnGatewayStatusError, "An active-active gateway needs two nodes: "+why)
	}
	// A failed attempt of the watchdog on a gateway already running on one node changes nothing
	if gateway.Status != model.VpnGatewayStatusPending {
		logger.Ctx(ctx).Warningf("VPN gateway %d still has a single node: %s", gateway.ID, why)
		return
	}
	logger.Ctx(ctx).Warningf("VPN gateway %d runs on a single node, without high availability: %s", gateway.ID, why)
	return VpnGatewayVrrpReady(ctx, vrrpInstanceID)
}

// VpnGatewayUnplaced is called when not even the MASTER interface of a VRRP instance found a node
func VpnGatewayUnplaced(ctx context.Context, vrrpInstanceID int64, why string) (err error) {
	gateway, err := vpnGatewayOfVrrp(ctx, vrrpInstanceID)
	if err != nil || gateway == nil || gateway.Status != model.VpnGatewayStatusPending {
		return
	}
	return vpnSetStatus(ctx, gateway.ID, model.VpnGatewayStatusError, "No node for the gateway: "+why)
}

// vpnGatewayPlaced tells whether the VRRP nodes of a gateway are known, so that it can be pushed: both
// for an active_active gateway, at least one for an active_standby gateway, which runs on a single node
// when its zone has no second one
func vpnGatewayPlaced(gateway *model.VpnGateway, nodes []int32) bool {
	if VpnIsActiveActive(gateway) {
		return len(nodes) == 2
	}
	return len(nodes) >= 1
}

// vpnZoneNodeCount counts the nodes a VRRP interface of the zone can be placed on, with the conditions of
// GetHyperGroup (skipHyper excluded, -1 for none)
func vpnZoneNodeCount(ctx context.Context, zoneID int64, skipHyper int32) (count int64, err error) {
	_, db := GetContextDB(ctx)
	query := db.Model(&model.Hyper{}).Where("status = 1 AND hostid >= 0 AND hostid <> ?", skipHyper)
	if zoneID > 0 {
		query = query.Where("zone_id = ?", zoneID)
	}
	if err = query.Count(&count).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to count the compute nodes of the zone", err)
	}
	return
}

func vpnGatewayVrrpNodes(ctx context.Context, gateway *model.VpnGateway) (nodes []int32, err error) {
	iface1, iface2, err := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
	if err != nil {
		return
	}
	for _, iface := range []*model.Interface{iface1, iface2} {
		if iface.Hyper >= 0 {
			nodes = append(nodes, iface.Hyper)
		}
	}
	return
}

func vpnHostIsVrrpNode(ctx context.Context, gateway *model.VpnGateway, hostid int32) bool {
	nodes, err := vpnGatewayVrrpNodes(ctx, gateway)
	if err != nil {
		return false
	}
	for _, node := range nodes {
		if node == hostid {
			return true
		}
	}
	return false
}

// VpnGatewayReady records that a VRRP node finished building the gateway
func VpnGatewayReady(ctx context.Context, gatewayID int64, hostid int32, state string) (err error) {
	gateway, err := loadVpnGateway(ctx, gatewayID)
	if err != nil {
		return
	}
	if !vpnHostIsVrrpNode(ctx, gateway, hostid) {
		logger.Ctx(ctx).Warningf("VPN gateway %d state reported by unexpected node %d", gatewayID, hostid)
		return
	}
	status := model.VpnGatewayStatusAvailable
	if state != "ready" {
		status = model.VpnGatewayStatusError
	}
	if gateway.Status == model.VpnGatewayStatusDeleting || gateway.Status == status {
		return
	}
	// A failure on one node while the other reported ready is still an error worth showing
	if status == model.VpnGatewayStatusAvailable && gateway.Status == model.VpnGatewayStatusError {
		return
	}
	return vpnSetStatus(ctx, gatewayID, status, fmt.Sprintf("Node %s failed to build the gateway (see its cloudlet log)", vpnHostName(ctx, hostid)))
}

// vpnHostName is the host name of a node for messages, its ID when it cannot be found
func vpnHostName(ctx context.Context, hostid int32) string {
	_, db := GetContextDB(ctx)
	hyper := &model.Hyper{}
	if db.Select("hostname").Where("hostid = ?", hostid).Take(hyper).Error != nil || hyper.Hostname == "" {
		return fmt.Sprint(hostid)
	}
	return hyper.Hostname
}

// VpnGatewayMaster records which node holds the floating IP and, when it changed, re-points the routes
// of the other nodes. Two different nodes claiming master within a minute is a split brain: the routes
// are left alone (flipping them every heartbeat only spreads the damage) and the event is logged.
func VpnGatewayMaster(ctx context.Context, gatewayID int64, hostid int32) (err error) {
	gateway, err := loadVpnGateway(ctx, gatewayID)
	if err != nil {
		return
	}
	if !vpnHostIsVrrpNode(ctx, gateway, hostid) {
		logger.Ctx(ctx).Warningf("VPN gateway %d master reported by unexpected node %d", gatewayID, hostid)
		return
	}
	ctx, db := GetContextDB(ctx)
	now := time.Now()
	if gateway.MasterHyper == hostid {
		// A claim that left the node before its own release but is handled after it (the heartbeat and the
		// fast path report concurrently, and cland forwards callbacks in parallel) must not wipe the release
		// and refresh the claim time: the peer's takeover would wait for the split-brain window. A node that
		// really holds the address again claims it at every heartbeat of its first minute.
		if vpnReleasedRecently(gatewayID, hostid) {
			logger.Ctx(ctx).Infof("VPN gateway %d: ignoring a master claim of node %d sent before it released the address", gatewayID, hostid)
			return
		}
		vpnForgetClaim(gatewayID)
		return db.Model(&model.VpnGateway{Model: model.Model{ID: gatewayID}}).Update("master_reported_at", now).Error
	}
	if gateway.MasterHyper >= 0 && gateway.MasterReportedAt != nil && now.Sub(*gateway.MasterReportedAt) < vpnMasterFlapWindow {
		// The window guards against a split brain; a second, independent witness that the old master is
		// gone lets the takeover through at once (vpn-gateway-plan.md §8.2, F4)
		if reason := vpnOldMasterGone(gateway.ID, gateway.MasterHyper, *gateway.MasterReportedAt); reason != "" {
			logger.Ctx(ctx).Infof("VPN gateway %d: node %d takes over from node %d at once (%s)", gatewayID, hostid, gateway.MasterHyper, reason)
			return vpnAcceptMaster(ctx, gateway, hostid)
		}
		vpnRecordClaim(gatewayID, hostid)
		logger.Ctx(ctx).Errorf("VPN gateway %d: node %d claims master while node %d reported %s ago, possible split brain, keeping routes",
			gatewayID, hostid, gateway.MasterHyper, now.Sub(*gateway.MasterReportedAt).Round(time.Second))
		return
	}
	return vpnAcceptMaster(ctx, gateway, hostid)
}

// vpnAcceptMaster records the new master and re-points the routes of the other nodes
func vpnAcceptMaster(ctx context.Context, gateway *model.VpnGateway, hostid int32) (err error) {
	ctx, db := GetContextDB(ctx)
	logger.Ctx(ctx).Infof("VPN gateway %d master changed from %d to %d", gateway.ID, gateway.MasterHyper, hostid)
	if err = db.Model(&model.VpnGateway{Model: model.Model{ID: gateway.ID}}).Updates(map[string]interface{}{
		"master_hyper": hostid, "master_reported_at": time.Now()}).Error; err != nil {
		return
	}
	vpnForgetClaim(gateway.ID)
	gateway.MasterHyper = hostid
	if gateway.Status == model.VpnGatewayStatusAvailable {
		err = dispatchVpnRoutes(ctx, gateway, -1)
	}
	return
}

// VpnResyncRouter re-pushes the connection and client configuration of a VPC's gateway after its subnets
// changed: the default local networks, BGP network statements and client AllowedIPs all follow the VPC
func VpnResyncRouter(ctx context.Context, routerID int64) (err error) {
	gateway, err := vpnGatewayOfRouter(ctx, routerID)
	if err != nil || gateway == nil || gateway.Status != model.VpnGatewayStatusAvailable {
		return
	}
	if err = dispatchVpnIpsec(ctx, gateway, -1); err != nil {
		return
	}
	if err = dispatchVpnBgp(ctx, gateway, -1); err != nil {
		return
	}
	if err = dispatchVpnWg(ctx, gateway, -1); err != nil {
		return
	}
	return dispatchVpnRoutes(ctx, gateway, -1)
}

var (
	vpnResyncMu   sync.Mutex
	vpnResyncLast = map[[2]int64]time.Time{}
)

// vpnResyncDue tells whether a route push to (router, node) is worth doing now: launch_vm reports once per
// instance, and a node rebuilding thirty instances of one VPC needs the same push once, not thirty times
func vpnResyncDue(routerID int64, hostid int32) bool {
	key := [2]int64{routerID, int64(hostid)}
	vpnResyncMu.Lock()
	defer vpnResyncMu.Unlock()
	now := time.Now()
	if last, ok := vpnResyncLast[key]; ok && now.Sub(last) < 10*time.Second {
		return false
	}
	vpnResyncLast[key] = now
	return true
}

// VpnResyncNode installs the gateway routes on one node that (re)joined the VPC: a first instance on the
// node, a migration target, or a node rebuilt after a reboot
func VpnResyncNode(ctx context.Context, routerID int64, hostid int32) (err error) {
	if routerID <= 0 || hostid < 0 {
		return
	}
	gateway, err := vpnGatewayOfRouter(ctx, routerID)
	if err != nil || gateway == nil || gateway.Status != model.VpnGatewayStatusAvailable {
		return
	}
	if !vpnResyncDue(routerID, hostid) {
		return
	}
	return dispatchVpnRoutes(ctx, gateway, hostid)
}

// VpnRecoverNode rebuilds every gateway of which the node is a VRRP member, after the node rebooted
func VpnRecoverNode(ctx context.Context, hostid int32) (recovered int, err error) {
	ctx, db := GetContextDB(ctx)
	ifaces := []*model.Interface{}
	if err = db.Preload("Address").Preload("Address.Subnet").Where("hyper = ? and type = 'vrrp'", hostid).Find(&ifaces).Error; err != nil {
		return
	}
	for _, iface := range ifaces {
		gateway := &model.VpnGateway{}
		if db.Where("vrrp_instance_id = ?", iface.Device).Take(gateway).Error != nil {
			continue
		}
		if gateway, err = loadVpnGateway(ctx, gateway.ID); err != nil {
			return
		}
		// A gateway in error gets the node rebuilt as well (the failure may have been on this node); the
		// status itself is cleared by a PATCH, which re-pushes both nodes
		if gateway.Status != model.VpnGatewayStatusAvailable && gateway.Status != model.VpnGatewayStatusError {
			continue
		}
		iface1, iface2, gerr := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
		if gerr != nil {
			return recovered, gerr
		}
		me, peer, role := iface1, iface2, "MASTER"
		if iface2.Hyper == hostid {
			me, peer, role = iface2, iface1, "BACKUP"
		}
		vlan := vpnVrrpVlan(gateway)
		control := fmt.Sprintf("inter=%d", hostid)
		command := fmt.Sprintf(vpnScriptDir+"set_vrrp_ip.sh '%d' '%d' '%d' '%s' '%s' '%s' '%s' '%s'", gateway.RouterID, gateway.VrrpInstanceID, vlan,
			ShellEscape(me.MacAddr), ShellEscape(me.Address.Address), ShellEscape(peer.MacAddr), ShellEscape(peer.Address.Address), ShellEscape(role))
		if err = HyperExecute(ctx, control, command); err != nil {
			return
		}
		// Same order as the load balancer recovery: the VRRP NIC first, then its fdb entries. iface is
		// the same VRRP NIC as me but carries Address.Subnet, which SendFdbRules dereferences
		if ferr := SendFdbRules(ctx, nil, gateway.VrrpInstance, iface); ferr != nil {
			logger.Ctx(ctx).Errorf("Failed to resync fdb rules for VPN gateway %d on hyper %d: %v", gateway.ID, hostid, ferr)
		}
		if err = dispatchVpnAll(ctx, gateway, hostid); err != nil {
			return
		}
		recovered++
	}
	return
}
