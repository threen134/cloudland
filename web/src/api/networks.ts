import client from './client'

// ============================================
// VPC Types and API
// ============================================
export interface VPC {
    id: string
    name: string
    status?: string
    subnets?: Subnet[]
    created_at?: string
    updated_at?: string
    owner?: string
}

export interface VPCPayload {
    name: string
}

export interface VPCListResponse {
    vpcs: VPC[]
    total: number
    limit: number
    offset: number
}

export const vpcsApi = {
    list: async (params?: { offset?: number; limit?: number }): Promise<VPCListResponse> => {
        const response = await client.get('/vpcs', { params })
        return response.data
    },
    get: async (id: string): Promise<VPC> => {
        const response = await client.get(`/vpcs/${id}`)
        return response.data
    },
    create: async (payload: VPCPayload): Promise<VPC> => {
        const response = await client.post('/vpcs', payload)
        return response.data
    },
    delete: async (id: string): Promise<void> => {
        await client.delete(`/vpcs/${id}`)
    }
}

// ============================================
// Subnet Types and API
// ============================================
export interface Subnet {
    id: string
    name: string
    network?: string
    network_cidr?: string
    gateway?: string
    netmask?: string
    start?: string
    end?: string
    start_ip?: string
    end_ip?: string
    dns?: string
    dhcp?: boolean
    type?: 'public' | 'internal' | 'private' | 'site'
    vpc?: { id: string; name: string }
    vlan?: number
    idle_count?: number
    total_count?: number
    allocated_count?: number
    reserved_count?: number
    available_count?: number
    priority?: number
    created_at?: string
    updated_at?: string
    owner?: string
}

export interface SubnetPayload {
    name: string
    network?: string
    network_cidr?: string
    gateway?: string
    netmask?: string
    start?: string
    end?: string
    start_ip?: string
    end_ip?: string
    dns?: string
    base_domain?: string
    dhcp?: boolean
    type?: 'public' | 'internal' | 'private' | 'site'
    vpc?: { id: string }
    group?: { id: string }
    vlan?: number
    priority?: number
}

export interface SubnetListResponse {
    subnets: Subnet[]
    total: number
    limit: number
    offset: number
}

export const subnetsApi = {
    list: async (params?: { offset?: number; limit?: number; vpc?: string }): Promise<SubnetListResponse> => {
        const response = await client.get('/subnets', { params })
        return response.data
    },
    get: async (id: string): Promise<Subnet> => {
        const response = await client.get(`/subnets/${id}`)
        return response.data
    },
    create: async (payload: SubnetPayload): Promise<Subnet> => {
        const response = await client.post('/subnets', payload)
        return response.data
    },
    patch: async (id: string, payload: Partial<SubnetPayload>): Promise<Subnet> => {
        const response = await client.patch(`/subnets/${id}`, payload)
        return response.data
    },
    delete: async (id: string): Promise<void> => {
        await client.delete(`/subnets/${id}`)
    },
    listAddresses: async (subnetId: string): Promise<{ total: number; addresses: any[] }> => {
        const response = await client.get(`/addresses/${subnetId}`)
        return response.data
    }
}

// ============================================
// Floating IP Types and API
// ============================================
export interface FloatingIP {
    id: string
    name?: string
    ip_address: string
    public_ip?: string
    status?: string
    instance?: { id: string; name: string }
    interface?: { id: string }
    target_interface?: {
        id: string;
        ip_address?: string;
        from_instance?: {
            id: string;
            hostname?: string;
        }
    }
    site_subnets?: Array<{ id: string; name: string }>
    created_at?: string
    updated_at?: string
    owner?: string
}

export interface FloatingIPPayload {
    name?: string
    site_subnets?: Array<{ id: string } | { name: string }>
}

export interface FloatingIPListResponse {
    floating_ips: FloatingIP[]
    total: number
    limit: number
    offset: number
}

export const floatingIpsApi = {
    list: async (params?: { offset?: number; limit?: number }): Promise<FloatingIPListResponse> => {
        const response = await client.get('/floating_ips', { params })
        return response.data
    },
    get: async (id: string): Promise<FloatingIP> => {
        const response = await client.get(`/floating_ips/${id}`)
        return response.data
    },
    create: async (payload: FloatingIPPayload): Promise<FloatingIP> => {
        const response = await client.post('/floating_ips', payload)
        return response.data
    },
    patch: async (id: string, payload: { name?: string; interface?: { id: string } | null }): Promise<FloatingIP> => {
        const response = await client.patch(`/floating_ips/${id}`, payload)
        return response.data
    },
    delete: async (id: string): Promise<void> => {
        await client.delete(`/floating_ips/${id}`)
    },
    attach: async (id: string, interfaceId: string): Promise<FloatingIP> => {
        const response = await client.patch(`/floating_ips/${id}`, {
            interface: { id: interfaceId }
        })
        return response.data
    },
    detach: async (id: string): Promise<FloatingIP> => {
        const response = await client.patch(`/floating_ips/${id}`, {
            interface: null
        })
        return response.data
    }
}

