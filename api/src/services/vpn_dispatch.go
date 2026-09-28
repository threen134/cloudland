/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/model"

	"github.com/apparentlymart/go-cidr/cidr"
)

const vpnScriptDir = "/opt/cloudland/scripts/backend/"

// Payloads handed to the node scripts on stdin. The scripts are declarative: each call carries the whole
// desired state of one aspect (all connections, all peers, all routes) and the node reconciles.

// vpnPublicAddress is one public address of the gateway on the nodes. Host is the node a fixed address
// (node1 / node2) lives on, -1 for a floating IP (vip1 / vip2) that keepalived moves.
type vpnPublicAddress struct {
	Endpoint string `json:"endpoint"`
	Host     int32  `json:"host"`
	*LoadBalancerFloatingIp
}

type vpnGatewayConfig struct {
	HaMode        string                  `json:"ha_mode"`
	Vrid          int                     `json:"vrid"`
	FloatingIp    *LoadBalancerFloatingIp `json:"floating_ip"`  // vip1; an active_active gateway without client VPN has none
	FloatingIps   []*vpnPublicAddress     `json:"floating_ips"` // every address, vip1 first
	IpsecEnabled  bool                    `json:"ipsec_enabled"`
	ClientEnabled bool                    `json:"client_enabled"`
	ClientPort    int32                   `json:"client_port"`
	ClientCidr    string                  `json:"client_cidr"`
	WgAddress     string                  `json:"wg_address"`
	WgPrivateKey  string                  `json:"wg_private_key"`
	Enabled       bool                    `json:"enabled"`
}

// vpnIpsecConnection is one tunnel: a swanctl connection with its own XFRM interface. Host is the node
// that runs it on an active_active gateway (the others skip it), -1 on an active_standby one (the master)
type vpnIpsecConnection struct {
	Name          string   `json:"name"`
	Host          int32    `json:"host"`
	IfID          int32    `json:"if_id"`
	LocalAddr     string   `json:"local_addr"`
	RemoteGateway string   `json:"remote_gateway"`
	RemoteID      string   `json:"remote_id"`
	LocalID       string   `json:"local_id"`
	RouteMode     string   `json:"route_mode"`
	LocalCidrs    []string `json:"local_cidrs"`
	RemoteCidrs   []string `json:"remote_cidrs"`
	Psk           string   `json:"psk"`
	IkeProposal   string   `json:"ike_proposal"`
	EspProposal   string   `json:"esp_proposal"`
	IkeLifetime   int32    `json:"ike_lifetime"`
	EspLifetime   int32    `json:"esp_lifetime"`
	DpdAction     string   `json:"dpd_action"`
	DpdDelay      int32    `json:"dpd_delay"`
	Initiator     bool     `json:"initiator"`
	TunnelLocalIP string   `json:"tunnel_local_ip"`
	TunnelPeerIP  string   `json:"tunnel_peer_ip"`
}

type vpnIpsecConfig struct {
	HaMode      string                `json:"ha_mode"`
	FloatingIp  string                `json:"floating_ip"`
	Connections []*vpnIpsecConnection `json:"connections"`
}

// vpnBgpConnection is the BGP session of one tunnel. LocalPref ranks the tunnels of a connection for our
// traffic, Prepend makes the peer rank them the same way for its own
type vpnBgpConnection struct {
	Name               string   `json:"name"`
	Host               int32    `json:"host"`
	LocalPref          int32    `json:"local_pref"`
	Prepend            int32    `json:"prepend"`
	Bfd                bool     `json:"bfd"`
	BfdInterval        int32    `json:"bfd_interval"`
	BfdMultiplier      int32    `json:"bfd_multiplier"`
	LocalAsn           int64    `json:"local_asn"`
	PeerAsn            int64    `json:"peer_asn"`
	TunnelLocalIP      string   `json:"tunnel_local_ip"`
	TunnelPeerIP       string   `json:"tunnel_peer_ip"`
	Password           string   `json:"password"`
	Keepalive          int32    `json:"keepalive"`
	Hold               int32    `json:"hold"`
	MaxPrefixes        int32    `json:"max_prefixes"`
	LocalCidrs         []string `json:"local_cidrs"`
	RemoteSummaryCidrs []string `json:"remote_summary_cidrs"`
}

// vpnIbgpConfig is the session between the two nodes of an active_active gateway, over their VRRP
// addresses: each node advertises what its own tunnels reach, ranked by local preference, so a node whose
// tunnel of a connection is not the best one forwards to the other node
type vpnIbgpConfig struct {
	Nodes         []*vpnIbgpNode `json:"nodes"`
	BfdInterval   int32          `json:"bfd_interval"`
	BfdMultiplier int32          `json:"bfd_multiplier"`
}

type vpnIbgpNode struct {
	Host int32  `json:"host"`
	IP   string `json:"ip"`
}

// vpnStaticTunnel is a tunnel of a static connection on an active_active gateway: while its CHILD_SA is
// up, the node running it adds a staticd route to the remote networks through its interface (vpn_watch.sh).
// Distance and LocalPref follow the rank: the best tunnel wins locally (distance 1), a lesser one only when
// the iBGP route of the other node (distance 200) is gone; Tag marks the route for the iBGP redistribution
type vpnStaticTunnel struct {
	Name      string   `json:"name"`
	Host      int32    `json:"host"`
	IfID      int32    `json:"if_id"`
	Tag       int32    `json:"tag"`
	Distance  int32    `json:"distance"`
	LocalPref int32    `json:"local_pref"`
	Cidrs     []string `json:"cidrs"`
}

