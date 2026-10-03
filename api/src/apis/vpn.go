/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var (
	vpnGatewayAPI      = &VpnGatewayAPI{}
	vpnConnectionAPI   = &VpnConnectionAPI{}
	vpnClientAPI       = &VpnClientAPI{}
	vpnGatewayAdmin    = &services.VpnGatewayAdmin{}
	vpnConnectionAdmin = &services.VpnConnectionAdmin{}
	vpnClientAdmin     = &services.VpnClientAdmin{}
)

type VpnGatewayAPI struct{}
type VpnConnectionAPI struct{}
type VpnClientAPI struct{}

// Values that end up verbatim in swanctl.conf / frr.conf / wg.conf are format-checked here so a request
// cannot inject configuration lines (the lesson of the load balancer backend address)
var (
	vpnProposalPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+){1,4}(,[a-z0-9]+(-[a-z0-9]+){1,4})*$`)
	vpnIkeIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,128}$`)
	vpnWgKeyPattern    = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)
	vpnBgpPassPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	vpnProposalTokens  = map[string]bool{
		"aes128": true, "aes192": true, "aes256": true, "aes128gcm16": true, "aes256gcm16": true, "chacha20poly1305": true,
		"sha1": true, "sha256": true, "sha384": true, "sha512": true,
		"prfsha256": true, "prfsha384": true, "prfsha512": true,
		"modp1024": true, "modp2048": true, "modp3072": true, "modp4096": true,
		"ecp256": true, "ecp384": true, "ecp521": true, "curve25519": true, "x25519": true,
	}
)

func validateProposal(value string) bool {
	if !vpnProposalPattern.MatchString(value) {
		return false
	}
	for _, proposal := range strings.Split(value, ",") {
		for _, token := range strings.Split(proposal, "-") {
			if !vpnProposalTokens[token] {
				return false
			}
		}
	}
	return true
}

func validatePsk(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e || r == '"' || r == '\\' || r == '\'' {
			return false
		}
	}
	return true
}

func validateDnsList(value string) bool {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ip := net.ParseIP(item); ip == nil || ip.To4() == nil {
			return false
		}
	}
	return true
}

type VpnNodeInfo struct {
	Hostid   int32  `json:"hostid"`
	Hostname string `json:"hostname"`
	Role     string `json:"role"`
	Master   bool   `json:"master"`
}

type VpnPrefixResponse struct {
	Cidr   string `json:"cidr"`
	Source string `json:"source"`
	RefID  int64  `json:"ref_id"`
}

type VpnGatewayResponse struct {
	*ResourceReference
	Description  string             `json:"description"`
	Status       string             `json:"status"`
	StatusReason string             `json:"status_reason,omitempty"` // why the gateway is in error; empty otherwise
	HaMode       string             `json:"ha_mode"`                 // active_standby or active_active
	VPC          *ResourceReference `json:"vpc,omitempty"`
	Zone         string             `json:"zone,omitempty"`
	FloatingIps  []*FloatingIpInfo  `json:"floating_ips"`
	PublicIp     string             `json:"public_ip"`  // vip1, or node1 on an active_active gateway without client VPN
	PublicIps    []*VpnPublicIpInfo `json:"public_ips"` // every address with its endpoint name
	// The public address POST /vpn_gateways/{id}/public_ips would add (vip2, or vip1 for the client VPN of an
	// active_active gateway), empty when the gateway has it already
	AddableEndpoint       string                   `json:"addable_endpoint"`
	Enabled               bool                     `json:"enabled"`
	IpsecEnabled          bool                     `json:"ipsec_enabled"`
	ClientEnabled         bool                     `json:"client_enabled"`
	ClientProtocol        string                   `json:"client_protocol"`
	ClientCidr            string                   `json:"client_cidr"`
	ClientPort            int32                    `json:"client_port"`
	ClientPublicKey       string                   `json:"client_public_key"`
	ClientDns             string                   `json:"client_dns"`
	ClientRoutes          string                   `json:"client_routes"`
	EffectiveClientRoutes []string                 `json:"effective_client_routes"`
	MasterHyper           int32                    `json:"master_hyper"`
	MasterHostname        string                   `json:"master_hostname"`
	MasterReportedAt      string                   `json:"master_reported_at,omitempty"`
	Nodes                 []*VpnNodeInfo           `json:"nodes"`
	ConnectionCount       int                      `json:"connection_count"`
	ClientCount           int                      `json:"client_count"`
	Connections           []*VpnConnectionResponse `json:"connections,omitempty"`
	Clients               []*VpnClientResponse     `json:"clients,omitempty"`
	RemotePrefixes        []*VpnPrefixResponse     `json:"remote_prefixes,omitempty"`
}

type VpnPublicIpInfo struct {
	Endpoint string `json:"endpoint"` // vip1 / vip2 (floating), node1 / node2 (fixed on one node of an active_active gateway)
	Address  string `json:"address"`
	Hostid   int32  `json:"hostid"`             // the node of a fixed address, -1 for a floating one
	Hostname string `json:"hostname,omitempty"` // the node of a fixed address
}

// VpnPublicIpResponse is one public address of a gateway (GET / POST .../public_ips)
type VpnPublicIpResponse struct {
	*VpnPublicIpInfo
	OwnerUUID string `json:"owner_uuid"`
}

type VpnGatewayListResponse struct {
	Offset      int                   `json:"offset"`
	Total       int                   `json:"total"`
	Limit       int                   `json:"limit"`
	VpnGateways []*VpnGatewayResponse `json:"vpn_gateways"`
}

type VpnGatewayPayload struct {
	Name        string         `json:"name" binding:"required,min=2,max=32"`
	Description string         `json:"description" binding:"omitempty,max=255"`
	VPC         *BaseReference `json:"vpc" binding:"required"`
	Zone        string         `json:"zone" binding:"omitempty,min=1,max=32"`
	// active_standby (default): the VRRP master runs every tunnel from one or two floating IPs.
	// active_active: each of the two nodes runs its own tunnels from a fixed address, both carry traffic
	HaMode       string         `json:"ha_mode" binding:"omitempty,oneof=active_standby active_active"`
	PublicSubnet *BaseReference `json:"public_subnet" binding:"omitempty"`
	PublicIp     string         `json:"public_ip" binding:"omitempty,ipv4"`
	// active_standby: one or two entries (vip1, vip2). active_active: node1, node2 and, with client VPN, the
	// client floating IP; missing entries come from the subnet of the first one. public_subnet / public_ip
	// above are the short form of a single entry and are ignored when this is given
	PublicIps     []*VpnPublicIpPayload `json:"public_ips" binding:"omitempty,max=3,dive"`
	Inbound       int32                 `json:"inbound" binding:"omitempty,min=1,max=20000"`
	Outbound      int32                 `json:"outbound" binding:"omitempty,min=1,max=20000"`
	IpsecEnabled  *bool                 `json:"ipsec_enabled"`
	ClientEnabled bool                  `json:"client_enabled"`
	ClientCidr    string                `json:"client_cidr" binding:"omitempty,max=64"`
	ClientPort    int32                 `json:"client_port" binding:"omitempty,min=1,max=65535"`
	ClientDns     string                `json:"client_dns" binding:"omitempty,max=128"`
	ClientRoutes  string                `json:"client_routes" binding:"omitempty,max=512"`
}

type VpnPublicIpPayload struct {
	PublicSubnet *BaseReference `json:"public_subnet" binding:"omitempty"`
	PublicIp     string         `json:"public_ip" binding:"omitempty,ipv4"`
}

