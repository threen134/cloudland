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
    fetchOrgs() {
        return client.get('/orgs')
    },

    // Get single organization
    getOrg(uuid: string) {
        return client.get(`/orgs/${uuid}`)
    },

    // Create organization
    createOrg(payload: CreateOrgPayload) {
        return client.post('/orgs', payload)
    },

    // Update organization
    updateOrg(uuid: string, payload: Partial<CreateOrgPayload>) {
        return client.patch(`/orgs/${uuid}`, payload)
    },

    // Delete organization
    deleteOrg(uuid: string) {
        return client.delete(`/orgs/${uuid}`)
    },

    // --- Member Management ---

    // List members of an org
    fetchMembers(orgUuid: string) {
        return client.get(`/orgs/${orgUuid}/members`)
    },

    // Add member to an org
    addMember(orgUuid: string, payload: AddMemberPayload) {
        return client.post(`/orgs/${orgUuid}/members`, payload)
    },

    // Update member role
    updateMemberRole(orgUuid: string, userUuid: string, payload: UpdateMemberRolePayload) {
        return client.patch(`/orgs/${orgUuid}/members/${userUuid}`, payload)
    },

    // Remove member from org
    removeMember(orgUuid: string, userUuid: string) {
        return client.delete(`/orgs/${orgUuid}/members/${userUuid}`)
    },

    // Transfer ownership
    transferOwnership(orgUuid: string, newOwnerUuid: string) {
        return client.post(`/orgs/${orgUuid}/transfer-owner`, { new_owner_uuid: newOwnerUuid })
    },

    // --- Invitations ---

    // Send invitation
    inviteMember(orgUuid: string, payload: InvitePayload) {
        return client.post(`/orgs/${orgUuid}/invitations`, payload)
    },

    // List pending invitations
    fetchInvitations(orgUuid: string) {
        return client.get(`/orgs/${orgUuid}/invitations`)
    },

    // Cancel invitation
    cancelInvitation(orgUuid: string, invitationUuid: string) {
        return client.delete(`/orgs/${orgUuid}/invitations/${invitationUuid}`)
    },
}
