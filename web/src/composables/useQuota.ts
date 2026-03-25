import { ref } from 'vue'
import { quotaApi, type OrgResourceSummary, type OrgResourceQuotaUpdate } from '../api/quota'

export function useQuota() {
    const quotaSummary = ref<OrgResourceSummary | null>(null)
    const quotaLoading = ref(false)
    const quotaError = ref('')
    const editingQuota = ref<Record<string, OrgResourceQuotaUpdate>>({})
    const savingQuota = ref<string | null>(null)

    const fetchQuota = async (orgId: string) => {
        quotaLoading.value = true
        quotaError.value = ''
        try {
            const response = await quotaApi.getOrgResourceSummary(orgId)
            quotaSummary.value = response.data
            editingQuota.value = {}
            for (const region of (response.data.regions || [])) {
                editingQuota.value[region.region_name] = {
                    max_cpu_cores: region.quota.max_cpu_cores,
                    max_ram_gb: region.quota.max_ram_gb,
                    max_public_ips: region.quota.max_public_ips,
                    max_disk_gb: region.quota.max_disk_gb,
                }
            }
        } catch (err: any) {
            quotaError.value = err.response?.data?.detail || 'Failed to load quota'
        } finally {
            quotaLoading.value = false
        }
    }

    const handleSaveQuota = async (orgId: string, regionName: string) => {
        savingQuota.value = regionName
        try {
            await quotaApi.updateOrgQuota(orgId, regionName, editingQuota.value[regionName])
            await fetchQuota(orgId)
        } catch (err: any) {
            quotaError.value = err.response?.data?.detail || 'Failed to update quota'
        } finally {
            savingQuota.value = null
        }
    }

    const getUsagePercent = (used: number, limit: number) => {
        if (limit <= 0) return 0
        return Math.min(100, Math.round((used / limit) * 100))
    }

    const getUsageColor = (percent: number) => {
        if (percent > 90) return 'var(--error-color, #ef4444)'
        if (percent > 75) return '#f59e0b'
        return '#22c55e'
    }

    return {
        quotaSummary,
        quotaLoading,
        quotaError,
        editingQuota,
        savingQuota,
        fetchQuota,
        handleSaveQuota,
        getUsagePercent,
        getUsageColor,
    }
}
