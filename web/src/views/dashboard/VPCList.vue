<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { vpcsApi, subnetsApi, type VPC, type SubnetPayload } from '../../api/networks'
import { useRegionStore } from '../../stores/region'
import { isValidName } from '../../utils/validation'

import { Layers, Plus, Trash2, Network, Search as SearchIcon, X, RefreshCw, Pencil, ChevronDown, HelpCircle } from 'lucide-vue-next'
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
    name: '',
    description: ''
})

const { t } = useI18n()
const toast = useToast()
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
    newVPCForm.value = { name: '', description: '' }
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
        toast.success(t('messages.createSuccess'))
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
        toast.success(t('messages.deleteSuccess'))
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

// --- Edit VPC Modal ---
const editModalVisible = ref(false)
const editingVPC = ref(false)
const editError = ref('')
const editTarget = ref<VPC | null>(null)
const editForm = ref({ name: '', description: '' })

const openEditModal = (vpc: VPC) => {
    editTarget.value = vpc
    editForm.value = { name: vpc.name, description: vpc.description || '' }
    editError.value = ''
    editModalVisible.value = true
}

const closeEditModal = () => {
    editModalVisible.value = false
    editTarget.value = null
    editError.value = ''
}

const handleEditVPC = async () => {
    editError.value = ''
    if (!editForm.value.name.trim()) {
        editError.value = t('messages.invalidHostname')
        return
    }
    if (!isValidName(editForm.value.name)) {
        editError.value = t('messages.invalidHostname')
        return
    }
    editingVPC.value = true
    try {
        await vpcsApi.patch(editTarget.value!.id, {
            name: editForm.value.name,
            description: editForm.value.description,
        })
        await fetchVPCs()
        closeEditModal()
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        editError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        editingVPC.value = false
    }
}

// --- Create Subnet Modal ---
const createSubnetVisible = ref(false)
const creatingSubnet = ref(false)
const createSubnetError = ref('')
const subnetTargetVPC = ref<VPC | null>(null)
const showAdvanced = ref(false)
const newSubnetForm = ref<SubnetPayload>({
    name: '',
    network_cidr: '',
    gateway: '',
    type: 'internal',
    dhcp: true,
    vpc: { id: '' },
    vlan: undefined,
    start_ip: '',
    end_ip: '',
    dns: '',
    base_domain: '',
})
const isSubnetNameValid = computed(() => isValidName(newSubnetForm.value.name))

const getStatusText = (status: string | undefined) => {
    return t(`dashboard.vpcStatus.${status?.toLowerCase() || 'active'}`)
}

const openCreateSubnetModal = (vpc: VPC) => {
    subnetTargetVPC.value = vpc
    newSubnetForm.value = {
        name: '',
        network_cidr: '',
        gateway: '',
        type: 'internal',
        dhcp: true,
        vpc: { id: vpc.id },
        vlan: undefined,
        start_ip: '',
        end_ip: '',
        dns: '',
        base_domain: '',
    }
    showAdvanced.value = false
    createSubnetError.value = ''
    createSubnetVisible.value = true
}

const closeCreateSubnetModal = () => {
    createSubnetVisible.value = false
    subnetTargetVPC.value = null
    createSubnetError.value = ''
}

