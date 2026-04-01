import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { authApi } from '../api/auth'
import { setAuthToken, beginTokenSwitch } from '../api/client'

export interface Organization {
    id: string
    name: string
    slug?: string
    org_role?: number
    is_owner?: boolean
    is_current?: boolean
    [key: string]: any
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
        const storedOrgId = localStorage.getItem('cloudland_org_id')
        if (storedOrgId) {
            currentOrgId.value = storedOrgId
        }
    }

    // Fetch organizations the current user belongs to
    const fetchOrganizations = async () => {
        isLoading.value = true
        error.value = null

        try {
            const response = await authApi.getMyOrgs()
            const raw = Array.isArray(response.data) ? response.data : (response.data?.orgs || [])
            // Backend returns uuid as the identifier
            organizations.value = raw.map((o: any) => ({
                ...o,
                id: o.uuid || o.id,
            }))

            // Ensure we have an org selected and a scoped token
            if (organizations.value.length > 0) {
                const targetOrgId = currentOrgId.value && organizations.value.some(o => o.id === currentOrgId.value)
                    ? currentOrgId.value
                    : organizations.value[0].id
                await switchOrg(targetOrgId)
            }
        } catch (err: any) {
            console.warn('Failed to fetch organizations:', err)
            error.value = err.message
        } finally {
            isLoading.value = false
        }
    }

    // Switch organization - calls backend to get new token scoped to org
    const switchOrg = async (orgId: string) => {
        isSwitching.value = true
        error.value = null
        const endSwitch = beginTokenSwitch()

        try {
            const response = await authApi.switchOrg(orgId)
            const newToken = response.data?.access_token
            if (newToken) {
                setAuthToken(newToken)
            }
            currentOrgId.value = orgId
            localStorage.setItem('cloudland_org_id', orgId)
        } catch (err: any) {
            console.error('Failed to switch org:', err)
            error.value = err.message
            throw err
        } finally {
            endSwitch()
            isSwitching.value = false
        }
    }

    // Set current organization (local only, no API call)
    const setCurrentOrg = (orgId: string) => {
        currentOrgId.value = orgId
        localStorage.setItem('cloudland_org_id', orgId)
    }

    // Clear tenant state (on logout)
    const clear = () => {
        organizations.value = []
        currentOrgId.value = null
        localStorage.removeItem('cloudland_org_id')
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
