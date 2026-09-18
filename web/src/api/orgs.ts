import client from './client'

export interface Organization {
    uuid: string
    name: string
    slug?: string
    description?: string
    created_at?: string
    owner_uuid?: string
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
    uuid: string
    user_uuid: string
    username: string
    email: string
    org_role: number
    is_owner: boolean
    joined_at?: string
    [key: string]: any
}

export interface AddMemberPayload {
    user_uuid: string
    org_role?: number
}

export interface InvitePayload {
    email: string
    org_role?: number
}

export interface OrgInvitation {
    uuid: string
    email: string
    org_uuid: string
    org_name: string
    org_role: number
    status: number
    inviter_email: string
    created_at: string
    expires_at: string
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
    async fetchOrgs() {
        const response = await client.get('/orgs')
        return response.data
    },

    // Get single organization
    async getOrg(uuid: string) {
        const response = await client.get(`/orgs/${uuid}`)
        return response.data
    },

    // Create organization
    async createOrg(payload: CreateOrgPayload) {
        const response = await client.post('/orgs', payload)
        return response.data
    },

    // Update organization
    async updateOrg(uuid: string, payload: Partial<CreateOrgPayload>) {
        const response = await client.patch(`/orgs/${uuid}`, payload)
        return response.data
    },

    // Delete organization
    async deleteOrg(uuid: string) {
        const response = await client.delete(`/orgs/${uuid}`)
        return response.data
    },

    // --- Member Management ---

    // List members of an org
    async fetchMembers(orgUuid: string) {
        const response = await client.get(`/orgs/${orgUuid}/members`)
        return response.data
    },

    // Add member to an org
    async addMember(orgUuid: string, payload: AddMemberPayload) {
        const response = await client.post(`/orgs/${orgUuid}/members`, payload)
        return response.data
    },

    // Update member role
    async updateMemberRole(orgUuid: string, userUuid: string, payload: UpdateMemberRolePayload) {
        const response = await client.patch(`/orgs/${orgUuid}/members/${userUuid}`, payload)
        return response.data
    },

    // Remove member from org
    async removeMember(orgUuid: string, userUuid: string) {
        const response = await client.delete(`/orgs/${orgUuid}/members/${userUuid}`)
        return response.data
    },

    // Transfer ownership
    async transferOwnership(orgUuid: string, newOwnerUuid: string) {
        const response = await client.post(`/orgs/${orgUuid}/transfer-owner`, { new_owner_uuid: newOwnerUuid })
        return response.data
    },

    // --- Invitations ---

    // Send invitation
    async inviteMember(orgUuid: string, payload: InvitePayload) {
        const response = await client.post(`/orgs/${orgUuid}/invitations`, payload)
        return response.data
    },

    // List pending invitations
    async fetchInvitations(orgUuid: string) {
        const response = await client.get(`/orgs/${orgUuid}/invitations`)
        return response.data
    },

    // Cancel invitation
    async cancelInvitation(orgUuid: string, invitationUuid: string) {
        const response = await client.delete(`/orgs/${orgUuid}/invitations/${invitationUuid}`)
        return response.data
    },

    // Update organization status
    async updateOrgStatus(uuid: string, status: number) {
        const response = await client.patch(`/orgs/${uuid}/status`, { status })
        return response.data
    },
}
