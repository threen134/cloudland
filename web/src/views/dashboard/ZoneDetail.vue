<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { zonesApi, type Zone } from '../../api/zones'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { ArrowLeft, MapPin, Copy, Check, Settings2, Trash2, X, Loader2, Server } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const toast = useToast()

const zone = ref<Zone | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)

// Associated hypervisors
const hypervisors = ref<Hypervisor[]>([])
const loadingHypers = ref(false)

// Edit modal
const showEditModal = ref(false)
const editing = ref(false)
const editForm = ref({ default: false, remark: '' })

// Delete modal
const showDeleteModal = ref(false)
const deleting = ref(false)

const STATUS_MAP: Record<number, { label: string; class: string }> = {
    0: { label: 'Disabled', class: 'status-disabled' },
    1: { label: 'Active', class: 'status-active' },
    2: { label: 'Maintaining', class: 'status-maintaining' },
    4: { label: 'Deploying', class: 'status-deploying' },
    5: { label: 'Deploy Failed', class: 'status-failed' },
}

const fetchZoneDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const zoneName = route.params.name as string
        const response = await zonesApi.getZone(zoneName)
        const data = response.data as any
        zone.value = data.zone || data
    } catch (err: any) {
        error.value = err.message || 'Failed to load Zone details'
    } finally {
        loading.value = false
    }
}