type VpnGatewayPatchPayload struct {
	Name          *string `json:"name" binding:"omitempty,min=2,max=32"`
	Description   *string `json:"description" binding:"omitempty,max=255"`
	IpsecEnabled  *bool   `json:"ipsec_enabled"`
	ClientEnabled *bool   `json:"client_enabled"`
	ClientCidr    *string `json:"client_cidr" binding:"omitempty,max=64"`
	ClientPort    *int32  `json:"client_port" binding:"omitempty,min=1,max=65535"`
	ClientDns     *string `json:"client_dns" binding:"omitempty,max=128"`
	ClientRoutes  *string `json:"client_routes" binding:"omitempty,max=512"`
	// false pauses the whole gateway (tunnels, BGP, WireGuard) while keeping its public IP and configuration
	Enabled *bool `json:"enabled"`
}

type VpnConnectionResponse struct {
	*ResourceReference
	Description         string               `json:"description"`
	Status              string               `json:"status"` // pending, up, degraded (only a standby tunnel is up), down, disabled
	LocalID             string               `json:"local_id"`
	RouteMode           string               `json:"route_mode"`
	LocalCidrs          string               `json:"local_cidrs"`
	EffectiveLocalCidrs []string             `json:"effective_local_cidrs"`
	RemoteCidrs         string               `json:"remote_cidrs"`
	RemoteSummaryCidrs  string               `json:"remote_summary_cidrs"`
	MaxPrefixes         int32                `json:"max_prefixes"`
	LocalAsn            int64                `json:"local_asn"`
	PeerAsn             int64                `json:"peer_asn"`
	BgpPasswordSet      bool                 `json:"bgp_password_set"`
	BgpKeepalive        int32                `json:"bgp_keepalive"`
	BgpHold             int32                `json:"bgp_hold"`
	AuthMethod          string               `json:"auth_method"`
	PskSet              bool                 `json:"psk_set"`
	IkeVersion          int32                `json:"ike_version"`
	IkeProposal         string               `json:"ike_proposal"`
	EspProposal         string               `json:"esp_proposal"`
	IkeLifetime         int32                `json:"ike_lifetime"`
	EspLifetime         int32                `json:"esp_lifetime"`
	DpdAction           string               `json:"dpd_action"`
	DpdDelay            int32                `json:"dpd_delay"`
	Initiator           bool                 `json:"initiator"`
	AsPathPrepend       int32                `json:"as_path_prepend"`
	BfdEnabled          bool                 `json:"bfd_enabled"`
	BfdInterval         int32                `json:"bfd_interval"`
	BfdMultiplier       int32                `json:"bfd_multiplier"`
	TrafficPolicy       string               `json:"traffic_policy"` // preferred or ecmp
	BytesIn             int64                `json:"bytes_in"`       // sum over the tunnels
	BytesOut            int64                `json:"bytes_out"`      // sum over the tunnels
	Tunnels             []*VpnTunnelResponse `json:"tunnels"`
	// The keys themselves, only for members with write permission on the gateway (vpnSecretsVisible)
	Psk         string `json:"psk,omitempty"`
	BgpPassword string `json:"bgp_password,omitempty"`
}

// VpnTunnelResponse is one tunnel of a connection: its configuration and what the master reports
type VpnTunnelResponse struct {
	ID            string                 `json:"id"`
	Slot          int32                  `json:"slot"`
	Endpoint      string                 `json:"endpoint"`  // vip1 / vip2, or node1 / node2 on an active_active gateway
	PublicIp      string                 `json:"public_ip"` // our address of this tunnel
	Hostid        int32                  `json:"hostid"`    // the node running it on an active_active gateway, -1 otherwise (the master)
	Hostname      string                 `json:"hostname,omitempty"`
	Priority      string                 `json:"priority"` // primary or standby (every tunnel is primary with traffic_policy ecmp)
	RemoteGateway string                 `json:"remote_gateway"`
	RemoteID      string                 `json:"remote_id"`
	PskSet        bool                   `json:"psk_set"` // a tunnel-specific key; otherwise the connection's
	TunnelLocalIP string                 `json:"tunnel_local_ip"`
	TunnelPeerIP  string                 `json:"tunnel_peer_ip"`
	IfID          int32                  `json:"if_id"`
	Status        string                 `json:"status"`
	EstablishedAt string                 `json:"established_at,omitempty"`
	BytesIn       int64                  `json:"bytes_in"`
	BytesOut      int64                  `json:"bytes_out"`
	LastError     string                 `json:"last_error"`
	Bgp           *services.VpnBgpReport `json:"bgp,omitempty"`
	BgpReportedAt string                 `json:"bgp_reported_at,omitempty"`
	BfdState      string                 `json:"bfd_state,omitempty"`
	// The tunnel-specific key, only for members with write permission on the gateway
	Psk string `json:"psk,omitempty"`
}

type VpnConnectionListResponse struct {
	Total       int                      `json:"total"`
	Connections []*VpnConnectionResponse `json:"connections"`
}

// VpnConnectionPayload creates a connection; the same shape with pointers (VpnConnectionPatchPayload)
// updates one. psk and bgp_password come back only to members with write permission (vpnSecretsVisible).
// VpnTunnelPayload is one tunnel of a connection. The list position is the slot. psk overrides the
// connection's key for this tunnel; on a patch, leaving it out keeps what the slot has.
type VpnTunnelPayload struct {
	Endpoint      string  `json:"endpoint" binding:"omitempty,oneof=vip1 vip2 node1 node2"`
	Priority      string  `json:"priority" binding:"omitempty,oneof=primary standby"`
	RemoteGateway string  `json:"remote_gateway" binding:"omitempty,ipv4"`
	RemoteID      string  `json:"remote_id" binding:"omitempty,max=128"`
	Psk           *string `json:"psk" binding:"omitempty,max=128"`
	TunnelLocalIP string  `json:"tunnel_local_ip" binding:"omitempty,ipv4"`
	TunnelPeerIP  string  `json:"tunnel_peer_ip" binding:"omitempty,ipv4"`
}

