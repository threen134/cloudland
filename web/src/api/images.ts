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
    fetchImages() {
        return client.get('/images')
    },

    // Get single image
    getImage(id: string) {
        return client.get(`/images/${id}`)
    },

    // Create image
    createImage(payload: ImagePayload) {
        return client.post('/images', payload)
    },

    // Patch image
    patchImage(id: string, payload: Record<string, any>) {
        return client.patch(`/images/${id}`, payload)
    },

    // Delete image
    deleteImage(id: string) {
        return client.delete(`/images/${id}`)
    }
}
