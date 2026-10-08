<script setup lang="ts">
// Transit gateways of the organization (vpc-transit-gateway-plan.md §5 T4): VPCs of this region attached to the
// same gateway reach each other over their internal networks. A gateway can be deleted once no VPC is attached.
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Waypoints, Plus, Pencil, Trash2, Search, RefreshCw, Check, Copy } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { useRegionStore } from '../../stores/region'
import { transitGatewaysApi, type TransitGateway } from '../../api/transitGateways'
import { tgwStatusText } from '../../utils/transitGateway'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import { formatToMinute } from '../../utils/format'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import TransitGatewayEditModal from '../../components/transitGateway/TransitGatewayEditModal.vue'

const { t, te } = useI18n()
const toast = useToast()
const region = useRegionStore()
const { copiedId, copyId } = useCopyId()

// ─── List ────────────────────────────────────────────────────────────────────
// Sortable columns are the real columns of transit_gateways: name, created_at. The attachment count is computed
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'attachment_count', label: t('dashboard.transitGateway.attachedVpcs'), align: 'center' },
    { key: 'description', label: t('dashboard.table.description'), hideBelow: 1280 },
    { key: 'created_at', label: t('dashboard.table.createdAt'), sortable: true, hideBelow: 1024 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const {
    items: gateways,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchGateways,
    reload: reloadGateways,
} = useListQuery<TransitGateway>(
    async ({ offset, limit, query, order }) => {
        const response = await transitGatewaysApi.list({ offset, limit, order, query: query || undefined })
        return { items: response.transit_gateways || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

// ─── Create ──────────────────────────────────────────────────────────────────
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newForm = ref({ name: '', description: '' })
const isNameValid = computed(() => isValidName(newForm.value.name))

const openCreateModal = () => {
    createError.value = ''
    newForm.value = { name: '', description: '' }
    createModalVisible.value = true
}
const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreate = async () => {
    createError.value = ''
    const f = newForm.value
    if (!f.name || !isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }
    creating.value = true
    try {
        await transitGatewaysApi.create({ name: f.name, description: f.description || undefined })
        // Newest first: the new gateway is on the first page
        await reloadGateways()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create transit gateway:', err)
        createError.value = errorMessage(err, t('messages.error'))
    } finally {
        creating.value = false
    }
}

// ─── Edit (name and description) ─────────────────────────────────────────────
const editing = ref<TransitGateway | null>(null)
const onEdited = async () => {
    editing.value = null
    await fetchGateways()
}

// ─── Delete ──────────────────────────────────────────────────────────────────
const deleteTarget = ref<TransitGateway | null>(null)
const deleting = ref(false)
const deleteError = ref('')
const deleteBlockedReason = (g: TransitGateway) =>
    g.attachment_count > 0 ? t('dashboard.transitGateway.deleteHasAttachments', { n: g.attachment_count }) : ''

const openDelete = (g: TransitGateway) => {
    deleteTarget.value = g
    deleteError.value = ''
}
const closeDelete = () => {
    deleteTarget.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!deleteTarget.value) return
    deleting.value = true
    deleteError.value = ''
    try {
        await transitGatewaysApi.delete(deleteTarget.value.id)
        await fetchGateways()
        closeDelete()
        toast.success(t('messages.deleteSuccess'))
    } catch (err) {
        console.error('Failed to delete transit gateway:', err)
        // 409 (133003): a VPC was attached since the list was loaded
        deleteError.value = errorMessage(err, t('messages.error'))
    } finally {
        deleting.value = false
    }
}

onMounted(() => {
    // useListQuery only reloads on changes: the first page must be requested explicitly
    if (region.currentRegionId) fetchGateways()
})
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    :title="$t('actions.refresh')"
                    @click="fetchGateways()"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.transitGateway.create') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="gateways"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="fetchGateways()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else class="empty-block">
                    <Waypoints :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p class="text-secondary">{{ $t('dashboard.transitGateway.empty') }}</p>
                    <p class="text-tertiary empty-hint">{{ $t('dashboard.transitGateway.emptyHint') }}</p>
                    <p class="text-tertiary empty-hint">{{ $t('dashboard.transitGateway.securityGroupHint') }}</p>
                    <button class="btn btn-primary btn-sm empty-action" @click="openCreateModal">
                        <Plus :size="14" /> {{ $t('dashboard.transitGateway.create') }}
                    </button>
                </div>
            </template>

            <template #cell-name="{ row: g }">
                <router-link :to="{ name: 'transit-gateway-detail', params: { id: g.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <Waypoints :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ g.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="g.id">{{ g.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                    @click.stop.prevent="copyId(g.id)"
                                >
                                    <Check v-if="copiedId === g.id" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-status="{ row: g }">
                <StatusBadge :status="g.status" :label="tgwStatusText(t, te, g.status)" />
            </template>

            <template #cell-attachment_count="{ row: g }">{{ g.attachment_count ?? 0 }}</template>

            <template #cell-description="{ row: g }">
                <span v-if="g.description" class="cell-desc" :title="g.description">{{ g.description }}</span>
                <span v-else class="text-tertiary">-</span>
            </template>

            <template #cell-created_at="{ row: g }">
                <span class="cell-time" :title="g.created_at">{{ formatToMinute(g.created_at) }}</span>
            </template>

            <template #cell-actions="{ row: g }">
                <div class="row-actions">
                    <button class="icon-btn-table" :title="$t('actions.edit')" @click.prevent="editing = g">
                        <Pencil :size="16" />
                    </button>
                    <button
                        class="icon-btn-table icon-danger"
                        :title="deleteBlockedReason(g) || $t('actions.delete')"
                        :disabled="g.attachment_count > 0"
                        @click.prevent="openDelete(g)"
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

        <!-- Create modal -->
        <BaseModal
            :show="createModalVisible"
            :title="$t('dashboard.transitGateway.create')"
            :loading="creating"
            form
            @close="closeCreateModal"
            @submit="handleCreate"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                <input
                    v-model="newForm.name"
                    type="text"
                    maxlength="32"
                    :class="['form-input', { 'input-error': !isNameValid }]"
                    :placeholder="$t('dashboard.transitGateway.namePlaceholder')"
                />
                <div v-if="!isNameValid" class="field-error">{{ $t('messages.invalidHostname') }}</div>
            </div>

            <div class="form-group">
                <label class="form-label"
                    >{{ $t('dashboard.forms.description') }} ({{ $t('dashboard.forms.optional') }})</label
                >
                <input
                    v-model="newForm.description"
                    type="text"
                    maxlength="255"
                    class="form-input"
                    :placeholder="$t('messages.placeholderDescription')"
                />
            </div>

            <p class="form-hint">{{ $t('dashboard.transitGateway.createHint') }}</p>

            <template #footer>
                <div v-if="createError" class="footer-error">{{ createError }}</div>
                <button type="button" class="btn btn-secondary" :disabled="creating" @click="closeCreateModal">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="creating || !newForm.name">
                    <span
                        v-if="creating"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ creating ? $t('messages.creating') : $t('dashboard.transitGateway.create') }}
                </button>
            </template>
        </BaseModal>

        <TransitGatewayEditModal :gateway="editing" @close="editing = null" @saved="onEdited" />

        <DeleteModal
            :show="deleteTarget !== null"
            :resource-name="deleteTarget?.name"
            :resource-id="deleteTarget?.id"
            :loading="deleting"
            :error="deleteError"
            @close="closeDelete"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
/* .resource-info, .resource-link, .row-actions, .icon-btn-table, .badge-* are global (index.css) */

.cell-desc {
    display: inline-block;
    max-width: 360px;
    overflow: hidden;
    color: var(--text-secondary);
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
}

.empty-block {
    max-width: 560px;
    margin: 0 auto;
}

.empty-hint {
    margin-top: var(--spacing-2);
    font-size: var(--font-size-sm);
    line-height: 1.6;
}

.empty-action {
    margin-top: var(--spacing-4);
}

.field-error {
    margin-top: 4px;
    font-size: var(--font-size-xs);
    color: var(--error-color);
}

.form-hint {
    margin: 0;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}
</style>
