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
    /** File systems between the cluster and the pools (gpfs); create_filesystem: more of them are made and deleted */
    filesystems: boolean
    create_filesystem?: boolean
    pools: boolean
    add_nodes: boolean
    remove_node: boolean
    add_disks: boolean
    remove_disk: boolean
    rebalance: boolean
    /** A failed disk is swapped for a new disk of the same host */
    replace_disk: boolean
    change_roles: boolean
    /** The SSH key of a managed cluster is renewed; client_key: the kind has a client key renewed too (ceph) */
    rotate_keys?: boolean
    client_key?: boolean
    /** Rolling upgrade of a managed cluster; finalize: a separate, irreversible step afterwards (gpfs) */
    upgrade?: boolean
    finalize?: boolean
    /** The hosts of another managed cluster of the kind mount a file system of this one (gpfs multi-cluster) */
    remote_mount?: boolean
}

/** A file system of one cluster mounted by the hosts of another, under the same name and mount point */
export interface StorageRemoteMount {
    id: string
    name: string
    owner: StorageRef
    access: StorageRef
    filesystem: string
    mount_point: string
    status: 'mounting' | 'ready' | 'unmounting' | 'error' | string
    reason?: string
    created_at: string
}

export interface StorageClusterNode {
    hypervisor: StorageRef
    roles: StorageRole[]
    /** Kind specific attributes, e.g. the failure group of a GPFS host */
    attrs?: Record<string, unknown>
    status: string
    state: string
    reason?: string
    /** When the health watchdog last saw the host */
    checked_at?: string
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
    /** What the storage software reports for the disk (GPFS availability, Ceph up / down) */
    state?: string
    checked_at?: string
}

/** An alarm the health watchdog raised for the cluster and that still fires */
export interface StorageClusterAlarm {
    name: string
    severity: 'warning' | 'critical' | string
    summary: string
    since: string
}

/** A host of the auto join zones on its way into the cluster */
export interface StoragePendingClient {
    hypervisor: StorageRef
    /** pending: waits for the cluster to be free; joining: its task runs; failed: left alone, see reason */
    status: 'pending' | 'joining' | 'failed' | string
    reason?: string
    task?: string
    since: string
}

/** The zones whose hosts join as clients on their own, and the hosts on their way in (detail only) */
export interface StorageAutoJoin {
    zones: StorageRef[]
    /** Only the hosts registered after since join; those of the zones registered before are left as they are */
    new_only?: boolean
    since?: string
    pending: StoragePendingClient[]
}

/** The last health report of a cluster (detail only) */
export interface StorageClusterHealth {
    checked_at?: string
    summary?: string
    /** Why the last check could not be done */
    error?: string
    messages?: string[]
    flags?: string[]
    /** Capacity of the whole cluster as its software reports it (Ceph) */
    capacity_bytes?: number
    free_bytes?: number
    /** The host that checked */
    hypervisor?: StorageRef
    alarms: StorageClusterAlarm[]
}

/**
 * What the layout of a cluster is made of; the keys are the kind's own. GPFS erasure code: code (4+2p, 8+3p...),
 * no_slot_map (the slot check is off: virtual machines or emulated disks, only for tests), recovery_group, vdisk_set,
 * node_class
 */
export type StorageClusterLayoutInfo = Record<string, string | number | boolean>