// VpnConnectionPayload creates a connection with one or two tunnels (tunnels). remote_gateway, remote_id,
// tunnel_local_ip and tunnel_peer_ip are the short form of a single tunnel and are ignored when tunnels is given.
type VpnConnectionPayload struct {
	Name        string              `json:"name" binding:"required,min=2,max=32"`
	Description string              `json:"description" binding:"omitempty,max=255"`
	Tunnels     []*VpnTunnelPayload `json:"tunnels" binding:"omitempty,max=4,dive"`
	// preferred (default): the primary tunnel carries the traffic; ecmp: every tunnel that is up shares it
	TrafficPolicy      string  `json:"traffic_policy" binding:"omitempty,oneof=preferred ecmp"`
	RemoteGateway      string  `json:"remote_gateway" binding:"omitempty,ipv4"`
	RemoteID           string  `json:"remote_id" binding:"omitempty,max=128"`
	LocalID            string  `json:"local_id" binding:"omitempty,max=128"`
	RouteMode          string  `json:"route_mode" binding:"omitempty,oneof=static bgp"`
	LocalCidrs         string  `json:"local_cidrs" binding:"omitempty,max=512"`
	RemoteCidrs        string  `json:"remote_cidrs" binding:"omitempty,max=512"`
	RemoteSummaryCidrs string  `json:"remote_summary_cidrs" binding:"omitempty,max=512"`
	MaxPrefixes        int32   `json:"max_prefixes" binding:"omitempty,min=1,max=100000"`
	LocalAsn           int64   `json:"local_asn" binding:"omitempty,min=1,max=4294967295"`
	PeerAsn            int64   `json:"peer_asn" binding:"omitempty,min=1,max=4294967295"`
	TunnelLocalIP      string  `json:"tunnel_local_ip" binding:"omitempty,ipv4"`
	TunnelPeerIP       string  `json:"tunnel_peer_ip" binding:"omitempty,ipv4"`
	BgpPassword        *string `json:"bgp_password" binding:"omitempty,max=80"`
	BgpKeepalive       int32   `json:"bgp_keepalive" binding:"omitempty,min=1,max=3600"`
	BgpHold            int32   `json:"bgp_hold" binding:"omitempty,min=3,max=10800"`
	Psk                *string `json:"psk" binding:"omitempty,max=128"`
	IkeProposal        string  `json:"ike_proposal" binding:"omitempty,max=128"`
	EspProposal        string  `json:"esp_proposal" binding:"omitempty,max=128"`
	IkeLifetime        int32   `json:"ike_lifetime" binding:"omitempty,min=300,max=604800"`
	EspLifetime        int32   `json:"esp_lifetime" binding:"omitempty,min=300,max=86400"`
	DpdAction          string  `json:"dpd_action" binding:"omitempty,oneof=restart clear none"`
	DpdDelay           int32   `json:"dpd_delay" binding:"omitempty,min=5,max=3600"`
	Initiator          *bool   `json:"initiator"`
	AsPathPrepend      *int32  `json:"as_path_prepend" binding:"omitempty,min=0,max=10"`
	BfdEnabled         *bool   `json:"bfd_enabled"`
	BfdInterval        int32   `json:"bfd_interval" binding:"omitempty,min=300,max=60000"`
	BfdMultiplier      int32   `json:"bfd_multiplier" binding:"omitempty,min=2,max=50"`
}

// VpnConnectionPatchPayload changes a connection; tunnels, when given, replaces the whole tunnel list.
// The single-tunnel short form (remote_gateway, ...) changes the first tunnel.
type VpnConnectionPatchPayload struct {
	Name               *string             `json:"name" binding:"omitempty,min=2,max=32"`
	Description        *string             `json:"description" binding:"omitempty,max=255"`
	Tunnels            []*VpnTunnelPayload `json:"tunnels" binding:"omitempty,max=4,dive"`
	TrafficPolicy      *string             `json:"traffic_policy" binding:"omitempty,oneof=preferred ecmp"`
	RemoteGateway      *string             `json:"remote_gateway" binding:"omitempty,max=64"`
	RemoteID           *string             `json:"remote_id" binding:"omitempty,max=128"`
	LocalID            *string             `json:"local_id" binding:"omitempty,max=128"`
	RouteMode          *string             `json:"route_mode" binding:"omitempty,oneof=static bgp"`
	LocalCidrs         *string             `json:"local_cidrs" binding:"omitempty,max=512"`
	RemoteCidrs        *string             `json:"remote_cidrs" binding:"omitempty,max=512"`
	RemoteSummaryCidrs *string             `json:"remote_summary_cidrs" binding:"omitempty,max=512"`
	MaxPrefixes        *int32              `json:"max_prefixes" binding:"omitempty,min=1,max=100000"`
	LocalAsn           *int64              `json:"local_asn" binding:"omitempty,min=1,max=4294967295"`
	PeerAsn            *int64              `json:"peer_asn" binding:"omitempty,min=1,max=4294967295"`
	TunnelLocalIP      *string             `json:"tunnel_local_ip" binding:"omitempty,max=64"`
	TunnelPeerIP       *string             `json:"tunnel_peer_ip" binding:"omitempty,max=64"`
	BgpPassword        *string             `json:"bgp_password" binding:"omitempty,max=80"`
	BgpKeepalive       *int32              `json:"bgp_keepalive" binding:"omitempty,min=1,max=3600"`
	BgpHold            *int32              `json:"bgp_hold" binding:"omitempty,min=3,max=10800"`
	Psk                *string             `json:"psk" binding:"omitempty,max=128"`
	IkeProposal        *string             `json:"ike_proposal" binding:"omitempty,max=128"`
	EspProposal        *string             `json:"esp_proposal" binding:"omitempty,max=128"`
	IkeLifetime        *int32              `json:"ike_lifetime" binding:"omitempty,min=300,max=604800"`
	EspLifetime        *int32              `json:"esp_lifetime" binding:"omitempty,min=300,max=86400"`
	DpdAction          *string             `json:"dpd_action" binding:"omitempty,oneof=restart clear none"`
	DpdDelay           *int32              `json:"dpd_delay" binding:"omitempty,min=5,max=3600"`
	Initiator          *bool               `json:"initiator"`
	AsPathPrepend      *int32              `json:"as_path_prepend" binding:"omitempty,min=0,max=10"`
	BfdEnabled         *bool               `json:"bfd_enabled"`
	BfdInterval        *int32              `json:"bfd_interval" binding:"omitempty,min=300,max=60000"`
	BfdMultiplier      *int32              `json:"bfd_multiplier" binding:"omitempty,min=2,max=50"`
}

type VpnClientResponse struct {
	*ResourceReference
	Description     string `json:"description"`
	Protocol        string `json:"protocol"`
	IPAddress       string `json:"ip_address"`
	PublicKey       string `json:"public_key"`
	PresharedKeySet bool   `json:"preshared_key_set"`
	Enabled         bool   `json:"enabled"`
	LastHandshakeAt string `json:"last_handshake_at,omitempty"`
	BytesIn         int64  `json:"bytes_in"`
	BytesOut        int64  `json:"bytes_out"`
}

// VpnClientCreateResponse is returned once: the private key is not stored anywhere
type VpnClientCreateResponse struct {
	*VpnClientResponse
	PrivateKey string `json:"private_key,omitempty"`
	Config     string `json:"config"`
}

type VpnClientListResponse struct {
	Total   int                  `json:"total"`
	Clients []*VpnClientResponse `json:"clients"`
}

type VpnClientPayload struct {
	Name         string `json:"name" binding:"required,min=2,max=32"`
	Description  string `json:"description" binding:"omitempty,max=255"`
	PublicKey    string `json:"public_key" binding:"omitempty,max=64"`
	PresharedKey bool   `json:"preshared_key"`
	Enabled      *bool  `json:"enabled"`
}

type VpnClientPatchPayload struct {
	Name        *string `json:"name" binding:"omitempty,min=2,max=32"`
	Description *string `json:"description" binding:"omitempty,max=255"`
	Enabled     *bool   `json:"enabled"`
}

type VpnClientConfigResponse struct {
	Config string `json:"config"`
}

func hyperHostname(ctx context.Context, hostid int32) string {
	if hostid < 0 {
		return ""
	}
	_, db := GetContextDB(ctx)
	hyper := &model.Hyper{}
	if db.Where("hostid = ?", hostid).Take(hyper).Error != nil {
		return ""
	}
	return hyper.Hostname
}

func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(TimeStringForMat)
}

