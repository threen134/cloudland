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

const (
	// The VRRP subnet is hardcoded in CreateVrrpInstance; the router point-to-point links are
	// 169.<x>.<y>.<z>/31 (create_local_router.sh), so nothing routed through a gateway may fall in either
	vrrpSubnetCidr = "192.168.196.0/24"
	routerLinkCidr = "169.0.0.0/8"
)

// ParseCidrList parses a comma separated list of IPv4 CIDRs and returns them normalized (host bits
// cleared, duplicates dropped) so the stored value never carries the user's spelling
func ParseCidrList(value string) (cidrs []string, err error) {
	seen := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		ip, ipNet, perr := net.ParseCIDR(item)
		if perr != nil || ip.To4() == nil {
			return nil, NewCLError(ErrInvalidCIDR, "Invalid CIDR: "+item, perr)
		}
		norm := ipNet.String()
		if seen[norm] {
			continue
		}
		seen[norm] = true
		cidrs = append(cidrs, norm)
	}
	return
}

func cidrsOverlap(a, b string) bool {
	_, na, err := net.ParseCIDR(a)
	if err != nil {
		return false
	}
	_, nb, err := net.ParseCIDR(b)
	if err != nil {
		return false
	}
	return na.Contains(nb.IP) || nb.Contains(na.IP)
}

func joinCidrs(cidrs []string) string {
	return strings.Join(cidrs, ",")
}

// ipOnly strips the prefix length from an address stored as a.b.c.d/n
func ipOnly(address string) string {
	if idx := strings.Index(address, "/"); idx >= 0 {
		return address[:idx]
	}
	return address
}

// vpcInternalCidrs returns the networks of the VPC's subnets other than the VRRP one, in creation order.
// It is the default for local traffic selectors, BGP network statements and client AllowedIPs, and is
// computed at dispatch time so subnets added later are picked up.
func vpcInternalCidrs(ctx context.Context, routerID int64) (cidrs []string, err error) {
	ctx, db := GetContextDB(ctx)
	subnets := []*model.Subnet{}
	if err = db.Where("router_id = ? and type <> 'vrrp'", routerID).Order("id").Find(&subnets).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query subnets of router %d: %v", routerID, err)
		err = NewCLError(ErrDatabaseError, "Failed to query VPC subnets", err)
		return
	}
	for _, subnet := range subnets {
		if _, ipNet, perr := net.ParseCIDR(subnet.Network); perr == nil {
			cidrs = append(cidrs, ipNet.String())
		}
	}
	return
}

func vpnPrefixExcluded(prefix *model.VpnRemotePrefix, excludeRef int64, excludeSources []string) bool {
	if excludeRef <= 0 || prefix.RefID != excludeRef {
		return false
	}
	for _, source := range excludeSources {
		if prefix.Source == source {
			return true
		}
	}
	return false
}

// validateVpnPrefixes rejects prefixes that overlap the VPC subnets, the VRRP subnet, the router links,
// each other, or what the gateway already routes (except the rows of the object being updated, given by
// excludeRef and the sources it owns). A default route is never accepted here: on the other nodes it
// would swallow the VPC's internet traffic into the gateway.
func validateVpnPrefixes(ctx context.Context, routerID, gatewayID int64, cidrs []string, excludeRef int64, excludeSources ...string) (err error) {
	vpcCidrs, err := vpcInternalCidrs(ctx, routerID)
	if err != nil {
		return
	}
	existing := []*model.VpnRemotePrefix{}
	if gatewayID > 0 {
		_, db := GetContextDB(ctx)
		if err = db.Where("vpn_gateway_id = ?", gatewayID).Find(&existing).Error; err != nil {
			err = NewCLError(ErrDatabaseError, "Failed to query VPN prefixes", err)
			return
		}
	}
	conflict := func(cidr, with string) error {
		return NewCLError(ErrVpnCidrConflict, fmt.Sprintf("Network %s overlaps %s", cidr, with), nil)
	}
	for i, cidr := range cidrs {
		if cidr == "0.0.0.0/0" {
			return NewCLError(ErrVpnCidrConflict, "0.0.0.0/0 is not allowed as a remote network", nil)
		}
		for _, vpc := range vpcCidrs {
			if cidrsOverlap(cidr, vpc) {
				return conflict(cidr, "VPC subnet "+vpc)
			}
		}
		if cidrsOverlap(cidr, vrrpSubnetCidr) {
			return conflict(cidr, "the VRRP subnet "+vrrpSubnetCidr)
		}
		if cidrsOverlap(cidr, routerLinkCidr) {
			return conflict(cidr, "the router link range "+routerLinkCidr)
		}
		for _, prefix := range existing {
			if vpnPrefixExcluded(prefix, excludeRef, excludeSources) {
				continue
			}
			if cidrsOverlap(cidr, prefix.Cidr) {
				return conflict(cidr, fmt.Sprintf("%s (%s)", prefix.Cidr, prefix.Source))
			}
		}
		for j := 0; j < i; j++ {
			if cidrsOverlap(cidr, cidrs[j]) {
				return conflict(cidr, cidrs[j])
			}
		}
	}
	return
}

