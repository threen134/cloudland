import client from './client'

/**
 * 告警事件。依据：api/src/model/notification.go 的 AlarmEvent（内嵌 model.Model）。
 * model.Model 的 ID / UpdatedAt / DeletedAt / Creater 没有 json tag，键名是 Go 字段名原样。
 */
export interface AlarmEvent {
    ID: number
    uuid: string
    created_at: string
    UpdatedAt: string
    DeletedAt: string | null
    Creater: number
    /** AlertManager 的指纹，同一条告警 firing→resolved 共用一行 */
    fingerprint: string
    rule_group_uuid: string
    alert_name: string
    /** 组织 ID 的字符串形式 */
    owner: string
    vm_uuid: string
    vm_name: string
    severity: string
    status: 'firing' | 'resolved'
    summary: string
    /** 后端存的是 JSON 字符串（model 里是 text），不是对象 */
    labels: string
    fired_at: string
    last_fired_at: string
    resolved_at: string | null
}

/** GET /alarm/events —— api/src/apis/notification.go ListAlarmEvents */
export interface AlarmEventListResponse {
    total: number
    page: number
    limit: number
    events: AlarmEvent[]
}

/**
 * 通知发送流水。依据：api/src/model/notification.go 的 AlarmDeliveryLog。
 */
export interface AlarmDeliveryLog {
    ID: number
    uuid: string
    created_at: string
    UpdatedAt: string
    DeletedAt: string | null
    Creater: number
    event_uuid: string
    channel_uuid: string
    channel_name: string
    channel_type: string
    /** firing_trigger | repeat_remind | resolved */
    notify_type: string
    status: 'sent' | 'failed'
    error_message: string
    sent_at: string
}

/** GET /alarm/events/:event_uuid/delivery-logs —— GetAlarmDeliveryLogs */
export interface AlarmDeliveryLogListResponse {
    delivery_logs: AlarmDeliveryLog[]
}

/**
 * 告警规则与通知渠道的绑定关系。
 * 依据：api/src/model/notification.go 的 AlarmNotificationBinding。
 */
export interface AlarmRuleChannelBinding {
    ID: number
    uuid: string
    created_at: string
    UpdatedAt: string
    DeletedAt: string | null
    Creater: number
    rule_group_uuid: string
    channel_uuid: string
    org_id: number
}

/** GET /alarm/rule-channels/:uuid —— GetRuleChannels */
export interface RuleChannelListResponse {
    bindings: AlarmRuleChannelBinding[]
}

/** POST /alarm/rule-channels —— BindRuleChannels，成功时只返回 {"status":"ok"} */
export interface BindRuleChannelsResponse {
    status: string
}

/**
 * 跨区域告警汇总。
 * 依据：cpgateway/src/apis/alarm_summary.go 的 alarmSummaryRegion / GetAlarmSummary。
 */
export interface AlarmSummaryRegion {
    region_uuid: string
    region_name: string
    /** 区域不可达或超时为 -1 */
    firing_count: number
}

export interface AlarmSummaryResponse {
    regions: AlarmSummaryRegion[]
    total_firing: number
}

export const alarmEventsApi = {
    async list(params?: { status?: string; query?: string; page?: number; page_size?: number }): Promise<AlarmEventListResponse> {
        const response = await client.get<AlarmEventListResponse>('/alarm/events', { params })
        return response.data
    },

    async getDeliveryLogs(eventUuid: string): Promise<AlarmDeliveryLogListResponse> {
        const response = await client.get<AlarmDeliveryLogListResponse>(
            `/alarm/events/${eventUuid}/delivery-logs`
        )
        return response.data
    },

    async getSummary(): Promise<AlarmSummaryResponse> {
        const response = await client.get<AlarmSummaryResponse>('/alarm/summary')
        return response.data
    },

    async bindRuleChannels(ruleGroupUuid: string, channelUuids: string[]): Promise<BindRuleChannelsResponse> {
        const response = await client.post<BindRuleChannelsResponse>('/alarm/rule-channels', {
            rule_group_uuid: ruleGroupUuid,
            channel_uuids: channelUuids,
        })
        return response.data
    },

    async getRuleChannels(ruleGroupUuid: string): Promise<RuleChannelListResponse> {
        const response = await client.get<RuleChannelListResponse>(`/alarm/rule-channels/${ruleGroupUuid}`)
        return response.data
    },
}
