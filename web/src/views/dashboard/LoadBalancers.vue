<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useToast } from '../../composables/useToast'
import { useRegionStore } from '../../stores/region'
import { loadBalancersApi, vpcsApi, type LoadBalancer, type VPC, type LoadBalancerPayload } from '../../api/networks'
import { isValidName } from '../../utils/validation'

import { GitFork, Plus, Trash2, Search, Edit, X, RefreshCw, Check, Copy } from 'lucide-vue-next'

const loadBalancers = ref<LoadBalancer[]>([])
const loading = ref(false)
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newLBForm = ref({
    name: '',
    description: '',
    vpc_id: '',
    zone: ''
})

const { t } = useI18n()
const toast = useToast()

const copiedId = ref<string | null>(null)
const copyId = (id: string) => {
    navigator.clipboard.writeText(id)
    copiedId.value = id
    setTimeout(() => { copiedId.value = null }, 2000)
}
const region = useRegionStore()
const isNameValid = computed(() => isValidName(newLBForm.value.name))

// --- Edit Modal ---
const editModalVisible = ref(false)
const editing = ref(false)
const editError = ref('')
const lbToEdit = ref<LoadBalancer | null>(null)
const editForm = ref({ name: '', description: '' })
const isEditValid = computed(() => isValidName(editForm.value.name))

const handleEditClick = (item: LoadBalancer) => {
    lbToEdit.value = item
    editForm.value = { name: item.name, description: item.description || '' }
    editError.value = ''
    editModalVisible.value = true
}
const closeEditModal = () => {
    editModalVisible.value = false
    lbToEdit.value = null
    editError.value = ''
}
const confirmEdit = async () => {
    if (!lbToEdit.value) return
    if (!isEditValid.value) {
        editError.value = t('messages.invalidHostname')
        return
    }
    editing.value = true
    editError.value = ''
    try {
        await loadBalancersApi.patch(lbToEdit.value.id, { name: editForm.value.name, description: editForm.value.description })
        await fetchLoadBalancers()
        closeEditModal()
        toast.success(t('messages.success'))
    } catch (error: any) {
        editError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        editing.value = false
    }
}

const vpcs = ref<VPC[]>([])
const router = useRouter()

const fetchLoadBalancers = async () => {
    loading.value = true
    try {
        const [lbResponse, vpcsResponse] = await Promise.all([
            loadBalancersApi.list(),
            vpcsApi.list()
        ])
        loadBalancers.value = lbResponse.load_balancers || []
        vpcs.value = vpcsResponse.vpcs || []
    } catch (err) {
        console.error('API fetch failed:', err)
        loadBalancers.value = []
        vpcs.value = []
    } finally {
        loading.value = false
    }
}

const openCreateModal = () => {
    newLBForm.value = { name: '', description: '', vpc_id: vpcs.value[0]?.id || '', zone: '' }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateLB = async () => {
    createError.value = ''
    if (!newLBForm.value.name || !newLBForm.value.vpc_id) return
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        const payload: LoadBalancerPayload = {
            name: newLBForm.value.name,
            vpc: { id: newLBForm.value.vpc_id }
        }
        if (newLBForm.value.description) payload.description = newLBForm.value.description
        if (newLBForm.value.zone) payload.zone = newLBForm.value.zone
        await loadBalancersApi.create(payload)
        
        // Refresh list
        const response = await loadBalancersApi.list()
        loadBalancers.value = response.load_balancers || []
        
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to create load balancer:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const filteredLoadBalancers = computed(() => {
    if (!searchQuery.value) return loadBalancers.value
    const query = searchQuery.value.toLowerCase()
    return loadBalancers.value.filter(lb => 
        lb.name.toLowerCase().includes(query) || 
        lb.id.toLowerCase().includes(query)
    )
})

const getStatusClass = (status: string) => {
    const map: Record<string, string> = {
        'active': 'status-running',
        'running': 'status-running',
        'available': 'status-running',
        'inactive': 'status-stopped',
        'stopped': 'status-stopped',
        'error': 'status-error',
        'failed': 'status-error',
    }
    return map[status?.toLowerCase()] || 'status-pending'
}

const formatListeners = (lb: LoadBalancer) => {
    if (!lb.listeners || lb.listeners.length === 0) return '-'
    return lb.listeners.map(l => `${l.port}/${l.mode.toUpperCase()}`).join(', ')
}

const navigateToDetail = (lb: LoadBalancer) => {
    router.push({ name: 'load-balancer-detail', params: { id: lb.id } })
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<LoadBalancer | null>(null)

const handleDeleteClick = (item: LoadBalancer) => {
    resourceToDelete.value = item
    deleteModalVisible.value = true
}
const closeDeleteModal = () => {
    deleteModalVisible.value = false
    resourceToDelete.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!resourceToDelete.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await loadBalancersApi.delete(resourceToDelete.value.id)
        await fetchLoadBalancers()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete load balancer:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchLoadBalancers()
    }
})

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchLoadBalancers()
    }
})
</script>

