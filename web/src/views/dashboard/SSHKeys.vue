<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
// import { useRouter } from 'vue-router'
import { keysApi, type SSHKey } from '../../api/keys'
import { isValidName } from '../../utils/validation'

import { Key, Plus, Trash2, Copy, Check, Search, X, RefreshCw } from 'lucide-vue-next'

const keys = ref<SSHKey[]>([])
const loading = ref(false)
const copiedId = ref<string | null>(null)
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newKeyForm = ref({
    name: '',
    public_key: ''
})


const { t } = useI18n()
const isNameValid = computed(() => isValidName(newKeyForm.value.name))

// const router = useRouter()

const fetchKeys = async () => {
    loading.value = true
    try {
        const response = await keysApi.fetchKeys()
        keys.value = (response.data as any).keys || []
    } catch (err) {
        console.error('API fetch failed:', err)
        keys.value = []
    } finally {
        loading.value = false
    }
}

const filteredKeys = computed(() => {
    if (!searchQuery.value) return keys.value
    const query = searchQuery.value.toLowerCase()
    return keys.value.filter(key => 
        key.name.toLowerCase().includes(query) || 
        key.finger_print?.toLowerCase().includes(query) ||
        key.id.toLowerCase().includes(query)
    )
})

// Detail view removed as requested
// const navigateToDetail = (key: SSHKey) => {
//     router.push({ name: 'ssh-key-detail', params: { id: key.id } })
// }

const copyFingerprint = async (key: SSHKey) => {
    if (key.finger_print) {
        await navigator.clipboard.writeText(key.finger_print)
        copiedId.value = key.id
        setTimeout(() => {
            copiedId.value = null
        }, 2000)
    }
}

const formatPublicKey = (key: string) => {
    if (key.length > 50) {
        return key.substring(0, 25) + '...' + key.substring(key.length - 20)
    }
    return key
}

const openCreateModal = () => {
    newKeyForm.value = { name: '', public_key: '' }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateKey = async () => {
    createError.value = ''
    if (!newKeyForm.value.name || !newKeyForm.value.public_key) {
        createError.value = t('messages.fillNameAndKey')
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        await keysApi.createKey(newKeyForm.value)
        await fetchKeys()
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create key:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<SSHKey | null>(null)

const handleDeleteClick = (item: SSHKey) => {
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
        await keysApi.deleteKey(resourceToDelete.value.id)
        await fetchKeys()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete SSH key:', error)
        if (error.response?.data?.error_code === 161006 || error.response?.data?.error_code_str === 'SSHKeyInUse') {
            deleteError.value = t('messages.sshKeyInUse')
        } else {
            deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
        }
    } finally {
        deletingResource.value = false
    }
}

onMounted(fetchKeys)
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
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchKeys" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createKey') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.userName') }}</th>
            <th>{{ $t('dashboard.table.type') }}</th>
            <th>{{ $t('dashboard.table.fingerprint') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="4" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredKeys.length === 0">
            <td colspan="4" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <Key :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noSSHKeys') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="key in filteredKeys" :key="key.id">
            <td>
              <div class="key-name">
                <Key :size="16" class="key-icon" />
                {{ key.name }}
              </div>
              <div class="key-id">{{ key.id }}</div>
            </td>
            <td>
              <span class="key-type">
                {{ key.public_key?.split(' ')[0] || 'ssh-rsa' }}
              </span>
            </td>
            <td>
              <div class="fingerprint-cell">
                <code class="fingerprint">{{ key.finger_print || '-' }}</code>
                <button 
                  class="btn btn-ghost btn-sm copy-btn" 
                  @click="copyFingerprint(key)"
                  :title="copiedId === key.id ? $t('messages.copied') : $t('actions.copy')"
                >
                  <component :is="copiedId === key.id ? Check : Copy" :size="14" />
                </button>
              </div>
            </td>
            <td>
              <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(key)">
                <Trash2 :size="14" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Key Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createKey') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input 
              v-model="newKeyForm.name" 
              type="text" 
              :class="['form-input', { 'input-error': !isNameValid }]" 
              placeholder="e.g. My Laptop Key" 
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>

          </div>
          
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.publicKey') }}</label>
            <textarea 
              v-model="newKeyForm.public_key" 
              class="form-input" 
              rows="5" 
              placeholder="ssh-rsa AAAAB3NzaC1yc2E..."
              style="font-family: monospace; font-size: 0.8em;"
            ></textarea>
            <p class="helper-text">{{ $t('dashboard.forms.publicKeyHelp') }}</p>
          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateKey" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createKey') }}
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

.key-name {
  display: flex;
  align-items: center;
  gap: var(--spacing-02);
  font-weight: var(--font-weight-medium);
}

.key-icon {
  color: var(--primary-color);
}

.key-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  margin-left: 24px;
}

.key-type {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
  padding: var(--spacing-01) var(--spacing-02);
  background: var(--gray-10);
  border-radius: var(--radius-sm);
}

.fingerprint-cell {
  display: flex;
  align-items: center;
  gap: var(--spacing-02);
}

.fingerprint {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
}

.copy-btn {
  opacity: 0.5;
}

.copy-btn:hover {
  opacity: 1;
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */
.helper-text {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  margin-top: var(--spacing-2);
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

/* .resource-link removed */
</style>
