import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { systemSettingsApi } from '../api/systemSettings'

// Hypervisor statuses whose node runs the agent: disabled, active, maintaining
const CONNECTED_STATUSES = [0, 1, 2]

// Host terminal entry on the hypervisor pages (system admins only). The terminal opens in its own window, which
// asks for the password; the entry is disabled while the feature is off in system settings.
export function useHostConsole() {
    const router = useRouter()
    const { t } = useI18n()
    const enabled = ref(false)

    const load = async () => {
        try {
            const res = await systemSettingsApi.list()
            const setting = (res.settings || []).find(s => s.key === 'HOST_CONSOLE_ENABLED')
            enabled.value = setting?.value === true || setting?.value === 'true'
        } catch {
            enabled.value = false
        }
    }

    // Why the terminal cannot be opened for a hypervisor, or '' when it can
    const disabledReason = (status: number) => {
        if (!enabled.value) return t('dashboard.hypervisorActions.consoleDisabled')
        if (!CONNECTED_STATUSES.includes(status)) return t('dashboard.hypervisorActions.consoleOffline')
        return ''
    }

    // Opened without noopener so the new window inherits sessionStorage (login session)
    const openHostConsole = (uuid: string) => {
        const url = router.resolve({ name: 'host-console', params: { id: uuid } }).href
        if (!window.open(url, '_blank')) {
            alert(t('dashboard.instanceDetail.popupBlocked'))
        }
    }

    onMounted(load)

    return { disabledReason, openHostConsole }
}
