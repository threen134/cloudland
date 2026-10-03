import client from './client'

// VPN gateway API. The shapes mirror the Go structs in api/src/apis/vpn.go
// (VpnGatewayResponse, VpnConnectionResponse, VpnClientResponse and their payloads).

export type VpnGatewayStatus = 'pending' | 'available' | 'error' | 'deleting'
// degraded: traffic still flows but not over every tunnel it should (preferred: only a standby is up;
// ecmp: some tunnels are down)
export type VpnConnectionStatus = 'pending' | 'down' | 'up' | 'degraded' | 'disabled' | 'error'
export type VpnTunnelStatus = 'pending' | 'down' | 'up' | 'disabled'
export type VpnRouteMode = 'static' | 'bgp'
export type VpnDpdAction = 'restart' | 'clear' | 'none'
// active_standby: the VRRP master runs every tunnel from one or two floating IPs (vip1, vip2).
// active_active: each of the two nodes runs its own tunnels from a fixed address (node1, node2) and the
// nodes exchange routes over iBGP; vip1 then only exists as the floating IP of the client VPN
export type VpnHaMode = 'active_standby' | 'active_active'
export type VpnEndpoint = 'vip1' | 'vip2' | 'node1' | 'node2'
export type VpnTunnelPriority = 'primary' | 'standby'
// preferred: the primary tunnel carries the traffic, the others stand by in order;
// ecmp: every tunnel that is up shares the traffic (flows hashed on addresses and ports)
export type VpnTrafficPolicy = 'preferred' | 'ecmp'

// One public address of a gateway. Floating ones (vip1, vip2) move with the VRRP master; a fixed one
// (node1, node2) belongs to one node of an active_active gateway
export interface VpnPublicIp {
    endpoint: VpnEndpoint | string
    address: string
    // The node of a fixed address, -1 for a floating one
    hostid?: number
    hostname?: string
}

// One of the two HA nodes of a gateway (the VRRP master / backup pair)
export interface VpnNodeInfo {
    hostid: number
    hostname: string
    role: 'MASTER' | 'BACKUP' | string
    // True for the node currently holding the public address
    master: boolean
}

// A remote network routed through the gateway; source tells where it came from (static connection, BGP, client pool)
export interface VpnPrefix {
    cidr: string
    source: string
    ref_id: number
}

// BGP snapshot reported by the master node for a BGP-routed connection (display only).
// The lists are Go slices and come back as null when empty.
export interface VpnBgpReport {
    name: string
    state: string
    uptime: string
    prefixes_received: number
    prefixes_sent: number
    // Prefixes the peer advertised inside remote_summary_cidrs
    accepted: string[] | null
    // Prefixes the peer advertised outside remote_summary_cidrs: the summary networks are too narrow
    rejected: string[] | null
    // Local networks actually advertised to the peer (a subnet without an instance is not advertised)
    advertised: string[] | null
    truncated: boolean
    // BFD session state (up, down, init) when BFD is enabled on the connection
    bfd?: string
}

// One tunnel of a site connection: its configuration and what the master node reports for it
export interface VpnTunnel {
    id: string
    // 1 to 4: the position in the tunnel list, also what restart ?tunnel=N takes
    slot: number
    endpoint: VpnEndpoint | string
    // Our public address of this tunnel (the address of its endpoint)
    public_ip: string
    // The node running the tunnel on an active_active gateway, -1 otherwise (the master runs it)
    hostid?: number
    hostname?: string
    // Every tunnel is primary with traffic_policy ecmp
    priority: VpnTunnelPriority | string
    // Empty when the gateway only responds to the peer on this tunnel
    remote_gateway: string
    remote_id: string
    // A tunnel-specific key exists; otherwise the tunnel uses the connection's key
    psk_set: boolean
    // The tunnel-specific key itself, only for members with write permission on the gateway
    psk?: string
    // BGP mode: the /30 pair of the BGP session inside this tunnel
    tunnel_local_ip: string
    tunnel_peer_ip: string
    if_id: number
    status: VpnTunnelStatus | string
    established_at?: string
    bytes_in: number
    bytes_out: number
    last_error: string
    bgp?: VpnBgpReport | null
    bgp_reported_at?: string
    bfd_state?: string
}

