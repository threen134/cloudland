<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { imagesApi, type Image, type ImagePayload } from '../../api/images'
import { isValidName } from '../../utils/validation'
import { useAuthStore } from '../../stores/auth'
import { useTenantStore } from '../../stores/tenant'

import { Disc, Search, Monitor, Server, Trash2, Plus, X, Globe, Cpu, User, Eye, EyeOff } from 'lucide-vue-next'

const images = ref<Image[]>([])
const loading = ref(false)
const searchQuery = ref('')
const selectedType = ref<string>('all')
const selectedVisibility = ref<string>('all')
const router = useRouter()
const auth = useAuthStore()
const tenant = useTenantStore()
const isSuperuser = computed(() => auth.user?.is_superuser === true)
const currentOrgName = computed(() => tenant.currentOrg?.name || '')

const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newImageForm = ref<ImagePayload>({
    name: '',
    os_code: 'linux',
    os_family: 'Ubuntu',
    os_version: '22.04',
    architecture: 'x86_64',
    boot_loader: 'uefi',
    download_url: '',
    user: 'admin'
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newImageForm.value.name))




const fetchImages = async () => {
    loading.value = true
    try {
        const response = await imagesApi.fetchImages()
        images.value = (response.data as any).images || []
    } catch (err) {
        console.error('API fetch failed:', err)
        images.value = []
    } finally {
        loading.value = false
    }
}