<template>
  <div>
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <Search :size="16" class="search-icon" />
          <input 
            type="text" 
            v-model="searchQuery"
            :placeholder="$t('actions.search') + '...'" 
            class="search-input"
          />
        </div>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchLoadBalancers" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createLoadBalancer') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.ipAddress') }}</th>
            <th>{{ $t('dashboard.table.vpc') }}</th>
            <th>{{ $t('dashboard.loadBalancerDetail.listeners') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="6" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredLoadBalancers.length === 0">
             <td colspan="6" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <GitFork :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p class="text-secondary">{{ $t('messages.noLoadBalancers') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="lb in filteredLoadBalancers" :key="lb.id">
            <td>
              <router-link :to="{ name: 'load-balancer-detail', params: { id: lb.id } }" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon">
                    <GitFork :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ lb.name }}</div>
                    <div class="resource-id-row">
                      <span class="resource-id" :title="lb.id">{{ lb.id.slice(0, 8) }}...</span>
                      <button class="copy-btn-mini" @click.stop.prevent="copyId(lb.id)" :title="t('actions.copy')">
                        <Check v-if="copiedId === lb.id" :size="10" style="color: #10b981;" />
                        <Copy v-else :size="10" />
                      </button>
                    </div>
                    <div v-if="lb.description" class="resource-desc">{{ lb.description }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <span :class="['badge', getStatusClass(lb.status || '')]">{{ lb.status }}</span>
            </td>
            <td>
              <code class="ip-address">{{ lb.floating_ips?.[0]?.fip_address || '-' }}</code>
            </td>
            <td>{{ lb.vpc?.name || '-' }}</td>
            <td class="text-secondary text-sm">
              {{ formatListeners(lb) }}
            </td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="handleEditClick(lb)">
                  <Edit :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm text-error" title="Delete" @click="handleDeleteClick(lb)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <!-- Create LB Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createLoadBalancer') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input 
              v-model="newLBForm.name" 
              type="text" 
              :class="['form-input', { 'input-error': !isNameValid }]" 
              :placeholder="$t('dashboard.forms.placeholder.lbNameExample')" 
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>

          </div>
          
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.description') }}（{{ $t('dashboard.forms.optional') }}）</label>
            <input v-model="newLBForm.description" type="text" class="form-input" :placeholder="$t('messages.placeholderDescription')" />
          </div>

          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.vpc') }}</label>
            <div class="select-wrapper">
                <select v-model="newLBForm.vpc_id" class="form-input">
                    <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">
                        {{ vpc.name }} ({{ vpc.id.slice(0, 8) }}...)
                    </option>
                </select>
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.zone') }}（{{ $t('dashboard.forms.optional') }}）</label>
            <input v-model="newLBForm.zone" type="text" class="form-input" placeholder="e.g. zone1" />
          </div>
        </div>
        <div class="modal-footer" style="flex-direction: column; align-items: stretch; gap: var(--spacing-2);">
          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>
          <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2);">
            <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreateLB" :disabled="creating">
              <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createLoadBalancer') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Edit Modal -->
    <div v-if="editModalVisible" class="modal-overlay" @click.self="closeEditModal">
      <div class="modal-content card" style="max-width: 460px;">
        <div class="modal-header">
          <h3>{{ $t('actions.edit') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeEditModal"><X :size="20" /></button>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input
              v-model="editForm.name"
              type="text"
              :class="['form-input', { 'input-error': !isEditValid }]"
              :placeholder="$t('dashboard.forms.placeholder.lbNameExample')"
              @keyup.enter="confirmEdit"
            />
            <div v-if="!isEditValid" class="text-error text-xs mt-1">{{ $t('messages.invalidHostname') }}</div>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
            <input
              v-model="editForm.description"
              type="text"
              class="form-input"
              :placeholder="$t('messages.placeholderDescription')"
            />
          </div>
        </div>
        <div v-if="editError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ editError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeEditModal" :disabled="editing">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="confirmEdit" :disabled="editing || !isEditValid">
            <span v-if="editing" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
            {{ editing ? $t('messages.saving') : $t('actions.save') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Delete Confirmation Modal -->
    <div v-if="deleteModalVisible" class="modal-overlay" @click.self="closeDeleteModal">
      <div class="modal-content card" style="max-width: 460px;">
        <div class="modal-header">
          <h3>{{ $t('actions.delete') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeDeleteModal"><X :size="20" /></button>
        </div>
        <div class="modal-body">
          <div style="text-align:center;padding:var(--spacing-4) 0">
            <div style="width:64px;height:64px;border-radius:50%;background:var(--error-light);display:flex;align-items:center;justify-content:center;margin:0 auto var(--spacing-4);color:var(--error-color)"><Trash2 :size="32" /></div>
            <p style="color:var(--text-secondary);margin:0 0 var(--spacing-4)">{{ $t('dashboard.deleteConfirm.message') }}</p>
            <div style="background:var(--bg-secondary);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:var(--spacing-3) var(--spacing-4);text-align:left">
              <span style="font-size:var(--font-size-xs);color:var(--text-tertiary);text-transform:uppercase;letter-spacing:0.05em;display:block;margin-bottom:var(--spacing-1)">{{ $t('dashboard.deleteConfirm.resource') }}</span>
              <span style="font-weight:var(--font-weight-semibold);display:block">{{ resourceToDelete?.name }}</span>
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ resourceToDelete?.id }}</span>
            </div>
            <div v-if="deleteError" class="text-error" style="margin-top:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
              {{ deleteError }}
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeDeleteModal" :disabled="deletingResource">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-danger" @click="confirmDelete" :disabled="deletingResource">
            <span v-if="deletingResource" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
            <Trash2 v-else :size="14" />
            {{ deletingResource ? $t('dashboard.deleteConfirm.deleting') : $t('actions.delete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.header-actions {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
}

.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

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

.search-icon {
  color: var(--gray-400);
}

.search-input {
  border: none;
  background: transparent;
  width: 100%;
  height: 100%;
  font-size: 0.875rem;
  color: var(--text-primary);
}

.search-input:focus {
  outline: none;
}

.table-card {
  padding: 0;
  overflow: hidden;
}

/* .resource-info etc. are global from index.css */

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

.actions {
  display: flex;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 20px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    transition: background var(--transition-base);
}
.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.resource-link {
  color: var(--primary-600);
  cursor: pointer;
}

.resource-link:hover {
  text-decoration: underline;
}

.resource-desc {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  margin-top: 2px;
}
</style>
