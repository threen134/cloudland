import { useI18n } from 'vue-i18n'

/**
 * Reasons the storage code sets on an instance (§6.1, §4.8 of the local storage plan):
 * storage_full while it is paused because its pool ran out of space, storage_pending while it waits for its pool
 * after the host rebooted, start_failed when its pools were fine but libvirt could not start it (the host retries).
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
        return null
    }
    return { storageReason }
}
