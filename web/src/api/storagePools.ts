import client from './client'

/**
 * Storage pools and the storage of hosts. Mirrors api/src/apis/storage_pool.go.
 * Capacities are in bytes (fields ending in _bytes).
 */

export interface ResourceRef {
    id?: string
    name?: string
    created_at?: string
    updated_at?: string
}

export type PoolMedia = 'ssd' | 'hdd' | 'nvme' | ''
export type PoolLayout = 'single' | 'linear' | 'raid1'

/** StoragePoolResponse; members only get the fields up to available_hosts */
export interface StoragePool extends ResourceRef {
    id: string
    name: string
    driver: string
    shared: boolean
    builtin: boolean
    media: PoolMedia
    is_default: boolean
    /** Hosts where the pool is usable now */
    available_hosts: number
    // --- system admins ---
    fallback_group?: string
    status?: 'active' | 'disabled'
    over_ratio?: number
    mount_path?: string
    description?: string
    hosts?: number
    capacity_bytes?: number
    used_bytes?: number
    allocated_bytes?: number
}

export interface StoragePoolListResponse {
    offset: number
    total: number
    limit: number
    storage_pools: StoragePool[]
}

export interface StoragePoolPayload {
    name: string
    media?: PoolMedia
    fallback_group?: string
    over_ratio?: number
    is_default?: boolean
    description?: string
}

export interface StoragePoolPatch {
    name?: string
    media?: PoolMedia
    fallback_group?: string
    over_ratio?: number
    status?: 'active' | 'disabled'
    is_default?: boolean
    description?: string
}

/** Status of a pool on one host (model.HyperPool* in api/src/model/storage_pool.go) */
export type HostPoolStatus =
    'creating' | 'extending' | 'removing' | 'ready' | 'degraded' | 'unavailable' | 'maintenance' | 'lost' | 'error'

export interface PoolDevice {
    id: string
    name: string
    serial: string
    model: string
    size_bytes: number
    media: string
    /** RAID1 array the disk belongs to; empty for single / linear */
    array: string
    /** RAID1 pair number, -1 without RAID */
    pair: number
}

export interface UsageEntry {
    path: string
    /** Space the file really takes */
    bytes: number
    /** Virtual size of the file */
    size: number
    volume_uuid?: string
    volume_name?: string
    instance?: string
    instance_uuid?: string
    owner?: string
}

/** HostPoolResponse: a pool set up on a host */
export interface HostPool {
    storage_pool: { id: string; name: string }
    hypervisor: { id: string; name: string } | null
    builtin: boolean
    media: string
    status: HostPoolStatus
    reason: string
    last_op: string
    /** What the host itself reported last (a lost pool keeps reporting its health) */
    reported_status: string
    reported_reason: string
    maintenance: boolean
    layout: string
    devices: PoolDevice[]
    capacity_bytes: number
    used_bytes: number
    avail_bytes: number
    own_bytes: number
    allocated_bytes: number
    reserved_bytes: number
    usage_ratio: number
    sync_percent: number
    volume_count: number
    /** Instances paused because this pool ran out of space */
    storage_full_paused: number
    checked_at?: string
    capacity_at?: string
    usage_at?: string
    usage?: UsageEntry[]
}

export interface HostPoolListResponse {
    storage_pools: HostPool[]
    /** Instances waiting for a pool to come back before they start */
    pending_instances: { id: string; name: string }[]
}

export type DiskState = 'free' | 'dirty' | 'in_use' | 'system' | 'cloudland_pool' | 'unknown_member' | 'shared'

export interface HostDisk {
    /** hyper_disks record id, used in PATCH /hypers/:uuid/disks/:id */
    id: number
    /** Stable identifier (wwn-..., nvme-eui..., ata-...) */
    disk_id: string
    name: string
    serial: string
    model: string
    size_bytes: number
    transport: string
    /** Media in effect */
    media: string
    detected_media: string
    /** 'manual' when an admin set the media */
    media_source: string
    state: DiskState
    detail: string
    pool_uuid?: string
    pool_name?: string
    owner_hostid?: number
    /** Volumes of that pool waiting for adoption (cloudland_pool disks) */
    orphan_count?: number
    scanned_at: string
}

export interface CreateHostPoolPayload {
    storage_pool: { id: string }
    layout: PoolLayout
    disks: string[]
    wipe: boolean
    allow_media_mismatch: boolean
    destroy_pools?: string[]
    /** Host name, typed to confirm */
    confirm: string
}

export interface ExtendHostPoolPayload {
    disks: string[]
    wipe: boolean
    allow_media_mismatch: boolean
    confirm: string
}

export interface ReplaceDiskPayload {
    failed_disk: string
    new_disk: string
    wipe: boolean
    allow_media_mismatch: boolean
    confirm: string
}

export interface PairWarning {
    pair: number
    usable_bytes: number
    wasted_bytes: number
}

