<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { zonesApi, type Zone, type CreateZonePayload } from '../../api/zones'
import { Search as SearchIcon, MapPin, Plus, RefreshCw, Trash2, Settings2, X, Loader2, Check } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()

const { t } = useI18n()
const toast = useToast()
const zoneList = ref<Zone[]>([])
const loading = ref(false)
const searchQuery = ref('')

// Create modal
const showCreateModal = ref(false)
const creating = ref(false)
const createForm = ref<CreateZonePayload>({ name: '', default: false, remark: '' })

// Edit modal
const showEditModal = ref(false)
const editing = ref(false)
const editingZone = ref<Zone | null>(null)
const editForm = ref({ default: false, remark: '' })

// Delete modal
const showDeleteModal = ref(false)
const deleting = ref(false)
const deletingZone = ref<Zone | null>(null)

const fetchZones = async () => {
    loading.value = true
    try {
        const response = await zonesApi.fetchZones()
        const data = response.data as any
        zoneList.value = Array.isArray(data) ? data : (data.zones || [])
    } catch (error) {
        console.error('API fetch failed:', error)
        zoneList.value = []
    } finally {
        loading.value = false
    }
}

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchZones()
    }
})

const filteredZones = computed(() => {
    if (!searchQuery.value) return zoneList.value
    const query = searchQuery.value.toLowerCase()
    return zoneList.value.filter(z =>
        (z.name && z.name.toLowerCase().includes(query)) ||
        (z.remark && z.remark.toLowerCase().includes(query))
    )
})

// Create
const openCreateModal = () => {
    createForm.value = { name: '', default: false, remark: '' }
    showCreateModal.value = true
}

const handleCreate = async () => {
    if (!createForm.value.name) return
    creating.value = true
    try {
        await zonesApi.createZone(createForm.value)
        showCreateModal.value = false
        toast.success(t('messages.success'))
        await fetchZones()
    } catch (err: any) {
        const msg = err.response?.data?.error || err.response?.data?.message || 'Create failed'
        toast.error(msg)
    } finally {
        creating.value = false
    }
}

// Edit
const openEditModal = (zone: Zone) => {
    editingZone.value = zone
    editForm.value = { default: zone.default, remark: zone.remark || '' }
    showEditModal.value = true
}

const handleEdit = async () => {
    if (!editingZone.value) return
    editing.value = true
    try {
        await zonesApi.updateZone(editingZone.value.name, editForm.value)
        showEditModal.value = false
        toast.success(t('messages.success'))
        await fetchZones()
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Update failed')
    } finally {
        editing.value = false
    }
}

// Delete
const confirmDelete = (zone: Zone) => {
    deletingZone.value = zone
    showDeleteModal.value = true
}

const handleDelete = async () => {
    if (!deletingZone.value) return
    deleting.value = true
    try {
        await zonesApi.deleteZone(deletingZone.value.name)
        showDeleteModal.value = false
        deletingZone.value = null
        toast.success(t('messages.success'))
        await fetchZones()
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Delete failed')
    } finally {
        deleting.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchZones()
    }
})
</script>

