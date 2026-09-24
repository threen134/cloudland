<script setup lang="ts">
// Storage pools of the region (§8.3 of the local storage plan): a pool is a name that hosts set up with their own
// disks; this page lists them with how many hosts have them and their summed capacity.
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, RefreshCw, Pencil, Trash2, Database, Search as SearchIcon, Check } from 'lucide-vue-next'
import { storagePoolsApi, type StoragePool, type PoolMedia } from '../../api/storagePools'
import { useListQuery } from '../../composables/useListQuery'
import { useRegionStore } from '../../stores/region'
import { useToast } from '../../composables/useToast'
import { errorMessage } from '../../utils/error'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import CapacityBar from '../../components/storage/CapacityBar.vue'

const { t } = useI18n()
const toast = useToast()
const region = useRegionStore()

const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.name') },
    { key: 'media', label: t('storage.media') },
    { key: 'fallback', label: t('storage.fallbackGroup') },
    { key: 'hosts', label: t('storage.hostsHaving') },
    { key: 'capacity', label: t('storage.capacity') },
    { key: 'status', label: t('storage.status') },
    { key: 'actions', label: t('dashboard.table.actions'), width: '100px', align: 'right' },
])

const {
    items: pools,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    load,
    reload,
} = useListQuery<StoragePool>(
    async ({ offset, limit, query }) => {
        const response = await storagePoolsApi.list({ offset, limit, query: query || undefined })
        return { items: response.storage_pools || [], total: response.total ?? 0 }
    },
    { defaultOrder: 'name', watchSources: [computed(() => region.currentRegionId)] }
)

// ---- create / edit ----
const showForm = ref(false)
const editing = ref<StoragePool | null>(null)
const saving = ref(false)
const form = ref({
    name: '',
    media: 'hdd' as PoolMedia,
    fallback_group: '',
    over_ratio: 1,
    is_default: false,
    status: 'active' as 'active' | 'disabled',
    description: '',
})
const openCreate = () => {
    editing.value = null
    form.value = {
        name: '',
        media: 'hdd',
        fallback_group: '',
        over_ratio: 1,
        is_default: false,
        status: 'active',
        description: '',
    }
    showForm.value = true
}
const openEdit = (p: StoragePool) => {
    editing.value = p
    form.value = {
        name: p.name,
        media: p.media,
        fallback_group: p.fallback_group || '',
        over_ratio: p.over_ratio || 1,
        is_default: p.is_default,
        status: p.status || 'active',
        description: p.description || '',
    }
    showForm.value = true
}
const save = async () => {
    if (!form.value.name) return
    saving.value = true
    try {
        if (editing.value) {
            const f = form.value
            await storagePoolsApi.update(editing.value.id, {
                name: f.name,
                media: editing.value.builtin ? undefined : f.media,
                fallback_group: f.fallback_group,
                over_ratio: editing.value.builtin ? undefined : Number(f.over_ratio),
                is_default: f.is_default,
                status: f.status,
                description: f.description,
            })
        } else {
            await storagePoolsApi.create({
                name: form.value.name,
                media: form.value.media,
                fallback_group: form.value.fallback_group || undefined,
                over_ratio: Number(form.value.over_ratio),
                is_default: form.value.is_default,
                description: form.value.description || undefined,
            })
        }
        showForm.value = false
        toast.success(t('messages.success'))
        await reload()
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    } finally {
        saving.value = false
    }
}

// ---- delete ----
const deleting = ref<StoragePool | null>(null)
const deleteBusy = ref(false)
const remove = async () => {
    if (!deleting.value) return
    deleteBusy.value = true
    try {
        await storagePoolsApi.delete(deleting.value.id)
        deleting.value = null
        await load()
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    } finally {
        deleteBusy.value = false
    }
}

const mediaText = (m: string) => (m ? m.toUpperCase() : '-')

onMounted(() => {
    if (region.currentRegionId) load()
})
</script>