const handleCreateSubnet = async () => {
    createSubnetError.value = ''
    if (!newSubnetForm.value.name || !newSubnetForm.value.network_cidr) {
        createSubnetError.value = 'Please fill in Name and CIDR.'
        return
    }
    if (!isSubnetNameValid.value) {
        createSubnetError.value = t('messages.invalidHostname')
        return
    }

    const payload: SubnetPayload = {
        name: newSubnetForm.value.name,
        network_cidr: newSubnetForm.value.network_cidr,
        type: 'internal',
        dhcp: newSubnetForm.value.dhcp,
        vpc: { id: subnetTargetVPC.value!.id },
    }
    if (newSubnetForm.value.gateway) payload.gateway = newSubnetForm.value.gateway
    if (newSubnetForm.value.start_ip) payload.start_ip = newSubnetForm.value.start_ip
    if (newSubnetForm.value.end_ip) payload.end_ip = newSubnetForm.value.end_ip
    if (newSubnetForm.value.dns) payload.dns = newSubnetForm.value.dns
    if (newSubnetForm.value.base_domain) payload.base_domain = newSubnetForm.value.base_domain
    if (newSubnetForm.value.vlan) payload.vlan = newSubnetForm.value.vlan

    creatingSubnet.value = true
    try {
        await subnetsApi.create(payload)
        await fetchVPCs()
        closeCreateSubnetModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        createSubnetError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creatingSubnet.value = false
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
                <div class="resource-info">
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
                {{ getStatusText(vpc.status) }}
              </span>
            </td>
            <td>
              <div class="subnets-column-wrapper" v-if="vpc.subnets && vpc.subnets.length > 0">
                <div class="subnet-list-vertical">
                  <div class="subnet-first-row">
                    <router-link 
                      :to="{ name: 'subnet-detail', params: { id: vpc.subnets[0].id } }" 
                      class="subnet-inline-item"
                    >
                      <span class="inline-name">{{ vpc.subnets[0].name }}</span>
                      <span class="inline-cidr">({{ vpc.subnets[0].network || vpc.subnets[0].network_cidr }})</span>
                    </router-link>
                    
                    <div v-if="vpc.subnets.length > 2" class="subnet-more-wrapper">
                      <button class="badge badge-multi-iface clickable">
                        <Network :size="10" />
                        +{{ vpc.subnets.length - 2 }} {{ $t('dashboard.instanceDetail.more').toLowerCase() }}
                      </button>
                      <div class="subnet-popover">
                        <div class="subnet-popover-header">{{ $t('dashboard.subnets') }}</div>
                        <router-link 
                          v-for="sub in vpc.subnets.slice(2)" 
                          :key="sub.id" 
                          :to="{ name: 'subnet-detail', params: { id: sub.id } }" 
                          class="subnet-popover-item"
                        >
                          <Network :size="12" />
                          <span class="subnet-popover-name">{{ sub.name }}</span>
                          <span class="subnet-popover-cidr">{{ sub.network || sub.network_cidr }}</span>
                        </router-link>
                      </div>
                    </div>
                  </div>
                  
                  <router-link 
                    v-if="vpc.subnets.length > 1"
                    :to="{ name: 'subnet-detail', params: { id: vpc.subnets[1].id } }" 
                    class="subnet-inline-item"
                  >
                    <span class="inline-name">{{ vpc.subnets[1].name }}</span>
                    <span class="inline-cidr">({{ vpc.subnets[1].network || vpc.subnets[1].network_cidr }})</span>
                  </router-link>
                </div>
              </div>
              <span v-else class="text-secondary">{{ $t('messages.noData') }}</span>
            </td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="openEditModal(vpc)">
                  <Pencil :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm" :title="$t('dashboard.buttons.createSubnet')" @click="openCreateSubnetModal(vpc)">
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
              :placeholder="$t('dashboard.forms.placeholder.vpcNameExample')" 
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>
          </div>
          
          <div class="form-group mt-4">
            <label class="form-label">{{ $t('dashboard.table.description') }}</label>
            <textarea 
              v-model="newVPCForm.description" 
              class="form-input" 
              rows="3"
              :placeholder="$t('messages.placeholderDescription')"
            ></textarea>
          </div>
        </div>
        <div class="modal-footer" style="flex-direction: column; align-items: stretch; gap: var(--spacing-2);">
          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>
          <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2);">
            <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreateVPC" :disabled="creating">
              <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createVpc') }}
            </button>
          </div>
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

    <!-- Edit VPC Modal -->
    <div v-if="editModalVisible" class="modal-overlay" @click.self="closeEditModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('actions.edit') }} - {{ editTarget?.name }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeEditModal">
            <X :size="20" />
          </button>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.name') }}</label>
            <input
              v-model="editForm.name"
              type="text"
              :class="['form-input', { 'input-error': editForm.name && !isValidName(editForm.name) }]"
              :placeholder="$t('dashboard.table.name')"
            />
            <div v-if="editForm.name && !isValidName(editForm.name)" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>
          </div>
          <div class="form-group mt-4">
            <label class="form-label">{{ $t('dashboard.table.description') }}</label>
            <textarea
              v-model="editForm.description"
              class="form-input"
              rows="3"
              :placeholder="$t('messages.placeholderDescription')"
            ></textarea>
          </div>
        </div>
        <div v-if="editError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ editError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeEditModal" :disabled="editingVPC">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleEditVPC" :disabled="editingVPC">
            <span v-if="editingVPC" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Create Subnet Modal -->
    <div v-if="createSubnetVisible" class="modal-overlay" @click.self="closeCreateSubnetModal">
      <div class="modal-content card" style="max-width: 600px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createSubnet') }} - {{ subnetTargetVPC?.name }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateSubnetModal">
            <X :size="20" />
          </button>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.name') }} *</label>
            <input
              v-model="newSubnetForm.name"
              type="text"
              :class="['form-input', { 'input-error': newSubnetForm.name && !isSubnetNameValid }]"
              :placeholder="$t('dashboard.forms.placeholder.subnetNameExample')"
            />
            <div v-if="newSubnetForm.name && !isSubnetNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>
          </div>

          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.cidr') }} *</label>
            <input
              v-model="newSubnetForm.network_cidr"
              type="text"
              class="form-input"
              :placeholder="$t('dashboard.forms.placeholder.cidrExample')"
            />
          </div>

          <div class="form-row">
            <div class="form-group flex-1">
              <label class="form-label">{{ $t('dashboard.forms.gateway') }}</label>
              <input
                v-model="newSubnetForm.gateway"
                type="text"
                class="form-input"
                :placeholder="$t('dashboard.forms.placeholder.gatewayExample')"
              />
            </div>
            <div class="form-group flex-1">
              <label class="form-label">
                {{ $t('dashboard.table.dhcp') }}
                <span class="tooltip-wrapper">
                  <HelpCircle :size="13" class="help-icon" />
                  <span class="tooltip-text">{{ $t('dashboard.forms.dhcpTooltip') }}</span>
                </span>
              </label>
              <div class="toggle-group">
                <label class="toggle-switch">
                  <input type="checkbox" v-model="newSubnetForm.dhcp">
                  <span class="toggle-slider"></span>
                </label>
                <span class="toggle-label">{{ newSubnetForm.dhcp ? $t('dashboard.alarmActions.enabled') : $t('dashboard.alarmActions.disabled') }}</span>
              </div>
            </div>
          </div>

          <div class="form-row">
            <div class="form-group flex-1">
              <label class="form-label">{{ $t('dashboard.forms.dns') }}</label>
              <input
                v-model="newSubnetForm.dns"
                type="text"
                class="form-input"
                :placeholder="$t('dashboard.forms.placeholder.dnsExample')"
              />
            </div>
            <div class="form-group flex-1">
              <label class="form-label">{{ $t('dashboard.forms.baseDomain') }}</label>
              <input
                v-model="newSubnetForm.base_domain"
                type="text"
                class="form-input"
                :placeholder="$t('dashboard.forms.placeholder.domainExample')"
              />
            </div>
          </div>

          <!-- Advanced Options -->
          <div class="form-section">
            <div class="form-section-title form-section-toggle" @click="showAdvanced = !showAdvanced">
              {{ $t('dashboard.forms.sections.advanced') }}
              <ChevronDown :size="14" :class="['chevron-icon', { 'chevron-open': showAdvanced }]" />
            </div>
            <div v-if="showAdvanced" class="form-section-body">
              <div class="form-row">
                <div class="form-group flex-1">
                  <label class="form-label">{{ $t('dashboard.forms.startIp') }}</label>
                  <input
                    v-model="newSubnetForm.start_ip"
                    type="text"
                    class="form-input"
                    :placeholder="$t('dashboard.forms.placeholder.ipExample')"
                  />
                </div>
                <div class="form-group flex-1">
                  <label class="form-label">{{ $t('dashboard.forms.endIp') }}</label>
                  <input
                    v-model="newSubnetForm.end_ip"
                    type="text"
                    class="form-input"
                    :placeholder="$t('dashboard.forms.placeholder.ipExample')"
                  />
                </div>
              </div>
              <div class="form-group">
                <label class="form-label">VXLAN</label>
                <input
                  v-model.number="newSubnetForm.vlan"
                  type="number"
                  class="form-input"
                  :placeholder="$t('dashboard.forms.placeholder.auto')"
                  min="1"
                  max="16777215"
                />
                <div class="form-hint">{{ $t('dashboard.vpcDetail.vlanHint') }}</div>
              </div>
            </div>
          </div>
        </div>

        <div v-if="createSubnetError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createSubnetError }}
        </div>

        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateSubnetModal" :disabled="creatingSubnet">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateSubnet" :disabled="creatingSubnet">
            <span v-if="creatingSubnet" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creatingSubnet ? $t('messages.creating') : $t('dashboard.buttons.createSubnet') }}
          </button>
        </div>
      </div>
    </div>
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
  overflow: visible;
}

