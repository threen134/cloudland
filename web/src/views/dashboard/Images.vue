<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { imagesApi, type Image, type ImagePayload } from '../../api/images'
import { isValidName } from '../../utils/validation'
import { useAuthStore } from '../../stores/auth'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useTenantStore } from '../../stores/tenant'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()

import { Disc, Search, Trash2, Plus, Eye, EyeOff, Check, Copy, RefreshCw } from 'lucide-vue-next'
import { quotaErrorMessage } from '../../utils/quotaError'
import { formatBytes } from '../../utils/format'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'

const images = ref<Image[]>([])
const loading = ref(false)
const searchQuery = ref('')
const selectedType = ref<string>('all')
const selectedVisibility = ref<string>('all')
const auth = useAuthStore()
const tenant = useTenantStore()
const isSuperuser = computed(() => auth.user?.is_superuser === true)
const currentOrgName = computed(() => tenant.currentOrg?.name || '')
const toast = useToast()

const { copiedId, copyId } = useCopyId()

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

const { t, te } = useI18n()
const isNameValid = computed(() => isValidName(newImageForm.value.name))




const fetchImages = async () => {
    loading.value = true
    try {
        const response = await imagesApi.fetchImages()
        images.value = (response as any).images || []
    } catch (err) {
        console.error('API fetch failed:', err)
        images.value = []
    } finally {
        loading.value = false
    }
}

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchImages()
    }
})

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
        createError.value = t('dashboard.overview.imageActions.fillRequired')
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
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to create image:', err)
        createError.value = quotaErrorMessage(err, t, te) || err.response?.data?.error_message || err.message || t('messages.error')
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
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to toggle visibility:', err)
        toast.error(err.response?.data?.error_message || err.message || t('messages.error'))
    }
}


// Watch for changes to trigger filtering automatically
watch(selectedType, filterImages)
watch(searchQuery, filterImages)
watch(selectedVisibility, filterImages)


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
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete image:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

const getStatusText = (status: string | undefined) => {
    if (!status) return t('dashboard.imageStatus.active')
    const key = status.toLowerCase()
    const translated = t(`dashboard.imageStatus.${key}`)
    return translated === `dashboard.imageStatus.${key}` ? status : translated
}

onMounted(async () => {
    if (region.currentRegionId) {
        await fetchImages()
        filterImages()
    }
})
</script>

<template>
  <div class="images-page">
    <!-- Header Removed by request -->

    <!-- Filters Bar replaced by Page Header -->
    <PageToolbar v-model:search="searchQuery">
      <template #filters>
        <select v-model="selectedVisibility" class="visibility-filter">
          <option value="all">{{ $t('dashboard.table.allVisibility') }}</option>
          <option value="public">{{ $t('dashboard.table.public') }}</option>
          <option value="private">{{ $t('dashboard.table.private') }}</option>
        </select>
      </template>

      <template #actions>
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchImages().then(filterImages)" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createImage') }}
        </button>
      </template>
    </PageToolbar>

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
              <router-link :to="{ name: 'image-detail', params: { id: image.id } }" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon" :class="image.os_code">
                    <Disc :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ image.name }}</div>
                    <div class="resource-id-row">
                      <span class="resource-id" :title="image.id">{{ image.id.slice(0, 8) }}...</span>
                      <button class="copy-btn-mini" @click.stop.prevent="copyId(image.id)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                        <Check v-if="copiedId === image.id" :size="10" style="color: #10b981;" />
                        <Copy v-else :size="10" />
                      </button>
                    </div>
                  </div>
                </div>
              </router-link>
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
              {{ formatBytes(image.size || 0) }}
            </td>
            <td>
               <StatusBadge :status="image.status" :label="getStatusText(image.status)" />
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
    <BaseModal
      :show="createModalVisible"
      :title="$t('dashboard.buttons.createImage')"
      :loading="creating"
      form
      @close="closeCreateModal"
      @submit="handleCreateImage"
    >
          <div class="form-grid">
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
              <input 
                v-model="newImageForm.name" 
                type="text" 
                :class="['form-input', { 'input-error': !isNameValid }]" 
                :placeholder="$t('dashboard.forms.placeholder.imageNameExample')" 
              />
              <div v-if="!isNameValid" class="text-error text-xs mt-1">
                {{ $t('messages.invalidHostname') }}
              </div>

            </div>
            
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.forms.downloadUrl') }}</label>
              <input 
                v-model="newImageForm.download_url" 
                type="text" 
                class="form-input" 
                :placeholder="$t('dashboard.forms.placeholder.urlExample')" 
              />
            </div>

            <div class="form-row">
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.osType') }}</label>
                <select v-model="newImageForm.os_code" class="form-input">
                  <option value="linux">{{ $t('dashboard.forms.osTypes.linux') }}</option>
                  <option value="windows">{{ $t('dashboard.forms.osTypes.windows') }}</option>
                  <option value="other">{{ $t('dashboard.forms.osTypes.other') }}</option>
                </select>
              </div>
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.bootLoader') }}</label>
                <select v-model="newImageForm.boot_loader" class="form-input">
                  <option value="bios">{{ $t('dashboard.forms.bootLoaders.bios') }}</option>
                  <option value="uefi">{{ $t('dashboard.forms.bootLoaders.uefi') }}</option>
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
                  :placeholder="$t('dashboard.forms.placeholder.osFamilyExample')" 
                />
              </div>
              <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.forms.osVersion') }}</label>
                <input 
                  v-model="newImageForm.os_version" 
                  type="text" 
                  class="form-input" 
                  :placeholder="$t('dashboard.forms.placeholder.osVersionExample')" 
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
                :placeholder="$t('dashboard.forms.placeholder.defaultUserExample')" 
              />
            </div>
          </div>

          <div v-if="createError" class="modal-error text-error">
            {{ createError }}
          </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="creating">
          <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ creating ? $t('messages.loading') : $t('dashboard.buttons.createImage') }}
        </button>
      </template>
    </BaseModal>

    <!-- Delete Confirmation Modal -->
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

.table-card {
  padding: 0;
  overflow: hidden;
}

/* .resource-info, .resource-icon etc. are global from index.css */

/* Global styles from index.css are used for .resource-name, .resource-id, .resource-link */

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

.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
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

.modal-error {
  margin-top: var(--spacing-4);
  font-size: var(--font-size-sm);
  background: var(--error-light);
  padding: var(--spacing-2);
  border-radius: var(--radius-sm);
}
</style>
