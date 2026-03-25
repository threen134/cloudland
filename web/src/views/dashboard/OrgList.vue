<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { orgsApi, type Organization } from '../../api/orgs'
import { type OrgResourceQuotaUpdate } from '../../api/quota'
import { useAuthStore } from '../../stores/auth'
import { useQuota } from '../../composables/useQuota'
import { Plus, Building2, Trash2, Edit2, User, Search, X, Gauge, RefreshCw } from 'lucide-vue-next'

const authStore = useAuthStore()
const isSuperuser = computed(() => authStore.user?.is_superuser === true)

const { t } = useI18n()

const orgs = ref<Organization[]>([])
const loading = ref(false)
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

const fetchOrgs = async () => {
    loading.value = true
    try {
        const response = await orgsApi.fetchOrgs()
        const data = response.data as any
        orgs.value = Array.isArray(data) ? data : (data.orgs || [])
    } catch (error) {
        console.error('Failed to fetch orgs:', error)
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
        createError.value = 'Please enter an organization name.'
        return
    }

    creating.value = true
    try {
        await orgsApi.createOrg(newOrgForm.value)
        await fetchOrgs()
        closeCreateModal()
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
        editError.value = 'Please enter an organization name.'
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

const handleSaveQuota = (regionName: string) => handleSaveQuotaBase(quotaOrgId.value, regionName)

onMounted(fetchOrgs)
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
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchOrgs" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createOrg') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.description') }}</th>
            <th>Owner ID</th>
            <th>{{ $t('dashboard.table.created') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="5" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredOrgs.length === 0">
            <td colspan="5" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <p>{{ $t('messages.noOrgsFound') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="org in filteredOrgs" :key="org.uuid">
            <td>
              <router-link :to="`/dashboard/orgs/${org.uuid}`" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon">
                    <Building2 :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ org.name }}</div>
                    <div class="resource-id">{{ org.uuid }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>{{ org.description || '-' }}</td>
            <td>
              <div class="owner-cell" v-if="org.owner_uuid">
                <User :size="12" />
                <span>{{ org.owner_uuid }}</span>
              </div>
              <span v-else>-</span>
            </td>
            <td>{{ org.created_at || '-' }}</td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="openEditModal(org)">
                  <Edit2 :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm" :title="$t('quota.manage')" @click="openQuotaModal(org)">
                  <Gauge :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(org)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Organization Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card" style="max-width: 500px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createOrg') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
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
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateOrg" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Edit Organization Modal -->
    <div v-if="editModalVisible" class="modal-overlay" @click.self="closeEditModal">
      <div class="modal-content card" style="max-width: 500px;">
        <div class="modal-header">
          <h3>{{ $t('actions.edit') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeEditModal"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
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
        </div>
        <div v-if="editError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ editError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeEditModal" :disabled="editing">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleEditOrg" :disabled="editing">
            <span v-if="editing" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ editing ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Quota Management Modal -->
    <div v-if="quotaModalVisible" class="modal-overlay" @click.self="closeQuotaModal">
      <div class="modal-content card" style="max-width: 700px; width: 95%;">
        <div class="modal-header">
          <h3><Gauge :size="18" style="margin-right: 8px;" />{{ $t('quota.manage') }} — {{ quotaOrgName }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeQuotaModal"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6); max-height: 60vh; overflow-y: auto;">
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
                  <tr v-for="res in [
                    { key: 'cpu_cores', qkey: 'max_cpu_cores', label: $t('quota.cpuCores') },
                    { key: 'ram_gb', qkey: 'max_ram_gb', label: $t('quota.ramGb') },
                    { key: 'disk_gb', qkey: 'max_disk_gb', label: $t('quota.diskGb') },
                    { key: 'public_ips', qkey: 'max_public_ips', label: $t('quota.publicIps') },
                  ]" :key="res.key">
                    <td>{{ res.label }}</td>
                    <td>
                      <input
                        v-if="isSuperuser"
                        type="number"
                        class="form-input quota-input"
                        :value="editingQuota[region.region_name]?.[res.qkey as keyof OrgResourceQuotaUpdate]"
                        @input="(e: any) => { if (editingQuota[region.region_name]) (editingQuota[region.region_name] as any)[res.qkey] = Number(e.target.value) }"
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
                  @click="handleSaveQuota(region.region_name)"
                  :disabled="savingQuota === region.region_name"
                >
                  {{ savingQuota === region.region_name ? $t('messages.loading') : $t('actions.save') }}
                </button>
              </div>
            </div>
          </div>
          <div v-else class="text-center text-secondary" style="padding: 48px;">
            {{ $t('quota.noQuota') || 'No quota assigned' }}
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeQuotaModal">{{ $t('actions.close') || $t('actions.cancel') }}</button>
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
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ resourceToDelete?.uuid }}</span>
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
  margin-bottom: 0px;
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

.actions {
  display: flex;
  gap: var(--spacing-02);
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

.header-actions { display: flex; gap: 8px; align-items: center; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.quota-region-card {
  padding: var(--spacing-4) 0;
  border-bottom: 1px solid var(--border-light);
}
.quota-region-card:last-child { border-bottom: none; }
.quota-region-title {
  margin: 0 0 var(--spacing-3);
  font-size: var(--font-size-md);
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
