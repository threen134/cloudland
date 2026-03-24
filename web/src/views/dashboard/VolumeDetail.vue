<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { volumesApi, type Volume } from '../../api/volumes'
import { useRegionStore } from '../../stores/region'
import { ArrowLeft, HardDrive, Paperclip, Maximize, Trash2, Copy, Check, Server, Play } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const region = useRegionStore()

const volume = ref<Volume | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)

const fetchVolume = async () => {
    const id = route.params.id as string
    if (!id) return

    loading.value = true
    error.value = null
    try {
        const response = await volumesApi.get(id)
        volume.value = response
    } catch (err: any) {
        console.error('Failed to fetch volume:', err)
        error.value = err.message || 'Failed to load volume details'
    } finally {
        loading.value = false
    }
}

watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchVolume()
    }
})

const goBack = () => {
    router.push({ name: 'volumes' })
}

const navigateToInstance = (id: string) => {
    router.push({ name: 'instance-detail', params: { id } })
}

const formatSize = (size: number) => {
    if (size >= 1000) {
        return `${(size / 1000).toFixed(1)} TB`
    }
    return `${size} GB`
}

const getStatusClass = (status: string) => {
    const statusMap: Record<string, string> = {
        'available': 'status-active',
        'attached': 'status-running',
        'in-use': 'status-running',
        'creating': 'status-pending',
        'deleting': 'status-pending',
        'detaching': 'status-pending',
        'attaching': 'status-pending',
        'error': 'status-error'
    }
    return statusMap[status] || 'status-pending'
}

const formatDate = (dateStr?: string) => {
    if (!dateStr) return '-'
    const d = new Date(dateStr)
    return d.toLocaleString()
}

const handleDelete = async () => {
    if (!confirm('Are you sure you want to delete this volume? This action cannot be undone.')) return
    
    try {
        await volumesApi.delete(route.params.id as string)
        router.push({ name: 'volumes' })
    } catch (err) {
        console.error('Failed to delete volume:', err)
        alert('Failed to delete volume.')
    }
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchVolume()
    }
})
</script>

