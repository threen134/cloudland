import { STORAGE_KEYS } from '../utils/storage'
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { authApi, type UserOrgItem } from '../api/auth'
import { setAuthToken, beginTokenSwitch, decodeTokenClaims, getToken } from '../api/client'
import { useAuthStore } from './auth'
import { errorMessage } from '../utils/error'

/**
 * GET /auth/me/orgs 返回的 UserOrgItem 加上一个前端补出来的 id（= uuid）。
 * 原来是 Partial + [key: string]: any 的松散结构，这里直接沿用接口类型。
 */
export interface Organization extends UserOrgItem {
    id: string
}

export const useTenantStore = defineStore('tenant', () => {
    const organizations = ref<Organization[]>([])
    const currentOrgId = ref<string | null>(null)
    const isLoading = ref(false)
    const isSwitching = ref(false)
    const error = ref<string | null>(null)

    // Current organization computed property
    const currentOrg = computed(() => {
        return organizations.value.find(org => org.id === currentOrgId.value) || null
    })

    // Initialize from localStorage
    const init = () => {
        const storedOrgId = localStorage.getItem(STORAGE_KEYS.orgId)
        if (storedOrgId) {
            currentOrgId.value = storedOrgId
        }
    }

    // Fetch organizations the current user belongs to
    const fetchOrganizations = async () => {
        isLoading.value = true
        error.value = null

        try {
            // GET /auth/me/orgs 直接返回数组（cpgateway 的 GetMyOrgs）
            const raw = await authApi.getMyOrgs()
            // Backend returns uuid as the identifier
            organizations.value = raw.map((o) => ({
                ...o,
                id: o.uuid,
            }))

            // Ensure we have an org selected and a scoped token
            if (organizations.value.length > 0) {
                const targetOrgId = currentOrgId.value && organizations.value.some(o => o.id === currentOrgId.value)
                    ? currentOrgId.value
                    : organizations.value[0].id
                // Switching issues a new token and revokes the current one: only do it when the token is not
                // scoped to that org yet, so reloading a page does not invalidate the tokens of other windows
                if (decodeTokenClaims(getToken())?.org_id === targetOrgId) {
                    currentOrgId.value = targetOrgId
                    localStorage.setItem(STORAGE_KEYS.orgId, targetOrgId)
                } else {
                    await switchOrg(targetOrgId)
                }
            }
        } catch (err) {
            console.warn('Failed to fetch organizations:', err)
            error.value = errorMessage(err, 'Failed to fetch organizations')
        } finally {
            isLoading.value = false
        }
    }

    // Switch organization - calls backend to get new token scoped to org
    const switchOrg = async (orgId: string) => {
        isSwitching.value = true
        error.value = null
        const endSwitch = beginTokenSwitch()
        const auth = useAuthStore()

        try {
            const response = await authApi.switchOrg(orgId)
            const newToken = response?.access_token
            if (newToken) {
                setAuthToken(newToken)
            }
            localStorage.setItem(STORAGE_KEYS.orgId, orgId)
        } catch (err) {
            console.error('Failed to switch org:', err)
            error.value = errorMessage(err, 'Failed to switch org')
            isSwitching.value = false
            throw err
        } finally {
            // Release token lock IMMEDIATELY after the token is updated.
            // This MUST happen before refreshUser() because refreshUser calls
            // /auth/me which would be blocked by the token switch lock (deadlock).
            endSwitch()
        }

        // Fetch fresh user info AFTER lock is released (this call goes through
        // the normal request interceptor and must not be blocked by our own lock)
        try {
            await auth.refreshUser()
        } catch (err) {
            console.warn('Post-switch user refresh failed:', err)
        }

        isSwitching.value = false

        // Trigger reactivity LAST so RouterView key changes and components re-mount with full new context
        currentOrgId.value = orgId
    }

    // Set current organization (local only, no API call)
    const setCurrentOrg = (orgId: string) => {
        currentOrgId.value = orgId
        localStorage.setItem(STORAGE_KEYS.orgId, orgId)
    }

    // Clear tenant state (on logout)
    const clear = () => {
        organizations.value = []
        currentOrgId.value = null
        localStorage.removeItem(STORAGE_KEYS.orgId)
    }

    // Initialize on store creation
    init()

    return {
        organizations,
        currentOrgId,
        currentOrg,
        isLoading,
        isSwitching,
        error,
        fetchOrganizations,
        switchOrg,
        setCurrentOrg,
        clear
    }
})