type vpnBgpConfig struct {
	HaMode string `json:"ha_mode"`
	// The ASN of the gateway: the local_asn of its BGP connections (all equal), or vpnDefaultIbgpAsn when an
	// active_active gateway only needs BGP between its nodes
	LocalAsn int64 `json:"local_asn"`
	// maximum-paths for the sessions of the tunnels (1 unless a connection shares its traffic)
	Multipath     int                 `json:"multipath"`
	Ibgp          *vpnIbgpConfig      `json:"ibgp,omitempty"`
	StaticTunnels []*vpnStaticTunnel  `json:"static_tunnels"`
	StaticCidrs   []string            `json:"static_cidrs"` // remote networks of the static connections (active_active: last-resort Null0)
	Connections   []*vpnBgpConnection `json:"connections"`
}

type vpnWgPeer struct {
	Name         string `json:"name"`
	PublicKey    string `json:"public_key"`
	PresharedKey string `json:"preshared_key"`
	AllowedIPs   string `json:"allowed_ips"`
}

type vpnWgConfig struct {
	Port       int32        `json:"port"`
	Address    string       `json:"address"`
	PrivateKey string       `json:"private_key"`
	Peers      []*vpnWgPeer `json:"peers"`
}

type vpnRoutePrefix struct {
	Cidr string `json:"cidr"`
	Type string `json:"type"` // static, bgp or client
	// What a VRRP node installs: "blackhole"; "frr" (active_active tunnels: FRR owns the route); or
	// interface names in order of preference separated by commas, the first one whose tunnel is up wins,
	// or by "+" when they share the traffic (every one that is up)
	Target string `json:"target"`
	// What the other nodes install: the VRRP addresses of the gateway nodes in order of preference, the first
	// reachable one wins; with Ecmp every reachable one shares the traffic (vpn_nexthop_watch.sh probes them)
	NextHops []string `json:"next_hops"`
	Ecmp     bool     `json:"ecmp"`
}

type vpnRouteConfig struct {
	HaMode      string            `json:"ha_mode"`
	VrrpNodes   []int32           `json:"vrrp_nodes"`
	MasterIP    string            `json:"master_ip"`
	VrrpIPs     map[string]string `json:"vrrp_ips"`
	VrrpVlan    int64             `json:"vrrp_vlan"`
	VrrpGateway string            `json:"vrrp_gateway"` // anycast gateway of the VRRP subnet, to build ns-<vlan> where it is missing
	// VRRP address of each gateway node -> the node's own address, which the other nodes probe to leave a
	// dead gateway node within a second (vpn-gateway-plan.md §8.2, F7)
	HostIPs  map[string]string `json:"host_ips"`
	Prefixes []*vpnRoutePrefix `json:"prefixes"`
}

const (
	// ASN of the iBGP session of an active_active gateway without any BGP connection: never seen by a peer
	vpnDefaultIbgpAsn = 64999
	// BFD between the two nodes of an active_active gateway: a private link, so a short interval
	vpnIbgpBfdInterval   = 300
	vpnIbgpBfdMultiplier = 3
)

func vpnVrrpGateway(gateway *model.VpnGateway) string {
	if gateway.VrrpInstance != nil && gateway.VrrpInstance.VrrpSubnet != nil {
		return gateway.VrrpInstance.VrrpSubnet.Gateway
	}
	return ""
}

// VpnConnName is the name of a site connection on the nodes: the swanctl block, the status reports and the
// traffic metrics all use it
func VpnConnName(conn *model.VpnConnection) string {
	return fmt.Sprintf("c%d", conn.ID)
}

func vpnIpsecIface(tunnel *model.VpnTunnel) string {
	return fmt.Sprintf("ipsec-%d", tunnel.IfID)
}

// vpnTunnelsByPreference lists the tunnels of a connection primary first, then by slot
func vpnTunnelsByPreference(conn *model.VpnConnection) []*model.VpnTunnel {
	ordered := []*model.VpnTunnel{}
	primary := VpnPrimaryTunnel(conn)
	if primary != nil {
		ordered = append(ordered, primary)
	}
	for _, t := range conn.Tunnels {
		if t != primary {
			ordered = append(ordered, t)
		}
	}
	return ordered
}

// vpnTunnelRanks ranks the tunnels of a connection: 0 carries the traffic (every tunnel with ecmp), the
// standbys follow in slot order from 1
func vpnTunnelRanks(conn *model.VpnConnection) map[int64]int {
	ranks := map[int64]int{}
	for i, t := range vpnTunnelsByPreference(conn) {
		if VpnIsEcmp(conn) {
			i = 0
		}
		ranks[t.ID] = i
	}
	return ranks
}

// vpnRankLocalPref is the BGP local preference of a tunnel of that rank: 200 for the best, then 100, 90, 80
func vpnRankLocalPref(rank int) int32 {
	if rank == 0 {
		return 200
	}
	lp := 110 - 10*rank
	if lp < 10 {
		lp = 10
	}
	return int32(lp)
}

// vpnRankPrepend is how many times the local ASN is prepended on the advertisements of a tunnel of that
// rank: none for the best, the connection's setting for the first standby, one more per further rank
func vpnRankPrepend(conn *model.VpnConnection, rank int) int32 {
	if rank == 0 || conn.AsPathPrepend <= 0 {
		return 0
	}
	prepend := conn.AsPathPrepend + int32(rank) - 1
	if prepend > 10 {
		prepend = 10
	}
	return prepend
}

