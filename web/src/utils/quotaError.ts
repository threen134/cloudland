// Turns a cpgateway 429 quota_exceeded response into a localized message; returns null for any other error.
// The response detail is {error: 'quota_exceeded', resource, region, requested, available, limit, message}.
type Translate = (key: string, params?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

export function quotaErrorMessage(err: unknown, t: Translate, te: HasKey): string | null {
    const detail = (err as { response?: { data?: { detail?: Record<string, unknown> } } })?.response?.data?.detail
    if (!detail || typeof detail !== 'object' || detail.error !== 'quota_exceeded') return null
    const resource = String(detail.resource ?? '')
    const labelKey = `quota.resources.${resource}`
    return t('quota.exceededMessage', {
        region: detail.region,
        resource: te(labelKey) ? t(labelKey) : resource,
        requested: detail.requested,
        available: detail.available,
        limit: detail.limit,
    })
}