const fetchAssociatedHypervisors = async () => {
    if (!zone.value) return
    loadingHypers.value = true
    try {
        const response = await hypervisorsApi.fetchHypervisors({ limit: 200 })
        const data = response.data as any
        const allHypers = data.hypers || []
        hypervisors.value = allHypers.filter((h: Hypervisor) => h.zone_name === zone.value!.name)
    } catch {
        hypervisors.value = []
    } finally {
        loadingHypers.value = false
    }
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

const goBack = () => {
    router.push({ name: 'zones' })
}

// Edit
const openEditModal = () => {
    if (!zone.value) return
    editForm.value = { default: zone.value.default, remark: zone.value.remark || '' }
    showEditModal.value = true
}

const handleEdit = async () => {
    if (!zone.value) return
    editing.value = true
    try {
        await zonesApi.updateZone(zone.value.name, editForm.value)
        showEditModal.value = false
        toast.success(t('messages.success'))
        await fetchZoneDetail()
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Update failed')
    } finally {
        editing.value = false
    }
}

// Delete
const handleDelete = async () => {
    if (!zone.value) return
    deleting.value = true
    try {
        await zonesApi.deleteZone(zone.value.name)
        toast.success(t('messages.success'))
        router.push({ name: 'zones' })
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Delete failed')
    } finally {
        deleting.value = false
    }
}

const getStatusInfo = (status: number) => STATUS_MAP[status] || { label: `Unknown(${status})`, class: 'status-disabled' }

const getUsagePercent = (used: number, total: number) => {
    if (!total) return 0
    return Math.round((used / total) * 100)
}

onMounted(async () => {
    await fetchZoneDetail()
    await fetchAssociatedHypervisors()
})
</script>

<template>
  <div class="vpc-detail">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>{{ t('dashboard.zones') }}</span>
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ t('messages.loading') }}</p>
    </div>

    <!-- Error -->
    <div v-else-if="error" class="error-container card">
      <MapPin :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchZoneDetail" style="margin-top: 12px;">
        {{ t('actions.refresh') }}
      </button>
    </div>

    <!-- Zone Detail Content -->
    <div v-else-if="zone" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <MapPin :size="28" />
          </div>
          <div>
            <h2 class="resource-title">{{ zone.name }}</h2>
            <div class="resource-id-row">
              <span class="resource-id-text">ID: {{ zone.id || '-' }}</span>
              <button class="copy-btn" v-if="zone.id" @click="copyToClipboard(String(zone.id), 'id')" :title="t('messages.copied')">
                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <span class="badge badge-lg" :class="zone.default ? 'badge-primary' : 'badge-secondary'">
            {{ zone.default ? t('dashboard.zoneActions.default') : 'Zone' }}
          </span>
          <button class="btn btn-secondary btn-sm" @click="openEditModal">
            <Settings2 :size="14" />
            {{ t('actions.edit') }}
          </button>
          <button class="btn btn-danger-outline btn-sm" @click="showDeleteModal = true">
            <Trash2 :size="14" />
            {{ t('actions.delete') }}
          </button>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <div class="info-card card">
          <h3 class="card-section-title">{{ t('dashboard.table.overview') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ t('dashboard.table.name') }}</span>
              <span class="info-value">{{ zone.name }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ t('dashboard.table.id') }}</span>
              <span class="info-value mono">{{ zone.id || '-' }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ t('dashboard.zoneActions.default') }}</span>
              <span class="info-value">
                <span class="badge" :class="zone.default ? 'badge-primary' : 'badge-secondary'">
                  {{ zone.default ? 'Yes' : 'No' }}
                </span>
              </span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ t('dashboard.zoneActions.remark') }}</span>
              <span class="info-value">{{ zone.remark || '-' }}</span>
            </div>
          </div>
        </div>

        <div class="info-card card">
          <h3 class="card-section-title">{{ t('dashboard.zoneActions.associatedHypervisors') }} ({{ hypervisors.length }})</h3>
          <div v-if="loadingHypers" class="loading-small">
            <div class="loading-spinner-sm"></div>
          </div>
          <div v-else-if="hypervisors.length === 0" class="empty-hypers">
            <Server :size="32" style="opacity: 0.2; margin-bottom: 8px;" />
            <p>{{ t('dashboard.zoneActions.noHypervisors') }}</p>
          </div>
          <div v-else class="hyper-list">
            <div v-for="h in hypervisors" :key="h.uuid" class="hyper-item">
              <div class="hyper-info">
                <Server :size="14" />
                <router-link :to="{ name: 'hypervisor-detail', params: { id: h.uuid } }" class="hyper-link">
                  {{ h.hostname }}
                </router-link>
                <span class="status-pill-sm" :class="getStatusInfo(h.status).class">
                  {{ getStatusInfo(h.status).label }}
                </span>
              </div>
              <div class="hyper-stats">
                <span class="stat-item">CPU {{ getUsagePercent(h.cpu, h.cpu_total) }}%</span>
                <span class="stat-item">MEM {{ getUsagePercent(h.memory, h.memory_total) }}%</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Edit Modal -->
    <Teleport to="body">
      <div v-if="showEditModal" class="modal-overlay" @click.self="showEditModal = false">
        <div class="modal-content" style="max-width: 480px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.zoneActions.editTitle') }} - {{ zone?.name }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showEditModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <div class="form-stack">
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.zoneActions.remark') }}</label>
                <input type="text" v-model="editForm.remark" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer;">
                  <input type="checkbox" v-model="editForm.default" />
                  {{ t('dashboard.zoneActions.default') }}
                </label>
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showEditModal = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleEdit" :disabled="editing">
              <Loader2 v-if="editing" :size="14" class="spinning" />
              {{ editing ? t('messages.saving') : t('actions.save') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Delete Modal -->
    <Teleport to="body">
      <div v-if="showDeleteModal" class="modal-overlay" @click.self="showDeleteModal = false">
        <div class="modal-content" style="max-width: 440px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.zoneActions.deleteTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showDeleteModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p>{{ t('dashboard.zoneActions.deleteConfirm', { name: zone?.name }) }}</p>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showDeleteModal = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-danger" @click="handleDelete" :disabled="deleting">
              <Loader2 v-if="deleting" :size="14" class="spinning" />
              {{ deleting ? t('messages.deleting') : t('actions.delete') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.vpc-detail { max-width: 1100px; }
.detail-header { margin-bottom: var(--spacing-4); }

.back-btn {
  display: inline-flex; align-items: center; gap: var(--spacing-2);
  font-size: var(--font-size-sm); color: var(--text-secondary);
  padding: var(--spacing-2) var(--spacing-3); border-radius: var(--radius-md);
  transition: all 0.2s;
}
.back-btn:hover { color: var(--primary-color); background: var(--primary-50); }

.loading-container {
  display: flex; flex-direction: column; align-items: center;
  justify-content: center; padding: 80px 0;
}
.loading-text { margin-top: var(--spacing-3); color: var(--text-secondary); font-size: var(--font-size-sm); }

.error-container {
  display: flex; flex-direction: column; align-items: center;
  justify-content: center; padding: 60px 20px; text-align: center;
}

.title-bar {
  display: flex; justify-content: space-between; align-items: center;
  margin-bottom: var(--spacing-5);
}
.title-info { display: flex; align-items: center; gap: var(--spacing-4); }
.title-icon {
  width: 52px; height: 52px; border-radius: var(--radius-lg);
  background: linear-gradient(135deg, var(--primary-50), var(--primary-100));
  color: var(--primary-color); display: flex; align-items: center;
  justify-content: center; flex-shrink: 0;
}
.resource-title {
  margin: 0 0 4px 0; font-size: var(--font-size-xl);
  font-weight: var(--font-weight-semibold); color: var(--text-primary);
}
.resource-id-row { display: flex; align-items: center; gap: var(--spacing-2); }
.resource-id-text { font-size: var(--font-size-xs); color: var(--text-light); font-family: var(--font-family-mono); }

.copy-btn {
  background: none; border: 1px solid var(--border-light);
  border-radius: var(--radius-sm); padding: 2px 5px; cursor: pointer;
  color: var(--text-light); display: inline-flex; align-items: center;
  transition: all 0.15s;
}
.copy-btn:hover { color: var(--primary-color); border-color: var(--primary-200); background: var(--primary-50); }
.copied-icon { color: var(--success-color); }

.title-actions { display: flex; align-items: center; gap: var(--spacing-3); }

.badge { display: inline-flex; align-items: center; padding: 2px 8px; border-radius: var(--radius-sm); font-size: var(--font-size-xs); font-weight: 500; }
.badge-lg { font-size: var(--font-size-sm); padding: 6px 14px; }
.badge-primary { background: rgba(59, 130, 246, 0.1); color: #3b82f6; }
.badge-secondary { background: var(--gray-100); color: var(--gray-600); }

.btn-danger-outline {
  background: transparent; color: #ef4444; border: 1px solid #fca5a5;
  padding: 6px 12px; border-radius: var(--radius-md); cursor: pointer;
  font-weight: 500; font-size: 0.8125rem; display: inline-flex;
  align-items: center; gap: 4px; transition: all 0.2s;
}
.btn-danger-outline:hover { background: #fef2f2; border-color: #ef4444; }

.btn-sm { font-size: 0.8125rem; padding: 6px 12px; display: inline-flex; align-items: center; gap: 4px; }

.info-grid {
  display: grid; grid-template-columns: repeat(2, 1fr);
  gap: var(--spacing-4); margin-bottom: var(--spacing-5);
}
.info-card { padding: var(--spacing-5); }
.card-section-title {
  margin: 0 0 var(--spacing-4) 0; font-size: var(--font-size-sm);
  font-weight: var(--font-weight-semibold); color: var(--text-secondary);
  text-transform: uppercase; letter-spacing: 0.05em;
  padding-bottom: var(--spacing-3); border-bottom: 1px solid var(--border-light);
}
.info-rows { display: flex; flex-direction: column; gap: var(--spacing-3); }
.info-row { display: flex; justify-content: space-between; align-items: center; padding: var(--spacing-1) 0; }
.info-label { font-size: var(--font-size-sm); color: var(--text-secondary); flex-shrink: 0; }
.info-value { font-size: var(--font-size-sm); color: var(--text-primary); font-weight: var(--font-weight-medium); text-align: right; }
.info-value.mono { font-family: var(--font-family-mono); font-size: var(--font-size-xs); }

/* Hypervisors list */
.loading-small { display: flex; justify-content: center; padding: 24px 0; }
.loading-spinner-sm {
  width: 24px; height: 24px; border: 2px solid var(--border-light);
  border-top-color: var(--primary-500); border-radius: 50%;
  animation: spin 1s linear infinite;
}
.empty-hypers {
  display: flex; flex-direction: column; align-items: center;
  padding: 24px 0; color: var(--text-tertiary); font-size: var(--font-size-sm);
}
.hyper-list { display: flex; flex-direction: column; gap: 8px; }
.hyper-item {
  display: flex; justify-content: space-between; align-items: center;
  padding: 8px 12px; background: var(--bg-secondary); border-radius: var(--radius-md);
  border: 1px solid var(--border-light);
}
.hyper-info { display: flex; align-items: center; gap: 8px; }
.hyper-link {
  font-size: var(--font-size-sm); font-weight: 500;
  color: var(--primary-600); text-decoration: none;
}
.hyper-link:hover { text-decoration: underline; }
.hyper-stats { display: flex; gap: 12px; }
.stat-item { font-size: var(--font-size-xs); color: var(--text-secondary); font-family: var(--font-family-mono); }

.status-pill-sm {
  font-size: 0.6875rem; padding: 1px 6px; border-radius: 10px; font-weight: 500;
}
.status-active { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-disabled { background: var(--gray-100); color: var(--gray-500); }
.status-maintaining { background: rgba(245, 158, 11, 0.1); color: #f59e0b; }
.status-deploying { background: rgba(59, 130, 246, 0.1); color: #3b82f6; }
.status-failed { background: rgba(239, 68, 68, 0.1); color: #ef4444; }

/* Modals */
.form-stack { display: flex; flex-direction: column; gap: 16px; }
.form-group { display: flex; flex-direction: column; }
.form-label { font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 4px; font-weight: 500; }
.form-input {
  width: 100%; padding: 8px 12px;
  border: 1px solid var(--border-light); border-radius: var(--radius-md);
  font-size: 0.875rem; background: var(--bg-primary); color: var(--text-primary);
}
.form-input:focus { outline: none; border-color: var(--primary-300); box-shadow: 0 0 0 2px var(--primary-100); }

.btn-danger {
  background: #ef4444; color: white; border: none;
  padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}
.btn-danger:hover { background: #dc2626; }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 768px) {
  .info-grid { grid-template-columns: 1fr; }
  .title-bar { flex-direction: column; align-items: flex-start; gap: var(--spacing-3); }
}
</style>
