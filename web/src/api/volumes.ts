import client from './client'

// Types based on actual API response
export interface Volume {
    id: string
    name: string
    status: string
    size: number  // in GB
    format?: string
    booting?: boolean
    instance?: {
        id: string
        name: string
    }
    owner?: string
    target?: string
    path?: string
    href?: string
    created_at?: string
    updated_at?: string
    iops_limit?: number
    iops_burst?: number
    bps_limit?: number
    bps_burst?: number
}

export interface VolumePayload {
    name: string
    size: number
    format?: string
    bootable?: boolean
    zone?: string
}

export interface VolumePatchPayload {
    name?: string
    action?: 'enable' | 'disable'
    size?: number  // for resize
    instance?: { id: string }  // for attach/detach
}

export interface VolumeListResponse {
    volumes: Volume[]
    total: number
    limit: number
    offset: number
}

export interface VolumeBackup {
    id: string
    name: string
    status: string
    volume?: {
        id: string
        name: string
    }
    size?: number
    created_at?: string
    owner?: string
}

export interface BackupListResponse {
    backups: VolumeBackup[]
    total: number
    limit: number
    offset: number
}

// Volume API functions
export const volumesApi = {
    // List volumes
    list: async (params?: {
        offset?: number
        limit?: number
        name?: string
        status?: string
    }): Promise<VolumeListResponse> => {
        const response = await client.get('/volumes', { params })
        return response.data
    },

    // Get single volume
    get: async (id: string): Promise<Volume> => {
        const response = await client.get(`/volumes/${id}`)
        return response.data
    },

    // Create volume
    create: async (payload: VolumePayload): Promise<Volume> => {
        const response = await client.post('/volumes', payload)
        return response.data
    },

    // Update volume
    patch: async (id: string, payload: VolumePatchPayload): Promise<Volume> => {
        const response = await client.patch(`/volumes/${id}`, payload)
        return response.data
    },

    // Delete volume
    delete: async (id: string): Promise<void> => {
        await client.delete(`/volumes/${id}`)
    },

    // Attach volume to instance
    attach: async (id: string, instanceId: string): Promise<Volume> => {
        const response = await client.patch(`/volumes/${id}`, {
            instance: { id: instanceId }
        })
        return response.data
    },

    // Detach volume from instance
    detach: async (id: string): Promise<Volume> => {
        const response = await client.patch(`/volumes/${id}`, {
            instance: null
        })
        return response.data
    },

    // Resize volume
    resize: async (id: string, newSize: number): Promise<Volume> => {
        const response = await client.patch(`/volumes/${id}`, {
            size: newSize
        })
        return response.data
    }
}

// Backup API functions
export const backupsApi = {
    // List backups
    list: async (params?: {
        offset?: number
        limit?: number
    }): Promise<BackupListResponse> => {
        const response = await client.get('/backups', { params })
        return response.data
    },

    // Get single backup
    get: async (id: string): Promise<VolumeBackup> => {
        const response = await client.get(`/backups/${id}`)
        return response.data
    },

    // Create backup from volume
    create: async (volumeId: string, name: string): Promise<VolumeBackup> => {
        const response = await client.post('/backups', {
            name,
            volume: { id: volumeId }
        })
        return response.data
    },

    // Delete backup
    delete: async (id: string): Promise<void> => {
        await client.delete(`/backups/${id}`)
    },

    // Restore backup to new volume
    restore: async (id: string, volumeName: string): Promise<Volume> => {
        const response = await client.post(`/backups/${id}/restore`, {
            name: volumeName
        })
        return response.data
    }
}

export default volumesApi
