import client from './client'

// 对应 api/src/apis/zone.go 的 ZoneResponse（内嵌 common.ResourceReference）
export interface Zone {
    id: string
    name: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
    default: boolean
    remark: string
}

export interface ZoneListResponse {
    offset: number
    total: number
    limit: number
    zones: Zone[]
}

export interface CreateZonePayload {
    name: string
    default?: boolean
    remark?: string
}

export interface UpdateZonePayload {
    default?: boolean
    remark?: string
}

export const zonesApi = {
    async fetchZones(): Promise<ZoneListResponse> {
        const response = await client.get<ZoneListResponse>('/zones')
        return response.data
    },

    async getZone(name: string): Promise<Zone> {
        const response = await client.get<Zone>(`/zones/${name}`)
        return response.data
    },

    async createZone(payload: CreateZonePayload): Promise<Zone> {
        const response = await client.post<Zone>('/zones', payload)
        return response.data
    },

    async updateZone(name: string, payload: UpdateZonePayload): Promise<Zone> {
        const response = await client.patch<Zone>(`/zones/${name}`, payload)
        return response.data
    },

    async deleteZone(name: string): Promise<void> {
        const response = await client.delete<void>(`/zones/${name}`)
        return response.data
    }
}
