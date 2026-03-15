<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { floatingIpsApi, type FloatingIP } from '../../api/networks'
import { ArrowLeft, Globe, Trash2, Server, Network } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const route = useRoute()
const router = useRouter()
const fipId = route.params.id as string

const fip = ref<FloatingIP | null>(null)
const loading = ref(true)
const error = ref('')
const { t } = useI18n()

// --- Delete Confirmation Modal Logic ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')

const handleDeleteClick = () => {
    deleteModalVisible.value = true
}

const closeDeleteModal = () => {
    deleteModalVisible.value = false
    deleteError.value = ''
}

const confirmDelete = async () => {
    if (!fip.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await floatingIpsApi.delete(fipId)
        router.push({ name: 'floating-ips' })
    } catch (err: any) {
        console.error('Failed to release Floating IP:', err)
        deleteError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

const fetchFip = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await floatingIpsApi.get(fipId)
        fip.value = response
    } catch (err) {
        console.error('Failed to fetch Floating IP:', err)
        error.value = 'Failed to load Floating IP details.'
    } finally {
        loading.value = false
    }
}

const goBack = () => {
    router.back()
}

const getStatusClass = (status: string) => {
    return status === 'in-use' ? 'status-success' : 'status-warning'
}

onMounted(fetchFip)
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
            <button class="btn btn-primary" @click="fetchFip">Retry</button>
        </div>

        <div v-else-if="fip" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="resource-icon">
                    <Globe :size="24" />
                </div>
                <div class="title-info">
                    <h1>{{ fip.name || fip.public_ip || fip.ip_address }}</h1>
                    <div class="subtitle">
                        <span class="id-text">{{ fip.id }}</span>
                        <span v-if="fip.public_ip && fip.name" class="ip-text">{{ fip.public_ip }}</span>
                        <!-- Status removed as requested in list view logic, or keep if useful in detail -->
                    </div>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>General Information</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">Name</span>
                            <span class="value">{{ fip.name || '-' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">Public IP</span>
                            <span class="value mono">{{ fip.public_ip || '-' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">Internal IP</span>
                            <span class="value mono">{{ fip.ip_address }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">Created At</span>
                            <span class="value">{{ fip.created_at || '-' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">Updated At</span>
                            <span class="value">{{ fip.updated_at || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- Association -->
                <div class="card info-card">
                    <h3>Association</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label"><Server :size="14" /> Instance</span>
                            <span class="value" v-if="fip.target_interface?.from_instance">
                                <router-link :to="{name: 'instance-detail', params: {id: fip.target_interface.from_instance.id}}" class="text-link">
                                    {{ fip.target_interface.from_instance.hostname }}
                                </router-link>
                            </span>
                            <span class="value text-secondary" v-else>Not Associated</span>
                        </div>
                         <div class="kv-item">
                            <span class="label"><Network :size="14" /> Interface ID</span>
                            <span class="value mono">{{ fip.target_interface?.id || '-' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">Interface IP</span>
                            <span class="value mono">{{ fip.target_interface?.ip_address || '-' }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Action Bar -->
             <div class="action-bar card">
                <div class="action-group">
                </div>
                <div class="action-group">
                     <button class="btn btn-danger" @click="handleDeleteClick">
                        <Trash2 :size="16" /> {{ $t('actions.delete') }}
                    </button>
                </div>
            </div>
        </div>

        <DeleteModal
            :show="deleteModalVisible"
            :resource-name="fip?.public_ip || fip?.ip_address"
            :resource-id="fip?.id"
            :loading="deletingResource"
            :error="deleteError"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />
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
    gap: 6px;
}

.kv-item .value {
    color: var(--text-primary);
    font-weight: 500;
    text-align: right;
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

/* Action Bar */
.action-bar {
    padding: var(--spacing-4);
    display: flex;
    justify-content: space-between;
    align-items: center;
}

.action-group {
    display: flex;
    gap: var(--spacing-3);
}

.btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
}
</style>
