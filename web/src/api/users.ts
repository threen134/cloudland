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
    }
}