// vpnEndpointFips maps the public addresses of a gateway by endpoint. Rows from before endpoints existed
// have none and are the first address.
func vpnEndpointFips(gateway *model.VpnGateway) map[string]*model.FloatingIp {
	fips := map[string]*model.FloatingIp{}
	for _, fip := range gateway.FloatingIps {
		if fip.FipAddress == "" {
			continue
		}
		endpoint := fip.VpnEndpoint
		if endpoint == "" {
			endpoint = model.VpnEndpointVip1
		}
		if fips[endpoint] == nil {
			fips[endpoint] = fip
		}
	}
	return fips
}

func vpnWgIface(gateway *model.VpnGateway) string {
	return fmt.Sprintf("wg-%d", gateway.ID)
}

// vpnWgAddress is the gateway's own address in the client pool: the first host, as a /32 so no connected
// route to the pool appears (the pool route is owned by vpn_notify.sh and depends on the VRRP role)
func vpnWgAddress(gateway *model.VpnGateway) string {
	_, ipNet, err := net.ParseCIDR(gateway.ClientCidr)
	if err != nil {
		return ""
	}
	first, _ := cidr.AddressRange(ipNet)
	return cidr.Inc(first).String() + "/32"
}

// vpnFloatingIp is the first public address: the one keepalived's master check and the client VPN use
func vpnFloatingIp(gateway *model.VpnGateway) *model.FloatingIp {
	return vpnEndpointFips(gateway)[model.VpnEndpointVip1]
}

func vpnPublicAddressOf(fip *model.FloatingIp) *LoadBalancerFloatingIp {
	return &LoadBalancerFloatingIp{
		Address: fip.FipAddress, Vlan: fip.Subnet.Vlan, Gateway: fip.Subnet.Gateway,
		MarkID: fip.ID, Inbound: fip.Inbound, Outbound: fip.Outbound,
	}
}

func vpnVrrpGroup(ctx context.Context, gateway *model.VpnGateway) (control string, err error) {
	hyperGroup, _, _, err := GetVrrpHyperGroup(ctx, gateway.VrrpInstance)
	if err != nil {
		return
	}
	return "toall=" + hyperGroup, nil
}

// vpnControl picks inter= for one node or the VRRP pair group otherwise
func vpnControl(ctx context.Context, gateway *model.VpnGateway, onlyHyper int32) (string, error) {
	if onlyHyper >= 0 {
		return fmt.Sprintf("inter=%d", onlyHyper), nil
	}
	return vpnVrrpGroup(ctx, gateway)
}

func vpnVrrpVlan(gateway *model.VpnGateway) int64 {
	if gateway.VrrpInstance != nil && gateway.VrrpInstance.VrrpSubnet != nil {
		return gateway.VrrpInstance.VrrpSubnet.Vlan
	}
	return 0
}

// dispatchVpnGateway builds the gateway skeleton on the VRRP pair: directories, public port, keepalived
// with the VPN notify hooks, INPUT rules and the WireGuard interface. Idempotent.
func dispatchVpnGateway(ctx context.Context, gateway *model.VpnGateway, onlyHyper int32) (err error) {
	iface1, iface2, err := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
	if err != nil {
		return
	}
	fip := vpnFloatingIp(gateway)
	if (fip == nil || fip.Subnet == nil) && !VpnIsActiveActive(gateway) {
		return NewCLError(ErrVpnGatewayNotReady, "VPN gateway has no public address", nil)
	}
	privateKey, err := DecryptSecret(gateway.ClientPrivateKey)
	if err != nil {
		return
	}
	cfg := &vpnGatewayConfig{
		HaMode:        vpnHaMode(gateway),
		Vrid:          gateway.VrrpInstance.Vrid,
		FloatingIps:   []*vpnPublicAddress{},
		IpsecEnabled:  gateway.IpsecEnabled,
		ClientEnabled: gateway.ClientEnabled,
		ClientPort:    gateway.ClientPort,
		ClientCidr:    gateway.ClientCidr,
		WgAddress:     vpnWgAddress(gateway),
		WgPrivateKey:  privateKey,
		Enabled:       !gateway.Disabled,
	}
	if fip != nil && fip.Subnet != nil {
		cfg.FloatingIp = vpnPublicAddressOf(fip)
	}
	endpoints := vpnEndpointFips(gateway)
	hosts := map[string]int32{model.VpnEndpointNode1: iface1.Hyper, model.VpnEndpointNode2: iface2.Hyper}
	for _, endpoint := range vpnEndpointOrder {
		if f := endpoints[endpoint]; f != nil && f.Subnet != nil {
			host, fixed := hosts[endpoint]
			if !fixed {
				host = -1
			}
			cfg.FloatingIps = append(cfg.FloatingIps, &vpnPublicAddress{Endpoint: endpoint, Host: host, LoadBalancerFloatingIp: vpnPublicAddressOf(f)})
		}
	}
	jsonData, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	vlan := vpnVrrpVlan(gateway)
	pairs := []struct {
		role     string
		me, peer *model.Interface
	}{{"MASTER", iface1, iface2}, {"BACKUP", iface2, iface1}}
	for _, p := range pairs {
		if p.me.Hyper < 0 || (onlyHyper >= 0 && p.me.Hyper != onlyHyper) {
			continue
		}
		control := fmt.Sprintf("inter=%d", p.me.Hyper)
		command := fmt.Sprintf(vpnScriptDir+"create_vpn_gateway.sh '%d' '%d' '%d' '%d' '%s' '%s' '%s' '%s' '%s'<<'EOF'\n%s\nEOF",
			gateway.RouterID, gateway.ID, gateway.VrrpInstanceID, vlan, ShellEscape(p.me.Address.Address), ShellEscape(p.me.MacAddr),
			ShellEscape(p.peer.Address.Address), ShellEscape(p.peer.MacAddr), ShellEscape(p.role), jsonData)
		if err = HyperExecute(ctx, control, command); err != nil {
			logger.Ctx(ctx).Errorf("Failed to dispatch VPN gateway %d to hyper %d: %v", gateway.ID, p.me.Hyper, err)
			return
		}
	}
	return
}

