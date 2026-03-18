<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { vpcsApi, type VPC, type Subnet } from '../../api/networks'
import { useRegionStore } from '../../stores/region'
import { ArrowLeft, Layers, Network, Trash2, Plus, Copy, Check, Edit } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const route = useRoute()
const router = useRouter()
const region = useRegionStore()

const vpc = ref<VPC | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
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
    if (!vpc.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await vpcsApi.delete(vpc.value.id)
        router.push({ name: 'vpcs' })
    } catch (err: any) {
        console.error('Failed to delete VPC:', err)
        if (err.response?.data?.error_code === 131307 || err.response?.data?.error_code_str === 'RouterHasFloatingIPs') {
            deleteError.value = t('messages.vpcHasFloatingIPs')
        } else {
            deleteError.value = err.response?.data?.error_message || err.message || t('messages.error')
        }
    } finally {
        deletingResource.value = false
    }
}

const fetchVPC = async () => {
    const id = route.params.id as string
    if (!id) return

    loading.value = true
    error.value = null
    try {
        const response = await vpcsApi.get(id)
        vpc.value = response
    } catch (err: any) {
        console.error('Failed to fetch VPC:', err)
        error.value = err.message || 'Failed to load VPC details'
    } finally {
        loading.value = false
    }
}

watch(() => region.currentRegionUuid, (newUuid) => {
    if (newUuid) {
        fetchVPC()
    }
})

const goBack = () => {
    router.push({ name: 'vpcs' })
}

const getStatusClass = (status?: string) => {
    const statusMap: Record<string, string> = {
        'active': 'status-active',
        'available': 'status-active',
        'creating': 'status-pending',
        'deleting': 'status-pending',
        'error': 'status-error'
    }
    return statusMap[status || ''] || 'status-active'
}

