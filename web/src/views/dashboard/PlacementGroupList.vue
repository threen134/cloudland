<script setup lang="ts">
// Placement groups of the organization (placement-group-plan.md §8): a group spreads its instances over
// different hosts or packs them on one, strictly or best effort. Only the name and the description can change
// after creation.
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Boxes, Plus, Pencil, Trash2, Search, RefreshCw, Check, Copy } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { useRegionStore } from '../../stores/region'
import { zonesApi, type Zone } from '../../api/zones'
import { placementGroupsApi, type PlacementGroup, type PlacementPolicy } from '../../api/placementGroups'
import { policyText, strictText } from '../../utils/placementGroup'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import { formatToMinute } from '../../utils/format'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PlacementGroupEditModal from '../../components/placementGroup/PlacementGroupEditModal.vue'
import { OPTION_LIST_LIMIT } from '../../api/listParams'

const { t, te } = useI18n()
const toast = useToast()
const region = useRegionStore()
const { copiedId, copyId } = useCopyId()

// ─── Zones: filter of the list and zone of a new group ──────────────────────
const zones = ref<Zone[]>([])
const zoneFilter = ref('')
const fetchZones = async () => {
    try {
        const res = await zonesApi.fetchZones({ limit: OPTION_LIST_LIMIT })
        zones.value = res.zones || []
    } catch (err) {
        console.error('Failed to fetch zones:', err)
        zones.value = []
    }
}
const defaultZoneName = () => (zones.value.find((z) => z.default) || zones.value[0])?.name || ''

// ─── List ────────────────────────────────────────────────────────────────────
// Sortable columns are the real columns of placement_groups: name, policy, created_at. The zone is a foreign
// key and the counts are computed, so those are not sortable
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'policy', label: t('dashboard.placementGroup.policy'), sortable: true },
    { key: 'zone', label: t('dashboard.table.zone') },
    { key: 'member_count', label: t('dashboard.placementGroup.memberCount'), align: 'center' },
    { key: 'distribution', label: t('dashboard.placementGroup.distribution') },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'created_at', label: t('dashboard.table.createdAt'), sortable: true, hideBelow: 1280 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const {
    items: groups,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchGroups,
    reload: reloadGroups,
} = useListQuery<PlacementGroup>(
    async ({ offset, limit, query, order }) => {
        const response = await placementGroupsApi.list({
            offset,
            limit,
            order,
            query: query || undefined,
            zone: zoneFilter.value || undefined,
        })
        return { items: response.placement_groups || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId), zoneFilter] }
)

const compliantLabel = (g: PlacementGroup) =>
    g.compliant ? t('dashboard.placementGroup.compliant') : t('dashboard.placementGroup.notCompliant')

// ─── Create ──────────────────────────────────────────────────────────────────
const POLICIES: PlacementPolicy[] = ['spread', 'pack']
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const emptyForm = () => ({
    name: '',
    description: '',
    policy: 'spread' as PlacementPolicy,
    // UI default: spread strict, pack best effort (a strict pack group is blocked as soon as its host is full)
    strict: true,
    zone: defaultZoneName(),
})
const newForm = ref(emptyForm())
const isNameValid = computed(() => isValidName(newForm.value.name))

// Switching the policy resets the strictness to that policy's default (§2.1)
watch(
    () => newForm.value.policy,
    (policy) => {
        newForm.value.strict = policy === 'spread'
    }
)

const strictHint = computed(() => {
    const f = newForm.value
    return t(`dashboard.placementGroup.strictHints.${f.policy}${f.strict ? 'Strict' : 'Soft'}`)
})

const openCreateModal = async () => {
    createError.value = ''
    newForm.value = emptyForm()
    createModalVisible.value = true
    if (!zones.value.length) {
        await fetchZones()
        if (!newForm.value.zone) newForm.value.zone = defaultZoneName()
    }
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
        await placementGroupsApi.create({
            name: f.name,
            description: f.description || undefined,
            policy: f.policy,
            strict: f.strict,
            zone: f.zone || undefined,
        })
        // Newest first: the new group is on the first page
        await reloadGroups()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create placement group:', err)
        createError.value = errorMessage(err, t('messages.error'))
    } finally {
        creating.value = false
    }
}

