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
    rule_id: string
    name: string
    owner: string
    rules: Record<string, any>[]
    linkedvms: string[]
    region_id: string
    level: string
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
    listRules(type: VMRuleType, params?: { page?: number; page_size?: number }) {
        return client.get<VMAlarmRuleListResponse>(`/metrics/alarm/${type}/rules`, { params })
    },

    createCPURule(payload: {
        name: string
        owner: string
        rule_id: string
        region_id: string
        duration_minutes?: number
        rules: CPURuleDetail[]
        linkedvms?: string[]
    }) {
        return client.post('/metrics/alarm/cpu/rules', payload)
    },

    createMemoryRule(payload: {
        name: string
        owner: string
        rule_id: string
        region_id: string
        rules: MemoryRuleDetail[]
        linkedvms?: string[]
    }) {
        return client.post('/metrics/alarm/memory/rules', payload)
    },

    createBWRule(payload: {
        name: string
        owner: string
        rule_id: string
        region_id: string
        enable: boolean
        rules: BWRuleDetail[]
        linkedvms?: { instance_id: string; target_device: string }[]
    }) {
        return client.post('/metrics/alarm/bw/rules', payload)
    },

    deleteRule(type: VMRuleType, uuid: string) {
        return client.delete(`/metrics/alarm/${type}/rule/${uuid}`)
    },

    linkRule(groupUuid: string, vmLinks: { vm_uuid: string; interface?: string }[]) {
        return client.post('/metrics/alarm/link', {
            group_uuid: groupUuid,
            vm_links: vmLinks,
        })
    },

    unlinkRule(groupUuid: string, vmLinks: { vm_uuid: string; interface?: string }[]) {
        return client.post('/metrics/alarm/unlink', {
            group_uuid: groupUuid,
            vm_links: vmLinks,
        })
    },
}
