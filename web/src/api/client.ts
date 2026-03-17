import axios from 'axios'
import type { AxiosError, InternalAxiosRequestConfig, AxiosResponse } from 'axios'

// Create axios instance
const client = axios.create({
    baseURL: '/api/v1',
    timeout: 30000,
    headers: {
        'Content-Type': 'application/json',
    },
})

// Request interceptor - add auth token and tenant/region headers
client.interceptors.request.use(
    (config: InternalAxiosRequestConfig) => {
        // Add JWT token if available
        const token = localStorage.getItem('cloudland_token')
        if (token && config.headers) {
            config.headers.Authorization = `Bearer ${token}`
        }

        // Add tenant/organization header
        const orgId = localStorage.getItem('cloudland_org_id')
        if (orgId && config.headers) {
            config.headers['X-Organization-ID'] = orgId
        }

        // Add region UUID as query parameter (skip for /regions endpoint itself)
        const regionUuid = localStorage.getItem('cloudland_region_uuid')
        if (regionUuid && config.url && !config.url.endsWith('/regions')) {
            config.params = config.params || {}
            config.params.region = regionUuid
        }

        return config
    },
    (error: AxiosError) => {
        return Promise.reject(error)
    }
)

// Response interceptor - handle errors globally
client.interceptors.response.use(
    (response: AxiosResponse) => {
        return response
    },
    (error: AxiosError) => {
        // Handle common error cases
        if (error.response) {
            const status = error.response.status

            switch (status) {
                case 429: {
                    // Quota exceeded
                    const data = error.response?.data as any
                    if (data?.error === 'quota_exceeded') {
                        const { resource, region, requested, available, limit } = data
                        console.error(
                            `Quota exceeded: ${resource} in ${region} — requested ${requested}, available ${available} (limit: ${limit})`
                        )
                        // Let the caller handle the structured error
                    }
                    break
                }
                case 401:
                    // Check if we are using a mock token
                    const token = localStorage.getItem('cloudland_token')
                    if (token && token.startsWith('mock-token-')) {
                        console.warn('API 401 ignored due to mock token')
                        break
                    }

                    // Check if 401 is caused by region backend failure (not a real auth issue)
                    const errorDetail = (error.response?.data as any)?.detail || ''
                    if (typeof errorDetail === 'string' && errorDetail.toLowerCase().includes('region')) {
                        console.warn('Region backend auth failed:', errorDetail)
                        break
                    }

                    // Unauthorized - clear auth and redirect to login
                    localStorage.removeItem('cloudland_token')
                    localStorage.removeItem('cloudland_user')
                    // Only redirect if not already on login page
                    if (!window.location.pathname.includes('/login')) {
                        window.location.href = '/login'
                    }
                    break
                case 403: {
                    // Check if 403 is caused by expired/invalid credentials
                    const detail403 = (error.response?.data as any)?.detail || ''
                    if (typeof detail403 === 'string' && detail403.toLowerCase().includes('credentials')) {
                        // Token expired or invalid - clear auth and redirect to login
                        localStorage.removeItem('cloudland_token')
                        localStorage.removeItem('cloudland_user')
                        if (!window.location.pathname.includes('/login')) {
                            window.location.href = '/login'
                        }
                    } else {
                        console.error('Access forbidden:', detail403)
                    }
                    break
                }
                case 404:
                    console.error('Resource not found')
                    break
                case 500:
                    console.error('Server error')
                    break
            }
        } else if (error.request) {
            console.error('Network error - no response received')
        }

        return Promise.reject(error)
    }
)

// Helper function to set auth token
export const setAuthToken = (token: string) => {
    localStorage.setItem('cloudland_token', token)
}

// Helper function to clear auth token
export const clearAuthToken = () => {
    localStorage.removeItem('cloudland_token')
}

export default client
