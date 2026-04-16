import client from './client'

export interface Hypervisor {
    uuid: string
    hostname: string
    status: number
    status_name: string
    children: number
    host_ip: string
    route_ip: string
    virt_type: string
    cpu_over_rate: number
    mem_over_rate: number
    disk_over_rate: number
    zone_name: string
    remark: string
    cpu: number
    cpu_total: number
    memory: number
    memory_total: number
    disk: number
    disk_total: number
    deploy_command?: string
}

export interface HyperListResponse {
    offset: number
    total: number
    limit: number
    hypers: Hypervisor[]
}

export interface HyperDeployPayload {
    ip: string
    hostname: string
    network_device?: string
    vlan_device?: string
    private_vlan_device?: string
    dns_server?: string
    domain?: string
    zone_name?: string
    virt_type?: string
}

export interface HyperMaintainPayload {
    target_hyper: number
    migrate: boolean
}

export const hypervisorsApi = {
    fetchHypervisors(params?: { offset?: number; limit?: number; q?: string }) {
        return client.get<HyperListResponse>('/hypers', { params })
    },

    getHypervisor(uuid: string) {
        return client.get<Hypervisor>(`/hypers/${uuid}`)
    },

    updateHypervisor(uuid: string, payload: {
        status?: number
        zone_id?: number
        cpu_over_rate?: number
        mem_over_rate?: number
        disk_over_rate?: number
        remark?: string
    }) {
        return client.patch<Hypervisor>(`/hypers/${uuid}`, payload)
    },

    deployHypervisor(payload: HyperDeployPayload) {
        return client.post<Hypervisor>('/hypers', payload)
    },

    maintainHypervisor(uuid: string, payload: HyperMaintainPayload) {
        return client.post(`/hypers/${uuid}/maintain`, payload)
    },

    deleteHypervisor(uuid: string) {
        return client.delete(`/hypers/${uuid}`)
    },

    // Monitoring Metrics
    getCPUMetrics(payload: { hostname: string[], start: string, end: string, step: string }) {
        return client.post('/metrics/hypers/cpu/his_data', payload)
    },

    getMemoryMetrics(payload: { hostname: string[], start: string, end: string, step: string }) {
        return client.post('/metrics/hypers/memory/his_data', payload)
    }
}