func (v *VpnGatewayAPI) getResponse(ctx context.Context, gateway *model.VpnGateway, detail bool) (resp *VpnGatewayResponse, err error) {
	resp = &VpnGatewayResponse{
		ResourceReference: &ResourceReference{
			ID: gateway.UUID, Name: gateway.Name, Owner: orgAdmin.GetOrgName(ctx, gateway.Owner), OwnerUUID: orgAdmin.GetOrgUUID(ctx, gateway.Owner),
			CreatedAt: gateway.CreatedAt.Format(TimeStringForMat), UpdatedAt: gateway.UpdatedAt.Format(TimeStringForMat),
		},
		Description: gateway.Description, Status: gateway.Status, StatusReason: gateway.StatusReason, Enabled: !gateway.Disabled, HaMode: services.VpnHaModeOf(gateway),
		IpsecEnabled: gateway.IpsecEnabled, ClientEnabled: gateway.ClientEnabled, ClientProtocol: gateway.ClientProtocol,
		ClientCidr: gateway.ClientCidr, ClientPort: gateway.ClientPort, ClientPublicKey: gateway.ClientPublicKey,
		ClientDns: gateway.ClientDns, ClientRoutes: gateway.ClientRoutes,
		MasterHyper: gateway.MasterHyper, MasterHostname: hyperHostname(ctx, gateway.MasterHyper), MasterReportedAt: formatTimePtr(gateway.MasterReportedAt),
		ConnectionCount: len(gateway.Connections), ClientCount: len(gateway.Clients),
		FloatingIps: []*FloatingIpInfo{}, Nodes: []*VpnNodeInfo{}, EffectiveClientRoutes: []string{},
	}
	if gateway.Router != nil {
		resp.VPC = &ResourceReference{ID: gateway.Router.UUID, Name: gateway.Router.Name}
	}
	if gateway.ZoneID > 0 {
		if zone, zerr := zoneAdmin.Get(ctx, gateway.ZoneID); zerr == nil && zone != nil {
			resp.Zone = zone.Name
		}
	}
	for _, fip := range gateway.FloatingIps {
		resp.FloatingIps = append(resp.FloatingIps, &FloatingIpInfo{ResourceReference: &ResourceReference{ID: fip.UUID, Name: fip.Name}, FipAddress: fip.FipAddress})
	}
	resp.PublicIps = vpnPublicIpInfos(ctx, gateway)
	for _, info := range resp.PublicIps {
		if resp.PublicIp == "" || info.Endpoint == model.VpnEndpointVip1 {
			resp.PublicIp = info.Address
		}
	}
	if endpoint := services.VpnAddableEndpoint(gateway); services.VpnEndpointAddresses(gateway)[endpoint] == "" {
		resp.AddableEndpoint = endpoint
	}
	if gateway.VrrpInstanceID > 0 {
		iface1, iface2, ierr := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
		if ierr == nil {
			for _, item := range []struct {
				iface *model.Interface
				role  string
			}{{iface1, "MASTER"}, {iface2, "BACKUP"}} {
				if item.iface.Hyper < 0 {
					continue
				}
				resp.Nodes = append(resp.Nodes, &VpnNodeInfo{Hostid: item.iface.Hyper, Hostname: hyperHostname(ctx, item.iface.Hyper), Role: item.role, Master: item.iface.Hyper == gateway.MasterHyper})
			}
		}
	}
	if !detail {
		return
	}
	if gateway.ClientEnabled {
		if routes, rerr := services.EffectiveClientRoutes(ctx, gateway); rerr == nil {
			resp.EffectiveClientRoutes = routes
		}
	}
	resp.Connections = []*VpnConnectionResponse{}
	view := newVpnConnView(ctx, gateway)
	for _, conn := range gateway.Connections {
		resp.Connections = append(resp.Connections, vpnConnectionAPI.responseIn(ctx, gateway, conn, view))
	}
	resp.Clients = []*VpnClientResponse{}
	for _, client := range gateway.Clients {
		resp.Clients = append(resp.Clients, vpnClientAPI.getResponse(client))
	}
	resp.RemotePrefixes = []*VpnPrefixResponse{}
	if prefixes, perr := services.LoadRemotePrefixes(ctx, gateway.ID); perr == nil {
		for _, p := range prefixes {
			resp.RemotePrefixes = append(resp.RemotePrefixes, &VpnPrefixResponse{Cidr: p.Cidr, Source: p.Source, RefID: p.RefID})
		}
	}
	return
}

// @Summary list VPN gateways
// @Description list VPN gateways
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} VpnGatewayListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways [get]
func (v *VpnGatewayAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	queryStr := c.DefaultQuery("query", "")
	order := c.DefaultQuery("order", "-created_at")
	vpcID := strings.TrimSpace(c.DefaultQuery("vpc_id", ""))
	var routerID int64
	if vpcID != "" {
		router, err := routerAdmin.GetRouterByUUID(ctx, vpcID)
		if err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid query vpc_id", err)
			return
		}
		routerID = router.ID
	}
	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit < 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	total, gateways, err := vpnGatewayAdmin.List(ctx, int64(offset), int64(limit), order, queryStr, routerID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list VPN gateways", err)
		return
	}
	resp := &VpnGatewayListResponse{Total: int(total), Offset: offset, Limit: len(gateways), VpnGateways: make([]*VpnGatewayResponse, len(gateways))}
	for i, gateway := range gateways {
		if resp.VpnGateways[i], err = v.getResponse(ctx, gateway, false); err != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a VPN gateway
// @Description get a VPN gateway with its connections, clients and routed prefixes; the connection keys (psk, bgp_password) are included for members with write permission
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} VpnGatewayResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id} [get]
func (v *VpnGatewayAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	resp, err := v.getResponse(ctx, gateway, true)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary create a VPN gateway
// @Description create a VPN gateway in a VPC with a public address. Its two nodes come from the zone: an active_active gateway needs two available compute nodes there; an active_standby gateway runs on a single node, without high availability, when the zone has only one, and gets its second node once the zone has another (error code 132009 when the zone has too few nodes)
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   message	body   VpnGatewayPayload  true   "VPN gateway create payload"
// @Success 200 {object} VpnGatewayResponse
// @Failure 400 {object} common.APIError "Bad request, or too few available compute nodes in the zone (132009)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways [post]
func (v *VpnGatewayAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &VpnGatewayPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	router, err := routerAdmin.GetRouter(ctx, payload.VPC)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to get vpc", err)
		return
	}
	var zone *model.Zone
	if payload.Zone != "" {
		zone, err = zoneAdmin.GetZoneByName(ctx, payload.Zone)
	} else {
		zone, err = zoneAdmin.GetDefaultZone(ctx)
	}
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid zone", err)
		return
	}
	publicIps := payload.PublicIps
	if len(publicIps) == 0 {
		publicIps = []*VpnPublicIpPayload{{PublicSubnet: payload.PublicSubnet, PublicIp: payload.PublicIp}}
	}
	publicParams := []*services.VpnPublicIpParams{}
	for _, p := range publicIps {
		entry := &services.VpnPublicIpParams{Address: p.PublicIp}
		if p.PublicSubnet != nil {
			subnet, serr := subnetAdmin.GetSubnet(ctx, p.PublicSubnet)
			if serr != nil {
				ErrorResponse(c, http.StatusBadRequest, "Failed to get public subnet", serr)
				return
			}
			entry.Subnets = append(entry.Subnets, subnet)
		}
		publicParams = append(publicParams, entry)
	}
	seenAddress := map[string]bool{}
	for _, p := range publicParams {
		if p.Address != "" && seenAddress[p.Address] {
			ErrorResponse(c, http.StatusBadRequest, "The public addresses must differ", nil)
			return
		}
		seenAddress[p.Address] = true
	}
	if payload.ClientDns != "" && !validateDnsList(payload.ClientDns) {
		ErrorResponse(c, http.StatusBadRequest, "client_dns must be IPv4 addresses", nil)
		return
	}
	ipsecEnabled := true
	if payload.IpsecEnabled != nil {
		ipsecEnabled = *payload.IpsecEnabled
	}
	params := &services.VpnGatewayParams{
		HaMode: payload.HaMode, Name: payload.Name, Description: payload.Description, IpsecEnabled: ipsecEnabled, ClientEnabled: payload.ClientEnabled,
		ClientCidr: payload.ClientCidr, ClientPort: payload.ClientPort, ClientDns: payload.ClientDns, ClientRoutes: payload.ClientRoutes,
		PublicIps: publicParams, Inbound: payload.Inbound, Outbound: payload.Outbound,
	}
	gateway, err := vpnGatewayAdmin.Create(ctx, params, router, zone)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	resp, err := v.getResponse(ctx, gateway, true)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary patch a VPN gateway
