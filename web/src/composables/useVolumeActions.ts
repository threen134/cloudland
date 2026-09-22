import { onUnmounted, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { BUSY_VOLUME_STATUSES, type Volume, type VolumeStatus } from '../api/volumes'

/**
 * Why an attach / detach / resize button is disabled for a volume ('' when it is allowed).
 * Mirrors the checks in services/volume.go so the user sees the reason before clicking.
 */
export function useVolumeActionGuards() {
    const { t, te } = useI18n()
    const statusText = (status: string) => {
        const key = `dashboard.volumeStatus.${status}`
        return te(key) ? t(key) : status
    }
    const unless = (volume: Volume, allowed: VolumeStatus[]) =>
        allowed.includes(volume.status)
            ? ''
            : t('dashboard.volumeActions.unavailableInStatus', { status: statusText(volume.status) })

    return {
        attachBlocked: (volume: Volume) => unless(volume, ['available']),
        detachBlocked: (volume: Volume) =>
            volume.booting ? t('dashboard.volumeActions.bootNotDetachable') : unless(volume, ['attached']),
        resizeBlocked: (volume: Volume) => unless(volume, ['available', 'attached']),
    }
}

const POLL_INTERVAL_MS = 3000
// Give up ~3 minutes after the busy set last changed: a volume stuck in a transitional state
// (e.g. its node went offline) should not keep the page polling forever
const MAX_POLLS = 60

/**
 * Attach / detach / resize only queue a node command, so the volume shows attaching / detaching /
 * resizing until the node calls back. While any watched volume is in such a state, call `refresh`
 * (a quiet reload) every few seconds.
 */
export function usePollBusyVolumes(volumes: Ref<Volume[] | Volume | null>, refresh: () => Promise<unknown>) {
    let timer: ReturnType<typeof setTimeout> | null = null
    let polls = 0
    let stopped = false
    let lastBusy = ''

    // "id:status" of every busy volume; a new action (or one settling) changes it
    const busySignature = () => {
        const v = volumes.value
        const list = Array.isArray(v) ? v : v ? [v] : []
        return list
            .filter((vol) => BUSY_VOLUME_STATUSES.includes(vol.status))
            .map((vol) => `${vol.id}:${vol.status}`)
            .sort()
            .join(',')
    }

    const schedule = () => {
        if (stopped || timer) return
        const busy = busySignature()
        // The poll budget belongs to the current set of busy volumes: one stuck volume must not
        // stop the refresh for an action started later on the same page
        if (busy !== lastBusy) {
            lastBusy = busy
            polls = 0
        }
        if (!busy || polls >= MAX_POLLS) return
        timer = setTimeout(async () => {
            timer = null
            polls++
            try {
                await refresh()
            } finally {
                // Also covers a failed refresh, which leaves the data (and so the watcher) untouched
                schedule()
            }
        }, POLL_INTERVAL_MS)
    }

    watch(volumes, schedule, { immediate: true })

    onUnmounted(() => {
        stopped = true
        if (timer) clearTimeout(timer)
    })
}
