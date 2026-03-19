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
    fetchRegions() {
        return client.get<RegionPublic[]>('/regions')
    },

    getRegion(uuid: string) {
        return client.get<RegionAdmin>(`/regions/${uuid}`)
    },

    createRegion(payload: CreateRegionPayload) {
        return client.post<RegionCreated>('/regions', payload)
    },

    updateRegion(uuid: string, payload: UpdateRegionPayload) {
        return client.patch<RegionAdmin>(`/regions/${uuid}`, payload)
    },

    deleteRegion(uuid: string) {
        return client.delete(`/regions/${uuid}`)
    },

    rotateSecret(uuid: string) {
        return client.post<RegionSecretRotated>(`/regions/${uuid}/rotate-secret`)
    }
}
