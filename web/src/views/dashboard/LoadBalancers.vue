<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { loadBalancersApi, vpcsApi, type LoadBalancer, type VPC } from '../../api/networks'
import { isValidName } from '../../utils/validation'

import { GitFork, Plus, Trash2, Search, Edit, X, RefreshCw } from 'lucide-vue-next'

const loadBalancers = ref<LoadBalancer[]>([])
const loading = ref(false)
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newLBForm = ref({
    name: '',
    vpc_id: ''
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newLBForm.value.name))

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
    newLBForm.value = { name: '', vpc_id: vpcs.value[0]?.id || '' }
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
        await loadBalancersApi.create({
            name: newLBForm.value.name,
            vpc: { id: newLBForm.value.vpc_id }
        })
        
        // Refresh list
        const response = await loadBalancersApi.list()
        loadBalancers.value = response.load_balancers || []
        
        closeCreateModal()
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
        'inactive': 'status-stopped',
        'error': 'status-error'
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
    } catch (error: any) {
        console.error('Failed to delete load balancer:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

onMounted(fetchLoadBalancers)
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
            <th>{{ $t('dashboard.table.listener') }}s</th>
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
              <div class="lb-name resource-link" @click="navigateToDetail(lb)">{{ lb.name }}</div>
              <div class="lb-id">{{ lb.id }}</div>
            </td>
            <td>
              <span :class="['badge', getStatusClass(lb.status || '')]">{{ lb.status }}</span>
            </td>
            <td>
              <code class="ip-address">{{ lb.floating_ips?.[0]?.ip_address || '-' }}</code>
            </td>
            <td>{{ lb.vpc?.name || '-' }}</td>
            <td class="text-secondary text-sm">
              {{ formatListeners(lb) }}
            </td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')">
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
              placeholder="e.g. web-lb-01" 
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>

          </div>
          
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.vpc') }}</label>
            <div class="select-wrapper">
                <select v-model="newLBForm.vpc_id" class="form-input">
                    <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">
                        {{ vpc.name }} ({{ vpc.id }})
                    </option>
                </select>
            </div>
          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateLB" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createLoadBalancer') }}
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

.lb-name {
  font-weight: var(--font-weight-medium);
  color: var(--primary-color);
}

.lb-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
}

.ip-address {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-sm);
}

.actions {
  display: flex;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  backdrop-filter: blur(4px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  animation: fadeIn 0.2s ease-out;
}

.modal-content {
  width: 100%;
  max-width: 500px;
  background: var(--bg-primary);
  border: 1px solid var(--border-light);
  box-shadow: var(--shadow-xl);
  animation: slideUp 0.3s cubic-bezier(0.16, 1, 0.3, 1);
  display: flex;
  flex-direction: column;
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-4);
  padding-bottom: var(--spacing-4);
  border-bottom: 1px solid var(--border-light);
}

.modal-header h3 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.modal-body {
  margin-bottom: var(--spacing-6);
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--spacing-3);
  padding-top: var(--spacing-4);
  border-top: 1px solid var(--border-light);
}

.icon-btn {
  padding: 4px;
}

@keyframes fadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}

@keyframes slideUp {
  from { transform: translateY(20px); opacity: 0; }
  to { transform: translateY(0); opacity: 1; }
}

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
</style>
