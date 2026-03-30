<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { hypervisorsApi, type Hypervisor, type HyperDeployPayload } from '../../api/hypervisors'
import { zonesApi } from '../../api/zones'
import { Search as SearchIcon, Server, Plus, Trash2, RefreshCw, Copy, Check, X, Loader2, HelpCircle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'

const { t } = useI18n()
const toast = useToast()
const hypervisorList = ref<Hypervisor[]>([])
const loading = ref(false)
const searchQuery = ref('')

// Pagination
const currentPage = ref(1)
const pageSize = ref(20)
const totalCount = ref(0)

// Deploy modal
const showDeployModal = ref(false)
const deploying = ref(false)
const deployResult = ref<Hypervisor | null>(null)
const deployForm = ref<HyperDeployPayload>({
    ip: '',
    hostname: '',
    network_device: 'eth0',
    vlan_device: '',
    private_vlan_device: '',
    dns_server: '8.8.8.8',
    domain: 'example.com',
    zone_name: '',
    virt_type: 'kvm-x86_64'
})
const zoneList = ref<any[]>([])
const copiedCmd = ref(false)

// Delete
const showDeleteConfirm = ref(false)
const deletingHyper = ref<Hypervisor | null>(null)
const deleting = ref(false)

const STATUS_MAP: Record<number, { label: string; class: string }> = {
    0: { label: 'Disabled', class: 'status-disabled' },
    1: { label: 'Active', class: 'status-active' },
    2: { label: 'Maintaining', class: 'status-warning' },
    4: { label: 'Deploying', class: 'status-info' },
    5: { label: 'Deploy Failed', class: 'status-error' }
}

const fetchHypervisors = async () => {
    loading.value = true
    try {
        const offset = (currentPage.value - 1) * pageSize.value
        const response = await hypervisorsApi.fetchHypervisors({
            offset,
            limit: pageSize.value,
            q: searchQuery.value || undefined
        })
        const data = response.data as any
        hypervisorList.value = Array.isArray(data) ? data : (data.hypers || [])
        totalCount.value = data.total || hypervisorList.value.length
    } catch (error) {
        console.error('API fetch failed:', error)
        hypervisorList.value = []
    } finally {
        loading.value = false
    }
}

const totalPages = computed(() => Math.max(1, Math.ceil(totalCount.value / pageSize.value)))

const goToPage = (page: number) => {
    if (page < 1 || page > totalPages.value) return
    currentPage.value = page
    fetchHypervisors()
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
const onSearchInput = () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
        currentPage.value = 1
        fetchHypervisors()
    }, 400)
}

const getStatusInfo = (status: number) => {
    return STATUS_MAP[status] || { label: `Unknown(${status})`, class: '' }
}

