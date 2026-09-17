<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useSecurityGroup } from '../../composables/useSecurityGroup'
import { securityGroupsApi, vpcsApi, type SecurityGroup, type VPC } from '../../api/networks'
import { isValidName } from '../../utils/validation'
import { useRegionStore } from '../../stores/region'

import { Shield, Plus, Trash2, Search, X, RefreshCw, Edit, HelpCircle, Check, Copy } from 'lucide-vue-next'

const region = useRegionStore()
const { translateDescription } = useSecurityGroup()
const { t } = useI18n()
const toast = useToast()
const { copiedId, copyId } = useCopyId()

const securityGroups = ref<SecurityGroup[]>([])
const vpcs = ref<VPC[]>([])
const loading = ref(false)
const searchQuery = ref('')
const vpcFilter = ref('')

// Pagination, name search and the VPC filter are done by the server
const currentPage = ref(1)
const pageSize = 20
const totalCount = ref(0)
const totalPages = computed(() => Math.max(1, Math.ceil(totalCount.value / pageSize)))
// Drops responses of superseded requests (fast typing, page switches)
let fetchGeneration = 0

const fetchSecurityGroups = async () => {
    const generation = ++fetchGeneration
    loading.value = true
    try {
        const response = await securityGroupsApi.list({
            offset: (currentPage.value - 1) * pageSize,
            limit: pageSize,
            query: searchQuery.value.trim() || undefined,
            vpc_id: vpcFilter.value || undefined
        })
        if (generation !== fetchGeneration) return
        securityGroups.value = response.security_groups || []
        totalCount.value = response.total || 0
        // The current page became empty (e.g. its last item was deleted): step back
        if (securityGroups.value.length === 0 && currentPage.value > 1) {
            currentPage.value = totalPages.value
            await fetchSecurityGroups()
        }
    } catch (err) {
        if (generation !== fetchGeneration) return
        console.error('API fetch failed:', err)
        securityGroups.value = []
        totalCount.value = 0
    } finally {
        if (generation === fetchGeneration) loading.value = false
    }
}

const fetchVpcs = async () => {
    try {
        const response = await vpcsApi.list()
        vpcs.value = response.vpcs || []
    } catch (err) {
        console.error('Failed to fetch VPCs:', err)
        vpcs.value = []
    }
}

const refresh = () => {
    fetchSecurityGroups()
    fetchVpcs()
}

const goToPage = (page: number) => {
    if (page < 1 || page > totalPages.value || page === currentPage.value) return
    currentPage.value = page
    fetchSecurityGroups()
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
const onSearchInput = () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
        currentPage.value = 1
        fetchSecurityGroups()
    }, 400)
}

const onVpcFilterChange = () => {
    currentPage.value = 1
    fetchSecurityGroups()
}

// The API returns "2006-01-02 15:04:05.999999": fractional seconds are noise in a list
const formatCreatedAt = (value?: string) => value ? value.replace(/\.\d+$/, '') : '-'

const ruleCount = (group: SecurityGroup, direction: 'ingress' | 'egress') =>
    (group.security_rules || []).filter(r => r.direction === direction).length

// --- Create Modal ---
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newGroupForm = ref({ name: '', description: '', vpc_id: '', is_default: false })
const isNameValid = computed(() => isValidName(newGroupForm.value.name))

