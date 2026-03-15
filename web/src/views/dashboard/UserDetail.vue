<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usersApi, type User } from '../../api/users'
import { ArrowLeft, User as UserIcon, Trash2, Mail, Shield, AlertTriangle } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const userId = route.params.id as string

const user = ref<User | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)

const fetchUser = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await usersApi.getUser(userId)
        user.value = (response.data as any).user // Assuming the backend returns { user: ... }
    } catch (err) {
        console.error('Failed to fetch user:', err)
        error.value = 'Failed to load user details.'
    } finally {
        loading.value = false
    }
}

const handleDelete = async () => {
    if (!confirm('Are you sure you want to delete this user? This action cannot be undone.')) return
    
    deleting.value = true
    try {
        await usersApi.deleteUser(userId)
        router.push({ name: 'users' })
    } catch (err) {
        console.error('Failed to delete user:', err)
        alert('Failed to delete user.')
        deleting.value = false
    }
}

const goBack = () => {
    router.back()
}

const getStatusClass = (status: string) => {
    return status === 'active' ? 'status-success' : 'status-warning'
}

onMounted(fetchUser)
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <div v-if="loading" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error" class="error-container card">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary" @click="fetchUser">Retry</button>
        </div>

        <div v-else-if="user" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="resource-icon">
                    <UserIcon :size="32" />
                </div>
                <div class="title-info">
                    <h1>{{ user.username || user.name || 'Unknown User' }}</h1>
                    <div class="subtitle">
                        <span class="id-text">{{ user.id }}</span>
                        <span :class="['status-badge', getStatusClass(user.status || 'active')]">
                            {{ user.status || 'active' }}
                        </span>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-danger" @click="handleDelete" :disabled="deleting">
                        <Trash2 :size="16" /> {{ deleting ? 'Deleting...' : 'Delete User' }}
                    </button>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>General Information</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">Username</span>
                            <span class="value">{{ user.username || user.name }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label"><Mail :size="14" /> Email</span>
                            <span class="value">{{ user.email || '-' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label"><Shield :size="14" /> Role</span>
                            <span class="value">{{ user.role || 'Member' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">Created At</span>
                            <span class="value">{{ user.created_at || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- Organization Info -->
                 <div class="card info-card">
                    <h3>Organization</h3>
                     <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">Organization ID</span>
                            <span class="value mono">{{ user.org?.id || '-' }}</span>
                        </div>
                         <div class="kv-item">
                            <span class="label">Organization Name</span>
                            <span class="value">{{ user.org?.name || '-' }}</span>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.detail-page {
    max-width: 1200px;
    margin: 0 auto;
}

.detail-header {
    margin-bottom: var(--spacing-4);
}

.loading-container, .error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px;
}

/* Title Bar */
.title-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
    padding: var(--spacing-6);
    margin-bottom: var(--spacing-6);
}

.resource-icon {
    width: 64px;
    height: 64px;
    background: var(--bg-tertiary);
    color: var(--primary-color);
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
}

.title-info {
    flex: 1;
}

.title-info h1 {
    font-size: var(--font-size-xl);
    font-weight: 600;
    margin: 0 0 4px 0;
    color: var(--text-primary);
}

.subtitle {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    font-size: var(--font-size-sm);
}

.id-text {
    font-family: var(--font-family-mono);
    color: var(--text-secondary);
}

.status-badge {
    display: inline-flex;
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
}

.status-success { background: var(--success-50); color: var(--success-700); }
.status-warning { background: var(--warning-50); color: var(--warning-700); }

/* Info Grid */
.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-6);
}

.info-card {
    padding: var(--spacing-5);
}

.info-card h3 {
    font-size: var(--font-size-md);
    font-weight: 600;
    margin: 0 0 var(--spacing-4) 0;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-3);
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.kv-item {
    display: flex;
    justify-content: space-between;
    font-size: var(--font-size-sm);
}

.kv-item .label {
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    gap: 8px;
}

.kv-item .value {
    color: var(--text-primary);
    font-weight: 500;
}

.value.mono {
    font-family: var(--font-family-mono);
}

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 20px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
}
.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
