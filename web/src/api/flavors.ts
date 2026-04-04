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
    fetchFlavors() {
        return client.get('/flavors')
    },

    // Get single flavor
    getFlavor(name: string) {
        return client.get(`/flavors/${name}`)
    },

    // Create flavor
    createFlavor(payload: FlavorPayload) {
        return client.post('/flavors', payload)
    },

    // Delete flavor
    deleteFlavor(name: string) {
        return client.delete(`/flavors/${name}`)
    }
}
