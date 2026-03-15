import client from './client'

export interface Zone {
    id: string
    name: string
    [key: string]: any
}

export const zonesApi = {
    fetchZones() {
        return client.get('/zones')
    },

    getZone(name: string) {
        return client.get(`/zones/${name}`)
    }
}
