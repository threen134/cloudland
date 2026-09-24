/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"time"

	"api/src/dbs"

	"gorm.io/gorm"
)

const (
	VpnGatewayStatusPending   = "pending"
	VpnGatewayStatusAvailable = "available"
	VpnGatewayStatusError     = "error"
	VpnGatewayStatusDeleting  = "deleting"

	VpnConnectionStatusPending = "pending"
	VpnConnectionStatusDown    = "down"
	VpnConnectionStatusUp      = "up"
	VpnConnectionStatusError   = "error"
	// The gateway is disabled: no tunnel runs and the node does not report
	VpnConnectionStatusDisabled = "disabled"

	VpnRouteModeStatic = "static"
	VpnRouteModeBgp    = "bgp"

	VpnClientProtocolWireguard = "wireguard"

	// Sources of a derived remote prefix (see VpnRemotePrefix)
	VpnPrefixSourceConnectionStatic  = "connection_static"
	VpnPrefixSourceConnectionSummary = "connection_summary"
	VpnPrefixSourceClientPool        = "client_pool"
)

// VpnGateway is the per-VPC VPN endpoint: a keepalived VRRP pair in the VPC router netns holding one
// public floating IP, with strongSwan for site-to-site IPsec connections and WireGuard for clients.
// One gateway per VPC is enforced in the service (the unique index covers name + router so a deleted
// gateway, which stays in the table with a renamed name, does not block a new one).
type VpnGateway struct {
	Model
	Owner          int64         `gorm:"default:1"` /* The organization ID of the resource */
	Name           string        `gorm:"uniqueIndex:idx_router_vpn;type:varchar(64)"`
	Description    string        `gorm:"type:varchar(255)"`
	Status         string        `gorm:"type:varchar(32)"`
	RouterID       int64         `gorm:"uniqueIndex:idx_router_vpn"`
	Router         *Router       `gorm:"foreignkey:RouterID"`
	VrrpInstanceID int64         `gorm:"index"`
	VrrpInstance   *VrrpInstance `gorm:"foreignkey:VrrpInstanceID"`
	ZoneID         int64
	FloatingIps    []*FloatingIp `gorm:"foreignkey:VpnGatewayID"`
	IpsecEnabled   bool
	ClientEnabled  bool
	// Disabled pauses the whole gateway: tunnels, BGP and WireGuard stop, the VRRP pair, the public IP and
	// every configuration stay. Stored inverted so that the zero value (and existing rows) mean enabled
	Disabled         bool   `gorm:"not null;default:false"`
	ClientProtocol   string `gorm:"type:varchar(16)"`
	ClientCidr       string `gorm:"type:varchar(64)"`
	ClientPort       int32
	ClientPrivateKey string `gorm:"type:text"` /* encrypted, never returned by the API */
	ClientPublicKey  string `gorm:"type:varchar(64)"`
	ClientDns        string `gorm:"type:varchar(128)"`
	ClientRoutes     string `gorm:"type:varchar(512)"` /* empty means "all internal subnets of the VPC", computed when a client config is generated */
	MasterHyper      int32  `gorm:"default:-1"`        /* node currently holding the floating IP, reported by the heartbeat */
	MasterReportedAt *time.Time
	Connections      []*VpnConnection `gorm:"foreignkey:VpnGatewayID"`
	Clients          []*VpnClient     `gorm:"foreignkey:VpnGatewayID"`
	OwnerInfo        *Organization    `gorm:"-"` /* Transient: populated for SystemAdmin list view */
}

