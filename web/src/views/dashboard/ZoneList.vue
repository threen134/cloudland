<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { zonesApi, type Zone, type CreateZonePayload } from '../../api/zones'
import { Search as SearchIcon, MapPin, Plus, RefreshCw, Trash2, Settings2, Loader2, Check, Copy } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { useRegionStore } from '../../stores/region'
import { errorMessage } from '../../utils/error'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const region = useRegionStore()

const { t } = useI18n()
const toast = useToast()

const { copiedId, copyId } = useCopyId()

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

// 排序在服务端做（sortField 是数据库列名，和列 key 不一定同名）。
// 类型列对应的是 zones.default，default 是 SQL 保留字、后端的 ORDER BY 不加引号，
// 会直接报语法错误，所以这一列不提供排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'remark', label: t('dashboard.zoneActions.remark'), sortable: true },
    { key: 'type', label: t('dashboard.table.type') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// 分页与搜索都在服务端做
const {
    items: zoneList,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchZones,
    reload: reloadZones,
} = useListQuery<Zone>(
    async ({ offset, limit, query, order }) => {
        const response = await zonesApi.fetchZones({ offset, limit, order, query: query || undefined })
        return { items: response.zones || [], total: response.total ?? 0 }
    },
    // 可用区列表后端默认按 name 排（不是 -created_at），初始排序跟它保持一致
    { defaultOrder: 'name', watchSources: [computed(() => region.currentRegionId)] }
)

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
        await reloadZones()
    } catch (err) {
        const msg = errorMessage(err, 'Create failed')
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
    } catch (err) {
        toast.error(errorMessage(err, 'Update failed'))
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
    } catch (err) {
        toast.error(errorMessage(err, 'Delete failed'))
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
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="() => fetchZones()"
                    :title="t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="zoneList"
            row-key="name"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="() => fetchZones()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ t('messages.noResults') }}</p>
                </div>
                <div v-else class="empty-state">
                    <MapPin :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p>{{ t('messages.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: zone }">
                <router-link :to="{ name: 'zone-detail', params: { name: zone.name } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <MapPin :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ zone.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="zone.id || '-'">
                                    {{ (zone.id || '-').slice(0, 8) }}{{ (zone.id || '').length > 8 ? '...' : '' }}
                                </span>
                                <button
                                    v-if="zone.id"
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(zone.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check v-if="copiedId === zone.id" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-remark="{ row: zone }">
                <span class="remark-cell">{{ zone.remark || '-' }}</span>
            </template>

            <template #cell-type="{ row: zone }">
                <span class="badge" :class="zone.default ? 'badge-primary' : 'badge-secondary'">
                    <Check v-if="zone.default" :size="12" />
                    {{ zone.default ? t('dashboard.zoneActions.default') : 'Zone' }}
                </span>
            </template>

            <template #cell-actions="{ row: zone }">
                <div class="table-actions">
                    <button class="icon-btn-table" @click.prevent="openEditModal(zone)" :title="t('actions.edit')">
                        <Settings2 :size="16" />
                    </button>
                    <button
                        class="icon-btn-table text-error"
                        @click.prevent="confirmDelete(zone)"
                        :title="t('actions.delete')"
                    >
                        <Trash2 :size="16" />
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

        <!-- Create Modal -->
        <BaseModal
            :show="showCreateModal"
            :title="t('dashboard.zoneActions.createTitle')"
            :loading="creating"
            form
            @close="showCreateModal = false"
            @submit="handleCreate"
        >
            <div class="form-stack">
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.name') }} *</label>
                    <input
                        type="text"
                        v-model="createForm.name"
                        class="form-input"
                        :placeholder="t('dashboard.forms.placeholder.zoneNameExample')"
                    />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.zoneActions.remark') }}</label>
                    <input type="text" v-model="createForm.remark" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer">
                        <input type="checkbox" v-model="createForm.default" />
                        {{ t('dashboard.zoneActions.default') }}
                    </label>
                </div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showCreateModal = false">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="creating || !createForm.name">
                    <Loader2 v-if="creating" :size="14" class="spinning" />
                    {{ creating ? t('messages.creating') : t('actions.create') }}
                </button>
            </template>
        </BaseModal>

        <!-- Edit Modal -->
        <BaseModal
            :show="showEditModal"
            :title="`${t('dashboard.zoneActions.editTitle')} - ${editingZone?.name ?? ''}`"
            :loading="editing"
            form
            @close="showEditModal = false"
            @submit="handleEdit"
        >
            <div class="form-stack">
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.zoneActions.remark') }}</label>
                    <input type="text" v-model="editForm.remark" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer">
                        <input type="checkbox" v-model="editForm.default" />
                        {{ t('dashboard.zoneActions.default') }}
                    </label>
                </div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showEditModal = false">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="editing">
                    <Loader2 v-if="editing" :size="14" class="spinning" />
                    {{ editing ? t('messages.saving') : t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <!-- Delete Confirm Modal -->
        <DeleteModal
            :show="showDeleteModal"
            :title="t('dashboard.zoneActions.deleteTitle')"
            :message="t('dashboard.zoneActions.deleteConfirm', { name: deletingZone?.name })"
            :loading="deleting"
            @close="showDeleteModal = false"
            @confirm="handleDelete"
        />
    </div>
</template>

<style scoped>
/* Standardized resource-info is global from index.css */

.resource-icon {
    width: 32px;
    height: 32px;
    background: var(--primary-light);
    color: var(--primary-color);
    border-radius: var(--radius-sm);
    display: flex;
    align-items: center;
    justify-content: center;
}

.resource-name {
    font-weight: var(--font-weight-semibold);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
}

.resource-id {
    font-size: var(--font-size-xs);
    color: var(--text-light);
    font-family: var(--font-family-mono);
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

.remark-cell {
    max-width: 250px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

.badge {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-xs);
    font-weight: 500;
}
.badge-primary {
    background: rgba(14, 165, 233, 0.1);
    color: var(--primary-color);
}
.badge-secondary {
    background: var(--gray-100);
    color: var(--gray-600);
}

.table-actions {
    display: flex;
    gap: 8px;
}

.icon-btn-table {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    border: none;
    background: transparent;
    color: var(--text-tertiary);
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: all 0.2s;
}
.icon-btn-table:hover {
    background-color: var(--bg-tertiary);
    color: var(--primary-500);
}
.icon-btn-table.text-error:hover {
    background-color: var(--error-light);
    color: var(--error-color);
}

.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
}

/* Modal */
.form-stack {
    display: flex;
    flex-direction: column;
    gap: 16px;
}
.form-group {
    display: flex;
    flex-direction: column;
}
.form-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: 4px;
    font-weight: 500;
}

.form-input {
    width: 100%;
    padding: 8px 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    background: var(--bg-primary);
    color: var(--text-primary);
}
.form-input:focus {
    outline: none;
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.spinning {
    animation: spin 1s linear infinite;
}
@keyframes spin {
    to {
        transform: rotate(360deg);
    }
}
</style>
