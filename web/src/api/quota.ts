import client from './client'

// 以下类型对应控制面网关 cpgateway/src/apis/schemas.go 与 resource_mgmt.go 的实际响应

// === 嵌套用（不含 org_uuid/region_name） ===

export interface QuotaFields {
    max_cpu_cores: number
    max_ram_gb: number
    max_public_ips: number
    max_disk_gb: number
    max_vpcs: number
    max_load_balancers: number
    max_vpn_gateways: number
    max_images: number
}

export interface ConsumptionFields {
    cpu_cores: number
    ram_gb: number
    public_ips: number
    disk_gb: number
    vpcs: number
    load_balancers: number
    vpn_gateways: number
    images: number
}

// Quota rows shown in the org quota views: consumption field, quota field and i18n label key.
// Keep in sync with the cpgateway quota fields.
export const QUOTA_ROWS: { key: keyof ConsumptionFields; qkey: keyof QuotaFields; label: string }[] = [
    { key: 'cpu_cores', qkey: 'max_cpu_cores', label: 'quota.cpuCores' },
    { key: 'ram_gb', qkey: 'max_ram_gb', label: 'quota.ramGb' },
    { key: 'disk_gb', qkey: 'max_disk_gb', label: 'quota.diskGb' },
    { key: 'public_ips', qkey: 'max_public_ips', label: 'quota.publicIps' },
    { key: 'vpcs', qkey: 'max_vpcs', label: 'quota.vpcs' },
    { key: 'load_balancers', qkey: 'max_load_balancers', label: 'quota.loadBalancers' },
    { key: 'vpn_gateways', qkey: 'max_vpn_gateways', label: 'quota.vpnGateways' },
    { key: 'images', qkey: 'max_images', label: 'quota.images' },
]

export interface OrgResourceQuotaUpdate {
    max_cpu_cores?: number
    max_ram_gb?: number
    max_public_ips?: number
    max_disk_gb?: number
    max_vpcs?: number
    max_load_balancers?: number
    max_vpn_gateways?: number
    max_images?: number
}

// === 独立返回用 ===

export interface OrgResourceQuota extends QuotaFields {
    org_uuid: string
    region_uuid: string
    region_name: string
    created_at: string
    updated_at: string
}

export interface OrgResourceConsumption extends ConsumptionFields {
    org_uuid: string
    region_uuid: string
    region_name: string
}

// === 组合类型 ===

export interface OrgResourceInfo {
    region_uuid: string
    region_name: string
    consumption: ConsumptionFields
    quota: QuotaFields
}

export interface OrgResourceSummary {
    org_uuid: string
    regions: OrgResourceInfo[]
}

export const quotaApi = {
    // 获取 org 在所有 region 的配额+消费汇总
    async getOrgResourceSummary(orgUuid: string): Promise<OrgResourceSummary> {
        const response = await client.get<OrgResourceSummary>(`/resources/info/${orgUuid}`)
        return response.data
    },

    // 获取 org 在特定 region 的配额+消费
    async getOrgRegionResourceInfo(orgUuid: string, regionUuid: string): Promise<OrgResourceInfo> {
        const response = await client.get<OrgResourceInfo>(`/resources/info/${orgUuid}/${regionUuid}`)
        return response.data
    },

    // 获取 org 在特定 region 的配额
    async getOrgQuota(orgUuid: string, regionUuid: string): Promise<OrgResourceQuota> {
        const response = await client.get<OrgResourceQuota>(`/resources/quota/${orgUuid}/${regionUuid}`)
        return response.data
    },

    // 更新 org 在特定 region 的配额 (superuser only)
    async updateOrgQuota(
        orgUuid: string,
        regionUuid: string,
        payload: OrgResourceQuotaUpdate
    ): Promise<OrgResourceQuota> {
        const response = await client.put<OrgResourceQuota>(`/resources/quota/${orgUuid}/${regionUuid}`, payload)
        return response.data
    },
}