const openCreateModal = () => {
    newGroupForm.value = { name: '', description: '', vpc_id: vpcFilter.value, is_default: false }
    createError.value = ''
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateGroup = async () => {
    createError.value = ''
    if (!newGroupForm.value.name) return
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }
    creating.value = true
    try {
        await securityGroupsApi.create({
            name: newGroupForm.value.name,
            description: newGroupForm.value.description,
            ...(newGroupForm.value.vpc_id ? { vpc: { id: newGroupForm.value.vpc_id } } : {}),
            is_default: newGroupForm.value.vpc_id ? newGroupForm.value.is_default : false
        })
        // The list is sorted newest first: show the first page
        currentPage.value = 1
        await fetchSecurityGroups()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to create security group:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

// --- Edit Modal ---
const editModalVisible = ref(false)
const editing = ref(false)
const editError = ref('')
const groupToEdit = ref<SecurityGroup | null>(null)
const editForm = ref({ name: '', description: '' })
const isEditValid = computed(() => !!editForm.value.name.trim() && isValidName(editForm.value.name))

const handleEditClick = (group: SecurityGroup) => {
    groupToEdit.value = group
    editForm.value = { name: group.name, description: group.description || '' }
    editError.value = ''
    editModalVisible.value = true
}

const closeEditModal = () => {
    editModalVisible.value = false
    groupToEdit.value = null
    editError.value = ''
}

const confirmEdit = async () => {
    if (!groupToEdit.value) return
    if (!isEditValid.value) {
        editError.value = t('messages.invalidHostname')
        return
    }
    editing.value = true
    editError.value = ''
    try {
        await securityGroupsApi.patch(groupToEdit.value.id, { name: editForm.value.name, description: editForm.value.description })
        await fetchSecurityGroups()
        closeEditModal()
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to update security group:', err)
        editError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        editing.value = false
    }
}

// --- Delete Modal ---
const deleteModalVisible = ref(false)
const deleting = ref(false)
const deleteError = ref('')
const groupToDelete = ref<SecurityGroup | null>(null)

const handleDeleteClick = (group: SecurityGroup) => {
    groupToDelete.value = group
    deleteError.value = ''
    deleteModalVisible.value = true
}

const closeDeleteModal = () => {
    deleteModalVisible.value = false
    groupToDelete.value = null
    deleteError.value = ''
}

const confirmDelete = async () => {
    if (!groupToDelete.value) return
    deleting.value = true
    deleteError.value = ''
    try {
        await securityGroupsApi.delete(groupToDelete.value.id)
        await fetchSecurityGroups()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (err: any) {
        console.error('Failed to delete security group:', err)
        deleteError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        deleting.value = false
    }
}

const loadPage = () => {
    currentPage.value = 1
    vpcFilter.value = ''
    refresh()
}

onMounted(() => {
    if (region.currentRegionId) loadPage()
})

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) loadPage()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div class="filters">
        <div class="search-box">
          <Search :size="16" class="search-icon" />
          <input
            type="text"
            v-model="searchQuery"
            @input="onSearchInput"
            :placeholder="$t('actions.search') + '...'"
            class="search-input"
          />
        </div>
        <div class="select-wrapper vpc-filter">
          <select v-model="vpcFilter" class="form-input" @change="onVpcFilterChange">
            <option value="">{{ $t('dashboard.table.vpc') }}: {{ $t('dashboard.forms.placeholder.all') }}</option>
            <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">{{ vpc.name }}</option>
          </select>
        </div>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="refresh" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createSecurityGroup') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <div class="table-responsive">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ $t('dashboard.table.nameId') }}</th>
              <th>{{ $t('dashboard.table.vpc') }}</th>
              <th>{{ $t('dashboard.table.securityRules') }}</th>
              <th>{{ $t('dashboard.securityGroupDetail.associatedInterfaces') }}</th>
              <th>{{ $t('dashboard.table.createdAt') }}</th>
              <th>{{ $t('dashboard.table.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading && securityGroups.length === 0">
              <td colspan="6" class="text-center">
                <div class="loading-spinner" style="margin: 20px auto;"></div>
              </td>
            </tr>
            <tr v-else-if="securityGroups.length === 0">
              <td colspan="6" class="text-center text-secondary" style="padding: 48px;">
                <div v-if="searchQuery || vpcFilter">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                  <Shield :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p class="text-secondary">{{ $t('messages.noSecurityGroups') }}</p>
                </div>
              </td>
            </tr>
            <tr v-else v-for="group in securityGroups" :key="group.id">
              <td>
                <router-link :to="{ name: 'security-group-detail', params: { id: group.id } }" class="resource-link">
                  <div class="resource-info">
                    <div class="resource-icon">
                      <Shield :size="16" />
                    </div>
                    <div>
                      <div class="name-row">
                        <span class="resource-name">{{ group.name }}</span>
                        <span v-if="group.is_default" class="badge badge-primary">{{ $t('dashboard.table.default') }}</span>
                      </div>
                      <div class="resource-id-row">
                        <span class="resource-id" :title="group.id">{{ group.id.slice(0, 8) }}...</span>
                        <button class="copy-btn-mini" @click.stop.prevent="copyId(group.id)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                          <Check v-if="copiedId === group.id" :size="10" style="color: #10b981;" />
                          <Copy v-else :size="10" />
                        </button>
                      </div>
                      <div v-if="group.description" class="resource-desc">{{ translateDescription(group.description) }}</div>
                    </div>
                  </div>
                </router-link>
              </td>
              <td>
                <router-link v-if="group.vpc" :to="{ name: 'vpc-detail', params: { id: group.vpc.id } }" class="text-link">
                  {{ group.vpc.name }}
                </router-link>
                <span v-else class="text-secondary">-</span>
              </td>
              <td class="rule-counts">
                <span class="direction-badge ingress">{{ $t('dashboard.table.ingress') }} {{ ruleCount(group, 'ingress') }}</span>
                <span class="direction-badge egress">{{ $t('dashboard.table.egress') }} {{ ruleCount(group, 'egress') }}</span>
              </td>
              <td>{{ group.target_interfaces?.length || 0 }}</td>
              <td class="text-secondary text-sm nowrap">{{ formatCreatedAt(group.created_at) }}</td>
              <td>
                <div class="actions">
                  <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="handleEditClick(group)">
                    <Edit :size="14" />
                  </button>
                  <button
                    class="btn btn-ghost btn-sm text-error"
                    :title="group.is_default ? $t('dashboard.securityGroupDetail.defaultNotDeletable') : $t('actions.delete')"
                    :disabled="group.is_default"
                    @click="handleDeleteClick(group)"
                  >
                    <Trash2 :size="14" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
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
              :placeholder="$t('dashboard.forms.placeholder.sgNameExample')"
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>
          </div>

          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.description') }} <span class="text-optional">({{ $t('dashboard.forms.optional') }})</span></label>
            <input
              v-model="newGroupForm.description"
              type="text"
              class="form-input"
              :placeholder="$t('messages.placeholderDescription')"
            />
          </div>

          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.vpc') }} <span class="text-optional">({{ $t('dashboard.forms.optional') }})</span></label>
            <div class="select-wrapper">
              <select v-model="newGroupForm.vpc_id" class="form-input">
                <option value="">{{ $t('dashboard.forms.placeholder.none') }}</option>
                <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">
                  {{ vpc.name }} ({{ vpc.id.slice(0, 8) }}...)
                </option>
              </select>
            </div>
          </div>

          <div v-if="newGroupForm.vpc_id" class="form-group form-group-checkbox">
            <label class="checkbox-label">
              <input type="checkbox" v-model="newGroupForm.is_default" class="checkbox-input" />
              <span>{{ $t('dashboard.forms.setAsDefault') }}</span>
              <span class="help-icon-wrap">
                <HelpCircle :size="14" class="help-icon" />
                <span class="help-tooltip">{{ $t('dashboard.forms.setAsDefaultTooltip') }}</span>
              </span>
            </label>
          </div>
        </div>
        <div class="modal-footer" style="flex-direction: column; align-items: stretch; gap: var(--spacing-2);">
          <div v-if="createError" class="error-box">{{ createError }}</div>
          <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2);">
            <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreateGroup" :disabled="creating || !newGroupForm.name || !isNameValid">
              <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createSecurityGroup') }}
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
              :placeholder="$t('dashboard.forms.placeholder.sgNameExample')"
              @keyup.enter="confirmEdit"
            />
            <div v-if="!isEditValid" class="text-error text-xs mt-1">{{ $t('messages.invalidHostname') }}</div>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
            <input v-model="editForm.description" type="text" class="form-input" :placeholder="$t('messages.placeholderDescription')" />
          </div>
          <div v-if="editError" class="error-box">{{ editError }}</div>
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
            <div style="width:64px;height:64px;border-radius:50%;background:var(--error-light);display:flex;align-items:center;justify-content:center;margin:0 auto var(--spacing-4);color:var(--error-color)"><Shield :size="32" /></div>
            <p style="color:var(--text-secondary);margin:0 0 var(--spacing-4)">{{ $t('dashboard.securityGroupDetail.deleteConfirm') }}</p>
            <div style="background:var(--bg-secondary);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:var(--spacing-3) var(--spacing-4);text-align:left">
              <span style="font-size:var(--font-size-xs);color:var(--text-tertiary);text-transform:uppercase;letter-spacing:0.05em;display:block;margin-bottom:var(--spacing-1)">{{ $t('dashboard.deleteConfirm.resource') }}</span>
              <span style="font-weight:var(--font-weight-semibold);display:block">{{ groupToDelete?.name }}</span>
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ groupToDelete?.id }}</span>
            </div>
            <div v-if="deleteError" class="error-box" style="margin-top:var(--spacing-4)">{{ deleteError }}</div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeDeleteModal" :disabled="deleting">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-danger" @click="confirmDelete" :disabled="deleting">
            <span v-if="deleting" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
            <Trash2 v-else :size="14" />
            {{ deleting ? $t('dashboard.deleteConfirm.deleting') : $t('actions.delete') }}
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
  gap: var(--spacing-3);
  flex-wrap: wrap;
  margin-bottom: 0;
  padding-right: 20px;
}

.filters {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  flex: 1;
  flex-wrap: wrap;
}

.search-box {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: 1;
  max-width: 400px;
  min-width: 200px;
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

.vpc-filter {
  width: 200px;
}

.vpc-filter .form-input {
  height: 40px;
  padding-top: 0;
  padding-bottom: 0;
}

.data-table th,
.nowrap {
  white-space: nowrap;
}

.table-card {
  padding: 0;
  overflow: hidden;
}

.table-responsive {
  overflow-x: auto;
}

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

/* .resource-info etc. are global from index.css */

.resource-link {
  text-decoration: none;
  display: block;
  padding: 4px 0;
  border-radius: var(--radius-sm);
  transition: all 0.15s;
  color: var(--primary-600);
  cursor: pointer;
}

.resource-link:hover .resource-name {
  color: var(--primary-600);
  text-decoration: underline;
}

.name-row {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
}

.resource-desc {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  margin-top: 2px;
}

.text-link { color: var(--primary-600); text-decoration: none; }
.text-link:hover { text-decoration: underline; }

.rule-counts {
  white-space: nowrap;
}

.rule-counts .direction-badge + .direction-badge {
  margin-left: var(--spacing-1);
}

.direction-badge {
  display: inline-block;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}

.direction-badge.ingress { background: var(--success-light); color: var(--success-dark); }
.direction-badge.egress { background: var(--info-light); color: var(--info-dark); }

.actions {
  display: flex;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

.error-box {
  color: var(--error-color);
  font-size: var(--font-size-sm);
  background: var(--error-light);
  padding: var(--spacing-2);
  border-radius: var(--radius-sm);
}

.input-error {
  border-color: var(--error-color) !important;
  box-shadow: 0 0 0 3px var(--error-light) !important;
}

.text-optional {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  font-weight: 400;
}

.form-group-checkbox {
  padding-top: var(--spacing-1);
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  cursor: pointer;
  font-size: var(--font-size-sm);
  color: var(--text-primary);
}

.checkbox-input {
  width: 16px;
  height: 16px;
  cursor: pointer;
  accent-color: var(--primary);
}

.help-icon-wrap {
  position: relative;
  display: inline-flex;
  align-items: center;
  margin-left: 2px;
}

.help-icon {
  color: var(--text-tertiary);
  cursor: default;
  flex-shrink: 0;
}

.help-tooltip {
  display: none;
  position: absolute;
  bottom: calc(100% + 6px);
  left: 50%;
  transform: translateX(-50%);
  background: var(--gray-800, #1f2937);
  color: #fff;
  font-size: var(--font-size-xs);
  font-weight: 400;
  line-height: 1.5;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  white-space: normal;
  width: 220px;
  text-align: left;
  pointer-events: none;
  z-index: 100;
  box-shadow: 0 2px 8px rgba(0,0,0,0.18);
}

.help-tooltip::after {
  content: '';
  position: absolute;
  top: 100%;
  left: 50%;
  transform: translateX(-50%);
  border: 5px solid transparent;
  border-top-color: var(--gray-800, #1f2937);
}

.help-icon-wrap:hover .help-tooltip {
  display: block;
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
</style>
