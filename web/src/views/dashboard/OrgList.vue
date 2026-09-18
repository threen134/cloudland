<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { orgsApi, type Organization } from '../../api/orgs'
import { QUOTA_ROWS, type OrgResourceQuotaUpdate } from '../../api/quota'
import { useAuthStore } from '../../stores/auth'
import { useQuota } from '../../composables/useQuota'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import {
    Plus, Building2, Trash2, Edit2, User, Search, Gauge, RefreshCw, Check, Copy,
    CheckCircle, AlertCircle, ShieldAlert, PauseCircle, PlayCircle
} from 'lucide-vue-next'
import PageToolbar from '../../components/base/PageToolbar.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'

const authStore = useAuthStore()
const isSuperuser = computed(() => authStore.user?.is_superuser === true)

const { t } = useI18n()
const toast = useToast()

const { copiedId, copyId } = useCopyId()

const orgs = ref<Organization[]>([])
const loading = ref(false)
const loadError = ref('')
const searchQuery = ref('')
const createModalVisible = ref(false)
const editing = ref(false)
const creating = ref(false)
const editModalVisible = ref(false)
const createError = ref('')
const editError = ref('')
const newOrgForm = ref({
    name: '',
    description: ''
})
const editOrgForm = ref({
    uuid: '',
    name: '',
    description: ''
})

// 状态列显示的是翻译后的文案，排序按原始状态码；属主列优先按显示的名字排
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'description', label: t('dashboard.table.description'), sortable: true, sortValue: (o) => o.description || '' },
    { key: 'status', label: t('dashboard.table.status'), sortable: true, sortValue: (o) => o.status ?? 0 },
    { key: 'owner', label: t('dashboard.table.owner'), sortable: true, sortValue: (o) => o.owner_name || o.owner_email || o.owner_uuid || '' },
    { key: 'created', label: t('dashboard.table.created'), sortable: true, sortValue: (o) => o.created_at || '' },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const fetchOrgs = async () => {
    loading.value = true
    loadError.value = ''
    try {
        const data = await orgsApi.fetchOrgs() as any
        orgs.value = Array.isArray(data) ? data : (data.orgs || [])
    } catch (error) {
        console.error('Failed to fetch orgs:', error)
        loadError.value = t('messages.error')
    } finally {
        loading.value = false
    }
}

const filteredOrgs = computed(() => {
    if (!searchQuery.value) return orgs.value
    const query = searchQuery.value.toLowerCase()
    return orgs.value.filter(org => 
        (org.name?.toLowerCase() || '').includes(query) || 
        (org.uuid?.toLowerCase() || '').includes(query)
    )
})

