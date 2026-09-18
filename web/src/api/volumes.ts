import client from './client'

/**
 * 云硬盘与备份/快照。
 * 依据：api/src/apis/volume.go 的 VolumeResponse / VolumeListResponse，
 *      api/src/apis/backup.go 的 VolBackupResponse / VolBackupListResponse，
 *      内嵌的 ResourceReference / BaseReference 见 api/src/common/http.go。
 */

/** api/src/common/http.go 的 BaseReference */
export interface BaseReference {
    id: string
    name: string
}

/** model.VolumeStatus（api/src/model/volume.go） */
export type VolumeStatus =
    | 'resizing'
    | 'available'
    | 'attached'
    | 'attaching'
    | 'detaching'
    | 'restoring'
    | 'backuping'
    | 'error'
    | 'pending'

/** model.BackupStatus（api/src/model/volume.go） */
export type BackupStatus = 'pending' | 'available' | 'error' | 'restoring'

export interface Volume {
    // --- ResourceReference（字段都是 omitempty，实际由 getVolumeResponse 填满） ---
    id: string
    name: string
    /** 组织名；查不到组织时可能缺省 */
    owner?: string
    owner_uuid?: string
    created_at: string
    updated_at: string
    // --- VolumeResponse 本体 ---
    path: string
    /** GB */
    size: number
    format: string
    status: VolumeStatus
    /** 挂载到虚拟机内的盘符，如 vdb */
    target: string
    href: string
    /** 是否系统盘 */
    booting: boolean
    /** 未挂载时为 null */
    instance: BaseReference | null
    iops_limit: number
    iops_burst: number
    /** MB/s */
    bps_limit: number
    bps_burst: number
}

/** POST /volumes 的请求体 —— apis/volume.go VolumePayload */
export interface VolumePayload {
    name: string
    /** GB */
    size: number
    count?: number
    pool_id?: string
    iops_limit?: number
    iops_burst?: number
    bps_limit?: number
    bps_burst?: number
}

/** PATCH /volumes/:id 的请求体 —— apis/volume.go VolumePatchPayload（instance 传 null 表示卸载） */
export interface VolumePatchPayload {
    name?: string
    size?: number
    instance?: { id: string } | null
}

export interface VolumeListResponse {
    offset: number
    total: number
    /** 本页实际条数（后端用 len(volumes) 填充，不是请求的 limit） */
    limit: number
    volumes: Volume[]
}

export interface VolumeBackup {
    // --- ResourceReference ---
    id: string
    name: string
    owner?: string
    owner_uuid?: string
    created_at: string
    updated_at: string
    // --- VolBackupResponse 本体 ---
    /** GB */
    size: number
    /** 源卷，缺失时为 null */
    volume: BaseReference | null
    status: BackupStatus
    path?: string
    task?: BaseReference
}

export interface BackupListResponse {
    offset: number
    total: number
    /** 本页实际条数 */
    limit: number
    backups: VolumeBackup[]
}

// Volume API functions
export const volumesApi = {
    // List volumes
    list: async (params?: {
        offset?: number
        limit?: number
        order?: string
        name?: string
        status?: string
        // 后端默认 data（只含数据盘）；要同时列出系统盘须传 all
        type?: 'data' | 'boot' | 'all'
    }): Promise<VolumeListResponse> => {
        const response = await client.get<VolumeListResponse>('/volumes', { params })
        return response.data
    },

    // Get single volume
    get: async (id: string): Promise<Volume> => {
        const response = await client.get<Volume>(`/volumes/${id}`)
        return response.data
    },

    // Create volume
    create: async (payload: VolumePayload): Promise<Volume> => {
        const response = await client.post<Volume>('/volumes', payload)
        return response.data
    },

    // Update volume
    patch: async (id: string, payload: VolumePatchPayload): Promise<Volume> => {
        const response = await client.patch<Volume>(`/volumes/${id}`, payload)
        return response.data
    },

    // Delete volume（后端返回 204 No Content）
    delete: async (id: string): Promise<void> => {
        await client.delete(`/volumes/${id}`)
    },

    // Attach volume to instance
    attach: async (id: string, instanceId: string): Promise<Volume> => {
        const response = await client.patch<Volume>(`/volumes/${id}`, {
            instance: { id: instanceId }
        })
        return response.data
    },

    // Detach volume from instance
    detach: async (id: string): Promise<Volume> => {
        const response = await client.patch<Volume>(`/volumes/${id}`, {
            instance: null
        })
        return response.data
    },

    // Resize volume
    resize: async (id: string, newSize: number): Promise<Volume> => {
        const response = await client.patch<Volume>(`/volumes/${id}`, {
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
        const response = await client.get<BackupListResponse>('/backups', { params })
        return response.data
    },

    // Get single backup
    get: async (id: string): Promise<VolumeBackup> => {
        const response = await client.get<VolumeBackup>(`/backups/${id}`)
        return response.data
    },

    // Create backup or snapshot from a volume。后端 VolBackupPayload 要的是
    // volume_id 与必填的 type（snapshot / backup），此前发的 volume: { id } 会被 400
    create: async (volumeId: string, name: string, type: 'snapshot' | 'backup' = 'backup', poolId?: string): Promise<VolumeBackup> => {
        const response = await client.post<VolumeBackup>('/backups', {
            name,
            volume_id: volumeId,
            type,
            ...(poolId ? { pool_id: poolId } : {}),
        })
        return response.data
    },

    // Delete backup
    delete: async (id: string): Promise<void> => {
        await client.delete(`/backups/${id}`)
    },

    // Restore backup。后端按 id 恢复、不读请求体，返回的是备份对象本身而非新卷
    restore: async (id: string): Promise<VolumeBackup> => {
        const response = await client.post<VolumeBackup>(`/backups/${id}/restore`)
        return response.data
    }
}

export default volumesApi
