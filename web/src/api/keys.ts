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
    fetchKeys() {
        return client.get('/keys')
    },

    // Get single key
    getKey(id: string) {
        return client.get(`/keys/${id}`)
    },

    // Create key
    createKey(payload: CreateKeyPayload) {
        return client.post('/keys', payload)
    },

    // Delete key
    deleteKey(id: string) {
        return client.delete(`/keys/${id}`)
    }
}
