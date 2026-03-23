<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { subnetsApi, vpcsApi, type Subnet, type SubnetPayload, type VPC } from '../../api/networks'
import { isValidName } from '../../utils/validation'

import { Network, Plus, Trash2, Edit, Search, X, Globe, Cpu, Zap, RefreshCw, ChevronDown, HelpCircle } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const subnets = ref<Subnet[]>([])
const vpcs = ref<VPC[]>([])
const loading = ref(false)
const searchQuery = ref('')
const router = useRouter()

const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const showAdvanced = ref(false)
const newSubnetForm = ref<SubnetPayload>({
    name: '',
    network_cidr: '10.0.1.0/24',
    gateway: '10.0.1.1',
    type: 'internal',
    dhcp: true,
    vpc: { id: '' },
    vlan: undefined,
    start_ip: '',
    end_ip: '',
    dns: '',
    base_domain: '',
    priority: undefined,
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newSubnetForm.value.name))


const fetchSubnets = async () => {
    loading.value = true
    try {
        const response = await subnetsApi.list()
        subnets.value = response.subnets || []
    } catch (err) {
        console.error('API fetch failed:', err)
        subnets.value = []
    } finally {
        loading.value = false
    }
}

const fetchVpcs = async () => {
    try {
        const response = await vpcsApi.list()
        vpcs.value = response.vpcs || []
        if (vpcs.value.length > 0 && !newSubnetForm.value.vpc?.id) {
            newSubnetForm.value.vpc = { id: vpcs.value[0].id }
        }
    } catch (err) {
        console.error('Failed to fetch VPCs:', err)
    }
}

const openCreateModal = async () => {
    newSubnetForm.value = {
        name: '',
        network_cidr: '10.0.1.0/24',
        gateway: '10.0.1.1',
        type: 'internal',
        dhcp: true,
        vpc: { id: '' },
        vlan: undefined,
        start_ip: '',
        end_ip: '',
        dns: '',
        base_domain: '',
        priority: undefined,
    }
    showAdvanced.value = false
    await fetchVpcs()
    createModalVisible.value = true
}