.table-card td {
  overflow: visible;
  position: relative;
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

.subnet-list-vertical {
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: flex-start;
}

.subnet-first-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.badge-multi-iface {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  background-color: var(--primary-600);
  color: #fff;
  border: none;
  border-radius: 4px;
  font-size: 0.7rem;
  font-weight: 600;
  padding: 2px 6px;
  cursor: pointer;
  transition: background-color 0.2s;
}

.badge-multi-iface:hover {
  background-color: var(--primary-700);
}

.subnet-inline-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-xs);
  color: var(--primary-600);
  text-decoration: none;
  transition: all 0.2s;
  white-space: nowrap;
}

.inline-name {
  font-weight: var(--font-weight-medium);
}

.inline-cidr {
  color: var(--text-secondary);
  font-family: var(--font-mono, monospace);
  font-size: 0.9em;
  font-weight: 600;
}

.subnet-inline-item:hover {
  background: var(--primary-50);
  border-color: var(--primary-200);
  color: var(--primary-700);
}

.subnet-more-wrapper {
  position: relative;
  display: inline-block;
}

.subnet-popover {
  display: none;
  position: absolute;
  top: calc(100% + 8px);
  left: 0;
  z-index: 50;
  min-width: 260px;
  background: var(--bg-primary);
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  box-shadow: 0 10px 15px -3px rgba(0, 0, 0, 0.1), 0 4px 6px -2px rgba(0, 0, 0, 0.05);
  padding: 8px 0;
}

