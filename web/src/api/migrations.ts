import client from './client'

export interface MigrationPhase {
    source: string
    name: string
    summary: string
    status: string
    message: string
}

export interface MigrationInstance {
    id: string
    hostname: string
}

export interface Migration {
    id: string
    name: string
    // 接口返回的是 instance 对象与节点 hostid，不是 instance_id / source_node
    instance?: MigrationInstance
    source_hyper: number
    target_hyper: number
    // 节点名称；目标节点由调度器自选（target_hyper 为 -1）时为空
    source_hyper_name?: string
    target_hyper_name?: string
    // 发起该迁移的用户名，创建时快照；本次改动之前的记录为空
    creater_name?: string
    force: boolean
    type: string
    status: string
    created_at: string
    updated_at: string
    phases?: MigrationPhase[]
    // 迁移进度：百分比与已传输 / 总字节数，仅迁移进行中有值
    progress?: number
    transferred?: number
    total?: number
}

// 迁移进行中的状态，前端据此自动刷新
export const MIGRATION_ACTIVE_STATUSES = ['in_progress', 'target_prepared', 'source_prepared']

export const migrationsApi = {
    fetchMigrations() {
        return client.get('/migrations')
    },

    getMigration(id: string) {
        return client.get(`/migrations/${id}`)
    },

    // 接口要求：name（2-32 字符）、instances 数组；target_hyper 为节点 hostid，省略则由调度器选择；force=true 为冷迁移
    createMigration(payload: { name: string; instances: { id: string }[]; target_hyper?: number; force?: boolean }) {
        return client.post('/migrations', payload)
    }
}
