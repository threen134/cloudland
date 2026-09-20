import { STORAGE_KEYS } from '../utils/storage'
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { regionsApi, type RegionPublic } from '../api/regions'
import { errorMessage } from '../utils/error'

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
        return regions.value.find((r) => r.id === currentRegionId.value) || null
    })

    // Available regions (filtering by status)
    const availableRegions = computed(() => {
        return regions.value.filter((r) => r.status !== 'offline')
    })

    // Initialize from localStorage
    const init = () => {
        const storedRegionId = localStorage.getItem(STORAGE_KEYS.regionId)
        if (storedRegionId) {
            currentRegionId.value = storedRegionId
        }
    }

    // Fetch regions from API
    const fetchRegions = async () => {
        isLoading.value = true
        error.value = null

        try {
            // 走 api 层，不再自己拼 client.get（返回约定统一在 api/regions.ts 里）
            const data: RegionPublic[] = await regionsApi.fetchRegions()

            // Map API response to Region interface
            // API now returns uuid instead of id
            regions.value = data.map((r) => {
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
                    // 注：GET /regions 的 regionPublicOut 里没有 endpoint_url（公开视图不含地址），
                    // 原先的 endpoint: r.endpoint_url 恒为 undefined 且全站无人读取，去掉赋值
                }
            })

            // Auto-select first available region if none selected or current selection no longer valid
            // Use availableRegions to avoid selecting an offline region by default
            const currentExists = availableRegions.value.some((r) => r.id === currentRegionId.value)
            if ((!currentRegionId.value || !currentExists) && availableRegions.value.length > 0) {
                setCurrentRegion(availableRegions.value[0].id)
            }
        } catch (err) {
            console.warn('Failed to fetch regions:', err)
            error.value = errorMessage(err, 'Failed to fetch regions')
        } finally {
            isLoading.value = false
        }
    }

    // Set current region
    const setCurrentRegion = (regionId: string) => {
        currentRegionId.value = regionId
        localStorage.setItem(STORAGE_KEYS.regionId, regionId)
        // Also persist as region_uuid for API query parameter usage
        localStorage.setItem(STORAGE_KEYS.regionUuid, regionId)
    }

    // Clear region state (on logout)
    const clear = () => {
        regions.value = []
        currentRegionId.value = null
        localStorage.removeItem(STORAGE_KEYS.regionId)
        localStorage.removeItem(STORAGE_KEYS.regionUuid)
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
        clear,
    }
})
