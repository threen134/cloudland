import client from './client'

// Placement groups (docs/architecture/plan/placement-group-plan.md §7.1): a group spreads its instances over
// different hosts or packs them on one host, strictly or on a best effort basis. Bound to one zone.

export type PlacementPolicy = 'spread' | 'pack'

// One member of a group (detail response only)
export interface PlacementGroupMember {
    id: string
    hostname: string
    status: string
    // Numbering of the hosts inside this group (1, 2, 3…; same host = same number); 0 = no location yet
    host_slot: number
    // Host slot of an in-flight migration target, 0 when none
    target_slot: number
    // Host name, system admins only
    hypervisor?: string
    // Still being created after more than 1 hour
    stale_provisioning: boolean
    // A migration of it has not moved for more than 1 hour
    stale_migration: boolean
    // Uuid of its in-flight migration, when there is one
    migration_id?: string
    // Its latest migration failed (explains a strict pack group split over two hosts)
    last_migration_failed: boolean
    // Its latest migration was done with ignore_placement
    ignored_placement: boolean
}

export interface PlacementGroup {
    id: string
    name: string
    owner?: string
    owner_uuid?: string
    created_at: string
    updated_at: string
    description: string
    policy: PlacementPolicy
    strict: boolean
    // Zone name
    zone: string
    member_count: number
    // Number of distinct hosts the members are on
    host_count: number
    compliant: boolean
    members?: PlacementGroupMember[]
}

// The group an instance belongs to, as carried by the instance responses
export interface PlacementGroupRef {
    id: string
    name: string
    policy: PlacementPolicy
    strict: boolean
}

export interface PlacementGroupListResponse {
    offset: number
    total: number
    limit: number
    placement_groups: PlacementGroup[]
}

export interface CreatePlacementGroupPayload {
    // 2-32 characters
    name: string
    description?: string
    policy: PlacementPolicy
    // Always sent: the backend default (spread strict, pack best effort) is only a fallback
    strict: boolean
    // Zone name; the default zone when left out
    zone?: string
}

// Only the name and the description can change; policy, strict and zone are refused with 400
export interface UpdatePlacementGroupPayload {
    name?: string
    description?: string
}

export const placementGroupsApi = {
    // Paging, search and sorting are done by the server; sortable columns: name, created_at, policy
    async list(params?: {
        offset?: number
        limit?: number
        order?: string
        query?: string
        // Zone name
        zone?: string
    }): Promise<PlacementGroupListResponse> {
        const response = await client.get<PlacementGroupListResponse>('/placement_groups', { params })
        return response.data
    },

    async get(id: string): Promise<PlacementGroup> {
        const response = await client.get<PlacementGroup>(`/placement_groups/${id}`)
        return response.data
    },

    async create(payload: CreatePlacementGroupPayload): Promise<PlacementGroup> {
        const response = await client.post<PlacementGroup>('/placement_groups', payload)
        return response.data
    },

    async update(id: string, payload: UpdatePlacementGroupPayload): Promise<PlacementGroup> {
        const response = await client.patch<PlacementGroup>(`/placement_groups/${id}`, payload)
        return response.data
    },

    // 409 (ErrPlacementGroupInUse) while the group still has members
    async delete(id: string): Promise<void> {
        await client.delete(`/placement_groups/${id}`)
    },
}
