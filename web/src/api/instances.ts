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
    fetchInstances() {
        return client.get('/instances')
    },

    // Get single instance
    getInstance(id: string) {
        return client.get(`/instances/${id}`)
    },

    // Create instance
    createInstance(payload: CreateInstancePayload) {
        return client.post('/instances', payload)
    },

    // Delete instance
    deleteInstance(id: string) {
        return client.delete(`/instances/${id}`)
    },

    // Instance Actions
    startInstance(id: string, hostname: string = '') {
        return client.patch(`/instances/${id}`, { hostname, power_action: 'start' })
    },

    stopInstance(id: string, hostname: string = '') {
        return client.patch(`/instances/${id}`, { hostname, power_action: 'stop' })
    },

    rebootInstance(id: string, hostname: string = '') {
        return client.patch(`/instances/${id}`, { hostname, power_action: 'restart' })
    },

    hardStopInstance(id: string, hostname: string = '') {
        return client.patch(`/instances/${id}`, { hostname, power_action: 'hard_stop' })
    },

    hardRebootInstance(id: string, hostname: string = '') {
        return client.patch(`/instances/${id}`, { hostname, power_action: 'hard_restart' })
    },

    pauseInstance(id: string, hostname: string = '') {
        return client.patch(`/instances/${id}`, { hostname, power_action: 'pause' })
    },

    resumeInstance(id: string, hostname: string = '') {
        return client.patch(`/instances/${id}`, { hostname, power_action: 'resume' })
    },

    resizeInstance(id: string, cpu: number, memory: number) {
        return client.post(`/instances/${id}/resize`, { cpu, memory })
    },

    getConsole(id: string) {
        return client.post(`/instances/${id}/console`)
    },

    setUserPassword(id: string, user_name: string, password: string) {
        return client.post(`/instances/${id}/set_user_password`, { user_name, password })
    },

    reinstallInstance(id: string, payload: { image?: { id: string }, password?: string, keys?: { id: string }[], flavor?: string, login_port?: number }) {
        return client.post(`/instances/${id}/reinstall`, payload)
    },

    renameInstance(id: string, hostname: string) {
        return client.patch(`/instances/${id}`, { hostname })
    }
}