export interface VpnConnection {
    id: string
    name: string
    description?: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
    // Aggregate of the tunnels: up while the primary is up (ecmp: every tunnel), degraded while only a
    // standby is (ecmp: some of the tunnels)
    status: VpnConnectionStatus | string
    // Empty on connections created before the field existed: preferred
    traffic_policy?: VpnTrafficPolicy | string
    local_id: string
    route_mode: VpnRouteMode | string
    // Comma separated; empty means all VPC internal subnets (see effective_local_cidrs)
    local_cidrs: string
    effective_local_cidrs: string[] | null
    remote_cidrs: string
    remote_summary_cidrs: string
    max_prefixes: number
    local_asn: number
    peer_asn: number
    bgp_password_set: boolean
    bgp_keepalive: number
    bgp_hold: number
    auth_method: string
    psk_set: boolean
    // The keys themselves, only for members with write permission on the gateway
    psk?: string
    bgp_password?: string
    ike_version: number
    ike_proposal: string
    esp_proposal: string
    ike_lifetime: number
    esp_lifetime: number
    dpd_action: VpnDpdAction | string
    dpd_delay: number
    initiator: boolean
    // BGP with two tunnels: how many times the standby tunnel repeats the local ASN (0 = none)
    as_path_prepend: number
    bfd_enabled: boolean
    // Milliseconds
    bfd_interval: number
    bfd_multiplier: number
    // Sums over the tunnels
    bytes_in: number
    bytes_out: number
    tunnels: VpnTunnel[] | null
}

export interface VpnClient {
    id: string
    name: string
    description?: string
    created_at?: string
    updated_at?: string
    protocol: string
    ip_address: string
    public_key: string
    preshared_key_set: boolean
    enabled: boolean
    last_handshake_at?: string
    bytes_in: number
    bytes_out: number
}

// Returned by the create call only: the private key (when generated by the platform) is never stored
export interface VpnClientCreateResponse extends VpnClient {
    private_key?: string
    config: string
}

export interface VpnGateway {
    id: string
    name: string
    description?: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
    status: VpnGatewayStatus | string
    // Why the gateway is in error (a node failed to build it, no node could take it); absent otherwise
    status_reason?: string
    // Empty on gateways created before the field existed: active_standby
    ha_mode?: VpnHaMode | string
    vpc?: { id: string; name: string }
    zone?: string
    floating_ips?: Array<{ id: string; name?: string; fip_address?: string }>
    // vip1, or node1 on an active_active gateway without vip1
    public_ip: string
    // Every address with its endpoint name, in endpoint order (vip1, vip2, node1, node2)
    public_ips: VpnPublicIp[] | null
    // The address POST .../public_ips would add: vip2 (active_standby with one address), vip1 (the client
    // VPN address of an active_active gateway), '' when the gateway has it already
    addable_endpoint?: VpnEndpoint | ''
    /** false: the gateway is paused (tunnels, BGP and WireGuard stopped, public IP kept) */
    enabled: boolean
    ipsec_enabled: boolean
    client_enabled: boolean
    client_protocol: string
    client_cidr: string
    client_port: number
    client_public_key: string
    client_dns: string
    // Comma separated; empty means all VPC internal subnets (see effective_client_routes)
    client_routes: string
    effective_client_routes: string[] | null
    master_hyper: number
    master_hostname: string
    master_reported_at?: string
    nodes: VpnNodeInfo[] | null
    connection_count: number
    client_count: number
    // Detail only (omitted by the list call)
    connections?: VpnConnection[]
    clients?: VpnClient[]
    remote_prefixes?: VpnPrefix[]
}

export interface VpnGatewayListResponse {
    offset: number
    total: number
    limit: number
    vpn_gateways: VpnGateway[]
}

export interface VpnGatewayPayload {
    name: string
    description?: string
    vpc: { id: string }
    zone?: string
    // Default active_standby; fixed at creation
    ha_mode?: VpnHaMode
    public_subnet?: { id: string }
    public_ip?: string
    // At most three entries. active_standby: one or two (vip1, vip2). active_active: node1, node2 and, only
    // with client_enabled, the client VPN address; missing entries come from the subnet of the first one.
    // public_subnet / public_ip above are the short form of a single entry and are ignored when this is given
    public_ips?: VpnPublicIpPayload[]
    inbound?: number
    outbound?: number
    ipsec_enabled?: boolean
    client_enabled?: boolean
    client_cidr?: string
    client_port?: number
    client_dns?: string
    client_routes?: string
}

