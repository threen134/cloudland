// Names of the public addresses (endpoints) of a VPN gateway and of the tunnels that start from them.
//
// active_standby: vip1 and optionally vip2, floating IPs that move with the VRRP master.
// active_active: node1 and node2, fixed addresses of the two nodes (each node runs the tunnels of its
// address), plus vip1 only as the floating IP of the client VPN.
import type { VpnPublicIp } from '../api/vpn'

type Translate = (key: string, params?: Record<string, unknown>) => string

export const isActiveActive = (gateway?: { ha_mode?: string } | null) => gateway?.ha_mode === 'active_active'

/**
 * An active_standby gateway running on one node: its zone had no second available node when it was built.
 * clapi adds the second node once the zone has one; until then a failure of that node stops the gateway.
 * A gateway still being built (pending) has one node for a moment too, so it does not count.
 */
export const isSingleNode = (gateway?: { ha_mode?: string; status?: string; nodes?: unknown[] | null } | null) =>
    !!gateway &&
    !isActiveActive(gateway) &&
    gateway.status !== 'pending' &&
    gateway.status !== 'deleting' &&
    (gateway.nodes?.length ?? 0) === 1

/** Endpoints a tunnel can start from, in endpoint order: vip1 / vip2, or node1 / node2 */
export const tunnelEndpoints = (publicIps: VpnPublicIp[], haMode?: string): VpnPublicIp[] =>
    publicIps.filter((p) =>
        haMode === 'active_active' ? p.endpoint === 'node1' || p.endpoint === 'node2' : p.endpoint.startsWith('vip')
    )

/**
 * The address the gateway may release, when it has it: vip2 of an active_standby gateway, the client VPN
 * address (vip1) of an active_active one. The backend still refuses while a tunnel or the client VPN uses it
 */
export const removableEndpoint = (gateway: { ha_mode?: string; public_ips?: VpnPublicIp[] | null }) => {
    const endpoint = isActiveActive(gateway) ? 'vip1' : 'vip2'
    return gateway.public_ips?.some((p) => p.endpoint === endpoint) ? endpoint : ''
}

/**
 * Name of an endpoint: "Public address 2", "Node 1", "Client VPN address". short gives "Address 2" for the
 * floating addresses of an active_standby gateway (tunnel details, where "public" is implied)
 */
export const endpointName = (t: Translate, endpoint: string, haMode?: string, short = false) => {
    if (endpoint === 'node1' || endpoint === 'node2') {
        return t('dashboard.vpnGateway.nodeN', { n: endpoint === 'node2' ? 2 : 1 })
    }
    if (endpoint === 'vip1' && haMode === 'active_active') return t('dashboard.vpnGateway.clientVpnAddress')
    return t(short ? 'dashboard.vpnGateway.addressN' : 'dashboard.vpnGateway.publicAddressN', {
        n: endpoint === 'vip2' ? 2 : 1,
    })
}

/** The endpoint name with the node that holds a fixed address: "Node 1 (work-02)" */
export const endpointLabel = (
    t: Translate,
    p: { endpoint: string; hostname?: string },
    haMode?: string,
    short = false
) =>
    p.hostname
        ? `${endpointName(t, p.endpoint, haMode, short)} (${p.hostname})`
        : endpointName(t, p.endpoint, haMode, short)
