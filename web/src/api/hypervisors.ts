import client from './client'

export interface Hypervisor {
    id: number
    hostname: string
    host_ip: string
    state: string
    status: string
    vcpus: number
    vcpus_used: number
    memory_mb: number
    memory_mb_used: number
    disk_available_least: number
    hypervisor_type: string
    version: number
    cpu_info: string
    cpu_over_commit: number
    mem_over_commit: number
    disk_over_commit: number
    zone: string
    remark: string
}

export const hypervisorsApi = {
    fetchHypervisors() {
        return client.get('/hypervisors')
    },

    getHypervisor(id: string) {
        return client.get(`/hypervisors/${id}`)
    },

    updateHypervisor(id: string, payload: Partial<Hypervisor>) {
        return client.put(`/hypervisors/${id}`, payload)
    }
}