// validateTunnelLink checks the pair of addresses a BGP session runs on inside the tunnel. Unlike remote
// networks they may sit in 169.254.0.0/16 (public cloud VPN gateways require it); only the VPC subnets
// and the VRRP subnet are off limits. The node rejects a /31 that collides with its own router link.
func validateTunnelLink(ctx context.Context, routerID int64, localIP, peerIP string) (err error) {
	local := net.ParseIP(localIP)
	peer := net.ParseIP(peerIP)
	if local == nil || local.To4() == nil || peer == nil || peer.To4() == nil {
		return NewCLError(ErrInvalidParameter, "Tunnel link addresses must be IPv4", nil)
	}
	if local.Equal(peer) {
		return NewCLError(ErrInvalidParameter, "Tunnel link addresses must differ", nil)
	}
	link30 := &net.IPNet{IP: local.Mask(net.CIDRMask(30, 32)), Mask: net.CIDRMask(30, 32)}
	if !link30.Contains(peer) {
		return NewCLError(ErrInvalidParameter, "Tunnel link addresses must be in the same /30", nil)
	}
	vpcCidrs, err := vpcInternalCidrs(ctx, routerID)
	if err != nil {
		return
	}
	for _, vpc := range append(vpcCidrs, vrrpSubnetCidr) {
		_, ipNet, _ := net.ParseCIDR(vpc)
		if ipNet != nil && (ipNet.Contains(local) || ipNet.Contains(peer)) {
			return NewCLError(ErrVpnCidrConflict, "Tunnel link addresses overlap "+vpc, nil)
		}
	}
	return
}

// rebuildRemotePrefixes recomputes the derived prefix set of a gateway from its live connections and
// client pool and replaces the stored rows. Every dispatch of routes reads the result, so the set has
// one owner and one place where all the sources are merged.
func rebuildRemotePrefixes(ctx context.Context, gateway *model.VpnGateway) (prefixes []*model.VpnRemotePrefix, err error) {
	ctx, db := GetContextDB(ctx)
	conns := []*model.VpnConnection{}
	if err = db.Where("vpn_gateway_id = ?", gateway.ID).Order("id").Find(&conns).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to query VPN connections", err)
		return
	}
	for _, conn := range conns {
		source, value := model.VpnPrefixSourceConnectionStatic, conn.RemoteCidrs
		if conn.RouteMode == model.VpnRouteModeBgp {
			source, value = model.VpnPrefixSourceConnectionSummary, conn.RemoteSummaryCidrs
		}
		cidrs, perr := ParseCidrList(value)
		if perr != nil {
			continue
		}
		for _, cidr := range cidrs {
			prefixes = append(prefixes, &model.VpnRemotePrefix{VpnGatewayID: gateway.ID, Cidr: cidr, Source: source, RefID: conn.ID})
		}
	}
	if gateway.ClientEnabled && gateway.ClientCidr != "" {
		prefixes = append(prefixes, &model.VpnRemotePrefix{VpnGatewayID: gateway.ID, Cidr: gateway.ClientCidr, Source: model.VpnPrefixSourceClientPool, RefID: gateway.ID})
	}
	if err = db.Where("vpn_gateway_id = ?", gateway.ID).Delete(&model.VpnRemotePrefix{}).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to clear VPN prefixes", err)
		return
	}
	for _, prefix := range prefixes {
		if err = db.Create(prefix).Error; err != nil {
			err = NewCLError(ErrDatabaseError, "Failed to store VPN prefix", err)
			return
		}
	}
	return
}

func loadRemotePrefixes(ctx context.Context, gatewayID int64) (prefixes []*model.VpnRemotePrefix, err error) {
	ctx, db := GetContextDB(ctx)
	if err = db.Where("vpn_gateway_id = ?", gatewayID).Order("id").Find(&prefixes).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query VPN prefixes of gateway %d: %v", gatewayID, err)
		err = NewCLError(ErrDatabaseError, "Failed to query VPN prefixes", err)
	}
	return
}

// effectiveLocalCidrs is what a connection presents as its local side: the explicit list, or all the
// VPC's internal subnets when the field is empty
func effectiveLocalCidrs(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection) ([]string, error) {
	if strings.TrimSpace(conn.LocalCidrs) != "" {
		return ParseCidrList(conn.LocalCidrs)
	}
	return vpcInternalCidrs(ctx, gateway.RouterID)
}

// effectiveClientRoutes is what a client is told to send through the tunnel (AllowedIPs)
func effectiveClientRoutes(ctx context.Context, gateway *model.VpnGateway) ([]string, error) {
	if strings.TrimSpace(gateway.ClientRoutes) != "" {
		return ParseCidrList(gateway.ClientRoutes)
	}
	return vpcInternalCidrs(ctx, gateway.RouterID)
}

// Exported views for the API layer
func EffectiveClientRoutes(ctx context.Context, gateway *model.VpnGateway) ([]string, error) {
	return effectiveClientRoutes(ctx, gateway)
}

func EffectiveLocalCidrs(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection) ([]string, error) {
	return effectiveLocalCidrs(ctx, gateway, conn)
}

func LoadRemotePrefixes(ctx context.Context, gatewayID int64) ([]*model.VpnRemotePrefix, error) {
	return loadRemotePrefixes(ctx, gatewayID)
}
