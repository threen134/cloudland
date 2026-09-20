import { STORAGE_KEYS } from '../utils/storage'
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { authApi } from '../api/auth'
import { setAuthToken, clearAuthToken, getToken } from '../api/client'

interface User {
    uuid?: string
    username?: string
    email?: string
    name?: string
    is_active?: boolean
    is_superuser?: boolean
    language?: string
    role?: 'admin' | 'user'
    current_org_uuid?: string
    current_region?: string
}

export const useAuthStore = defineStore('auth', () => {
    const user = ref<User | null>(null)
    const isLoading = ref(true)
    const failedAttempts = ref(0)

    const refreshUser = async () => {
        try {
            // GET /auth/me 返回的是扁平的用户对象（cpgateway 的 GetMe），
            // 不是 { message, user } 包装——后者是注册接口的形状
            const userData = await authApi.getUserInfo()
            user.value = userData
            const userStorage = localStorage.getItem(STORAGE_KEYS.remember) === '1' ? localStorage : sessionStorage
            userStorage.setItem(STORAGE_KEYS.user, JSON.stringify(user.value))
            return userData
        } catch (err) {
            console.error('Failed to refresh user info:', err)
            throw err
        }
    }

    // Initialize from local/session storage
    const init = () => {
        const storedUser = sessionStorage.getItem(STORAGE_KEYS.user) || localStorage.getItem(STORAGE_KEYS.user)
        if (storedUser) {
            try {
                user.value = JSON.parse(storedUser)
            } catch {
                // 存储里的用户信息损坏：清掉当成未登录处理，
                // 否则这里抛错会中断 store 初始化（路由守卫里调用），整个页面白屏
                console.warn('[auth] stored user info is not valid JSON, clearing it')
                localStorage.removeItem(STORAGE_KEYS.user)
                sessionStorage.removeItem(STORAGE_KEYS.user)
            }
            // Restore token if needed, or check validity
            const token = getToken()
            if (user.value && token) {
                setAuthToken(token)
                // Fetch fresh user info to ensure we have the latest (e.g. username)
                refreshUser().catch(() => {})
            }
        }

        const storedAttempts = localStorage.getItem(STORAGE_KEYS.loginAttempts)
        if (storedAttempts) {
            failedAttempts.value = parseInt(storedAttempts, 10)
        }
        isLoading.value = false
    }

    const login = async (username: string, password?: string, rememberMe: boolean = false) => {
        isLoading.value = true

        try {
            const response = await authApi.login({ username, password })
            const token = response.access_token

            setAuthToken(token, rememberMe)

            // Fetch user info from /auth/me
            const userData = await authApi.getUserInfo()
            user.value = userData
            const userStorage = rememberMe ? localStorage : sessionStorage
            userStorage.setItem(STORAGE_KEYS.user, JSON.stringify(user.value))
            failedAttempts.value = 0
            localStorage.removeItem(STORAGE_KEYS.loginAttempts)

        } catch (error) {
            console.error('Login failed:', error)
            failedAttempts.value++
            localStorage.setItem(STORAGE_KEYS.loginAttempts, failedAttempts.value.toString())
            throw error
        } finally {
            isLoading.value = false
        }
    }

    const logout = () => {
        user.value = null
        localStorage.removeItem(STORAGE_KEYS.user)
        sessionStorage.removeItem(STORAGE_KEYS.user)
        clearAuthToken()
    }

    // Run init immediately
    init()

    return { user, isLoading, failedAttempts, login, logout, refreshUser }
})