func buildVpnIpsecConfig(ctx context.Context, gateway *model.VpnGateway) (cfg *vpnIpsecConfig, err error) {
	cfg = &vpnIpsecConfig{HaMode: vpnHaMode(gateway), Connections: []*vpnIpsecConnection{}}
	if fip := vpnFloatingIp(gateway); fip != nil {
		cfg.FloatingIp = ipOnly(fip.FipAddress)
	} else if !VpnIsActiveActive(gateway) {
		return nil, NewCLError(ErrVpnGatewayNotReady, "VPN gateway has no public address", nil)
	}
	if !gateway.IpsecEnabled {
		return
	}
	endpoints := vpnEndpointFips(gateway)
	hosts, err := vpnEndpointHosts(ctx, gateway)
	if err != nil {
		return
	}
	for _, conn := range gateway.Connections {
		var connPsk, localCidrs, remoteCidrs = "", []string{}, []string{}
		if connPsk, err = DecryptSecret(conn.Psk); err != nil {
			return
		}
		if localCidrs, err = effectiveLocalCidrs(ctx, gateway, conn); err != nil {
			return
		}
		if conn.RouteMode == model.VpnRouteModeStatic {
			if remoteCidrs, err = ParseCidrList(conn.RemoteCidrs); err != nil {
				return
			}
		}
		for _, tunnel := range conn.Tunnels {
			localAddr := cfg.FloatingIp
			if f := endpoints[tunnel.Endpoint]; f != nil {
				localAddr = ipOnly(f.FipAddress)
			}
			psk := connPsk
			if tunnel.Psk != "" {
				if psk, err = DecryptSecret(tunnel.Psk); err != nil {
					return
				}
			}
			localID := conn.LocalID
			if localID == "" {
				localID = localAddr
			}
			remoteID := tunnel.RemoteID
			if remoteID == "" {
				remoteID = tunnel.RemoteGateway
			}
			cfg.Connections = append(cfg.Connections, &vpnIpsecConnection{
				Name: VpnTunnelName(tunnel), Host: vpnTunnelHost(hosts, tunnel), IfID: tunnel.IfID, LocalAddr: localAddr, RemoteGateway: tunnel.RemoteGateway, RemoteID: remoteID,
				LocalID: localID, RouteMode: conn.RouteMode, LocalCidrs: localCidrs, RemoteCidrs: remoteCidrs, Psk: psk,
				IkeProposal: conn.IkeProposal, EspProposal: conn.EspProposal, IkeLifetime: conn.IkeLifetime, EspLifetime: conn.EspLifetime,
				DpdAction: conn.DpdAction, DpdDelay: conn.DpdDelay, Initiator: conn.Initiator,
				TunnelLocalIP: tunnel.TunnelLocalIP, TunnelPeerIP: tunnel.TunnelPeerIP,
			})
		}
	}
	return
}

// dispatchVpnIpsec rewrites swanctl.conf and the XFRM interfaces on the VRRP pair
func dispatchVpnIpsec(ctx context.Context, gateway *model.VpnGateway, onlyHyper int32) (err error) {
	cfg, err := buildVpnIpsecConfig(ctx, gateway)
	if err != nil {
		return
	}
	jsonData, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	control, err := vpnControl(ctx, gateway, onlyHyper)
	if err != nil {
		return
	}
	command := fmt.Sprintf(vpnScriptDir+"create_vpn_ipsec_conf.sh '%d' '%d'<<'EOF'\n%s\nEOF", gateway.RouterID, gateway.ID, jsonData)
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to dispatch IPsec config of VPN gateway %d: %v", gateway.ID, err)
	}
	return
}

