import apiClient from './client'

export interface SystemSetting {
    key: string
    // 实际类型随 value_type 变（字符串 / 数字 / 布尔 / json 解析出来的数组或对象），
    // 由读取方按 value_type 在运行时收窄
    value: unknown
    value_type: string // string | number | boolean | json | secret
    category: string // general | quota | notification
    description?: string
    is_secret: boolean
    updated_at: string
}

export interface SystemSettingsResponse {
    settings: SystemSetting[]
}

export interface TestNotificationRequest {
    channel: 'email' | 'feishu'
}

export interface TestNotificationResponse {
    channel: string
    success: boolean
    message: string
}

export const systemSettingsApi = {
    async list(): Promise<SystemSettingsResponse> {
        const response = await apiClient.get('/system/settings')
        return response.data
    },

    async update(payload: Record<string, unknown>): Promise<SystemSettingsResponse> {
        const response = await apiClient.put('/system/settings', payload)
        return response.data
    },

    async testNotification(channel: string): Promise<TestNotificationResponse> {
        const response = await apiClient.post('/system/settings/test-notification', { channel })
        return response.data
    },
}
