import { useI18n } from 'vue-i18n'

// Relative ("5 mins ago") and absolute (locale string) time for activity entries.
export function useActivityTime() {
    const { t } = useI18n()

    const relativeTime = (iso: string) => {
        const mins = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
        if (mins < 1) return t('dashboard.overview.justNow')
        if (mins < 60) return t('dashboard.overview.minsAgo', { n: mins })
        const hours = Math.floor(mins / 60)
        if (hours < 24) return t('dashboard.overview.hoursAgo', { n: hours })
        return t('dashboard.overview.daysAgo', { n: Math.floor(hours / 24) })
    }

    const absoluteTime = (iso: string) => new Date(iso).toLocaleString()

    return { relativeTime, absoluteTime }
}