func buildVpnBgpConfig(ctx context.Context, gateway *model.VpnGateway) (cfg *vpnBgpConfig, err error) {
	cfg = &vpnBgpConfig{HaMode: vpnHaMode(gateway), Multipath: 1, StaticTunnels: []*vpnStaticTunnel{}, StaticCidrs: []string{}, Connections: []*vpnBgpConnection{}}
	if !gateway.IpsecEnabled {
		return
	}
	hosts, err := vpnEndpointHosts(ctx, gateway)
	if err != nil {
		return
	}
	aa := VpnIsActiveActive(gateway)
	for _, conn := range gateway.Connections {
		ranks := vpnTunnelRanks(conn)
		if VpnIsEcmp(conn) && len(conn.Tunnels) > cfg.Multipath {
			cfg.Multipath = len(conn.Tunnels)
		}
		if conn.RouteMode != model.VpnRouteModeBgp {
			// An active_active gateway routes static connections through FRR too: each node's tunnels become
			// staticd routes while their SA is up, and iBGP tells the other node about them
			if !aa {
				continue
			}
			remote, perr := ParseCidrList(conn.RemoteCidrs)
			if perr != nil {
				return nil, perr
			}
			cfg.StaticCidrs = append(cfg.StaticCidrs, remote...)
			for _, tunnel := range conn.Tunnels {
				rank := ranks[tunnel.ID]
				distance := int32(1)
				if rank > 0 {
					distance = 210 + int32(rank)
				}
				cfg.StaticTunnels = append(cfg.StaticTunnels, &vpnStaticTunnel{
					Name: VpnTunnelName(tunnel), Host: vpnTunnelHost(hosts, tunnel), IfID: tunnel.IfID, Tag: 100 + int32(rank),
					Distance: distance, LocalPref: vpnRankLocalPref(rank), Cidrs: remote,
				})
			}
			continue
		}
		var password string
		if password, err = DecryptSecret(conn.BgpPassword); err != nil {
			return
		}
		localCidrs, lerr := effectiveLocalCidrs(ctx, gateway, conn)
		if lerr != nil {
			return nil, lerr
		}
		summary, serr := ParseCidrList(conn.RemoteSummaryCidrs)
		if serr != nil {
			return nil, serr
		}
		cfg.LocalAsn = conn.LocalAsn
		for _, tunnel := range conn.Tunnels {
			rank := ranks[tunnel.ID]
			entry := &vpnBgpConnection{
				Name: VpnTunnelName(tunnel), Host: vpnTunnelHost(hosts, tunnel), LocalAsn: conn.LocalAsn, PeerAsn: conn.PeerAsn,
				TunnelLocalIP: tunnel.TunnelLocalIP, TunnelPeerIP: tunnel.TunnelPeerIP, Password: password,
				Keepalive: conn.BgpKeepalive, Hold: conn.BgpHold, MaxPrefixes: conn.MaxPrefixes,
				LocalCidrs: localCidrs, RemoteSummaryCidrs: summary, LocalPref: 200,
				Bfd: conn.BfdEnabled, BfdInterval: conn.BfdInterval, BfdMultiplier: conn.BfdMultiplier,
			}
			if len(conn.Tunnels) > 1 {
				entry.LocalPref, entry.Prepend = vpnRankLocalPref(rank), vpnRankPrepend(conn, rank)
			}
			cfg.Connections = append(cfg.Connections, entry)
		}
	}
	if aa && len(gateway.Connections) > 0 {
		if cfg.LocalAsn == 0 {
			cfg.LocalAsn = vpnDefaultIbgpAsn
		}
		iface1, iface2, ierr := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
		if ierr != nil {
			return nil, ierr
		}
		cfg.Ibgp = &vpnIbgpConfig{BfdInterval: vpnIbgpBfdInterval, BfdMultiplier: vpnIbgpBfdMultiplier}
		for _, iface := range []*model.Interface{iface1, iface2} {
			if iface.Hyper >= 0 && iface.Address != nil {
				cfg.Ibgp.Nodes = append(cfg.Ibgp.Nodes, &vpnIbgpNode{Host: iface.Hyper, IP: ipOnly(iface.Address.Address)})
			}
		}
	}
	return
}

// dispatchVpnBgp rewrites the FRR config on the VRRP pair; the node holding the floating IP (re)loads it
func dispatchVpnBgp(ctx context.Context, gateway *model.VpnGateway, onlyHyper int32) (err error) {
	cfg, err := buildVpnBgpConfig(ctx, gateway)
	if err != nil {
		return
	}
	jsonData, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	control, err := vpnControl(ctx, gateway, onlyHyper)
	if err != nil {
		return
	}
	command := fmt.Sprintf(vpnScriptDir+"create_vpn_bgp_conf.sh '%d' '%d'<<'EOF'\n%s\nEOF", gateway.RouterID, gateway.ID, jsonData)
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to dispatch BGP config of VPN gateway %d: %v", gateway.ID, err)
	}
	return
}

func buildVpnWgConfig(ctx context.Context, gateway *model.VpnGateway) (cfg *vpnWgConfig, err error) {
	cfg = &vpnWgConfig{Port: gateway.ClientPort, Address: vpnWgAddress(gateway), Peers: []*vpnWgPeer{}}
	if !gateway.ClientEnabled {
		return
	}
	if cfg.PrivateKey, err = DecryptSecret(gateway.ClientPrivateKey); err != nil {
		return
	}
	for _, client := range gateway.Clients {
		if !client.Enabled || client.PublicKey == "" || client.IPAddress == "" {
			continue
		}
		var psk string
		if psk, err = DecryptSecret(client.PresharedKey); err != nil {
			return
		}
		cfg.Peers = append(cfg.Peers, &vpnWgPeer{Name: client.Name, PublicKey: client.PublicKey, PresharedKey: psk, AllowedIPs: client.IPAddress + "/32"})
	}
	return
}

// dispatchVpnWg rewrites wg.conf on the VRRP pair and applies it with wg syncconf
func dispatchVpnWg(ctx context.Context, gateway *model.VpnGateway, onlyHyper int32) (err error) {
	cfg, err := buildVpnWgConfig(ctx, gateway)
	if err != nil {
		return
	}
	jsonData, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	control, err := vpnControl(ctx, gateway, onlyHyper)
	if err != nil {
		return
	}
	command := fmt.Sprintf(vpnScriptDir+"create_vpn_wg_conf.sh '%d' '%d'<<'EOF'\n%s\nEOF", gateway.RouterID, gateway.ID, jsonData)
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to dispatch WireGuard config of VPN gateway %d: %v", gateway.ID, err)
	}
	return
}

