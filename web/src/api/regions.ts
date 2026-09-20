import client from './client'

// 以下类型对应控制面网关 cpgateway/src/apis/schemas.go 与 region_mgmt.go 的实际响应

// schemas.go 的 regionPublicOut（GET /regions，公开视图，不含 internal_endpoint / secret）
export interface RegionPublic {
    uuid: string
    name: string
    display_name: string | null
    is_available: boolean
    maintenance_mode: boolean
    description: string | null
    last_check_at: string | null
    status_message: string | null
}

// schemas.go 的 regionAdminOut
export interface RegionAdmin extends RegionPublic {
    internal_endpoint: string
    fail_count: number
    created_at: string
    updated_at: string
}

// schemas.go 的 regionCreatedOut，internal_secret 只在创建时返回一次
export interface RegionCreated extends RegionAdmin {
    internal_secret: string
}

// region_mgmt.go CreateRegion 的绑定结构
export interface CreateRegionPayload {
    // 必须匹配 ^[a-z0-9][a-z0-9-]*[a-z0-9]$
    name: string
    display_name?: string
    // 必须以 http:// 或 https:// 开头
    internal_endpoint: string
    // 省略或传 "auto-generate" 表示由后端生成
    internal_secret?: string
    description?: string
}

// region_mgmt.go UpdateRegion 实际解析的字段（按 raw JSON 逐个取值，显式 null 表示清空）
export interface UpdateRegionPayload {
    display_name?: string | null
    internal_endpoint?: string | null
    description?: string | null
    is_available?: boolean
    maintenance_mode?: boolean
}

// region_mgmt.go RotateRegionSecret 返回的 gin.H
export interface RegionSecretRotated {
    region_uuid: string
    name: string
    new_secret: string
}

export const regionsApi = {
    async fetchRegions(): Promise<RegionPublic[]> {
        const response = await client.get<RegionPublic[]>('/regions')
        return response.data
    },

    async getRegion(uuid: string): Promise<RegionAdmin> {
        const response = await client.get<RegionAdmin>(`/regions/${uuid}`)
        return response.data
    },

    async createRegion(payload: CreateRegionPayload): Promise<RegionCreated> {
        const response = await client.post<RegionCreated>('/regions', payload)
        return response.data
    },

    async updateRegion(uuid: string, payload: UpdateRegionPayload): Promise<RegionAdmin> {
        const response = await client.patch<RegionAdmin>(`/regions/${uuid}`, payload)
        return response.data
    },

    // 后端返回 204 No Content
    async deleteRegion(uuid: string): Promise<void> {
        await client.delete(`/regions/${uuid}`)
    },

    async rotateSecret(uuid: string): Promise<RegionSecretRotated> {
        const response = await client.post<RegionSecretRotated>(`/regions/${uuid}/rotate-secret`)
        return response.data
    },
}
