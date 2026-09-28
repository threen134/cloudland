import type { PlacementGroupMember } from '../api/placementGroups'

// Texts of a placement group shared by the list, the detail page, the create-instance modal, the instance
// detail and the migration modal. The keys live under dashboard.placementGroup.

type Translate = (key: string, params?: Record<string, unknown>) => string
type Exists = (key: string) => boolean

interface GroupRule {
    policy: string
    strict: boolean
}

// Key suffix of a policy × strictness combination: spreadStrict, spreadSoft, packStrict, packSoft
const ruleKey = (g: GroupRule) => `${g.policy}${g.strict ? 'Strict' : 'Soft'}`

/** "Spread" / "Pack"; an unknown policy shows as it is rather than as an i18n key */
export const policyText = (t: Translate, te: Exists, policy: string): string => {
    const key = `dashboard.placementGroup.policies.${policy}`
    return te(key) ? t(key) : policy || '-'
}

/** "Strict" / "Best effort" */
export const strictText = (t: Translate, strict: boolean): string =>
    strict ? t('dashboard.placementGroup.strict') : t('dashboard.placementGroup.bestEffort')

/** "Strict spread", "Best-effort pack"… */
export const ruleText = (t: Translate, te: Exists, g: GroupRule): string => {
    const key = `dashboard.placementGroup.rules.${ruleKey(g)}`
    return te(key) ? t(key) : `${policyText(t, te, g.policy)} · ${strictText(t, g.strict)}`
}

/** What the rule does, one line: "each instance on a different host; fails when no free host is left" */
export const ruleHint = (t: Translate, te: Exists, g: GroupRule): string => {
    const key = `dashboard.placementGroup.ruleHints.${ruleKey(g)}`
    return te(key) ? t(key) : ''
}

/** Host numbers used by more than one member (spread violations); members without a location are left out */
export const crowdedSlots = (members: PlacementGroupMember[]): Set<number> => {
    const count = new Map<number, number>()
    for (const m of members) {
        if (m.host_slot > 0) count.set(m.host_slot, (count.get(m.host_slot) || 0) + 1)
    }
    return new Set([...count].filter(([, n]) => n > 1).map(([slot]) => slot))
}
