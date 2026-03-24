<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { vpcsApi, subnetsApi, type VPC, type SubnetPayload } from '../../api/networks'
import { isValidName } from '../../utils/validation'
import { useRegionStore } from '../../stores/region'
import { ArrowLeft, Layers, Network, Trash2, Plus, Copy, Check, Pencil, ChevronDown, CalendarDays, ShieldAlert, X, HelpCircle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const route = useRoute()
const router = useRouter()
const region = useRegionStore()
const { t } = useI18n()

const vpc = ref<VPC | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)
const showActionMenu = ref(false)

// --- Toast Notification ---
const toast = ref<{ message: string, type: 'success' | 'error' } | null>(null)
let toastTimer: ReturnType<typeof setTimeout> | null = null

const showToast = (message: string, type: 'success' | 'error' = 'success') => {
    if (toastTimer) clearTimeout(toastTimer)
    toast.value = { message, type }
    toastTimer = setTimeout(() => { toast.value = null }, 3000)
}

// --- Delete Confirmation Modal Logic ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')

const handleDeleteClick = () => {
    deleteModalVisible.value = true
}

const closeDeleteModal = () => {
    deleteModalVisible.value = false
    deleteError.value = ''
}

const confirmDelete = async () => {
    if (!vpc.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await vpcsApi.delete(vpc.value.id)
        router.push({ name: 'vpcs' })
    } catch (err: any) {
        console.error('Failed to delete VPC:', err)
        if (err.response?.data?.error_code === 131307 || err.response?.data?.error_code_str === 'RouterHasFloatingIPs') {
            deleteError.value = t('messages.vpcHasFloatingIPs')
        } else {
            deleteError.value = err.response?.data?.error_message || err.message || t('messages.error')
        }
    } finally {
        deletingResource.value = false
    }
}

// --- Edit Modal Logic ---
const showEditModal = ref(false)
const editForm = ref({ name: '', description: '' })
const editLoading = ref(false)
const editError = ref('')

const openEditModal = () => {
    editForm.value = {
        name: vpc.value?.name || '',
        description: vpc.value?.description || '',
    }
    editError.value = ''
    showEditModal.value = true
}

const confirmEdit = async () => {
    if (!editForm.value.name.trim()) {
        editError.value = t('dashboard.instanceDetail.hostnameRequired')
        return
    }
    editLoading.value = true
    editError.value = ''
    try {
        await vpcsApi.patch(vpc.value!.id, {
            name: editForm.value.name,
            description: editForm.value.description
        })
        showEditModal.value = false
        showToast(t('messages.updateSuccess'))
        await fetchVPC()
    } catch (err: any) {
        editError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        editLoading.value = false
    }
}

// --- Create Subnet Modal Logic ---
const createSubnetVisible = ref(false)
const creatingSubnet = ref(false)
const createSubnetError = ref('')
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

const openCreateSubnetModal = () => {
    newSubnetForm.value = {
        name: '',
        network_cidr: '',
        gateway: '',
        type: 'internal',
        dhcp: true,
        vpc: { id: vpc.value?.id || '' },
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
        vpc: { id: vpc.value!.id },
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
        closeCreateSubnetModal()
        showToast(t('messages.createSuccess'))
        await fetchVPC()
    } catch (err: any) {
        console.error('Failed to create subnet:', err)
        createSubnetError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creatingSubnet.value = false
    }
}

const fetchVPC = async () => {
    const id = route.params.id as string
    if (!id) return

    loading.value = true
    error.value = null
    try {
        const response = await vpcsApi.get(id)
        vpc.value = response
    } catch (err: any) {
        console.error('Failed to fetch VPC:', err)
        error.value = err.message || 'Failed to load VPC details'
    } finally {
        loading.value = false
    }
}

watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchVPC()
    }
})

const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}

const closeActionMenu = () => {
    showActionMenu.value = false
}

const goBack = () => {
    router.back()
}

const getStatusClass = (status?: string) => {
    const statusMap: Record<string, string> = {
        'active': 'status-running',
        'available': 'status-running',
        'creating': 'status-pending',
        'deleting': 'status-pending',
        'error': 'status-error'
    }
    return statusMap[status || ''] || 'status-running'
}

const formatDate = (dateStr?: string) => {
    if (!dateStr) return '-'
    const d = new Date(dateStr)
    return d.toLocaleString()
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchVPC()
    }
})
</script>

