import type { StatusVariant } from './status'
import { isValidCIDRv4 } from './validation'

// Texts and helpers of a transit gateway shared by the list, the detail page and its modals. The keys live under
// dashboard.transitGateway.

type Translate = (key: string, params?: Record<string, unknown>) => string
type Exists = (key: string) => boolean

// A known value is translated; an unknown one (added on the backend later) shows as it is, never as an i18n key
const translated = (t: Translate, te: Exists, group: string, value?: string): string => {
    if (!value) return '-'
    const key = `dashboard.transitGateway.${group}.${value}`
    return te(key) ? t(key) : value
}

/** available / deleting */
export const tgwStatusText = (t: Translate, te: Exists, status?: string) => translated(t, te, 'statuses', status)

/** attaching / available / detaching / error */
export const attachmentStatusText = (t: Translate, te: Exists, status?: string) =>
    translated(t, te, 'attachmentStatuses', status)

/** synced / syncing / error */
export const syncStatusText = (t: Translate, te: Exists, status?: string) => translated(t, te, 'syncStatuses', status)

/** ok / pending / error */
export const nodeStatusText = (t: Translate, te: Exists, status?: string) => translated(t, te, 'nodeStatuses', status)

/** propagated / static / blackhole */
export const routeTypeText = (t: Translate, te: Exists, type?: string) => translated(t, te, 'routeTypes', type)

// synced and ok are good states: the generic status map does not know them
export const syncStatusVariant = (status?: string): StatusVariant =>
    status === 'synced' ? 'success' : status === 'error' ? 'error' : 'pending'

export const nodeStatusVariant = (status?: string): StatusVariant =>
    status === 'ok' ? 'success' : status === 'error' ? 'error' : status === 'leaving' ? 'neutral' : 'pending'

/** Badge class of a route type */
export const routeTypeBadge = (type?: string): string =>
    type === 'blackhole' ? 'badge-error' : type === 'static' ? 'badge-primary' : 'badge-info'

// The two reason shapes clapi writes on an attachment (see TgwAttachment.status_reason)
const WAITING_REASON = /^waiting for nodes (.+)$/
const NODE_SEGMENT = /^node ([^\s:;]+): ([\s\S]*)$/
// Segments are joined by "; "; the script message of a segment may contain "; " itself, so only split before the
// next "node <label>: "
const SEGMENT_SPLIT = /; (?=node [^\s:;]+: )/

/**
 * The reason of an attachment in the viewer's language: "waiting for nodes X" and the "node X: <message>" segments
 * are translated, the script message is kept as it is. A reason of any other shape is returned unchanged.
 */
export const localizeTgwReason = (t: Translate, reason?: string): string => {
    const text = (reason || '').trim()
    if (!text) return ''
    const waiting = WAITING_REASON.exec(text)
    if (waiting) return t('dashboard.transitGateway.reasonWaiting', { nodes: waiting[1] })
    const segments = text.split(SEGMENT_SPLIT)
    const parsed = segments.map((s) => NODE_SEGMENT.exec(s))
    if (parsed.some((m) => !m)) return text
    return parsed
        .map((m) => t('dashboard.transitGateway.reasonNode', { node: m![1], message: m![2] }))
        .join(t('dashboard.transitGateway.reasonSeparator'))
}

/** Attachment states the nodes still have to apply: the detail page polls while one is shown */
export const BUSY_ATTACHMENT_STATUSES = ['attaching', 'detaching']

/** Split a comma / whitespace / newline separated list of CIDRs, dropping empty items and duplicates */
export const parseCidrList = (text: string): string[] => {
    const seen = new Set<string>()
    for (const item of text.split(/[\s,，;；]+/)) {
        const cidr = item.trim()
        if (cidr) seen.add(cidr)
    }
    return [...seen]
}

/** The items of a list that are not IPv4 CIDRs */
export const invalidCidrs = (list: string[]): string[] => list.filter((cidr) => !isValidCIDRv4(cidr))

/** A CIDR with a /0 prefix: refused as a static route destination (it would take the internet traffic) */
export const isDefaultRoute = (cidr: string): boolean => /\/0+$/.test(cidr.trim())

/** Maximum number of prefixes of a propagation (clapi binding max=16) */
export const MAX_PROPAGATION_PREFIXES = 16
