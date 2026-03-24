<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { volumesApi, type Volume } from '../../api/volumes'
import { useRegionStore } from '../../stores/region'
import { isValidName } from '../../utils/validation'

import { HardDrive, Plus, MoreVertical, Paperclip, Trash2, Maximize, Search, X, RefreshCw } from 'lucide-vue-next'

const region = useRegionStore()

const volumes = ref<Volume[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newVolumeForm = ref({
    name: '',
    size: 10,
    format: 'qcow2',
    bootable: false
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newVolumeForm.value.name))

const fetchVolumes = async () => {

    loading.value = true
    error.value = null
    try {
        const response = await volumesApi.list()
        volumes.value = response.volumes || (Array.isArray(response) ? response : [])
    } catch (err: any) {
        console.error('Failed to fetch volumes:', err)
        error.value = err.message
        volumes.value = []
    } finally {
        loading.value = false
    }
}

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchVolumes()
    }
})

const filteredVolumes = computed(() => {
    if (!searchQuery.value) return volumes.value
    const query = searchQuery.value.toLowerCase()
    return volumes.value.filter(vol => {
        const nameMatch = (vol.name?.toLowerCase() || '').includes(query)
        const idMatch = (vol.id?.toLowerCase() || '').includes(query)
        const instanceMatch = (vol.instance?.name?.toLowerCase() || '').includes(query)
        return nameMatch || idMatch || instanceMatch
    })
})

const getStatusText = (status: string) => {
    const key = status?.toLowerCase().replace(/ /g, '_')
    const translated = t(`dashboard.volumeStatus.${key}`)
    return translated === `dashboard.volumeStatus.${key}` ? status : translated
}

const formatSize = (size: number) => {
    if (size >= 1000) {
        return `${(size / 1000).toFixed(1)} TB`
    }
    return `${size} GB`
}

const getStatusClass = (status: string) => {
    const statusMap: Record<string, string> = {
        'available': 'status-active',
        'attached': 'status-running',
        'in-use': 'status-running',
        'creating': 'status-pending',
        'deleting': 'status-pending',
        'detaching': 'status-pending',
        'attaching': 'status-pending',
        'error': 'status-error'
    }
    return statusMap[status] || 'status-pending'
}

const openCreateModal = () => {
    newVolumeForm.value = { name: '', size: 10, format: 'qcow2', bootable: false }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateVolume = async () => {
    createError.value = ''
    if (!newVolumeForm.value.name) {
        createError.value = t('messages.nameRequired')
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        await volumesApi.create(newVolumeForm.value)
        await fetchVolumes()
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create volume:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<Volume | null>(null)

const handleDeleteClick = (item: Volume) => {
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
        await volumesApi.delete(resourceToDelete.value.id)
        await fetchVolumes()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete volume:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

onMounted(() => {
    // If region ID is already available, fetch immediately
    if (region.currentRegionId) {
        fetchVolumes()
    }
    // Otherwise the watcher on currentRegionId will trigger the fetch
})
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
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchVolumes" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createVolume') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.size') }}</th>
            <th>{{ $t('dashboard.table.boot') }}</th>
            <th>{{ $t('dashboard.table.format') }}</th>
            <th>{{ $t('dashboard.table.attachedTo') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredVolumes.length === 0">
            <td colspan="7" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <HardDrive :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noVolumes') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="volume in filteredVolumes" :key="volume.id">
            <td>
              <router-link :to="{ name: 'volume-detail', params: { id: volume.id } }" class="resource-link">
                <div class="resource-name">{{ volume.name }}</div>
                <div class="resource-id">{{ volume.id }}</div>
              </router-link>
            </td>
            <td>
              <span :class="['badge', getStatusClass(volume.status)]">
                {{ getStatusText(volume.status) }}
              </span>
            </td>
            <td>{{ formatSize(volume.size) }}</td>
            <td>
              <span :class="['badge', volume.booting ? 'status-running' : 'status-pending']">
                {{ volume.booting ? $t('messages.yes') : $t('messages.no') }}
              </span>
            </td>
            <td>{{ volume.format || '-' }}</td>
            <td>
              <span v-if="volume.instance" class="text-primary">
                {{ volume.instance.name }}
              </span>
              <span v-else class="text-light">{{ $t('messages.notAttached') }}</span>
            </td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" :title="$t('actions.attach')">
                  <Paperclip :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm" :title="$t('actions.resize')">
                  <Maximize :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(volume)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Volume Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createVolume') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input 
              v-model="newVolumeForm.name" 
              type="text" 
              :class="['form-input', { 'input-error': !isNameValid }]" 
              placeholder="e.g. data-disk-01" 
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>

          </div>
          
          <div class="grid-2">
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.forms.size') }}</label>
              <input 
                v-model.number="newVolumeForm.size" 
                type="number" 
                min="1"
                class="form-input" 
              />
            </div>
            
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.forms.format') }}</label>
              <div class="select-wrapper">
                <select v-model="newVolumeForm.format" class="form-input">
                  <option value="qcow2">QCOW2</option>
                  <option value="raw">RAW</option>
                </select>
              </div>
            </div>
          </div>

          <div class="form-group">
            <label class="checkbox-label">
              <input type="checkbox" v-model="newVolumeForm.bootable" />
              <span>{{ $t('dashboard.forms.bootable') }}</span>
            </label>
          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateVolume" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createVolume') }}
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
            <div style="width:64px;height:64px;border-radius:50%;background:var(--error-light);display:flex;align-items:center;justify-content:center;margin:0 auto var(--spacing-4);color:var(--error-color)"><Trash2 :size="32" /></div>
            <p style="color:var(--text-secondary);margin:0 0 var(--spacing-4)">{{ $t('dashboard.deleteConfirm.message') }}</p>
            <div style="background:var(--bg-secondary);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:var(--spacing-3) var(--spacing-4);text-align:left">
              <span style="font-size:var(--font-size-xs);color:var(--text-tertiary);text-transform:uppercase;letter-spacing:0.05em;display:block;margin-bottom:var(--spacing-1)">{{ $t('dashboard.deleteConfirm.resource') }}</span>
              <span style="font-weight:var(--font-weight-semibold);display:block">{{ resourceToDelete?.name }}</span>
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ resourceToDelete?.id }}</span>
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

.resource-name {
  font-weight: var(--font-weight-medium);
  color: var(--primary-color);
}

.resource-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
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

.actions {
  display: flex;
  gap: var(--spacing-02);
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */
.checkbox-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    cursor: pointer;
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

.header-actions { display: flex; gap: 8px; align-items: center; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
