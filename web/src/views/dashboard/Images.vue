<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { imagesApi, type Image, type ImagePayload } from '../../api/images'
import { isValidName } from '../../utils/validation'
import { useAuthStore } from '../../stores/auth'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { useTenantStore } from '../../stores/tenant'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()

import { Disc, Search, Trash2, Plus, Eye, EyeOff, Check, Copy, RefreshCw } from 'lucide-vue-next'
import { quotaErrorMessage } from '../../utils/quotaError'
import { errorMessage } from '../../utils/error'
import { formatBytes } from '../../utils/format'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

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
    boot_loader: 'uefi',
    download_url: '',
    user: 'admin',
})

const { t, te } = useI18n()
const isNameValid = computed(() => isValidName(newImageForm.value.name))

// 排序在服务端做（列 key 即 images 表的真实列名）；
// 操作系统一列是前端按镜像名推断出来的，和任何一列都对不上，所以不给排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'visibility', label: t('dashboard.table.visibility'), sortable: true },
    { key: 'os', label: t('dashboard.table.os') },
    { key: 'architecture', label: t('dashboard.table.architecture'), sortable: true },
    { key: 'format', label: t('dashboard.table.format'), sortable: true },
    { key: 'size', label: t('dashboard.table.size'), sortable: true },
    { key: 'status', label: t('dashboard.table.status'), sortable: true },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// 分页、搜索、排序、公开/私有筛选都在服务端做（后端 /images 支持 visibility=public|private）
const {
    items: images,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchImages,
    reload: reloadImages,
} = useListQuery<Image>(
    async ({ offset, limit, query, order }) => {
        const response = await imagesApi.fetchImages({
            offset,
            limit,
            order,
            query: query || undefined,
            visibility: selectedVisibility.value === 'all' ? undefined : selectedVisibility.value,
        })
        return { items: response.images || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId), selectedVisibility] }
)

const openCreateModal = () => {
    newImageForm.value = {
        name: '',
        os_code: 'linux',
        os_family: 'Ubuntu',
        os_version: '22.04',
        // 不传 architecture：后端 ImagePayload 不收这个字段，建镜像时固定按 x86_64 存，
        // 而且这个值全流程都没人用（不参与调度也不影响启动），原先那个 aarch64 下拉
        // 只会让用户以为自己建了一台 ARM 镜像
        boot_loader: 'uefi',
        download_url: '',
        user: 'admin',
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
        // 列表按创建时间倒序，新建的在第一页
        await reloadImages()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create image:', err)
        createError.value = quotaErrorMessage(err, t, te) || errorMessage(err, t('messages.error'))
    } finally {
        creating.value = false
    }
}

const canDelete = (image: Image) => {
    if (isSuperuser.value) return true
    return image.owner === currentOrgName.value
}

const toggleVisibility = async (image: Image) => {
    try {
        await imagesApi.patchImage(image.id, { public: !image.public })
        await fetchImages()
        toast.success(t('messages.updateSuccess'))
    } catch (err) {
        console.error('Failed to toggle visibility:', err)
        toast.error(errorMessage(err, t('messages.error')))
    }
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
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error) {
        console.error('Failed to delete image:', error)
        deleteError.value = errorMessage(error, t('messages.error'))
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
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchImages()" :title="$t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.createImage') }}
                </button>
            </template>
        </PageToolbar>

        <!-- Table View -->
        <DataTable
            :columns="columns"
            :rows="images"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="fetchImages()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <p>{{ $t('messages.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: image }">
                <router-link :to="{ name: 'image-detail', params: { id: image.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon" :class="image.os_code">
                            <Disc :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ image.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="image.id">{{ image.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(image.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check
                                        v-if="copiedId === image.id"
                                        :size="10"
                                        style="color: var(--success-color)"
                                    />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-visibility="{ row: image }">
                <span :class="['badge', image.public ? 'status-running' : 'status-stopped']">
                    {{ image.public ? $t('dashboard.table.public') : $t('dashboard.table.private') }}
                </span>
            </template>

            <template #cell-os="{ row: image }">
                <span class="os-text">{{ getOsName(image) }}</span>
            </template>

            <template #cell-architecture="{ row: image }">
                <span class="arch-tag">{{ image.architecture }}</span>
            </template>

            <template #cell-format="{ row: image }">
                <span class="format-text">{{ image.format?.toUpperCase() || '-' }}</span>
            </template>

            <template #cell-size="{ row: image }">
                {{ formatBytes(image.size || 0) }}
            </template>

            <template #cell-status="{ row: image }">
                <StatusBadge :status="image.status" :label="getStatusText(image.status)" />
            </template>

            <template #cell-actions="{ row: image }">
                <div class="actions">
                    <button
                        v-if="isSuperuser"
                        class="btn btn-ghost btn-sm"
                        :title="image.public ? $t('dashboard.table.setPrivate') : $t('dashboard.table.setPublic')"
                        @click="toggleVisibility(image)"
                    >
                        <EyeOff v-if="image.public" :size="14" />
                        <Eye v-else :size="14" />
                    </button>
                    <button
                        v-if="canDelete(image)"
                        class="btn btn-ghost btn-sm text-error"
                        :title="$t('actions.delete')"
                        @click="handleDeleteClick(image)"
                    >
                        <Trash2 :size="14" />
                    </button>
                </div>
            </template>

            <template #footer>
                <PaginationBar
                    :page="page"
                    :page-size="pageSize"
                    :total="total"
                    @update:page="page = $event"
                    @update:page-size="pageSize = $event"
                />
            </template>
        </DataTable>

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
                <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="creating">
                    <span
                        v-if="creating"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
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

.actions {
    display: flex;
    justify-content: center;
    gap: var(--spacing-2);
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
