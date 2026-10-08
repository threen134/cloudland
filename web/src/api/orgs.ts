import client from './client'
import type { ListParams } from './listParams'

// 以下类型对应控制面网关 cpgateway/src/apis/schemas.go 与 org_mgmt.go 的实际响应

// schemas.go 的 orgOut
export interface Organization {
    uuid: string
    name: string
    slug: string
    org_type: number
    status: number
    // 无属主时为空字符串 / null
    owner_uuid: string
    owner_name: string | null
    owner_email: string | null
    description: string
    created_at: string
}

// schemas.go 的 orgDetailOut（仅 GET /orgs/:uuid 返回）
export interface OrganizationDetail extends Organization {
    member_count: number
}

// org_mgmt.go CreateOrg 的绑定结构：name 与 slug 均为 required
export interface CreateOrgPayload {
    name: string
    slug: string
}

// org_mgmt.go UpdateOrg 的绑定结构
export interface UpdateOrgPayload {
    name?: string
    description?: string
}

// schemas.go 的 memberOut
export interface OrgMember {
    uuid: string
    user_uuid: string
    org_uuid: string
    org_role: number
    user_email: string | null
    username: string
    is_owner: boolean
    is_superuser: boolean
    // 正式成员为 null，邀请中的成员为邀请状态码
    invitation_status: number | null
    created_at: string
}

// org_mgmt.go AddMember 的绑定结构
export interface AddMemberPayload {
    user_uuid: string
    org_role?: number
}

// org_mgmt.go CreateInvitation 的绑定结构
export interface InvitePayload {
    email: string
    org_role?: number
    // 仅系统组织可用
    is_superuser?: boolean
}

// schemas.go 的 invitationOut
export interface OrgInvitation {
    uuid: string
    email: string
    org_uuid: string
    org_name: string
    org_role: number
    status: number
    inviter_email: string
    created_at: string
    expires_at: string | null
}

// org_mgmt.go UpdateMemberRole 的绑定结构
export interface UpdateMemberRolePayload {
    org_role: number
}

export const ORG_ROLES: Record<number, string> = {
    0: 'None',
    1: 'Reader',
    2: 'Writer',
    3: 'Admin',
}

export interface MemberListResponse {
    total: number
    members: OrgMember[]
}

export interface OrgListResponse {
    total: number
    orgs: Organization[]
}

export const orgsApi = {
    /** GET /orgs —— 服务端分页 + 名称/标识搜索，返回 { total, orgs } */
    async fetchOrgs(params?: ListParams): Promise<OrgListResponse> {
        const response = await client.get<OrgListResponse>('/orgs', { params })
        return response.data
    },

    // Get single organization
    async getOrg(uuid: string): Promise<OrganizationDetail> {
        const response = await client.get<OrganizationDetail>(`/orgs/${uuid}`)
        return response.data
    },

    // Create organization
    async createOrg(payload: CreateOrgPayload): Promise<Organization> {
        const response = await client.post<Organization>('/orgs', payload)
        return response.data
    },

    // Update organization
    async updateOrg(uuid: string, payload: UpdateOrgPayload): Promise<Organization> {
        const response = await client.patch<Organization>(`/orgs/${uuid}`, payload)
        return response.data
    },

    // Delete organization（204 No Content）
    async deleteOrg(uuid: string): Promise<void> {
        await client.delete(`/orgs/${uuid}`)
    },

    // --- Member Management ---

    // List members of an org
    /** GET /orgs/:uuid/members —— 服务端分页 + 用户名/邮箱搜索，返回 { total, members } */
    async fetchMembers(orgUuid: string, params?: ListParams): Promise<MemberListResponse> {
        const response = await client.get<MemberListResponse>(`/orgs/${orgUuid}/members`, { params })
        return response.data
    },

    // Add member to an org
    async addMember(orgUuid: string, payload: AddMemberPayload): Promise<OrgMember> {
        const response = await client.post<OrgMember>(`/orgs/${orgUuid}/members`, payload)
        return response.data
    },

    // Update member role
    async updateMemberRole(orgUuid: string, userUuid: string, payload: UpdateMemberRolePayload): Promise<OrgMember> {
        const response = await client.patch<OrgMember>(`/orgs/${orgUuid}/members/${userUuid}`, payload)
        return response.data
    },

    // Remove member from org（204 No Content）
    async removeMember(orgUuid: string, userUuid: string): Promise<void> {
        await client.delete(`/orgs/${orgUuid}/members/${userUuid}`)
    },

    // Transfer ownership
    async transferOwnership(orgUuid: string, newOwnerUuid: string): Promise<Organization> {
        const response = await client.post<Organization>(`/orgs/${orgUuid}/transfer-owner`, {
            new_owner_uuid: newOwnerUuid,
        })
        return response.data
    },

    // --- Invitations ---

    // Send invitation
    async inviteMember(orgUuid: string, payload: InvitePayload): Promise<OrgInvitation> {
        const response = await client.post<OrgInvitation>(`/orgs/${orgUuid}/invitations`, payload)
        return response.data
    },

    // List pending invitations
    async fetchInvitations(orgUuid: string): Promise<OrgInvitation[]> {
        const response = await client.get<OrgInvitation[]>(`/orgs/${orgUuid}/invitations`)
        return response.data
    },

    // Cancel invitation（204 No Content）
    async cancelInvitation(orgUuid: string, invitationUuid: string): Promise<void> {
        await client.delete(`/orgs/${orgUuid}/invitations/${invitationUuid}`)
    },

    // Update organization status
    async updateOrgStatus(uuid: string, status: number): Promise<Organization> {
        const response = await client.patch<Organization>(`/orgs/${uuid}/status`, { status })
        return response.data
    },
}