const formatMemory = (mb: number) => {
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`
    return `${mb} MB`
}

const formatDisk = (gb: number) => {
    if (gb >= 1024) return `${(gb / 1024).toFixed(1)} TB`
    return `${gb} GB`
}

const usagePercent = (used: number, total: number) => {
    if (total <= 0) return 0
    return Math.round((used / total) * 100)
}

// Deploy
const openDeployModal = async () => {
    deployForm.value = {
        ip: '', hostname: '', network_device: '', vlan_device: '', private_vlan_device: '',
        dns_server: '8.8.8.8', domain: 'example.com', zone_name: '', virt_type: 'kvm-x86_64'
    }
    deployResult.value = null
    showDeployModal.value = true
    try {
        const resp = await zonesApi.fetchZones()
        const data = resp.data as any
        zoneList.value = Array.isArray(data) ? data : (data.zones || [])
    } catch { zoneList.value = [] }
}

const handleDeploy = async () => {
    if (!deployForm.value.ip || !deployForm.value.hostname || !deployForm.value.network_device) return
    deploying.value = true
    try {
        const resp = await hypervisorsApi.deployHypervisor(deployForm.value)
        deployResult.value = resp.data as any
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Deploy failed')
    } finally {
        deploying.value = false
    }
}

const copyDeployCommand = () => {
    if (deployResult.value?.deploy_command) {
        navigator.clipboard.writeText(deployResult.value.deploy_command)
        copiedCmd.value = true
        setTimeout(() => { copiedCmd.value = false }, 2000)
    }
}

const closeDeployModal = () => {
    showDeployModal.value = false
    if (deployResult.value) fetchHypervisors()
}

// Delete
const confirmDelete = (h: Hypervisor) => {
    deletingHyper.value = h
    showDeleteConfirm.value = true
}

const handleDelete = async () => {
    if (!deletingHyper.value) return
    deleting.value = true
    try {
        await hypervisorsApi.deleteHypervisor(deletingHyper.value.uuid)
        showDeleteConfirm.value = false
        deletingHyper.value = null
        await fetchHypervisors()
    } catch (err: any) {
        toast.error(err.response?.data?.error || t('messages.deleteFailed'))
    } finally {
        deleting.value = false
    }
}

onMounted(fetchHypervisors)
</script>

<template>
  <div class="vpc-list-container">
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <SearchIcon :size="16" class="search-icon" />
          <input type="text" v-model="searchQuery" @input="onSearchInput" :placeholder="t('actions.search') + '...'" class="search-input" />
        </div>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchHypervisors" :title="t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openDeployModal">
          <Plus :size="14" />
          <span>{{ t('dashboard.hypervisorActions.deploy') }}</span>
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('dashboard.table.nameId') }}</th>
            <th>{{ t('dashboard.table.hostIp') }}</th>
            <th>{{ t('dashboard.table.status') }}</th>
            <th>{{ t('dashboard.table.vcpus') }}</th>
            <th>{{ t('dashboard.table.memory') }}</th>
            <th>{{ t('dashboard.table.disk') }}</th>
            <th>{{ t('dashboard.table.zone') }}</th>
            <th>{{ t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="8" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="hypervisorList.length === 0">
            <td colspan="8" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                   <p>{{ t('messages.noResults') }}</p>
                </div>
                <div v-else class="empty-state">
                   <Server :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                   <p>{{ t('messages.noData') }}</p>
                </div>
             </td>
           </tr>
           <tr v-else v-for="h in hypervisorList" :key="h.uuid">
             <td>
               <router-link :to="{ name: 'hypervisor-detail', params: { id: h.uuid } }" class="resource-link">
                 <div class="resource-info">
                   <div class="resource-icon">
                     <Server :size="16" />
                   </div>
                   <div>
                     <div class="resource-name">{{ h.hostname }}</div>
                     <div class="resource-id">{{ h.uuid }}</div>
                   </div>
                 </div>
               </router-link>
             </td>
            <td><code class="mono-value">{{ h.host_ip }}</code></td>
            <td>
              <span class="status-pill" :class="getStatusInfo(h.status).class">
                <span class="status-dot"></span>
                {{ h.status_name || getStatusInfo(h.status).label }}
              </span>
            </td>
            <td>
              <div class="usage-cell">
                <span>{{ h.cpu }} / {{ h.cpu_total }}</span>
                <div class="usage-bar"><div class="usage-fill" :style="{ width: usagePercent(h.cpu, h.cpu_total) + '%' }"></div></div>
              </div>
            </td>
            <td>
              <div class="usage-cell">
                <span>{{ formatMemory(h.memory) }} / {{ formatMemory(h.memory_total) }}</span>
                <div class="usage-bar"><div class="usage-fill" :style="{ width: usagePercent(h.memory, h.memory_total) + '%' }"></div></div>
              </div>
            </td>
            <td>
              <div class="usage-cell">
                <span>{{ formatDisk(h.disk) }} / {{ formatDisk(h.disk_total) }}</span>
                <div class="usage-bar"><div class="usage-fill" :style="{ width: usagePercent(h.disk, h.disk_total) + '%' }"></div></div>
              </div>
            </td>
            <td>{{ h.zone_name || '-' }}</td>
            <td>
              <button class="icon-btn-table text-error" @click.prevent="confirmDelete(h)" :title="t('actions.delete')">
                <Trash2 :size="16" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>

      <!-- Pagination -->
      <div v-if="totalPages > 1" class="pagination-bar">
        <span class="pagination-info">{{ t('dashboard.pagination.showing', { from: (currentPage - 1) * pageSize + 1, to: Math.min(currentPage * pageSize, totalCount), total: totalCount }) }}</span>
        <div class="pagination-controls">
          <button class="page-btn" :disabled="currentPage <= 1" @click="goToPage(currentPage - 1)">&lsaquo;</button>
          <template v-for="p in totalPages" :key="p">
            <button v-if="p === 1 || p === totalPages || (p >= currentPage - 1 && p <= currentPage + 1)" class="page-btn" :class="{ active: p === currentPage }" @click="goToPage(p)">{{ p }}</button>
            <span v-else-if="p === currentPage - 2 || p === currentPage + 2" class="page-ellipsis">...</span>
          </template>
          <button class="page-btn" :disabled="currentPage >= totalPages" @click="goToPage(currentPage + 1)">&rsaquo;</button>
        </div>
      </div>
    </div>

    <!-- Deploy Modal -->
    <Teleport to="body">
      <div v-if="showDeployModal" class="modal-overlay" @click.self="closeDeployModal">
        <div class="modal-content card" style="max-width: 560px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.hypervisorActions.deploy') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="closeDeployModal"><X :size="18" /></button>
          </div>

          <!-- Deploy Form -->
          <div v-if="!deployResult" class="modal-body">
            <div class="form-grid">
              <div class="form-group">
                <label class="form-label">IP *</label>
                <input type="text" v-model="deployForm.ip" class="form-input" :placeholder="t('dashboard.forms.placeholder.ipExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.hostname') }} *</label>
                <input type="text" v-model="deployForm.hostname" class="form-input" :placeholder="t('dashboard.forms.placeholder.hostnameExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">
                  {{ t('dashboard.hypervisorDeploy.networkDevice') }} *
                  <span class="tooltip-wrapper">
                    <HelpCircle :size="14" class="help-icon" />
                    <span class="tooltip-text">{{ t('dashboard.hypervisorDeploy.tooltips.networkDevice') }}</span>
                  </span>
                </label>
                <input type="text" v-model="deployForm.network_device" class="form-input" :placeholder="t('dashboard.forms.placeholder.netDeviceExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">
                  {{ t('dashboard.hypervisorDeploy.vlanDevice') }}
                  <span class="tooltip-wrapper">
                    <HelpCircle :size="14" class="help-icon" />
                    <span class="tooltip-text">{{ t('dashboard.hypervisorDeploy.tooltips.vlanDevice') }}</span>
                  </span>
                </label>
                <input type="text" v-model="deployForm.vlan_device" class="form-input" :placeholder="deployForm.network_device || t('dashboard.forms.placeholder.netDeviceExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">
                  {{ t('dashboard.hypervisorDeploy.privateVlanDevice') }}
                  <span class="tooltip-wrapper">
                    <HelpCircle :size="14" class="help-icon" />
                    <span class="tooltip-text">{{ t('dashboard.hypervisorDeploy.tooltips.privateVlanDevice') }}</span>
                  </span>
                </label>
                <input type="text" v-model="deployForm.private_vlan_device" class="form-input" :placeholder="deployForm.vlan_device || deployForm.network_device || t('dashboard.forms.placeholder.netDeviceExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.hypervisorDeploy.dnsServer') }}</label>
                <input type="text" v-model="deployForm.dns_server" class="form-input" :placeholder="t('dashboard.forms.placeholder.dnsExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.hypervisorDeploy.domain') }}</label>
                <input type="text" v-model="deployForm.domain" class="form-input" :placeholder="t('dashboard.forms.placeholder.domainExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.zone') }}</label>
                <select v-model="deployForm.zone_name" class="form-input">
                  <option value="">{{ t('dashboard.hypervisorDeploy.autoZone') }}</option>
                  <option v-for="z in zoneList" :key="z.name" :value="z.name">{{ z.name }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.hypervisorDeploy.virtType') }}</label>
                <select v-model="deployForm.virt_type" class="form-input">
                  <option value="kvm-x86_64">kvm-x86_64</option>
                  <option value="kvm-aarch64">kvm-aarch64</option>
                </select>
              </div>
            </div>
          </div>

          <!-- Deploy Result -->
          <div v-else class="modal-body">
            <div class="deploy-success">
              <Check :size="32" style="color: #10b981; margin-bottom: 12px;" />
              <p style="font-weight: 600; margin-bottom: 16px;">{{ t('dashboard.hypervisorDeploy.created') }}</p>
              <p style="font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 16px;">{{ t('dashboard.hypervisorDeploy.runCommand') }}</p>
              <div class="deploy-cmd-box">
                <pre>{{ deployResult.deploy_command }}</pre>
                <button class="copy-cmd-btn" @click="copyDeployCommand">
                  <Check v-if="copiedCmd" :size="14" style="color: #10b981;" />
                  <Copy v-else :size="14" />
                </button>
              </div>
            </div>
          </div>

          <div class="modal-footer">
            <button class="btn btn-secondary" @click="closeDeployModal">{{ deployResult ? t('actions.close') : t('actions.cancel') }}</button>
            <button v-if="!deployResult" class="btn btn-primary" @click="handleDeploy" :disabled="deploying || !deployForm.ip || !deployForm.hostname || !deployForm.network_device">
              {{ deploying ? t('messages.loading') : t('dashboard.hypervisorActions.deploy') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Delete Confirm Modal -->
    <Teleport to="body">
      <div v-if="showDeleteConfirm" class="modal-overlay" @click.self="showDeleteConfirm = false">
        <div class="modal-content card" style="max-width: 440px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.hypervisorActions.deleteTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showDeleteConfirm = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p>{{ t('dashboard.hypervisorActions.deleteConfirm', { hostname: deletingHyper?.hostname }) }}</p>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showDeleteConfirm = false">{{ t('actions.cancel') }}</button>
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
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0;
  padding-right: 20px;
}

.search-wrapper {
  flex: 1;
  max-width: 400px;
}

.header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

.search-box {
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--bg-secondary);
  padding: 0 12px;
  height: 40px;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-light);
  transition: all 0.2s;
}

.search-box:focus-within {
  border-color: var(--primary-300);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.search-icon { color: var(--gray-400); }

.search-input {
  border: none;
  background: transparent;
  width: 100%;
  height: 100%;
  font-size: 0.875rem;
  color: var(--text-primary);
}

.search-input:focus { outline: none; }

.table-card { padding: 0; overflow: hidden; }

.resource-link {
  text-decoration: none;
  display: block;
  padding: 4px 0;
  border-radius: var(--radius-sm);
  transition: all 0.15s;
}

.resource-link:hover .resource-name {
  color: var(--primary-600);
  text-decoration: underline;
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: var(--radius-full);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  background: var(--gray-100);
  color: var(--gray-700);
}

.status-active { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-error { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
.status-warning { background: rgba(245, 158, 11, 0.1); color: #f59e0b; }
.status-info { background: rgba(59, 130, 246, 0.1); color: #3b82f6; }
.status-disabled { background: var(--gray-100); color: var(--gray-500); }

.status-dot {
  width: 6px;
  height: 6px;
  background: currentColor;
  border-radius: 50%;
}

.mono-value {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
  color: var(--text-primary);
}

.usage-cell {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--font-size-xs);
  min-width: 120px;
}

.usage-bar {
  height: 4px;
  background: var(--gray-100);
  border-radius: 2px;
  overflow: hidden;
}

.usage-fill {
  height: 100%;
  background: var(--primary-color);
  border-radius: 2px;
  transition: width 0.3s;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}

.icon-btn-table {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: all 0.2s;
}

.icon-btn-table:hover { background-color: var(--bg-tertiary); color: var(--primary-500); }
.icon-btn-table.text-error:hover { background-color: #fef2f2; color: #ef4444; }

/* Modal */
.form-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.form-group { display: flex; flex-direction: column; }

.form-label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 0.8125rem;
  color: var(--text-secondary);
  margin-bottom: 4px;
  font-weight: 500;
}

:deep(.modal-body) {
  overflow: visible !important;
}

.help-icon {
  color: var(--text-tertiary);
  opacity: 0.7;
  transition: opacity 0.2s;
}

.tooltip-wrapper {
  position: relative;
  display: inline-flex;
  cursor: help;
}

.tooltip-text {
  display: none;
  position: absolute;
  bottom: calc(100% + 8px);
  left: -10px;
  background: #1f2937;
  color: white;
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 0.75rem;
  font-weight: 400;
  width: 220px;
  line-height: 1.4;
  z-index: 1000;
  box-shadow: 0 10px 15px -3px rgba(0, 0, 0, 0.4);
  pointer-events: none;
  white-space: normal;
  text-align: left;
}

/* Tooltip arrow */
.tooltip-text::after {
  content: "";
  position: absolute;
  top: 100%;
  left: 17px;
  margin-left: -5px;
  border-width: 5px;
  border-style: solid;
  border-color: #1f2937 transparent transparent transparent;
}

.tooltip-wrapper:hover .tooltip-text {
  display: block;
}

.tooltip-wrapper:hover .help-icon {
  opacity: 1;
  color: var(--primary-500);
}

.form-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  font-size: 0.875rem;
  background: var(--bg-primary);
  color: var(--text-primary);
}

.form-input:focus {
  outline: none;
  border-color: var(--primary-300);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.btn-danger {
  background: #ef4444;
  color: white;
  border: none;
  padding: 8px 16px;
  border-radius: var(--radius-md);
  cursor: pointer;
  font-weight: 500;
}

.btn-danger:hover { background: #dc2626; }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.deploy-success {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
}

.deploy-cmd-box {
  position: relative;
  width: 100%;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  padding: 12px 40px 12px 12px;
}

.deploy-cmd-box pre {
  margin: 0;
  font-size: 0.75rem;
  font-family: var(--font-family-mono);
  white-space: pre-wrap;
  word-break: break-all;
  text-align: left;
}

.copy-cmd-btn {
  position: absolute;
  top: 8px;
  right: 8px;
  background: none;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-sm);
  padding: 4px 6px;
  cursor: pointer;
  color: var(--text-light);
  display: flex;
  align-items: center;
}

.copy-cmd-btn:hover { background: var(--bg-secondary); }

/* Pagination */
.pagination-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 24px;
  border-top: 1px solid var(--border-light);
  font-size: var(--font-size-xs);
}

.pagination-info { color: var(--text-secondary); }

.pagination-controls {
  display: flex;
  align-items: center;
  gap: 4px;
}

.page-btn {
  min-width: 32px;
  height: 32px;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-sm);
  background: var(--bg-primary);
  color: var(--text-primary);
  cursor: pointer;
  font-size: var(--font-size-xs);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.15s;
}

.page-btn:hover:not(:disabled):not(.active) {
  background: var(--bg-tertiary);
  border-color: var(--primary-300);
}

.page-btn.active {
  background: var(--primary-color);
  color: #fff;
  border-color: var(--primary-color);
}

.page-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.page-ellipsis {
  padding: 0 4px;
  color: var(--text-light);
}

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
