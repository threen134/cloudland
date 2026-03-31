<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { flavorsApi, type Flavor, type FlavorPayload } from '../../api/flavors'
import { isValidName } from '../../utils/validation'

import { Plus, SquareStack, MemoryStick, Search, Trash2, Cpu, HardDrive, X, RefreshCw } from 'lucide-vue-next'

const flavors = ref<Flavor[]>([])
const loading = ref(false)
const searchQuery = ref('')
const toast = useToast()

const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newFlavorForm = ref<FlavorPayload>({
    name: '',
    cpu: 1,
    memory: 1024, // Starting with 1GB in MB? Min was 16. Let's assume MB.
    disk: 20
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newFlavorForm.value.name))


const fetchFlavors = async () => {
    loading.value = true
    try {
        const response = await flavorsApi.fetchFlavors()
        flavors.value = (response.data as any).flavors || []
    } catch (err) {
        console.error('API fetch failed:', err)
        flavors.value = []
    } finally {
        loading.value = false
    }
}

const openCreateModal = () => {
    newFlavorForm.value = {
        name: '',
        cpu: 1,
        memory: 1024,
        disk: 20
    }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateFlavor = async () => {
    createError.value = ''
    if (!newFlavorForm.value.name) {
        createError.value = t('dashboard.flavorActions.enterName')
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        await flavorsApi.createFlavor(newFlavorForm.value)
        await fetchFlavors()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to create flavor:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const filteredFlavors = computed(() => {
    if (!searchQuery.value) return flavors.value
    const query = searchQuery.value.toLowerCase()
    return flavors.value.filter(flavor => 
        flavor.name.toLowerCase().includes(query) || 
        flavor.id.toLowerCase().includes(query)
    )
})

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<any>(null)

const handleDeleteClick = (item: any) => {
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
        await flavorsApi.deleteFlavor(resourceToDelete.value.name || resourceToDelete.value.id)
        await fetchFlavors()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete flavor:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

const formatRam = (val: number | string) => {
    const mb = typeof val === 'string' ? parseInt(val) : val
    if (!mb) return '-'
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(0)} ${t('specs.gb')}`
    }
    // If the value is very small (like 1, 2, 4), it's probably already in GB
    if (mb < 64) {
        return `${mb} ${t('specs.gb')}`
    }
    return `${mb} ${t('specs.mb')}`
}

onMounted(fetchFlavors)
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
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchFlavors" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createFlavor') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('specs.cpu') }}</th>
            <th>{{ $t('specs.ram') }}</th>
            <th>{{ $t('specs.storage') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="5" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredFlavors.length === 0">
             <td colspan="5" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <SquareStack :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p class="text-secondary">{{ $t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="flavor in filteredFlavors" :key="flavor.id">
            <td>
              <div class="resource-link-static">
                <div class="resource-info">
                  <div class="resource-icon">
                    <SquareStack :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ flavor.name }}</div>
                    <div class="resource-id">{{ flavor.id }}</div>
                  </div>
                </div>
              </div>
            </td>
            <td>
              <div class="spec-cell">
                <Cpu :size="14" class="text-secondary" />
                <span>{{ flavor.vcpus || flavor.cpu || '-' }}</span>
              </div>
            </td>
            <td>
              <div class="spec-cell">
                <MemoryStick :size="14" class="text-secondary" />
                <span>{{ formatRam(flavor.ram || flavor.memory) }}</span>
              </div>
            </td>
            <td>
              <div class="spec-cell">
                <HardDrive :size="14" class="text-secondary" />
                <span>{{ flavor.disk }} {{ $t('specs.gb') }}</span>
              </div>
            </td>
            <td>
              <div class="actions">

                <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(flavor)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Flavor Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createFlavor') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <div class="form-grid">
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.table.name') }}</label>
              <input 
                v-model="newFlavorForm.name" 
                type="text" 
                :class="['form-input', { 'input-error': !isNameValid }]" 
                :placeholder="$t('dashboard.forms.placeholder.flavorNameExample')" 
              />
              <div v-if="!isNameValid" class="text-error text-xs mt-1">
                {{ $t('messages.invalidHostname') }}
              </div>

            </div>
            
            <div class="form-row">
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.table.vcpus') }}</label>
                <div class="input-with-unit">
                  <input 
                    v-model.number="newFlavorForm.cpu" 
                    type="number" 
                    class="form-input" 
                    min="1"
                  />
                  <span class="unit">{{ $t('specs.cores').replace('{n}', '') }}</span>
                </div>
              </div>
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('specs.ram') }}</label>
                <div class="input-with-unit">
                  <input 
                    v-model.number="newFlavorForm.memory" 
                    type="number" 
                    class="form-input" 
                    min="16"
                  />
                  <span class="unit">{{ $t('specs.mb') }}</span>
                </div>
              </div>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.overview.disk') }}</label>
              <div class="input-with-unit">
                <input 
                  v-model.number="newFlavorForm.disk" 
                  type="number" 
                  class="form-input" 
                  min="1"
                />
                <span class="unit">{{ $t('specs.gb') }}</span>
              </div>
            </div>
          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateFlavor" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.loading') : $t('dashboard.buttons.createFlavor') }}
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

.table-card {
  padding: 0;
  overflow: hidden;
}

/* .resource-info etc. are global from index.css */

.resource-link-static {
  display: block;
  padding: 4px 0;
}

.spec-cell {
  display: flex;
  align-items: center;
  gap: 8px;
  font-family: var(--font-family-mono);
}

.actions {
  display: flex;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */
.form-grid {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-4);
}

.form-row {
  display: flex;
  gap: var(--spacing-4);
}

.flex-1 {
  flex: 1;
}

.form-group {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-1);
}

.form-label {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--text-secondary);
}

.form-input {
  padding: 8px 12px;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-light);
  background: var(--bg-primary);
  color: var(--text-primary);
  font-size: var(--font-size-sm);
  width: 100%;
}

.form-input:focus {
  outline: none;
  border-color: var(--primary-color);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.input-with-unit {
  position: relative;
  display: flex;
  align-items: center;
}

.input-with-unit .form-input {
  padding-right: 60px;
}

.input-with-unit .unit {
  position: absolute;
  right: 12px;
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  pointer-events: none;
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