// VpnConnection is one site-to-site IPsec (IKEv2) connection of a gateway. Static mode gives the traffic
// selectors exactly; BGP mode opens them to 0.0.0.0/0 and learns prefixes over eBGP inside the tunnel,
// bounded by RemoteSummaryCidrs (static route on the other nodes, nonat entry, and inbound prefix-list).
type VpnConnection struct {
	Model
	Owner              int64       `gorm:"default:1"`
	Name               string      `gorm:"uniqueIndex:idx_vpn_conn;type:varchar(64)"`
	Description        string      `gorm:"type:varchar(255)"`
	VpnGatewayID       int64       `gorm:"uniqueIndex:idx_vpn_conn"`
	VpnGateway         *VpnGateway `gorm:"foreignkey:VpnGatewayID"`
	Status             string      `gorm:"type:varchar(32)"`
	RemoteGateway      string      `gorm:"type:varchar(64)"` /* peer public address; empty = responder only (%any) */
	RemoteID           string      `gorm:"type:varchar(128)"`
	LocalID            string      `gorm:"type:varchar(128)"`
	RouteMode          string      `gorm:"type:varchar(16)"`
	LocalCidrs         string      `gorm:"type:varchar(512)"` /* empty means "all internal subnets of the VPC", computed at dispatch time */
	RemoteCidrs        string      `gorm:"type:varchar(512)"`
	RemoteSummaryCidrs string      `gorm:"type:varchar(512)"`
	MaxPrefixes        int32
	LocalAsn           int64
	PeerAsn            int64
	TunnelLocalIP      string `gorm:"type:varchar(64)"`
	TunnelPeerIP       string `gorm:"type:varchar(64)"`
	BgpPassword        string `gorm:"type:text"` /* encrypted */
	BgpKeepalive       int32
	BgpHold            int32
	AuthMethod         string `gorm:"type:varchar(16)"`
	Psk                string `gorm:"type:text"` /* encrypted, never returned by the API */
	IkeVersion         int32
	IkeProposal        string `gorm:"type:varchar(128)"`
	EspProposal        string `gorm:"type:varchar(128)"`
	IkeLifetime        int32
	EspLifetime        int32
	DpdAction          string `gorm:"type:varchar(16)"`
	DpdDelay           int32
	Initiator          bool
	IfID               int32 /* XFRM interface id, unique within the gateway */
	EstablishedAt      *time.Time
	BytesIn            int64
	BytesOut           int64
	LastError          string `gorm:"type:varchar(512)"`
	BgpState           string `gorm:"type:varchar(32)"`
	BgpStatus          string `gorm:"type:text"` /* JSON snapshot reported by the master node, display only */
	BgpReportedAt      *time.Time
}

// VpnClient is a WireGuard peer of a gateway. Only the public key is stored: a key pair generated by the
// platform is returned once in the create response and forgotten.
type VpnClient struct {
	Model
	Owner           int64  `gorm:"default:1"`
	Name            string `gorm:"uniqueIndex:idx_vpn_client;type:varchar(64)"`
	Description     string `gorm:"type:varchar(255)"`
	VpnGatewayID    int64  `gorm:"uniqueIndex:idx_vpn_client"`
	Protocol        string `gorm:"type:varchar(16)"`
	IPAddress       string `gorm:"type:varchar(64)"` /* /32 from the client pool, released on delete */
	PublicKey       string `gorm:"type:varchar(64)"`
	PresharedKey    string `gorm:"type:text"` /* encrypted, optional */
	Enabled         bool
	LastHandshakeAt *time.Time
	BytesIn         int64
	BytesOut        int64
}

// VpnRemotePrefix is the derived set of networks a gateway makes reachable through the whole VPC:
// static connection remote networks, BGP connection summaries and the client pool. It only changes with
// the API, never with BGP convergence, and is the single source for the per-node routes, the nonat
// entries and the overlap checks. Not soft-deleted: rows are replaced when the set is recomputed.
type VpnRemotePrefix struct {
	ID           int64  `gorm:"primaryKey"`
	VpnGatewayID int64  `gorm:"index"`
	Cidr         string `gorm:"type:varchar(64)"`
	Source       string `gorm:"type:varchar(24)"`
	RefID        int64
	CreatedAt    time.Time
}

func init() {
	dbs.AutoMigrate(&VpnGateway{})
	dbs.AutoMigrate(&VpnConnection{})
	dbs.AutoMigrate(&VpnClient{})
	dbs.AutoMigrate(&VpnRemotePrefix{})
	// VPN clients must never reach remote sites: the option that advertised the client pool is gone
	dbs.AutoUpgrade("vpn_connections_drop_advertise_client_cidr_v1", func(db *gorm.DB) error {
		return db.Exec("ALTER TABLE vpn_connections DROP COLUMN IF EXISTS advertise_client_cidr").Error
	})
}