// @Description patch a VPN gateway
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   message	body   VpnGatewayPatchPayload  true   "VPN gateway patch payload"
// @Success 200 {object} VpnGatewayResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id} [patch]
func (v *VpnGatewayAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	payload := &VpnGatewayPatchPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	if payload.ClientDns != nil && *payload.ClientDns != "" && !validateDnsList(*payload.ClientDns) {
		ErrorResponse(c, http.StatusBadRequest, "client_dns must be IPv4 addresses", nil)
		return
	}
	patch := &services.VpnGatewayPatch{
		Name: payload.Name, Description: payload.Description, IpsecEnabled: payload.IpsecEnabled, ClientEnabled: payload.ClientEnabled,
		ClientCidr: payload.ClientCidr, ClientPort: payload.ClientPort, ClientDns: payload.ClientDns, ClientRoutes: payload.ClientRoutes,
		Enabled: payload.Enabled,
	}
	// A pure enable / disable request gets its own action name in the audit log and the activity feed
	if payload.Enabled != nil && payload.Name == nil && payload.Description == nil && payload.IpsecEnabled == nil && payload.ClientEnabled == nil &&
		payload.ClientCidr == nil && payload.ClientPort == nil && payload.ClientDns == nil && payload.ClientRoutes == nil {
		if *payload.Enabled {
			SetAuditAction(c, "vpn_gateway.enable")
		} else {
			SetAuditAction(c, "vpn_gateway.disable")
		}
	}
	gateway, err = vpnGatewayAdmin.Update(ctx, gateway, patch)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Patch VPN gateway failed", err)
		return
	}
	resp, err := v.getResponse(ctx, gateway, true)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary delete a VPN gateway
