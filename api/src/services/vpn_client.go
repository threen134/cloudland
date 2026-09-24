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

	"github.com/apparentlymart/go-cidr/cidr"
	"gorm.io/gorm/clause"
)

var vpnClientAdmin = &VpnClientAdmin{}

type VpnClientAdmin struct{}

const vpnClientKeepalive = 25

type VpnClientParams struct {
	Name         string
	Description  string
	PublicKey    string // empty: the platform generates the key pair and returns the private key once
	PresharedKey bool   // generate a WireGuard preshared key for this peer
	Enabled      bool
}

func (a *VpnClientAdmin) Get(ctx context.Context, gateway *model.VpnGateway, uuID string) (client *model.VpnClient, err error) {
	for _, c := range gateway.Clients {
		if c.UUID == uuID {
			return c, nil
		}
	}
	return nil, NewCLError(ErrVpnClientNotFound, "VPN client not found", nil)
}

// allocateClientAddress hands out the lowest free host of the client pool. The first host is the
// gateway's own address; soft-deleted rows are included so an address released by a delete that has not
// cleared it yet is never reused.
func allocateClientAddress(ctx context.Context, gateway *model.VpnGateway) (address string, err error) {
	ctx, db := GetContextDB(ctx)
	_, ipNet, err := net.ParseCIDR(gateway.ClientCidr)
	if err != nil {
		return "", NewCLError(ErrInvalidCIDR, "Invalid client pool", err)
	}
	rows := []*model.VpnClient{}
	if err = db.Unscoped().Where("vpn_gateway_id = ? and ip_address <> ''", gateway.ID).Find(&rows).Error; err != nil {
		return "", NewCLError(ErrDatabaseError, "Failed to query client addresses", err)
	}
	used := map[string]bool{}
	for _, row := range rows {
		used[row.IPAddress] = true
	}
	first, last := cidr.AddressRange(ipNet)
	ip := cidr.Inc(cidr.Inc(first)) // skip network and gateway addresses
	for ; ipNet.Contains(ip) && !ip.Equal(last); ip = cidr.Inc(ip) {
		if !used[ip.String()] {
			return ip.String(), nil
		}
	}
	return "", NewCLError(ErrVpnClientPoolExhausted, "No free address in the client pool", nil)
}

var wgKeyPattern = regexpMustCompileWgKey()

func regexpMustCompileWgKey() func(string) bool {
	return func(s string) bool {
		if len(s) != 44 || s[43] != '=' {
			return false
		}
		for _, r := range s[:43] {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/') {
				return false
			}
		}
		return true
	}
}

// buildClientConfig renders the WireGuard client configuration. The private key is only known when the
// platform generated it in this very call; otherwise a placeholder is written for the user to fill in.
func buildClientConfig(ctx context.Context, gateway *model.VpnGateway, client *model.VpnClient, privateKey, presharedKey string) (config string, err error) {
	routes, err := effectiveClientRoutes(ctx, gateway)
	if err != nil {
		return
	}
	fip := vpnFloatingIp(gateway)
	if fip == nil {
		return "", NewCLError(ErrVpnGatewayNotReady, "VPN gateway has no public address", nil)
	}
	if privateKey == "" {
		privateKey = "<your private key>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s/32\n", privateKey, client.IPAddress)
	if gateway.ClientDns != "" {
		fmt.Fprintf(&b, "DNS = %s\n", gateway.ClientDns)
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\n", gateway.ClientPublicKey)
	if presharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", presharedKey)
	}
	fmt.Fprintf(&b, "Endpoint = %s:%d\nAllowedIPs = %s\nPersistentKeepalive = %d\n", ipOnly(fip.FipAddress), gateway.ClientPort, strings.Join(routes, ", "), vpnClientKeepalive)
	return b.String(), nil
}

