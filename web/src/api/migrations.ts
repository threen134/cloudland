import client from './client'

export interface Migration {
    id: string
    instance_id: string
    source_node: string
    dest_node: string
    dest_compute: string
    status: string
    migration_type: string
    created_at: string
    updated_at: string
}

export const migrationsApi = {
    fetchMigrations() {
        return client.get('/migrations')
    },

    getMigration(id: string) {
        return client.get(`/migrations/${id}`)
    },

    createMigration(payload: { instance_id: string; dest_node?: string, migration_type?: string }) {
        return client.post('/migrations', payload)
    }
}
