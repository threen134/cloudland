<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { securityGroupsApi, vpcsApi, type SecurityGroup, type SecurityRule, type VPC } from '../../api/networks'
import { isValidName } from '../../utils/validation'

import { Shield, Plus, Trash2, ChevronDown, ChevronRight, Search, X } from 'lucide-vue-next'

const securityGroups = ref<SecurityGroup[]>([])
const loading = ref(false)
const expandedGroups = ref<string[]>([])
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newGroupForm = ref({
    name: '',
    vpc_id: ''
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newGroupForm.value.name))

const vpcs = ref<VPC[]>([])
const router = useRouter()

const toggleGroup = (id: string) => {
    const index = expandedGroups.value.indexOf(id)
    if (index === -1) {
        expandedGroups.value.push(id)
    } else {
        expandedGroups.value.splice(index, 1)
    }
}

const isExpanded = (id: string) => expandedGroups.value.includes(id)

const fetchSecurityGroups = async () => {
    loading.value = true
    try {
        const [groupsResponse, vpcsResponse] = await Promise.all([
            securityGroupsApi.list(),
            vpcsApi.list()
        ])
        securityGroups.value = groupsResponse.security_groups || []
        vpcs.value = vpcsResponse.vpcs || []
    } catch (err) {
        console.error('API fetch failed:', err)
        securityGroups.value = []
        vpcs.value = []
    } finally {
        loading.value = false
    }
}

