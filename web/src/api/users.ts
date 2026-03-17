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
}
