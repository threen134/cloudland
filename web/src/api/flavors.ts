import client from './client'

// 对应 api/src/apis/flavor.go 的 FlavorResponse
// 注意：后端字段是 cpu / memory（MB），不是 vcpus / ram
export interface Flavor {
    uuid: string
    name: string
    cpu: number
    memory: number
    disk: number
}

export interface FlavorListResponse {
    offset: number
    total: number
    limit: number
    flavors: Flavor[]
}

export interface FlavorPayload {
    name: string
    cpu: number
    memory: number
    disk: number
}

export const flavorsApi = {
    // List flavors（分页与搜索都在服务端做）
    async fetchFlavors(params?: {
        offset?: number
        limit?: number
        order?: string
        query?: string
    }): Promise<FlavorListResponse> {
        const response = await client.get<FlavorListResponse>('/flavors', { params })
        return response.data
    },

    // Get single flavor
    async getFlavor(name: string): Promise<Flavor> {
        const response = await client.get<Flavor>(`/flavors/${name}`)
        return response.data
    },

    // Create flavor
    async createFlavor(payload: FlavorPayload): Promise<Flavor> {
        const response = await client.post<Flavor>('/flavors', payload)
        return response.data
    },

    // Delete flavor
    async deleteFlavor(name: string): Promise<void> {
        const response = await client.delete<void>(`/flavors/${name}`)
        return response.data
    },
}
