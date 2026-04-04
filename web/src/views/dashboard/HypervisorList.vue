<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { hypervisorsApi, type Hypervisor, type HyperDeployPayload } from '../../api/hypervisors'
import { zonesApi } from '../../api/zones'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()
import { Search as SearchIcon, Server, Plus, Trash2, RefreshCw, Copy, Check, X, Loader2, HelpCircle, Pencil, Wrench, MoreVertical, ChevronDown } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'

const vClickOutside = {
  mounted(el: any, binding: any) {
    el.clickOutsideEvent = (event: Event) => {
      if (!(el === event.target || el.contains(event.target))) {
        binding.value(event)
      }
    }
    document.addEventListener('click', el.clickOutsideEvent)
  },
  unmounted(el: any) {
    document.removeEventListener('click', el.clickOutsideEvent)
  }
}

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

// Edit
const showEditModal = ref(false)
const editingHyper = ref<Hypervisor | null>(null)
const saving = ref(false)
const editForm = ref({
    status: 0,
    zone_id: 0,
    cpu_over_rate: 1,
    mem_over_rate: 1,
    disk_over_rate: 1,
    remark: ''
})

// Maintain
const showMaintainModal = ref(false)
const maintainingHyper = ref<Hypervisor | null>(null)
const maintaining = ref(false)
const maintainForm = ref({
    migrate: true,
    target_hyper: -1
})

const activeActionMenuId = ref<string | null>(null)
const toggleActionMenu = (uuid: string) => {
    activeActionMenuId.value = activeActionMenuId.value === uuid ? null : uuid
}
const closeActionMenu = () => {
    activeActionMenuId.value = null
}

const { copiedId, copyId } = useCopyId()

const STATUS_MAP: Record<number, { labelKey: string; class: string }> = {
    0: { labelKey: 'disabled', class: 'status-disabled' },
    1: { labelKey: 'active', class: 'status-active' },
    2: { labelKey: 'maintaining', class: 'status-warning' },
    4: { labelKey: 'deploying', class: 'status-info' },
    5: { labelKey: 'deployFailed', class: 'status-error' }
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

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchHypervisors()
    }
})

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
    return STATUS_MAP[status] || { labelKey: 'unknown', class: '' }
}

const getStatusLabel = (status: number) => {
    const info = getStatusInfo(status)
    if (info.labelKey === 'unknown') return t('dashboard.hypervisorStatus.unknown', { status })
    return t('dashboard.hypervisorStatus.' + info.labelKey)
}

