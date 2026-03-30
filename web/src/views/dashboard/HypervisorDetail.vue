<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { zonesApi } from '../../api/zones'
import { ArrowLeft, Server, Cpu, Copy, Check, Edit, Save, X, Wrench, Loader2, ChevronDown, Pencil, Settings, Info, Activity } from 'lucide-vue-next'
import HostMonitoringCharts from '../../components/monitoring/HostMonitoringCharts.vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'

const { t } = useI18n()
const toast = useToast()
const route = useRoute()
const router = useRouter()
const hypervisor = ref<Hypervisor | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)
const saving = ref(false)
const editMode = ref(false)
const zoneList = ref<any[]>([])
const showActionMenu = ref(false)
const toggleActionMenu = () => { showActionMenu.value = !showActionMenu.value }
const closeActionMenu = () => { showActionMenu.value = false }

const STATUS_MAP: Record<number, { label: string; class: string }> = {
    0: { label: 'Disabled', class: 'status-disabled' },
    1: { label: 'Active', class: 'status-active' },
    2: { label: 'Maintaining', class: 'status-warning' },
    4: { label: 'Deploying', class: 'status-info' },
    5: { label: 'Deploy Failed', class: 'status-error' }
}

const form = ref({
    status: 0 as number | undefined,
    zone_id: 0 as number | undefined,
    cpu_over_rate: 1,
    mem_over_rate: 1,
    disk_over_rate: 1,
    remark: ''
})

// Maintain modal
const showMaintainModal = ref(false)
const maintaining = ref(false)
const maintainForm = ref({
    migrate: true,
    target_hyper: -1
})

const activeTab = ref('overview')
const tabs = [
    { id: 'overview', label: 'dashboard.table.overview', icon: Info },
    { id: 'monitor', label: 'dashboard.instanceDetail.resourceMonitoring', icon: Activity }
]

const fetchHypervisorDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const uuid = route.params.id as string
        const response = await hypervisorsApi.getHypervisor(uuid)
        hypervisor.value = response.data as any
        syncForm()
    } catch (err: any) {
        console.error('Failed to fetch hypervisor detail:', err)
        error.value = err.message || 'Failed to load Hypervisor details'
    } finally {
        loading.value = false
    }
}