export const storagePoolsApi = {
    list: async (params?: { offset?: number; limit?: number; query?: string }): Promise<StoragePoolListResponse> => {
        const response = await client.get<StoragePoolListResponse>('/storage_pools', { params })
        return response.data
    },
    get: async (id: string): Promise<StoragePool> => {
        const response = await client.get<StoragePool>(`/storage_pools/${id}`)
        return response.data
    },
    create: async (payload: StoragePoolPayload): Promise<StoragePool> => {
        const response = await client.post<StoragePool>('/storage_pools', payload)
        return response.data
    },
    update: async (id: string, payload: StoragePoolPatch): Promise<StoragePool> => {
        const response = await client.patch<StoragePool>(`/storage_pools/${id}`, payload)
        return response.data
    },
    delete: async (id: string): Promise<void> => {
        await client.delete(`/storage_pools/${id}`)
    },
    hosts: async (id: string): Promise<HostPoolListResponse> => {
        const response = await client.get<HostPoolListResponse>(`/storage_pools/${id}/hypers`)
        return response.data
    },
    abandonOrphans: async (id: string, oldHostid: number, confirm: string): Promise<{ volumes: number }> => {
        const response = await client.post<{ volumes: number }>(`/storage_pools/${id}/orphans/abandon`, {
            old_hostid: oldHostid,
            confirm,
        })
        return response.data
    },
}

/** Storage of one host (system admins); `uuid` is the hypervisor UUID */
export const hostStorageApi = {
    scanDisks: async (uuid: string): Promise<void> => {
        await client.post(`/hypers/${uuid}/disks/scan`)
    },
    disks: async (uuid: string): Promise<HostDisk[]> => {
        const response = await client.get<{ disks: HostDisk[] }>(`/hypers/${uuid}/disks`)
        return response.data.disks
    },
    setDiskMedia: async (uuid: string, diskId: number, media: string): Promise<HostDisk> => {
        const response = await client.patch<HostDisk>(`/hypers/${uuid}/disks/${diskId}`, { media })
        return response.data
    },
    pools: async (uuid: string): Promise<HostPoolListResponse> => {
        const response = await client.get<HostPoolListResponse>(`/hypers/${uuid}/storage_pools`)
        return response.data
    },
    createPool: async (
        uuid: string,
        payload: CreateHostPoolPayload
    ): Promise<{ raid1_pairs: PairWarning[] | null }> => {
        const response = await client.post<{ raid1_pairs: PairWarning[] | null }>(
            `/hypers/${uuid}/storage_pools`,
            payload
        )
        return response.data
    },
    extendPool: async (uuid: string, poolId: string, payload: ExtendHostPoolPayload): Promise<void> => {
        await client.post(`/hypers/${uuid}/storage_pools/${poolId}/extend`, payload)
    },
    replaceDisk: async (uuid: string, poolId: string, payload: ReplaceDiskPayload): Promise<void> => {
        await client.post(`/hypers/${uuid}/storage_pools/${poolId}/replace_disk`, payload)
    },
    removePool: async (uuid: string, poolId: string, confirm: string, force = false): Promise<void> => {
        // The gateway drops DELETE bodies: the confirmation goes in the query
        await client.delete(`/hypers/${uuid}/storage_pools/${poolId}`, {
            params: force ? { confirm, force: true } : { confirm },
        })
    },
    setMaintenance: async (uuid: string, poolId: string, enable: boolean): Promise<void> => {
        await client.post(`/hypers/${uuid}/storage_pools/${poolId}/maintenance`, { enable })
    },
    declareLost: async (
        uuid: string,
        poolId: string,
        confirm: string,
        nodeOfflineAck: boolean
    ): Promise<{ volumes: number }> => {
        const response = await client.post<{ volumes: number }>(`/hypers/${uuid}/storage_pools/${poolId}/lost`, {
            confirm,
            node_offline_ack: nodeOfflineAck,
        })
        return response.data
    },
    restore: async (uuid: string, poolId: string): Promise<{ volumes: number }> => {
        const response = await client.post<{ volumes: number }>(`/hypers/${uuid}/storage_pools/${poolId}/restore`)
        return response.data
    },
    adopt: async (uuid: string, poolId: string, confirm: string): Promise<void> => {
        await client.post(`/hypers/${uuid}/storage_pools/adopt`, { storage_pool: { id: poolId }, confirm })
    },
    scanUsage: async (uuid: string, poolId: string): Promise<void> => {
        await client.post(`/hypers/${uuid}/storage_pools/${poolId}/usage`)
    },
    usage: async (uuid: string, poolId: string): Promise<{ usage: UsageEntry[]; usage_at?: string }> => {
        const response = await client.get<{ usage: UsageEntry[]; usage_at?: string }>(
            `/hypers/${uuid}/storage_pools/${poolId}/usage`
        )
        return response.data
    },
}