// buildVpnRouteConfig turns the derived prefix set into what the nodes install. The VRRP pair receives
// per-prefix targets: on an active_standby gateway vpn_notify.sh installs them by role, on an active_active
// one FRR owns the tunnel prefixes and only the client pool follows the floating IP. Every other node gets
// the gateway nodes to route through, in order of preference per prefix (vpnPrefixNextHops).
func buildVpnRouteConfig(ctx context.Context, gateway *model.VpnGateway, prefixes []*model.VpnRemotePrefix) (cfg *vpnRouteConfig, vrrpHypers []int32, err error) {
	iface1, iface2, err := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
	if err != nil {
		return
	}
	aa := VpnIsActiveActive(gateway)
	cfg = &vpnRouteConfig{HaMode: vpnHaMode(gateway), VrrpVlan: vpnVrrpVlan(gateway), VrrpGateway: vpnVrrpGateway(gateway),
		VrrpIPs: map[string]string{}, HostIPs: map[string]string{}, Prefixes: []*vpnRoutePrefix{}}
	nodeIP := map[int32]string{}
	for _, iface := range []*model.Interface{iface1, iface2} {
		if iface.Hyper < 0 || iface.Address == nil {
			continue
		}
		ip := ipOnly(iface.Address.Address)
		cfg.VrrpNodes = append(cfg.VrrpNodes, iface.Hyper)
		cfg.VrrpIPs[strconv.Itoa(int(iface.Hyper))] = ip
		nodeIP[iface.Hyper] = ip
		vrrpHypers = append(vrrpHypers, iface.Hyper)
		if hostIP := vpnHyperHostIP(ctx, iface.Hyper); hostIP != "" {
			cfg.HostIPs[ip] = hostIP
		}
	}
	master, other := iface1, iface2
	if gateway.MasterHyper >= 0 && iface2.Hyper == gateway.MasterHyper {
		master, other = iface2, iface1
	}
	if master.Address != nil {
		cfg.MasterIP = ipOnly(master.Address.Address)
	}
	// The floating IP holder first, the other node as the fallback F7 switches to
	masterHops := []string{}
	for _, iface := range []*model.Interface{master, other} {
		if ip := nodeIP[iface.Hyper]; ip != "" && iface.Hyper >= 0 {
			masterHops = append(masterHops, ip)
		}
	}
	connTarget := map[int64]string{}
	connHops := map[int64][]string{}
	connEcmp := map[int64]bool{}
	var hopState *vpnHopState // read on the first connection of an active_active gateway
	for _, conn := range gateway.Connections {
		ifaces := []string{}
		for _, tunnel := range vpnTunnelsByPreference(conn) {
			ifaces = append(ifaces, vpnIpsecIface(tunnel))
		}
		sep := ","
		if VpnIsEcmp(conn) {
			sep = "+"
		}
		connTarget[conn.ID] = strings.Join(ifaces, sep)
		connHops[conn.ID], connEcmp[conn.ID] = masterHops, false
		if aa {
			connTarget[conn.ID] = "frr"
			if hopState == nil {
				hopState, _ = vpnHopStateOf(ctx, gateway)
			}
			hops, ecmp := vpnPrefixNextHops(hopState, conn)
			connHops[conn.ID], connEcmp[conn.ID] = []string{}, ecmp
			for _, h := range hops {
				if ip := nodeIP[h]; ip != "" {
					connHops[conn.ID] = append(connHops[conn.ID], ip)
				}
			}
		}
	}
	for _, prefix := range prefixes {
		entry := &vpnRoutePrefix{Cidr: prefix.Cidr, NextHops: masterHops}
		switch prefix.Source {
		case model.VpnPrefixSourceConnectionStatic:
			entry.Type, entry.Target = "static", connTarget[prefix.RefID]
			entry.NextHops, entry.Ecmp = connHops[prefix.RefID], connEcmp[prefix.RefID]
		case model.VpnPrefixSourceConnectionSummary:
			entry.Type, entry.Target = "bgp", "blackhole"
			if aa {
				entry.Target = "frr"
				entry.NextHops, entry.Ecmp = connHops[prefix.RefID], connEcmp[prefix.RefID]
			}
		case model.VpnPrefixSourceClientPool:
			entry.Type, entry.Target = "client", vpnWgIface(gateway)
		default:
			continue
		}
		if entry.Target == "" {
			continue
		}
		cfg.Prefixes = append(cfg.Prefixes, entry)
	}
	return
}

func vpnRouteTargets(ctx context.Context, gateway *model.VpnGateway, vrrpHypers []int32, onlyHyper int32) (targets []int32, err error) {
	if onlyHyper >= 0 {
		return []int32{onlyHyper}, nil
	}
	hypers, err := RouterHyperSet(ctx, gateway.RouterID)
	if err != nil {
		return
	}
	seen := map[int32]bool{}
	for _, h := range append(hypers, vrrpHypers...) {
		if !seen[h] {
			seen[h] = true
			targets = append(targets, h)
		}
	}
	return
}

// dispatchVpnRoutes installs the current prefix set (routes and nonat entries) on every node hosting
// the VPC, or on one node. An empty target list sends nothing: cland treats an empty group as "all nodes".
func dispatchVpnRoutes(ctx context.Context, gateway *model.VpnGateway, onlyHyper int32) (err error) {
	prefixes, err := loadRemotePrefixes(ctx, gateway.ID)
	if err != nil {
		return
	}
	cfg, vrrpHypers, err := buildVpnRouteConfig(ctx, gateway, prefixes)
	if err != nil {
		return
	}
	targets, err := vpnRouteTargets(ctx, gateway, vrrpHypers, onlyHyper)
	if err != nil || len(targets) == 0 {
		return
	}
	jsonData, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	control := fmt.Sprintf("inter=%d", targets[0])
	if len(targets) > 1 {
		control = fmt.Sprintf("toall=group-vpn-%d:%d", gateway.ID, targets[0])
		for _, h := range targets[1:] {
			control = fmt.Sprintf("%s,%d", control, h)
		}
	}
	command := fmt.Sprintf(vpnScriptDir+"set_vpn_route.sh '%d' '%d'<<'EOF'\n%s\nEOF", gateway.RouterID, gateway.ID, jsonData)
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to dispatch routes of VPN gateway %d: %v", gateway.ID, err)
	}
	return
}

