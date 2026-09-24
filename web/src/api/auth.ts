import client from './client'

/**
 * 认证相关接口走控制面网关 cpgateway，响应形状以 cpgateway/src/apis/auth.go 的
 * handler 实际拼装的 JSON 为准（不是 clapi 的模型）。
 */

export interface LoginPayload {
    username?: string
    password?: string
}

/**
 * POST /auth/token、/auth/token/form、/auth/switch-org、/auth/switch-region 的响应。
 * 依据：cpgateway/src/services/auth.go 的 TokenWithContext + newTokenResponse。
 * org_uuid / org_name / region 是指针，未绑定组织时为 null。
 */
export interface LoginResponse {
    access_token: string
    /** 固定为 "bearer" */
    token_type: string
    /** 秒数（access_token_expire_minutes * 60） */
    expires_in: number
    org_uuid: string | null
    org_name: string | null
    region: string | null
}

/**
 * GET /auth/me 的响应。依据：cpgateway/src/apis/auth.go GetMe —— 是一个扁平对象，
 * 没有外层 { message, user } 包装。
 */
export interface MeResponse {
    uuid: string
    email: string
    username: string
    first_name: string
    last_name: string
    /** model.SystemRole 的整数值 */
    system_role: number
    is_superuser: boolean
    /** UserStatus.Name()：invited | active | dormant | disabled */
    status: string
    /** 当前 token 里的组织 UUID，未绑定组织时为空字符串 */
    current_org_uuid: string
    /** 当前 token 里的区域 UUID */
    current_region: string
}

/** cpgateway/src/apis/schemas.go 的 userOut（注册成功时返回的用户对象） */
export interface UserOut {
    email: string
    username: string
    language: string
    uuid: string
    is_active: boolean
    is_superuser: boolean
    system_role: number
    status: string
    first_name: string
    last_name: string
    remark: string
    created_at: string
}

/** POST /auth/register 的请求体。依据：cpgateway/src/services/auth.go 的 RegisterInput */
export interface RegisterPayload {
    email: string
    username: string
    password: string
    org_name: string
    org_slug: string
    language?: string
}

/** POST /auth/register 的响应（201） */
export interface RegisterResponse {
    message: string
    user: UserOut
}

/** GET /auth/activate 的响应 —— cpgateway/src/apis/auth.go ActivateAccount */
export interface ActivateResponse {
    /** "Account activated" 或 "Account already activated" */
    message: string
    result: boolean
    status: string
}

/**
 * GET /auth/me/orgs 的响应元素。
 * 依据：cpgateway/src/apis/auth.go 的 userOrgItem —— 接口直接返回数组，没有外层包装。
 */
export interface UserOrgItem {
    uuid: string
    name: string
    slug: string
    /** model.OrgType 的整数值 */
    org_type: number
    /** model.OrgStatus 的整数值 */
    status: number
    /** model.OrgRole 的整数值 */
    org_role: number
    is_owner: boolean
    is_current: boolean
}

/** GET /auth/invitation/info —— cpgateway/src/services/invitation.go GetInvitationInfo */
export interface InvitationInfo {
    email: string
    org_name: string
    /** model.OrgRole 的整数值 */
    org_role: number
    inviter_email: string
    is_existing_user: boolean
    expires_at: string | null
}

/** POST /auth/invitation/accept —— cpgateway/src/services/invitation.go AcceptInvitation */
export interface AcceptInvitationResponse {
    message: string
    org_uuid: string
    org_name: string
    is_new_user: boolean
}

export const authApi = {
    // Login
    async login(payload: LoginPayload): Promise<LoginResponse> {
        const formData = new URLSearchParams()
        formData.append('username', payload.username || '')
        formData.append('password', payload.password || '')

        const response = await client.post<LoginResponse>('/auth/token/form', formData, {
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
            },
        })
        return response.data
    },

    // Get current user info
    async getUserInfo(): Promise<MeResponse> {
        const response = await client.get<MeResponse>('/auth/me')
        return response.data
    },

    // Register
    async register(payload: RegisterPayload): Promise<RegisterResponse> {
        const response = await client.post<RegisterResponse>('/auth/register', payload)
        return response.data
    },

    // Activate Account
    async activateAccount(token: string): Promise<ActivateResponse> {
        const response = await client.get<ActivateResponse>(`/auth/activate?token=${token}`)
        return response.data
    },

    // Switch organization (returns new token)
    async switchOrg(orgUuid: string, region?: string): Promise<LoginResponse> {
        const response = await client.post<LoginResponse>('/auth/switch-org', { org_uuid: orgUuid, region })
        return response.data
    },

    // Switch region (returns new token)
    async switchRegion(regionUuid: string): Promise<LoginResponse> {
        const response = await client.post<LoginResponse>('/auth/switch-region', { region: regionUuid })
        return response.data
    },

    // Get current user's organizations
    async getMyOrgs(): Promise<UserOrgItem[]> {
        const response = await client.get<UserOrgItem[]>('/auth/me/orgs')
        return response.data
    },

    // Logout
    logout(): Promise<void> {
        return Promise.resolve()
    },

    // --- Invitation (public) ---

    // Get invitation info by token
    async getInvitationInfo(token: string): Promise<InvitationInfo> {
        const response = await client.get<InvitationInfo>(`/auth/invitation/info?token=${encodeURIComponent(token)}`)
        return response.data
    },

    // Accept invitation
    async acceptInvitation(payload: {
        token: string
        username?: string
        password?: string
    }): Promise<AcceptInvitationResponse> {
        const response = await client.post<AcceptInvitationResponse>('/auth/invitation/accept', payload)
        return response.data
    },
}
