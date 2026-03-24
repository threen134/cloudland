<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { vpcsApi, type VPC } from '../../api/networks'
import { useRegionStore } from '../../stores/region'
import { isValidName } from '../../utils/validation'

import { Layers, Plus, Trash2, Network, Search as SearchIcon, X, RefreshCw } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const region = useRegionStore()

const vpcs = ref<VPC[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newVPCForm = ref({
    name: ''
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newVPCForm.value.name))


const fetchVPCs = async () => {
    loading.value = true
    error.value = null
    try {
        const response = await vpcsApi.list()
        vpcs.value = response.vpcs || (Array.isArray(response) ? response : [])
    } catch (err: any) {
        console.error('Failed to fetch VPCs:', err)
        error.value = err.message
        vpcs.value = []
    } finally {
        loading.value = false
    }
}

watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchVPCs()
    }
})

const filteredVPCs = computed(() => {
    if (!searchQuery.value) return vpcs.value
    const query = searchQuery.value.toLowerCase()
    return vpcs.value.filter(vpc => 
        vpc.name.toLowerCase().includes(query) || 
        vpc.id.toLowerCase().includes(query)
    )
})

const openCreateModal = () => {
    newVPCForm.value = { name: '' }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateVPC = async () => {
    createError.value = ''
    if (!newVPCForm.value.name) {
        createError.value = 'Please enter a VPC name.'
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        await vpcsApi.create(newVPCForm.value)
        await fetchVPCs()
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create VPC:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<VPC | null>(null)

const handleDeleteClick = (item: VPC) => {
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
        await vpcsApi.delete(resourceToDelete.value.id)
        await fetchVPCs()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete VPC:', error)
        if (error.response?.data?.error_code === 131307 || error.response?.data?.error_code_str === 'RouterHasFloatingIPs') {
            deleteError.value = t('messages.vpcHasFloatingIPs')
        } else {
            deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
        }
    } finally {
        deletingResource.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchVPCs()
    }
})
</script>

<template>
  <div class="vpc-list-container">
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <SearchIcon :size="16" class="search-icon" />
          <input 
            type="text" 
            v-model="searchQuery"
            :placeholder="$t('actions.search') + '...'" 
            class="search-input"
          />
        </div>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchVPCs" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createVpc') }}
        </button>
      </div>
    </div>
    
    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.subnets') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="4" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredVPCs.length === 0">
            <td colspan="4" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else class="empty-state">
                <Layers :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                <p>{{ $t('messages.noVpcs') }}</p>
              </div>
            </td>
          </tr>
          <tr v-else v-for="vpc in filteredVPCs" :key="vpc.id">
            <td>
              <router-link :to="{ name: 'vpc-detail', params: { id: vpc.id } }" class="resource-link">
                <div class="vpc-info">
                  <div class="resource-icon">
                    <Layers :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ vpc.name }}</div>
                    <div class="resource-id">{{ vpc.id }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <span class="status-pill status-active">
                <span class="status-dot"></span>
                {{ vpc.status || 'Active' }}
              </span>
            </td>
            <td>
              <div class="subnet-badges">
                <div v-if="vpc.subnets && vpc.subnets.length > 0">
                  <span v-for="sub in vpc.subnets" :key="sub.id" class="subnet-badge" :title="sub.network || sub.network_cidr">
                    <Network :size="10" />
                    {{ sub.name }}
                  </span>
                </div>
                <span v-else class="text-secondary">{{ $t('messages.noData') }}</span>
              </div>
            </td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" :title="$t('dashboard.buttons.addRule')">
                  <Plus :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(vpc)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create VPC Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createVpc') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <div class="form-group">
             <label class="form-label">{{ $t('dashboard.table.name') }}</label>
            <input 
              v-model="newVPCForm.name" 
              type="text" 
              :class="['form-input', { 'input-error': !isNameValid }]" 
              placeholder="e.g. production-vpc" 
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>

          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateVPC" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createVpc') }}
          </button>
        </div>
      </div>
    </div>

    <DeleteModal
      :show="deleteModalVisible"
      :resource-name="resourceToDelete?.name"
      :resource-id="resourceToDelete?.id"
      :loading="deletingResource"
      :error="deleteError"
      @close="closeDeleteModal"
      @confirm="confirmDelete"
    />
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

.vpc-info {
  display: flex;
  align-items: center;
  gap: var(--spacing-03);
}

.resource-icon {
  width: 32px;
  height: 32px;
  background: var(--primary-light);
  color: var(--primary-color);
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
}

.resource-name {
  font-weight: var(--font-weight-semibold);
  color: var(--text-main);
  font-size: var(--font-size-sm);
}

.resource-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  font-family: var(--font-family-mono);
}

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
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}

.status-dot {
  width: 6px;
  height: 6px;
  background: currentColor;
  border-radius: 50%;
}

.subnet-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.subnet-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  background: var(--gray-10);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
}

.actions {
  display: flex;
  gap: var(--spacing-02);
}

.text-error {
  color: var(--error-color);
}

.mt-6 {
  margin-top: 24px;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}

.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.header-actions { display: flex; gap: 8px; align-items: center; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