const syncForm = () => {
    if (!hypervisor.value) return
    form.value = {
        status: hypervisor.value.status,
        zone_id: hypervisor.value.zone_id,
        cpu_over_rate: hypervisor.value.cpu_over_rate || 1,
        mem_over_rate: hypervisor.value.mem_over_rate || 1,
        disk_over_rate: hypervisor.value.disk_over_rate || 1,
        remark: hypervisor.value.remark || ''
    }
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

const formatMemory = (mb: number) => {
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`
    return `${mb} MB`
}

const formatDisk = (gb: number) => {
    if (gb >= 1024) return `${(gb / 1024).toFixed(1)} TB`
    return `${gb} GB`
}

const getStatusInfo = (status: number) => {
    return STATUS_MAP[status] || { label: `Unknown(${status})`, class: '' }
}

const goBack = () => { router.push({ name: 'hypervisors' }) }

const toggleEdit = async () => {
    closeActionMenu()
    editMode.value = true
    try {
        const resp = await zonesApi.fetchZones()
        const data = resp.data as any
        zoneList.value = Array.isArray(data) ? data : (data.zones || [])
    } catch { zoneList.value = [] }
}

const cancelEdit = () => {
    editMode.value = false
    syncForm()
}

const handleSave = async () => {
    if (!hypervisor.value) return
    saving.value = true
    try {
        const payload: any = {}
        if (form.value.status !== hypervisor.value.status) payload.status = form.value.status
        if (form.value.zone_id !== hypervisor.value.zone_id) payload.zone_id = form.value.zone_id
        if (form.value.cpu_over_rate !== hypervisor.value.cpu_over_rate) payload.cpu_over_rate = Number(form.value.cpu_over_rate)
        if (form.value.mem_over_rate !== hypervisor.value.mem_over_rate) payload.mem_over_rate = Number(form.value.mem_over_rate)
        if (form.value.disk_over_rate !== hypervisor.value.disk_over_rate) payload.disk_over_rate = Number(form.value.disk_over_rate)
        if (form.value.remark !== (hypervisor.value.remark || '')) payload.remark = form.value.remark

        await hypervisorsApi.updateHypervisor(hypervisor.value.uuid, payload)
        await fetchHypervisorDetail()
        editMode.value = false
        toast.success(t('messages.success'))
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Failed to update hypervisor')
    } finally {
        saving.value = false
    }
}

// Maintain
const openMaintainModal = () => {
    closeActionMenu()
    maintainForm.value = { migrate: true, target_hyper: -1 }
    showMaintainModal.value = true
}

const handleMaintain = async () => {
    if (!hypervisor.value) return
    maintaining.value = true
    try {
        await hypervisorsApi.maintainHypervisor(hypervisor.value.uuid, maintainForm.value)
        showMaintainModal.value = false
        toast.success(t('messages.success'))
        await fetchHypervisorDetail()
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Failed to start maintenance')
    } finally {
        maintaining.value = false
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
        <span>{{ t('dashboard.hypervisors') }}</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <Server :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchHypervisorDetail" style="margin-top: 12px;">
        {{ t('actions.refresh') }}
      </button>
    </div>

    <!-- Detail Content -->
    <div v-else-if="hypervisor" class="detail-content">
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <Server :size="28" />
          </div>
          <div>
            <h2 class="resource-title">
              {{ hypervisor.hostname }}
              <span :class="['badge', getStatusInfo(hypervisor.status).class]">{{ hypervisor.status_name || getStatusInfo(hypervisor.status).label }}</span>
            </h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ hypervisor.uuid }}</span>
              <button class="copy-btn" @click="copyToClipboard(hypervisor.uuid, 'uuid')" :title="t('messages.copied')">
                <Check v-if="copiedField === 'uuid'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <div class="action-dropdown">
            <button class="btn btn-primary" @click="toggleActionMenu">
              {{ t('actions.actions') }} <ChevronDown :size="14" />
            </button>
            <Transition name="dropdown">
              <div v-if="showActionMenu" class="dropdown-menu">
                <button v-if="hypervisor.status === 1" class="dropdown-item" @click="openMaintainModal">
                  <Wrench :size="14" /> {{ t('dashboard.hypervisorActions.maintain') }}
                </button>
                <button class="dropdown-item" @click="toggleEdit">
                  <Pencil :size="14" /> {{ t('actions.edit') }}
                </button>
              </div>
            </Transition>
            <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
          </div>
        </div>
      </div>

      <!-- Tabs -->
      <div class="detail-tabs">
        <button 
          v-for="tab in tabs" 
          :key="tab.id"
          class="tab-btn" 
          :class="{ active: activeTab === tab.id }"
          @click="activeTab = tab.id"
        >
          <component :is="tab.icon" :size="16" />
          {{ t(tab.label) }}
        </button>
      </div>

      <!-- Info Sections -->
      <div v-if="activeTab === 'overview'">
        <div class="info-grid">
          <!-- Basic Info Card -->
          <div class="info-card card">
            <h3 class="card-section-title">{{ t('dashboard.table.overview') }}</h3>
            <div class="info-rows">
              <div class="info-row">
                <span class="info-label">{{ t('dashboard.table.hostname') }}</span>
                <span class="info-value">{{ hypervisor.hostname }}</span>
              </div>
              <div class="info-row">
                <span class="info-label">{{ t('dashboard.table.hostIp') }}</span>
                <span class="info-value mono">{{ hypervisor.host_ip }}</span>
              </div>
              <div class="info-row">
                <span class="info-label">Route IP</span>
                <span class="info-value mono">{{ hypervisor.route_ip || '-' }}</span>
              </div>
              <div class="info-row">
                <span class="info-label">{{ t('dashboard.hypervisorDeploy.virtType') }}</span>
                <span class="info-value">{{ hypervisor.virt_type }}</span>
              </div>
              <div class="info-row">
                <span class="info-label">Host ID</span>
                <span class="info-value mono">{{ hypervisor.hostid }}</span>
              </div>
              <div class="info-row">
                <span class="info-label">{{ t('dashboard.table.zone') }}</span>
                <span class="info-value">{{ hypervisor.zone_name || '-' }}</span>
              </div>
            </div>
          </div>

          <!-- Resources Card -->
          <div class="info-card card">
            <h3 class="card-section-title">{{ t('dashboard.table.resourcesCapacity') }}</h3>
            <div class="info-rows">
              <div class="info-row">
                <span class="info-label">{{ t('dashboard.table.vcpus') }}</span>
                <span class="info-value">{{ hypervisor.cpu }} / {{ hypervisor.cpu_total }} cores</span>
              </div>
              <div class="info-row">
                <span class="info-label">{{ t('dashboard.table.memory') }}</span>
                <span class="info-value">{{ formatMemory(hypervisor.memory) }} / {{ formatMemory(hypervisor.memory_total) }}</span>
              </div>
              <div class="info-row">
                <span class="info-label">{{ t('dashboard.table.disk') }}</span>
                <span class="info-value">{{ formatDisk(hypervisor.disk) }} / {{ formatDisk(hypervisor.disk_total) }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- Overcommit Card -->
        <div class="info-card card" style="margin-bottom: var(--spacing-5);">
          <h3 class="card-section-title">{{ t('dashboard.table.overcommit') }}</h3>
          
          <div class="info-rows">
            <div class="info-row">
               <span class="info-label">{{ t('dashboard.table.cpuOverCommit') }}</span>
              <span class="info-value">{{ hypervisor.cpu_over_rate }}x</span>
            </div>
            <div class="info-row">
               <span class="info-label">{{ t('dashboard.table.memOverCommit') }}</span>
              <span class="info-value">{{ hypervisor.mem_over_rate }}x</span>
            </div>
            <div class="info-row">
               <span class="info-label">{{ t('dashboard.table.diskOverCommit') }}</span>
              <span class="info-value">{{ hypervisor.disk_over_rate }}x</span>
            </div>
            <div class="info-row" v-if="hypervisor.remark">
               <span class="info-label">{{ t('dashboard.table.remark') }}</span>
              <span class="info-value">{{ hypervisor.remark }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Monitoring Content -->
      <div v-else-if="activeTab === 'monitor'" class="monitor-section">
        <HostMonitoringCharts :hostname="hypervisor.hostname" />
      </div>
    </div>

    <!-- Edit Overcommit Modal -->
    <Teleport to="body">
      <div v-if="editMode" class="modal-overlay" @click.self="cancelEdit">
        <div class="modal-content" style="max-width: 500px;">
          <div class="modal-header">
            <h3>{{ t('actions.edit') }} {{ t('dashboard.table.overcommit') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="cancelEdit"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <div class="form-grid" style="grid-template-columns: 1fr 1fr; gap: 16px;">
              <div class="form-group">
                 <label class="form-label">{{ t('dashboard.table.zone') }}</label>
                <select v-model="form.zone_id" class="form-select">
                    <option :value="0">-</option>
                    <option v-for="z in zoneList" :key="z.id" :value="z.id">{{ z.name }}</option>
                </select>
              </div>
              <div class="form-group">
                 <label class="form-label">{{ t('dashboard.table.cpuOverCommit') }}</label>
                <input type="number" step="0.1" min="1" v-model="form.cpu_over_rate" class="form-select" />
              </div>
              <div class="form-group">
                 <label class="form-label">{{ t('dashboard.table.memOverCommit') }}</label>
                <input type="number" step="0.1" min="1" v-model="form.mem_over_rate" class="form-select" />
              </div>
              <div class="form-group">
                 <label class="form-label">{{ t('dashboard.table.diskOverCommit') }}</label>
                <input type="number" step="0.1" min="1" v-model="form.disk_over_rate" class="form-select" />
              </div>
              <div class="form-group" style="grid-column: span 2;">
                 <label class="form-label">{{ t('dashboard.table.remark') }}</label>
                <input type="text" v-model="form.remark" class="form-select" />
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="cancelEdit" :disabled="saving">{{ t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleSave" :disabled="saving">
              <Loader2 v-if="saving" :size="14" class="spinning" />
              {{ saving ? t('messages.loading') : t('actions.save') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Maintain Modal -->
    <Teleport to="body">
      <div v-if="showMaintainModal" class="modal-overlay" @click.self="showMaintainModal = false">
        <div class="modal-content" style="max-width: 460px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.hypervisorActions.maintainTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showMaintainModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p style="margin-bottom: 16px; color: var(--text-secondary); font-size: 0.875rem;">
              {{ t('dashboard.hypervisorActions.maintainDesc', { hostname: hypervisor?.hostname }) }}
            </p>
            <div class="form-group" style="margin-bottom: 16px;">
              <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer;">
                <input type="checkbox" v-model="maintainForm.migrate" />
                {{ t('dashboard.hypervisorActions.migrateInstances') }}
              </label>
            </div>
            <div v-if="maintainForm.migrate" class="form-group">
              <label class="form-label">{{ t('dashboard.hypervisorActions.targetHyper') }}</label>
              <input type="number" v-model="maintainForm.target_hyper" class="form-select" />
              <span style="font-size: 0.75rem; color: var(--text-light); margin-top: 4px;">{{ t('dashboard.hypervisorActions.targetHyperHint') }}</span>
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showMaintainModal = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-warning" @click="handleMaintain" :disabled="maintaining">
              <Loader2 v-if="maintaining" :size="14" class="spinning" />
              {{ maintaining ? t('messages.loading') : t('dashboard.hypervisorActions.maintain') }}
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
  display: inline-flex;
  align-items: center;
  gap: var(--spacing-2);
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  padding: var(--spacing-2) var(--spacing-3);
  border-radius: var(--radius-md);
  transition: all 0.2s;
}

.back-btn:hover { color: var(--primary-color); background: var(--primary-50); }

.loading-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 0;
}

.loading-text { margin-top: var(--spacing-3); color: var(--text-secondary); font-size: var(--font-size-sm); }

.error-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  text-align: center;
}

.title-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-5);
}

.title-info { display: flex; align-items: center; gap: var(--spacing-4); }

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
  margin: 0;
  font-size: var(--font-size-xl);
  font-weight: var(--font-weight-semibold);
  color: var(--text-primary);
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
}

.resource-title .badge {
  font-size: var(--font-size-xs);
  font-weight: 500;
  vertical-align: middle;
}

.resource-id-row { display: flex; align-items: center; gap: var(--spacing-2); }

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

.copy-btn:hover { color: var(--primary-color); border-color: var(--primary-200); background: var(--primary-50); }
.copied-icon { color: var(--success-color); }
.title-actions { display: flex; align-items: center; gap: var(--spacing-3); }
.badge-lg { font-size: var(--font-size-sm); padding: 6px 14px; }

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
  min-width: 180px;
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
  gap: 8px;
  width: 100%;
  padding: 8px 14px;
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

.dropdown-enter-active {
  transition: opacity 0.15s, transform 0.15s;
}

.dropdown-leave-active {
  transition: opacity 0.1s, transform 0.1s;
}

.dropdown-enter-from {
  opacity: 0;
  transform: translateY(-4px);
}

.dropdown-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

.badge.status-active, .status-active { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-error { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
.status-warning { background: rgba(245, 158, 11, 0.1); color: #f59e0b; }
.status-info { background: rgba(59, 130, 246, 0.1); color: #3b82f6; }
.status-disabled { background: var(--gray-100); color: var(--gray-500); }

.info-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--spacing-4);
  margin-bottom: var(--spacing-5);
}

.info-card { padding: var(--spacing-5); }

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

.info-rows { display: flex; flex-direction: column; gap: var(--spacing-3); }

.info-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--spacing-1) 0;
  min-height: 24px;
}

.info-label { font-size: var(--font-size-sm); color: var(--text-secondary); flex-shrink: 0; }

.info-value {
  font-size: var(--font-size-sm);
  color: var(--text-primary);
  font-weight: var(--font-weight-medium);
  text-align: right;
  word-break: break-all;
}

.info-value.mono { font-family: var(--font-family-mono); font-size: var(--font-size-xs); }

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.form-group { display: flex; flex-direction: column; }
.form-label { font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 4px; }

.form-select {
  width: 100%;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  padding: 8px;
  font-size: 0.875rem;
  background: var(--bg-primary);
  color: var(--text-primary);
}

.form-select:focus { outline: none; border-color: var(--primary-300); box-shadow: 0 0 0 2px var(--primary-100); }

/* Modal */
.btn-warning {
  background: #f59e0b; color: white; border: none;
  padding: 8px 16px; border-radius: var(--radius-md);
  cursor: pointer; font-weight: 500;
}

.btn-warning:hover { background: #d97706; }
.btn-warning:disabled { opacity: 0.5; cursor: not-allowed; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.detail-tabs {
  display: flex;
  gap: var(--spacing-4);
  margin-bottom: var(--spacing-6);
  border-bottom: 1px solid var(--border-color);
  padding: 0 var(--spacing-2);
}

.tab-btn {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  padding: var(--spacing-3) var(--spacing-4);
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-secondary);
  font-size: 0.95rem;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
  margin-bottom: -1px;
}

.tab-btn:hover {
  color: var(--text-primary);
}

.tab-btn.active {
  color: var(--text-primary);
  border-bottom-color: var(--primary-color);
}

.tab-btn svg {
  opacity: 0.7;
}

.tab-btn.active svg {
  opacity: 1;
  color: var(--primary-color);
}

.monitor-section {
  animation: fadeIn 0.3s ease-out;
}

@keyframes fadeIn {
  from { opacity: 0; transform: translateY(10px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (max-width: 768px) {
  .info-grid, .form-grid { grid-template-columns: 1fr; }
  .title-bar { flex-direction: column; align-items: flex-start; gap: var(--spacing-3); }
}
</style>
