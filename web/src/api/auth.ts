import client from './client'

export interface LoginPayload {
    username?: string
    password?: string
}

export interface LoginResponse {
    access_token: string
    token_type: string
    [key: string]: any
}

export const authApi = {
    // Login
    async login(payload: LoginPayload) {
        const formData = new URLSearchParams()
        formData.append('username', payload.username || '')
        formData.append('password', payload.password || '')

        const response = await client.post<LoginResponse>('/auth/token/form', formData, {
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded'
            }
        })
        return response.data
    },

    // Get current user info
    async getUserInfo() {
        const response = await client.get('/auth/me')
        return response.data
    },

    // Register
    async register(payload: any) {
        const response = await client.post('/auth/register', payload)
        return response.data
    },

    // Activate Account
    async activateAccount(token: string) {
        const response = await client.get(`/auth/activate?token=${token}`)
        return response.data
    },

    // Switch organization (returns new token)
    async switchOrg(orgUuid: string, region?: string) {
        const response = await client.post<LoginResponse>('/auth/switch-org', { org_uuid: orgUuid, region })
        return response.data
    },

    // Switch region (returns new token)
    async switchRegion(regionUuid: string) {
        const response = await client.post<LoginResponse>('/auth/switch-region', { region: regionUuid })
        return response.data
    },

    // Get current user's organizations
    async getMyOrgs() {
        const response = await client.get('/auth/me/orgs')
        return response.data
    },

    // Logout
    logout() {
        return Promise.resolve()
    },

    // --- Invitation (public) ---

    // Get invitation info by token
    async getInvitationInfo(token: string) {
        const response = await client.get(`/auth/invitation/info?token=${encodeURIComponent(token)}`)
        return response.data
    },

    // Accept invitation
    async acceptInvitation(payload: { token: string; username?: string; password?: string }) {
        const response = await client.post('/auth/invitation/accept', payload)
        return response.data
    },
}