// @Description delete a VPN gateway with its connections and clients
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id} [delete]
func (v *VpnGatewayAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	if err = vpnGatewayAdmin.Delete(ctx, gateway); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// vpnSecretsVisible tells whether the caller gets the stored keys back: members who may change the
// gateway can already replace them, read-only members never see them
func vpnSecretsVisible(ctx context.Context, gateway *model.VpnGateway) bool {
	return GetMemberShip(ctx).CheckResourceOrg(model.OrgWriter, gateway.Owner)
}

// vpnRevealSecret decrypts a stored key for a response; a key that cannot be decrypted (VPN_SECRET_KEY
// missing or changed) is left out instead of failing the whole query
func vpnRevealSecret(ctx context.Context, enc string) string {
	plain, err := DecryptSecret(enc)
	if err != nil {
		logger.Ctx(ctx).Warningf("VPN credential not returned: %v", err)
		return ""
	}
	return plain
}

// vpnConnView is what every connection response of one gateway needs besides the connection, read once
// per request instead of once per connection: the node of each endpoint of an active_active gateway with
// its host name, and the VPC's internal subnets (the local networks of a connection that names none)
type vpnConnView struct {
	endpointHosts map[string]int32
	hostnames     map[int32]string
	vpcCidrs      []string
	vpcCidrsRead  bool
}

func newVpnConnView(ctx context.Context, gateway *model.VpnGateway) *vpnConnView {
	view := &vpnConnView{endpointHosts: services.VpnEndpointHostsOf(ctx, gateway), hostnames: map[int32]string{}}
	for _, host := range view.endpointHosts {
		if host >= 0 {
			view.hostnames[host] = hyperHostname(ctx, host)
		}
	}
	return view
}

func (view *vpnConnView) localCidrs(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection) ([]string, error) {
	if strings.TrimSpace(conn.LocalCidrs) != "" {
		return services.EffectiveLocalCidrs(ctx, gateway, conn)
	}
	if !view.vpcCidrsRead {
		cidrs, err := services.VpcInternalCidrs(ctx, gateway.RouterID)
		if err != nil {
			return nil, err
		}
		view.vpcCidrs, view.vpcCidrsRead = cidrs, true
	}
	return view.vpcCidrs, nil
}

func (v *VpnConnectionAPI) getResponse(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection) *VpnConnectionResponse {
	return v.responseIn(ctx, gateway, conn, newVpnConnView(ctx, gateway))
}

// responseIn builds the response of one connection with the view shared by all the connections of the gateway
func (v *VpnConnectionAPI) responseIn(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, view *vpnConnView) *VpnConnectionResponse {
	secrets := vpnSecretsVisible(ctx, gateway)
	resp := &VpnConnectionResponse{
		ResourceReference: &ResourceReference{
			ID: conn.UUID, Name: conn.Name, Owner: orgAdmin.GetOrgName(ctx, conn.Owner), OwnerUUID: orgAdmin.GetOrgUUID(ctx, conn.Owner),
			CreatedAt: conn.CreatedAt.Format(TimeStringForMat), UpdatedAt: conn.UpdatedAt.Format(TimeStringForMat),
		},
		Description: conn.Description, Status: conn.Status, LocalID: conn.LocalID,
		RouteMode: conn.RouteMode, LocalCidrs: conn.LocalCidrs, RemoteCidrs: conn.RemoteCidrs, RemoteSummaryCidrs: conn.RemoteSummaryCidrs,
		MaxPrefixes: conn.MaxPrefixes, LocalAsn: conn.LocalAsn, PeerAsn: conn.PeerAsn, BgpPasswordSet: conn.BgpPassword != "",
		BgpKeepalive: conn.BgpKeepalive, BgpHold: conn.BgpHold, AuthMethod: conn.AuthMethod, PskSet: conn.Psk != "", IkeVersion: conn.IkeVersion,
		IkeProposal: conn.IkeProposal, EspProposal: conn.EspProposal, IkeLifetime: conn.IkeLifetime, EspLifetime: conn.EspLifetime,
		DpdAction: conn.DpdAction, DpdDelay: conn.DpdDelay, Initiator: conn.Initiator,
		AsPathPrepend: conn.AsPathPrepend, BfdEnabled: conn.BfdEnabled, BfdInterval: conn.BfdInterval, BfdMultiplier: conn.BfdMultiplier,
		TrafficPolicy: services.VpnTrafficPolicyOf(conn), EffectiveLocalCidrs: []string{}, Tunnels: []*VpnTunnelResponse{},
	}
	if cidrs, err := view.localCidrs(ctx, gateway, conn); err == nil {
		resp.EffectiveLocalCidrs = cidrs
	}
	if secrets {
		resp.Psk, resp.BgpPassword = vpnRevealSecret(ctx, conn.Psk), vpnRevealSecret(ctx, conn.BgpPassword)
	}
	endpoints := services.VpnEndpointAddresses(gateway)
	// By endpoint, not by tunnel: a connection just created is not in gateway.Connections yet
	for _, t := range conn.Tunnels {
		host, ok := view.endpointHosts[t.Endpoint]
		if !ok {
			host = -1
		}
		tr := &VpnTunnelResponse{
			ID: t.UUID, Slot: t.Slot, Endpoint: t.Endpoint, PublicIp: endpoints[t.Endpoint], Priority: t.Priority,
			Hostid: host, Hostname: view.hostnames[host],
			RemoteGateway: t.RemoteGateway, RemoteID: t.RemoteID, PskSet: t.Psk != "", TunnelLocalIP: t.TunnelLocalIP, TunnelPeerIP: t.TunnelPeerIP,
			IfID: t.IfID, Status: t.Status, EstablishedAt: formatTimePtr(t.EstablishedAt), BytesIn: t.BytesIn, BytesOut: t.BytesOut,
			LastError: t.LastError, BgpReportedAt: formatTimePtr(t.BgpReportedAt), BfdState: t.BfdState,
		}
		if secrets {
			tr.Psk = vpnRevealSecret(ctx, t.Psk)
		}
		if t.BgpStatus != "" {
			report := &services.VpnBgpReport{}
			if json.Unmarshal([]byte(t.BgpStatus), report) == nil {
				tr.Bgp = report
			}
		}
		resp.BytesIn += t.BytesIn
		resp.BytesOut += t.BytesOut
		resp.Tunnels = append(resp.Tunnels, tr)
	}
	return resp
}

// tunnelParamsOf converts the tunnel list of a payload; the single-tunnel short form becomes one entry
func tunnelParamsOf(tunnels []*VpnTunnelPayload, remoteGateway, remoteID, tunnelLocalIP, tunnelPeerIP string) []*services.VpnTunnelParams {
	if len(tunnels) == 0 {
		return []*services.VpnTunnelParams{{RemoteGateway: remoteGateway, RemoteID: remoteID, TunnelLocalIP: tunnelLocalIP, TunnelPeerIP: tunnelPeerIP}}
	}
	params := []*services.VpnTunnelParams{}
	for _, t := range tunnels {
		p := &services.VpnTunnelParams{
			Endpoint: t.Endpoint, Priority: t.Priority, RemoteGateway: t.RemoteGateway, RemoteID: t.RemoteID,
			TunnelLocalIP: t.TunnelLocalIP, TunnelPeerIP: t.TunnelPeerIP,
		}
		if t.Psk != nil {
			p.Psk, p.PskSet = *t.Psk, true
		}
		params = append(params, p)
	}
	return params
}

// currentTunnelParams is the stored tunnel list of a connection as parameters (keys kept as they are)
func currentTunnelParams(conn *model.VpnConnection) []*services.VpnTunnelParams {
	params := []*services.VpnTunnelParams{}
	for _, t := range conn.Tunnels {
		params = append(params, &services.VpnTunnelParams{
			Endpoint: t.Endpoint, Priority: t.Priority, RemoteGateway: t.RemoteGateway, RemoteID: t.RemoteID,
			TunnelLocalIP: t.TunnelLocalIP, TunnelPeerIP: t.TunnelPeerIP,
		})
	}
	return params
}

func (v *VpnConnectionAPI) validatePayload(p *VpnConnectionParamsView) error {
	if p.IkeProposal != "" && !validateProposal(p.IkeProposal) {
		return NewCLError(ErrInvalidParameter, "ike_proposal is not a supported proposal list", nil)
	}
	if p.EspProposal != "" && !validateProposal(p.EspProposal) {
		return NewCLError(ErrInvalidParameter, "esp_proposal is not a supported proposal list", nil)
	}
	for _, t := range p.Tunnels {
		if t.RemoteID != "" && !vpnIkeIDPattern.MatchString(t.RemoteID) {
			return NewCLError(ErrInvalidParameter, "remote_id contains unsupported characters", nil)
		}
		if t.PskSet && t.Psk != "" && !validatePsk(t.Psk) {
			return NewCLError(ErrInvalidParameter, "psk must be 8-128 printable ASCII characters without quotes or backslashes", nil)
		}
	}
	if p.LocalID != "" && !vpnIkeIDPattern.MatchString(p.LocalID) {
		return NewCLError(ErrInvalidParameter, "local_id contains unsupported characters", nil)
	}
	if p.PskSet && !validatePsk(p.Psk) {
		return NewCLError(ErrInvalidParameter, "psk must be 8-128 printable ASCII characters without quotes or backslashes", nil)
	}
	if p.BgpPasswordSet && p.BgpPassword != "" && !vpnBgpPassPattern.MatchString(p.BgpPassword) {
		return NewCLError(ErrInvalidParameter, "bgp_password may only contain letters, digits, '.', '_' and '-'", nil)
	}
	return nil
}

// VpnConnectionParamsView is the service parameter set; alias kept local for the validator
type VpnConnectionParamsView = services.VpnConnectionParams

// @Summary list VPN connections
// @Description list the site-to-site connections of a VPN gateway; psk and bgp_password are included for members with write permission
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} VpnConnectionListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/connections [get]
func (v *VpnConnectionAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	resp := &VpnConnectionListResponse{Total: len(gateway.Connections), Connections: []*VpnConnectionResponse{}}
	view := newVpnConnView(ctx, gateway)
	for _, conn := range gateway.Connections {
		resp.Connections = append(resp.Connections, v.responseIn(ctx, gateway, conn, view))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a VPN connection
// @Description get a site-to-site connection; psk and bgp_password are included for members with write permission
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} VpnConnectionResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/connections/{conn_id} [get]
func (v *VpnConnectionAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	conn, err := vpnConnectionAdmin.Get(ctx, gateway, c.Param("conn_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN connection query", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(ctx, gateway, conn))
}

// @Summary create a VPN connection
// @Description create a site-to-site IPsec connection (static or BGP routed)
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   message	body   VpnConnectionPayload  true   "VPN connection create payload"
// @Success 200 {object} VpnConnectionResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/connections [post]
func (v *VpnConnectionAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	payload := &VpnConnectionPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	params := &services.VpnConnectionParams{
		Name: payload.Name, Description: payload.Description, LocalID: payload.LocalID,
		RouteMode: payload.RouteMode, LocalCidrs: payload.LocalCidrs, RemoteCidrs: payload.RemoteCidrs, RemoteSummaryCidrs: payload.RemoteSummaryCidrs,
		MaxPrefixes: payload.MaxPrefixes, LocalAsn: payload.LocalAsn, PeerAsn: payload.PeerAsn,
		BgpKeepalive: payload.BgpKeepalive, BgpHold: payload.BgpHold,
		IkeProposal: payload.IkeProposal, EspProposal: payload.EspProposal, IkeLifetime: payload.IkeLifetime, EspLifetime: payload.EspLifetime,
		DpdAction: payload.DpdAction, DpdDelay: payload.DpdDelay, Initiator: true,
		AsPathPrepend: services.VpnDefaultAsPrepend, BfdInterval: payload.BfdInterval, BfdMultiplier: payload.BfdMultiplier,
		TrafficPolicy: payload.TrafficPolicy,
		Tunnels:       tunnelParamsOf(payload.Tunnels, payload.RemoteGateway, payload.RemoteID, payload.TunnelLocalIP, payload.TunnelPeerIP),
	}
	if payload.Initiator != nil {
		params.Initiator = *payload.Initiator
	}
	if payload.AsPathPrepend != nil {
		params.AsPathPrepend = *payload.AsPathPrepend
	}
	if payload.BfdEnabled != nil {
		params.BfdEnabled = *payload.BfdEnabled
	}
	if payload.Psk != nil {
		params.Psk, params.PskSet = *payload.Psk, true
	}
	if payload.BgpPassword != nil {
		params.BgpPassword, params.BgpPasswordSet = *payload.BgpPassword, true
	}
	if !params.PskSet {
		ErrorResponse(c, http.StatusBadRequest, "psk is required", nil)
		return
	}
	if err = v.validatePayload(params); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid connection parameters", err)
		return
	}
	conn, err := vpnConnectionAdmin.Create(ctx, gateway, params)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(ctx, gateway, conn))
}