const formatMemory = (mb: number) => {
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} ${t('specs.gb')}`
    return `${mb} ${t('specs.mb')}`
}

const formatDisk = (gb: number) => {
    if (gb >= 1024) return `${(gb / 1024).toFixed(1)} ${t('specs.tb')}`
    return `${gb} ${t('specs.gb')}`
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
        toast.error(err.response?.data?.error || t('messages.deployFailed'))
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
    closeActionMenu()
}

const handleDelete = async () => {
    if (!deletingHyper.value) return
    deleting.value = true
    try {
        await hypervisorsApi.deleteHypervisor(deletingHyper.value.uuid)
        showDeleteConfirm.value = false
        deletingHyper.value = null
        await fetchHypervisors()
        toast.success(t('messages.deleteSuccess'))
    } catch (err: any) {
        toast.error(err.response?.data?.error || t('messages.deleteFailed'))
    } finally {
        deleting.value = false
    }
}

// Edit logic
const openEditModal = async (h: Hypervisor) => {
    editingHyper.value = h
    editForm.value = {
        status: h.status,
        zone_id: h.zone_id,
        cpu_over_rate: h.cpu_over_rate || 1,
        mem_over_rate: h.mem_over_rate || 1,
        disk_over_rate: h.disk_over_rate || 1,
        remark: h.remark || ''
    }
    showEditModal.value = true
    closeActionMenu()
    
    // Fetch zones if not already loaded
    if (zoneList.value.length === 0) {
        try {
            const resp = await zonesApi.fetchZones()
            const data = resp.data as any
            zoneList.value = Array.isArray(data) ? data : (data.zones || [])
        } catch { zoneList.value = [] }
    }
}

const handleEditSave = async () => {
    if (!editingHyper.value) return
    saving.value = true
    try {
        const payload: any = {}
        if (editForm.value.status !== editingHyper.value.status) payload.status = editForm.value.status
        if (editForm.value.zone_id !== editingHyper.value.zone_id) payload.zone_id = editForm.value.zone_id
        if (editForm.value.cpu_over_rate !== editingHyper.value.cpu_over_rate) payload.cpu_over_rate = Number(editForm.value.cpu_over_rate)
        if (editForm.value.mem_over_rate !== editingHyper.value.mem_over_rate) payload.mem_over_rate = Number(editForm.value.mem_over_rate)
        if (editForm.value.disk_over_rate !== editingHyper.value.disk_over_rate) payload.disk_over_rate = Number(editForm.value.disk_over_rate)
        if (editForm.value.remark !== (editingHyper.value.remark || '')) payload.remark = editForm.value.remark

        await hypervisorsApi.updateHypervisor(editingHyper.value.uuid, payload)
        showEditModal.value = false
        toast.success(t('messages.success'))
        await fetchHypervisors()
    } catch (err: any) {
        toast.error(err.response?.data?.error || t('messages.error'))
    } finally {
        saving.value = false
    }
}

// Maintain logic
const openMaintainModal = (h: Hypervisor) => {
    maintainingHyper.value = h
    maintainForm.value = { migrate: true, target_hyper: -1 }
    showMaintainModal.value = true
    closeActionMenu()
}

const handleMaintain = async () => {
    if (!maintainingHyper.value) return
    maintaining.value = true
    try {
        await hypervisorsApi.maintainHypervisor(maintainingHyper.value.uuid, maintainForm.value)
        showMaintainModal.value = false
        toast.success(t('messages.success'))
        await fetchHypervisors()
    } catch (err: any) {
        toast.error(err.response?.data?.error || t('messages.error'))
    } finally {
        maintaining.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchHypervisors()
    }
})
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
            <th>{{ t('dashboard.table.vcpus') }} ({{ t('dashboard.table.available') }})</th>
            <th>{{ t('dashboard.table.memory') }} ({{ t('dashboard.table.available') }})</th>
            <th>{{ t('dashboard.table.disk') }} ({{ t('dashboard.table.available') }})</th>
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
           <tr v-else v-for="h in hypervisorList" :key="h.uuid" :class="{'active-row': activeActionMenuId === h.uuid}">
             <td>
               <router-link :to="{ name: 'hypervisor-detail', params: { id: h.uuid } }" class="resource-link">
                 <div class="resource-info">
                   <div class="resource-icon">
                     <Server :size="16" />
                   </div>
                    <div>
                      <div class="resource-name">{{ h.hostname }}</div>
                      <div class="resource-id-row">
                        <span class="resource-id" :title="h.uuid">{{ h.uuid.slice(0, 8) }}...</span>
                        <button class="copy-btn-mini" @click.stop.prevent="copyId(h.uuid)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                          <Check v-if="copiedId === h.uuid" :size="10" style="color: #10b981;" />
                          <Copy v-else :size="10" />
                        </button>
                      </div>
                    </div>
                  </div>
                </router-link>
              </td>
            <td><code class="mono-value">{{ h.host_ip }}</code></td>
            <td>
              <span class="status-pill" :class="getStatusInfo(h.status).class">
                <span class="status-dot"></span>
                {{ h.status_name || getStatusLabel(h.status) }}
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
            <td class="actions-cell">
              <div class="action-dropdown">
                <button class="icon-btn-table" @click.stop="toggleActionMenu(h.uuid)" :title="t('actions.actions')">
                  <MoreVertical :size="16" />
                </button>
                <Transition name="dropdown">
                  <div v-if="activeActionMenuId === h.uuid" class="dropdown-menu dropdown-menu-right" @click.stop v-click-outside="closeActionMenu">
                    <button class="dropdown-item" @click="openEditModal(h)">
                        <Pencil :size="14" /> {{ t('actions.edit') }}
                    </button>
                    <button v-if="h.status === 1" class="dropdown-item" @click="openMaintainModal(h)">
                        <Wrench :size="14" /> {{ t('dashboard.hypervisorActions.maintain') }}
                    </button>
                    <div class="dropdown-divider"></div>
                    <button class="dropdown-item text-error" @click="confirmDelete(h)">
                        <Trash2 :size="14" /> {{ t('actions.delete') }}
                    </button>
                  </div>
                </Transition>
              </div>
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
              {{ deleting ? t('dashboard.deleteConfirm.deleting') : t('actions.delete') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
    <!-- Edit Modal -->
    <Teleport to="body">
      <div v-if="showEditModal" class="modal-overlay" @click.self="showEditModal = false">
        <div class="modal-content card" style="max-width: 500px;">
          <div class="modal-header">
            <h3>{{ t('actions.edit') }} - {{ editingHyper?.hostname }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showEditModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <div class="form-grid" style="grid-template-columns: 1fr 1fr; gap: 16px;">
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.status') }}</label>
                <select v-model="editForm.status" class="form-input">
                  <option :value="0">{{ t('dashboard.hypervisorStatus.disabled') }}</option>
                  <option :value="1">{{ t('dashboard.hypervisorStatus.active') }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.zone') }}</label>
                <select v-model="editForm.zone_id" class="form-input">
                  <option :value="0">-</option>
                  <option v-for="z in zoneList" :key="z.id" :value="z.id">{{ z.name }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.cpuOverCommit') }}</label>
                <input type="number" step="0.1" min="1" v-model="editForm.cpu_over_rate" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.memOverCommit') }}</label>
                <input type="number" step="0.1" min="1" v-model="editForm.mem_over_rate" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.diskOverCommit') }}</label>
                <input type="number" step="0.1" min="1" v-model="editForm.disk_over_rate" class="form-input" />
              </div>
              <div class="form-group" style="grid-column: span 2;">
                <label class="form-label">{{ t('dashboard.table.remark') }}</label>
                <input type="text" v-model="editForm.remark" class="form-input" :placeholder="t('dashboard.table.remark')" />
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showEditModal = false" :disabled="saving">{{ t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleEditSave" :disabled="saving">
              <Loader2 v-if="saving" :size="14" class="spinning" />
              {{ saving ? t('messages.saving') : t('actions.save') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Maintain Modal -->
    <Teleport to="body">
      <div v-if="showMaintainModal" class="modal-overlay" @click.self="showMaintainModal = false">
        <div class="modal-content card" style="max-width: 460px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.hypervisorActions.maintainTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showMaintainModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p style="margin-bottom: 16px; color: var(--text-secondary); font-size: 0.875rem;">
              {{ t('dashboard.hypervisorActions.maintainDesc', { hostname: maintainingHyper?.hostname }) }}
            </p>
            <div class="form-group" style="margin-bottom: 16px;">
              <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer;">
                <input type="checkbox" v-model="maintainForm.migrate" />
                {{ t('dashboard.hypervisorActions.migrateInstances') }}
              </label>
            </div>
            <div v-if="maintainForm.migrate" class="form-group">
              <label class="form-label">{{ t('dashboard.hypervisorActions.targetHyper') }}</label>
              <input type="number" v-model="maintainForm.target_hyper" class="form-input" />
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

.table-card { padding: 0; overflow: visible; }

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

/* Action Dropdown */
.action-dropdown {
  position: relative;
  display: flex;
  justify-content: center;
}

.dropdown-menu {
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  min-width: 160px;
  background: var(--bg-primary);
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  padding: 4px 0;
  z-index: 100;
}

.dropdown-menu-right {
    right: 0;
    left: auto;
}

.active-row {
  position: relative;
  z-index: 20;
}

.dropdown-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: none;
  background: none;
  font-size: 0.8125rem;
  color: var(--text-primary);
  cursor: pointer;
  transition: background 0.15s;
  text-align: left;
}

.dropdown-item:hover:not(:disabled) {
  background: var(--bg-tertiary);
}

.dropdown-item.text-error {
  color: #ef4444;
}

.dropdown-item.text-error:hover {
  background: #fef2f2;
}

.dropdown-divider {
  height: 1px;
  background: var(--border-light);
  margin: 4px 0;
}

.dropdown-enter-active, .dropdown-leave-active {
  transition: all 0.2s ease;
}

.dropdown-enter-from, .dropdown-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}

.btn-warning {
  background: #f59e0b;
  color: white;
  border: none;
  padding: 8px 16px;
  border-radius: var(--radius-md);
  cursor: pointer;
  font-weight: 500;
}

.btn-warning:hover { background: #d97706; }
.btn-warning:disabled { opacity: 0.5; cursor: not-allowed; }

.form-input:focus {
  outline: none;
  border-color: var(--primary-300);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.actions-cell {
  width: 80px;
  text-align: center;
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
