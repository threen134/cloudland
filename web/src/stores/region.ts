import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import client from '../api/client'

export interface Region {
    id: string
    name: string
    label?: string
    status?: 'available' | 'maintenance' | 'offline'
    endpoint?: string
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

    // Initialize from localStorage
    const init = () => {
        const storedRegionId = localStorage.getItem('cloudland_region_id')
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
            // API now returns uuid instead of id
            regions.value = data.map((r: any) => {
                let status: 'available' | 'maintenance' | 'offline' = 'available'
                if (r.maintenance_mode) {
                    status = 'maintenance'
                } else if (!r.is_available) {
                    status = 'offline'
                }

                return {
                    id: r.uuid,
                    name: r.name,
                    label: r.description || r.name,
                    status,
                    endpoint: r.endpoint_url,
                }
            })

            // Auto-select first available region if none selected or current selection no longer valid
            // Use availableRegions to avoid selecting an offline region by default
            const currentExists = availableRegions.value.some(r => r.id === currentRegionId.value)
            if ((!currentRegionId.value || !currentExists) && availableRegions.value.length > 0) {
                setCurrentRegion(availableRegions.value[0].id)
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
        localStorage.setItem('cloudland_region_id', regionId)
        // Also persist as region_uuid for API query parameter usage
        localStorage.setItem('cloudland_region_uuid', regionId)
    }

    // Clear region state (on logout)
    const clear = () => {
        regions.value = []
        currentRegionId.value = null
        localStorage.removeItem('cloudland_region_id')
        localStorage.removeItem('cloudland_region_uuid')
    }

    // Initialize on store creation
    init()

    return {
        regions,
        currentRegionId,
        currentRegion,
        availableRegions,
        isLoading,
        error,
        fetchRegions,
        setCurrentRegion,
        clear
    }
})
