import client from './client'

/**
 * 虚拟机告警规则（clapi /metrics/alarm/*，handler 在 api/src/apis/alarms.go）。
 *
 * 列表接口返回的是 handler 手工拼的 gin.H，不是模型直接序列化，
 * 所以下面的字段严格按 GetCPURules / GetMemoryRules / GetBWRules 的 responseData 写。
 */

/** GetCPURules 的 rules[] */
export interface CPURuleDetail {
    name: string
    /** 比较符：gt / lt */
    rule: string
    limit: number
    /** 分钟 */
    duration: number
    /** critical | warning | info */
    level: string
}

/** GetMemoryRules 的 rules[]（与 CPU 同形，列表也返回 rule 比较符） */
export interface MemoryRuleDetail {
    name: string
    rule: string
    limit: number
    duration: number
    level: string
}

/** GetBWRules 的 rules[] */
export interface BWRuleDetail {
    direction: 'in' | 'out'
    name: string
    limit: number
    duration: number
    level: string
}

export type VMAlarmRuleDetail = CPURuleDetail | MemoryRuleDetail | BWRuleDetail

/** 带宽规则的关联虚拟机（BW 列表里 linkedvms 是对象，CPU/内存是 uuid 字符串） */
export interface BWLinkedVM {
    instance_id: string
    target_device: string
}

export interface VMAlarmRuleGroup {
    uuid: string
    rule_id: string
    name: string
    /** 组织 ID（RuleGroupV2.Owner 是 int64） */
    owner: number
    rules: VMAlarmRuleDetail[]
    linkedvms: (string | BWLinkedVM)[]
    region_id: string
    enable: boolean
    /** 后端列表接口不返回，由前端按请求的规则类型本地附加 */
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

/** CreateCPURule / CreateMemoryRule 的响应（两者字段一致） */
export interface CreateVMAlarmRuleResponse {
    status: string
    data: {
        group_uuid: string
        rule_id: string
        enabled: boolean
        linkedvms: string[] | null
        region_id: string
    }
}

/** CreateBWRule 的响应（字段与 CPU/内存不同：返回 uuid 而不是 group_uuid，且没有 rule_id/region_id） */
export interface CreateBWRuleResponse {
    status: string
    data: {
        uuid: string
        enabled: boolean
        linkedvms: BWLinkedVM[] | null
    }
}

/** DeleteCPURule / DeleteMemoryRule / DeleteBWRules 的响应 */
export interface DeleteVMAlarmRuleResponse {
    status: string
    data: {
        group_uuid: string
        rule_id: string
        deleted_files: string[]
        /** 三种类型的删除接口都只回传虚拟机 uuid 字符串 */
        linked_vms: string[]
    }
}

export interface LinkedVMRef {
    vm_uuid: string
    interface: string
}

/** POST /metrics/alarm/link —— LinkRuleToVMWithType */
export interface LinkRuleResponse {
    status: string
    data: {
        rule_category: string
        group_uuid: string
        rule_id: string
        added_count: number
        total_linked_vms: LinkedVMRef[] | null
        warnings?: {
            already_linked: string[]
            message: string
        }
    }
}

/** POST /metrics/alarm/unlink —— UnlinkRuleFromVMWithType */
export interface UnlinkRuleResponse {
    status: string
    data: {
        rule_category: string
        group_uuid: string
        rule_id: string
        unlinked_vms: LinkedVMRef[] | null
        unlinked_count: number
        remaining_vms: LinkedVMRef[] | null
        total_deleted: number
        is_batch_delete: boolean
        warnings?: {
            not_linked_vms: { vm_uuid: string; interface?: string }[]
            message: string
        }
    }
}

/** POST /metrics/alarm/:id/{enable,disable} —— ToggleRuleStatus */
export interface ToggleRuleResponse {
    status: string
    data: {
        group_uuid: string
        /** 规则组类型：cpu / memory / bw 等 */
        rule_type: string
        /** alarm 或 adjust */
        rule_source: string
        action: 'enable' | 'disable'
        enabled: boolean
        /** 组织 ID 的字符串形式 */
        owner: string
        target_files: string[] | null
    }
}

export type VMRuleType = 'cpu' | 'memory' | 'bw'

export const VM_RULE_TYPES = [
    { value: 'cpu' as const, label: 'CPU' },
    { value: 'memory' as const, label: 'Memory' },
    { value: 'bw' as const, label: 'Bandwidth' },
] as const

export const vmAlarmRulesApi = {
    async listRules(
        type: VMRuleType,
        params?: { page?: number; page_size?: number }
    ): Promise<VMAlarmRuleListResponse> {
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
    }): Promise<CreateVMAlarmRuleResponse> {
        const response = await client.post<CreateVMAlarmRuleResponse>('/metrics/alarm/cpu/rules', payload)
        return response.data
    },

    async createMemoryRule(payload: {
        name: string
        rule_id?: string
        region_id: string
        rules: MemoryRuleDetail[]
        linkedvms?: string[]
    }): Promise<CreateVMAlarmRuleResponse> {
        const response = await client.post<CreateVMAlarmRuleResponse>('/metrics/alarm/memory/rules', payload)
        return response.data
    },

    async createBWRule(payload: {
        name: string
        rule_id?: string
        region_id: string
        enable: boolean
        rules: BWRuleDetail[]
        linkedvms?: BWLinkedVM[]
    }): Promise<CreateBWRuleResponse> {
        const response = await client.post<CreateBWRuleResponse>('/metrics/alarm/bw/rules', payload)
        return response.data
    },

    async deleteRule(type: VMRuleType, uuid: string): Promise<DeleteVMAlarmRuleResponse> {
        const response = await client.delete<DeleteVMAlarmRuleResponse>(`/metrics/alarm/${type}/rule/${uuid}`)
        return response.data
    },

    async linkRule(groupUuid: string, vmLinks: { vm_uuid: string; interface?: string }[]): Promise<LinkRuleResponse> {
        const response = await client.post<LinkRuleResponse>('/metrics/alarm/link', {
            group_uuid: groupUuid,
            vm_links: vmLinks,
        })
        return response.data
    },

    async unlinkRule(
        groupUuid: string,
        vmLinks: { vm_uuid: string; interface?: string }[]
    ): Promise<UnlinkRuleResponse> {
        const response = await client.post<UnlinkRuleResponse>('/metrics/alarm/unlink', {
            group_uuid: groupUuid,
            vm_links: vmLinks,
        })
        return response.data
    },

    async enableRule(uuid: string): Promise<ToggleRuleResponse> {
        const response = await client.post<ToggleRuleResponse>(`/metrics/alarm/${uuid}/enable`)
        return response.data
    },

    async disableRule(uuid: string): Promise<ToggleRuleResponse> {
        const response = await client.post<ToggleRuleResponse>(`/metrics/alarm/${uuid}/disable`)
        return response.data
    },
}
