<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { ArrowLeft, ServerCog, Copy, Check, Edit, Save, X } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const hypervisor = ref<Hypervisor | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)
const saving = ref(false)
const editMode = ref(false)

const form = ref({
    status: '',
    zone: '',
    cpu_over_commit: 1,
    mem_over_commit: 1,
    disk_over_commit: 1,
    remark: ''
})

const fetchHypervisorDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const id = route.params.id as string
        const response = await hypervisorsApi.getHypervisor(id)
        const data = response.data as any
        hypervisor.value = data.hypervisor || data
        
        if (hypervisor.value) {
            form.value = {
                status: hypervisor.value.status,
                zone: hypervisor.value.zone,
                cpu_over_commit: hypervisor.value.cpu_over_commit || 1,
                mem_over_commit: hypervisor.value.mem_over_commit || 1,
                disk_over_commit: hypervisor.value.disk_over_commit || 1,
                remark: hypervisor.value.remark || ''
            }
        }
    } catch (err: any) {
        console.error('Failed to fetch hypervisor detail:', err)
        error.value = err.message || 'Failed to load Hypervisor details'
    } finally {
        loading.value = false
    }
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

const formatMemory = (mb: number) => {
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(1)} GB`
    }
    return `${mb} MB`
}

const goBack = () => {
    router.push({ name: 'hypervisors' })
}

const toggleEdit = () => {
    editMode.value = true
}

const cancelEdit = () => {
    editMode.value = false
    if (hypervisor.value) {
        form.value = {
            status: hypervisor.value.status,
            zone: hypervisor.value.zone,
            cpu_over_commit: hypervisor.value.cpu_over_commit || 1,
            mem_over_commit: hypervisor.value.mem_over_commit || 1,
            disk_over_commit: hypervisor.value.disk_over_commit || 1,
            remark: hypervisor.value.remark || ''
        }
    }
}

const handleSave = async () => {
    if (!hypervisor.value) return
    saving.value = true
    try {
        const payload = {
            status: form.value.status,
            zone: form.value.zone,
            cpu_over_commit: Number(form.value.cpu_over_commit),
            mem_over_commit: Number(form.value.mem_over_commit),
            disk_over_commit: Number(form.value.disk_over_commit),
            remark: form.value.remark
        }
        await hypervisorsApi.updateHypervisor(hypervisor.value.id.toString(), payload)
        await fetchHypervisorDetail()
        editMode.value = false
    } catch (err) {
        console.error('Failed to update hypervisor:', err)
        alert('Failed to update hypervisor details.')
    } finally {
        saving.value = false
    }
}

onMounted(fetchHypervisorDetail)
</script>

<template>
  <div class="vpc-detail">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>{{ $t('dashboard.hypervisors') }}</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ $t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <ServerCog :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchHypervisorDetail" style="margin-top: 12px;">
        {{ $t('actions.refresh') }}
      </button>
    </div>

    <!-- Detail Content -->
    <div v-else-if="hypervisor" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <ServerCog :size="28" />
          </div>
          <div>
            <h2 class="resource-title">{{ hypervisor.hostname }}</h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ hypervisor.id }}</span>
              <button class="copy-btn" @click="copyToClipboard(hypervisor.id.toString(), 'id')" :title="$t('messages.copied')">
                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <span :class="['badge', 'badge-lg', hypervisor.state === 'up' ? 'status-active' : 'status-error' ]">
            {{ hypervisor.state }}
          </span>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <!-- Basic Info Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ $t('dashboard.table.overview') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.hostname') }}</span>
              <span class="info-value">{{ hypervisor.hostname }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.hostIp') }}</span>
              <span class="info-value mono">{{ hypervisor.host_ip }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.hypervisorType') }}</span>
              <span class="info-value">{{ hypervisor.hypervisor_type }} (v{{ hypervisor.version }})</span>
            </div>
          </div>
        </div>

        <!-- Resources Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ $t('dashboard.table.resourcesCapacity') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.vcpus') }}</span>
              <span class="info-value">{{ hypervisor.vcpus_used }} used / {{ hypervisor.vcpus }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.memory') }}</span>
              <span class="info-value">{{ formatMemory(hypervisor.memory_mb_used) }} used / {{ formatMemory(hypervisor.memory_mb) }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.diskAvailableLeast') }}</span>
              <span class="info-value">{{ hypervisor.disk_available_least }} GB</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Configuration Card -->
      <div class="info-card card" style="margin-bottom: var(--spacing-5);">
        <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-light); margin-bottom: var(--spacing-4); padding-bottom: var(--spacing-3);">
           <h3 class="card-section-title" style="margin: 0; padding: 0; border: none;">{{ $t('dashboard.table.configuration') }}</h3>
          <button v-if="!editMode" class="btn btn-secondary btn-sm" @click="toggleEdit">
            <Edit :size="14" /> {{ $t('actions.edit') }}
          </button>
          <div v-else style="display: flex; gap: 8px;">
            <button class="btn btn-ghost btn-sm" @click="cancelEdit" :disabled="saving">
               <X :size="14" /> {{ $t('actions.cancel') }}
            </button>
            <button class="btn btn-primary btn-sm" @click="handleSave" :disabled="saving">
              <Save :size="14" v-if="!saving" />
               {{ saving ? $t('dashboard.migrationForm.starting') : $t('actions.save') }}
            </button>
          </div>
        </div>
        
        <div class="info-rows" v-if="!editMode">
          <div class="info-row">
             <span class="info-label">{{ $t('dashboard.table.status') }}</span>
            <span class="info-value">
              <span :class="['badge', hypervisor.status === 'enabled' ? 'status-active' : '']" :style="hypervisor.status !== 'enabled' ? 'background: var(--gray-100); color: var(--gray-700);' : ''">{{ hypervisor.status }}</span>
            </span>
          </div>
          <div class="info-row">
             <span class="info-label">{{ $t('dashboard.table.zone') }}</span>
            <span class="info-value">{{ hypervisor.zone || '-' }}</span>
          </div>
          <div class="info-row">
             <span class="info-label">{{ $t('dashboard.table.cpuOverCommit') }}</span>
            <span class="info-value">{{ hypervisor.cpu_over_commit }}x</span>
          </div>
          <div class="info-row">
             <span class="info-label">{{ $t('dashboard.table.memOverCommit') }}</span>
            <span class="info-value">{{ hypervisor.mem_over_commit }}x</span>
          </div>
          <div class="info-row">
             <span class="info-label">{{ $t('dashboard.table.diskOverCommit') }}</span>
            <span class="info-value">{{ hypervisor.disk_over_commit }}x</span>
          </div>
          <div class="info-row" v-if="hypervisor.remark">
             <span class="info-label">{{ $t('dashboard.table.remark') }}</span>
            <span class="info-value">{{ hypervisor.remark }}</span>
          </div>
        </div>

        <div class="form-grid" v-else>
          <div class="form-group">
             <label class="form-label">{{ $t('dashboard.table.status') }}</label>
            <select v-model="form.status" class="form-select" style="width: 100%; border: 1px solid var(--border-light); border-radius: var(--radius-md); padding: 8px;">
                <option value="enabled">Enabled</option>
                <option value="disabled">Disabled</option>
            </select>
          </div>
          <div class="form-group">
             <label class="form-label">{{ $t('dashboard.table.zone') }}</label>
            <input type="text" v-model="form.zone" class="form-input" style="width: 100%; border: 1px solid var(--border-light); border-radius: var(--radius-md); padding: 8px;" />
          </div>
          <div class="form-group">
             <label class="form-label">{{ $t('dashboard.table.cpuOverCommit') }}</label>
            <input type="number" step="0.1" v-model="form.cpu_over_commit" class="form-input" style="width: 100%; border: 1px solid var(--border-light); border-radius: var(--radius-md); padding: 8px;" />
          </div>
          <div class="form-group">
             <label class="form-label">{{ $t('dashboard.table.memOverCommit') }}</label>
            <input type="number" step="0.1" v-model="form.mem_over_commit" class="form-input" style="width: 100%; border: 1px solid var(--border-light); border-radius: var(--radius-md); padding: 8px;" />
          </div>
          <div class="form-group">
             <label class="form-label">{{ $t('dashboard.table.diskOverCommit') }}</label>
            <input type="number" step="0.1" v-model="form.disk_over_commit" class="form-input" style="width: 100%; border: 1px solid var(--border-light); border-radius: var(--radius-md); padding: 8px;" />
          </div>
          <div class="form-group" style="grid-column: 1 / -1;">
             <label class="form-label">{{ $t('dashboard.table.remark') }}</label>
            <input type="text" v-model="form.remark" class="form-input" style="width: 100%; border: 1px solid var(--border-light); border-radius: var(--radius-md); padding: 8px;" />
          </div>
        </div>

      </div>

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
.badge.status-active, .status-active {
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}
.status-error {
  background: rgba(239, 68, 68, 0.1);
  color: #ef4444;
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
  min-height: 24px;
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

/* Form Styles */
.form-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.form-group {
    display: flex;
    flex-direction: column;
}

.form-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: 4px;
}

@media (max-width: 768px) {
  .info-grid, .form-grid {
    grid-template-columns: 1fr;
  }

  .title-bar {
    flex-direction: column;
    align-items: flex-start;
    gap: var(--spacing-3);
  }
}
</style>