// dispatchVpnAll pushes everything, in the order the pieces depend on each other: the skeleton (keepalived,
// public port, WireGuard interface), then the IPsec connections (XFRM interfaces must exist before FRR
// validates its network statements), then BGP, then the WireGuard peers and finally the routes
func dispatchVpnAll(ctx context.Context, gateway *model.VpnGateway, onlyHyper int32) (err error) {
	if err = dispatchVpnGateway(ctx, gateway, onlyHyper); err != nil {
		return
	}
	if err = dispatchVpnIpsec(ctx, gateway, onlyHyper); err != nil {
		return
	}
	if err = dispatchVpnBgp(ctx, gateway, onlyHyper); err != nil {
		return
	}
	if err = dispatchVpnWg(ctx, gateway, onlyHyper); err != nil {
		return
	}
	return dispatchVpnRoutes(ctx, gateway, onlyHyper)
}

// dispatchVpnClear removes the gateway from every node: the VRRP pair tears down processes, interfaces,
// keepalived and the public port; the other nodes drop the routes and nonat entries
func dispatchVpnClear(ctx context.Context, gateway *model.VpnGateway) (err error) {
	iface1, iface2, err := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
	if err != nil {
		return
	}
	// Every public address as (address, vlan, mark) triplets, the first address first
	fipArgs := ""
	endpoints := vpnEndpointFips(gateway)
	for _, endpoint := range vpnEndpointOrder {
		fip := endpoints[endpoint]
		if fip == nil {
			continue
		}
		vlan := int64(0)
		if fip.Subnet != nil {
			vlan = fip.Subnet.Vlan
		}
		fipArgs += fmt.Sprintf(" '%s' '%d' '%d'", ShellEscape(fip.FipAddress), vlan, fip.ID)
	}
	if fipArgs == "" {
		fipArgs = " '-' '0' '0'"
	}
	vrrpHypers := map[int32]bool{}
	for _, iface := range []*model.Interface{iface1, iface2} {
		if iface.Hyper < 0 {
			continue
		}
		vrrpHypers[iface.Hyper] = true
		control := fmt.Sprintf("inter=%d", iface.Hyper)
		command := fmt.Sprintf(vpnScriptDir+"clear_vpn_gateway.sh '%d' '%d'%s", gateway.RouterID, gateway.ID, fipArgs)
		if err = HyperExecute(ctx, control, command); err != nil {
			logger.Ctx(ctx).Errorf("Failed to clear VPN gateway %d on hyper %d: %v", gateway.ID, iface.Hyper, err)
			return
		}
	}
	hypers, err := RouterHyperSet(ctx, gateway.RouterID)
	if err != nil {
		return
	}
	cfg := &vpnRouteConfig{HaMode: vpnHaMode(gateway), VrrpVlan: vpnVrrpVlan(gateway), VrrpGateway: vpnVrrpGateway(gateway),
		VrrpIPs: map[string]string{}, HostIPs: map[string]string{}, Prefixes: []*vpnRoutePrefix{}}
	jsonData, _ := json.Marshal(cfg)
	for _, h := range hypers {
		if vrrpHypers[h] {
			continue
		}
		control := fmt.Sprintf("inter=%d", h)
		command := fmt.Sprintf(vpnScriptDir+"set_vpn_route.sh '%d' '%d'<<'EOF'\n%s\nEOF", gateway.RouterID, gateway.ID, jsonData)
		if err = HyperExecute(ctx, control, command); err != nil {
			logger.Ctx(ctx).Errorf("Failed to clear routes of VPN gateway %d on hyper %d: %v", gateway.ID, h, err)
			return
		}
	}
	return
}

// dispatchVpnRestart terminates and re-initiates tunnels of one connection on the nodes that run them
func dispatchVpnRestart(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, names []string) (err error) {
	control, err := vpnVrrpGroup(ctx, gateway)
	if err != nil {
		return
	}
	command := fmt.Sprintf(vpnScriptDir+"restart_vpn_conn.sh '%d' '%d'", gateway.RouterID, gateway.ID)
	for _, name := range names {
		command += fmt.Sprintf(" '%s'", ShellEscape(name))
	}
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to restart connection %d of VPN gateway %d: %v", conn.ID, gateway.ID, err)
	}
	return
}

// VpnEndpointAddresses maps the public addresses of a gateway (without prefix length) by endpoint
func VpnEndpointAddresses(gateway *model.VpnGateway) map[string]string {
	addresses := map[string]string{}
	for endpoint, fip := range vpnEndpointFips(gateway) {
		addresses[endpoint] = ipOnly(fip.FipAddress)
	}
	return addresses
}

// vpnEndpointOrder is the order public addresses are listed in: the floating IPs, then the node addresses
var vpnEndpointOrder = []string{model.VpnEndpointVip1, model.VpnEndpointVip2, model.VpnEndpointNode1, model.VpnEndpointNode2}

// vpnHaMode is the HA mode of a gateway, active_standby for rows from before modes existed
func vpnHaMode(gateway *model.VpnGateway) string {
	if gateway.HaMode == "" {
		return model.VpnHaModeActiveStandby
	}
	return gateway.HaMode
}

// vpnEndpointHosts maps the node endpoints of an active_active gateway to the node they live on: node1 is
// the node of the MASTER VRRP interface, node2 of the BACKUP one (-1 until the pair is placed). Empty for an
// active_standby gateway, whose tunnels all run on the master.
func vpnEndpointHosts(ctx context.Context, gateway *model.VpnGateway) (hosts map[string]int32, err error) {
	hosts = map[string]int32{}
	if !VpnIsActiveActive(gateway) {
		return
	}
	iface1, iface2, err := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
	if err != nil {
		return
	}
	hosts[model.VpnEndpointNode1], hosts[model.VpnEndpointNode2] = iface1.Hyper, iface2.Hyper
	return
}

