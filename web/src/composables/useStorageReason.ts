import { useI18n } from 'vue-i18n'

/**
 * Reasons set on an instance that are worth a notice next to its status.
 * Storage (§6.1, §4.8 of the local storage plan): storage_full while it is paused because its pool ran out of
 * space, storage_pending while it waits for its pool after the host rebooted, start_failed when its pools were
 * fine but libvirt could not start it (the host retries).
 * password_failed: the node could not set the guest password, most likely because the guest agent was not up
 * yet; the next successful password change clears it.
 * Other reasons are not shown here.
 */
export function useStorageReason() {
    const { t } = useI18n()
    const storageReason = (reason?: string) => {
        if (reason === 'storage_full') return { text: t('storage.reasonFull'), hint: t('storage.reasonFullHint') }
        if (reason === 'storage_pending')
            return { text: t('storage.reasonPending'), hint: t('storage.reasonPendingHint') }
        if (reason === 'start_failed')
            return { text: t('storage.reasonStartFailed'), hint: t('storage.reasonStartFailedHint') }
        if (reason === 'password_failed')
            return {
                text: t('dashboard.instanceDetail.reasonPasswordFailed'),
                hint: t('dashboard.instanceDetail.reasonPasswordFailedHint'),
            }
        return null
    }
    return { storageReason }
}
