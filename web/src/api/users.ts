import client from './client'

export interface User {
    user: {
        uuid: string
        name: string
        updated_at?: string
    }
    role: string
    org?: {
        uuid: string
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
    user_uuid: string
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
    getUser(uuid: string) {
        return client.get(`/users/${uuid}`)
    },

    // Create user
    createUser(payload: CreateUserPayload) {
        return client.post('/users', payload)
    },

    // Update user
    updateUser(uuid: string, payload: Partial<CreateUserPayload>) {
        return client.put(`/users/${uuid}`, payload)
    },

    // Delete user
    deleteUser(uuid: string) {
        return client.delete(`/users/${uuid}`)
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
