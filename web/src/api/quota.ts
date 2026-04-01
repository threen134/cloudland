import client from './client'

// === 嵌套用（不含 org_uuid/region_name） ===

export interface QuotaFields {
    max_cpu_cores: number
    max_ram_gb: number
    max_public_ips: number
    max_disk_gb: number
}

export interface ConsumptionFields {
    cpu_cores: number
    ram_gb: number
    public_ips: number
    disk_gb: number
}

export interface OrgResourceQuotaUpdate {
    max_cpu_cores?: number
    max_ram_gb?: number
    max_public_ips?: number
    max_disk_gb?: number
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
    getOrgResourceSummary(orgUuid: string) {
        return client.get<OrgResourceSummary>(`/resources/info/${orgUuid}`)
    },

    // 获取 org 在特定 region 的配额+消费
    getOrgRegionResourceInfo(orgUuid: string, regionUuid: string) {
        return client.get<OrgResourceInfo>(`/resources/info/${orgUuid}/${regionUuid}`)
    },

    // 获取 org 在特定 region 的配额
    getOrgQuota(orgUuid: string, regionUuid: string) {
        return client.get<OrgResourceQuota>(`/resources/quota/${orgUuid}/${regionUuid}`)
    },

    // 更新 org 在特定 region 的配额 (superuser only)
    updateOrgQuota(orgUuid: string, regionUuid: string, payload: OrgResourceQuotaUpdate) {
        return client.put<OrgResourceQuota>(`/resources/quota/${orgUuid}/${regionUuid}`, payload)
    },
}
