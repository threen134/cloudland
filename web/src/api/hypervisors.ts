import client from './client'

export interface Hypervisor {
    uuid: string
    // 节点编号，迁移接口的 target_hyper 用它
    hostid: number
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
    // 该节点上的虚拟机数量，由后端一次分组统计得出
    instance_count: number
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

export interface HostConsoleResponse {
    hyper: { id: string; name?: string }
    token: string
    console_url: string
    // Seconds without traffic after which the node closes the session
    idle_timeout: number
}

export interface HyperMaintainPayload {
    target_hyper: number
    migrate: boolean
}

export const hypervisorsApi = {
    async fetchHypervisors(params?: { offset?: number; limit?: number; q?: string }) {
        const response = await client.get<HyperListResponse>('/hypers', { params })
        return response.data
    },

    async getHypervisor(uuid: string) {
        const response = await client.get<Hypervisor>(`/hypers/${uuid}`)
        return response.data
    },

    async updateHypervisor(uuid: string, payload: {
        status?: number
        zone_id?: number
        cpu_over_rate?: number
        mem_over_rate?: number
        disk_over_rate?: number
        remark?: string
    }) {
        const response = await client.patch<Hypervisor>(`/hypers/${uuid}`, payload)
        return response.data
    },

    async deployHypervisor(payload: HyperDeployPayload) {
        const response = await client.post<Hypervisor>('/hypers', payload)
        return response.data
    },

    async maintainHypervisor(uuid: string, payload: HyperMaintainPayload) {
        const response = await client.post(`/hypers/${uuid}/maintain`, payload)
        return response.data
    },

    async deleteHypervisor(uuid: string) {
        const response = await client.delete(`/hypers/${uuid}`)
        return response.data
    },

    // Root shell on the hypervisor (system admins, when enabled in system settings). The gateway checks the
    // password again while HOST_CONSOLE_REQUIRE_PASSWORD is on, and answers 400 when it is missing;
    // rows and cols are the terminal size the shell starts with
    async openConsole(uuid: string, payload: { password?: string; rows?: number; cols?: number }) {
        const response = await client.post<HostConsoleResponse>(`/hypers/${uuid}/console`, payload)
        return response.data
    },

    // Monitoring Metrics
    async getCPUMetrics(payload: { hostname: string[], start: string, end: string, step: string }) {
        const response = await client.post('/metrics/hypers/cpu/his_data', payload)
        return response.data
    },

    async getMemoryMetrics(payload: { hostname: string[], start: string, end: string, step: string }) {
        const response = await client.post('/metrics/hypers/memory/his_data', payload)
        return response.data
    }
}
