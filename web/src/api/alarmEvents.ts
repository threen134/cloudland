import client from './client'

export interface AlarmEvent {
    ID: number
    uuid: string
    fingerprint: string
    rule_group_uuid: string
    alert_name: string
    owner: string
    vm_uuid: string
    vm_name: string
    severity: string
    status: 'firing' | 'resolved'
    summary: string
    labels: string
    fired_at: string
    last_fired_at: string
    resolved_at: string | null
}

export interface AlarmEventListResponse {
    total: number
    page: number
    limit: number
    events: AlarmEvent[]
}

export interface AlarmDeliveryLog {
    ID: number
    uuid: string
    event_uuid: string
    channel_uuid: string
    channel_name: string
    channel_type: string
    notify_type: string
    status: 'sent' | 'failed'
    error_message: string
    sent_at: string
}

export interface AlarmSummaryRegion {
    region_uuid: string
    region_name: string
    firing_count: number
}

export interface AlarmSummaryResponse {
    regions: AlarmSummaryRegion[]
    total_firing: number
}

export const alarmEventsApi = {
    async list(params?: { status?: string; page?: number; page_size?: number }) {
        const response = await client.get<AlarmEventListResponse>('/alarm/events', { params })
        return response.data
    },

    async getDeliveryLogs(eventUuid: string) {
        const response = await client.get<{ delivery_logs: AlarmDeliveryLog[] }>(
            `/alarm/events/${eventUuid}/delivery-logs`
        )
        return response.data
    },

    async getSummary() {
        const response = await client.get<AlarmSummaryResponse>('/alarm/summary')
        return response.data
    },

    async bindRuleChannels(ruleGroupUuid: string, channelUuids: string[]) {
        const response = await client.post('/alarm/rule-channels', {
            rule_group_uuid: ruleGroupUuid,
            channel_uuids: channelUuids,
        })
        return response.data
    },

    async getRuleChannels(ruleGroupUuid: string) {
        const response = await client.get(`/alarm/rule-channels/${ruleGroupUuid}`)
        return response.data
    },
}
