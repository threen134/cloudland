<script setup lang="ts">
// Storage packages (shared-storage-design.md §6.1, §13.3): installers of storage software in the package repository.
// Upload one, wait for its verification, read and accept its license; deployments can then use it.
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw, Package, Upload, FileText, Trash2 } from 'lucide-vue-next'
import { storagePackagesApi, type StoragePackage } from '../../api/storagePackages'
import { useListQuery } from '../../composables/useListQuery'
import { useRegionStore } from '../../stores/region'
import { formatBytes, formatDateTime } from '../../utils/format'
import { storageKindText, storageKindShort } from '../../utils/storageCluster'
import { errorMessage } from '../../utils/error'
import type { StatusVariant } from '../../utils/status'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import StoragePackageUploadModal from '../../components/storage/StoragePackageUploadModal.vue'
import StorageLicenseModal from '../../components/storage/StorageLicenseModal.vue'

const { t, te } = useI18n()
const region = useRegionStore()

const query = useListQuery<StoragePackage>(
    async ({ offset, limit }) => {
        const response = await storagePackagesApi.list({ offset, limit })
        return { items: response.storage_packages || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

const columns = computed<Column[]>(() => [
    { key: 'file_name', label: t('storage.packages.file') },
    { key: 'kind', label: t('storage.cluster.kind'), hideBelow: 1280 },
    { key: 'version', label: t('storage.packages.version'), hideBelow: 1280 },
    { key: 'size_bytes', label: t('storage.packages.size'), hideBelow: 1440 },
    { key: 'status', label: t('storage.status') },
    { key: 'license', label: t('storage.packages.license') },
    { key: 'created_at', label: t('dashboard.table.createdAt'), hideBelow: 1600 },
    { key: 'actions', label: '', align: 'right' },
])

const statusVariant = (p: StoragePackage): StatusVariant =>
    p.status === 'ready' ? 'success' : p.status === 'error' ? 'error' : 'pending'
const statusText = (p: StoragePackage) => {
    if (p.status === 'uploading' && !p.source_url && p.total_parts)
        return t('storage.packages.statusUploading', { percent: Math.floor((p.parts_done * 100) / p.total_parts) })
    return te(`storage.packages.statuses.${p.status}`) ? t(`storage.packages.statuses.${p.status}`) : p.status
}

// Packages being verified or downloaded by clapi move on by themselves: refresh quietly meanwhile
let timer: ReturnType<typeof setInterval> | null = null
const anyBusy = computed(() =>
    query.items.value.some((p) => p.status === 'verifying' || (p.status === 'uploading' && p.source_url))
)
watch(
    anyBusy,
    (busy) => {
        if (busy && !timer) timer = setInterval(() => query.load(true), 5000)
        else if (!busy && timer) {
            clearInterval(timer)
            timer = null
        }
    },
    { immediate: true }
)
onUnmounted(() => {
    if (timer) clearInterval(timer)
})

const showUpload = ref(false)
const licenseId = ref('')
const deleting = ref<StoragePackage | null>(null)
const deleteError = ref('')
const deleteBusy = ref(false)

const uploaded = () => {
    showUpload.value = false
    query.load()
}
// An upload stopped or cut off is in the list as unfinished, and offered to resume next time
const closeUpload = () => {
    showUpload.value = false
    query.load(true)
}
const accepted = () => {
    licenseId.value = ''
    query.load(true)
}
const confirmDelete = async () => {
    if (!deleting.value) return
    deleteBusy.value = true
    deleteError.value = ''
    try {
        await storagePackagesApi.remove(deleting.value.id)
        deleting.value = null
        query.load()
    } catch (err) {
        deleteError.value = errorMessage(err, t('messages.error'))
    } finally {
        deleteBusy.value = false
    }
}

const page = query.page
const pageSize = query.pageSize
onMounted(() => {
    if (region.currentRegionId) query.load()
})
</script>

<template>
    <div class="vpc-list-container">
        <div class="page-actions">
            <p class="page-intro">{{ t('storage.packages.intro') }}</p>
            <div class="actions">
                <button class="btn btn-secondary btn-sm btn-icon" :title="t('actions.refresh')" @click="query.load()">
                    <RefreshCw :size="14" :class="{ spinning: query.loading.value }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="showUpload = true">
                    <Upload :size="14" />
                    <span>{{ t('storage.packages.upload') }}</span>
                </button>
            </div>
        </div>

        <DataTable
            :columns="columns"
            :rows="query.items.value"
            row-key="id"
            :loading="query.loading.value"
            :error="query.error.value"
            @retry="() => query.load()"
        >
            <template #empty>
                <div class="empty-state">
                    <Package :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p class="empty-title">{{ t('storage.packages.emptyTitle') }}</p>
                    <p class="empty-hint">{{ t('storage.packages.emptyHint') }}</p>
                </div>
            </template>
            <template #cell-file_name="{ row: p }">
                <div class="resource-info">
                    <div class="resource-icon"><Package :size="16" /></div>
                    <div>
                        <div class="resource-name">{{ p.file_name }}</div>
                        <div class="resource-id-row">
                            <span class="resource-id" :title="p.sha256">{{
                                p.sha256 ? 'SHA-256 ' + p.sha256.slice(0, 16) + '…' : p.id.slice(0, 8)
                            }}</span>
                        </div>
                    </div>
                </div>
            </template>
            <template #cell-kind="{ row: p }">
                <span class="nowrap" :title="storageKindText(t, te, p.kind)">{{
                    storageKindShort(t, te, p.kind)
                }}</span>
            </template>
            <template #cell-version="{ row: p }">
                <div>{{ p.version || '-' }}</div>
                <div v-if="p.edition" class="sub nowrap">
                    {{
                        te(`storage.packages.editions.${p.edition}`)
                            ? t(`storage.packages.editions.${p.edition}`)
                            : p.edition
                    }}
                </div>
                <div v-if="p.distros.length" class="sub nowrap">{{ p.distros.join(', ') }}</div>
            </template>
            <template #cell-size_bytes="{ row: p }">
                <span class="nowrap">{{ p.size_bytes ? formatBytes(p.size_bytes) : '-' }}</span>
            </template>
            <template #cell-status="{ row: p }">
                <StatusBadge :variant="statusVariant(p)" :label="statusText(p)" />
                <div v-if="p.reason" class="sub reason" :title="p.reason">{{ p.reason }}</div>
            </template>
            <template #cell-license="{ row: p }">
                <span v-if="p.accepted_by" class="accepted nowrap" :title="formatDateTime(p.accepted_at || '')">{{
                    t('storage.packages.acceptedShort', { user: p.accepted_by })
                }}</span>
                <span v-else-if="p.status === 'ready'" class="text-warning">{{
                    t('storage.packages.notAccepted')
                }}</span>
                <span v-else class="text-secondary">-</span>
            </template>
            <template #cell-created_at="{ row: p }">
                <span class="nowrap">{{ formatDateTime(p.created_at) }}</span>
            </template>
            <template #cell-actions="{ row: p }">
                <div class="row-actions">
                    <button
                        class="icon-btn-table"
                        :title="t('storage.packages.license')"
                        :disabled="p.status !== 'ready'"
                        @click="licenseId = p.id"
                    >
                        <FileText :size="16" />
                    </button>
                    <button class="icon-btn-table icon-danger" :title="t('actions.delete')" @click="deleting = p">
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>
            <template #footer>
                <PaginationBar
                    :page="page"
                    :page-size="pageSize"
                    :total="query.total.value"
                    @update:page="page = $event"
                    @update:page-size="pageSize = $event"
                />
            </template>
        </DataTable>

        <StoragePackageUploadModal
            :show="showUpload"
            :packages="query.items.value"
            @close="closeUpload"
            @done="uploaded"
        />
        <StorageLicenseModal :show="!!licenseId" :package-id="licenseId" @close="licenseId = ''" @accepted="accepted" />
        <DeleteModal
            :show="!!deleting"
            :title="t('storage.packages.deleteTitle')"
            :message="t('storage.packages.deleteMessage', { name: deleting?.file_name || '' })"
            :loading="deleteBusy"
            :error="deleteError"
            @close="deleting = null"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
.page-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-4);
}

.page-intro {
    margin: 0;
    flex: 1;
    min-width: 280px;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.actions {
    display: flex;
    gap: var(--spacing-2);
}

.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: var(--spacing-6) var(--spacing-4);
}

.empty-title {
    font-weight: 600;
    margin: 0 0 var(--spacing-2);
}

.empty-hint {
    max-width: 560px;
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
    text-align: center;
}

.sub {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.reason {
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    margin-top: 2px;
}

.accepted {
    color: var(--success-dark);
}

.nowrap {
    white-space: nowrap;
}
</style>
