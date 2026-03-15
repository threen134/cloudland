import client from './client'

export interface Organization {
    id: string
    name: string
    description?: string
    created_at?: string
    owner_id?: string
    [key: string]: any
}

export interface CreateOrgPayload {
    name: string
    description?: string
}

export const orgsApi = {
    // List organizations
    fetchOrgs() {
        return client.get('/orgs')
    },

    // Get single organization
    getOrg(id: string) {
        return client.get(`/orgs/${id}`)
    },

    // Create organization
    createOrg(payload: CreateOrgPayload) {
        return client.post('/orgs', payload)
    },

    // Update organization
    updateOrg(id: string, payload: CreateOrgPayload) {
        return client.put(`/orgs/${id}`, payload)
    },

    // Delete organization
    deleteOrg(id: string) {
        return client.delete(`/orgs/${id}`)
    }
}