<template>
  <div class="vpc-list-container">
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <SearchIcon :size="16" class="search-icon" />
          <input type="text" v-model="searchQuery" :placeholder="t('actions.search') + '...'" class="search-input" />
        </div>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchZones" :title="t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" />
          <span>{{ t('actions.create') }}</span>
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('dashboard.table.nameId') }}</th>
            <th>{{ t('dashboard.zoneActions.remark') }}</th>
            <th>{{ t('dashboard.table.type') }}</th>
            <th>{{ t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="4" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredZones.length === 0">
            <td colspan="4" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ t('messages.noResults') }}</p>
               </div>
               <div v-else class="empty-state">
                  <MapPin :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                  <p>{{ t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="zone in filteredZones" :key="zone.name">
            <td>
              <router-link :to="{ name: 'zone-detail', params: { name: zone.name } }" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon">
                    <MapPin :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ zone.name }}</div>
                    <div class="resource-id">ID: {{ zone.id || '-' }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td class="remark-cell">{{ zone.remark || '-' }}</td>
            <td>
              <span class="badge" :class="zone.default ? 'badge-primary' : 'badge-secondary'">
                <Check v-if="zone.default" :size="12" />
                {{ zone.default ? t('dashboard.zoneActions.default') : 'Zone' }}
              </span>
            </td>
            <td>
              <div class="table-actions">
                <button class="icon-btn-table" @click.prevent="openEditModal(zone)" :title="t('actions.edit')">
                  <Settings2 :size="16" />
                </button>
                <button class="icon-btn-table text-error" @click.prevent="confirmDelete(zone)" :title="t('actions.delete')">
                  <Trash2 :size="16" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Modal -->
    <Teleport to="body">
      <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
        <div class="modal-content card" style="max-width: 480px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.zoneActions.createTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showCreateModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <div class="form-stack">
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.name') }} *</label>
                <input type="text" v-model="createForm.name" class="form-input" :placeholder="t('dashboard.forms.placeholder.zoneNameExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.zoneActions.remark') }}</label>
                <input type="text" v-model="createForm.remark" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer;">
                  <input type="checkbox" v-model="createForm.default" />
                  {{ t('dashboard.zoneActions.default') }}
                </label>
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showCreateModal = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreate" :disabled="creating || !createForm.name">
              <Loader2 v-if="creating" :size="14" class="spinning" />
              {{ creating ? t('messages.creating') : t('actions.create') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Edit Modal -->
    <Teleport to="body">
      <div v-if="showEditModal" class="modal-overlay" @click.self="showEditModal = false">
        <div class="modal-content card" style="max-width: 480px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.zoneActions.editTitle') }} - {{ editingZone?.name }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showEditModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <div class="form-stack">
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.zoneActions.remark') }}</label>
                <input type="text" v-model="editForm.remark" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer;">
                  <input type="checkbox" v-model="editForm.default" />
                  {{ t('dashboard.zoneActions.default') }}
                </label>
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showEditModal = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleEdit" :disabled="editing">
              <Loader2 v-if="editing" :size="14" class="spinning" />
              {{ editing ? t('messages.saving') : t('actions.save') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Delete Confirm Modal -->
    <Teleport to="body">
      <div v-if="showDeleteModal" class="modal-overlay" @click.self="showDeleteModal = false">
        <div class="modal-content card" style="max-width: 440px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.zoneActions.deleteTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showDeleteModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p>{{ t('dashboard.zoneActions.deleteConfirm', { name: deletingZone?.name }) }}</p>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showDeleteModal = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-danger" @click="handleDelete" :disabled="deleting">
              <Loader2 v-if="deleting" :size="14" class="spinning" />
              {{ deleting ? t('messages.deleting') : t('actions.delete') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
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

.search-wrapper { flex: 1; max-width: 400px; }

.header-actions { display: flex; gap: 8px; align-items: center; }

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

.search-icon { color: var(--gray-400); }

.search-input {
  border: none; background: transparent; width: 100%;
  height: 100%; font-size: 0.875rem; color: var(--text-primary);
}
.search-input:focus { outline: none; }

.table-card { padding: 0; overflow: hidden; }

/* Standardized resource-info is global from index.css */

.resource-icon {
  width: 32px; height: 32px;
  background: var(--primary-light); color: var(--primary-color);
  border-radius: var(--radius-sm);
  display: flex; align-items: center; justify-content: center;
}

.resource-name { font-weight: var(--font-weight-semibold); color: var(--text-main); font-size: var(--font-size-sm); }

.resource-id {
  font-size: var(--font-size-xs); color: var(--text-light);
  font-family: var(--font-family-mono);
}

.resource-link {
  text-decoration: none; display: block; padding: 4px 0;
  border-radius: var(--radius-sm); transition: all 0.15s;
}
.resource-link:hover .resource-name { color: var(--primary-600); text-decoration: underline; }

.remark-cell {
  max-width: 250px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--font-size-sm); color: var(--text-secondary);
}

.badge {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 8px; border-radius: var(--radius-sm);
  font-size: var(--font-size-xs); font-weight: 500;
}
.badge-primary { background: rgba(59, 130, 246, 0.1); color: #3b82f6; }
.badge-secondary { background: var(--gray-100); color: var(--gray-600); }

.table-actions { display: flex; gap: 8px; }

.icon-btn-table {
  width: 32px; height: 32px; border-radius: 8px; border: none;
  background: transparent; color: var(--text-tertiary);
  display: flex; align-items: center; justify-content: center;
  cursor: pointer; transition: all 0.2s;
}
.icon-btn-table:hover { background-color: var(--bg-tertiary); color: var(--primary-500); }
.icon-btn-table.text-error:hover { background-color: #fef2f2; color: #ef4444; }

.empty-state {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
}

/* Modal */
.form-stack { display: flex; flex-direction: column; gap: 16px; }
.form-group { display: flex; flex-direction: column; }
.form-label { font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 4px; font-weight: 500; }

.form-input {
  width: 100%; padding: 8px 12px;
  border: 1px solid var(--border-light); border-radius: var(--radius-md);
  font-size: 0.875rem; background: var(--bg-primary); color: var(--text-primary);
}
.form-input:focus { outline: none; border-color: var(--primary-300); box-shadow: 0 0 0 2px var(--primary-100); }

.btn-danger {
  background: #ef4444; color: white; border: none;
  padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}
.btn-danger:hover { background: #dc2626; }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
