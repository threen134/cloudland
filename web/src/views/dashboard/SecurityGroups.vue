<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useSecurityGroup } from '../../composables/useSecurityGroup'
import { securityGroupsApi, vpcsApi, type SecurityGroup, type VPC } from '../../api/networks'
import { isValidName } from '../../utils/validation'
import { useRegionStore } from '../../stores/region'

import { Shield, Plus, Trash2, Search, RefreshCw, Edit, HelpCircle, Check, Copy } from 'lucide-vue-next'
import PageToolbar from '../../components/base/PageToolbar.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const region = useRegionStore()
const { translateDescription } = useSecurityGroup()
const { t } = useI18n()
const toast = useToast()
const { copiedId, copyId } = useCopyId()

const securityGroups = ref<SecurityGroup[]>([])
const vpcs = ref<VPC[]>([])
const loading = ref(false)
const loadError = ref('')
const searchQuery = ref('')
const vpcFilter = ref('')

// Pagination, name search and the VPC filter are done by the server
const currentPage = ref(1)
const pageSize = 20
const totalCount = ref(0)
const totalPages = computed(() => Math.max(1, Math.ceil(totalCount.value / pageSize)))
// Drops responses of superseded requests (fast typing, page switches)
let fetchGeneration = 0

// 分页、搜索、VPC 过滤都在服务端做，前端排序只能排当前页，所以这些列不开放排序。
// 描述列吃掉剩余宽度（其余列按内容宽度）
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId') },
    { key: 'description', label: t('dashboard.table.description'), width: '100%' },
    { key: 'vpc', label: t('dashboard.table.vpc') },
    { key: 'rules', label: t('dashboard.table.securityRules') },
    { key: 'interfaces', label: t('dashboard.securityGroupDetail.associatedInterfaces'), align: 'center' },
    { key: 'created', label: t('dashboard.table.createdAt') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'right' },
])

