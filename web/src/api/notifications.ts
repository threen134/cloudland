import client from './client'

export interface NotificationChannel {
    uuid: string
    name: string
    type: 'feishu' | 'webhook'
    config: Record<string, any>
    enabled: boolean
    created_at: string
    updated_at: string
}

export interface ChannelListResponse {
    total: number
    channels: NotificationChannel[]
}

export interface CreateChannelPayload {
    name: string
    type: 'feishu' | 'webhook'
    config: Record<string, any>
    enabled?: boolean
}

export interface UpdateChannelPayload {
    name?: string
    config?: Record<string, any>
    enabled?: boolean
}

export const notificationsApi = {
    list() {
        return client.get<ChannelListResponse>('/notification-channels')
    },

    get(uuid: string) {
        return client.get<NotificationChannel>(`/notification-channels/${uuid}`)
    },

    create(payload: CreateChannelPayload) {
        return client.post<NotificationChannel>('/notification-channels', payload)
    },

    update(uuid: string, payload: UpdateChannelPayload) {
        return client.put<NotificationChannel>(`/notification-channels/${uuid}`, payload)
    },

    delete(uuid: string) {
        return client.delete(`/notification-channels/${uuid}`)
    },
}
