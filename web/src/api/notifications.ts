import client from './client'

/**
 * 通知渠道配置。
 * 后端存为 map[string]interface{}（cpgateway/src/apis/schemas.go channelOut.Config），
 * 但实际读取的键是固定的：
 *  - feishu：webhook_url（必填，validateChannel 校验）、secret（HMAC 签名，返回时被掩码）
 *  - webhook：url（必填）、method（默认 POST）、headers
 * 见 cpgateway/src/apis/notification_channels.go validateChannel 与
 *    api/src/services/notifier.go sendFeishu / sendWebhook。
 */
export interface NotificationChannelConfig {
    webhook_url?: string
    /** 返回时非空会被替换成掩码值 */
    secret?: string
    url?: string
    method?: string
    headers?: Record<string, string>
}

/**
 * 通知渠道。依据：cpgateway/src/apis/schemas.go 的 channelOut（渠道由网关管理，clapi 只有只读镜像）。
 * type 在 Go 里是 string，但 validateChannel 只接受 feishu / webhook。
 */
export interface NotificationChannel {
    uuid: string
    name: string
    type: 'feishu' | 'webhook'
    config: NotificationChannelConfig
    enabled: boolean
    created_at: string
    updated_at: string
}

/** GET /notification-channels —— cpgateway ListChannels */
export interface ChannelListResponse {
    total: number
    channels: NotificationChannel[]
}

export interface CreateChannelPayload {
    name: string
    type: 'feishu' | 'webhook'
    config: NotificationChannelConfig
    enabled?: boolean
}

export interface UpdateChannelPayload {
    name?: string
    config?: NotificationChannelConfig
    enabled?: boolean
}

export const notificationsApi = {
    async list(): Promise<ChannelListResponse> {
        const response = await client.get<ChannelListResponse>('/notification-channels')
        return response.data
    },

    async get(uuid: string): Promise<NotificationChannel> {
        const response = await client.get<NotificationChannel>(`/notification-channels/${uuid}`)
        return response.data
    },

    async create(payload: CreateChannelPayload): Promise<NotificationChannel> {
        const response = await client.post<NotificationChannel>('/notification-channels', payload)
        return response.data
    },

    async update(uuid: string, payload: UpdateChannelPayload): Promise<NotificationChannel> {
        const response = await client.put<NotificationChannel>(`/notification-channels/${uuid}`, payload)
        return response.data
    },

    // DeleteChannel 返回 204 No Content，没有响应体
    async delete(uuid: string): Promise<void> {
        await client.delete(`/notification-channels/${uuid}`)
    },
}
