import client from './client'

// Organization activity: a trimmed view of clapi audit logs, scoped to the current organization and region.
export interface Activity {
    id: string
    actor: string
    // Semantic action name, e.g. instance.create, instance.stop, hyper.maintain
    action: string
    resource_type: string
    resource_id: string
    // Snapshot of the resource name at the time of the operation
    resource_name: string
    success: boolean
    // RFC3339
    created_at: string
}

export interface ActivityListResponse {
    activities: Activity[]
    // Non-empty when older entries exist
    next_cursor: string
}

export interface ActivityQuery {
    limit?: number
    cursor?: string
    // RFC3339. Defaults to the last 7 days; the span must not exceed 90 days.
    start?: string
    end?: string
    resource_type?: string
    resource_uuid?: string
    // true: succeeded only, false: failed only, omitted: all
    success?: boolean
}

export const activitiesApi = {
    list: async (params?: ActivityQuery): Promise<ActivityListResponse> => {
        const response = await client.get('/activities', { params })
        return response.data
    }
}
