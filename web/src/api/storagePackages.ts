import client from './client'

/**
 * The storage package repository (shared-storage-design.md §6.1): installers of storage software uploaded in parts
 * and verified by clapi; a deployment needs one whose license is accepted. Mirrors api/src/apis/storage_package.go;
 * every route is for system admins.
 */

export type StoragePackageStatus = 'uploading' | 'verifying' | 'ready' | 'error'

export interface StoragePackage {
    id: string
    kind: string
    edition?: string
    version?: string
    file_name: string
    size_bytes: number
    sha256?: string
    /** Parts are part_size bytes, the last one less; the next part to send is parts_done + 1 */
    part_size: number
    total_parts: number
    parts_done: number
    source_url?: string
    distros: string[]
    status: StoragePackageStatus
    reason?: string
    accepted_by?: string
    accepted_at?: string
    created_at: string
    /** Detail only: the license text by language (en, zh, zh_TW) */
    license?: Record<string, string>
    manifest_count?: number
}

export const storagePackagesApi = {
    list: async (params: { offset?: number; limit?: number } = {}) => {
        const response = await client.get<{ total: number; storage_packages: StoragePackage[] }>('/storage_packages', {
            params,
        })
        return response.data
    },
    get: async (id: string): Promise<StoragePackage> => {
        const response = await client.get<StoragePackage>(`/storage_packages/${id}`)
        return response.data
    },
    startUpload: async (kind: string, fileName: string, sizeBytes: number): Promise<StoragePackage> => {
        const response = await client.post<StoragePackage>('/storage_packages', {
            kind,
            file_name: fileName,
            size_bytes: sizeBytes,
        })
        return response.data
    },
    startDownload: async (kind: string, url: string): Promise<StoragePackage> => {
        const response = await client.post<StoragePackage>('/storage_packages', { kind, url })
        return response.data
    },
    uploadPart: async (id: string, n: number, body: Blob, signal?: AbortSignal): Promise<StoragePackage> => {
        const response = await client.put<StoragePackage>(`/storage_packages/${id}/parts/${n}`, body, {
            headers: { 'Content-Type': 'application/octet-stream' },
            signal,
            // A part is 8 MiB: give it time on a slow line
            timeout: 10 * 60 * 1000,
        })
        return response.data
    },
    complete: async (id: string): Promise<StoragePackage> => {
        const response = await client.post<StoragePackage>(`/storage_packages/${id}/complete`)
        return response.data
    },
    acceptLicense: async (id: string): Promise<StoragePackage> => {
        const response = await client.post<StoragePackage>(`/storage_packages/${id}/accept_license`)
        return response.data
    },
    remove: async (id: string): Promise<void> => {
        await client.delete(`/storage_packages/${id}`)
    },
}
