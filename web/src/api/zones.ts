import client from './client'

export interface Zone {
    id: string
    name: string
    default: boolean
    remark: string
    createdAt?: string
    updatedAt?: string
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
    async fetchZones() {
        const response = await client.get('/zones')
        return response.data
    },

    async getZone(name: string) {
        const response = await client.get(`/zones/${name}`)
        return response.data
    },

    async createZone(payload: CreateZonePayload) {
        const response = await client.post('/zones', payload)
        return response.data
    },

    async updateZone(name: string, payload: UpdateZonePayload) {
        const response = await client.patch(`/zones/${name}`, payload)
        return response.data
    },

    async deleteZone(name: string) {
        const response = await client.delete(`/zones/${name}`)
        return response.data
    }
}