<template>
    <div class="vpc-list-container">
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button class="btn btn-secondary btn-sm btn-icon" :title="t('actions.refresh')" @click="() => load()">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreate">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="pools"
            row-key="id"
            :loading="loading"
            :error="loadError"
            @retry="() => load()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ t('messages.noResults') }}</p>
                </div>
                <div v-else class="empty-state">
                    <Database :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p>{{ t('messages.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: p }">
                <router-link :to="{ name: 'storage-pool-detail', params: { id: p.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon"><Database :size="16" /></div>
                        <div>
                            <div class="resource-name">
                                {{ p.name }}
                                <span class="badge badge-secondary">{{
                                    p.shared ? t('storage.shared') : t('storage.local')
                                }}</span>
                                <span v-if="p.is_default" class="badge badge-primary"
                                    ><Check :size="12" /> {{ t('storage.default') }}</span
                                >
                            </div>
                            <div class="resource-id-row">
                                <span class="resource-id">{{
                                    p.builtin ? t('storage.builtinNote') : p.description || p.id.slice(0, 8)
                                }}</span>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-media="{ row: p }">{{ p.builtin ? '-' : mediaText(p.media) }}</template>
            <template #cell-fallback="{ row: p }">{{ p.fallback_group || '-' }}</template>
            <template #cell-hosts="{ row: p }">{{ p.available_hosts }} / {{ p.hosts ?? '-' }}</template>
            <template #cell-capacity="{ row: p }">
                <CapacityBar
                    v-if="p.capacity_bytes"
                    :capacity="p.capacity_bytes"
                    :used="p.used_bytes || 0"
                    :allocated="p.allocated_bytes || 0"
                />
                <span v-else class="text-secondary">-</span>
            </template>
            <template #cell-status="{ row: p }">
                <StatusBadge :status="p.status" :label="t(`storage.poolStatus.${p.status || 'active'}`)" />
            </template>
            <template #cell-actions="{ row: p }">
                <div class="row-actions">
                    <button class="icon-btn-table" :title="t('actions.edit')" @click.prevent="openEdit(p)">
                        <Pencil :size="16" />
                    </button>
                    <button
                        class="icon-btn-table icon-danger"
                        :title="p.builtin ? t('storage.builtinNoDelete') : t('actions.delete')"
                        :disabled="p.builtin"
                        @click.prevent="deleting = p"
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

        <BaseModal
            :show="showForm"
            :title="editing ? t('storage.editPoolTitle', { name: editing.name }) : t('storage.createPoolTitle')"
            :loading="saving"
            form
            @close="showForm = false"
            @submit="save"
        >
            <div class="form-stack">
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.name') }} *</label>
                    <input v-model="form.name" type="text" class="form-input" maxlength="64" required />
                </div>
                <div v-if="!editing?.builtin" class="form-group">
                    <label class="form-label">{{ t('storage.media') }}</label>
                    <select v-model="form.media" class="form-input">
                        <option value="hdd">HDD</option>
                        <option value="ssd">SSD</option>
                        <option value="nvme">NVMe</option>
                    </select>
                    <span class="form-hint">{{ t('storage.mediaHint') }}</span>
                </div>
                <div v-if="!editing?.builtin" class="form-group">
                    <label class="form-label">{{ t('storage.fallbackGroup') }}</label>
                    <input v-model="form.fallback_group" type="text" class="form-input" maxlength="64" />
                    <span class="form-hint">{{ t('storage.fallbackHint') }}</span>
                </div>
                <div v-if="!editing?.builtin" class="form-group">
                    <label class="form-label">{{ t('storage.overRatio') }}</label>
                    <input v-model="form.over_ratio" type="number" min="0.1" max="20" step="0.1" class="form-input" />
                    <span class="form-hint">{{ t('storage.overRatioHint') }}</span>
                </div>
                <div v-if="editing" class="form-group">
                    <label class="form-label">{{ t('storage.status') }}</label>
                    <select v-model="form.status" class="form-input">
                        <option value="active">{{ t('storage.poolStatus.active') }}</option>
                        <option value="disabled">{{ t('storage.poolStatus.disabled') }}</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="checkbox-inline">
                        <input v-model="form.is_default" type="checkbox" />
                        {{ t('storage.setDefault') }}
                    </label>
                    <span class="form-hint">{{ t('storage.defaultHint') }}</span>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.description') }}</label>
                    <input v-model="form.description" type="text" class="form-input" maxlength="256" />
                </div>
            </div>
            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showForm = false">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="saving || !form.name">
                    {{ t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleting !== null"
            :title="t('storage.deletePoolTitle')"
            :message="t('storage.deletePoolMessage', { name: deleting?.name })"
            :loading="deleteBusy"
            @close="deleting = null"
            @confirm="remove"
        />
    </div>
</template>

<style scoped>
.form-stack {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
}

.form-hint {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: 4px;
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.resource-name .badge {
    margin-left: 6px;
    font-size: 0.6875rem;
}
</style>