// Create adds a peer. The response carries the generated private key and the full configuration exactly
// once; the database keeps only the public key.
func (a *VpnClientAdmin) Create(ctx context.Context, gateway *model.VpnGateway, params *VpnClientParams) (client *model.VpnClient, privateKey, config string, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to change the VPN gateway", nil)
		return
	}
	if err = vpnRequireGatewayReady(gateway); err != nil {
		return
	}
	if !gateway.ClientEnabled {
		err = NewCLError(ErrInvalidParameter, "Client access is disabled on this gateway", nil)
		return
	}
	if err = SecretStoreReady(); err != nil {
		return
	}
	for _, other := range gateway.Clients {
		if other.Name == params.Name {
			err = NewCLError(ErrInvalidParameter, "A client with this name already exists", nil)
			return
		}
		if params.PublicKey != "" && other.PublicKey == params.PublicKey {
			err = NewCLError(ErrInvalidParameter, "This public key is already registered", nil)
			return
		}
	}
	publicKey := strings.TrimSpace(params.PublicKey)
	if publicKey == "" {
		if privateKey, publicKey, err = generateWgKeyPair(); err != nil {
			return
		}
	} else if !wgKeyPattern(publicKey) {
		err = NewCLError(ErrInvalidParameter, "public_key is not a WireGuard key", nil)
		return
	}
	presharedKey, encryptedPsk := "", ""
	if params.PresharedKey {
		if presharedKey, err = generateWgPresharedKey(); err != nil {
			return
		}
		if encryptedPsk, err = EncryptSecret(presharedKey); err != nil {
			return
		}
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	// Lock the gateway row so two concurrent creates cannot pick the same address
	if err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", gateway.ID).Take(&model.VpnGateway{}).Error; err != nil {
		err = NewCLError(ErrVpnGatewayNotFound, "VPN gateway not found", err)
		return
	}
	address, err := allocateClientAddress(ctx, gateway)
	if err != nil {
		return
	}
	client = &model.VpnClient{
		Model: model.Model{Creater: memberShip.UserID}, Owner: gateway.Owner, Name: params.Name, Description: params.Description,
		VpnGatewayID: gateway.ID, Protocol: model.VpnClientProtocolWireguard, IPAddress: address,
		PublicKey: publicKey, PresharedKey: encryptedPsk, Enabled: params.Enabled,
	}
	if err = db.Create(client).Error; err != nil {
		err = NewCLError(ErrVpnClientCreateFailed, "Failed to create VPN client", err)
		return
	}
	if config, err = buildClientConfig(ctx, gateway, client, privateKey, presharedKey); err != nil {
		return
	}
	if gateway, err = loadVpnGateway(ctx, gateway.ID); err != nil {
		return
	}
	err = dispatchVpnWg(ctx, gateway, -1)
	return
}

func (a *VpnClientAdmin) Update(ctx context.Context, gateway *model.VpnGateway, client *model.VpnClient, name, description *string, enabled *bool) (updated *model.VpnClient, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to change the VPN gateway", nil)
		return
	}
	updates := map[string]interface{}{}
	if name != nil && *name != client.Name {
		for _, other := range gateway.Clients {
			if other.ID != client.ID && other.Name == *name {
				err = NewCLError(ErrInvalidParameter, "A client with this name already exists", nil)
				return
			}
		}
		updates["name"] = *name
	}
	if description != nil && *description != client.Description {
		updates["description"] = *description
	}
	redispatch := false
	if enabled != nil && *enabled != client.Enabled {
		updates["enabled"] = *enabled
		redispatch = true
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if len(updates) > 0 {
		if err = db.Model(&model.VpnClient{Model: model.Model{ID: client.ID}}).Updates(updates).Error; err != nil {
			err = NewCLError(ErrVpnClientUpdateFailed, "Failed to update VPN client", err)
			return
		}
	}
	if gateway, err = loadVpnGateway(ctx, gateway.ID); err != nil {
		return
	}
	if redispatch && gateway.Status == model.VpnGatewayStatusAvailable {
		if err = dispatchVpnWg(ctx, gateway, -1); err != nil {
			return
		}
	}
	updated, err = a.Get(ctx, gateway, client.UUID)
	return
}

// Delete removes the peer and releases its pool address (a soft-deleted row keeping the address would
// slowly eat the pool)
func (a *VpnClientAdmin) Delete(ctx context.Context, gateway *model.VpnGateway, client *model.VpnClient) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, gateway.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the VPN gateway", nil)
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if err = db.Model(&model.VpnClient{Model: model.Model{ID: client.ID}}).Update("ip_address", "").Error; err != nil {
		err = NewCLError(ErrVpnClientDeleteFailed, "Failed to release the client address", err)
		return
	}
	if err = softDeleteRenamed(ctx, &model.VpnClient{}, client.ID, client.Name, client.CreatedAt); err != nil {
		return
	}
	if gateway.Status == model.VpnGatewayStatusAvailable {
		if gateway, err = loadVpnGateway(ctx, gateway.ID); err != nil {
			return
		}
		err = dispatchVpnWg(ctx, gateway, -1)
	}
	return
}

// Config renders the configuration template of an existing client: never a private key, and the
// preshared key only because it lives on the gateway side too
func (a *VpnClientAdmin) Config(ctx context.Context, gateway *model.VpnGateway, client *model.VpnClient) (config string, err error) {
	presharedKey, err := DecryptSecret(client.PresharedKey)
	if err != nil {
		return
	}
	return buildClientConfig(ctx, gateway, client, "", presharedKey)
}