export interface StorageCluster {
    id: string
    name: string
    created_at?: string
    updated_at?: string
    kind: StorageKind
    mode: 'managed' | 'external'
    layout?: string
    /** What this cluster supports: its kind's operations, fewer in some layouts (gpfs erasure code) */
    capabilities?: StorageCapabilities
    /** What the layout is made of (gpfs erasure code: the code, the recovery group, the vdisk set) */
    layout_info?: StorageClusterLayoutInfo
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
    health_info?: StorageClusterHealth
    auto_join?: StorageAutoJoin
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

/** The whole log of a run, fetched from its host on request */
export interface StorageRunLog {
    /** requested: the host was asked, ask again in a moment; ready: content holds it (with a message: the host is
     * offline and this is the copy fetched at updated_at); error: see message */
    status: 'requested' | 'ready' | 'error'
    message?: string
    /** Size of the log on the host; larger than the content when only its end (16 MiB) was kept */
    size: number
    truncated: boolean
    content: string
    updated_at?: string
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
    /** add disks: the file system they go into, the first one when empty */
    filesystem?: string
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

/** One curve of a chart, aligned with StorageMetricsResponse.timestamps; null is no sample */
export interface StorageMetricSeries {
    /** read, write, used, total, active, mounted, up, in, pool */
    key: string
    /** The file system or the pool when the chart has one curve per file system or pool */
    label?: string
    values: Array<number | null>
}

export interface StorageMetricChart {
    /** capacity, throughput, iops, pools, nodes, osds */
    key: string
    unit: 'bytes' | 'bytes_per_second' | 'ops_per_second' | 'percent' | 'count' | string
    series: StorageMetricSeries[]
}

export interface StorageMetricsResponse {
    start: number
    end: number
    step: string
    timestamps: number[]
    charts: StorageMetricChart[]
}

export const storageClustersApi = {
    metrics: async (
        id: string,
        params: { start: number; end: number; step: string }
    ): Promise<StorageMetricsResponse> => {
        const response = await client.get<StorageMetricsResponse>(`/storage_clusters/${id}/metrics`, { params })
        return response.data
    },
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
    /** Description and the zones whose hosts join as clients on their own; retry_auto_join forgets the failed ones */
    update: async (
        id: string,
        payload: {
            description?: string
            auto_join_zones?: string[]
            auto_join_new_only?: boolean
            retry_auto_join?: boolean
        }
    ): Promise<StorageCluster> => {
        const response = await client.patch<StorageCluster>(`/storage_clusters/${id}`, payload)
        return response.data
    },
    /** A failed disk swapped for a new disk (a scanned disk id) of the same host */
    replaceDisk: async (
        id: string,
        diskId: string,
        payload: { disk_id: string; media?: string; wipe?: boolean }
    ): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/disks/${diskId}/replace`, payload)
        return response.data
    },
    changeRoles: async (id: string, hypervisor: string, roles: StorageRole[]): Promise<StorageTask> => {
        const response = await client.patch<StorageTask>(`/storage_clusters/${id}/nodes/${hypervisor}`, { roles })
        return response.data
    },
    /** Renew the SSH key and / or the client key (ceph); neither named: both */
    rotateKeys: async (id: string, payload: { ssh?: boolean; client?: boolean }): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/rotate_keys`, payload)
        return response.data
    },
    /** gpfs: package (UUID of the new release) or finalize; ceph: image (empty: the release of the distribution) */
    upgrade: async (
        id: string,
        payload: { package?: string; finalize?: boolean; image?: string }
    ): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/upgrade`, payload)
        return response.data
    },
    /** A new file system on new disks of the members (gpfs) */
    createFilesystem: async (
        id: string,
        payload: { name: string; block_size?: string; data_replicas?: number; disks: ExpandPayload['disks'] }
    ): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/filesystems`, payload)
        return response.data
    },
    /** The remote mounts a cluster has a part in: its file systems others mount, the ones of others it mounts */
    remoteMounts: async (id: string): Promise<StorageRemoteMount[]> => {
        const response = await client.get<{ remote_mounts: StorageRemoteMount[] }>(
            `/storage_clusters/${id}/remote_mounts`
        )
        return response.data.remote_mounts || []
    },
    /** Another cluster mounts a file system of this one: a task of that cluster */
    createRemoteMount: async (id: string, payload: { filesystem: string; cluster: string }): Promise<StorageTask> => {
        const response = await client.post<StorageTask>(`/storage_clusters/${id}/remote_mounts`, payload)
        return response.data
    },
    deleteRemoteMount: async (id: string, mountId: string): Promise<StorageTask> => {
        const response = await client.delete<StorageTask>(`/storage_clusters/${id}/remote_mounts/${mountId}`)
        return response.data
    },
    /** Deletes a file system no pool is on, with its disks */
    deleteFilesystem: async (id: string, name: string): Promise<StorageTask> => {
        const response = await client.delete<StorageTask>(
            `/storage_clusters/${id}/filesystems/${encodeURIComponent(name)}`
        )
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
    runLog: async (taskId: string, runId: number): Promise<StorageRunLog> => {
        const response = await client.get<StorageRunLog>(`/storage_tasks/${taskId}/runs/${runId}/log`)
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
