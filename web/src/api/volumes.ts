import client from './client'

/**
 * Volumes. Mirrors api/src/apis/volume.go (VolumeResponse / VolumeListResponse);
 * the embedded ResourceReference / BaseReference are in api/src/common/http.go.
 */

/** api/src/common/http.go BaseReference */
export interface BaseReference {
    id: string
    name: string
}

/** common/http.go ResourceReference: every field is omitempty */
export interface ResourceRef {
    id?: string
    name?: string
}

/** model.VolumeStatus (api/src/model/volume.go) */
export type VolumeStatus =
    | 'resizing'
    | 'available'
    | 'attached'
    | 'attaching'
    | 'detaching'
    | 'error'
    | 'pending'
    // The host is deleting the file; the volume goes away when it reports back (§5.5 of the storage plan)
    | 'deleting'
    // Deleting timed out: only another delete is allowed
    | 'delete_failed'
    // Its storage pool was declared lost: delete the record or detach it by force
    | 'lost'
    // Its host was deleted with the pools kept: waits for the pool to be adopted
    | 'orphaned'

export interface Volume {
    // --- ResourceReference (omitempty, filled by getVolumeResponse) ---
    id: string
    name: string
    owner?: string
    owner_uuid?: string
    created_at: string
    updated_at: string
    // --- VolumeResponse ---
    /** Path of the file relative to the root of its pool */
    path: string
    /** GB */
    size: number
    format: string
    status: VolumeStatus
    /** Why the last operation failed, or why the volume is lost */
    reason: string
    /** Device name in the guest, e.g. vdb */
    target: string
    href: string
    booting: boolean
    /** null when not attached */
    instance: BaseReference | null
    storage_pool: ResourceRef | null
    /** Host holding the file (system admins only); missing until the first attach */
    hypervisor?: ResourceRef
}

/** POST /volumes body (apis/volume.go VolumePayload) */
export interface VolumePayload {
    name: string
    /** GB */
    size: number
    count?: number
    /** Default pool when left out */
    storage_pool?: { id: string }
}

/**
 * PATCH /volumes/:id body (apis/volume.go VolumePatchPayload). Capacity goes through `resize`, not here.
 * `instance: null` detaches; leaving `instance` out keeps the attachment (e.g. a rename).
 */
export interface VolumePatchPayload {
    name?: string
    instance?: { id: string } | null
}

/** Statuses during which the backend refuses another operation on the volume (model.Volume.IsBusy) */
export const BUSY_VOLUME_STATUSES: VolumeStatus[] = ['pending', 'resizing', 'attaching', 'detaching', 'deleting']

export interface VolumeListResponse {
    offset: number
    total: number
    /** Rows of this page (the backend fills len(volumes), not the requested limit) */
    limit: number
    volumes: Volume[]
}

export const volumesApi = {
    list: async (params?: {
        offset?: number
        limit?: number
        order?: string
        name?: string
        status?: string
        // The backend defaults to data (data volumes only); pass all to include boot volumes
        type?: 'data' | 'boot' | 'all'
    }): Promise<VolumeListResponse> => {
        const response = await client.get<VolumeListResponse>('/volumes', { params })
        return response.data
    },

    get: async (id: string): Promise<Volume> => {
        const response = await client.get<Volume>(`/volumes/${id}`)
        return response.data
    },

    create: async (payload: VolumePayload): Promise<Volume> => {
        const response = await client.post<Volume>('/volumes', payload)
        return response.data
    },

    patch: async (id: string, payload: VolumePatchPayload): Promise<Volume> => {
        const response = await client.patch<Volume>(`/volumes/${id}`, payload)
        return response.data
    },

    /**
     * 202: the host deletes the file and the volume stays in `deleting` until it reports back;
     * 204: the record is gone (a volume not created yet, or a lost one)
     */
    delete: async (id: string): Promise<{ deferred: boolean }> => {
        const response = await client.delete(`/volumes/${id}`)
        return { deferred: response.status === 202 }
    },

    attach: async (id: string, instanceId: string): Promise<Volume> => {
        const response = await client.patch<Volume>(`/volumes/${id}`, {
            instance: { id: instanceId },
        })
        return response.data
    },

    detach: async (id: string): Promise<Volume> => {
        const response = await client.patch<Volume>(`/volumes/${id}`, {
            instance: null,
        })
        return response.data
    },

    // Grow only. PATCH ignores `size`; the resize endpoint is the one the gateway meters against the disk quota.
    // The response body is empty: re-fetch the volume afterwards.
    resize: async (id: string, newSize: number): Promise<void> => {
        await client.post(`/volumes/${id}/resize`, { size: newSize })
    },

    // A volume of a lost pool: drop it from the definition of its instance without touching the file
    forceDetach: async (id: string): Promise<void> => {
        await client.post(`/volumes/${id}/force_detach`)
    },
}

export default volumesApi