const openCreateModal = () => {
    newImageForm.value = {
        name: '',
        os_code: 'linux',
        os_family: 'Ubuntu',
        os_version: '22.04',
        architecture: 'x86_64',
        boot_loader: 'uefi',
        download_url: '',
        user: 'admin'
    }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateImage = async () => {
    createError.value = ''
    if (!newImageForm.value.name || !newImageForm.value.download_url) {
        createError.value = 'Please fill in Name and Download URL.'
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        await imagesApi.createImage(newImageForm.value)
        await fetchImages()
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create image:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const filteredImages = ref<Image[]>([])

const filterImages = () => {
    filteredImages.value = images.value.filter(img => {
        const matchesSearch = img.name.toLowerCase().includes(searchQuery.value.toLowerCase())

        let matchesType = true
        if (selectedType.value === 'all') {
            matchesType = true
        } else if (selectedType.value === 'linux') {
            matchesType = img.os_code === 'linux'
        } else if (selectedType.value === 'windows') {
            matchesType = img.os_code === 'windows'
        } else if (selectedType.value === 'windows-server') {
             matchesType = img.name.toLowerCase().includes('windows') && img.name.toLowerCase().includes('server')
        } else {
            // Specific distro check
            matchesType = img.name.toLowerCase().includes(selectedType.value)
        }

        let matchesVisibility = true
        if (selectedVisibility.value === 'public') {
            matchesVisibility = img.public === true
        } else if (selectedVisibility.value === 'private') {
            matchesVisibility = !img.public
        }

        return matchesSearch && matchesType && matchesVisibility
    })
}

const canDelete = (image: Image) => {
    if (isSuperuser.value) return true
    return image.owner === currentOrgName.value
}

const toggleVisibility = async (image: Image) => {
    try {
        await imagesApi.patchImage(image.id, { public: !image.public })
        await fetchImages()
        filterImages()
    } catch (err: any) {
        console.error('Failed to toggle visibility:', err)
    }
}

const navigateToDetail = (image: Image) => {
    router.push({ name: 'image-detail', params: { id: image.id } })
}

// Watch for changes to trigger filtering automatically
watch(selectedType, filterImages)
watch(searchQuery, filterImages)
watch(selectedVisibility, filterImages)

const formatSize = (bytes: number) => {
    if (!bytes) return '0 B'
    const k = 1024
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`
}

const getOsName = (image: Image) => {
    const nameLower = image.name.toLowerCase()
    if (nameLower.includes('ubuntu')) return 'Ubuntu'
    if (nameLower.includes('centos')) return 'CentOS'
    if (nameLower.includes('debian')) return 'Debian'
    if (nameLower.includes('fedora')) return 'Fedora'
    if (nameLower.includes('rocky')) return 'Rocky Linux'
    if (nameLower.includes('windows')) return 'Windows'
    if (nameLower.includes('server')) return 'Windows Server' // Fallback for Windows Server
    // Capitalize first letter of os_code as fallback
    return image.os_code.charAt(0).toUpperCase() + image.os_code.slice(1)
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<Image | null>(null)

const handleDeleteClick = (item: Image) => {
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
        await imagesApi.deleteImage(resourceToDelete.value.id)
        await fetchImages()
        filterImages()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete image:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

const getStatusClass = (status: string | undefined) => {
    if (!status) return 'status-running'
    const s = status.toLowerCase()
    if (s === 'active' || s === 'available') return 'status-running'
    if (s === 'error' || s === 'failed') return 'status-error'
    if (s === 'deleting' || s === 'pending') return 'status-pending'
    return 'status-stopped'
}

onMounted(async () => {
    await fetchImages()
    filterImages()
})
</script>

<template>
  <div class="images-page">
    <!-- Header Removed by request -->

    <!-- Filters Bar replaced by Page Header -->
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
        <select v-model="selectedVisibility" class="visibility-filter">
          <option value="all">{{ $t('dashboard.table.allVisibility') }}</option>
          <option value="public">{{ $t('dashboard.table.public') }}</option>
          <option value="private">{{ $t('dashboard.table.private') }}</option>
        </select>
      </div>

      <button class="btn btn-primary btn-sm" @click="openCreateModal">
        <Plus :size="14" /> {{ $t('dashboard.buttons.createImage') }}
      </button>
    </div>

    <!-- Table View -->
    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.visibility') }}</th>
            <th>{{ $t('dashboard.table.os') }}</th>
            <th>{{ $t('dashboard.table.architecture') }}</th>
            <th>{{ $t('dashboard.table.format') }}</th>
            <th>{{ $t('dashboard.table.size') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="8" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredImages.length === 0">
            <td colspan="8" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <p>{{ $t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="image in filteredImages" :key="image.id">
            <td>
              <div class="image-info clickable" @click="navigateToDetail(image)">
                <div class="os-icon" :class="image.os_code">
                  <Server v-if="image.os_code === 'linux'" :size="16" />
                  <Monitor v-else-if="image.os_code === 'windows'" :size="16" />
                  <Disc v-else :size="16" />
                </div>
                <div>
                  <div class="resource-name resource-link">{{ image.name }}</div>
                  <div class="resource-id">{{ image.id }}</div>
                </div>
              </div>
            </td>
            <td>
              <span :class="['badge', image.public ? 'status-running' : 'status-stopped']">
                {{ image.public ? $t('dashboard.table.public') : $t('dashboard.table.private') }}
              </span>
            </td>
            <td>
              <span class="os-text">{{ getOsName(image) }}</span>
            </td>
            <td>
              <span class="arch-tag">{{ image.architecture }}</span>
            </td>
            <td>
              <span class="format-text">{{ image.format?.toUpperCase() || '-' }}</span>
            </td>
            <td>
              {{ formatSize(image.size || 0) }}
            </td>
            <td>
               <span :class="['badge', getStatusClass(image.status)]">
                  {{ image.status || 'active' }}
               </span>
            </td>
            <td>
              <div class="actions">
                <button v-if="isSuperuser" class="btn btn-ghost btn-sm" :title="image.public ? $t('dashboard.table.setPrivate') : $t('dashboard.table.setPublic')" @click="toggleVisibility(image)">
                  <EyeOff v-if="image.public" :size="14" />
                  <Eye v-else :size="14" />
                </button>
                <button v-if="canDelete(image)" class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(image)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Image Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createImage') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <div class="form-grid">
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
              <input 
                v-model="newImageForm.name" 
                type="text" 
                :class="['form-input', { 'input-error': !isNameValid }]" 
                placeholder="e.g. Ubuntu 22.04 Custom" 
              />
              <div v-if="!isNameValid" class="text-error text-xs mt-1">
                {{ $t('messages.invalidHostname') }}
              </div>

            </div>
            
            <div class="form-group">
              <label class="form-label">Download URL</label>
              <input 
                v-model="newImageForm.download_url" 
                type="text" 
                class="form-input" 
                placeholder="https://example.com/image.qcow2" 
              />
            </div>

            <div class="form-row">
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.osType') }}</label>
                <select v-model="newImageForm.os_code" class="form-input">
                  <option value="linux">Linux</option>
                  <option value="windows">Windows</option>
                  <option value="other">Other</option>
                </select>
              </div>
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.bootLoader') }}</label>
                <select v-model="newImageForm.boot_loader" class="form-input">
                  <option value="bios">BIOS</option>
                  <option value="uefi">UEFI</option>
                </select>
              </div>
            </div>

            <div class="form-row">
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.osFamily') }}</label>
                <input 
                  v-model="newImageForm.os_family" 
                  type="text" 
                  class="form-input" 
                  placeholder="e.g. Ubuntu" 
                />
              </div>
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.osVersion') }}</label>
                <input 
                  v-model="newImageForm.os_version" 
                  type="text" 
                  class="form-input" 
                  placeholder="e.g. 22.04" 
                />
              </div>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.forms.architecture') }}</label>
              <select v-model="newImageForm.architecture" class="form-input">
                <option value="x86_64">x86_64</option>
                <option value="aarch64">aarch64 (ARM)</option>
              </select>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.forms.defaultUser') }}</label>
              <input 
                v-model="newImageForm.user" 
                type="text" 
                class="form-input" 
                placeholder="e.g. root or ubuntu" 
              />
            </div>
          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateImage" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.loading') : $t('dashboard.buttons.createImage') }}
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
  max-width: 500px;
  display: flex;
  gap: 10px;
  align-items: center;
}

.visibility-filter {
  height: 40px;
  padding: 0 10px;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-light);
  background: var(--bg-secondary);
  color: var(--text-primary);
  font-size: 0.875rem;
  cursor: pointer;
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

.image-info {
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
}

.os-icon {
  width: 32px;
  height: 32px;
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.os-icon.linux { background-color: var(--success-light); color: var(--success-dark); }
.os-icon.windows { background-color: var(--primary-light); color: var(--primary-color); }
.os-icon.other { background-color: var(--bg-tertiary); color: var(--text-tertiary); }

.resource-name {
  font-weight: var(--font-weight-medium);
  color: var(--primary-color);
  font-size: var(--font-size-sm);
}

.resource-link {
  color: var(--primary-600);
  font-weight: 500;
  cursor: pointer;
}

.resource-link:hover {
  text-decoration: underline;
}

.clickable {
  cursor: pointer;
}

.resource-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  font-family: var(--font-family-mono);
}

.os-text {
  font-weight: 500;
  color: var(--text-primary);
}

.arch-tag {
  font-family: var(--font-family-mono);
  font-size: 0.75rem;
  color: var(--text-secondary);
  background: var(--bg-secondary);
  padding: 2px 6px;
  border-radius: 4px;
  border: 1px solid var(--border-light);
}

.format-text {
  font-weight: 500;
  color: var(--text-secondary);
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
  max-width: 600px;
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
  padding: var(--spacing-4) var(--spacing-6);
  border-bottom: 1px solid var(--border-light);
}

.modal-header h3 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.modal-body {
  padding: var(--spacing-6);
  max-height: 70vh;
  overflow-y: auto;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--spacing-3);
  padding: var(--spacing-4) var(--spacing-6);
  border-top: 1px solid var(--border-light);
  background: var(--bg-secondary);
}

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
  background: var(--bg-secondary);
  color: var(--text-primary);
  font-size: var(--font-size-sm);
}

.form-input:focus {
  outline: none;
  border-color: var(--primary-color);
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
}

.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
