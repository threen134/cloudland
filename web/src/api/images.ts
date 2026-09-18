import client from './client'

export interface Image {
    id: string
    name: string
    os_code: 'linux' | 'windows' | 'other'
    os_family?: string
    os_version?: string
    architecture?: string
    format?: string
    size?: number
    status?: string
    created_at?: string
    public?: boolean
    owner?: string
    [key: string]: any
}

export interface ImagePayload {
    name: string
    os_code: 'linux' | 'windows' | 'other'
    os_family: string
    os_version: string
    architecture: string
    boot_loader: 'bios' | 'uefi'
    download_url: string
    user: string
    instance_uuid?: string
    is_resque?: boolean
}

export const imagesApi = {
    // List images
    async fetchImages() {
        const response = await client.get('/images')
        return response.data
    },

    // Get single image
    async getImage(id: string) {
        const response = await client.get(`/images/${id}`)
        return response.data
    },

    // Create image
    async createImage(payload: ImagePayload) {
        const response = await client.post('/images', payload)
        return response.data
    },

    // Patch image
    async patchImage(id: string, payload: Record<string, any>) {
        const response = await client.patch(`/images/${id}`, payload)
        return response.data
    },

    // Delete image
    async deleteImage(id: string) {
        const response = await client.delete(`/images/${id}`)
        return response.data
    }
}
