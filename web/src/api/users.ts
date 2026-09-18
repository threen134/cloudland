import client from './client'

// 对应控制面网关 cpgateway/src/apis/schemas.go 的 userOut（GET /users、GET /users/:uuid、PUT /users/:uuid）
export interface User {
    email: string
    username: string
    language: string
    uuid: string
    is_active: boolean
    is_superuser: boolean
    system_role: number
    // model.UserStatus.Name()，如 active / invited / dormant / disabled
    status: string
    first_name: string
    last_name: string
    remark: string
    created_at: string
}

// cpgateway/src/apis/user_mgmt.go 的 UpdateUser（PUT /users/:uuid）绑定结构。
// username 传入与当前不同的值一律 400（用户名创建后不可更改）；
// role 是调用方当前组织内的角色名：admin / writer / reader / member / user，owner 表示不改
export interface UpdateUserPayload {
    username?: string
    email?: string
    role?: string
}

// createUser 使用；控制面网关没有 POST /users 路由，此结构仅为保持调用点签名不变
export interface CreateUserPayload {
    username: string
    password?: string
    email?: string
    role?: string
}

// user_mgmt.go 中 enable/disable/demote/delete/profile/password 一类接口的响应：gin.H{"status": "ok"}
export interface UserActionResponse {
    status: string
    new_status?: string
}

export const usersApi = {
    // List users（响应是 userOut 数组，不是 { users: [...] } 包装）
    async fetchUsers(): Promise<User[]> {
        const response = await client.get<User[]>('/users')
        return response.data
    },

    // Get single user
    async getUser(uuid: string): Promise<User> {
        const response = await client.get<User>(`/users/${uuid}`)
        return response.data
    },

    // Create user
    // 注意：控制面网关 routes.go 中没有 POST /users 路由，该调用实际会 404；
    // 返回类型按用户对象标注，等待调用点侧统一处理
    async createUser(payload: CreateUserPayload): Promise<User> {
        const response = await client.post<User>('/users', payload)
        return response.data
    },

    // Update user（PUT /users/:uuid，返回更新后的用户）
    async updateUser(uuid: string, payload: UpdateUserPayload): Promise<User> {
        const response = await client.put<User>(`/users/${uuid}`, payload)
        return response.data
    },

    // Delete user（返回 200 与 {"status": "ok"}，不是 204）
    async deleteUser(uuid: string): Promise<UserActionResponse> {
        const response = await client.delete<UserActionResponse>(`/users/${uuid}`)
        return response.data
    },
}
