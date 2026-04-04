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
    fetchZones() {
        return client.get('/zones')
    },

    getZone(name: string) {
        return client.get(`/zones/${name}`)
    },

    createZone(payload: CreateZonePayload) {
        return client.post('/zones', payload)
    },

    updateZone(name: string, payload: UpdateZonePayload) {
        return client.patch(`/zones/${name}`, payload)
    },

    deleteZone(name: string) {
        return client.delete(`/zones/${name}`)
    }
}