const requiresVpc = computed(() => newSubnetForm.value.type === 'internal')

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateSubnet = async () => {
    createError.value = ''
    if (!newSubnetForm.value.name || !newSubnetForm.value.network_cidr) {
        createError.value = 'Please fill in Name and CIDR.'
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }
    if (requiresVpc.value && !newSubnetForm.value.vpc?.id) {
        createError.value = 'VPC is required for internal subnets.'
        return
    }

    // Build clean payload, omit empty optional fields
    const payload: SubnetPayload = {
        name: newSubnetForm.value.name,
        network_cidr: newSubnetForm.value.network_cidr,
        type: newSubnetForm.value.type,
        dhcp: newSubnetForm.value.dhcp,
    }
    if (newSubnetForm.value.gateway) payload.gateway = newSubnetForm.value.gateway
    if (newSubnetForm.value.vpc?.id) payload.vpc = newSubnetForm.value.vpc
    if (newSubnetForm.value.start_ip) payload.start_ip = newSubnetForm.value.start_ip
    if (newSubnetForm.value.end_ip) payload.end_ip = newSubnetForm.value.end_ip
    if (newSubnetForm.value.dns) payload.dns = newSubnetForm.value.dns
    if (newSubnetForm.value.base_domain) payload.base_domain = newSubnetForm.value.base_domain
    if (newSubnetForm.value.vlan) payload.vlan = newSubnetForm.value.vlan
    if (newSubnetForm.value.priority != null) payload.priority = newSubnetForm.value.priority

    creating.value = true
    try {
        await subnetsApi.create(payload)
        await fetchSubnets()
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create subnet:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const filteredSubnets = computed(() => {
    if (!searchQuery.value) return subnets.value
    const query = searchQuery.value.toLowerCase()
    return subnets.value.filter(subnet => 
        subnet.name.toLowerCase().includes(query) || 
        subnet.id.toLowerCase().includes(query)
    )
})

const getTypeClass = (type: string) => {
    const map: Record<string, string> = {
        'public': 'badge-success',
        'internal': 'badge-primary',
        'site': 'badge-warning'
    }
    return map[type] || 'badge-gray'
}

const navigateToDetail = (subnet: Subnet) => {
    router.push({ name: 'subnet-detail', params: { id: subnet.id } })
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<Subnet | null>(null)

const handleDeleteClick = (item: Subnet) => {
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
        await subnetsApi.delete(resourceToDelete.value.id)
        await fetchSubnets()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete subnet:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

onMounted(fetchSubnets)
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
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchSubnets" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createSubnet') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.cidr') }}</th>
            <th>{{ $t('dashboard.table.networkRange') }} / VLAN</th>
            <th>Usage (Alloc/Avail/Total)</th>
            <th>{{ $t('dashboard.table.vpc') }}</th>
            <th>{{ $t('dashboard.table.type') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredSubnets.length === 0">
            <td colspan="7" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <Network :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noSubnets') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="subnet in filteredSubnets" :key="subnet.id">
            <td>
              <div class="resource-name resource-link" @click="navigateToDetail(subnet)">{{ subnet.name }}</div>
              <div class="resource-id">{{ subnet.id }}</div>
            </td>
            <td>
              <div class="cidr-group">
                <code class="cidr">{{ subnet.network || subnet.network_cidr }}</code>
                <div class="text-xs text-light" style="margin-top:2px">{{ $t('dashboard.table.gateway') }}: {{ subnet.gateway || '-' }}</div>
              </div>
            </td>
            <td>
              <div class="range-info">
                 <div class="text-xs monospace">{{ subnet.start }} - {{ subnet.end }}</div>
                 <div class="vlan-info mt-1">
                    <span v-if="subnet.vlan">
                        <span class="badge badge-gray text-xs">
                           {{ (subnet.vlan > 4094) ? 'VXLAN' : 'VLAN' }}: {{ subnet.vlan }}
                        </span>
                    </span>
                 </div>
              </div>
            </td>
            <td>
              <div class="usage-stats">
                  <div class="text-xs">
                      <span class="text-primary font-bold">{{ subnet.allocated_count }}</span> / 
                      <span class="text-success">{{ subnet.available_count }}</span> / 
                      <span>{{ subnet.total_count }}</span>
                  </div>
                  <div class="usage-progress" style="width: 100px; height: 4px; background: var(--gray-100); border-radius: 2px; margin-top: 4px; overflow: hidden;">
                      <div :style="{ width: ((subnet.allocated_count || 0) / (subnet.total_count || 1) * 100) + '%', background: 'var(--primary-color)', height: '100%' }"></div>
                  </div>
              </div>
            </td>
            <td>
              <div v-if="subnet.vpc" class="vpc-cell">
                <router-link :to="{ name: 'vpc-detail', params: { id: subnet.vpc.id } }" class="vpc-link">
                  {{ subnet.vpc.name }}
                </router-link>
              </div>
              <span v-else class="text-light italic text-xs">{{ $t('dashboard.table.standalone') || 'Standalone' }}</span>
            </td>
            <td>
              <span :class="['badge', getTypeClass(subnet.type || '')]">
                {{ subnet.type || 'internal' }}
              </span>
            </td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" title="Edit">
                  <Edit :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm text-error" title="Delete" @click="handleDeleteClick(subnet)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Subnet Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card" style="max-width: 600px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createSubnet') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>

        <div class="modal-body">
          <!-- Section 1: Basic Info -->
          <div class="form-section">
            <div class="form-section-title">{{ $t('dashboard.forms.sections.general') }}</div>
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.table.name') }} *</label>
              <input
                v-model="newSubnetForm.name"
                type="text"
                :class="['form-input', { 'input-error': !isNameValid }]"
                placeholder="e.g. backend-subnet"
              />
              <div v-if="!isNameValid" class="text-error text-xs mt-1">
                {{ $t('messages.invalidHostname') }}
              </div>
            </div>

            <div class="form-row">
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.cidr') }} *</label>
                <input
                  v-model="newSubnetForm.network_cidr"
                  type="text"
                  class="form-input"
                  placeholder="e.g. 10.0.1.0/24"
                />
              </div>
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.table.type') }}</label>
                <select v-model="newSubnetForm.type" class="form-input">
                  <option value="internal">Internal</option>
                  <option value="public">Public</option>
                  <option value="site">Site</option>
                </select>
              </div>
            </div>

            <div class="form-group" v-if="requiresVpc">
              <label class="form-label">{{ $t('dashboard.forms.vpc') }} *</label>
              <select v-model="newSubnetForm.vpc!.id" class="form-input">
                <option value="" disabled>{{ $t('dashboard.forms.placeholder.selectVpc') }}</option>
                <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">
                  {{ vpc.name }} ({{ vpc.id }})
                </option>
              </select>
            </div>
          </div>

          <!-- Section 2: Network Configuration -->
          <div class="form-section">
            <div class="form-section-title">{{ $t('dashboard.forms.sections.networkConfig') }}</div>
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
          </div>

          <!-- Section 3: Advanced Options (collapsible) -->
          <div class="form-section">
            <div class="form-section-title form-section-toggle" @click="showAdvanced = !showAdvanced">
              {{ $t('dashboard.forms.sections.advanced') }}
              <ChevronDown :size="14" :class="['chevron-icon', { 'chevron-open': showAdvanced }]" />
            </div>
            <div v-if="showAdvanced" class="form-section-body">
              <div class="form-row">
                <div class="form-group flex-1">
                  <label class="form-label">{{ $t('dashboard.forms.vlan') }}</label>
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
                <div class="form-group flex-1">
                  <label class="form-label">{{ $t('dashboard.forms.priority') }}</label>
                  <input
                    v-model.number="newSubnetForm.priority"
                    type="number"
                    class="form-input"
                    placeholder="0"
                    min="0"
                    max="100000"
                  />
                  <div class="form-hint">0-100000, lower = higher priority</div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>

        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateSubnet" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createSubnet') }}
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
  margin-bottom: var(--spacing-06);
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

.resource-name {
  font-weight: var(--font-weight-medium);
  color: var(--primary-color);
}

.resource-link {
  color: var(--primary-600);
  cursor: pointer;
}

.resource-link:hover {
  text-decoration: underline;
}

.resource-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
}

.cidr, .ip {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-sm);
}

.vpc-name {
  color: var(--text-secondary);
  font-size: var(--font-size-sm);
}

.text-success {
  color: var(--success-color);
}

.actions {
  display: flex;
  gap: var(--spacing-02);
}

.cidr-group {
    display: flex;
    flex-direction: column;
}

.text-xs { font-size: 0.75rem; }
.text-light { color: var(--text-light); }
.text-primary { color: var(--primary-color); }
.text-success { color: var(--success-color); }
.font-bold { font-weight: 700; }
.monospace { font-family: var(--font-family-mono); }
.mt-1 { margin-top: 4px; }
.italic { font-style: italic; }

.vpc-link {
    color: var(--primary-600);
    text-decoration: none;
    font-weight: 500;
}

.vpc-link:hover {
    text-decoration: underline;
}

.usage-stats {
    display: flex;
    flex-direction: column;
}

.badge-gray {
    background: var(--gray-100);
    color: var(--gray-700);
}

.text-error {
  color: var(--error-color);
}

.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.header-actions { display: flex; gap: 8px; align-items: center; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.form-section {
  margin-bottom: var(--spacing-4);
}

.form-section:last-child {
  margin-bottom: 0;
}

.form-section-title {
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-semibold);
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: var(--spacing-3);
  padding-bottom: var(--spacing-2);
  border-bottom: 1px solid var(--border-light);
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

.chevron-icon {
  transition: transform 0.2s;
}

.chevron-open {
  transform: rotate(180deg);
}

.form-section-body {
  animation: fadeIn 0.2s ease-out;
  padding-top: var(--spacing-2);
}

.form-hint {
  font-size: 0.7rem;
  color: var(--text-light);
  margin-top: 4px;
}

.tooltip-wrapper {
  position: relative;
  display: inline-flex;
  align-items: center;
  margin-left: 4px;
  cursor: help;
}

.help-icon {
  color: var(--text-secondary);
  transition: color 0.15s;
}

.tooltip-wrapper:hover .help-icon {
  color: var(--primary-color);
}

.tooltip-text {
  visibility: hidden;
  opacity: 0;
  position: absolute;
  bottom: calc(100% + 8px);
  left: 0;
  background: var(--gray-900);
  color: #fff;
  font-size: 0.75rem;
  font-weight: normal;
  text-transform: none;
  letter-spacing: normal;
  line-height: 1.4;
  padding: 8px 12px;
  border-radius: var(--radius-md);
  white-space: normal;
  width: 260px;
  z-index: 10;
  transition: opacity 0.15s, visibility 0.15s;
  pointer-events: none;
  box-shadow: 0 4px 12px rgba(0,0,0,0.15);
}

.tooltip-text::after {
  content: '';
  position: absolute;
  top: 100%;
  left: 16px;
  border: 5px solid transparent;
  border-top-color: var(--gray-900);
}

.tooltip-wrapper:hover .tooltip-text {
  visibility: visible;
  opacity: 1;
}

@keyframes fadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}
</style>
