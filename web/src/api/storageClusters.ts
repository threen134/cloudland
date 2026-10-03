import client from './client'

/**
 * Storage clusters (GPFS / Ceph) and their tasks. Mirrors api/src/apis/storage_cluster.go; every route is for system
 * admins (shared-storage-design.md §13.1).
 */

export interface StorageRef {
    id?: string
    name?: string
}

/** A kind of storage cluster: gpfs, ceph, or any other the backend registers (GET /storage_backends) */
export type StorageKind = string
/** A role of a member host; which roles a kind has comes from its backend */
export type StorageRole = string

/** What the forms need to know about a kind (shared-storage-design.md §4.5) */
export interface StorageBackend {
    kind: StorageKind
    /** Roles a host may have, in the order forms show them */
    roles: StorageRole[]
    /** Role of the hosts that give disks; absent when the kind takes no disks */
    disk_role?: StorageRole
    /** Suggested roles of one more host: default_roles always, plus each of default_once_roles while no host has it */
    default_roles: StorageRole[]
    default_once_roles: StorageRole[]
    systems: { id: string; version: string; kernel_prefix?: string; kernel_suffix?: string }[]
    kernel_module: boolean
    containers: boolean
    /** Installs from an uploaded package rather than from the distribution */
    package: boolean
    /** What the kind supports; the interface offers only these */
    capabilities: StorageCapabilities
    /** Driver of the CloudLand pools on clusters of the kind */
    pool_driver: string
}

export interface StorageCapabilities {
    /** CloudLand deploys clusters of the kind / imports clusters set up by others */
    managed: boolean
    external: boolean
    /** File systems between the cluster and the pools (gpfs) */
    filesystems: boolean
    pools: boolean
    add_nodes: boolean
    remove_node: boolean
    add_disks: boolean
    remove_disk: boolean
    rebalance: boolean
    clients: boolean
}

export interface StorageClusterNode {
    hypervisor: StorageRef
    roles: StorageRole[]
    /** Kind specific attributes, e.g. the failure group of a GPFS host */
    attrs?: Record<string, unknown>
    status: string
    state: string
    reason?: string
    reserved_mem_mb: number
}

export interface StorageClusterDisk {
    /** UUID of the disk in the cluster */
    id: string
    hypervisor: StorageRef
    disk_id: string
    serial: string
    role: string
    name: string
    media: string
    size_bytes: number
    /** Kind specific attributes, e.g. the usage and storage pool of a GPFS NSD */
    attrs?: Record<string, unknown>
    status: string
    reason?: string
}

export interface StorageCluster {
    id: string
    name: string
    created_at?: string
    updated_at?: string
    kind: StorageKind
    mode: 'managed' | 'external'
    layout?: string
    status: string
    health: string
    version?: string
    /** The storage software's own id: GPFS cluster name and id, Ceph fsid */
    cluster_ref?: string
    unsupported: boolean
    /** Tasks holding the slots of the cluster: structural changes, pool changes */
    active_task?: string
    active_pool_task?: string
    description?: string
    nodes?: StorageClusterNode[]
    disks?: StorageClusterDisk[]
    /** Detail only */
    filesystems?: StorageFilesystem[]
    pools?: StorageClusterPool[]
    node_count: number
    disk_count: number
    pool_count: number
    capacity_bytes: number
    free_bytes: number
    /** Sum of the volumes in the pools of the cluster */
    allocated_bytes: number
}

export interface StorageFilesystem {
    id: string
    name: string
    mount_point: string
    block_size: string
    data_replicas: number
    meta_replicas: number
    status: string
    capacity_bytes: number
    free_bytes: number
    capacity_at?: string
}

export interface StorageClusterPool {
    id: string
    name: string
    driver: string
    status: string
    media: string
    mount_path?: string
    quota_bytes: number
    capacity_bytes: number
    used_bytes: number
    /** Sum of the volumes of the pool */
    allocated_bytes: number
    driver_params?: Record<string, unknown>
}

export type StorageTaskStatus = 'running' | 'failed' | 'aborting' | 'succeeded' | 'aborted'
export type StorageRunStatus = 'dispatched' | 'running' | 'failed' | 'succeeded'

export interface StorageRun {
    id: number
    hypervisor: StorageRef
    hostid: number
    attempt: number
    status: StorageRunStatus
    /** How many times the command was sent; a command the host never got is sent again */
    dispatches: number
    progress: number
    message: string
    log_tail: string
    /** What the step script returned (precheck: items and facts) */
    result?: unknown
    started_at: string
    updated_at: string
}

export interface StorageStep {
    seq: number
    name: string
    scope: 'nodes' | 'admin'
    status: 'pending' | 'running' | 'failed' | 'succeeded'
    timeout_sec: number
    started_at?: string
    finished_at?: string
    runs: StorageRun[]
}

export interface StorageTask {
    id: string
    cluster?: StorageRef
    kind: string
    /** Kind of storage the task works on */
    backend?: string
    status: StorageTaskStatus
    current_step: number
    creator: string
    message: string
    created_at: string
    finished_at?: string
    steps?: StorageStep[]
}

