<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { subnetsApi, type Subnet } from '../../api/networks'
import { ArrowLeft, Network, Trash2, Globe, Lock, Activity, Copy, Check } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const route = useRoute()
const router = useRouter()
const subnetId = route.params.id as string

const subnet = ref<Subnet | null>(null)
const loading = ref(true)
const error = ref('')
const copiedField = ref<string | null>(null)
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
    if (!subnet.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await subnetsApi.delete(subnetId)
        router.push({ name: 'subnets' })
    } catch (err: any) {
        console.error('Failed to delete subnet:', err)
        deleteError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

const fetchSubnet = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await subnetsApi.get(subnetId)
        subnet.value = response
    } catch (err) {
        console.error('Failed to fetch subnet:', err)
        error.value = 'Failed to load subnet details.'
    } finally {
        loading.value = false
    }
}

const goBack = () => {
    router.back()
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

const getTypeBadgeClass = (type: string) => {
    const map: Record<string, string> = {
        'public': 'badge-green',
        'internal': 'badge-blue',
        'site': 'badge-purple'
    }
    return map[type] || 'badge-gray'
}

onMounted(fetchSubnet)
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
            <button class="btn btn-primary" @click="fetchSubnet">Retry</button>
        </div>

        <div v-else-if="subnet" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="title-info">
                    <div class="title-icon">
                        <Network :size="28" />
                    </div>
                    <div>
                        <h2 class="resource-title">{{ subnet.name }}</h2>
                        <div class="resource-id-row">
                            <span class="resource-id">{{ subnet.id }}</span>
                            <button class="copy-btn" @click="copyToClipboard(subnet.id, 'id')" :title="$t('messages.copied')">
                                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.name') }}</span>
                            <span class="value">{{ subnet.name }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.createdAt') }}</span>
                            <span class="value">{{ subnet.created_at || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- Network Details -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.networkDetails') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item" v-if="subnet.vpc">
                            <span class="label">{{ $t('dashboard.table.vpc') }}</span>
                            <span class="value">
                                <router-link :to="{name: 'vpc-detail', params: {id: subnet.vpc.id}}" class="text-link">
                                    {{ subnet.vpc.name }}
                                </router-link>
                            </span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.subnetType') }}</span>
                            <span class="value">
                                <span :class="['type-badge', getTypeBadgeClass(subnet.type || 'internal')]" style="padding: 2px 10px; font-size: 11px;">
                                    {{ subnet.type || 'internal' }}
                                </span>
                            </span>
                        </div>
                        <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.cidr') }}</span>
                             <span class="value mono">{{ subnet.network || subnet.network_cidr }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.networkRange') }}</span>
                            <span class="value mono">{{ subnet.start }} - {{ subnet.end }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.gateway') }}</span>
                            <span class="value mono">{{ subnet.gateway || '-' }}</span>
                        </div>
                        <div class="kv-item" v-if="subnet.vlan">
                            <span class="label">{{ (subnet.vlan > 4094) ? 'VXLAN' : 'VLAN' }}</span>
                            <span class="value mono">{{ subnet.vlan }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.dhcp') }}</span>
                            <span class="value">{{ subnet.dhcp ? 'Enabled' : 'Disabled' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">DNS</span>
                            <span class="value mono">{{ subnet.dns || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- Usage Stats -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.ipUsageStats') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.totalIps') }}</span>
                            <span class="value">{{ subnet.total_count }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.allocated') }}</span>
                            <span class="value text-blue">{{ subnet.allocated_count }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.available') }}</span>
                            <span class="value text-green">{{ subnet.available_count }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.idleReserved') }}</span>
                            <span class="value">{{ subnet.idle_count }} / {{ subnet.reserved_count }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Action Bar -->
             <div class="action-bar card">
                <div class="action-group">
                    <!-- Placeholder for Edit logic -->
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
            :resource-name="subnet?.name"
            :resource-id="subnet?.id"
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
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-5);
}

.title-info {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
}

.title-icon {
    width: 52px;
    height: 52px;
    border-radius: var(--radius-lg);
    background: linear-gradient(135deg, var(--primary-50), var(--primary-100));
    color: var(--primary-color);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
}

.resource-title {
    margin: 0 0 4px 0;
    font-size: var(--font-size-xl);
    font-weight: var(--font-weight-semibold);
    color: var(--primary-color);
}

.resource-id-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.resource-id {
    font-size: var(--font-size-xs);
    color: var(--text-light);
    font-family: var(--font-family-mono);
}

.copy-btn {
    background: none;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    padding: 2px 5px;
    cursor: pointer;
    color: var(--text-light);
    display: inline-flex;
    align-items: center;
    transition: all 0.15s;
}

.copy-btn:hover {
    color: var(--primary-color);
    border-color: var(--primary-200);
    background: var(--primary-50);
}

.copied-icon {
    color: var(--success-color);
}

.title-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

.type-badge {
    display: inline-flex;
    padding: 6px 14px;
    border-radius: 12px;
    font-size: var(--font-size-sm);
    font-weight: 600;
    text-transform: uppercase;
}

.badge-green { background: var(--success-50); color: var(--success-700); }
.badge-blue { background: var(--info-50); color: var(--info-700); }
.badge-purple { background: var(--primary-50); color: var(--primary-700); }
.badge-gray { background: var(--gray-100); color: var(--gray-700); }

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

.text-blue {
    color: var(--primary-color);
}

.text-green {
    color: var(--success-color);
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

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 16px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    transition: background var(--transition-base);
}

.btn-danger:hover {
    background: var(--error-dark);
}
</style>
