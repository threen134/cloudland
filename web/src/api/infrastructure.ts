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
    sci_shared_secret: string     // always masked
    sci_shared_secret_set: boolean
}

export interface TestS3Response {
    success: boolean
    message: string
    latency_ms?: number
}

export const infrastructureApi = {
    get(): Promise<{ data: InfrastructureConfig }> {
        return apiClient.get('/system/infrastructure')
    },

    testS3(): Promise<{ data: TestS3Response }> {
        return apiClient.post('/system/infrastructure/test-s3')
    },
}
