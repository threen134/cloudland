import client from './client'

export interface CPURuleDetail {
    name: string
    rule: string
    limit: number
    duration: number
    level: string
}

export interface MemoryRuleDetail {
    name: string
    limit: number
    duration: number
    level: string
}

export interface BWRuleDetail {
    direction: 'in' | 'out'
    name: string
    limit: number
    duration: number
    level: string
}

export interface VMAlarmRuleGroup {
    uuid: string
    created_at: string
    rule_id: string
    name: string
    owner: number
    rules: Record<string, any>[]
    linkedvms: (string | { instance_id: string; target_device: string })[]
    region_id: string
    enable: boolean
    type?: VMRuleType
}

export interface VMAlarmRuleListResponse {
    data: VMAlarmRuleGroup[]
    meta: {
        total: number
        current_page: number
        per_page: number
        total_pages: number
    }
}

export type VMRuleType = 'cpu' | 'memory' | 'bw'

export const VM_RULE_TYPES = [
    { value: 'cpu' as const, label: 'CPU' },
    { value: 'memory' as const, label: 'Memory' },
    { value: 'bw' as const, label: 'Bandwidth' },
] as const

export const vmAlarmRulesApi = {
    async listRules(type: VMRuleType, params?: { page?: number; page_size?: number }) {
        const response = await client.get<VMAlarmRuleListResponse>(`/metrics/alarm/${type}/rules`, { params })
        return response.data
    },

    async createCPURule(payload: {
        name: string
        rule_id?: string
        region_id: string
        duration_minutes?: number
        rules: CPURuleDetail[]
        linkedvms?: string[]
    }) {
        const response = await client.post('/metrics/alarm/cpu/rules', payload)
        return response.data
    },

    async createMemoryRule(payload: {
        name: string
        rule_id?: string
        region_id: string
        rules: MemoryRuleDetail[]
        linkedvms?: string[]
    }) {
        const response = await client.post('/metrics/alarm/memory/rules', payload)
        return response.data
    },

    async createBWRule(payload: {
        name: string
        rule_id?: string
        region_id: string
        enable: boolean
        rules: BWRuleDetail[]
        linkedvms?: { instance_id: string; target_device: string }[]
    }) {
        const response = await client.post('/metrics/alarm/bw/rules', payload)
        return response.data
    },

    async deleteRule(type: VMRuleType, uuid: string) {
        const response = await client.delete(`/metrics/alarm/${type}/rule/${uuid}`)
        return response.data
    },

    async linkRule(groupUuid: string, vmLinks: { vm_uuid: string; interface?: string }[]) {
        const response = await client.post('/metrics/alarm/link', {
            group_uuid: groupUuid,
            vm_links: vmLinks,
        })
        return response.data
    },

    async unlinkRule(groupUuid: string, vmLinks: { vm_uuid: string; interface?: string }[]) {
        const response = await client.post('/metrics/alarm/unlink', {
            group_uuid: groupUuid,
            vm_links: vmLinks,
        })
        return response.data
    },

    async enableRule(uuid: string) {
        const response = await client.post(`/metrics/alarm/${uuid}/enable`)
        return response.data
    },

    async disableRule(uuid: string) {
        const response = await client.post(`/metrics/alarm/${uuid}/disable`)
        return response.data
    },
}
