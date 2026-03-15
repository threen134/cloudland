import { defineStore } from 'pinia'
import { ref } from 'vue'
import { authApi } from '../api/auth'
import { setAuthToken, clearAuthToken } from '../api/client'

interface User {
    id?: number
    uuid?: string
    username?: string
    email?: string
    name?: string
    is_active?: boolean
    is_superuser?: boolean
    language?: string
    role?: 'admin' | 'user'
}

export const useAuthStore = defineStore('auth', () => {
    const user = ref<User | null>(null)
    const isLoading = ref(true)
    const failedAttempts = ref(0)

    // Initialize from local storage
    const init = () => {
        const storedUser = localStorage.getItem('ibm_cloud_china_user')
        if (storedUser) {
            user.value = JSON.parse(storedUser)
            // Restore token if needed, or check validity
            const token = localStorage.getItem('ibm_cloud_china_token')
            if (token) {
                setAuthToken(token)
                // Fetch fresh user info to ensure we have the latest (e.g. username)
                authApi.getUserInfo().then(res => {
                    // API returns { message, user: {...} } — extract the nested user object
                    const userData = res.data?.user || res.data
                    user.value = userData
                    localStorage.setItem('ibm_cloud_china_user', JSON.stringify(user.value))
                }).catch(err => {
                    console.error('Failed to refresh user info:', err)
                })
            }
        }

        const storedAttempts = localStorage.getItem('ibm_cloud_china_login_attempts')
        if (storedAttempts) {
            failedAttempts.value = parseInt(storedAttempts, 10)
        }
        isLoading.value = false
    }

    const login = async (username: string, password?: string) => {
        isLoading.value = true

        try {
            const response = await authApi.login({ username, password })
            const token = response.data.access_token

            setAuthToken(token)

            // Fetch user info from /auth/me
            const userInfoRes = await authApi.getUserInfo()
            // API returns { message, user: {...} } — extract the nested user object
            const userData = userInfoRes.data?.user || userInfoRes.data
            user.value = userData
            localStorage.setItem('ibm_cloud_china_user', JSON.stringify(user.value))
            failedAttempts.value = 0
            localStorage.removeItem('ibm_cloud_china_login_attempts')

        } catch (error) {
            console.error('Login failed:', error)
            failedAttempts.value++
            localStorage.setItem('ibm_cloud_china_login_attempts', failedAttempts.value.toString())
            throw error
        } finally {
            isLoading.value = false
        }
    }

    const logout = () => {
        user.value = null
        localStorage.removeItem('ibm_cloud_china_user')
        clearAuthToken()
    }

    // Run init immediately
    init()

    return { user, isLoading, failedAttempts, login, logout }
})