// ─── Edit (name and description only) ────────────────────────────────────────
const editing = ref<PlacementGroup | null>(null)
const onEdited = async () => {
    editing.value = null
    await fetchGroups()
}

// ─── Delete ──────────────────────────────────────────────────────────────────
const deleteTarget = ref<PlacementGroup | null>(null)
const deleting = ref(false)
const deleteError = ref('')
const deleteBlockedReason = (g: PlacementGroup) =>
    g.member_count > 0 ? t('dashboard.placementGroup.deleteHasMembers', { n: g.member_count }) : ''

const openDelete = (g: PlacementGroup) => {
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
        await placementGroupsApi.delete(deleteTarget.value.id)
        await fetchGroups()
        closeDelete()
        toast.success(t('messages.deleteSuccess'))
    } catch (err) {
        console.error('Failed to delete placement group:', err)
        // 409 ErrPlacementGroupInUse: a member was added since the list was loaded
        deleteError.value = errorMessage(err, t('messages.error'))
    } finally {
        deleting.value = false
    }
}

onMounted(() => {
    fetchZones()
    // useListQuery only reloads on changes: the first page must be requested explicitly
    if (region.currentRegionId) fetchGroups()
})
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #filters>
                <select v-model="zoneFilter" class="filter-select" :aria-label="$t('dashboard.table.zone')">
                    <option value="">{{ $t('dashboard.placementGroup.allZones') }}</option>
                    <option v-for="z in zones" :key="z.id" :value="z.name">{{ z.name }}</option>
                </select>
            </template>
            <template #actions>
                <button class="btn btn-secondary btn-sm btn-icon" :title="$t('actions.refresh')" @click="fetchGroups()">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.placementGroup.create') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="groups"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="fetchGroups()"
        >
            <template #empty>
                <div v-if="searchQuery || zoneFilter">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <Boxes :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p class="text-secondary">{{ $t('dashboard.placementGroup.empty') }}</p>
                    <p class="text-tertiary empty-hint">{{ $t('dashboard.placementGroup.emptyHint') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: g }">
                <router-link :to="{ name: 'placement-group-detail', params: { id: g.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <Boxes :size="16" />
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
                            <div v-if="g.description" class="resource-desc" :title="g.description">
                                {{ g.description }}
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-policy="{ row: g }">
                <span class="policy-cell">
                    <span class="badge" :class="g.policy === 'pack' ? 'badge-info' : 'badge-primary'">{{
                        policyText(t, te, g.policy)
                    }}</span>
                    <span class="badge" :class="g.strict ? 'badge-warning' : 'badge-secondary'">{{
                        strictText(t, g.strict)
                    }}</span>
                </span>
            </template>

            <template #cell-zone="{ row: g }">{{ g.zone || '-' }}</template>
            <template #cell-member_count="{ row: g }">{{ g.member_count ?? 0 }}</template>
            <template #cell-distribution="{ row: g }">
                <span v-if="g.host_count > 0" class="nowrap">{{
                    $t('dashboard.placementGroup.hostCount', { n: g.host_count })
                }}</span>
                <span v-else class="text-tertiary">-</span>
            </template>

            <template #cell-status="{ row: g }">
                <StatusBadge :variant="g.compliant ? 'success' : 'warning'" :label="compliantLabel(g)" />
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
                        :disabled="g.member_count > 0"
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
            :title="$t('dashboard.placementGroup.create')"
            :loading="creating"
            size="lg"
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
                    :placeholder="$t('dashboard.placementGroup.namePlaceholder')"
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

            <!-- Policy: two radio cards with what each does and when to use it -->
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.placementGroup.policy') }}</label>
                <div class="mode-options">
                    <label
                        v-for="p in POLICIES"
                        :key="p"
                        class="mode-option"
                        :class="{ selected: newForm.policy === p }"
                    >
                        <input v-model="newForm.policy" type="radio" :value="p" />
                        <span class="mode-option-body">
                            <span class="mode-option-title">{{ policyText(t, te, p) }}</span>
                            <span class="mode-option-desc">{{ $t(`dashboard.placementGroup.policyDesc.${p}`) }}</span>
                            <span class="mode-option-desc">{{
                                $t(`dashboard.placementGroup.policyUseCase.${p}`)
                            }}</span>
                            <span v-if="p === 'pack'" class="mode-option-risk">{{
                                $t('dashboard.placementGroup.packRisk')
                            }}</span>
                        </span>
                    </label>
                </div>
            </div>

            <div class="form-group">
                <div class="switch-row">
                    <span class="form-label mb-0">{{ $t('dashboard.placementGroup.strict') }}</span>
                    <label class="switch">
                        <input v-model="newForm.strict" type="checkbox" />
                        <span class="slider"></span>
                    </label>
                </div>
                <div class="form-hint">{{ strictHint }}</div>
            </div>

            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.zone') }}</label>
                <select v-model="newForm.zone" class="form-input">
                    <option v-if="!zones.length" value="">{{ $t('dashboard.forms.zoneAuto') }}</option>
                    <option v-for="z in zones" :key="z.id" :value="z.name">
                        {{ z.name }}{{ z.default ? ` · ${$t('dashboard.forms.zoneDefaultTag')}` : '' }}
                    </option>
                </select>
                <div class="form-hint">{{ $t('dashboard.placementGroup.zoneHint') }}</div>
            </div>

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
                    {{ creating ? $t('messages.creating') : $t('dashboard.placementGroup.create') }}
                </button>
            </template>
        </BaseModal>

        <!-- Edit modal: the policy, the strictness and the zone are fixed at creation -->
        <PlacementGroupEditModal :group="editing" @close="editing = null" @saved="onEdited" />

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

.resource-desc {
    max-width: 320px;
    margin-top: 2px;
    overflow: hidden;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    text-overflow: ellipsis;
    white-space: nowrap;
}

.policy-cell {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-1);
    white-space: nowrap;
}

