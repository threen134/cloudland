import client from './client'

// 对应 api/src/common/http.go 的 BaseReference
export interface BaseReference {
    id: string
    name?: string
}

// 对应 api/src/common/http.go 的 ResourceReference（所有字段都是 omitempty）
export interface ResourceReference {
    id: string
    name?: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
}

// 对应 api/src/apis/interface.go 的 AddressInfo
export interface AddressInfo {
    ip_address: string
    subnet: ResourceReference
}

// 对应 api/src/apis/floatingip.go 的 FloatingIpInfo
export interface FloatingIpInfo extends ResourceReference {
    ip_address: string
    fip_address: string
    group?: BaseReference
    vlan?: number
    type?: string
}

// 对应 api/src/apis/subnet.go 的 SiteSubnetInfo
export interface SiteSubnetInfo extends ResourceReference {
    network: string
    netmask: string
    gateway: string
    start: string
    end: string
    group?: BaseReference
    vlan: number
}

// 对应 api/src/apis/interface.go 的 InterfaceResponse（内嵌 BaseReference + AddressInfo）
export interface InstanceInterface extends BaseReference, AddressInfo {
    secondary_addresses?: AddressInfo[]
    mac_address: string
    is_primary: boolean
    inbound: number
    outbound: number
    site_subnets?: SiteSubnetInfo[]
    floating_ips?: FloatingIpInfo[]
    security_groups?: ResourceReference[]
}

export interface InterfaceListResponse {
    offset: number
    total: number
    limit: number
    interfaces: InstanceInterface[]
}

// 对应 api/src/apis/volume.go 的 VolumeInfoResponse（内嵌 ResourceReference）
export interface InstanceVolume extends ResourceReference {
    target: string
    booting: boolean
}

// 对应 api/src/apis/instance.go 的 InstanceResponse（内嵌 ResourceReference）
export interface Instance extends ResourceReference {
    hostname: string
    status: string
    login_port: number
    interfaces: InstanceInterface[]
    volumes: InstanceVolume[]
    cpu: number
    memory: number
    disk: number
    // 后端返回的是规格名字符串，不是对象
    flavor: string
    image: ResourceReference | null
    keys: ResourceReference[]
    root_passwd?: string
    passwd_login: boolean
    zone: string
    vpc?: ResourceReference
    hypervisor?: string
    reason: string
}

export interface InstanceListResponse {
    offset: number
    total: number
    limit: number
    instances: Instance[]
}

