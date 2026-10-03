import client from './client'

// Transit gateways (docs/architecture/plan/vpc-transit-gateway-plan.md §4): VPCs of one organization in one region
// attached to the same gateway reach each other. Every attachment is routed by one route table of the gateway; a
// route table holds the subnets propagated into it (optionally filtered by prefixes) and static routes / blackholes.

export type TgwSyncStatus = 'synced' | 'syncing' | 'error'
// leaving: the node left the gateway (no member instance there any more), its removal is not confirmed yet
export type TgwNodeStatus = 'ok' | 'error' | 'pending' | 'leaving'
export type TgwAttachmentStatus = 'attaching' | 'available' | 'detaching' | 'error'
export type TgwRouteType = 'static' | 'blackhole'
export type TgwEffectiveRouteType = 'propagated' | 'static' | 'blackhole'

export interface TgwNamedRef {
    id: string
    name: string
}

// An attachment named by its VPC
export interface TgwAttachmentRef {
    id: string
    vpc: TgwNamedRef
}

// One node of the gateway: a node hosting an instance of a member VPC (detail response only)
export interface TgwNode {
    // Number of the node in the list (1, 2, …): the attachment reasons name the nodes by it for members who are
    // not system admins
    index: number
    // Host name, system admins only
    hypervisor?: string
    // Latest generation the node applied
    generation: number
    status: TgwNodeStatus
    reason?: string
}

export interface TransitGateway {
    id: string
    name: string
    owner?: string
    owner_uuid?: string
    created_at: string
    updated_at: string
    description: string
    // available | deleting
    status: string
    attachment_count: number
    // Detail only. synced: every node applied the latest change; syncing: some did not yet; error: a node failed
    sync_status?: TgwSyncStatus
    // Detail only: generation of the desired state (left out while 0)
    generation?: number
    nodes?: TgwNode[]
    // Detail only: pairs of attachments where "from" reaches "to" but the replies from "to" have no route back
    // (route tables propagated one way only); the traffic between them is dropped
    asymmetric_routes?: TgwAsymmetricRoute[]
}

export interface TgwAsymmetricRoute {
    from: TgwAttachmentRef
    to: TgwAttachmentRef
}

export interface TransitGatewayListResponse {
    offset: number
    total: number
    limit: number
    transit_gateways: TransitGateway[]
}

export interface CreateTransitGatewayPayload {
    // 2-32 characters
    name: string
    description?: string
}

export interface UpdateTransitGatewayPayload {
    name?: string
    description?: string
}

export interface TgwAttachment {
    id: string
    vpc: TgwNamedRef
    // The route table the traffic of the VPC is routed by
    route_table: TgwNamedRef
    status: TgwAttachmentStatus
    // Why it is in error or still waiting, rewritten per viewer: "node <label>: <script message>" segments joined by
    // "; ", or "waiting for nodes <label>, <label>". The label is the host name for system admins, the node index
    // (nodes[].index, "?" for a node no longer listed) for the other members. See localizeTgwReason
    status_reason?: string
    // Internal subnets of the VPC
    subnets: string[]
    // The /31 of the attachment: the address in the VPC router and the one in the gateway
    router_address: string
    gateway_address: string
    created_at: string
}

export interface CreateTgwAttachmentPayload {
    vpc: { id: string }
    // The default table when left out
    route_table?: { id: string }
    // Add the subnets of the VPC to the default route table (true when left out)
    propagate?: boolean
}

export interface TgwPropagation {
    id: string
    attachment: TgwAttachmentRef
    // Allow list; empty: every subnet of the VPC
    prefixes: string[]
}

export interface TgwRoute {
    id: string
    destination: string
    type: TgwRouteType
    // Static routes only
    attachment?: TgwAttachmentRef
}

export interface TgwRouteTable {
    id: string
    name: string
    is_default: boolean
    created_at: string
    // Attachments routed by this table (the ones being detached left out)
    associations: TgwAttachmentRef[]
    propagations: TgwPropagation[]
    routes: TgwRoute[]
}

export interface TgwEffectiveRoute {
    destination: string
    type: TgwEffectiveRouteType
    // The attachment the destination goes to; none for a blackhole
    attachment?: TgwAttachmentRef
}

export interface CreateTgwPropagationPayload {
    attachment: { id: string }
    // At most 16; each must overlap a subnet of the VPC. Empty or left out: every subnet
    prefixes?: string[]
}

// Exactly one of attachment / blackhole true
export interface CreateTgwRoutePayload {
    destination: string
    attachment?: { id: string }
    blackhole?: boolean
}

const base = (id: string) => `/transit_gateways/${id}`

