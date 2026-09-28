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
	// Only a standby tunnel of the connection is up
	VpnConnectionStatusDegraded = "degraded"
	// The gateway is disabled: no tunnel runs and the node does not report
	VpnConnectionStatusDisabled = "disabled"

	VpnRouteModeStatic = "static"
	VpnRouteModeBgp    = "bgp"

	VpnClientProtocolWireguard = "wireguard"

	// Sources of a derived remote prefix (see VpnRemotePrefix)
	VpnPrefixSourceConnectionStatic  = "connection_static"
	VpnPrefixSourceConnectionSummary = "connection_summary"
	VpnPrefixSourceClientPool        = "client_pool"

	// How the two VRRP nodes of a gateway share the work, chosen at creation (VpnGateway.HaMode).
	// active_standby: the node holding the floating IPs runs every tunnel; active_active: each node runs
	// the tunnels of its own fixed public address and both carry traffic, exchanging routes over iBGP
	VpnHaModeActiveStandby = "active_standby"
	VpnHaModeActiveActive  = "active_active"

	// Public addresses of a gateway (FloatingIp.VpnEndpoint). vip1 / vip2 are floating IPs held by
	// keepalived on the master (active_standby; an active_active gateway only has vip1, for its client
	// VPN). node1 / node2 are the fixed addresses of the two VRRP nodes of an active_active gateway: node1
	// sits on the node of the MASTER VRRP interface, node2 on the BACKUP one
	VpnEndpointVip1  = "vip1"
	VpnEndpointVip2  = "vip2"
	VpnEndpointNode1 = "node1"
	VpnEndpointNode2 = "node2"

	VpnTunnelPriorityPrimary = "primary"
	VpnTunnelPriorityStandby = "standby"

	// How a connection uses its tunnels (VpnConnection.TrafficPolicy). preferred: the primary carries the
	// traffic, the others stand by in order; ecmp: every tunnel that is up carries a share (flows are
	// hashed on the addresses and ports), and the peer should do the same
	VpnTrafficPolicyPreferred = "preferred"
	VpnTrafficPolicyEcmp      = "ecmp"
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
	StatusReason   string        `gorm:"type:varchar(255)"` /* why the gateway is in error; empty otherwise */
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
	// VpnHaModeActiveStandby (empty on older rows) or VpnHaModeActiveActive; fixed at creation
	HaMode           string `gorm:"type:varchar(16)"`
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
	Status             string      `gorm:"type:varchar(32)"` /* aggregate of the tunnels, see VpnConnectionStatusDegraded */
	LocalID            string      `gorm:"type:varchar(128)"`
	RouteMode          string      `gorm:"type:varchar(16)"`
	LocalCidrs         string      `gorm:"type:varchar(512)"` /* empty means "all internal subnets of the VPC", computed at dispatch time */
	RemoteCidrs        string      `gorm:"type:varchar(512)"`
	RemoteSummaryCidrs string      `gorm:"type:varchar(512)"`
	MaxPrefixes        int32
	LocalAsn           int64
	PeerAsn            int64
	BgpPassword        string `gorm:"type:text"` /* encrypted */
	BgpKeepalive       int32
	BgpHold            int32
	AuthMethod         string `gorm:"type:varchar(16)"`
	Psk                string `gorm:"type:text"` /* encrypted, returned only to members with write permission */
	IkeVersion         int32
	IkeProposal        string `gorm:"type:varchar(128)"`
	EspProposal        string `gorm:"type:varchar(128)"`
	IkeLifetime        int32
	EspLifetime        int32
	DpdAction          string `gorm:"type:varchar(16)"`
	DpdDelay           int32
	Initiator          bool
	// BGP only. The standby tunnel advertises the local networks with the local ASN prepended this many
	// times so that the peer prefers the primary for its return traffic; BFD watches every BGP session
	AsPathPrepend int32
	BfdEnabled    bool
	BfdInterval   int32 /* milliseconds, transmit and receive */
	BfdMultiplier int32
	// VpnTrafficPolicyPreferred (empty on older rows) or VpnTrafficPolicyEcmp
	TrafficPolicy string       `gorm:"type:varchar(16)"`
	Tunnels       []*VpnTunnel `gorm:"foreignkey:VpnConnectionID"`
}