.subnet-more-wrapper:hover .subnet-popover {
  display: block;
}

.subnet-popover-header {
  padding: 4px 12px 8px;
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-semibold);
  color: var(--text-light);
  border-bottom: 1px solid var(--border-subtle);
  margin-bottom: 4px;
}

.subnet-popover-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
  text-decoration: none;
  transition: all 0.2s;
}

.subnet-popover-item:hover {
  background: var(--primary-50);
  color: var(--primary-color);
}

.subnet-popover-item:hover .subnet-popover-name {
  color: var(--primary-600);
  text-decoration: underline;
}

.subnet-popover-name {
  font-weight: var(--font-weight-medium);
  color: var(--primary-color);
}

.subnet-popover-cidr {
  color: var(--text-light);
  margin-left: auto;
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

/* Form elements for modals */
.form-row {
  display: flex;
  gap: var(--spacing-4);
}

.flex-1 { flex: 1; }

.form-hint {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  margin-top: 4px;
}

.form-section {
  margin-top: var(--spacing-3);
}

.form-section-title {
  font-size: var(--font-size-sm);
  font-weight: 600;
  color: var(--text-secondary);
  padding: var(--spacing-2) 0;
  border-bottom: 1px solid var(--border-light);
  margin-bottom: var(--spacing-3);
}

.form-section-toggle {
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: space-between;
  user-select: none;
}

.form-section-toggle:hover { color: var(--primary-color); }

.form-section-body { padding-top: var(--spacing-3); }

.chevron-icon { transition: transform 0.2s; }
.chevron-open { transform: rotate(180deg); }

/* Toggle switch */
.toggle-group { display: flex; align-items: center; gap: var(--spacing-2); margin-top: 4px; }

.toggle-switch {
  position: relative;
  display: inline-block;
  width: 36px;
  height: 20px;
  cursor: pointer;
}

.toggle-switch input { opacity: 0; width: 0; height: 0; }

.toggle-slider {
  position: absolute;
  inset: 0;
  background: var(--gray-300);
  border-radius: 20px;
  transition: background 0.2s;
}

.toggle-slider::before {
  content: '';
  position: absolute;
  width: 16px;
  height: 16px;
  left: 2px;
  bottom: 2px;
  background: white;
  border-radius: 50%;
  transition: transform 0.2s;
}

.toggle-switch input:checked + .toggle-slider { background: var(--primary-color); }
.toggle-switch input:checked + .toggle-slider::before { transform: translateX(16px); }

.toggle-label { font-size: var(--font-size-sm); color: var(--text-secondary); }

/* Tooltip */
.tooltip-wrapper { position: relative; display: inline-flex; cursor: help; }
.help-icon { color: var(--text-light); }

.tooltip-text {
  display: none;
  position: absolute;
  bottom: calc(100% + 6px);
  left: 50%;
  transform: translateX(-50%);
  background: var(--gray-800);
  color: white;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  font-size: var(--font-size-xs);
  white-space: nowrap;
  z-index: 20;
}

.tooltip-wrapper:hover .tooltip-text { display: block; }
</style>