// ============================================
// Security Group Types and API
// ============================================
export interface SecurityRule {
    id: string
    name?: string
    direction: 'ingress' | 'egress'
    protocol: 'tcp' | 'udp' | 'icmp'
    port_min?: number
    port_max?: number
    remote_cidr?: string
    ip_version?: string
    created_at?: string
    owner?: string
}

export interface SecurityGroup {
    id: string
    name: string
    is_default?: boolean
    vpc?: { id: string; name: string }
    target_interfaces?: Array<{ id: string; name?: string }>
    security_rules?: SecurityRule[]
    created_at?: string
    updated_at?: string
    owner?: string
}

export interface SecurityGroupPayload {
    name: string
    is_default?: boolean
    vpc?: { id: string }
}

export interface SecurityRulePayload {
    name?: string
    direction: 'ingress' | 'egress'
    protocol: 'tcp' | 'udp' | 'icmp'
    port_min?: number
    port_max?: number
    remote_cidr?: string
}

export interface SecurityGroupListResponse {
    security_groups: SecurityGroup[]
    total: number
    limit: number
    offset: number
}

export const securityGroupsApi = {
    list: async (params?: { offset?: number; limit?: number }): Promise<SecurityGroupListResponse> => {
        const response = await client.get('/security_groups', { params })
        return response.data
    },
    get: async (id: string): Promise<SecurityGroup> => {
        const response = await client.get(`/security_groups/${id}`)
        return response.data
    },
    create: async (payload: SecurityGroupPayload): Promise<SecurityGroup> => {
        const response = await client.post('/security_groups', payload)
        return response.data
    },
    patch: async (id: string, payload: { name?: string; is_default?: boolean }): Promise<SecurityGroup> => {
        const response = await client.patch(`/security_groups/${id}`, payload)
        return response.data
    },
    delete: async (id: string): Promise<void> => {
        await client.delete(`/security_groups/${id}`)
    },
    // Security rules within a group
    addRule: async (groupId: string, rule: SecurityRulePayload): Promise<SecurityRule> => {
        const response = await client.post(`/security_groups/${groupId}/rules`, rule)
        return response.data
    },
    deleteRule: async (groupId: string, ruleId: string): Promise<void> => {
        await client.delete(`/security_groups/${groupId}/rules/${ruleId}`)
    }
}

// ============================================
// Load Balancer Types and API
// ============================================
export interface Backend {
    id: string
    name?: string
    address: string
    port: number
    weight?: number
    status?: string
}

export interface Listener {
    id: string
    name: string
    mode: 'http' | 'tcp'
    port: number
    status?: string
    backends?: Backend[]
    created_at?: string
    owner?: string
}

export interface LoadBalancer {
    id: string
    name: string
    status?: string
    vpc?: { id: string; name: string }
    floating_ips?: Array<{ id: string; ip_address: string }>
    listeners?: Listener[]
    created_at?: string
    updated_at?: string
    owner?: string
}

export interface LoadBalancerPayload {
    name: string
    vpc: { id: string }
    zone?: string
}

export interface ListenerPayload {
    name: string
    mode: 'http' | 'tcp'
    port: number
    cert?: string
    key?: string
}

export interface BackendPayload {
    name?: string
    address: string
    port: number
    weight?: number
}

export interface LoadBalancerListResponse {
    load_balancers: LoadBalancer[]
    total: number
    limit: number
    offset: number
}

export const loadBalancersApi = {
    list: async (params?: { offset?: number; limit?: number }): Promise<LoadBalancerListResponse> => {
        const response = await client.get('/load_balancers', { params })
        return response.data
    },
    get: async (id: string): Promise<LoadBalancer> => {
        const response = await client.get(`/load_balancers/${id}`)
        return response.data
    },
    create: async (payload: LoadBalancerPayload): Promise<LoadBalancer> => {
        const response = await client.post('/load_balancers', payload)
        return response.data
    },
    patch: async (id: string, payload: { name?: string; action?: 'enable' | 'disable' }): Promise<LoadBalancer> => {
        const response = await client.patch(`/load_balancers/${id}`, payload)
        return response.data
    },
    delete: async (id: string): Promise<void> => {
        await client.delete(`/load_balancers/${id}`)
    },
    // Listeners
    addListener: async (lbId: string, listener: ListenerPayload): Promise<Listener> => {
        const response = await client.post(`/load_balancers/${lbId}/listeners`, listener)
        return response.data
    },
    deleteListener: async (lbId: string, listenerId: string): Promise<void> => {
        await client.delete(`/load_balancers/${lbId}/listeners/${listenerId}`)
    },
    // Backends
    addBackend: async (lbId: string, listenerId: string, backend: BackendPayload): Promise<Backend> => {
        const response = await client.post(`/load_balancers/${lbId}/listeners/${listenerId}/backends`, backend)
        return response.data
    },
    deleteBackend: async (lbId: string, listenerId: string, backendId: string): Promise<void> => {
        await client.delete(`/load_balancers/${lbId}/listeners/${listenerId}/backends/${backendId}`)
    }
}

export default {
    vpcs: vpcsApi,
    subnets: subnetsApi,
    floatingIps: floatingIpsApi,
    securityGroups: securityGroupsApi,
    loadBalancers: loadBalancersApi
}
