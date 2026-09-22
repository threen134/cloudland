<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { volumesApi, type Volume, type VolumePayload } from '../../api/volumes'
import { useRegionStore } from '../../stores/region'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import { useVolumeActionGuards, usePollBusyVolumes } from '../../composables/useVolumeActions'

import { HardDrive, Plus, Link, Unlink, Trash2, Maximize2, Search, Check, Copy, RefreshCw } from 'lucide-vue-next'
import { formatDisk } from '../../utils/format'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import VolumeActionModals from '../../components/volume/VolumeActionModals.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const region = useRegionStore()

const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
// 只有名称和容量：后端建卷时格式固定为 raw，booting 只在虚拟机创建系统盘时为真，
// 界面上原来的「格式」下拉和「可启动」勾选实际都不生效（apis/volume.go 的 VolumePayload 不收）
const newVolumeForm = ref<VolumePayload>({
    name: '',
    size: 10,
})

const { t } = useI18n()
const toast = useToast()
const isNameValid = computed(() => isValidName(newVolumeForm.value.name))

const { copiedId, copyId } = useCopyId()

// 排序在服务端做（sortField 是 volumes 表的真实列名）；
// 「挂载到」显示的是虚拟机名，在 instances 表里，后端没有 join 排序，所以不给排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'status', label: t('dashboard.table.status'), sortable: true },
    { key: 'size', label: t('dashboard.table.size'), sortable: true },
    { key: 'boot', label: t('dashboard.table.boot'), sortable: true, sortField: 'booting' },
    { key: 'format', label: t('dashboard.table.format'), sortable: true },
    { key: 'attachedTo', label: t('dashboard.table.attachedTo') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// 分页、搜索、排序都在服务端做（卷列表的搜索参数是 name，不是通用的 query）
const {
    items: volumes,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchVolumes,
    reload: reloadVolumes,
} = useListQuery<Volume>(
    async ({ offset, limit, query, order }) => {
        // 列表页有「启动盘」一列，系统盘与数据盘都要显示
        const response = await volumesApi.list({ offset, limit, order, type: 'all', name: query || undefined })
        return { items: response.volumes || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

// Attach / detach / resize dialogs; rows in a transitional state are refreshed quietly until they settle
const volumeActions = ref<InstanceType<typeof VolumeActionModals> | null>(null)
const { attachBlocked, detachBlocked, resizeBlocked } = useVolumeActionGuards()
usePollBusyVolumes(volumes, () => fetchVolumes(true))

const getStatusText = (status: string) => {
    const key = status?.toLowerCase().replace(/ /g, '_')
    const translated = t(`dashboard.volumeStatus.${key}`)
    return translated === `dashboard.volumeStatus.${key}` ? status : translated
}

const openCreateModal = () => {
    newVolumeForm.value = { name: '', size: 10 }
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
        // 列表按创建时间倒序，新建的在第一页
        await reloadVolumes()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create volume:', err)
        createError.value = errorMessage(err, t('messages.error'))
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
    } catch (error) {
        console.error('Failed to delete volume:', error)
        deleteError.value = errorMessage(error, t('messages.error'))
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
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="() => fetchVolumes()"
                    :title="$t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.createVolume') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="volumes"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="() => fetchVolumes()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <HardDrive :size="48" style="opacity: 0.3; margin-bottom: 16px" />
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
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(volume.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check
                                        v-if="copiedId === volume.id"
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

            <template #footer>
                <PaginationBar
                    :page="page"
                    :page-size="pageSize"
                    :total="total"
                    @update:page="page = $event"
                    @update:page-size="pageSize = $event"
                />
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
                <div class="row-actions">
                    <button
                        v-if="volume.instance"
                        class="icon-btn-table"
                        :title="detachBlocked(volume) || $t('actions.detach')"
                        :disabled="!!detachBlocked(volume)"
                        @click="volumeActions?.openDetach(volume)"
                    >
                        <Unlink :size="16" />
                    </button>
                    <button
                        v-else
                        class="icon-btn-table"
                        :title="attachBlocked(volume) || $t('actions.attach')"
                        :disabled="!!attachBlocked(volume)"
                        @click="volumeActions?.openAttach(volume)"
                    >
                        <Link :size="16" />
                    </button>
                    <button
                        class="icon-btn-table"
                        :title="resizeBlocked(volume) || $t('actions.resize')"
                        :disabled="!!resizeBlocked(volume)"
                        @click="volumeActions?.openResize(volume)"
                    >
                        <Maximize2 :size="16" />
                    </button>
                    <button
                        class="icon-btn-table icon-danger"
                        :title="$t('actions.delete')"
                        @click="handleDeleteClick(volume)"
                    >
                        <Trash2 :size="16" />
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
                    <input v-model.number="newVolumeForm.size" type="number" min="1" class="form-input" />
                </div>
            </div>

            <div
                v-if="createError"
                class="text-error"
                style="
                    font-size: var(--font-size-sm);
                    background: var(--error-light);
                    padding: var(--spacing-2);
                    border-radius: var(--radius-sm);
                "
            >
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
                    {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createVolume') }}
                </button>
            </template>
        </BaseModal>

        <VolumeActionModals ref="volumeActions" @changed="() => fetchVolumes(true)" />

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

/* Modal Styles */
.checkbox-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    cursor: pointer;
}
</style>
