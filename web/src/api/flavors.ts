import client from './client'

export interface Flavor {
    uuid: string
    name: string
    vcpus: number
    ram: number
    disk: number
    [key: string]: any
}

export interface FlavorPayload {
    name: string
    cpu: number
    memory: number
    disk: number
}

export const flavorsApi = {
    // List flavors
    async fetchFlavors() {
        const response = await client.get('/flavors')
        return response.data
    },

    // Get single flavor
    async getFlavor(name: string) {
        const response = await client.get(`/flavors/${name}`)
        return response.data
    },

    // Create flavor
    async createFlavor(payload: FlavorPayload) {
        const response = await client.post('/flavors', payload)
        return response.data
    },

    // Delete flavor
    async deleteFlavor(name: string) {
        const response = await client.delete(`/flavors/${name}`)
        return response.data
    }
}
