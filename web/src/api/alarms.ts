import client from './client'

export interface NodeAlarmRule {
    id: number
    uuid: string
    rule_type: string
    name: string
    config: Record<string, any>
    description: string
    enabled: boolean
    owner: string
    created_at?: string
    updated_at?: string
}

export interface NodeAlarmRuleListResponse {
    status: string
    data: NodeAlarmRule[]
    count: number
}

export interface CreateNodeAlarmRulePayload {
    rule_type: string
    name: string
    config: Record<string, any>
    description?: string
    enabled?: boolean
}

export const RULE_TYPES = [
    { value: 'node_available', label: 'Node Available' },
    { value: 'control_node', label: 'Control Node' },
    { value: 'compute_node', label: 'Compute Node' },
    { value: 'hypervisor_vcpu', label: 'Hypervisor vCPU' },
    { value: 'packet_drop', label: 'Packet Drop' },
    { value: 'ip_block', label: 'IP Block' },
    { value: 'ipgroup_available_ip', label: 'IP Group Available IP' },
] as const

export const alarmsApi = {
    async fetchAlarmRules(params?: { uuid?: string; rule_type?: string }) {
        const response = await client.get<NodeAlarmRuleListResponse>('/node-alarm-rules', { params })
        return response.data
    },

    async createAlarmRule(payload: CreateNodeAlarmRulePayload) {
        const response = await client.post('/node-alarm-rules', payload)
        return response.data
    },

    async deleteAlarmRule(uuid: string) {
        const response = await client.delete(`/node-alarm-rules/${uuid}`)
        return response.data
    },

    async syncMappings() {
        const response = await client.post('/metrics/alarm/sync-mappings')
        return response.data
    }
}