// @Summary patch a VPN connection
// @Description patch a site-to-site connection; changed fields are pushed to the gateway
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   message	body   VpnConnectionPatchPayload  true   "VPN connection patch payload"
// @Success 200 {object} VpnConnectionResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/connections/{conn_id} [patch]
func (v *VpnConnectionAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	conn, err := vpnConnectionAdmin.Get(ctx, gateway, c.Param("conn_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN connection query", err)
		return
	}
	payload := &VpnConnectionPatchPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	// Start from the stored values so an omitted field keeps what it has
	params := &services.VpnConnectionParams{
		Name: conn.Name, Description: conn.Description, LocalID: conn.LocalID,
		RouteMode: conn.RouteMode, LocalCidrs: conn.LocalCidrs, RemoteCidrs: conn.RemoteCidrs, RemoteSummaryCidrs: conn.RemoteSummaryCidrs,
		MaxPrefixes: conn.MaxPrefixes, LocalAsn: conn.LocalAsn, PeerAsn: conn.PeerAsn,
		BgpKeepalive: conn.BgpKeepalive, BgpHold: conn.BgpHold,
		IkeProposal: conn.IkeProposal, EspProposal: conn.EspProposal, IkeLifetime: conn.IkeLifetime, EspLifetime: conn.EspLifetime,
		DpdAction: conn.DpdAction, DpdDelay: conn.DpdDelay, Initiator: conn.Initiator,
		AsPathPrepend: conn.AsPathPrepend, BfdEnabled: conn.BfdEnabled, BfdInterval: conn.BfdInterval, BfdMultiplier: conn.BfdMultiplier,
		TrafficPolicy: services.VpnTrafficPolicyOf(conn), Tunnels: currentTunnelParams(conn),
	}
	setStr := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setI32 := func(dst *int32, src *int32) {
		if src != nil {
			*dst = *src
		}
	}
	setStr(&params.Name, payload.Name)
	setStr(&params.Description, payload.Description)
	// Leaving ecmp: every tunnel was primary, the defaults pick one again unless the tunnels say otherwise
	if payload.TrafficPolicy != nil && *payload.TrafficPolicy != params.TrafficPolicy && *payload.TrafficPolicy == model.VpnTrafficPolicyPreferred {
		for _, t := range params.Tunnels {
			t.Priority = ""
		}
	}
	setStr(&params.TrafficPolicy, payload.TrafficPolicy)
	if payload.Tunnels != nil {
		params.Tunnels = tunnelParamsOf(payload.Tunnels, "", "", "", "")
	} else if len(params.Tunnels) > 0 {
		// short form: the first tunnel
		first := params.Tunnels[0]
		setStr(&first.RemoteGateway, payload.RemoteGateway)
		setStr(&first.RemoteID, payload.RemoteID)
		setStr(&first.TunnelLocalIP, payload.TunnelLocalIP)
		setStr(&first.TunnelPeerIP, payload.TunnelPeerIP)
	}
	setStr(&params.LocalID, payload.LocalID)
	setStr(&params.RouteMode, payload.RouteMode)
	setStr(&params.LocalCidrs, payload.LocalCidrs)
	setStr(&params.RemoteCidrs, payload.RemoteCidrs)
	setStr(&params.RemoteSummaryCidrs, payload.RemoteSummaryCidrs)
	setStr(&params.IkeProposal, payload.IkeProposal)
	setStr(&params.EspProposal, payload.EspProposal)
	setStr(&params.DpdAction, payload.DpdAction)
	setI32(&params.MaxPrefixes, payload.MaxPrefixes)
	setI32(&params.BgpKeepalive, payload.BgpKeepalive)
	setI32(&params.BgpHold, payload.BgpHold)
	setI32(&params.IkeLifetime, payload.IkeLifetime)
	setI32(&params.EspLifetime, payload.EspLifetime)
	setI32(&params.DpdDelay, payload.DpdDelay)
	setI32(&params.AsPathPrepend, payload.AsPathPrepend)
	setI32(&params.BfdInterval, payload.BfdInterval)
	setI32(&params.BfdMultiplier, payload.BfdMultiplier)
	if payload.BfdEnabled != nil {
		params.BfdEnabled = *payload.BfdEnabled
	}
	if payload.LocalAsn != nil {
		params.LocalAsn = *payload.LocalAsn
	}
	if payload.PeerAsn != nil {
		params.PeerAsn = *payload.PeerAsn
	}
	if payload.Initiator != nil {
		params.Initiator = *payload.Initiator
	}
	if payload.Psk != nil {
		params.Psk, params.PskSet = *payload.Psk, true
	}
	if payload.BgpPassword != nil {
		params.BgpPassword, params.BgpPasswordSet = *payload.BgpPassword, true
	}
	for _, t := range params.Tunnels {
		if t.RemoteGateway != "" {
			if ip := net.ParseIP(t.RemoteGateway); ip == nil || ip.To4() == nil {
				ErrorResponse(c, http.StatusBadRequest, "remote_gateway must be an IPv4 address", nil)
				return
			}
		}
	}
	if err = v.validatePayload(params); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid connection parameters", err)
		return
	}
	conn, err = vpnConnectionAdmin.Update(ctx, gateway, conn, params)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Patch VPN connection failed", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(ctx, gateway, conn))
}

