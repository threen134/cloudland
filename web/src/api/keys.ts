import client from './client'

export interface SSHKey {
    id: string
    name: string
    finger_print?: string
    public_key?: string
    type?: string
    created_at?: string
    [key: string]: any
}

export interface CreateKeyPayload {
    name: string
    public_key: string
}

export const keysApi = {
    // List keys
    async fetchKeys() {
        const response = await client.get('/keys')
        return response.data
    },

    // Get single key
    async getKey(id: string) {
        const response = await client.get(`/keys/${id}`)
        return response.data
    },

    // Create key
    async createKey(payload: CreateKeyPayload) {
        const response = await client.post('/keys', payload)
        return response.data
    },

    // Delete key
    async deleteKey(id: string) {
        const response = await client.delete(`/keys/${id}`)
        return response.data
    }
}