<template>
  <div class="detail-page">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost btn-sm" @click="goBack">
        <ArrowLeft :size="16" />
        <span>{{ $t('actions.back') }}</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ $t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <HardDrive :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchVolume" style="margin-top: 12px;">
        {{ $t('actions.refresh') }}
      </button>
    </div>

    <!-- Volume Detail Content -->
    <div v-else-if="volume" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <HardDrive :size="28" />
          </div>
          <div>
            <h2 class="volume-title">{{ volume.name }}</h2>
            <div class="volume-id-row">
              <span class="volume-id">{{ volume.id }}</span>
              <button class="copy-btn" @click="copyToClipboard(volume.id, 'id')" :title="$t('messages.copied')">
                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <span :class="['badge', 'badge-lg', getStatusClass(volume.status)]">
            {{ volume.status }}
          </span>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <!-- Basic Info Card -->
        <div class="card info-card">
          <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
          <div class="key-value-list">
            <div class="kv-item">
              <span class="label"><HardDrive :size="14" /> {{ $t('dashboard.table.size') }}</span>
              <span class="value">{{ formatSize(volume.size) }}</span>
            </div>
            <div class="kv-item">
              <span class="label"><Maximize :size="14" /> {{ $t('dashboard.table.format') }}</span>
              <span class="value">{{ volume.format || '-' }}</span>
            </div>
            <div class="kv-item">
              <span class="label"><Play :size="14" /> {{ $t('dashboard.table.boot') }}</span>
              <span class="value">
                <span :class="['status-badge', volume.booting ? 'status-success' : 'status-default']" style="font-size: 10px; padding: 0 6px;">
                  {{ volume.booting ? 'YES' : 'NO' }}
                </span>
              </span>
            </div>
            <div class="kv-item">
              <span class="label">{{ $t('dashboard.table.status') }}</span>
              <span class="value">
                <span :class="['badge', getStatusClass(volume.status)]">{{ volume.status }}</span>
              </span>
            </div>
          </div>
        </div>

        <!-- Attachment Info Card -->
        <div class="card info-card">
          <h3>{{ $t('dashboard.table.attachedTo') }}</h3>
          <div class="key-value-list">
            <div class="kv-item">
              <span class="label"><Server :size="14" /> {{ $t('dashboard.instances') }}</span>
              <span v-if="volume.instance" class="value text-primary clickable" @click="navigateToInstance(volume.instance.id)">
                {{ volume.instance.name }}
              </span>
              <span v-else class="value text-light">{{ $t('messages.notAttached') }}</span>
            </div>
            <div v-if="volume.instance" class="kv-item">
               <span class="label">{{ $t('dashboard.table.instanceId') }}</span>
              <span class="value mono">{{ volume.instance.id }}</span>
            </div>
            <div class="kv-item">
               <span class="label">{{ $t('dashboard.table.target') }}</span>
              <span class="value mono">{{ volume.target || '-' }}</span>
            </div>
            <div class="kv-item">
               <span class="label">{{ $t('dashboard.table.path') }}</span>
              <span class="value mono">{{ volume.path || '-' }}</span>
            </div>
          </div>
        </div>

        <!-- QoS Info Card -->
        <div class="card info-card">
           <h3>{{ $t('dashboard.table.performance') }}</h3>
          <div class="key-value-list">
            <div class="kv-item">
               <span class="label">{{ $t('dashboard.table.iopsLimitBurst') }}</span>
              <span class="value">{{ volume.iops_limit ?? '-' }} / {{ volume.iops_burst ?? '-' }}</span>
            </div>
            <div class="kv-item">
               <span class="label">{{ $t('dashboard.table.bpsLimitBurst') }}</span>
              <span class="value">{{ volume.bps_limit ?? '-' }} / {{ volume.bps_burst ?? '-' }}</span>
            </div>
          </div>
        </div>

        <!-- Metadata Card -->
        <div class="card info-card">
           <h3>{{ $t('dashboard.table.metadata') }}</h3>
          <div class="key-value-list">
            <div class="kv-item">
               <span class="label">{{ $t('dashboard.table.owner') }}</span>
              <span class="value">{{ volume.owner || '-' }}</span>
            </div>
            <div class="kv-item">
               <span class="label">{{ $t('dashboard.table.created') }}</span>
              <span class="value">{{ formatDate(volume.created_at) }}</span>
            </div>
            <div class="kv-item">
               <span class="label">{{ $t('dashboard.table.updatedAt') }}</span>
              <span class="value">{{ formatDate(volume.updated_at) }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Action Bar -->
      <div class="action-bar card">
        <div class="action-group">
          <button class="btn btn-secondary">
            <Paperclip :size="16" />
            {{ $t('actions.attach') }} / {{ $t('actions.detach') }}
          </button>
          <button class="btn btn-secondary">
            <Maximize :size="16" />
            {{ $t('actions.resize') }}
          </button>
        </div>
        <div class="action-group">
          <button class="btn btn-danger" @click="handleDelete">
            <Trash2 :size="16" />
            {{ $t('actions.delete') }}
          </button>
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

.loading-text {
  margin-top: var(--spacing-3);
  color: var(--text-secondary);
  font-size: var(--font-size-sm);
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

.volume-title {
  margin: 0 0 4px 0;
  font-size: var(--font-size-xl);
  font-weight: var(--font-weight-semibold);
  color: var(--primary-color);
}

.volume-id-row {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
}

.volume-id {
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

/* Status markers used locally */
.status-badge {
    display: inline-flex;
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
}
.status-success { background: var(--success-50); color: var(--success-700); }
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
  display: flex;
  align-items: center;
  gap: 6px;
}

.kv-item .value {
  color: var(--text-primary);
  font-weight: 500;
  text-align: right;
  word-break: break-all;
}

.value.mono {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
}

.value.text-primary {
  color: var(--primary-color);
}

.value.clickable {
  cursor: pointer;
}

.value.clickable:hover {
  text-decoration: underline;
}

.value.text-light {
  color: var(--text-light);
  font-weight: normal;
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
