import client from './client'

export interface Organization {
    id: string
    name: string
    slug?: string
    description?: string
    created_at?: string
    owner_id?: string
    owner_email?: string
    member_count?: number
    [key: string]: any
}

export interface CreateOrgPayload {
    name: string
    slug?: string
    description?: string
}

export interface OrgMember {
    user_id: number
    username: string
    email: string
    org_role: number
    is_owner: boolean
    joined_at?: string
    [key: string]: any
}

export interface AddMemberPayload {
    user_id: number
    org_role?: number
}

export interface UpdateMemberRolePayload {
    org_role: number
}

export const ORG_ROLES: Record<number, string> = {
    0: 'None',
    1: 'Reader',
    2: 'Writer',
    3: 'Admin',
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
    updateOrg(id: string, payload: Partial<CreateOrgPayload>) {
        return client.patch(`/orgs/${id}`, payload)
    },

    // Delete organization
    deleteOrg(id: string) {
        return client.delete(`/orgs/${id}`)
    },

    // --- Member Management ---

    // List members of an org
    fetchMembers(orgId: string) {
        return client.get(`/orgs/${orgId}/members`)
    },

    // Add member to an org
    addMember(orgId: string, payload: AddMemberPayload) {
        return client.post(`/orgs/${orgId}/members`, payload)
    },

    // Update member role
    updateMemberRole(orgId: string, userId: number, payload: UpdateMemberRolePayload) {
        return client.patch(`/orgs/${orgId}/members/${userId}`, payload)
    },

    // Remove member from org
    removeMember(orgId: string, userId: number) {
        return client.delete(`/orgs/${orgId}/members/${userId}`)
    },

    // Transfer ownership
    transferOwnership(orgId: string, newOwnerId: number) {
        return client.post(`/orgs/${orgId}/transfer-owner`, { new_owner_id: newOwnerId })
    },
}
