<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { loadBalancersApi, type LoadBalancer } from '../../api/networks'
import { ArrowLeft, GitFork, Trash2, Activity, Globe } from 'lucide-vue-next'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const lbId = route.params.id as string

const lb = ref<LoadBalancer | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)

const fetchLB = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await loadBalancersApi.get(lbId)
        lb.value = response
    } catch (err) {
        console.error('Failed to fetch load balancer:', err)
        error.value = t('dashboard.loadBalancerDetail.loadError')
    } finally {
        loading.value = false
    }
}

const handleDelete = async () => {
    if (!confirm(t('dashboard.loadBalancerDetail.deleteConfirm'))) return
    
    deleting.value = true
    try {
        await loadBalancersApi.delete(lbId)
        router.push({ name: 'load-balancers' })
    } catch (err) {
        console.error('Failed to delete load balancer:', err)
        alert(t('dashboard.loadBalancerDetail.deleteFailed'))
        deleting.value = false
    }
}

const goBack = () => {
    router.back()
}

const getStatusClass = (status: string) => {
    const map: Record<string, string> = {
        'active': 'status-success',
        'running': 'status-success',
        'error': 'status-error',
        'inactive': 'status-default'
    }
    return map[status.toLowerCase()] || 'status-default'
}

onMounted(fetchLB)
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
            <button class="btn btn-primary" @click="fetchLB">{{ $t('dashboard.loadBalancerDetail.retry') }}</button>
        </div>

        <div v-else-if="lb" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="resource-icon">
                    <GitFork :size="24" />
                </div>
                <div class="title-info">
                    <h1>{{ lb.name }}</h1>
                    <div class="subtitle">
                        <span class="id-text">{{ lb.id }}</span>
                        <span :class="['status-badge', getStatusClass(lb.status || 'inactive')]">
                            {{ lb.status || 'inactive' }}
                        </span>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-danger" @click="handleDelete" :disabled="deleting">
                        <Trash2 :size="16" /> {{ deleting ? $t('dashboard.loadBalancerDetail.deleting') : $t('dashboard.loadBalancerDetail.deleteLb') }}
                    </button>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.loadBalancerDetail.generalInfo') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.loadBalancerDetail.name') }}</span>
                            <span class="value">{{ lb.name }}</span>
                        </div>
                        <div class="kv-item">
                             <span class="label">{{ $t('dashboard.loadBalancerDetail.vpc') }}</span>
                             <span class="value" v-if="lb.vpc">
                                <router-link :to="{name: 'vpc-detail', params: {id: lb.vpc.id}}" class="text-link">
                                    {{ lb.vpc.name }}
                                </router-link>
                            </span>
                             <span class="value" v-else>-</span>
                        </div>
                         <div class="kv-item">
                            <span class="label">{{ $t('dashboard.loadBalancerDetail.createdAt') }}</span>
                            <span class="value">{{ lb.created_at || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- Network Info -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.loadBalancerDetail.network') }}</h3>
                     <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.loadBalancerDetail.vipAddress') }}</span>
                            <span class="value mono">{{ lb.floating_ips?.[0]?.ip_address || '-' }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Listeners -->
            <div class="card listeners-card">
                <div class="card-header">
                    <h3>{{ $t('dashboard.loadBalancerDetail.listeners') }}</h3>
                    <!-- Add Listener logic can be here -->
                </div>
                <div class="table-responsive">
                    <table class="data-table">
                        <thead>
                            <tr>
                                <th>{{ $t('dashboard.table.name') }}</th>
                                <th>{{ $t('dashboard.loadBalancerDetail.protocol') || 'Protocol/Port' }}</th>
                                <th>{{ $t('dashboard.table.status') }}</th>
                                <th>{{ $t('dashboard.loadBalancerDetail.backends') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-if="!lb.listeners?.length">
                                <td colspan="4" class="text-center text-secondary">{{ $t('messages.noListeners') }}</td>
                            </tr>
                             <tr v-else v-for="listener in lb.listeners" :key="listener.id">
                                <td>{{ listener.name }}</td>
                                <td class="mono">{{ listener.mode.toUpperCase() }}:{{ listener.port }}</td>
                                 <td>{{ listener.status || 'active' }}</td>
                                <td>{{ listener.backends?.length || 0 }} {{ $t('dashboard.loadBalancerDetail.backends') }}</td>
                            </tr>
                        </tbody>
                    </table>
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
    width: 48px;
    height: 48px;
    background: var(--bg-tertiary);
    color: var(--primary-color);
    border-radius: var(--radius-md);
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
.status-error { background: var(--error-50); color: var(--error-700); }
.status-default { background: var(--gray-100); color: var(--gray-700); }

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
}

.kv-item .value {
    color: var(--text-primary);
    font-weight: 500;
}

.value.mono {
     font-family: var(--font-family-mono);
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}

.text-link:hover {
    text-decoration: underline;
}

/* Listeners Card */
.listeners-card {
    padding: 0;
    overflow: hidden;
}

.card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--spacing-4) var(--spacing-5);
    border-bottom: 1px solid var(--border-light);
}

.card-header h3 {
    margin: 0;
    font-size: var(--font-size-md);
}

.table-responsive {
    overflow-x: auto;
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
