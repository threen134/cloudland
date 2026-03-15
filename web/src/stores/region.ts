import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import client from '../api/client'

export interface Region {
    id: string
    name: string
    label?: string
    status?: 'available' | 'maintenance' | 'offline'
    endpoint?: string
    uuid?: string
}

export const useRegionStore = defineStore('region', () => {
    const regions = ref<Region[]>([])
    const currentRegionId = ref<string | null>(null)
    const isLoading = ref(false)
    const error = ref<string | null>(null)

    // Current region computed property
    const currentRegion = computed(() => {
        return regions.value.find(r => r.id === currentRegionId.value) || null
    })

    // Available regions (filtering by status)
    const availableRegions = computed(() => {
        return regions.value.filter(r => r.status !== 'offline')
    })

    // Current region UUID for API requests
    const currentRegionUuid = computed(() => {
        return currentRegion.value?.uuid || null
    })

    // Initialize from localStorage
    const init = () => {
        const storedRegionId = localStorage.getItem('ibm_cloud_china_region_id')
        if (storedRegionId) {
            currentRegionId.value = storedRegionId
        }
    }

    // Fetch regions from API
    const fetchRegions = async () => {
        isLoading.value = true
        error.value = null

        try {
            const response = await client.get('/regions')
            const data = Array.isArray(response.data) ? response.data : (response.data?.regions || [])

            // Map API response to Region interface
            // API returns: { id, name, description, endpoint_url, uuid, ... }
            regions.value = data.map((r: any) => ({
                id: String(r.id),
                name: r.name,
                label: r.description || r.name,
                status: 'available' as const,
                endpoint: r.endpoint_url,
                uuid: r.uuid,
            }))

            // Auto-select first available region if none selected or current selection no longer valid
            const currentExists = regions.value.some(r => r.id === currentRegionId.value)
            if ((!currentRegionId.value || !currentExists) && regions.value.length > 0) {
                setCurrentRegion(regions.value[0].id)
            } else {
                // Ensure UUID is persisted even if region selection didn't change
                const current = regions.value.find(r => r.id === currentRegionId.value)
                if (current?.uuid) {
                    localStorage.setItem('ibm_cloud_china_region_uuid', current.uuid)
                }
            }
        } catch (err: any) {
            console.warn('Failed to fetch regions:', err)
            error.value = err.message
        } finally {
            isLoading.value = false
        }
    }

    // Set current region
    const setCurrentRegion = (regionId: string) => {
        currentRegionId.value = regionId
        localStorage.setItem('ibm_cloud_china_region_id', regionId)
        // Persist the UUID for API query parameter usage
        const region = regions.value.find(r => r.id === regionId)
        if (region?.uuid) {
            localStorage.setItem('ibm_cloud_china_region_uuid', region.uuid)
        }
    }

    // Clear region state (on logout)
    const clear = () => {
        regions.value = []
        currentRegionId.value = null
        localStorage.removeItem('ibm_cloud_china_region_id')
        localStorage.removeItem('ibm_cloud_china_region_uuid')
    }

    // Initialize on store creation
    init()

    return {
        regions,
        currentRegionId,
        currentRegion,
        currentRegionUuid,
        availableRegions,
        isLoading,
        error,
        fetchRegions,
        setCurrentRegion,
        clear
    }
})
