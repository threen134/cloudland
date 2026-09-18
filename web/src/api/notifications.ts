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
    async list() {
        const response = await client.get<ChannelListResponse>('/notification-channels')
        return response.data
    },

    async get(uuid: string) {
        const response = await client.get<NotificationChannel>(`/notification-channels/${uuid}`)
        return response.data
    },

    async create(payload: CreateChannelPayload) {
        const response = await client.post<NotificationChannel>('/notification-channels', payload)
        return response.data
    },

    async update(uuid: string, payload: UpdateChannelPayload) {
        const response = await client.put<NotificationChannel>(`/notification-channels/${uuid}`, payload)
        return response.data
    },

    async delete(uuid: string) {
        const response = await client.delete(`/notification-channels/${uuid}`)
        return response.data
    },
}
