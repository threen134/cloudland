import apiClient from './client'

export interface SystemSetting {
    key: string
    value: any
    value_type: string  // string | number | boolean | json | secret
    category: string    // general | quota | notification
    description?: string
    is_secret: boolean
    updated_at: string
}

export interface SystemSettingsResponse {
    settings: SystemSetting[]
}

export interface TestNotificationRequest {
    channel: 'email' | 'feishu' | 'slack' | 'webhook'
}

export interface TestNotificationResponse {
    channel: string
    success: boolean
    message: string
}

export const systemSettingsApi = {
    list(): Promise<{ data: SystemSettingsResponse }> {
        return apiClient.get('/system/settings')
    },

    update(payload: Record<string, any>): Promise<{ data: SystemSettingsResponse }> {
        return apiClient.put('/system/settings', payload)
    },

    testNotification(channel: string): Promise<{ data: TestNotificationResponse }> {
        return apiClient.post('/system/settings/test-notification', { channel })
    },
}
