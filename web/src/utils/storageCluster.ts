// Texts of storage clusters and their tasks. Kinds, statuses and step names come from the backend; one without a
// translation is shown as it is rather than as a key.
import type { StatusVariant } from './status'

type T = (key: string, values?: Record<string, unknown>) => string
type Te = (key: string) => boolean

const lookup = (t: T, te: Te, group: string, value: string | undefined) => {
    if (!value) return '-'
    const key = `storage.cluster.${group}.${value}`
    return te(key) ? t(key) : value
}

export const storageKindText = (t: T, te: Te, kind?: string) => lookup(t, te, 'kinds', kind)
/** The short name of a kind for narrow places (table cells): GPFS rather than GPFS (IBM Storage Scale) */
export const storageKindShort = (t: T, te: Te, kind?: string) => lookup(t, te, 'kindShort', kind)
export const clusterStatusText = (t: T, te: Te, status?: string) => lookup(t, te, 'clusterStatus', status)
export const clusterHealthText = (t: T, te: Te, health?: string) => lookup(t, te, 'healthStatus', health)
export const taskKindText = (t: T, te: Te, kind?: string) => lookup(t, te, 'taskKinds', kind)
export const taskStatusText = (t: T, te: Te, status?: string) => lookup(t, te, 'taskStatus', status)
export const stepStatusText = (t: T, te: Te, status?: string) => lookup(t, te, 'stepStatus', status)
export const runStatusText = (t: T, te: Te, status?: string) => lookup(t, te, 'runStatus', status)
export const stepNameText = (t: T, te: Te, name?: string) => lookup(t, te, 'stepNames', name)
export const roleText = (t: T, te: Te, role?: string) => lookup(t, te, 'roles', role)
export const checkStatusText = (t: T, te: Te, status?: string) => lookup(t, te, 'checkStatusText', status)

/** Tasks, steps and runs: "running" is work in progress here, not the good state it is for a server */
export const taskVariant = (status?: string): StatusVariant => {
    switch (status) {
        case 'succeeded':
            return 'success'
        case 'failed':
            return 'error'
        case 'aborted':
            return 'neutral'
        default:
            return 'pending'
    }
}

/** Name of a precheck item; disks and data directories carry their id or path */
export const checkNameText = (t: T, te: Te, name: string) => {
    const [kind, ...rest] = name.split(' ')
    const arg = rest.join(' ')
    const key = `storage.cluster.checkNames.${kind}`
    if (!te(key)) return name
    return arg ? t(key, { arg }) : t(key)
}

export const checkStatusVariant = (status: string): StatusVariant =>
    status === 'ok' ? 'success' : status === 'warn' ? 'warning' : 'error'

/** A step timeout as minutes or seconds */
export const formatTimeout = (t: T, seconds: number) =>
    seconds % 60 === 0
        ? t('storage.cluster.minutes', { n: seconds / 60 })
        : t('storage.cluster.seconds', { n: seconds })
