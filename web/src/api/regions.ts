import client from './client'

export interface RegionPublic {
    uuid: string
    name: string
    display_name: string | null
    is_available: boolean
    maintenance_mode: boolean
    description: string | null
}

export interface RegionAdmin extends RegionPublic {
    internal_endpoint: string
    created_at: string
    updated_at: string
}

export interface RegionCreated extends RegionAdmin {
    internal_secret: string
}

export interface CreateRegionPayload {
    name: string
    display_name?: string
    internal_endpoint: string
    internal_secret?: string
    description?: string
}

export interface UpdateRegionPayload {
    display_name?: string
    internal_endpoint?: string
    maintenance_mode?: boolean
    description?: string
}

export interface RegionSecretRotated {
    region_uuid: string
    name: string
    new_secret: string
}

export const regionsApi = {
    async fetchRegions() {
        const response = await client.get<RegionPublic[]>('/regions')
        return response.data
    },

    async getRegion(uuid: string) {
        const response = await client.get<RegionAdmin>(`/regions/${uuid}`)
        return response.data
    },

    async createRegion(payload: CreateRegionPayload) {
        const response = await client.post<RegionCreated>('/regions', payload)
        return response.data
    },

    async updateRegion(uuid: string, payload: UpdateRegionPayload) {
        const response = await client.patch<RegionAdmin>(`/regions/${uuid}`, payload)
        return response.data
    },

    async deleteRegion(uuid: string) {
        const response = await client.delete(`/regions/${uuid}`)
        return response.data
    },

    async rotateSecret(uuid: string) {
        const response = await client.post<RegionSecretRotated>(`/regions/${uuid}/rotate-secret`)
        return response.data
    }
}
