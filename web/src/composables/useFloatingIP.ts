import { useI18n } from 'vue-i18n'

const typeBadgeClassMap: Record<string, string> = {
    'floating': 'badge-primary',
    'site': 'badge-success',
    'loadbalancer': 'badge-warning',
    'internal': 'badge-info',
    'native': 'badge-primary'
}

export function useFloatingIP() {
    const { t } = useI18n()

    const getTypeBadgeClass = (type: string) => {
        return typeBadgeClassMap[type] || 'badge-gray'
    }

    const getTypeLabel = (type: string) => {
        if (!type) return '-'
        const key = `dashboard.floatingIPType.${type}`
        const translated = t(key)
        return translated === key ? type : translated
    }

    return { getTypeBadgeClass, getTypeLabel }
}