const fetchSecurityGroups = async () => {
    const generation = ++fetchGeneration
    loading.value = true
    loadError.value = ''
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
        loadError.value = t('messages.error')
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
// 搜索框现在由 PageToolbar 渲染，改用 watch 触发原来的输入防抖
watch(searchQuery, onSearchInput)

const onVpcFilterChange = () => {
    currentPage.value = 1
    fetchSecurityGroups()
}

// The API returns "2006-01-02 15:04:05.999999": minutes are precise enough for a list
const formatCreatedAt = (value?: string) => value ? value.slice(0, 16) : '-'

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

// 搜索防抖定时器：组件卸载后不应再触发请求
onUnmounted(() => {
    if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
  <div>
    <PageToolbar v-model:search="searchQuery">
      <template #filters>
        <div class="select-wrapper vpc-filter">
          <select v-model="vpcFilter" class="form-input" @change="onVpcFilterChange">
            <option value="">{{ $t('dashboard.table.vpc') }}: {{ $t('dashboard.forms.placeholder.all') }}</option>
            <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">{{ vpc.name }}</option>
          </select>
        </div>
      </template>
      <template #actions>
        <button class="btn btn-secondary btn-sm btn-icon" @click="refresh" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createSecurityGroup') }}
        </button>
      </template>
    </PageToolbar>

    <DataTable
      :columns="columns"
      :rows="securityGroups"
      row-key="id"
      :loading="loading"
      :error="loadError"
      @retry="fetchSecurityGroups"
    >
      <template #empty>
        <div v-if="searchQuery || vpcFilter">
          <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noResults') }}</p>
        </div>
        <div v-else>
          <Shield :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p class="text-secondary">{{ $t('messages.noSecurityGroups') }}</p>
        </div>
      </template>

      <template #cell-name="{ row: group }">
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
            </div>
          </div>
        </router-link>
      </template>

      <template #cell-description="{ row: group }">
        <div v-if="group.description" class="desc-text" :title="translateDescription(group.description)">{{ translateDescription(group.description) }}</div>
        <span v-else class="text-light">-</span>
      </template>

      <template #cell-vpc="{ row: group }">
        <router-link v-if="group.vpc" :to="{ name: 'vpc-detail', params: { id: group.vpc.id } }" class="text-link nowrap">
          {{ group.vpc.name }}
        </router-link>
        <span v-else class="text-secondary">-</span>
      </template>

      <template #cell-rules="{ row: group }">
        <div class="rule-counts">
          <span class="direction-badge ingress">{{ $t('dashboard.table.ingress') }} {{ ruleCount(group as any, 'ingress') }}</span>
          <span class="direction-badge egress">{{ $t('dashboard.table.egress') }} {{ ruleCount(group as any, 'egress') }}</span>
        </div>
      </template>

      <template #cell-interfaces="{ row: group }">{{ group.target_interfaces?.length || 0 }}</template>

      <template #cell-created="{ row: group }">
        <span class="text-secondary text-sm nowrap" :title="group.created_at">{{ formatCreatedAt(group.created_at) }}</span>
      </template>

      <template #cell-actions="{ row: group }">
        <div class="actions">
          <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="handleEditClick(group as any)">
            <Edit :size="14" />
          </button>
          <button
            class="btn btn-ghost btn-sm text-error"
            :title="group.is_default ? $t('dashboard.securityGroupDetail.defaultNotDeletable') : $t('actions.delete')"
            :disabled="group.is_default"
            @click="handleDeleteClick(group as any)"
          >
            <Trash2 :size="14" />
          </button>
        </div>
      </template>

      <template #footer>
        <PaginationBar :page="currentPage" :page-size="pageSize" :total="totalCount" @update:page="goToPage" />
      </template>
    </DataTable>

    <!-- Create Security Group Modal -->
    <BaseModal
      :show="createModalVisible"
      :title="$t('dashboard.buttons.createSecurityGroup')"
      :loading="creating"
      form
      @close="closeCreateModal"
      @submit="handleCreateGroup"
    >
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

      <div v-if="createError" class="error-box">{{ createError }}</div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="creating || !newGroupForm.name || !isNameValid">
          <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createSecurityGroup') }}
        </button>
      </template>
    </BaseModal>

    <!-- Edit Modal -->
    <BaseModal
      :show="editModalVisible"
      :title="$t('actions.edit')"
      :loading="editing"
      form
      @close="closeEditModal"
      @submit="confirmEdit"
    >
      <div class="form-group">
        <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
        <input
          v-model="editForm.name"
          type="text"
          :class="['form-input', { 'input-error': !isEditValid }]"
          :placeholder="$t('dashboard.forms.placeholder.sgNameExample')"
        />
        <div v-if="!isEditValid" class="text-error text-xs mt-1">{{ $t('messages.invalidHostname') }}</div>
      </div>
      <div class="form-group">
        <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
        <input v-model="editForm.description" type="text" class="form-input" :placeholder="$t('messages.placeholderDescription')" />
      </div>
      <div v-if="editError" class="error-box">{{ editError }}</div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeEditModal" :disabled="editing">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="editing || !isEditValid">
          <span v-if="editing" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
          {{ editing ? $t('messages.saving') : $t('actions.save') }}
        </button>
      </template>
    </BaseModal>

    <!-- Delete Confirmation Modal -->
    <DeleteModal
      :show="deleteModalVisible"
      :message="$t('dashboard.securityGroupDetail.deleteConfirm')"
      :resource-name="groupToDelete?.name"
      :resource-id="groupToDelete?.id"
      :loading="deleting"
      :error="deleteError"
      @close="closeDeleteModal"
      @confirm="confirmDelete"
    />
  </div>
</template>

<style scoped>
.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.vpc-filter {
  width: 200px;
}

.vpc-filter .form-input {
  height: 40px;
  padding-top: 0;
  padding-bottom: 0;
}

.nowrap {
  white-space: nowrap;
}

/* .resource-info etc. are global from index.css */

/* 省略号原先靠 td 的 max-width: 1px 实现，表格移入 DataTable 后那条规则失效了。
   改成限制单元格内容自身的宽度，长描述才不会把表格撑宽 */
.desc-text {
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
}

.text-light {
  color: var(--text-light);
}

.resource-link {
  text-decoration: none;
  display: inline-block;
  padding: 0;
  border-radius: var(--radius-sm);
  transition: all 0.15s;
  color: var(--primary-600);
  cursor: pointer;
}

.resource-link .resource-info {
  gap: var(--spacing-3);
}

.resource-link .resource-icon {
  width: 28px;
  height: 28px;
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
  justify-content: flex-end;
  gap: 2px;
}

.actions .btn {
  padding-left: 6px;
  padding-right: 6px;
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
  accent-color: var(--primary-color);
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
</style>