/** One line of the precheck of a host (scripts/kvm/storage/stc_precheck.sh) */
export interface PrecheckItem {
    name: string
    status: 'ok' | 'warn' | 'fail'
    detail: string
}

export interface PrecheckResult {
    items: PrecheckItem[]
    facts?: { hostname?: string; kernel?: string; os?: string; addresses?: string[] }
}

/** Parameters of a cluster: test is common to every kind, the rest is checked by the backend of the kind */
export interface StorageParams {
    /** Single node layouts: one quorum node / one mon, one replica */
    test?: boolean
    [key: string]: unknown
}

export interface PrecheckPayload {
    kind: StorageKind
    nodes: { hypervisor: string; roles: StorageRole[] }[]
    disks: { hypervisor: string; disk_id: string; media?: string; wipe?: boolean }[]
    params?: StorageParams
    allow_unsupported?: boolean
}

export interface ClusterCreatePayload extends PrecheckPayload {
    name: string
    description?: string
    /** UUID of a verified package whose license is accepted (kinds that install from one) */
    package_id?: string
}

export interface ClusterImportPayload {
    kind: StorageKind
    name: string
    description?: string
    hypervisors: string[]
    params?: Record<string, unknown>
}

export interface ExpandPayload {
    nodes?: { hypervisor: string; roles: StorageRole[] }[]
    disks?: { hypervisor: string; disk_id: string; media?: string; wipe?: boolean }[]
    allow_unsupported?: boolean
}

export interface SelftestPayload {
    hypervisors: string[]
    steps: number
    sleep_sec?: number
    fail_hypervisors?: string[]
    fail_attempts?: number
    fail_step?: number
    lock?: string
    timeout_sec?: number
}

export interface ListParams {
    offset?: number
    limit?: number
}

export const storageClustersApi = {
    backends: async (): Promise<StorageBackend[]> => {
        const response = await client.get<{ storage_backends: StorageBackend[] }>('/storage_backends')
        return response.data.storage_backends || []
    },
    list: async (params: ListParams = {}) => {
        const response = await client.get<{ total: number; storage_clusters: StorageCluster[] }>('/storage_clusters', {
            params,
        })
        return response.data
    },
    get: async (id: string): Promise<StorageCluster> => {
        const response = await client.get<StorageCluster>(`/storage_clusters/${id}`)
        return response.data
    },
    precheck: async (payload: PrecheckPayload): Promise<StorageTask> => {
        const response = await client.post<StorageTask>('/storage_clusters/precheck', payload)
        return response.data
    },
    create: async (payload: ClusterCreatePayload): Promise<StorageCluster> => {
        const response = await client.post<StorageCluster>('/storage_clusters', payload)
        return response.data
    },
    import: async (payload: ClusterImportPayload): Promise<StorageCluster> => {
        const response = await client.post<StorageCluster>('/storage_clusters/import', payload)
        return response.data
    },
    /** The gateway drops the body of DELETE requests: the confirmation goes in the query */
    remove: async (id: string, confirmName: string, purgePackages: boolean): Promise<StorageTask> => {
        const response = await client.delete<StorageTask>(`/storage_clusters/${id}`, {
            params: { confirm_name: confirmName, purge_packages: purgePackages },
        })
        return response.data
    },
    addNodes: async (id: string, payload: ExpandPayload): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/nodes`, payload)
        return response.data
    },
    removeNode: async (
        id: string,
        hypervisor: string,
        opts: { offline?: boolean; confirm?: string; purge_packages?: boolean } = {}
    ): Promise<StorageTask> => {
        const response = await client.delete<StorageTask>(`/storage_clusters/${id}/nodes/${hypervisor}`, {
            params: opts,
        })
        return response.data
    },
    addDisks: async (id: string, payload: ExpandPayload): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/disks`, payload)
        return response.data
    },
    removeDisk: async (id: string, diskId: string): Promise<StorageTask> => {
        const response = await client.delete<StorageTask>(`/storage_clusters/${id}/disks/${diskId}`)
        return response.data
    },
    rebalance: async (id: string, filesystem = ''): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/rebalance`, { filesystem })
        return response.data
    },
    /** cluster_id: a cluster UUID, '0' for the tasks of no cluster (precheck, selftest), omitted for all */
    listTasks: async (params: ListParams & { cluster_id?: string; status?: StorageTaskStatus } = {}) => {
        const response = await client.get<{ total: number; tasks: StorageTask[] }>('/storage_tasks', { params })
        return response.data
    },
    getTask: async (id: string): Promise<StorageTask> => {
        const response = await client.get<StorageTask>(`/storage_tasks/${id}`)
        return response.data
    },
    selftest: async (payload: SelftestPayload): Promise<StorageTask> => {
        const response = await client.post<StorageTask>('/storage_tasks/selftest', payload)
        return response.data
    },
    retryTask: async (id: string): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_tasks/${id}/retry`)
        return response.data
    },
    abortTask: async (id: string): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_tasks/${id}/abort`)
        return response.data
    },
}

export const TASK_LIVE_STATUSES: StorageTaskStatus[] = ['running', 'aborting']