.nowrap {
    white-space: nowrap;
}

.empty-hint {
    margin-top: var(--spacing-2);
    font-size: var(--font-size-sm);
}

/* The error sits in the footer next to the buttons: in the body it can end up below the fold */
.footer-error {
    flex: 1;
    align-self: center;
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--error-light);
    color: var(--error-color);
    font-size: var(--font-size-sm);
}

.field-error {
    margin-top: 4px;
    font-size: var(--font-size-xs);
    color: var(--error-color);
}

.form-hint {
    margin-top: 4px;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}

/* Policy: two radio cards */
.mode-options {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-3);
}

.mode-option {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-2);
    padding: var(--spacing-3);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: border-color 0.15s;
}

.mode-option:hover {
    border-color: var(--primary-color);
}

.mode-option.selected {
    border-color: var(--primary-color);
    background: var(--primary-50);
}

.mode-option input {
    flex-shrink: 0;
    margin-top: 3px;
}

.mode-option-body {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
}

.mode-option-title {
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    color: var(--text-primary);
}

.mode-option-desc {
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-secondary);
}

.mode-option-risk {
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--warning-dark);
}

/* Strict: a toggle switch next to its label */
.switch-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

.switch {
    position: relative;
    display: inline-block;
    flex-shrink: 0;
    width: 44px;
    height: 24px;
}

.switch input {
    width: 0;
    height: 0;
    opacity: 0;
}

.slider {
    position: absolute;
    inset: 0;
    border-radius: 24px;
    background-color: var(--gray-300);
    cursor: pointer;
    transition: 0.2s;
}

.slider::before {
    position: absolute;
    bottom: 3px;
    left: 3px;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background-color: var(--bg-primary);
    content: '';
    transition: 0.2s;
}

.switch input:checked + .slider {
    background-color: var(--primary-color);
}

.switch input:focus-visible + .slider {
    box-shadow: 0 0 0 2px var(--primary-100);
}

.switch input:checked + .slider::before {
    transform: translateX(20px);
}

/* Phones: the policy cards one above the other */
@media (max-width: 640px) {
    .mode-options {
        grid-template-columns: 1fr;
    }
}
</style>