const openCreateModal = () => {
    newGroupForm.value = { name: '', vpc_id: vpcs.value[0]?.id || '' }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateGroup = async () => {
    createError.value = ''
    if (!newGroupForm.value.name || !newGroupForm.value.vpc_id) return
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        await securityGroupsApi.create({
            name: newGroupForm.value.name,
            vpc: { id: newGroupForm.value.vpc_id },
            is_default: false
        })
        
        // Refresh list
        const response = await securityGroupsApi.list()
        securityGroups.value = response.security_groups || []
        
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create security group:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const filteredSecurityGroups = computed(() => {
    if (!searchQuery.value) return securityGroups.value
    const query = searchQuery.value.toLowerCase()
    return securityGroups.value.filter(group => 
        group.name.toLowerCase().includes(query) || 
        group.id.toLowerCase().includes(query)
    )
})

const formatPort = (rule: SecurityRule) => {
    if (rule.port_min === rule.port_max) {
        return rule.port_min?.toString() || 'All'
    }
    if (rule.port_min === 1 && rule.port_max === 65535) {
        return 'All'
    }
    return `${rule.port_min}-${rule.port_max}`
}

const navigateToDetail = (group: SecurityGroup) => {
    router.push({ name: 'security-group-detail', params: { id: group.id } })
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const ruleToDelete = ref<{ groupId: string; rule: SecurityRule } | null>(null)

const handleDeleteClick = (groupId: string, rule: SecurityRule) => {
    ruleToDelete.value = { groupId, rule }
    deleteModalVisible.value = true
}
const closeDeleteModal = () => {
    deleteModalVisible.value = false
    ruleToDelete.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!ruleToDelete.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await securityGroupsApi.deleteRule(ruleToDelete.value.groupId, ruleToDelete.value.rule.id)
        await fetchSecurityGroups()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete security rule:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

onMounted(fetchSecurityGroups)
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
      <button class="btn btn-primary btn-sm" @click="openCreateModal">
        <Plus :size="14" /> {{ $t('dashboard.buttons.createSecurityGroup') }}
      </button>
    </div>

    <div v-if="loading" class="text-center" style="padding: 48px;">
      <div class="loading-spinner"></div>
    </div>

    <div v-else-if="filteredSecurityGroups.length === 0" class="text-center text-secondary" style="padding: 48px;">
       <div v-if="searchQuery">
          <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noResults') }}</p>
       </div>
       <div v-else>
          <Shield :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p class="text-secondary">{{ $t('messages.noData') }}</p>
       </div>
    </div>

    <div v-else class="security-groups-list">
      <div v-for="group in filteredSecurityGroups" :key="group.id" class="card sg-card">
        <div class="sg-header" @click="toggleGroup(group.id)">
          <div class="sg-info">
            <component :is="isExpanded(group.id) ? ChevronDown : ChevronRight" :size="16" class="expand-icon" />

            <div>
              <h3 class="sg-name">
                <span class="resource-link" @click.stop="navigateToDetail(group)">{{ group.name }}</span>
                <span v-if="group.is_default" class="badge badge-primary">{{ $t('dashboard.table.default') || 'Default' }}</span>
              </h3>
              <span class="sg-id">{{ group.id }} • {{ group.vpc?.name || $t('dashboard.org.noVpcs') || 'No VPC' }}</span>
            </div>
          </div>
          <div class="sg-stats">
            <span class="rule-count">{{ group.security_rules?.length || 0 }} {{ $t('dashboard.buttons.addRule').replace('Add ', '') }}s</span>
            <button class="btn btn-ghost btn-sm" @click.stop>
              <Plus :size="14" /> {{ $t('dashboard.buttons.addRule') }}
            </button>
          </div>
        </div>

        <div v-show="isExpanded(group.id)" class="sg-rules">
          <table class="rules-table">
            <thead>
              <tr>
                <th>{{ $t('dashboard.table.direction') }}</th>
                <th>{{ $t('dashboard.table.protocol') }}</th>
                <th>{{ $t('dashboard.table.ports') }}</th>
                <th>{{ $t('dashboard.table.remote') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!group.security_rules?.length">
                <td colspan="5" class="text-center text-secondary">{{ $t('messages.noData') }}</td>
              </tr>
              <tr v-else v-for="rule in group.security_rules" :key="rule.id">
                <td>
                  <span :class="['direction-badge', rule.direction]">
                    {{ rule.direction }}
                  </span>
                </td>
                <td class="protocol">{{ rule.protocol.toUpperCase() }}</td>
                <td class="port">{{ formatPort(rule) }}</td>
                <td class="cidr">{{ rule.remote_cidr || $t('dashboard.forms.placeholder.none') }}</td>
                <td>
                  <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(group.id, rule)">
                    <Trash2 :size="14" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>


    <!-- Create Security Group Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createSecurityGroup') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input 
              v-model="newGroupForm.name" 
              type="text" 
              :class="['form-input', { 'input-error': !isNameValid }]" 
              placeholder="e.g. web-servers" 
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>

          </div>
          
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.vpc') }}</label>
            <div class="select-wrapper">
                <select v-model="newGroupForm.vpc_id" class="form-input">
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
          <button class="btn btn-primary" @click="handleCreateGroup" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createSecurityGroup') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Delete Rule Confirmation Modal -->
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
              <span style="font-weight:var(--font-weight-semibold);display:block">{{ ruleToDelete?.rule.protocol?.toUpperCase() }} : {{ ruleToDelete?.rule.port_min }}-{{ ruleToDelete?.rule.port_max }}</span>
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ ruleToDelete?.rule.id }}</span>
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

.security-groups-list {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-4);
}

.sg-card {
  padding: 0;
  overflow: hidden;
}

.sg-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--spacing-4) var(--spacing-5);
  cursor: pointer;
  transition: background var(--transition-fast);
}

.sg-header:hover {
  background: var(--hover-ui);
}

.sg-info {
  display: flex;
  align-items: center;
  gap: 10px;
}

.expand-icon {
  color: var(--text-light);
}

.sg-name {
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
  font-size: var(--font-size-base);
  font-weight: var(--font-weight-medium);
  margin-bottom: 0;
}

.sg-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
}

.sg-stats {
  display: flex;
  align-items: center;
  gap: var(--spacing-4);
}

.rule-count {
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
}

.sg-rules {
  border-top: 1px solid var(--border-subtle);
  background: var(--gray-50);
  padding-left: 20px;
}

.rules-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-size-sm);
}

.rules-table th {
  text-align: left;
  padding: var(--spacing-3) var(--spacing-5);
  font-weight: var(--font-weight-medium);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  background: var(--gray-100);
}

.rules-table td {
  padding: var(--spacing-3) var(--spacing-5);
  border-bottom: 1px solid var(--border-subtle);
}

.rules-table tr:last-child td {
  border-bottom: none;
}

.direction-badge {
  display: inline-block;
  padding: var(--spacing-1) var(--spacing-2);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  text-transform: uppercase;
}

.direction-badge.ingress {
  background: var(--success-light);
  color: var(--success-dark);
}

.direction-badge.egress {
  background: var(--info-light);
  color: var(--info-dark);
}

.protocol {
  font-family: var(--font-family-mono);
  font-weight: var(--font-weight-medium);
}

.port {
  font-family: var(--font-family-mono);
}

.cidr {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
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
