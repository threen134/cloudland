import client from './client'

export interface User {
    user: {
        id: string
        name: string
        updated_at?: string
    }
    role: string
    org?: {
        id: string
        name: string
    }
    token?: string
    created_at?: string
    [key: string]: any
}

export interface CreateUserPayload {
    username: string
    password?: string
    email?: string
    role?: string
}

export interface ResourceQuota {
    id: number
    user_id: number
    max_cpu_cores: number
    max_ram_gb: number
    max_traffic_gb: number
    max_public_ips: number
    max_disk_gb: number
    created_at: string
    updated_at: string
}

export interface ResourceQuotaUpdate {
    max_cpu_cores?: number
    max_ram_gb?: number
    max_traffic_gb?: number
    max_public_ips?: number
    max_disk_gb?: number
}

export const usersApi = {
    // List users
    fetchUsers() {
        return client.get('/users')
    },

    // Get single user
    getUser(id: string) {
        return client.get(`/users/${id}`)
    },

    // Create user
    createUser(payload: CreateUserPayload) {
        return client.post('/users', payload)
    },

    // Update user
    updateUser(id: string, payload: Partial<CreateUserPayload>) {
        return client.put(`/users/${id}`, payload)
    },

    // Delete user
    deleteUser(id: string) {
        return client.delete(`/users/${id}`)
    },

    // Get user quota
    getUserQuota(userUuid: string) {
        return client.get<ResourceQuota>(`/resources/quota/${userUuid}`)
    },

    // Update user quota (admin only)
    updateUserQuota(userUuid: string, payload: ResourceQuotaUpdate) {
        return client.put<ResourceQuota>(`/resources/quota/${userUuid}`, payload)
    },

    // Get user resource info (quota + consumption)
    getUserResourceInfo(userUuid: string) {
        return client.get(`/resources/info/${userUuid}`)
    }
}
