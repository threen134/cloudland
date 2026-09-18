import apiClient from './client'

export type InfrastructureMode = 'minio' | 'external_s3' | 'legacy'

export interface InfrastructureConfig {
    region_uuid: string
    region_name: string
    mode: InfrastructureMode
    s3_enabled: boolean
    s3_endpoint: string
    s3_access_key: string
    s3_secret_key: string         // always masked ("******" when set, "" when empty)
    s3_secret_key_set: boolean
    s3_bucket: string
    s3_region: string
    s3_use_ssl: boolean
    s3_upload_timeout_minutes: number
    minio_hostname: string
    clapi_hostname: string
    clapi_internal_url: string
    capture_upload_secret: string     // always masked
    capture_upload_secret_set: boolean
}

export interface TestS3Response {
    success: boolean
    message: string
    latency_ms?: number
}

export const infrastructureApi = {
    async get(): Promise<InfrastructureConfig> {
        const response = await apiClient.get('/system/infrastructure')
        return response.data
    },

    async testS3(): Promise<TestS3Response> {
        const response = await apiClient.post('/system/infrastructure/test-s3')
        return response.data
    },
}