// VpnTunnel is one IKE / IPsec SA pair of a connection, between one public address of the gateway and one
// address of the peer. A connection has one tunnel, or up to four to survive the loss of a peer device, a
// peer line or one tunnel: an active_standby gateway runs them all on its master (two local addresses
// times two peer addresses at most), an active_active one runs each on the node of its endpoint (one per
// node). Each tunnel has its own XFRM interface ipsec-<if_id> and, in BGP mode, its own link addresses
// and BGP session.
type VpnTunnel struct {
	Model
	Owner           int64  `gorm:"default:1"`
	VpnGatewayID    int64  `gorm:"index"`
	VpnConnectionID int64  `gorm:"index"`
	Slot            int32  /* 1 to 4, the position in the connection's tunnel list */
	Endpoint        string `gorm:"type:varchar(16)"` /* VpnEndpointVip1 / Vip2 / Node1 / Node2 */
	Priority        string `gorm:"type:varchar(16)"` /* VpnTunnelPriorityPrimary / VpnTunnelPriorityStandby */
	RemoteGateway   string `gorm:"type:varchar(64)"` /* peer public address; empty = responder only (%any) */
	RemoteID        string `gorm:"type:varchar(128)"`
	Psk             string `gorm:"type:text"` /* encrypted, optional: empty uses the connection's */
	TunnelLocalIP   string `gorm:"type:varchar(64)"`
	TunnelPeerIP    string `gorm:"type:varchar(64)"`
	IfID            int32  /* XFRM interface id: the tunnel id, unique for ever */
	Status          string `gorm:"type:varchar(32)"`
	EstablishedAt   *time.Time
	BytesIn         int64
	BytesOut        int64
	LastError       string `gorm:"type:varchar(512)"`
	BgpState        string `gorm:"type:varchar(32)"`
	BgpStatus       string `gorm:"type:text"` /* JSON snapshot reported by the master node, display only */
	BgpReportedAt   *time.Time
	BfdState        string `gorm:"type:varchar(32)"`
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
	dbs.AutoMigrate(&VpnTunnel{})
	dbs.AutoMigrate(&VpnClient{})
	dbs.AutoMigrate(&VpnRemotePrefix{})
	// VPN clients must never reach remote sites: the option that advertised the client pool is gone
	dbs.AutoUpgrade("vpn_connections_drop_advertise_client_cidr_v1", func(db *gorm.DB) error {
		return db.Exec("ALTER TABLE vpn_connections DROP COLUMN IF EXISTS advertise_client_cidr").Error
	})
	// Connections created before tunnels existed carried their only tunnel in their own columns: move it to
	// a tunnel row (slot 1, primary, the first public address) and drop the columns. The interface id stays,
	// so the running XFRM interface and its counters are kept; tunnel ids then continue above every
	// interface id in use, because the interface id of a new tunnel is its own id.
	dbs.AutoUpgrade("vpn_tunnels_from_connections_v1", func(db *gorm.DB) error {
		var n int64
		if err := db.Raw("SELECT count(*) FROM information_schema.columns WHERE table_name = 'vpn_connections' AND column_name = 'if_id'").Scan(&n).Error; err != nil || n == 0 {
			return err
		}
		return db.Transaction(func(tx *gorm.DB) error {
			// Deleted connections keep their history: their tunnel is deleted along with them
			if err := tx.Exec(`INSERT INTO vpn_tunnels (created_at, updated_at, deleted_at, uuid, creater, owner, vpn_gateway_id, vpn_connection_id, slot,
				endpoint, priority, remote_gateway, remote_id, psk, tunnel_local_ip, tunnel_peer_ip, if_id, status, established_at,
				bytes_in, bytes_out, last_error, bgp_state, bgp_status, bgp_reported_at, bfd_state)
				SELECT c.created_at, now(), c.deleted_at, gen_random_uuid()::text, c.creater, c.owner, c.vpn_gateway_id, c.id, 1,
				'vip1', 'primary', COALESCE(c.remote_gateway, ''), COALESCE(c.remote_id, ''), '', COALESCE(c.tunnel_local_ip, ''),
				COALESCE(c.tunnel_peer_ip, ''), c.if_id, c.status, c.established_at, COALESCE(c.bytes_in, 0), COALESCE(c.bytes_out, 0),
				COALESCE(c.last_error, ''), COALESCE(c.bgp_state, ''), COALESCE(c.bgp_status, ''), c.bgp_reported_at, ''
				FROM vpn_connections c
				WHERE NOT EXISTS (SELECT 1 FROM vpn_tunnels t WHERE t.vpn_connection_id = c.id)`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`SELECT setval(pg_get_serial_sequence('vpn_tunnels', 'id'),
				GREATEST((SELECT COALESCE(MAX(id), 0) FROM vpn_tunnels), (SELECT COALESCE(MAX(if_id), 0) FROM vpn_tunnels), 1))`).Error; err != nil {
				return err
			}
			for _, col := range []string{"remote_gateway", "remote_id", "tunnel_local_ip", "tunnel_peer_ip", "if_id", "established_at",
				"bytes_in", "bytes_out", "last_error", "bgp_state", "bgp_status", "bgp_reported_at"} {
				if err := tx.Exec("ALTER TABLE vpn_connections DROP COLUMN IF EXISTS " + col).Error; err != nil {
					return err
				}
			}
			return nil
		})
	})
}
