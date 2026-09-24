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
	"strconv"

	. "api/src/common"
	"api/src/model"

	"github.com/apparentlymart/go-cidr/cidr"
)

const vpnScriptDir = "/opt/cloudland/scripts/backend/"

// Payloads handed to the node scripts on stdin. The scripts are declarative: each call carries the whole
// desired state of one aspect (all connections, all peers, all routes) and the node reconciles.

type vpnGatewayConfig struct {
	Vrid          int                     `json:"vrid"`
	FloatingIp    *LoadBalancerFloatingIp `json:"floating_ip"`
	IpsecEnabled  bool                    `json:"ipsec_enabled"`
	ClientEnabled bool                    `json:"client_enabled"`
	ClientPort    int32                   `json:"client_port"`
	ClientCidr    string                  `json:"client_cidr"`
	WgAddress     string                  `json:"wg_address"`
	WgPrivateKey  string                  `json:"wg_private_key"`
	Enabled       bool                    `json:"enabled"`
}

type vpnIpsecConnection struct {
	Name          string   `json:"name"`
	IfID          int32    `json:"if_id"`
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
	FloatingIp  string                `json:"floating_ip"`
	Connections []*vpnIpsecConnection `json:"connections"`
}

type vpnBgpConnection struct {
	Name               string   `json:"name"`
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

type vpnBgpConfig struct {
	Connections []*vpnBgpConnection `json:"connections"`
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
	Cidr   string `json:"cidr"`
	Type   string `json:"type"`   // static, bgp or client
	Target string `json:"target"` // what the master installs: an interface name or "blackhole"
}

type vpnRouteConfig struct {
	VrrpNodes   []int32           `json:"vrrp_nodes"`
	MasterIP    string            `json:"master_ip"`
	VrrpIPs     map[string]string `json:"vrrp_ips"`
	VrrpVlan    int64             `json:"vrrp_vlan"`
	VrrpGateway string            `json:"vrrp_gateway"` // anycast gateway of the VRRP subnet, to build ns-<vlan> where it is missing
	Prefixes    []*vpnRoutePrefix `json:"prefixes"`
}

func vpnVrrpGateway(gateway *model.VpnGateway) string {
	if gateway.VrrpInstance != nil && gateway.VrrpInstance.VrrpSubnet != nil {
		return gateway.VrrpInstance.VrrpSubnet.Gateway
	}
	return ""
}

func vpnConnName(conn *model.VpnConnection) string {
	return fmt.Sprintf("c%d", conn.ID)
}

func vpnIpsecIface(conn *model.VpnConnection) string {
	return fmt.Sprintf("ipsec-%d", conn.IfID)
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

func vpnFloatingIp(gateway *model.VpnGateway) *model.FloatingIp {
	for _, fip := range gateway.FloatingIps {
		if fip.FipAddress != "" {
			return fip
		}
	}
	return nil
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
	if fip == nil || fip.Subnet == nil {
		return NewCLError(ErrVpnGatewayNotReady, "VPN gateway has no public address", nil)
	}
	privateKey, err := DecryptSecret(gateway.ClientPrivateKey)
	if err != nil {
		return
	}
	cfg := &vpnGatewayConfig{
		Vrid: gateway.VrrpInstance.Vrid,
		FloatingIp: &LoadBalancerFloatingIp{
			Address: fip.FipAddress, Vlan: fip.Subnet.Vlan, Gateway: fip.Subnet.Gateway,
			MarkID: fip.ID, Inbound: fip.Inbound, Outbound: fip.Outbound,
		},
		IpsecEnabled:  gateway.IpsecEnabled,
		ClientEnabled: gateway.ClientEnabled,
		ClientPort:    gateway.ClientPort,
		ClientCidr:    gateway.ClientCidr,
		WgAddress:     vpnWgAddress(gateway),
		WgPrivateKey:  privateKey,
		Enabled:       !gateway.Disabled,
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
	fip := vpnFloatingIp(gateway)
	if fip == nil {
		return nil, NewCLError(ErrVpnGatewayNotReady, "VPN gateway has no public address", nil)
	}
	cfg = &vpnIpsecConfig{FloatingIp: ipOnly(fip.FipAddress), Connections: []*vpnIpsecConnection{}}
	if !gateway.IpsecEnabled {
		return
	}
	for _, conn := range gateway.Connections {
		var psk, localCidrs, remoteCidrs = "", []string{}, []string{}
		if psk, err = DecryptSecret(conn.Psk); err != nil {
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
		localID := conn.LocalID
		if localID == "" {
			localID = cfg.FloatingIp
		}
		remoteID := conn.RemoteID
		if remoteID == "" {
			remoteID = conn.RemoteGateway
		}
		cfg.Connections = append(cfg.Connections, &vpnIpsecConnection{
			Name: vpnConnName(conn), IfID: conn.IfID, RemoteGateway: conn.RemoteGateway, RemoteID: remoteID, LocalID: localID,
			RouteMode: conn.RouteMode, LocalCidrs: localCidrs, RemoteCidrs: remoteCidrs, Psk: psk,
			IkeProposal: conn.IkeProposal, EspProposal: conn.EspProposal, IkeLifetime: conn.IkeLifetime, EspLifetime: conn.EspLifetime,
			DpdAction: conn.DpdAction, DpdDelay: conn.DpdDelay, Initiator: conn.Initiator,
			TunnelLocalIP: conn.TunnelLocalIP, TunnelPeerIP: conn.TunnelPeerIP,
		})
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
	cfg = &vpnBgpConfig{Connections: []*vpnBgpConnection{}}
	if !gateway.IpsecEnabled {
		return
	}
	for _, conn := range gateway.Connections {
		if conn.RouteMode != model.VpnRouteModeBgp {
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
		cfg.Connections = append(cfg.Connections, &vpnBgpConnection{
			Name: vpnConnName(conn), LocalAsn: conn.LocalAsn, PeerAsn: conn.PeerAsn,
			TunnelLocalIP: conn.TunnelLocalIP, TunnelPeerIP: conn.TunnelPeerIP, Password: password,
			Keepalive: conn.BgpKeepalive, Hold: conn.BgpHold, MaxPrefixes: conn.MaxPrefixes,
			LocalCidrs: localCidrs, RemoteSummaryCidrs: summary,
		})
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

// buildVpnRouteConfig turns the derived prefix set into what the nodes install. The master's VRRP
// address is the nexthop for every other node; the VRRP pair receives the per-prefix targets and lets
// vpn_notify.sh pick by role.
func buildVpnRouteConfig(ctx context.Context, gateway *model.VpnGateway, prefixes []*model.VpnRemotePrefix) (cfg *vpnRouteConfig, vrrpHypers []int32, err error) {
	iface1, iface2, err := GetVrrpInterfaces(ctx, gateway.VrrpInstanceID)
	if err != nil {
		return
	}
	cfg = &vpnRouteConfig{VrrpVlan: vpnVrrpVlan(gateway), VrrpGateway: vpnVrrpGateway(gateway), VrrpIPs: map[string]string{}, Prefixes: []*vpnRoutePrefix{}}
	for _, iface := range []*model.Interface{iface1, iface2} {
		if iface.Hyper < 0 || iface.Address == nil {
			continue
		}
		cfg.VrrpNodes = append(cfg.VrrpNodes, iface.Hyper)
		cfg.VrrpIPs[strconv.Itoa(int(iface.Hyper))] = ipOnly(iface.Address.Address)
		vrrpHypers = append(vrrpHypers, iface.Hyper)
	}
	master := iface1
	if gateway.MasterHyper >= 0 && iface2.Hyper == gateway.MasterHyper {
		master = iface2
	}
	if master.Address != nil {
		cfg.MasterIP = ipOnly(master.Address.Address)
	}
	connIface := map[int64]string{}
	for _, conn := range gateway.Connections {
		connIface[conn.ID] = vpnIpsecIface(conn)
	}
	for _, prefix := range prefixes {
		entry := &vpnRoutePrefix{Cidr: prefix.Cidr}
		switch prefix.Source {
		case model.VpnPrefixSourceConnectionStatic:
			entry.Type, entry.Target = "static", connIface[prefix.RefID]
		case model.VpnPrefixSourceConnectionSummary:
			entry.Type, entry.Target = "bgp", "blackhole"
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
	fipAddr, fipVlan, fipMark := "-", int64(0), int64(0)
	if fip := vpnFloatingIp(gateway); fip != nil {
		fipAddr, fipMark = fip.FipAddress, fip.ID
		if fip.Subnet != nil {
			fipVlan = fip.Subnet.Vlan
		}
	}
	vrrpHypers := map[int32]bool{}
	for _, iface := range []*model.Interface{iface1, iface2} {
		if iface.Hyper < 0 {
			continue
		}
		vrrpHypers[iface.Hyper] = true
		control := fmt.Sprintf("inter=%d", iface.Hyper)
		command := fmt.Sprintf(vpnScriptDir+"clear_vpn_gateway.sh '%d' '%d' '%s' '%d' '%d'", gateway.RouterID, gateway.ID, ShellEscape(fipAddr), fipVlan, fipMark)
		if err = HyperExecute(ctx, control, command); err != nil {
			logger.Ctx(ctx).Errorf("Failed to clear VPN gateway %d on hyper %d: %v", gateway.ID, iface.Hyper, err)
			return
		}
	}
	hypers, err := RouterHyperSet(ctx, gateway.RouterID)
	if err != nil {
		return
	}
	cfg := &vpnRouteConfig{VrrpVlan: vpnVrrpVlan(gateway), VrrpGateway: vpnVrrpGateway(gateway), VrrpIPs: map[string]string{}, Prefixes: []*vpnRoutePrefix{}}
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

// dispatchVpnRestart terminates and re-initiates one connection on the node holding the floating IP
func dispatchVpnRestart(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection) (err error) {
	control, err := vpnVrrpGroup(ctx, gateway)
	if err != nil {
		return
	}
	command := fmt.Sprintf(vpnScriptDir+"restart_vpn_conn.sh '%d' '%d' '%s'", gateway.RouterID, gateway.ID, ShellEscape(vpnConnName(conn)))
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to restart connection %d of VPN gateway %d: %v", conn.ID, gateway.ID, err)
	}
	return
}