// An empty entry lets clapi pick the subnet and the address
export interface VpnPublicIpPayload {
    public_subnet?: { id: string }
    public_ip?: string
}

export interface VpnGatewayPatchPayload {
    name?: string
    description?: string
    enabled?: boolean
    ipsec_enabled?: boolean
    client_enabled?: boolean
    client_cidr?: string
    client_port?: number
    client_dns?: string
    client_routes?: string
}

// One tunnel of a connection; the list position is the slot. endpoint defaults to the gateway's tunnel
// addresses in turn (vip1, vip2 / node1, node2); priority defaults to primary for one tunnel (on an
// active_active gateway the one on the node carrying fewer primaries) and is ignored with ecmp. psk
// overrides the connection's key for this tunnel; on update an entry without psk keeps the key of its
// slot and an empty psk removes it
export interface VpnTunnelPayload {
    endpoint?: VpnEndpoint
    priority?: VpnTunnelPriority
    remote_gateway?: string
    remote_id?: string
    psk?: string
    tunnel_local_ip?: string
    tunnel_peer_ip?: string
}

// psk is required on create; psk and bgp_password are write-only. clapi still accepts a single tunnel
// as top-level remote_gateway / remote_id / tunnel_local_ip / tunnel_peer_ip, the UI always sends tunnels
export interface VpnConnectionPayload {
    name: string
    description?: string
    // Up to four tunnels (two on an active_active gateway, one per node); on update the list replaces
    // the stored one slot by slot
    tunnels?: VpnTunnelPayload[]
    // Default preferred
    traffic_policy?: VpnTrafficPolicy
    local_id?: string
    route_mode?: VpnRouteMode
    local_cidrs?: string
    remote_cidrs?: string
    remote_summary_cidrs?: string
    max_prefixes?: number
    local_asn?: number
    peer_asn?: number
    bgp_password?: string
    bgp_keepalive?: number
    bgp_hold?: number
    psk?: string
    ike_proposal?: string
    esp_proposal?: string
    ike_lifetime?: number
    esp_lifetime?: number
    dpd_action?: VpnDpdAction
    dpd_delay?: number
    initiator?: boolean
    // BGP only: 0-10, default 2 (matters with two tunnels)
    as_path_prepend?: number
    bfd_enabled?: boolean
    // 300-60000 ms, default 1000
    bfd_interval?: number
    // 2-50, default 3
    bfd_multiplier?: number
}

// Every field is optional on update; an omitted field keeps its stored value
export type VpnConnectionPatchPayload = Partial<VpnConnectionPayload>

export interface VpnClientPayload {
    name: string
    description?: string
    // Empty: the platform generates the key pair and returns the private key once
    public_key?: string
    preshared_key?: boolean
    enabled?: boolean
}

export interface VpnClientPatchPayload {
    name?: string
    description?: string
    enabled?: boolean
}

export interface VpnClientConfigResponse {
    // WireGuard config without the private key (placeholder <your private key>)
    config: string
}

export const vpnGatewaysApi = {
    list: async (params?: {
        offset?: number
        limit?: number
        order?: string
        query?: string
        vpc_id?: string
    }): Promise<VpnGatewayListResponse> => {
        const response = await client.get('/vpn_gateways', { params })
        return response.data
    },
    get: async (id: string): Promise<VpnGateway> => {
        const response = await client.get(`/vpn_gateways/${id}`)
        return response.data
    },
    create: async (payload: VpnGatewayPayload): Promise<VpnGateway> => {
        const response = await client.post('/vpn_gateways', payload)
        return response.data
    },
    update: async (id: string, payload: VpnGatewayPatchPayload): Promise<VpnGateway> => {
        const response = await client.patch(`/vpn_gateways/${id}`, payload)
        return response.data
    },
    delete: async (id: string): Promise<void> => {
        await client.delete(`/vpn_gateways/${id}`)
    },
    /** Adds the gateway's addable_endpoint address (1 public IP of the quota); returns the updated gateway */
    addPublicIp: async (id: string, payload: VpnPublicIpPayload): Promise<VpnGateway> => {
        const response = await client.post(`/vpn_gateways/${id}/public_ips`, payload)
        return response.data
    },
    /** Releases the addable address once nothing uses it (vip2: no tunnel on it; vip1: client VPN off) */
    removePublicIp: async (id: string, endpoint: string): Promise<void> => {
        await client.delete(`/vpn_gateways/${id}/public_ips/${endpoint}`)
    },
    /**
     * Traffic history in bits per second (GET /vpn_gateways/:id/traffic); start / end in unix seconds.
     * by=tunnel returns one connection series per tunnel instead of one per connection
     */
    traffic: async (
        id: string,
        params: { start: number; end: number; step: string; by?: 'connection' | 'tunnel' }
    ): Promise<VpnTrafficResponse> => {
        const response = await client.get(`/vpn_gateways/${id}/traffic`, { params })
        return response.data
    },
}

