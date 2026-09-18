<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { volumesApi, type Volume } from '../../api/volumes'
import { useRegionStore } from '../../stores/region'
import { isValidName } from '../../utils/validation'

import { HardDrive, Plus, Paperclip, Trash2, Maximize, Search, Check, Copy, RefreshCw } from 'lucide-vue-next'
import { formatDisk } from '../../utils/format'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'

const region = useRegionStore()

const volumes = ref<Volume[]>([])
const loading = ref(false)
const loadError = ref('')
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
const toast = useToast()
const isNameValid = computed(() => isValidName(newVolumeForm.value.name))

const { copiedId, copyId } = useCopyId()

const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'status', label: t('dashboard.table.status'), sortable: true },
    { key: 'size', label: t('dashboard.table.size'), sortable: true },
    { key: 'boot', label: t('dashboard.table.boot'), sortable: true, sortValue: (v) => (v.booting ? 0 : 1) },
    { key: 'format', label: t('dashboard.table.format'), sortable: true },
    { key: 'attachedTo', label: t('dashboard.table.attachedTo'), sortable: true, sortValue: (v) => v.instance?.name ?? null },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const fetchVolumes = async () => {

    loading.value = true
    loadError.value = ''
    try {
        // 列表页有「启动盘」一列，系统盘与数据盘都要显示
        const response = await volumesApi.list({ type: 'all' })
        volumes.value = response.volumes || (Array.isArray(response) ? response : [])
    } catch (err: any) {
        console.error('Failed to fetch volumes:', err)
        loadError.value = t('messages.error')
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
        toast.success(t('messages.createSuccess'))
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
        toast.success(t('messages.deleteSuccess'))
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
    <PageToolbar v-model:search="searchQuery">
      <template #actions>
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchVolumes" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createVolume') }}
        </button>
      </template>
    </PageToolbar>

    <DataTable
      :columns="columns"
      :rows="filteredVolumes"
      row-key="id"
      :loading="loading"
      :error="loadError"
      @retry="fetchVolumes"
    >
      <template #empty>
        <div v-if="searchQuery">
          <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noResults') }}</p>
        </div>
        <div v-else>
          <HardDrive :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noVolumes') }}</p>
        </div>
      </template>

      <template #cell-name="{ row: volume }">
        <router-link :to="{ name: 'volume-detail', params: { id: volume.id } }" class="resource-link">
          <div class="resource-info">
            <div class="resource-icon">
              <HardDrive :size="16" />
            </div>
            <div>
              <div class="resource-name">{{ volume.name }}</div>
              <div class="resource-id-row">
                <span class="resource-id" :title="volume.id">{{ volume.id.slice(0, 8) }}...</span>
                <button class="copy-btn-mini" @click.stop.prevent="copyId(volume.id)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                  <Check v-if="copiedId === volume.id" :size="10" style="color: #10b981;" />
                  <Copy v-else :size="10" />
                </button>
              </div>
            </div>
          </div>
        </router-link>
      </template>

      <template #cell-status="{ row: volume }">
        <StatusBadge :status="volume.status" :label="getStatusText(volume.status)" />
      </template>

      <template #cell-size="{ row: volume }">{{ formatDisk(volume.size) }}</template>

      <template #cell-boot="{ row: volume }">
        <span :class="['badge', volume.booting ? 'status-running' : 'status-pending']">
          {{ volume.booting ? $t('messages.yes') : $t('messages.no') }}
        </span>
      </template>

      <template #cell-format="{ row: volume }">{{ volume.format || '-' }}</template>

      <template #cell-attachedTo="{ row: volume }">
        <span v-if="volume.instance" class="text-primary">
          {{ volume.instance.name }}
        </span>
        <span v-else class="text-light">{{ $t('messages.notAttached') }}</span>
      </template>

      <template #cell-actions="{ row: volume }">
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
      </template>
    </DataTable>

    <!-- Create Volume Modal -->
    <BaseModal
      :show="createModalVisible"
      :title="$t('dashboard.buttons.createVolume')"
      :loading="creating"
      form
      @close="closeCreateModal"
      @submit="handleCreateVolume"
    >
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input 
              v-model="newVolumeForm.name" 
              type="text" 
              :class="['form-input', { 'input-error': !isNameValid }]" 
              :placeholder="$t('dashboard.forms.placeholder.volumeNameExample')" 
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

          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="creating">
          <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createVolume') }}
        </button>
      </template>
    </BaseModal>

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

.actions {
  display: flex;
  justify-content: center;
  gap: var(--spacing-2);
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

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