// 对应 api/src/apis/console.go 的 ConsoleResponse
export interface ConsoleResponse {
    instance: ResourceReference
    token: string
    console_url: string
    console_host: string
    console_port: number
    console_path: string
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
    /** 宿主机 uuid（后端 binding:"omitempty,uuid"） */
    hypervisor?: string
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

// 监控指标：对应 api/src/apis/monitor.go 的 CPUResponse / MemoryResponse / DiskResponse / NetworkResponse
export interface MetricValue {
    time: string
    value: string
}

export interface CPUMetricsResponse {
    status: string
    data: {
        resultType: string
        label: string
        unit: string
        result: {
            metric: { domain: string; instance: string; job: string; uuid?: string }
            values: MetricValue[]
        }[]
    }
}

export interface MemoryMetricsResponse {
    status: string
    data: {
        resultType: string
        chart_type: string
        label: string[]
        unit: string
        result: {
            metric: { domain: string; instance: string; job: string; uuid?: string }
            // 二维数组 [total, used]
            values: MetricValue[][]
        }[]
    }
}

export interface DiskMetricsResponse {
    status: string
    data: {
        chart_type: string
        label: string[]
        unit: string
        result: {
            metric: { domain: string; instance: string; job: string; target_device: string }
            // 二维数组 [read, write]
            values: MetricValue[][]
        }[]
    }
}

export interface NetworkMetricsResponse {
    status: string
    data: {
        chart_type: string
        label: string[]
        unit: string
        resultType: string
        result: {
            metric: {
                domain: string
                instance: string
                job: string
                target_device: string
                interface_id: string
            }
            // 二维数组 [receive, transmit]
            values: MetricValue[][]
        }[]
    }
}

export const instancesApi = {
    // List instances
    // hyper：按所在计算节点过滤（host id）；不传表示不过滤
    async fetchInstances(params?: {
        offset?: number
        limit?: number
        order?: string
        query?: string
        hyper?: number
    }): Promise<InstanceListResponse> {
        const response = await client.get<InstanceListResponse>('/instances', { params })
        return response.data
    },

    // Get single instance
    async getInstance(id: string): Promise<Instance> {
        const response = await client.get<Instance>(`/instances/${id}`)
        return response.data
    },

    // Create instance —— 后端返回的是实例数组（支持 count 批量创建）
    async createInstance(payload: CreateInstancePayload): Promise<Instance[]> {
        const response = await client.post<Instance[]>('/instances', payload)
        return response.data
    },

    // Delete instance
    async deleteInstance(id: string): Promise<void> {
        const response = await client.delete<void>(`/instances/${id}`)
        return response.data
    },

    // Instance Actions
    async startInstance(id: string, hostname: string = ''): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname, power_action: 'start' })
        return response.data
    },

    async stopInstance(id: string, hostname: string = ''): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname, power_action: 'stop' })
        return response.data
    },

    async rebootInstance(id: string, hostname: string = ''): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname, power_action: 'restart' })
        return response.data
    },

    async hardStopInstance(id: string, hostname: string = ''): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname, power_action: 'hard_stop' })
        return response.data
    },

    async hardRebootInstance(id: string, hostname: string = ''): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname, power_action: 'hard_restart' })
        return response.data
    },

    async pauseInstance(id: string, hostname: string = ''): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname, power_action: 'pause' })
        return response.data
    },

    async resumeInstance(id: string, hostname: string = ''): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname, power_action: 'resume' })
        return response.data
    },

    // 后端返回 200 + null
    async resizeInstance(id: string, cpu: number, memory: number): Promise<void> {
        const response = await client.post<void>(`/instances/${id}/resize`, { cpu, memory })
        return response.data
    },

    // type: vnc (graphical, default) or serial (text console)
    async getConsole(id: string, type?: 'vnc' | 'serial'): Promise<ConsoleResponse> {
        const response = await client.post<ConsoleResponse>(`/instances/${id}/console`, type ? { type } : undefined)
        return response.data
    },

    // 后端返回 200 + null
    async setUserPassword(id: string, user_name: string, password: string): Promise<void> {
        const response = await client.post<void>(`/instances/${id}/set_user_password`, { user_name, password })
        return response.data
    },

    // 后端返回 204 No Content
    async reinstallInstance(
        id: string,
        payload: {
            image?: BaseReference
            password?: string
            keys?: BaseReference[]
            flavor?: string
            login_port?: number
        }
    ): Promise<void> {
        const response = await client.post<void>(`/instances/${id}/reinstall`, payload)
        return response.data
    },

    async renameInstance(id: string, hostname: string): Promise<Instance> {
        const response = await client.patch<Instance>(`/instances/${id}`, { hostname })
        return response.data
    },

    async getInterfaces(instanceId: string): Promise<InterfaceListResponse> {
        const response = await client.get<InterfaceListResponse>(`/instances/${instanceId}/interfaces`)
        return response.data
    },

    async patchInterface(
        instanceId: string,
        ifaceId: string,
        payload: { security_groups?: BaseReference[] }
    ): Promise<InstanceInterface> {
        const response = await client.patch<InstanceInterface>(
            `/instances/${instanceId}/interfaces/${ifaceId}`,
            payload
        )
        return response.data
    },

    // Monitoring Metrics
    async getCPUMetrics(payload: {
        id: string[]
        start: string
        end: string
        step: string
    }): Promise<CPUMetricsResponse> {
        const response = await client.post<CPUMetricsResponse>('/metrics/instances/cpu/his_data', payload)
        return response.data
    },

    async getMemoryMetrics(payload: {
        id: string[]
        start: string
        end: string
        step: string
    }): Promise<MemoryMetricsResponse> {
        const response = await client.post<MemoryMetricsResponse>('/metrics/instances/memory/his_data', payload)
        return response.data
    },

    async getDiskMetrics(payload: {
        id: string[]
        disk: string[]
        start: string
        end: string
        step: string
    }): Promise<DiskMetricsResponse> {
        const response = await client.post<DiskMetricsResponse>('/metrics/instances/disk/his_data', payload)
        return response.data
    },

    // 后端按 interface_ids 顺序返回数组，每个元素是一个网卡的 NetworkResponse
    async getNetworkMetrics(payload: {
        interface_ids: string[]
        start: string
        end: string
        step: string
    }): Promise<NetworkMetricsResponse[]> {
        const response = await client.post<NetworkMetricsResponse[]>('/metrics/instances/network/his_data', payload)
        return response.data
    },
}