const openCreateModal = () => {
    newOrgForm.value = { name: '', description: '' }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateOrg = async () => {
    createError.value = ''
    if (!newOrgForm.value.name) {
        createError.value = t('dashboard.org.enterName')
        return
    }

    creating.value = true
    try {
        await orgsApi.createOrg(newOrgForm.value)
        await fetchOrgs()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to create organization:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const openEditModal = (org: Organization) => {
    editOrgForm.value = {
        uuid: org.uuid,
        name: org.name,
        description: org.description || ''
    }
    editModalVisible.value = true
}

const closeEditModal = () => {
    editModalVisible.value = false
    editError.value = ''
}

const handleEditOrg = async () => {
    editError.value = ''
    if (!editOrgForm.value.name) {
        editError.value = t('dashboard.org.enterName')
        return
    }

    editing.value = true
    try {
        await orgsApi.updateOrg(editOrgForm.value.uuid, {
            name: editOrgForm.value.name,
            description: editOrgForm.value.description
        })
        await fetchOrgs()
        closeEditModal()
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to update organization:', err)
        editError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        editing.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<Organization | null>(null)

const handleDeleteClick = (item: Organization) => {
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
        await orgsApi.deleteOrg(resourceToDelete.value.uuid)
        await fetchOrgs()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete organization:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

// --- Quota Modal ---
const quotaModalVisible = ref(false)
const quotaOrgId = ref('')
const quotaOrgName = ref('')
const {
    quotaSummary,
    quotaLoading,
    quotaError,
    editingQuota,
    savingQuota,
    fetchQuota,
    handleSaveQuota: handleSaveQuotaBase,
    getUsagePercent,
    getUsageColor,
} = useQuota()

const openQuotaModal = (org: Organization) => {
    quotaOrgId.value = org.uuid
    quotaOrgName.value = org.name
    quotaSummary.value = null
    quotaError.value = ''
    quotaModalVisible.value = true
    fetchQuota(org.uuid)
}

const closeQuotaModal = () => {
    quotaModalVisible.value = false
}

const handleSaveQuota = async (regionUuid: string) => {
    try {
        await handleSaveQuotaBase(quotaOrgId.value, regionUuid)
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        toast.error(err.response?.data?.error_message || err.message || t('messages.error'))
    }
}

const handleUpdateStatus = async (orgId: string, status: number) => {
    try {
        await orgsApi.updateOrgStatus(orgId, status)
        await fetchOrgs()
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        toast.error(err.response?.data?.error_message || err.message || t('messages.error'))
    }
}

onMounted(fetchOrgs)
</script>

<template>
  <div>
    <PageToolbar v-model:search="searchQuery">
      <template #actions>
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchOrgs" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createOrg') }}
        </button>
      </template>
    </PageToolbar>

    <DataTable
      :columns="columns"
      :rows="filteredOrgs"
      row-key="uuid"
      :loading="loading"
      :error="loadError"
      @retry="fetchOrgs"
    >
      <template #empty>
        <div v-if="searchQuery">
          <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noResults') }}</p>
        </div>
        <div v-else>
          <p>{{ $t('messages.noOrgsFound') }}</p>
        </div>
      </template>

      <template #cell-name="{ row: org }">
        <router-link :to="`/dashboard/orgs/${org.uuid}`" class="resource-link">
          <div class="resource-info">
            <div class="resource-icon">
              <Building2 :size="16" />
            </div>
            <div>
              <div class="resource-name">{{ org.name }}</div>
              <div class="resource-id-row">
                <span class="resource-id" :title="org.uuid">{{ org.uuid.slice(0, 8) }}...</span>
                <button class="copy-btn-mini" @click.stop.prevent="copyId(org.uuid)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                  <Check v-if="copiedId === org.uuid" :size="10" style="color: #10b981;" />
                  <Copy v-else :size="10" />
                </button>
              </div>
            </div>
          </div>
        </router-link>
      </template>

      <template #cell-description="{ row: org }">{{ org.description || '-' }}</template>

      <template #cell-status="{ row: org }">
        <div class="status-cell" :class="'status-' + (org.status || 0)">
          <CheckCircle v-if="org.status === 1" :size="14" />
          <PauseCircle v-else-if="org.status === 2" :size="14" />
          <ShieldAlert v-else-if="org.status === 3" :size="14" />
          <AlertCircle v-else :size="14" />
          <span>
            {{
              org.status === 0 ? $t('dashboard.org.status.pending') :
              org.status === 1 ? $t('dashboard.org.status.active') :
              org.status === 2 ? $t('dashboard.org.status.suspended') :
              org.status === 3 ? $t('dashboard.org.status.disabled') :
              $t('dashboard.org.status.pending')
            }}
          </span>
        </div>
      </template>

      <template #cell-owner="{ row: org }">
        <div class="owner-cell" v-if="org.owner_name || org.owner_email">
          <div class="owner-info">
            <div class="owner-name">
              <User :size="12" />
              <span>{{ org.owner_name }}</span>
            </div>
            <div class="owner-email" v-if="org.owner_email">{{ org.owner_email }}</div>
          </div>
        </div>
        <div class="owner-cell" v-else-if="org.owner_uuid">
          <User :size="12" />
          <div class="resource-id-row">
            <span class="resource-id" :title="org.owner_uuid">{{ org.owner_uuid.slice(0, 8) }}...</span>
            <button class="copy-btn-mini" @click.stop.prevent="copyId(org.owner_uuid)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
              <Check v-if="copiedId === org.owner_uuid" :size="10" style="color: #10b981;" />
              <Copy v-else :size="10" />
            </button>
          </div>
        </div>
        <span v-else>-</span>
      </template>

      <template #cell-created="{ row: org }">{{ org.created_at || '-' }}</template>

      <template #cell-actions="{ row: org }">
        <div class="actions">
          <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="openEditModal(org as any)">
            <Edit2 :size="14" />
          </button>
          <button class="btn btn-ghost btn-sm" :title="$t('quota.manage')" @click="openQuotaModal(org as any)">
            <Gauge :size="14" />
          </button>
          <!-- Admin Status Actions -->
          <template v-if="isSuperuser">
            <button v-if="org.status !== 1" class="btn btn-ghost btn-sm text-success" :title="$t('actions.enable')" @click="handleUpdateStatus(org.uuid, 1)">
              <PlayCircle :size="14" />
            </button>
            <button v-if="org.status === 1" class="btn btn-ghost btn-sm text-warning" :title="$t('dashboard.org.status.suspended')" @click="handleUpdateStatus(org.uuid, 2)">
              <PauseCircle :size="14" />
            </button>
            <button v-if="org.status !== 3" class="btn btn-ghost btn-sm text-error" :title="$t('actions.disable')" @click="handleUpdateStatus(org.uuid, 3)">
              <ShieldAlert :size="14" />
            </button>
          </template>
          <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(org as any)">
            <Trash2 :size="14" />
          </button>
        </div>
      </template>
    </DataTable>

    <!-- Create Organization Modal -->
    <BaseModal
      :show="createModalVisible"
      :title="$t('dashboard.buttons.createOrg')"
      :loading="creating"
      form
      @close="closeCreateModal"
      @submit="handleCreateOrg"
    >
      <div class="form-group">
        <label class="form-label">{{ $t('dashboard.table.name') }}</label>
        <input
          v-model="newOrgForm.name"
          type="text"
          class="form-input"
          :placeholder="$t('dashboard.table.name')"
        />
      </div>
      <div class="form-group">
        <label class="form-label">{{ $t('dashboard.table.description') }}</label>
        <textarea
          v-model="newOrgForm.description"
          class="form-input"
          rows="3"
          style="resize: none;"
          :placeholder="$t('dashboard.table.description')"
        ></textarea>
      </div>
      <div v-if="createError" class="text-error modal-error">
        {{ createError }}
      </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="creating">
          <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ creating ? $t('messages.loading') : $t('actions.confirm') }}
        </button>
      </template>
    </BaseModal>

    <!-- Edit Organization Modal -->
    <BaseModal
      :show="editModalVisible"
      :title="$t('actions.edit')"
      :loading="editing"
      form
      @close="closeEditModal"
      @submit="handleEditOrg"
    >
      <div class="form-group">
        <label class="form-label">{{ $t('dashboard.table.name') }}</label>
        <input
          v-model="editOrgForm.name"
          type="text"
          class="form-input"
          :placeholder="$t('dashboard.table.name')"
        />
      </div>
      <div class="form-group">
        <label class="form-label">{{ $t('dashboard.table.description') }}</label>
        <textarea
          v-model="editOrgForm.description"
          class="form-input"
          rows="3"
          style="resize: none;"
          :placeholder="$t('dashboard.table.description')"
        ></textarea>
      </div>
      <div v-if="editError" class="text-error modal-error">
        {{ editError }}
      </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeEditModal" :disabled="editing">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="editing">
          <span v-if="editing" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ editing ? $t('messages.loading') : $t('actions.confirm') }}
        </button>
      </template>
    </BaseModal>

    <!-- Quota Management Modal -->
    <BaseModal :show="quotaModalVisible" size="xl" @close="closeQuotaModal">
      <template #header>
        <h3><Gauge :size="18" style="margin-right: 8px;" />{{ $t('quota.manage') }} — {{ quotaOrgName }}</h3>
      </template>

      <div v-if="quotaLoading" class="text-center" style="padding: 48px;">
        <div class="loading-spinner" style="margin: 0 auto;"></div>
      </div>
      <div v-else-if="quotaError" class="text-center" style="padding: 24px;">
        <p class="text-error">{{ quotaError }}</p>
        <button class="btn btn-secondary btn-sm" @click="fetchQuota(quotaOrgId)">{{ $t('actions.retry') }}</button>
      </div>
      <div v-else-if="quotaSummary && quotaSummary.regions.length > 0">
        <div v-for="region in quotaSummary.regions" :key="region.region_name" class="quota-region-card">
          <h5 class="quota-region-title">{{ region.region_name }}</h5>
          <table class="data-table quota-table">
            <thead>
              <tr>
                <th>{{ $t('dashboard.table.name') }}</th>
                <th>{{ $t('quota.limit') }}</th>
                <th>{{ $t('quota.used') }}</th>
                <th style="width: 200px;">{{ $t('dashboard.table.status') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="res in QUOTA_ROWS" :key="res.key">
                <td>{{ $t(res.label) }}</td>
                <td>
                  <input
                    v-if="isSuperuser"
                    type="number"
                    class="form-input quota-input"
                    :value="editingQuota[region.region_uuid]?.[res.qkey as keyof OrgResourceQuotaUpdate]"
                    @input="(e: any) => { if (editingQuota[region.region_uuid]) (editingQuota[region.region_uuid] as any)[res.qkey] = Number(e.target.value) }"
                    min="0"
                    step="1"
                  />
                  <span v-else>{{ (region.quota as any)[res.qkey] }}</span>
                </td>
                <td>{{ (region.consumption as any)[res.key] }}</td>
                <td>
                  <div class="usage-bar-container">
                    <div
                      class="usage-bar"
                      :style="{
                        width: getUsagePercent((region.consumption as any)[res.key], (region.quota as any)[res.qkey]) + '%',
                        background: getUsageColor(getUsagePercent((region.consumption as any)[res.key], (region.quota as any)[res.qkey]))
                      }"
                    ></div>
                  </div>
                  <span class="usage-text">{{ getUsagePercent((region.consumption as any)[res.key], (region.quota as any)[res.qkey]) }}%</span>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="isSuperuser" class="quota-save-row">
            <button
              class="btn btn-primary btn-sm"
              @click="handleSaveQuota(region.region_uuid)"
              :disabled="savingQuota === region.region_uuid"
            >
              {{ savingQuota === region.region_uuid ? $t('messages.loading') : $t('actions.save') }}
            </button>
          </div>
        </div>
      </div>
      <div v-else class="text-center text-secondary" style="padding: 48px;">
        {{ $t('quota.noQuota') || 'No quota assigned' }}
      </div>

      <template #footer>
        <button class="btn btn-secondary" @click="closeQuotaModal">{{ $t('actions.close') || $t('actions.cancel') }}</button>
      </template>
    </BaseModal>

    <!-- Delete Confirmation Modal -->
    <DeleteModal
      :show="deleteModalVisible"
      :resource-name="resourceToDelete?.name"
      :resource-id="resourceToDelete?.uuid"
      :loading="deletingResource"
      :error="deleteError"
      @close="closeDeleteModal"
      @confirm="confirmDelete"
    />
  </div>
</template>

<style scoped>
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

.owner-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
}

.owner-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.owner-name {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--text-primary);
  font-weight: var(--font-weight-medium);
}

.owner-email {
  color: var(--text-tertiary);
  font-size: var(--font-size-tiny);
  padding-left: 16px;
}

.actions {
  display: flex;
  justify-content: center;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */

.modal-error {
  font-size: var(--font-size-sm);
  background: var(--error-light);
  padding: var(--spacing-2);
  border-radius: var(--radius-sm);
  margin-top: var(--spacing-2);
}

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.quota-region-card {
  padding: var(--spacing-4) 0;
  border-bottom: 1px solid var(--border-light);
}
.quota-region-card:last-child { border-bottom: none; }
.quota-region-title {
  margin: 0 0 var(--spacing-3);
  font-size: var(--font-size-base);
  font-weight: var(--font-weight-semibold);
  color: var(--text-primary);
}
.quota-table { margin-bottom: var(--spacing-3); }
.quota-input { width: 100px; padding: 4px 8px; font-size: var(--font-size-sm); }
.quota-save-row { display: flex; justify-content: flex-end; padding-top: var(--spacing-2); }
.usage-bar-container {
  display: inline-block;
  width: 120px;
  height: 8px;
  background: var(--bg-tertiary, #e5e7eb);
  border-radius: 4px;
  overflow: hidden;
  vertical-align: middle;
  margin-right: var(--spacing-2);
}
.usage-bar { height: 100%; border-radius: 4px; transition: width 0.3s ease; }
.usage-text { font-size: var(--font-size-xs); color: var(--text-secondary); }
</style>
