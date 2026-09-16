import client from './client'

// 组织操作动态：来自 clapi 审计日志的精简视图，范围为当前组织、当前区域
export interface Activity {
    id: string
    actor: string
    // 语义动作名，如 instance.create、instance.stop、hyper.maintain
    action: string
    resource_type: string
    resource_id: string
    // 操作当时的资源名快照
    resource_name: string
    success: boolean
    // RFC3339
    created_at: string
}

export interface ActivityListResponse {
    activities: Activity[]
    // 非空表示还有更早的记录
    next_cursor: string
}

export interface ActivityQuery {
    limit?: number
    cursor?: string
    // RFC3339；不传时默认最近 7 天，跨度上限 90 天
    start?: string
    end?: string
    resource_type?: string
    resource_uuid?: string
}

export const activitiesApi = {
    list: async (params?: ActivityQuery): Promise<ActivityListResponse> => {
        const response = await client.get('/activities', { params })
        return response.data
    }
}
