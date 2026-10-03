import client from './client'

/**
 * 节点告警规则。
 * 依据：api/src/model/alarmrules.go 的 NodeAlarmRule（内嵌 model.Model）。
 * 注意 model.Model 的 ID / UpdatedAt / DeletedAt / Creater 没有 json tag，
 * 序列化出来就是 Go 字段名原样（大驼峰），只有 created_at / uuid 有 tag。
 */
export interface NodeAlarmRule {
    ID: number
    uuid: string
    created_at: string
    UpdatedAt: string
    /** gorm.DeletedAt：未删除时为 null */
    DeletedAt: string | null
    /** 创建者用户 ID，仅审计用 */
    Creater: number
    rule_type: string
    name: string
    /**
     * 后端存为 ConfigWrapper（json.RawMessage），只校验是合法 JSON，不解析结构，
     * 具体字段按 rule_type 自由定义 —— 属实际的自由格式 JSON。
     */
    config: Record<string, unknown>
    description: string
    enabled: boolean
    /** 组织 ID 的字符串形式（model 里是 varchar(64)） */
    owner: string
}

/** GET /node-alarm-rules —— apis/alarms.go GetNodeAlarmRules */
export interface NodeAlarmRuleListResponse {
    status: string
    data: NodeAlarmRule[]
    count: number
}

/** POST /node-alarm-rules —— apis/alarms.go CreateNodeAlarmRule */
export interface CreateNodeAlarmRuleResponse {
    status: string
    data: NodeAlarmRule
}

/** DELETE /node-alarm-rules/:uuid —— apis/alarms.go DeleteNodeAlarmRule */
export interface DeleteNodeAlarmRuleResponse {
    status: string
    message: string
    uuid: string
    deleted_files: string[]
}

export interface CreateNodeAlarmRulePayload {
    rule_type: string
    name: string
    config: Record<string, unknown>
    description?: string
    enabled?: boolean
}

/** PATCH /node-alarm-rules/:uuid —— 只传要改的字段，规则类型不可改（每种类型只能有一条） */
export interface UpdateNodeAlarmRulePayload {
    name?: string
    description?: string
    config?: Record<string, unknown>
    enabled?: boolean
}

// 只有值，文案在 i18n 的 dashboard.alarmRuleTypes.<value>（同 VM_RULE_TYPES 的做法）。
// 原先这里带 label 且是写死的英文，中文界面上「规则类型」一列显示的是 Node Available 这种
export const RULE_TYPES = [
    'node_available',
    'control_node',
    'compute_node',
    'hypervisor_vcpu',
    'packet_drop',
    'ip_block',
    'ipgroup_available_ip',
    'local_pool',
] as const

export const alarmsApi = {
    async fetchAlarmRules(params?: { uuid?: string; rule_type?: string }): Promise<NodeAlarmRuleListResponse> {
        const response = await client.get<NodeAlarmRuleListResponse>('/node-alarm-rules', { params })
        return response.data
    },

    async createAlarmRule(payload: CreateNodeAlarmRulePayload): Promise<CreateNodeAlarmRuleResponse> {
        const response = await client.post<CreateNodeAlarmRuleResponse>('/node-alarm-rules', payload)
        return response.data
    },

    async updateAlarmRule(uuid: string, payload: UpdateNodeAlarmRulePayload): Promise<CreateNodeAlarmRuleResponse> {
        const response = await client.patch<CreateNodeAlarmRuleResponse>(`/node-alarm-rules/${uuid}`, payload)
        return response.data
    },

    async deleteAlarmRule(uuid: string): Promise<DeleteNodeAlarmRuleResponse> {
        const response = await client.delete<DeleteNodeAlarmRuleResponse>(`/node-alarm-rules/${uuid}`)
        return response.data
    },
}
