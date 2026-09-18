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

export const usersApi = {
    // List users
    async fetchUsers() {
        const response = await client.get('/users')
        return response.data
    },

    // Get single user
    async getUser(uuid: string) {
        const response = await client.get(`/users/${uuid}`)
        return response.data
    },

    // Create user
    async createUser(payload: CreateUserPayload) {
        const response = await client.post('/users', payload)
        return response.data
    },

    // Update user
    async updateUser(uuid: string, payload: Partial<CreateUserPayload>) {
        const response = await client.put(`/users/${uuid}`, payload)
        return response.data
    },

    // Delete user
    async deleteUser(uuid: string) {
        const response = await client.delete(`/users/${uuid}`)
        return response.data
    },
}