<template>
  <div class="detail-page">
    <!-- Toast Notification -->
    <Transition name="toast">
        <div v-if="toast" :class="['toast', 'toast-' + toast.type]" @click="toast = null">
            <Check v-if="toast.type === 'success'" :size="16" />
            <ShieldAlert v-else :size="16" />
            {{ toast.message }}
        </div>
    </Transition>

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
      <Layers :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchVPC" style="margin-top: 12px;">
        {{ $t('actions.refresh') }}
      </button>
    </div>

    <!-- VPC Detail Content -->
    <div v-else-if="vpc" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <Layers :size="28" />
          </div>
          <div>
            <h2 class="resource-title">
              {{ vpc.name }}
              <span :class="['badge', getStatusClass(vpc.status)]">{{ vpc.status || 'Active' }}</span>
            </h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ vpc.id }}</span>
              <button class="copy-btn" @click="copyToClipboard(vpc.id, 'id')" :title="$t('messages.copied')">
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
                        <button class="dropdown-item" @click="openEditModal">
                            <Pencil :size="14" /> {{ $t('actions.edit') }}
                        </button>
                        <button class="dropdown-item" @click="openCreateSubnetModal">
                            <Plus :size="14" /> {{ $t('dashboard.buttons.createSubnet') }}
                        </button>
                        <div class="dropdown-divider"></div>
                        <button class="dropdown-item dropdown-item-danger" @click="handleDeleteClick">
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
                <span class="label">{{ $t('dashboard.table.name') }}</span>
                <span class="value">{{ vpc.name }}</span>
              </div>
              <div class="kv-item">
                <span class="label">{{ $t('dashboard.table.status') }}</span>
                <span class="value">
                  <span :class="['status-badge', getStatusClass(vpc.status)]">{{ vpc.status || 'Active' }}</span>
                </span>
              </div>
              <div class="kv-item">
                <span class="label">{{ $t('dashboard.table.description') }}</span>
                <span class="value">{{ vpc.description || '-' }}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="col-stack">
          <!-- Metadata Card -->
          <div class="card info-card">
            <h3>{{ t('dashboard.table.metadata') }}</h3>
            <div class="key-value-list">
              <div class="kv-item">
                <span class="label">{{ t('dashboard.subnets') }}</span>
                <span class="value">{{ vpc.subnets?.length || 0 }}</span>
              </div>
              <div class="kv-item">
                <span class="label">{{ $t('dashboard.table.owner') }}</span>
                <span class="value">{{ vpc.owner || '-' }}</span>
              </div>
              <div class="kv-item">
                <span class="label"><CalendarDays :size="14" /> {{ $t('dashboard.table.created') }}</span>
                <span class="value">{{ formatDate(vpc.created_at) }}</span>
              </div>
              <div class="kv-item">
                <span class="label"><CalendarDays :size="14" /> {{ $t('dashboard.table.updatedAt') }}</span>
                <span class="value">{{ formatDate(vpc.updated_at) }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Subnets Section -->
      <div class="subnets-section card">
        <div class="subnets-header">
          <h3>
            {{ $t('dashboard.subnets') }}
            <span class="subnet-count">{{ vpc.subnets?.length || 0 }}</span>
          </h3>
        </div>

        <div v-if="vpc.subnets && vpc.subnets.length > 0" class="subnet-table-wrap">
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ $t('dashboard.table.name') }}</th>
                <th>{{ $t('dashboard.table.cidr') }}</th>
                <th>{{ $t('dashboard.table.gateway') }}</th>
                <th>{{ $t('dashboard.table.type') }}</th>
                <th>{{ $t('dashboard.table.network') }}</th>
                <th>{{ $t('dashboard.table.dhcp') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="subnet in vpc.subnets" :key="subnet.id">
                <td>
                  <div class="text-primary font-medium">{{ subnet.name }}</div>
                  <div class="text-light mono" style="font-size: 11px;">{{ subnet.id }}</div>
                </td>
                <td><span class="mono">{{ (subnet.network || subnet.network_cidr) || '-' }}</span></td>
                <td><span class="mono">{{ subnet.gateway || '-' }}</span></td>
                <td>
                  <span class="badge badge-secondary">{{ subnet.type || 'internal' }}</span>
                </td>
                <td>{{ subnet.vlan ?? '-' }}</td>
                <td>
                  <span :class="['badge', subnet.dhcp ? 'status-running' : 'status-stopped']">
                    {{ subnet.dhcp ? 'ON' : 'OFF' }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty-subnets">
          <Network :size="36" style="opacity: 0.2; margin-bottom: 12px;" />
          <p class="text-secondary">{{ $t('messages.noData') }}</p>
        </div>
      </div>

      <!-- Modals -->
      <DeleteModal
        :show="deleteModalVisible"
        :resource-name="vpc?.name"
        :resource-id="vpc?.id"
        :loading="deletingResource"
        :error="deleteError"
        @close="closeDeleteModal"
        @confirm="confirmDelete"
      />

      <!-- Edit Modal -->
      <div v-if="showEditModal" class="modal-backdrop">
        <div class="modal-content card shadow-lg">
          <div class="modal-header">
            <h3>{{ t('actions.edit') }}</h3>
            <button class="btn-close" @click="showEditModal = false">&times;</button>
          </div>
          <div class="modal-body">
            <div class="form-group">
              <label>{{ t('dashboard.table.name') }}</label>
              <input v-model="editForm.name" type="text" class="form-input" :placeholder="t('dashboard.table.name')" />
            </div>
            <div class="form-group">
              <label>{{ t('dashboard.table.description') }}</label>
              <textarea v-model="editForm.description" class="form-input" rows="3" :placeholder="t('dashboard.table.description')"></textarea>
            </div>
            <p v-if="editError" class="text-error small">{{ editError }}</p>
          </div>
          <div class="modal-footer">
            <button class="btn btn-ghost" @click="showEditModal = false" :disabled="editLoading">{{ t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="confirmEdit" :disabled="editLoading">
              <span v-if="editLoading" class="loading-spinner small"></span>
              {{ t('actions.confirm') }}
            </button>
          </div>
        </div>
      </div>

      <!-- Create Subnet Modal -->
      <div v-if="createSubnetVisible" class="modal-backdrop" @click.self="closeCreateSubnetModal">
        <div class="modal-content card shadow-lg" style="max-width: 600px;">
          <div class="modal-header">
            <h3>{{ $t('dashboard.buttons.createSubnet') }}</h3>
            <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateSubnetModal">
              <X :size="20" />
            </button>
          </div>

          <div class="modal-body">
            <!-- Network Configuration -->
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.table.name') }} *</label>
              <input
                v-model="newSubnetForm.name"
                type="text"
                :class="['form-input', { 'input-error': newSubnetForm.name && !isSubnetNameValid }]"
                placeholder="e.g. backend-subnet"
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
                placeholder="e.g. 10.0.1.0/24"
              />
            </div>

            <div class="form-row">
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.gateway') }}</label>
                <input
                  v-model="newSubnetForm.gateway"
                  type="text"
                  class="form-input"
                  placeholder="e.g. 10.0.1.1"
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
                  placeholder="e.g. 8.8.8.8"
                />
              </div>
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.baseDomain') }}</label>
                <input
                  v-model="newSubnetForm.base_domain"
                  type="text"
                  class="form-input"
                  placeholder="e.g. example.com"
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
                      placeholder="e.g. 10.0.1.2"
                    />
                  </div>
                  <div class="form-group flex-1">
                    <label class="form-label">{{ $t('dashboard.forms.endIp') }}</label>
                    <input
                      v-model="newSubnetForm.end_ip"
                      type="text"
                      class="form-input"
                      placeholder="e.g. 10.0.1.254"
                    />
                  </div>
                </div>
                <div class="form-group">
                  <label class="form-label">VXLAN</label>
                  <input
                    v-model.number="newSubnetForm.vlan"
                    type="number"
                    class="form-input"
                    placeholder="Auto"
                    min="1"
                    max="16777215"
                  />
                  <div class="form-hint">1-16777215, auto-generated if empty</div>
                </div>
              </div>
            </div>
          </div>

          <div v-if="createSubnetError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createSubnetError }}
          </div>

          <div class="modal-footer">
            <button class="btn btn-ghost" @click="closeCreateSubnetModal" :disabled="creatingSubnet">{{ $t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreateSubnet" :disabled="creatingSubnet">
              <span v-if="creatingSubnet" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creatingSubnet ? $t('messages.creating') : $t('dashboard.buttons.createSubnet') }}
            </button>
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

.resource-title {
  margin: 0 0 4px 0;
  font-size: var(--font-size-xl);
  font-weight: var(--font-weight-semibold);
  color: var(--primary-color);
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
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

/* Subnets Section */
.subnets-section {
  padding: 0;
  overflow: hidden;
}

.subnets-header {
  padding: var(--spacing-4) var(--spacing-5);
  border-bottom: 1px solid var(--border-light);
}

.subnets-header h3 {
    margin: 0;
    font-size: var(--font-size-md);
    font-weight: 600;
}

.subnet-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  padding: 0 6px;
  border-radius: var(--radius-full);
  background: var(--primary-100);
  color: var(--primary-color);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-semibold);
  margin-left: var(--spacing-2);
}

.subnet-table-wrap {
  overflow-x: auto;
}

.empty-subnets {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px 20px;
  text-align: center;
}

/* Status Badges */
.status-badge {
    display: inline-flex;
    padding: 2px 10px;
    border-radius: 12px;
    font-size: var(--font-size-xs);
    font-weight: 500;
}

/* Toast */
.toast {
    position: fixed;
    top: 24px;
    left: 50%;
    transform: translateX(-50%);
    padding: 12px 24px;
    border-radius: var(--radius-md);
    background: #333;
    color: white;
    display: flex;
    align-items: center;
    gap: 8px;
    z-index: 1000;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
}

.toast-success { background: var(--success-600); }
.toast-error { background: var(--error-600); }

.toast-enter-active, .toast-leave-active { transition: all 0.3s ease; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translate(-50%, -20px); }

/* Modal */
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}

.modal-content {
  width: 100%;
  max-width: 500px;
  background: var(--bg-primary);
}

.modal-header {
  padding: var(--spacing-4) var(--spacing-5);
  border-bottom: 1px solid var(--border-light);
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.modal-body {
  padding: var(--spacing-5);
}

.modal-footer {
  padding: var(--spacing-4) var(--spacing-5);
  border-top: 1px solid var(--border-light);
  display: flex;
  justify-content: flex-end;
  gap: var(--spacing-3);
}

.form-group {
  margin-bottom: var(--spacing-4);
}

.form-group label {
  display: block;
  margin-bottom: 6px;
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
}

.form-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  font-size: var(--font-size-sm);
}

.btn-close {
  background: none;
  border: none;
  font-size: 24px;
  cursor: pointer;
  color: var(--text-light);
}

/* Form elements for create subnet modal */
.form-row {
  display: flex;
  gap: var(--spacing-4);
}

.flex-1 {
  flex: 1;
}

.form-label {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 6px;
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
}

.form-hint {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  margin-top: 4px;
}

.input-error {
  border-color: var(--error-color) !important;
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

.form-section-toggle:hover {
  color: var(--primary-color);
}

.form-section-body {
  padding-top: var(--spacing-3);
}

.chevron-icon {
  transition: transform 0.2s;
}

.chevron-open {
  transform: rotate(180deg);
}

/* Toggle switch */
.toggle-group {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  margin-top: 4px;
}

.toggle-switch {
  position: relative;
  display: inline-block;
  width: 36px;
  height: 20px;
  cursor: pointer;
}

.toggle-switch input {
  opacity: 0;
  width: 0;
  height: 0;
}

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

.toggle-switch input:checked + .toggle-slider {
  background: var(--primary-color);
}

.toggle-switch input:checked + .toggle-slider::before {
  transform: translateX(16px);
}

.toggle-label {
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
}

/* Tooltip */
.tooltip-wrapper {
  position: relative;
  display: inline-flex;
  cursor: help;
}

.help-icon {
  color: var(--text-light);
}

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

.tooltip-wrapper:hover .tooltip-text {
  display: block;
}

.icon-btn {
  padding: 4px;
}

/* Badge colors */
.status-running { background: var(--success-50); color: var(--success-700); }
.status-stopped { background: var(--gray-100); color: var(--gray-700); }
.status-pending { background: var(--warning-50); color: var(--warning-700); }
.status-error { background: var(--error-50); color: var(--error-700); }

/* Responsive */
@media (max-width: 768px) {
  .two-col-layout {
    grid-template-columns: 1fr;
  }
}
</style>
