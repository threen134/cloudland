import client from './client'

export interface ResourceUsage {
    used: number
    total: number
    unit?: string
    percentage: number
}

export interface SystemStats {
    cpu: ResourceUsage
    memory: ResourceUsage
    disk: ResourceUsage
    instances: ResourceUsage
    volume: ResourceUsage
    images: ResourceUsage
    public_ip: ResourceUsage
    private_ip: ResourceUsage
}

export const statsApi = {
    // Get system resource usage stats
    // Search in swagger.json for 'quota', 'stats', 'usage' returned no results.
    // If a specific administrative endpoint exists (e.g. /quotas or /hypervisors/stats), update here.
    // For now, using it as a placeholder.
    getStats() {
        return client.get<SystemStats>('/quotas')
    }
}
