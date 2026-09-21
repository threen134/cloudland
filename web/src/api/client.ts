import { STORAGE_KEYS, clearAuthStorage } from '../utils/storage'
import axios from 'axios'
import type { AxiosError, InternalAxiosRequestConfig, AxiosResponse } from 'axios'

let _tokenSwitchCount = 0
let _tokenSwitchResolve: (() => void) | null = null
let _tokenSwitchPromise: Promise<void> | null = null

// Track when the last token switch completed so the 401 handler can
// distinguish "revoked old token" from "genuinely expired session".
let _lastTokenSwitchAt = 0

/** Returns true if a token switch is active or finished within the last 5 s. */
export const isTokenSwitchRecent = (): boolean => {
    if (_tokenSwitchCount > 0) return true
    return Date.now() - _lastTokenSwitchAt < 5000
}

export const beginTokenSwitch = (): (() => void) => {
    _tokenSwitchCount++
    if (!_tokenSwitchPromise) {
        _tokenSwitchPromise = new Promise<void>((resolve) => {
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
            _lastTokenSwitchAt = Date.now()
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
            await Promise.race([_tokenSwitchPromise, new Promise<void>((resolve) => setTimeout(resolve, 15000))])
        }

        // Add JWT token if available
        const token = getToken()
        if (token && config.headers) {
            config.headers.Authorization = `Bearer ${token}`
        }

        // Add tenant/organization header
        const orgId = localStorage.getItem(STORAGE_KEYS.orgId)
        if (orgId && config.headers) {
            config.headers['X-Organization-ID'] = orgId
        }

        // Add region UUID as query parameter (skip for /regions endpoint itself)
        const regionUuid = localStorage.getItem(STORAGE_KEYS.regionUuid)
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

// cpgateway 的错误体是 FastAPI 风格的 { detail: ... }：detail 多数是字符串，
// 配额超限时是一个对象（见下面 429 分支）
interface GatewayErrorBody {
    detail?: string | QuotaExceededDetail
}

interface QuotaExceededDetail {
    error?: string
    resource?: string
    region?: string
    requested?: number
    available?: number
    limit?: number
}

// 请求配置上打的重试标记：token 切换导致的 401 只重试一次
interface RetriableConfig extends InternalAxiosRequestConfig {
    __retried?: boolean
}

const getErrorDetail = (error: AxiosError): string => {
    const detail = (error.response?.data as GatewayErrorBody | undefined)?.detail
    return typeof detail === 'string' ? detail : 'unknown'
}

// 后端响应头 X-Trace-ID：反馈问题时提供给运维，在 Grafana 中按 trace id 查询完整链路
const traceHint = (error: AxiosError): string => {
    const traceId = error.response?.headers?.['x-trace-id']
    return traceId ? `(trace ${traceId})` : ''
}

// 最近一次 API 错误的 trace id，供错误提示展示（3 秒内有效，读取后清除）
let lastErrorTrace: { id: string; at: number } | null = null

export const consumeRecentTraceId = (): string | undefined => {
    const recent = lastErrorTrace
    lastErrorTrace = null
    return recent && Date.now() - recent.at < 3000 ? recent.id : undefined
}

const forceLogout = (reason: string, url?: string) => {
    console.error(`[client] auth failure on ${url ?? '?'} — ${reason}. Clearing session and redirecting to login.`)
    // 清干净再跳：这里原先只删了 token 和 user，组织 / 区域会留到下一个登录的账号
    // （正常从菜单登出走的是 Layout.handleLogout，由各个 store 自己清）
    clearAuthStorage()
    if (!window.location.pathname.includes('/login')) {
        window.location.href = '/login'
    }
}

// Response interceptor - handle errors globally
client.interceptors.response.use(
    (response: AxiosResponse) => {
        return response
    },
    (error: AxiosError) => {
        // Handle common error cases
        if (error.response) {
            const status = error.response.status
            const traceId = error.response.headers?.['x-trace-id']
            if (traceId) {
                lastErrorTrace = { id: String(traceId), at: Date.now() }
            }

            switch (status) {
                case 429: {
                    // Quota exceeded — FastAPI wraps detail in { detail: { ... } }
                    const body = error.response?.data as GatewayErrorBody | undefined
                    const detail = typeof body?.detail === 'object' ? body.detail : undefined
                    if (detail?.error === 'quota_exceeded') {
                        const { resource, region, requested, available, limit } = detail
                        console.error(
                            `Quota exceeded: ${resource} in ${region} — requested ${requested}, available ${available} (limit: ${limit})`
                        )
                        // Let the caller handle the structured error
                    }
                    break
                }
                case 401: {
                    const errorDetail = getErrorDetail(error)
                    // Check if 401 is caused by region backend failure (not a real auth issue)
                    if (typeof errorDetail === 'string' && errorDetail.toLowerCase().includes('region')) {
                        console.warn('Region backend auth failed:', errorDetail)
                        break
                    }

                    // If a token switch is in progress or just finished, a 401
                    // likely means the request used a stale/revoked token.
                    // Retry once with the current (fresh) token instead of
                    // nuking the session.
                    // The same applies when another window switched the token and this window has
                    // received the new one after sending the request.
                    const freshToken = getToken()
                    const usedToken = String(error.config?.headers?.Authorization || '').replace(/^Bearer /, '')
                    if (isTokenSwitchRecent() || (freshToken && usedToken && freshToken !== usedToken)) {
                        const originalConfig = error.config as RetriableConfig | undefined
                        if (freshToken && originalConfig && !originalConfig.__retried) {
                            originalConfig.__retried = true
                            originalConfig.headers.Authorization = `Bearer ${freshToken}`
                            console.warn('401 during token switch — retrying with fresh token')
                            return client.request(originalConfig)
                        }
                        // If retry already happened or no token, fall through
                    }

                    forceLogout(`401 detail: ${errorDetail}`, error.config?.url)
                    break
                }
                case 403: {
                    const detail403 = getErrorDetail(error)
                    // Token expired or invalid — credentials mismatch signals session is no longer valid
                    if (typeof detail403 === 'string' && detail403.toLowerCase().includes('credentials')) {
                        forceLogout(`403 detail: ${detail403}`, error.config?.url)
                    } else {
                        console.error('Access forbidden:', detail403, traceHint(error))
                    }
                    break
                }
                case 404:
                    console.error('Resource not found', traceHint(error))
                    break
                case 500:
                    console.error('Server error', traceHint(error))
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
    return sessionStorage.getItem(STORAGE_KEYS.token) || localStorage.getItem(STORAGE_KEYS.token)
}

const getStorage = (): Storage => {
    return localStorage.getItem(STORAGE_KEYS.remember) === '1' ? localStorage : sessionStorage
}

// cpgateway 签发的 access token 声明（`src/services/auth.go`）。浏览器只用到其中几项，
// 其余保留索引签名以免漏一个就编译不过
export interface TokenClaims {
    sub?: string
    user_id?: number
    org_id?: string
    org_name?: string
    region_uuid?: string
    jti?: string
    exp?: number
    iat?: number
    [key: string]: unknown
}

// Reads the claims of a JWT without verifying it: the server verifies tokens, the browser only needs
// to know whose token it is (sub) and which org / region it is scoped to (org_id, region)
export const decodeTokenClaims = (token: string | null): TokenClaims | null => {
    const payload = token?.split('.')[1]
    if (!payload) return null
    try {
        const base64 = payload.replace(/-/g, '+').replace(/_/g, '/')
        const json = decodeURIComponent(
            Array.from(atob(base64), (c) => '%' + c.charCodeAt(0).toString(16).padStart(2, '0')).join('')
        )
        return JSON.parse(json)
    } catch {
        return null
    }
}

const storeToken = (token: string) => {
    const storage = getStorage()
    sessionStorage.removeItem(STORAGE_KEYS.token)
    localStorage.removeItem(STORAGE_KEYS.token)
    storage.setItem(STORAGE_KEYS.token, token)
}

// cpgateway revokes the previous token whenever it issues a new one (login, org or region switch), while
// without "remember me" every window keeps its own copy in sessionStorage. New tokens are therefore passed to
// the other windows of the same user (console windows, other tabs), which would otherwise be logged out on
// their next request.
const authChannel = typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('cloudland-auth') : null

authChannel?.addEventListener('message', (event: MessageEvent) => {
    const { type, token } = event.data || {}
    if (type !== 'token' || typeof token !== 'string') return
    const current = getToken()
    // A logged-out window stays logged out, and a window of another user keeps its own session
    if (!current || current === token) return
    const sub = decodeTokenClaims(current)?.sub
    if (!sub || sub !== decodeTokenClaims(token)?.sub) return
    storeToken(token)
})

// Helper function to set auth token
export const setAuthToken = (token: string, remember?: boolean) => {
    if (remember !== undefined) {
        if (remember) {
            localStorage.setItem(STORAGE_KEYS.remember, '1')
        } else {
            localStorage.removeItem(STORAGE_KEYS.remember)
        }
    }
    storeToken(token)
    authChannel?.postMessage({ type: 'token', token })
}

// Helper function to clear auth token
export const clearAuthToken = () => {
    sessionStorage.removeItem(STORAGE_KEYS.token)
    localStorage.removeItem(STORAGE_KEYS.token)
    localStorage.removeItem(STORAGE_KEYS.remember)
}

export default client