/** One site connection or client: rates aligned with VpnTrafficResponse.timestamps, null where no sample */
export interface VpnTrafficSeries {
    /** the connection or client; the tunnel with by=tunnel */
    id: string
    /** by=tunnel: "<connection name> / t<slot>" */
    name: string
    /** from the site / from the client, bits per second */
    in: Array<number | null>
    /** to the site / to the client, bits per second */
    out: Array<number | null>
    /** by=tunnel only: the connection UUID and the tunnel slot of the series */
    connection_id?: string
    slot?: number
}

export interface VpnTrafficResponse {
    start: number
    end: number
    step: string
    timestamps: number[]
    connections: VpnTrafficSeries[]
    clients: VpnTrafficSeries[]
}

export const vpnConnectionsApi = {
    list: async (gatewayId: string): Promise<{ total: number; connections: VpnConnection[] }> => {
        const response = await client.get(`/vpn_gateways/${gatewayId}/connections`)
        return response.data
    },
    get: async (gatewayId: string, connId: string): Promise<VpnConnection> => {
        const response = await client.get(`/vpn_gateways/${gatewayId}/connections/${connId}`)
        return response.data
    },
    create: async (gatewayId: string, payload: VpnConnectionPayload): Promise<VpnConnection> => {
        const response = await client.post(`/vpn_gateways/${gatewayId}/connections`, payload)
        return response.data
    },
    update: async (gatewayId: string, connId: string, payload: VpnConnectionPatchPayload): Promise<VpnConnection> => {
        const response = await client.patch(`/vpn_gateways/${gatewayId}/connections/${connId}`, payload)
        return response.data
    },
    delete: async (gatewayId: string, connId: string): Promise<void> => {
        await client.delete(`/vpn_gateways/${gatewayId}/connections/${connId}`)
    },
    // Terminates and re-initiates the IKE SAs: every tunnel, or only the one in the given slot (1 to 4)
    restart: async (gatewayId: string, connId: string, tunnel?: number): Promise<VpnConnection> => {
        const response = await client.post(
            `/vpn_gateways/${gatewayId}/connections/${connId}/restart`,
            undefined,
            tunnel ? { params: { tunnel } } : undefined
        )
        return response.data
    },
}

export const vpnClientsApi = {
    list: async (gatewayId: string): Promise<{ total: number; clients: VpnClient[] }> => {
        const response = await client.get(`/vpn_gateways/${gatewayId}/clients`)
        return response.data
    },
    get: async (gatewayId: string, clientId: string): Promise<VpnClient> => {
        const response = await client.get(`/vpn_gateways/${gatewayId}/clients/${clientId}`)
        return response.data
    },
    create: async (gatewayId: string, payload: VpnClientPayload): Promise<VpnClientCreateResponse> => {
        const response = await client.post(`/vpn_gateways/${gatewayId}/clients`, payload)
        return response.data
    },
    update: async (gatewayId: string, clientId: string, payload: VpnClientPatchPayload): Promise<VpnClient> => {
        const response = await client.patch(`/vpn_gateways/${gatewayId}/clients/${clientId}`, payload)
        return response.data
    },
    delete: async (gatewayId: string, clientId: string): Promise<void> => {
        await client.delete(`/vpn_gateways/${gatewayId}/clients/${clientId}`)
    },
    // Config template without the private key, for a client whose key was generated earlier or brought by the user
    config: async (gatewayId: string, clientId: string): Promise<VpnClientConfigResponse> => {
        const response = await client.get(`/vpn_gateways/${gatewayId}/clients/${clientId}/config`)
        return response.data
    },
}

export default {
    gateways: vpnGatewaysApi,
    connections: vpnConnectionsApi,
    clients: vpnClientsApi,
}
