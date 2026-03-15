import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import client from '../api/client'

export interface Organization {
    id: string
    name: string
    owner: string
    role?: string
    created_at?: string
    updated_at?: string
}

export const useTenantStore = defineStore('tenant', () => {
    const organizations = ref<Organization[]>([])
    const currentOrgId = ref<string | null>(null)
    const isLoading = ref(false)
    const error = ref<string | null>(null)

    // Current organization computed property
    const currentOrg = computed(() => {
        return organizations.value.find(org => org.id === currentOrgId.value) || null
    })

    // Initialize from localStorage
    const init = () => {
        const storedOrgId = localStorage.getItem('ibm_cloud_china_org_id')
        if (storedOrgId) {
            currentOrgId.value = storedOrgId
        }
    }

    // Fetch organizations from API
    const fetchOrganizations = async () => {
        isLoading.value = true
        error.value = null

        try {
            const response = await client.get('/orgs')
            if (response.data?.orgs) {
                organizations.value = response.data.orgs
            } else if (Array.isArray(response.data)) {
                organizations.value = response.data
            }

            // Auto-select first org if none selected
            if (!currentOrgId.value && organizations.value.length > 0) {
                setCurrentOrg(organizations.value[0].id)
            }
        } catch (err: any) {
            console.warn('Failed to fetch organizations, using mock data:', err)
            // Mock data for development
            organizations.value = [
                { id: 'org-1', name: 'Default Organization', owner: 'admin', role: 'owner' },
                { id: 'org-2', name: 'Development Team', owner: 'admin', role: 'member' },
            ]
            if (!currentOrgId.value) {
                setCurrentOrg(organizations.value[0].id)
            }
            error.value = err.message
        } finally {
            isLoading.value = false
        }
    }

    // Set current organization
    const setCurrentOrg = (orgId: string) => {
        currentOrgId.value = orgId
        localStorage.setItem('ibm_cloud_china_org_id', orgId)
    }

    // Clear tenant state (on logout)
    const clear = () => {
        organizations.value = []
        currentOrgId.value = null
        localStorage.removeItem('ibm_cloud_china_org_id')
    }

    // Initialize on store creation
    init()

    return {
        organizations,
        currentOrgId,
        currentOrg,
        isLoading,
        error,
        fetchOrganizations,
        setCurrentOrg,
        clear
    }
})
