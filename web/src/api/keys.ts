import client from './client'

// 对应 api/src/apis/key.go 的 KeyResponse（内嵌 common.ResourceReference）
export interface SSHKey {
    id: string
    name: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
    finger_print: string
    public_key: string
}

export interface KeyListResponse {
    offset: number
    total: number
    limit: number
    keys: SSHKey[]
}

export interface CreateKeyPayload {
    name: string
    public_key: string
}

export const keysApi = {
    // List keys（分页与搜索都在服务端做）
    async fetchKeys(params?: {
        offset?: number
        limit?: number
        order?: string
        query?: string
    }): Promise<KeyListResponse> {
        const response = await client.get<KeyListResponse>('/keys', { params })
        return response.data
    },

    // Get single key
    async getKey(id: string): Promise<SSHKey> {
        const response = await client.get<SSHKey>(`/keys/${id}`)
        return response.data
    },

    // Create key
    async createKey(payload: CreateKeyPayload): Promise<SSHKey> {
        const response = await client.post<SSHKey>('/keys', payload)
        return response.data
    },

    // Delete key
    async deleteKey(id: string): Promise<void> {
        const response = await client.delete<void>(`/keys/${id}`)
        return response.data
    },
}
