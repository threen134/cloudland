import client from './client'

export interface Instance {
    id: string
    name: string
    hostname?: string
    status: string
    ip_address?: string
    image?: { id: string, name: string }
    flavor?: {
        id: string
        name: string
        cpu: number
        memory: number
        disk?: number
    }
    vpc?: { id: string, name: string }
    zone?: string
    owner?: string
    hypervisor?: string
    root_passwd?: string
    login_port?: number
    created_at?: string
    keys?: any[]
    interfaces?: {
        id: string
        name?: string
        ip_address?: string
        mac_address?: string
        subnet?: { id: string, name: string }
        is_primary?: boolean
        floating_ips?: any[]
        security_groups?: { id: string, name: string }[]
    }[]
    volumes?: {
        id: string
        name?: string
        size?: number
        device?: string
        target?: string
        booting?: boolean
        boot_index?: number
        volume_type?: string
        created_at?: string
    }[]
    [key: string]: any
}

export interface BaseReference {
    id: string
    name?: string
}

export interface InterfacePayload {
    subnet?: BaseReference
    subnets?: BaseReference[]
    ip_address?: string
    mac_address?: string
    public_addresses?: BaseReference[]
    count?: number
    security_groups?: BaseReference[]
    allow_spoofing?: boolean
    inbound?: number
    outbound?: number
}

export interface CreateInstancePayload {
    count?: number
    hypervisor?: number
    hostname: string
    keys?: BaseReference[]
    root_passwd?: string
    login_port?: number
    cpu?: number
    memory?: number
    disk?: number
    disk_iops_limit?: number
    disk_bps_limit?: number
    flavor?: string
    image: BaseReference
    primary_interface: InterfacePayload
    secondary_interfaces?: InterfacePayload[]
    zone: string
    vpc?: BaseReference
    userdata?: string
    userdata_type?: string
    nested_enable?: boolean
    pool_id?: string
}


export const instancesApi = {
    // List instances
    // hyper：按所在计算节点过滤（host id）；不传表示不过滤
    async fetchInstances(params?: { offset?: number; limit?: number; query?: string; hyper?: number }) {
        const response = await client.get('/instances', { params })
        return response.data
    },

    // Get single instance
    async getInstance(id: string) {
        const response = await client.get(`/instances/${id}`)
        return response.data
    },

    // Create instance
    async createInstance(payload: CreateInstancePayload) {
        const response = await client.post('/instances', payload)
        return response.data
    },

    // Delete instance
    async deleteInstance(id: string) {
        const response = await client.delete(`/instances/${id}`)
        return response.data
    },

    // Instance Actions
    async startInstance(id: string, hostname: string = '') {
        const response = await client.patch(`/instances/${id}`, { hostname, power_action: 'start' })
        return response.data
    },

    async stopInstance(id: string, hostname: string = '') {
        const response = await client.patch(`/instances/${id}`, { hostname, power_action: 'stop' })
        return response.data
    },

    async rebootInstance(id: string, hostname: string = '') {
        const response = await client.patch(`/instances/${id}`, { hostname, power_action: 'restart' })
        return response.data
    },

    async hardStopInstance(id: string, hostname: string = '') {
        const response = await client.patch(`/instances/${id}`, { hostname, power_action: 'hard_stop' })
        return response.data
    },

    async hardRebootInstance(id: string, hostname: string = '') {
        const response = await client.patch(`/instances/${id}`, { hostname, power_action: 'hard_restart' })
        return response.data
    },

    async pauseInstance(id: string, hostname: string = '') {
        const response = await client.patch(`/instances/${id}`, { hostname, power_action: 'pause' })
        return response.data
    },

    async resumeInstance(id: string, hostname: string = '') {
        const response = await client.patch(`/instances/${id}`, { hostname, power_action: 'resume' })
        return response.data
    },

    async resizeInstance(id: string, cpu: number, memory: number) {
        const response = await client.post(`/instances/${id}/resize`, { cpu, memory })
        return response.data
    },

    // type: vnc (graphical, default) or serial (text console)
    async getConsole(id: string, type?: 'vnc' | 'serial') {
        const response = await client.post(`/instances/${id}/console`, type ? { type } : undefined)
        return response.data
    },

    async setUserPassword(id: string, user_name: string, password: string) {
        const response = await client.post(`/instances/${id}/set_user_password`, { user_name, password })
        return response.data
    },

    async reinstallInstance(id: string, payload: { image?: { id: string }, password?: string, keys?: { id: string }[], flavor?: string, login_port?: number }) {
        const response = await client.post(`/instances/${id}/reinstall`, payload)
        return response.data
    },

    async renameInstance(id: string, hostname: string) {
        const response = await client.patch(`/instances/${id}`, { hostname })
        return response.data
    },

    async getInterfaces(instanceId: string) {
        const response = await client.get<{ interfaces: { id: string; name: string; ip_address?: string; is_primary?: boolean }[] }>(`/instances/${instanceId}/interfaces`)
        return response.data
    },

    async patchInterface(instanceId: string, ifaceId: string, payload: { security_groups?: BaseReference[] }) {
        const response = await client.patch(`/instances/${instanceId}/interfaces/${ifaceId}`, payload)
        return response.data
    },

    // Monitoring Metrics
    async getCPUMetrics(payload: { id: string[], start: string, end: string, step: string }) {
        const response = await client.post('/metrics/instances/cpu/his_data', payload)
        return response.data
    },

    async getMemoryMetrics(payload: { id: string[], start: string, end: string, step: string }) {
        const response = await client.post('/metrics/instances/memory/his_data', payload)
        return response.data
    },

    async getDiskMetrics(payload: { id: string[], disk: string[], start: string, end: string, step: string }) {
        const response = await client.post('/metrics/instances/disk/his_data', payload)
        return response.data
    },

    async getNetworkMetrics(payload: { interface_ids: string[], start: string, end: string, step: string }) {
        const response = await client.post('/metrics/instances/network/his_data', payload)
        return response.data
    }
}
