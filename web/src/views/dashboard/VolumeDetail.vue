<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { volumesApi, type Volume } from '../../api/volumes'
import { useRegionStore } from '../../stores/region'
import { ArrowLeft, HardDrive, Paperclip, Maximize, Trash2, Copy, Check, Server, Play, ChevronDown, CalendarDays } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const region = useRegionStore()

const volume = ref<Volume | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)
const showActionMenu = ref(false)

const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}

const closeActionMenu = () => {
    showActionMenu.value = false
}

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

const getStatusText = (status: string | undefined) => {
    if (!status) return '-'
    const key = status.toLowerCase()
    const translated = t(`dashboard.volumeStatus.${key}`)
    return translated === `dashboard.volumeStatus.${key}` ? status : translated
}

const getStatusClass = (status: string | undefined) => {
    if (!status) return 'status-pending'
    const statusMap: Record<string, string> = {
        'available': 'status-running',
        'attached': 'status-success',
        'in-use': 'status-success',
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
            <h2 class="volume-title">
                {{ volume.name }}
                <span :class="['badge', getStatusClass(volume.status)]">
                    {{ getStatusText(volume.status) }}
                </span>
            </h2>
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
           <div class="action-dropdown">
                <button class="btn btn-primary" @click="toggleActionMenu">
                    {{ $t('actions.actions') }} <ChevronDown :size="14" />
                </button>
                <Transition name="dropdown">
                    <div v-if="showActionMenu" class="dropdown-menu">
                        <button class="dropdown-item">
                            <Paperclip :size="14" /> {{ $t('actions.attach') }} / {{ $t('actions.detach') }}
                        </button>
                        <button class="dropdown-item">
                            <Maximize :size="14" /> {{ $t('actions.resize') }}
                        </button>
                        <div class="dropdown-divider"></div>
                        <button class="dropdown-item dropdown-item-danger" @click.stop="handleDelete">
                            <Trash2 :size="14" /> {{ $t('actions.delete') }}
                        </button>
                    </div>
                </Transition>
                <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
           </div>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="two-col-layout">
        <div class="col-stack">
          <!-- Basic Info Card (Merged with Metadata) -->
          <div class="card info-card">
            <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
            <div class="key-value-list">
              <div class="kv-item">
                <span class="label"><HardDrive :size="14" /> {{ $t('dashboard.table.size') }}</span>
                <span class="value">{{ formatSize(volume.size) }}</span>
              </div>
              <div class="kv-item">
                <span class="label"><Maximize :size="14" /> {{ $t('dashboard.table.format') }}</span>
                <span class="value" style="text-transform: uppercase;">{{ volume.format || '-' }}</span>
              </div>
              <div class="kv-item">
                <span class="label"><Play :size="14" /> {{ $t('dashboard.table.boot') }}</span>
                <span class="value">
                  <span :class="['status-badge', volume.booting ? 'status-success' : 'status-default']">
                    {{ volume.booting ? $t('messages.yes') : $t('messages.no') }}
                  </span>
                </span>
              </div>
              <div class="kv-item">
                <span class="label"><Server :size="14" /> {{ $t('dashboard.table.status') }}</span>
                <span class="value">{{ getStatusText(volume.status) }}</span>
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
                <span class="label"><CalendarDays :size="14" /> {{ $t('dashboard.table.created') }}</span>
                <span class="value">{{ formatDate(volume.created_at) }}</span>
              </div>
              <div class="kv-item">
                <span class="label"><CalendarDays :size="14" /> {{ $t('dashboard.table.updatedAt') }}</span>
                <span class="value">{{ formatDate(volume.updated_at) }}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="col-stack">
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
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
}

.volume-title .badge {
  font-size: var(--font-size-xs);
  font-weight: 500;
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

/* Action Dropdown */
.action-dropdown {
    position: relative;
}

.dropdown-backdrop {
    position: fixed;
    inset: 0;
    z-index: 9;
}

.dropdown-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 200px;
    background: var(--bg-primary, #fff);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
    padding: 4px 0;
    z-index: 10;
}

.dropdown-item {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 10px 16px;
    border: none;
    background: none;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
    transition: background 0.15s;
    text-align: left;
}

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-hover, #f3f4f6);
}

.dropdown-item-danger {
    color: var(--error-color, #ef4444);
}

.dropdown-item-danger:hover:not(:disabled) {
    background: #fef2f2;
}

.dropdown-divider {
    height: 1px;
    background: var(--border-light);
    margin: 4px 0;
}

.dropdown-enter-active, .dropdown-leave-active {
    transition: opacity 0.15s, transform 0.15s;
}

.dropdown-enter-from, .dropdown-leave-to {
    opacity: 0;
    transform: translateY(-4px);
}

/* Two-Column Layout */
.two-col-layout {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--spacing-4);
  margin-bottom: var(--spacing-6);
  align-items: start;
}

.col-stack {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-4);
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

.status-badge {
    display: inline-flex;
    padding: 2px 10px;
    border-radius: 12px;
    font-size: var(--font-size-xs);
    font-weight: 500;
}
.status-success { background: var(--success-50); color: var(--success-700); }
.status-default { background: var(--gray-100); color: var(--gray-700); }

/* Responsive */
@media (max-width: 768px) {
  .two-col-layout {
    grid-template-columns: 1fr;
  }
}
</style>
