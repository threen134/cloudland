import client from './client'

export interface AlarmPolicy {
    id: string
    name: string
    description?: string
    severity?: string
    status?: string
    [key: string]: any
}

export const alarmsApi = {
    fetchAlarms() {
        return client.get('/alarm_policies')
    },

    getAlarm(id: string) {
        return client.get(`/alarm_policies/${id}`)
    }
}
