import client from './client'

// 对应 clapi api/src/apis/migration.go 的 TaskResponse
export interface MigrationPhase {
    source: string
    name: string
    summary: string
    status: string
    message: string
}

// 对应 api/src/apis/floatingip.go 的 InstanceInfo（ResourceReference + hostname）；
// 迁移接口只填 id 与 hostname，其余 ResourceReference 字段 omitempty
export interface MigrationInstance {
    id: string
    hostname: string
    name?: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
}

// 对应 migration.go 的 MigrationResponse
export interface Migration {
    id: string
    name: string
    created_at: string
    updated_at: string
    // 接口返回的是 instance 对象与节点 hostid，不是 instance_id / source_node；
    // 迁移记录未关联实例时为 null
    instance: MigrationInstance | null
    source_hyper: number
    target_hyper: number
    // 节点名称；目标节点由调度器自选（target_hyper 为 -1）时为空字符串
    source_hyper_name: string
    target_hyper_name: string
    // 发起该迁移的用户名 / UUID，创建时快照；本次改动之前的记录为空字符串
    creater_name: string
    creater_uuid: string
    force: boolean
    type: string
    status: string
    phases: MigrationPhase[]
    // 迁移进度：百分比与已传输 / 总字节数，未开始传输时为 0
    progress: number
    transferred: number
    total: number
}

// 对应 migration.go 的 MigrationListResponse
export interface MigrationListResponse {
    offset: number
    total: number
    limit: number
    migrations: Migration[]
}

// 对应 migration.go 的 MigrationPayload
export interface CreateMigrationPayload {
    // 2-32 字符
    name: string
    instances: { id: string }[]
    // 节点 hostid；省略则由调度器选择
    target_hyper?: number
    // true 为冷迁移
    force?: boolean
}

// 迁移进行中的状态，前端据此自动刷新
export const MIGRATION_ACTIVE_STATUSES = ['in_progress', 'target_prepared', 'source_prepared']

export const migrationsApi = {
    // 分页与搜索都在服务端做
    async fetchMigrations(params?: { offset?: number; limit?: number; order?: string; query?: string }): Promise<MigrationListResponse> {
        const response = await client.get<MigrationListResponse>('/migrations', { params })
        return response.data
    },

    async getMigration(id: string): Promise<Migration> {
        const response = await client.get<Migration>(`/migrations/${id}`)
        return response.data
    },

    // 接口按 instances 数组逐个建迁移，返回的是 MigrationResponse 数组而非单个对象
    async createMigration(payload: CreateMigrationPayload): Promise<Migration[]> {
        const response = await client.post<Migration[]>('/migrations', payload)
        return response.data
    }
}
