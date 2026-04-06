import axios from 'axios'
import type { AxiosError, InternalAxiosRequestConfig, AxiosResponse } from 'axios'

let _tokenSwitchCount = 0
let _tokenSwitchResolve: (() => void) | null = null
let _tokenSwitchPromise: Promise<void> | null = null

export const beginTokenSwitch = (): (() => void) => {
    _tokenSwitchCount++
    if (!_tokenSwitchPromise) {
        _tokenSwitchPromise = new Promise<void>(resolve => {
            _tokenSwitchResolve = resolve
        })
    }
    
    let resolved = false
    return () => {
        if (resolved) return
        resolved = true
        _tokenSwitchCount--
        if (_tokenSwitchCount <= 0) {
            _tokenSwitchResolve?.()
            _tokenSwitchPromise = null
            _tokenSwitchResolve = null
            _tokenSwitchCount = 0
        }
    }
}

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
    async (config: InternalAxiosRequestConfig) => {
        // If a token switch is in progress, wait for it to complete before
        // sending any request (except the switch call itself).
        if (_tokenSwitchPromise && !config.url?.startsWith('/auth/')) {
            // Safety timeout: never block a request for more than 15 seconds
            await Promise.race([
                _tokenSwitchPromise,
                new Promise<void>(resolve => setTimeout(resolve, 15000))
            ])
        }

        // Add JWT token if available
        const token = getToken()
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
                    // Quota exceeded — FastAPI wraps detail in { detail: { ... } }
                    const detail = (error.response?.data as any)?.detail
                    if (detail?.error === 'quota_exceeded') {
                        const { resource, region, requested, available, limit } = detail
                        console.error(
                            `Quota exceeded: ${resource} in ${region} — requested ${requested}, available ${available} (limit: ${limit})`
                        )
                        // Let the caller handle the structured error
                    }
                    break
                }
                case 401:


                    // Check if 401 is caused by region backend failure (not a real auth issue)
                    const errorDetail = (error.response?.data as any)?.detail || ''
                    if (typeof errorDetail === 'string' && errorDetail.toLowerCase().includes('region')) {
                        console.warn('Region backend auth failed:', errorDetail)
                        break
                    }

                    // Unauthorized - clear auth and redirect to login
                    clearAuthToken()
                    localStorage.removeItem('cloudland_user')
                    sessionStorage.removeItem('cloudland_user')
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
                        clearAuthToken()
                        localStorage.removeItem('cloudland_user')
                        sessionStorage.removeItem('cloudland_user')
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

// Storage helpers — rememberMe controls persistence across browser sessions
export const getToken = (): string | null => {
    return sessionStorage.getItem('cloudland_token') || localStorage.getItem('cloudland_token')
}

const getStorage = (): Storage => {
    return localStorage.getItem('cloudland_remember') === '1' ? localStorage : sessionStorage
}

// Helper function to set auth token
export const setAuthToken = (token: string, remember?: boolean) => {
    if (remember !== undefined) {
        if (remember) {
            localStorage.setItem('cloudland_remember', '1')
        } else {
            localStorage.removeItem('cloudland_remember')
        }
    }
    const storage = getStorage()
    sessionStorage.removeItem('cloudland_token')
    localStorage.removeItem('cloudland_token')
    storage.setItem('cloudland_token', token)
}

// Helper function to clear auth token
export const clearAuthToken = () => {
    sessionStorage.removeItem('cloudland_token')
    localStorage.removeItem('cloudland_token')
    localStorage.removeItem('cloudland_remember')
}

export default client
