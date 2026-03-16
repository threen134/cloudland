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
    login(payload: LoginPayload) {
        const formData = new URLSearchParams()
        formData.append('username', payload.username || '')
        formData.append('password', payload.password || '')

        return client.post<LoginResponse>('/auth/token/form', formData, {
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded'
            }
        })
    },

    // Get current user info
    getUserInfo() {
        return client.get('/auth/me')
    },

    // Register
    register(payload: any) {
        return client.post('/auth/register', payload)
    },

    // Activate Account
    activateAccount(token: string) {
        return client.get(`/auth/activate?token=${token}`)
    },

    // Switch organization (returns new token)
    switchOrg(orgUuid: string, region?: string) {
        return client.post<LoginResponse>('/auth/switch-org', { org_uuid: orgUuid, region })
    },

    // Get current user's organizations
    getMyOrgs() {
        return client.get('/auth/me/orgs')
    },

    // Logout
    logout() {
        return Promise.resolve()
    },

    // --- Invitation (public) ---

    // Get invitation info by token
    getInvitationInfo(token: string) {
        return client.get(`/auth/invitation/info?token=${encodeURIComponent(token)}`)
    },

    // Accept invitation
    acceptInvitation(payload: { token: string; username?: string; password?: string }) {
        return client.post('/auth/invitation/accept', payload)
    },
}
