import client from './client'

export interface AlarmEvent {
    ID: number
    UUID: string
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
    UUID: string
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
    list(params?: { status?: string; page?: number; page_size?: number }) {
        return client.get<AlarmEventListResponse>('/alarm/events', { params })
    },

    getDeliveryLogs(eventUuid: string) {
        return client.get<{ delivery_logs: AlarmDeliveryLog[] }>(
            `/alarm/events/${eventUuid}/delivery-logs`
        )
    },

    getSummary() {
        return client.get<AlarmSummaryResponse>('/alarm/summary')
    },

    bindRuleChannels(ruleGroupUuid: string, channelUuids: string[]) {
        return client.post('/alarm/rule-channels', {
            rule_group_uuid: ruleGroupUuid,
            channel_uuids: channelUuids,
        })
    },

    getRuleChannels(ruleGroupUuid: string) {
        return client.get(`/alarm/rule-channels/${ruleGroupUuid}`)
    },
}