export const transitGatewaysApi = {
    // Paging, search and sorting are done by the server; sortable columns: name, created_at
    async list(params?: {
        offset?: number
        limit?: number
        order?: string
        query?: string
    }): Promise<TransitGatewayListResponse> {
        const response = await client.get<TransitGatewayListResponse>('/transit_gateways', { params })
        return response.data
    },

    async get(id: string): Promise<TransitGateway> {
        const response = await client.get<TransitGateway>(base(id))
        return response.data
    },

    // 409 (133002) when the name is taken in the organization
    async create(payload: CreateTransitGatewayPayload): Promise<TransitGateway> {
        const response = await client.post<TransitGateway>('/transit_gateways', payload)
        return response.data
    },

    async update(id: string, payload: UpdateTransitGatewayPayload): Promise<TransitGateway> {
        const response = await client.patch<TransitGateway>(base(id), payload)
        return response.data
    },

    // 409 (133003) while VPCs are attached or being detached
    async delete(id: string): Promise<void> {
        await client.delete(base(id))
    },

    // Send the current state to every node of the gateway again
    async resync(id: string): Promise<TransitGateway> {
        const response = await client.post<TransitGateway>(`${base(id)}/resync`)
        return response.data
    },

    // ─── Attachments ─────────────────────────────────────────────────────────
    // The ones being detached included
    async listAttachments(id: string): Promise<TgwAttachment[]> {
        const response = await client.get<{ attachments: TgwAttachment[] }>(`${base(id)}/attachments`)
        return response.data.attachments || []
    },

    // 400 (133014) overlapping networks, 409 (133012) VPC attached already, 409 (133013) too many attachments
    async attach(id: string, payload: CreateTgwAttachmentPayload): Promise<TgwAttachment> {
        const response = await client.post<TgwAttachment>(`${base(id)}/attachments`, payload)
        return response.data
    },

    // Associate the attachment with another route table
    async updateAttachment(id: string, attachmentId: string, routeTableId: string): Promise<TgwAttachment> {
        const response = await client.patch<TgwAttachment>(`${base(id)}/attachments/${attachmentId}`, {
            route_table: { id: routeTableId },
        })
        return response.data
    },

    // The attachment stays as detaching until every node applied the change; detaching again retries
    async detach(id: string, attachmentId: string): Promise<void> {
        await client.delete(`${base(id)}/attachments/${attachmentId}`)
    },

    // ─── Route tables ────────────────────────────────────────────────────────
    async listRouteTables(id: string): Promise<TgwRouteTable[]> {
        const response = await client.get<{ route_tables: TgwRouteTable[] }>(`${base(id)}/route_tables`)
        return response.data.route_tables || []
    },

    // 409 (133022) name taken or too many tables
    async createRouteTable(id: string, name: string): Promise<TgwRouteTable> {
        const response = await client.post<TgwRouteTable>(`${base(id)}/route_tables`, { name })
        return response.data
    },

    async renameRouteTable(id: string, tableId: string, name: string): Promise<TgwRouteTable> {
        const response = await client.patch<TgwRouteTable>(`${base(id)}/route_tables/${tableId}`, { name })
        return response.data
    },

    // 409 (133023) for the default table or while attachments are associated with it
    async deleteRouteTable(id: string, tableId: string): Promise<void> {
        await client.delete(`${base(id)}/route_tables/${tableId}`)
    },

    async effectiveRoutes(id: string, tableId: string): Promise<TgwEffectiveRoute[]> {
        const response = await client.get<{ routes: TgwEffectiveRoute[] }>(
            `${base(id)}/route_tables/${tableId}/effective_routes`
        )
        return response.data.routes || []
    },

    // Returns the table; 409 (133042) when the attachment propagates into it already
    async addPropagation(id: string, tableId: string, payload: CreateTgwPropagationPayload): Promise<TgwRouteTable> {
        const response = await client.post<TgwRouteTable>(`${base(id)}/route_tables/${tableId}/propagations`, payload)
        return response.data
    },

    async deletePropagation(id: string, tableId: string, propagationId: string): Promise<void> {
        await client.delete(`${base(id)}/route_tables/${tableId}/propagations/${propagationId}`)
    },

    // Returns the table; 400 (133014) default route or a VPN network, 409 (133032) the destination exists already
    async addRoute(id: string, tableId: string, payload: CreateTgwRoutePayload): Promise<TgwRouteTable> {
        const response = await client.post<TgwRouteTable>(`${base(id)}/route_tables/${tableId}/routes`, payload)
        return response.data
    },

    async deleteRoute(id: string, tableId: string, routeId: string): Promise<void> {
        await client.delete(`${base(id)}/route_tables/${tableId}/routes/${routeId}`)
    },
}