const formatDate = (dateStr?: string) => {
    if (!dateStr) return '-'
    const d = new Date(dateStr)
    return d.toLocaleString()
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

onMounted(() => {
    if (region.currentRegionUuid) {
        fetchVPC()
    }
})
</script>

<template>
  <div class="vpc-detail">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>{{ $t('dashboard.vpcs') }}</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ $t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <Layers :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchVPC" style="margin-top: 12px;">
        {{ $t('actions.refresh') }}
      </button>
    </div>

    <!-- VPC Detail Content -->
    <div v-else-if="vpc" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <Layers :size="28" />
          </div>
          <div>
            <h2 class="resource-title">{{ vpc.name }}</h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ vpc.id }}</span>
              <button class="copy-btn" @click="copyToClipboard(vpc.id, 'id')" :title="$t('messages.copied')">
                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <span :class="['badge', 'badge-lg', getStatusClass(vpc.status)]">
            {{ vpc.status || 'Active' }}
          </span>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <!-- Basic Info Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ $t('dashboard.table.name') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.name') }}</span>
              <span class="info-value">{{ vpc.name }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.id') }}</span>
              <span class="info-value mono">{{ vpc.id }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.status') }}</span>
              <span class="info-value">
                <span :class="['badge', getStatusClass(vpc.status)]">{{ vpc.status || 'Active' }}</span>
              </span>
            </div>
          </div>
        </div>

        <!-- Metadata Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ $t('dashboard.table.metadata') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.owner') }}</span>
              <span class="info-value">{{ vpc.owner || '-' }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.created') }}</span>
              <span class="info-value">{{ formatDate(vpc.created_at) }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.updatedAt') }}</span>
              <span class="info-value">{{ formatDate(vpc.updated_at) }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Subnets Section -->
      <div class="subnets-section card">
        <div class="subnets-header">
          <h3 class="card-section-title" style="margin-bottom: 0; padding-bottom: 0; border-bottom: none;">
            {{ $t('dashboard.subnets') }}
            <span class="subnet-count">{{ vpc.subnets?.length || 0 }}</span>
          </h3>
        </div>

        <div v-if="vpc.subnets && vpc.subnets.length > 0" class="subnet-table-wrap">
          <table class="data-table subnet-table">
            <thead>
              <tr>
                <th>{{ $t('dashboard.table.name') }}</th>
                <th>{{ $t('dashboard.table.cidr') }}</th>
                <th>{{ $t('dashboard.table.gateway') }}</th>
                <th>{{ $t('dashboard.table.type') }}</th>
                <th>{{ $t('dashboard.table.network') }}</th>
                <th>{{ $t('dashboard.table.dhcp') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="subnet in vpc.subnets" :key="subnet.id">
                <td>
                  <div class="subnet-name">{{ subnet.name }}</div>
                  <div class="subnet-id">{{ subnet.id }}</div>
                </td>
                <td><span class="mono-value">{{ (subnet.network || subnet.network_cidr) || '-' }}</span></td>
                <td><span class="mono-value">{{ subnet.gateway || '-' }}</span></td>
                <td>
                  <span class="badge status-pending">{{ subnet.type || 'internal' }}</span>
                </td>
                <td>{{ subnet.vlan ?? '-' }}</td>
                <td>
                  <span :class="['badge', subnet.dhcp ? 'status-active' : 'status-pending']">
                    {{ subnet.dhcp ? 'ON' : 'OFF' }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty-subnets">
          <Network :size="36" style="opacity: 0.2; margin-bottom: 12px;" />
          <p class="text-secondary">{{ $t('messages.noData') }}</p>
        </div>
      </div>

      <!-- Action Bar -->
      <div class="action-bar card">
        <button class="btn btn-secondary btn-sm">
          <Edit :size="14" />
          {{ $t('actions.edit') }}
        </button>
        <button class="btn btn-secondary btn-sm">
          <Plus :size="14" />
          {{ $t('dashboard.buttons.createSubnet') }}
        </button>
        <button class="btn btn-danger btn-sm" @click="handleDeleteClick">
          <Trash2 :size="14" />
          {{ $t('actions.delete') }}
        </button>
      </div>

      <DeleteModal
        :show="deleteModalVisible"
        :resource-name="vpc?.name"
        :resource-id="vpc?.id"
        :loading="deletingResource"
        :error="deleteError"
        @close="closeDeleteModal"
        @confirm="confirmDelete"
      />
    </div>
  </div>
</template>

<style scoped>
.vpc-detail {
  max-width: 1100px;
}

.detail-header {
  margin-bottom: var(--spacing-4);
}

.back-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--spacing-2);
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  padding: var(--spacing-2) var(--spacing-3);
  border-radius: var(--radius-md);
  transition: all 0.2s;
}

.back-btn:hover {
  color: var(--primary-color);
  background: var(--primary-50);
}

/* Loading */
.loading-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 0;
}

.loading-text {
  margin-top: var(--spacing-3);
  color: var(--text-secondary);
  font-size: var(--font-size-sm);
}

/* Error */
.error-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  text-align: center;
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
  color: var(--text-primary);
}

.resource-id-row {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
}

.resource-id-text {
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

.badge-lg {
  font-size: var(--font-size-sm);
  padding: 6px 14px;
}

/* Info Grid */
.info-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--spacing-4);
  margin-bottom: var(--spacing-5);
}

.info-card {
  padding: var(--spacing-5);
}

.card-section-title {
  margin: 0 0 var(--spacing-4) 0;
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-semibold);
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding-bottom: var(--spacing-3);
  border-bottom: 1px solid var(--border-light);
}

.info-rows {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-3);
}

.info-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--spacing-1) 0;
}

.info-label {
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  flex-shrink: 0;
}

.info-value {
  font-size: var(--font-size-sm);
  color: var(--text-primary);
  font-weight: var(--font-weight-medium);
  text-align: right;
  word-break: break-all;
}

.info-value.mono {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
}

/* Subnets Section */
.subnets-section {
  margin-bottom: var(--spacing-5);
  padding: 0;
  overflow: hidden;
}

.subnets-header {
  padding: var(--spacing-4) var(--spacing-5);
  border-bottom: 1px solid var(--border-light);
}

.subnet-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  padding: 0 6px;
  border-radius: var(--radius-full);
  background: var(--primary-100);
  color: var(--primary-color);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-semibold);
  margin-left: var(--spacing-2);
  text-transform: none;
  letter-spacing: 0;
}

.subnet-table-wrap {
  overflow-x: auto;
}

.subnet-table {
  margin: 0;
}

.subnet-name {
  font-weight: var(--font-weight-medium);
  color: var(--primary-color);
  font-size: var(--font-size-sm);
}

.subnet-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  font-family: var(--font-family-mono);
}

.mono-value {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
  color: var(--text-primary);
}

.empty-subnets {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px 20px;
  text-align: center;
}

/* Action Bar */
.action-bar {
  display: flex;
  gap: var(--spacing-3);
  padding: var(--spacing-4) var(--spacing-5);
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

/* Responsive */
@media (max-width: 768px) {
  .info-grid {
    grid-template-columns: 1fr;
  }

  .title-bar {
    flex-direction: column;
    align-items: flex-start;
    gap: var(--spacing-3);
  }
}
</style>