// vpnTunnelHost is the node that runs a tunnel on an active_active gateway, -1 (the master) otherwise
func vpnTunnelHost(hosts map[string]int32, tunnel *model.VpnTunnel) int32 {
	if host, ok := hosts[tunnel.Endpoint]; ok {
		return host
	}
	return -1
}

// vpnHyperHostIP is the node's own address (the one cland and the other nodes reach it on)
func vpnHyperHostIP(ctx context.Context, hostid int32) string {
	_, db := GetContextDB(ctx)
	hyper := &model.Hyper{}
	if db.Select("host_ip").Where("hostid = ?", hostid).Take(hyper).Error != nil {
		return ""
	}
	return hyper.HostIP
}

// vpnHopState is what vpnPrefixNextHops reads besides the connection, read once per gateway rather than
// once per connection: the node of each endpoint, and whether cland lost those nodes
type vpnHopState struct {
	endpointHosts map[string]int32
	gone          map[int32]bool
}

func vpnHopStateOf(ctx context.Context, gateway *model.VpnGateway) (*vpnHopState, error) {
	hosts, err := vpnEndpointHosts(ctx, gateway)
	if err != nil {
		return nil, err
	}
	state := &vpnHopState{endpointHosts: hosts, gone: map[int32]bool{}}
	for _, host := range hosts {
		if host >= 0 {
			state.gone[host] = vpnNodeGone(ctx, host)
		}
	}
	return state, nil
}

// vpnPrefixNextHops orders the nodes of an active_active gateway for the networks of one connection, as
// seen from the other nodes: the node of the best tunnel first. A node cland lost goes last, and so does a
// node none of whose tunnels of the connection is up while the other node has one (the first node would
// forward over iBGP anyway; this only saves the detour). With ecmp every node with a tunnel up is listed
// and shares the traffic.
func vpnPrefixNextHops(state *vpnHopState, conn *model.VpnConnection) (hosts []int32, ecmp bool) {
	if state == nil {
		return
	}
	endpointHosts := state.endpointHosts
	type candidate struct {
		host      int32
		up, alive bool
	}
	candidates := []*candidate{}
	byHost := map[int32]*candidate{}
	for _, tunnel := range vpnTunnelsByPreference(conn) {
		host := vpnTunnelHost(endpointHosts, tunnel)
		if host < 0 {
			continue
		}
		c := byHost[host]
		if c == nil {
			c = &candidate{host: host, alive: !state.gone[host]}
			byHost[host] = c
			candidates = append(candidates, c)
		}
		c.up = c.up || tunnel.Status == model.VpnConnectionStatusUp
	}
	// Every node of the pair can forward: the ones without a tunnel of this connection go after them
	for _, host := range []int32{endpointHosts[model.VpnEndpointNode1], endpointHosts[model.VpnEndpointNode2]} {
		if host >= 0 && byHost[host] == nil {
			c := &candidate{host: host, alive: !state.gone[host]}
			byHost[host] = c
			candidates = append(candidates, c)
		}
	}
	score := func(c *candidate) int {
		switch {
		case !c.alive:
			return 2
		case c.up:
			return 0
		}
		return 1
	}
	sort.SliceStable(candidates, func(i, j int) bool { return score(candidates[i]) < score(candidates[j]) })
	ecmp = VpnIsEcmp(conn)
	for _, c := range candidates {
		// With ecmp only the nodes carrying the connection share it; the rest stay as the fallback when none can
		if ecmp && len(hosts) > 0 && score(c) > 0 {
			break
		}
		hosts = append(hosts, c.host)
	}
	if ecmp && len(hosts) < 2 {
		ecmp = false
		hosts = hosts[:0]
		for _, c := range candidates {
			hosts = append(hosts, c.host)
		}
	}
	return
}

// vpnNextHopSignature summarizes where the other nodes send the traffic of an active_active gateway, to
// tell whether a status change moved it
func vpnNextHopSignature(ctx context.Context, gateway *model.VpnGateway) string {
	if !VpnIsActiveActive(gateway) {
		return ""
	}
	state, err := vpnHopStateOf(ctx, gateway)
	if err != nil {
		return ""
	}
	parts := []string{}
	for _, conn := range gateway.Connections {
		hosts, ecmp := vpnPrefixNextHops(state, conn)
		parts = append(parts, fmt.Sprintf("%d:%v:%v", conn.ID, hosts, ecmp))
	}
	return strings.Join(parts, ";")
}

// VpnHaModeOf is the HA mode of a gateway (active_standby for rows from before modes existed)
func VpnHaModeOf(gateway *model.VpnGateway) string {
	return vpnHaMode(gateway)
}

// VpnTrafficPolicyOf is the traffic policy of a connection (preferred for rows from before policies existed)
func VpnTrafficPolicyOf(conn *model.VpnConnection) string {
	if conn.TrafficPolicy == "" {
		return model.VpnTrafficPolicyPreferred
	}
	return conn.TrafficPolicy
}

// VpnAddableEndpoint is the public address the gateway can take in addition to the ones it was created with
func VpnAddableEndpoint(gateway *model.VpnGateway) string {
	return vpnAddableEndpoint(gateway)
}

// VpnEndpointHostsOf maps node1 / node2 of an active_active gateway to their node (empty otherwise)
func VpnEndpointHostsOf(ctx context.Context, gateway *model.VpnGateway) map[string]int32 {
	hosts, err := vpnEndpointHosts(ctx, gateway)
	if err != nil {
		return map[string]int32{}
	}
	return hosts
}