// @Summary delete a VPN connection
// @Description delete a site-to-site connection
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/connections/{conn_id} [delete]
func (v *VpnConnectionAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	conn, err := vpnConnectionAdmin.Get(ctx, gateway, c.Param("conn_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN connection query", err)
		return
	}
	if err = vpnConnectionAdmin.Delete(ctx, gateway, conn); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary restart a VPN connection
// @Description terminate and re-initiate the IKE SAs of a connection: every tunnel, or only the one in the given slot
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   tunnel query int false "Slot of the tunnel to restart (1 to 4); all tunnels when omitted"
// @Success 200 {object} VpnConnectionResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/connections/{conn_id}/restart [post]
func (v *VpnConnectionAPI) Restart(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	conn, err := vpnConnectionAdmin.Get(ctx, gateway, c.Param("conn_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN connection query", err)
		return
	}
	slot := int32(0)
	if value := c.Query("tunnel"); value != "" {
		n, perr := strconv.Atoi(value)
		if perr != nil || n < 1 || n > 4 {
			ErrorResponse(c, http.StatusBadRequest, "tunnel must be a slot, 1 to 4", nil)
			return
		}
		slot = int32(n)
	}
	if err = vpnConnectionAdmin.Restart(ctx, gateway, conn, slot); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to restart", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(ctx, gateway, conn))
}

func (v *VpnClientAPI) getResponse(client *model.VpnClient) *VpnClientResponse {
	return &VpnClientResponse{
		ResourceReference: &ResourceReference{ID: client.UUID, Name: client.Name, CreatedAt: client.CreatedAt.Format(TimeStringForMat), UpdatedAt: client.UpdatedAt.Format(TimeStringForMat)},
		Description:       client.Description, Protocol: client.Protocol, IPAddress: client.IPAddress, PublicKey: client.PublicKey,
		PresharedKeySet: client.PresharedKey != "", Enabled: client.Enabled, LastHandshakeAt: formatTimePtr(client.LastHandshakeAt),
		BytesIn: client.BytesIn, BytesOut: client.BytesOut,
	}
}

// @Summary list VPN clients
// @Description list the WireGuard clients of a VPN gateway
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} VpnClientListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/clients [get]
func (v *VpnClientAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	resp := &VpnClientListResponse{Total: len(gateway.Clients), Clients: []*VpnClientResponse{}}
	for _, client := range gateway.Clients {
		resp.Clients = append(resp.Clients, v.getResponse(client))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a VPN client
// @Description get a WireGuard client (never includes a private key)
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} VpnClientResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/clients/{client_id} [get]
func (v *VpnClientAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	client, err := vpnClientAdmin.Get(ctx, gateway, c.Param("client_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN client query", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(client))
}

// @Summary create a VPN client
// @Description create a WireGuard client; the response carries the generated private key and full config exactly once
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   message	body   VpnClientPayload  true   "VPN client create payload"
// @Success 200 {object} VpnClientCreateResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/clients [post]
func (v *VpnClientAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	payload := &VpnClientPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	if payload.PublicKey != "" && !vpnWgKeyPattern.MatchString(payload.PublicKey) {
		ErrorResponse(c, http.StatusBadRequest, "public_key is not a WireGuard key", nil)
		return
	}
	params := &services.VpnClientParams{Name: payload.Name, Description: payload.Description, PublicKey: payload.PublicKey, PresharedKey: payload.PresharedKey, Enabled: true}
	if payload.Enabled != nil {
		params.Enabled = *payload.Enabled
	}
	client, privateKey, config, err := vpnClientAdmin.Create(ctx, gateway, params)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	c.JSON(http.StatusOK, &VpnClientCreateResponse{VpnClientResponse: v.getResponse(client), PrivateKey: privateKey, Config: config})
}

// @Summary patch a VPN client
// @Description rename, describe or enable/disable a WireGuard client
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   message	body   VpnClientPatchPayload  true   "VPN client patch payload"
// @Success 200 {object} VpnClientResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/clients/{client_id} [patch]
func (v *VpnClientAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	client, err := vpnClientAdmin.Get(ctx, gateway, c.Param("client_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN client query", err)
		return
	}
	payload := &VpnClientPatchPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	client, err = vpnClientAdmin.Update(ctx, gateway, client, payload.Name, payload.Description, payload.Enabled)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Patch VPN client failed", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(client))
}

// @Summary delete a VPN client
// @Description delete a WireGuard client and release its address
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/clients/{client_id} [delete]
func (v *VpnClientAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	client, err := vpnClientAdmin.Get(ctx, gateway, c.Param("client_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN client query", err)
		return
	}
	if err = vpnClientAdmin.Delete(ctx, gateway, client); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary get a VPN client configuration template
// @Description WireGuard configuration of a client without the private key; the preshared key is filled in for members with write permission
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} VpnClientConfigResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/clients/{client_id}/config [get]
func (v *VpnClientAPI) Config(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	client, err := vpnClientAdmin.Get(ctx, gateway, c.Param("client_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN client query", err)
		return
	}
	config, err := vpnClientAdmin.Config(ctx, gateway, client)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to render the configuration", err)
		return
	}
	c.JSON(http.StatusOK, &VpnClientConfigResponse{Config: config})
}

// vpnPublicIpInfos lists the public addresses of a gateway in endpoint order, with the node of a fixed one
func vpnPublicIpInfos(ctx context.Context, gateway *model.VpnGateway) []*VpnPublicIpInfo {
	infos := []*VpnPublicIpInfo{}
	endpoints := services.VpnEndpointAddresses(gateway)
	hosts := services.VpnEndpointHostsOf(ctx, gateway)
	for _, endpoint := range []string{model.VpnEndpointVip1, model.VpnEndpointVip2, model.VpnEndpointNode1, model.VpnEndpointNode2} {
		address := endpoints[endpoint]
		if address == "" {
			continue
		}
		info := &VpnPublicIpInfo{Endpoint: endpoint, Address: address, Hostid: -1}
		if host, ok := hosts[endpoint]; ok {
			info.Hostid, info.Hostname = host, hyperHostname(ctx, host)
		}
		infos = append(infos, info)
	}
	return infos
}

func (v *VpnGatewayAPI) publicIpResponse(ctx context.Context, gateway *model.VpnGateway, endpoint string) (*VpnPublicIpResponse, bool) {
	for _, info := range vpnPublicIpInfos(ctx, gateway) {
		if info.Endpoint == endpoint {
			return &VpnPublicIpResponse{VpnPublicIpInfo: info, OwnerUUID: orgAdmin.GetOrgUUID(ctx, gateway.Owner)}, true
		}
	}
	return nil, false
}

// @Summary get a public address of a VPN gateway
// @Description get one public address of a VPN gateway by endpoint (vip1, vip2, node1, node2)
// @tags VPN Gateway
// @Produce json
// @Success 200 {object} VpnPublicIpResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/public_ips/{endpoint} [get]
func (v *VpnGatewayAPI) GetPublicIp(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	resp, ok := v.publicIpResponse(ctx, gateway, c.Param("endpoint"))
	if !ok {
		ErrorResponse(c, http.StatusNotFound, "The gateway has no such public address", nil)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary add a public address to a VPN gateway
// @Description give an active-standby gateway its second floating IP (vip2), or an active-active gateway the floating IP of its client VPN (vip1); see addable_endpoint of the gateway
// @tags VPN Gateway
// @Accept  json
// @Produce json
// @Param   message	body   VpnPublicIpPayload  true   "public subnet and / or address, both optional"
// @Success 200 {object} VpnGatewayResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/public_ips [post]
func (v *VpnGatewayAPI) AddPublicIp(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	payload := &VpnPublicIpPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	params := &services.VpnPublicIpParams{Address: payload.PublicIp}
	if payload.PublicSubnet != nil {
		subnet, serr := subnetAdmin.GetSubnet(ctx, payload.PublicSubnet)
		if serr != nil {
			ErrorResponse(c, http.StatusBadRequest, "Failed to get public subnet", serr)
			return
		}
		params.Subnets = append(params.Subnets, subnet)
	}
	if gateway, err = vpnGatewayAdmin.AddPublicIp(ctx, gateway, params); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to add the public address", err)
		return
	}
	resp, err := v.getResponse(ctx, gateway, true)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary remove a public address from a VPN gateway
// @Description release the second floating IP of an active-standby gateway (no tunnel may use it) or the client VPN floating IP of an active-active gateway (client VPN disabled)
// @tags VPN Gateway
// @Produce json
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/public_ips/{endpoint} [delete]
func (v *VpnGatewayAPI) RemovePublicIp(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	if _, err = vpnGatewayAdmin.RemovePublicIp(ctx, gateway, c.Param("endpoint")); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to remove the public address", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}
