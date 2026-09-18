import client from './client'

// 对应 clapi api/src/apis/hyper.go 的 HyperResponse
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
    // 该节点上的虚拟机数量，由后端一次分组统计得出（仅列表接口填充，详情恒为 0）
    instance_count: number
    cpu: number
    cpu_total: number
    memory: number
    memory_total: number
    disk: number
    disk_total: number
    // 仅 POST /hypers 返回（omitempty）
    deploy_command?: string
}

// 对应 hyper.go 的 HyperListResponse
export interface HyperListResponse {
    offset: number
    total: number
    limit: number
    hypers: Hypervisor[]
}

// 对应 hyper.go 的 HyperDeployPayload
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

// 对应 hyper.go 的 HyperPatchPayload（全部为指针，不传即不改）
export interface HyperPatchPayload {
    status?: number
    /** 可用区 UUID（后端按 UUID 解析，见 apis/hyper.go 的 HyperPatchPayload） */
    zone_id?: string
    cpu_over_rate?: number
    mem_over_rate?: number
    disk_over_rate?: number
    remark?: string
}

// common/http.go 的 ResourceReference，字段全部 omitempty
export interface ResourceReference {
    id?: string
    name?: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
}

// 对应 api/src/apis/console.go 的 HostConsoleResponse
export interface HostConsoleResponse {
    // 后端只填 id（hyper uuid）与 name（hostname）
    hyper: ResourceReference
    token: string
    console_url: string
    // Seconds without traffic after which the node closes the session
    idle_timeout: number
}

// 对应 console.go 的 HostConsolePayload，外加网关 host_console.go 校验并剥离的 password
export interface HostConsolePayload {
    password?: string
    rows?: number
    cols?: number
}

// 对应 hyper.go 的 HyperMaintainPayload；target_hyper 为指针，省略表示由调度器自选
export interface HyperMaintainPayload {
    target_hyper?: number
    migrate: boolean
}

// POST /hypers/{uuid}/maintain 返回 map[string]string{"result": "success"}
export interface HyperMaintainResponse {
    result: string
}

// 对应 api/src/apis/monitor.go 的 MetricsRequest（节点监控只用 hostname）
export interface HyperMetricsPayload {
    hostname: string[]
    start: string
    end: string
    step: string
}

export interface MetricSeriesLabels {
    domain: string
    instance: string
    job: string
    uuid?: string
}

export interface MetricPoint {
    time: string
    value: string
}

// 对应 monitor.go 的 CPUResponse：一维 values
export interface HyperCPUMetricsResponse {
    status: string
    data: {
        resultType: string
        label: string
        unit: string
        result: {
            metric: MetricSeriesLabels
            values: MetricPoint[]
        }[]
    }
}

// 对应 monitor.go 的 MemoryResponse：二维 values（[total, used]）
export interface HyperMemoryMetricsResponse {
    status: string
    data: {
        resultType: string
        chart_type: string
        label: string[]
        unit: string
        result: {
            metric: MetricSeriesLabels
            values: MetricPoint[][]
        }[]
    }
}

export const hypervisorsApi = {
    async fetchHypervisors(params?: { offset?: number; limit?: number; q?: string }): Promise<HyperListResponse> {
        const response = await client.get<HyperListResponse>('/hypers', { params })
        return response.data
    },

    async getHypervisor(uuid: string): Promise<Hypervisor> {
        const response = await client.get<Hypervisor>(`/hypers/${uuid}`)
        return response.data
    },

    async updateHypervisor(uuid: string, payload: HyperPatchPayload): Promise<Hypervisor> {
        const response = await client.patch<Hypervisor>(`/hypers/${uuid}`, payload)
        return response.data
    },

    async deployHypervisor(payload: HyperDeployPayload): Promise<Hypervisor> {
        const response = await client.post<Hypervisor>('/hypers', payload)
        return response.data
    },

    async maintainHypervisor(uuid: string, payload: HyperMaintainPayload): Promise<HyperMaintainResponse> {
        const response = await client.post<HyperMaintainResponse>(`/hypers/${uuid}/maintain`, payload)
        return response.data
    },

    // 后端返回 204 No Content
    async deleteHypervisor(uuid: string): Promise<void> {
        await client.delete(`/hypers/${uuid}`)
    },

    // Root shell on the hypervisor (system admins, when enabled in system settings). The gateway checks the
    // password again while HOST_CONSOLE_REQUIRE_PASSWORD is on, and answers 400 when it is missing;
    // rows and cols are the terminal size the shell starts with
    async openConsole(uuid: string, payload: HostConsolePayload): Promise<HostConsoleResponse> {
        const response = await client.post<HostConsoleResponse>(`/hypers/${uuid}/console`, payload)
        return response.data
    },

    // Monitoring Metrics
    async getCPUMetrics(payload: HyperMetricsPayload): Promise<HyperCPUMetricsResponse> {
        const response = await client.post<HyperCPUMetricsResponse>('/metrics/hypers/cpu/his_data', payload)
        return response.data
    },

    async getMemoryMetrics(payload: HyperMetricsPayload): Promise<HyperMemoryMetricsResponse> {
        const response = await client.post<HyperMemoryMetricsResponse>('/metrics/hypers/memory/his_data', payload)
        return response.data
    }
}
