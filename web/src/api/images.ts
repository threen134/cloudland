import client from './client'

// 对应 api/src/apis/image.go 的 ImageResponse（内嵌 common.ResourceReference）
export interface Image {
    id: string
    name: string
    owner?: string
    owner_uuid?: string
    created_at?: string
    updated_at?: string
    os_code: 'linux' | 'windows' | 'other'
    os_version: string
    os_family: string
    architecture: string
    format: string
    size: number
    user: string
    status: string
    boot_loader: 'bios' | 'uefi'
    public: boolean
}

export interface ImageListResponse {
    offset: number
    total: number
    limit: number
    images: Image[]
}

// 对应 api/src/apis/image.go 的 ImagePayload
export interface ImagePayload {
    name: string
    os_code: 'linux' | 'windows' | 'other'
    os_family: string
    os_version: string
    // 注意：后端 ImagePayload 里没有 architecture 字段，会被忽略；前端表单仍在传
    architecture: string
    boot_loader: 'bios' | 'uefi'
    download_url: string
    user: string
    instance_uuid?: string
    is_rescue?: boolean
    rescue_image?: { id?: string; name?: string }
}

// 对应 api/src/apis/image.go 的 ImagePatchPayload
export interface ImagePatchPayload {
    name?: string
    os_code?: 'linux' | 'windows' | 'other'
    os_version?: string
    os_family?: string
    user?: string
    pools?: string[]
    public?: boolean
}

export const imagesApi = {
    // List images
    async fetchImages(params?: {
        offset?: number
        limit?: number
        order?: string
        query?: string
        visibility?: string
    }): Promise<ImageListResponse> {
        const response = await client.get<ImageListResponse>('/images', { params })
        return response.data
    },

    // Get single image
    async getImage(id: string): Promise<Image> {
        const response = await client.get<Image>(`/images/${id}`)
        return response.data
    },

    // Create image
    async createImage(payload: ImagePayload): Promise<Image> {
        const response = await client.post<Image>('/images', payload)
        return response.data
    },

    // Patch image
    async patchImage(id: string, payload: ImagePatchPayload): Promise<Image> {
        const response = await client.patch<Image>(`/images/${id}`, payload)
        return response.data
    },

    // Delete image
    async deleteImage(id: string): Promise<void> {
        const response = await client.delete<void>(`/images/${id}`)
        return response.data
    },
}
