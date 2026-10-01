<script setup lang="ts">
// Route tables tab of a transit gateway: the tables on the left, the selected one on the right with the VPCs routed
// by it (associations), the VPCs whose subnets it learns (propagations, optionally filtered by prefixes) and its
// static routes / blackholes. Every change bumps the generation of the gateway, so the parent reloads everything.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Pencil, Trash2, Table2, Copy, Check } from 'lucide-vue-next'
import {
    transitGatewaysApi,
    type TgwAttachment,
    type TgwPropagation,
    type TgwRoute,
    type TgwRouteTable,
} from '../../api/transitGateways'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { formatDateTime } from '../../utils/format'
import { isValidName, isValidCIDRv4 } from '../../utils/validation'
import {
    parseCidrList,
    invalidCidrs,
    isDefaultRoute,
    routeTypeText,
    routeTypeBadge,
    MAX_PROPAGATION_PREFIXES,
} from '../../utils/transitGateway'
import BaseModal from '../modals/BaseModal.vue'
import DeleteModal from '../modals/DeleteModal.vue'
import InfoRow from '../base/InfoRow.vue'
import DataTable, { type Column } from '../base/DataTable.vue'

const props = defineProps<{
    gatewayId: string
    routeTables: TgwRouteTable[]
    attachments: TgwAttachment[]
    /** Id of the selected table */
    modelValue: string
}>()
const emit = defineEmits<{ 'update:modelValue': [id: string]; changed: [] }>()

const { t, te } = useI18n()
const toast = useToast()
const { copiedId, copyId } = useCopyId()

// Default table first, then by creation
const tables = computed(() =>
    [...props.routeTables].sort(
        (a, b) => Number(b.is_default) - Number(a.is_default) || a.created_at.localeCompare(b.created_at)
    )
)
const selected = computed(() => props.routeTables.find((rt) => rt.id === props.modelValue) || tables.value[0] || null)

// Keep a valid selection: the default table first, another one when the selected table was deleted
watch(
    tables,
    (list) => {
        if (list.length && !list.some((rt) => rt.id === props.modelValue)) emit('update:modelValue', list[0].id)
    },
    { immediate: true }
)

const select = (rt: TgwRouteTable) => emit('update:modelValue', rt.id)

// Attachments that can still be routed or propagated: one being detached is refused by the backend (133015)
const usableAttachments = computed(() => props.attachments.filter((a) => a.status !== 'detaching'))

const tableDeleteBlocked = (rt: TgwRouteTable) => {
    if (rt.is_default) return t('dashboard.transitGateway.defaultTableNotDeletable')
    if (rt.associations.length) return t('dashboard.transitGateway.tableInUse', { n: rt.associations.length })
    return ''
}

// ─── Propagations and routes tables ──────────────────────────────────────────
const propagationColumns = computed<Column[]>(() => [
    { key: 'vpc', label: t('dashboard.transitGateway.vpc') },
    { key: 'prefixes', label: t('dashboard.transitGateway.prefixes') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center', width: '80px' },
])
const routeColumns = computed<Column[]>(() => [
    { key: 'destination', label: t('dashboard.transitGateway.destination') },
    { key: 'type', label: t('dashboard.table.type') },
    { key: 'target', label: t('dashboard.transitGateway.target') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center', width: '80px' },
])

// ─── Create / rename a table ─────────────────────────────────────────────────
const nameModal = ref<{ table: TgwRouteTable | null } | null>(null)
const nameForm = ref('')
const nameSaving = ref(false)
const nameError = ref('')
const isTableNameValid = computed(() => isValidName(nameForm.value))

const openCreateTable = () => {
    nameModal.value = { table: null }
    nameForm.value = ''
    nameError.value = ''
}
const openRenameTable = () => {
    const rt = selected.value
    if (!rt) return
    nameModal.value = { table: rt }
    nameForm.value = rt.name
    nameError.value = ''
}
const closeNameModal = () => {
    nameModal.value = null
    nameError.value = ''
}
const saveName = async () => {
    if (!nameModal.value) return
    nameError.value = ''
    if (!nameForm.value || !isTableNameValid.value) {
        nameError.value = t('messages.invalidHostname')
        return
    }
    nameSaving.value = true
    try {
        const editing = nameModal.value.table
        if (editing) {
            await transitGatewaysApi.renameRouteTable(props.gatewayId, editing.id, nameForm.value)
            toast.success(t('messages.updateSuccess'))
        } else {
            const created = await transitGatewaysApi.createRouteTable(props.gatewayId, nameForm.value)
            emit('update:modelValue', created.id)
            toast.success(t('messages.createSuccess'))
        }
        closeNameModal()
        emit('changed')
    } catch (err) {
        console.error('Failed to save route table:', err)
        nameError.value = errorMessage(err, t('messages.error'))
    } finally {
        nameSaving.value = false
    }
}

// ─── Add a propagation ───────────────────────────────────────────────────────
const propagationModal = ref(false)
const propagationForm = ref({ attachmentId: '', prefixes: '' })
const propagationSaving = ref(false)
const propagationError = ref('')
// An attachment propagates into a table at most once (409 133042)
const propagationCandidates = computed(() => {
    const taken = new Set((selected.value?.propagations || []).map((p) => p.attachment?.id))
    return usableAttachments.value.filter((a) => !taken.has(a.id))
})
const candidateSubnets = computed(
    () => props.attachments.find((a) => a.id === propagationForm.value.attachmentId)?.subnets || []
)

const openAddPropagation = () => {
    propagationForm.value = { attachmentId: propagationCandidates.value[0]?.id || '', prefixes: '' }
    propagationError.value = ''
    propagationModal.value = true
}
const closePropagationModal = () => {
    propagationModal.value = false
    propagationError.value = ''
}
const savePropagation = async () => {
    const table = selected.value
    if (!table) return
    propagationError.value = ''
    if (!propagationForm.value.attachmentId) {
        propagationError.value = t('dashboard.transitGateway.selectVpcRequired')
        return
    }
    const prefixes = parseCidrList(propagationForm.value.prefixes)
    const bad = invalidCidrs(prefixes)
    if (bad.length) {
        propagationError.value = t('dashboard.transitGateway.invalidCidrs', { cidrs: bad.join(', ') })
        return
    }
    if (prefixes.length > MAX_PROPAGATION_PREFIXES) {
        propagationError.value = t('dashboard.transitGateway.tooManyPrefixes', { n: MAX_PROPAGATION_PREFIXES })
        return
    }
    propagationSaving.value = true
    try {
        await transitGatewaysApi.addPropagation(props.gatewayId, table.id, {
            attachment: { id: propagationForm.value.attachmentId },
            prefixes: prefixes.length ? prefixes : undefined,
        })
        toast.success(t('messages.createSuccess'))
        closePropagationModal()
        emit('changed')
    } catch (err) {
        console.error('Failed to add propagation:', err)
        propagationError.value = errorMessage(err, t('messages.error'))
    } finally {
        propagationSaving.value = false
    }
}

// ─── Add a static route ──────────────────────────────────────────────────────
const routeModal = ref(false)
const routeForm = ref({ destination: '', target: 'attachment' as 'attachment' | 'blackhole', attachmentId: '' })
const routeSaving = ref(false)
const routeError = ref('')
const destinationError = computed(() => {
    const d = routeForm.value.destination.trim()
    if (!d) return ''
    if (!isValidCIDRv4(d)) return t('dashboard.transitGateway.invalidCidr')
    if (isDefaultRoute(d)) return t('dashboard.transitGateway.defaultRouteRefused')
    return ''
})

const openAddRoute = () => {
    routeForm.value = { destination: '', target: 'attachment', attachmentId: usableAttachments.value[0]?.id || '' }
    routeError.value = ''
    routeModal.value = true
}
const closeRouteModal = () => {
    routeModal.value = false
    routeError.value = ''
}
const saveRoute = async () => {
    const table = selected.value
    if (!table) return
    routeError.value = ''
    const f = routeForm.value
    const destination = f.destination.trim()
    if (!destination || destinationError.value) {
        routeError.value = destinationError.value || t('dashboard.transitGateway.invalidCidr')
        return
    }
    if (f.target === 'attachment' && !f.attachmentId) {
        routeError.value = t('dashboard.transitGateway.selectVpcRequired')
        return
    }
    routeSaving.value = true
    try {
        await transitGatewaysApi.addRoute(
            props.gatewayId,
            table.id,
            f.target === 'blackhole'
                ? { destination, blackhole: true }
                : { destination, attachment: { id: f.attachmentId } }
        )
        toast.success(t('messages.createSuccess'))
        closeRouteModal()
        emit('changed')
    } catch (err) {
        console.error('Failed to add route:', err)
        routeError.value = errorMessage(err, t('messages.error'))
    } finally {
        routeSaving.value = false
    }
}

// ─── Delete a table, a propagation or a route ────────────────────────────────
type DeleteTarget =
    | { kind: 'table'; table: TgwRouteTable }
    | { kind: 'propagation'; table: TgwRouteTable; propagation: TgwPropagation }
    | { kind: 'route'; table: TgwRouteTable; route: TgwRoute }
const deleteTarget = ref<DeleteTarget | null>(null)
const deleting = ref(false)
const deleteError = ref('')

const deleteTitle = computed(() => {
    const d = deleteTarget.value
    if (!d) return ''
    if (d.kind === 'table') return t('dashboard.transitGateway.deleteRouteTable')
    if (d.kind === 'propagation') return t('dashboard.transitGateway.deletePropagation')
    return t('dashboard.transitGateway.deleteRoute')
})
const deleteMessage = computed(() => {
    const d = deleteTarget.value
    if (!d) return ''
    if (d.kind === 'table') return t('dashboard.transitGateway.deleteRouteTableMessage')
    if (d.kind === 'propagation') {
        return t('dashboard.transitGateway.deletePropagationMessage', {
            vpc: d.propagation.attachment?.vpc?.name || '-',
            table: d.table.name,
        })
    }
    return t('dashboard.transitGateway.deleteRouteMessage', { destination: d.route.destination, table: d.table.name })
})
const deleteName = computed(() => {
    const d = deleteTarget.value
    if (!d) return undefined
    if (d.kind === 'table') return d.table.name
    if (d.kind === 'propagation') return d.propagation.attachment?.vpc?.name
    return d.route.destination
})

// The handlers read the selection themselves: a template closure does not keep the v-if narrowing
const askDeleteTable = () => {
    if (selected.value) deleteTarget.value = { kind: 'table', table: selected.value }
}
const askDeletePropagation = (propagation: TgwPropagation) => {
    if (selected.value) deleteTarget.value = { kind: 'propagation', table: selected.value, propagation }
}
const askDeleteRoute = (route: TgwRoute) => {
    if (selected.value) deleteTarget.value = { kind: 'route', table: selected.value, route }
}

const closeDelete = () => {
    deleteTarget.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    const d = deleteTarget.value
    if (!d) return
    deleting.value = true
    deleteError.value = ''
    try {
        if (d.kind === 'table') await transitGatewaysApi.deleteRouteTable(props.gatewayId, d.table.id)
        else if (d.kind === 'propagation') {
            await transitGatewaysApi.deletePropagation(props.gatewayId, d.table.id, d.propagation.id)
        } else await transitGatewaysApi.deleteRoute(props.gatewayId, d.table.id, d.route.id)
        toast.success(t('messages.deleteSuccess'))
        closeDelete()
        emit('changed')
    } catch (err) {
        console.error('Failed to delete:', err)
        deleteError.value = errorMessage(err, t('messages.error'))
    } finally {
        deleting.value = false
    }
}
</script>

<template>
    <div class="rt-layout">
        <!-- Tables -->
        <div class="card rt-list">
            <div class="rt-list-header">
                <h3>{{ $t('dashboard.transitGateway.routeTables') }}</h3>
                <!-- Short label: the column is narrow; the full action is in the tooltip -->
                <button
                    class="btn btn-secondary btn-sm"
                    :title="$t('dashboard.transitGateway.createRouteTable')"
                    @click="openCreateTable"
                >
                    <Plus :size="14" /> {{ $t('actions.create') }}
                </button>
            </div>
            <div class="rt-items" role="listbox" :aria-label="$t('dashboard.transitGateway.routeTables')">
                <button
                    v-for="rt in tables"
                    :key="rt.id"
                    type="button"
                    role="option"
                    class="rt-item"
                    :class="{ active: selected?.id === rt.id }"
                    :aria-selected="selected?.id === rt.id"
                    @click="select(rt)"
                >
                    <span class="rt-item-name">
                        <Table2 :size="14" class="rt-item-icon" />
                        <span class="rt-item-text">{{ rt.name }}</span>
                        <span v-if="rt.is_default" class="badge badge-primary">{{
                            $t('dashboard.transitGateway.defaultTag')
                        }}</span>
                    </span>
                    <span class="rt-item-meta">
                        {{
                            $t('dashboard.transitGateway.tableCounts', {
                                associations: rt.associations.length,
                                propagations: rt.propagations.length,
                                routes: rt.routes.length,
                            })
                        }}
                    </span>
                </button>
                <div v-if="!tables.length" class="empty-hint">{{ $t('dashboard.transitGateway.noRouteTables') }}</div>
            </div>
        </div>

        <!-- Selected table -->
        <div v-if="selected" class="rt-detail">
            <div class="card info-card">
                <div class="rt-section-header">
                    <h3>
                        {{ selected.name }}
                        <span v-if="selected.is_default" class="badge badge-primary">{{
                            $t('dashboard.transitGateway.defaultTag')
                        }}</span>
                    </h3>
                    <div class="row-actions">
                        <button
                            class="icon-btn-table"
                            :title="$t('dashboard.transitGateway.renameRouteTable')"
                            @click="openRenameTable"
                        >
                            <Pencil :size="16" />
                        </button>
                        <button
                            class="icon-btn-table icon-danger"
                            :title="tableDeleteBlocked(selected) || $t('dashboard.transitGateway.deleteRouteTable')"
                            :disabled="!!tableDeleteBlocked(selected)"
                            @click="askDeleteTable"
                        >
                            <Trash2 :size="16" />
                        </button>
                    </div>
                </div>
                <div class="key-value-list">
                    <InfoRow :label="$t('dashboard.table.id')" mono>
                        <span class="nowrap">{{ selected.id }}</span>
                        <button
                            class="copy-btn"
                            :title="$t('actions.copy')"
                            :aria-label="$t('actions.copy')"
                            @click="copyId(selected.id, 'rt')"
                        >
                            <Check v-if="copiedId === 'rt'" :size="12" class="copied-icon" />
                            <Copy v-else :size="12" />
                        </button>
                    </InfoRow>
                    <InfoRow :label="$t('dashboard.table.createdAt')">{{
                        formatDateTime(selected.created_at)
                    }}</InfoRow>
                    <InfoRow :label="$t('dashboard.transitGateway.associations')">
                        <div v-if="selected.associations.length" class="chip-list">
                            <router-link
                                v-for="a in selected.associations"
                                :key="a.id"
                                :to="{ name: 'vpc-detail', params: { id: a.vpc?.id } }"
                                class="vpc-chip"
                                >{{ a.vpc?.name || a.id.slice(0, 8) }}</router-link
                            >
                        </div>
                        <span v-else class="text-tertiary">{{ $t('dashboard.transitGateway.noAssociations') }}</span>
                    </InfoRow>
                </div>
                <p class="section-hint">{{ $t('dashboard.transitGateway.associationsHint') }}</p>
            </div>

            <div class="card info-card">
                <div class="rt-section-header">
                    <h3>
                        {{ $t('dashboard.transitGateway.propagations') }}
                        <span class="count-chip">{{ selected.propagations.length }}</span>
                    </h3>
                    <button
                        class="btn btn-secondary btn-sm"
                        :disabled="!propagationCandidates.length"
                        :title="
                            propagationCandidates.length
                                ? undefined
                                : $t('dashboard.transitGateway.noPropagationCandidate')
                        "
                        @click="openAddPropagation"
                    >
                        <Plus :size="14" /> {{ $t('dashboard.transitGateway.addPropagation') }}
                    </button>
                </div>
                <p class="section-hint top">{{ $t('dashboard.transitGateway.propagationsHint') }}</p>
                <DataTable
                    :columns="propagationColumns"
                    :rows="selected.propagations"
                    row-key="id"
                    :empty-text="$t('dashboard.transitGateway.noPropagations')"
                >
                    <template #cell-vpc="{ row: p }">
                        <router-link
                            v-if="p.attachment?.vpc?.id"
                            :to="{ name: 'vpc-detail', params: { id: p.attachment.vpc.id } }"
                            class="text-link"
                            >{{ p.attachment.vpc.name }}</router-link
                        >
                        <span v-else class="text-tertiary">-</span>
                    </template>
                    <template #cell-prefixes="{ row: p }">
                        <div v-if="p.prefixes?.length" class="chip-list">
                            <code v-for="cidr in p.prefixes" :key="cidr" class="cidr-chip">{{ cidr }}</code>
                        </div>
                        <span v-else class="text-secondary">{{ $t('dashboard.transitGateway.allSubnets') }}</span>
                    </template>
                    <template #cell-actions="{ row: p }">
                        <div class="row-actions">
                            <button
                                class="icon-btn-table icon-danger"
                                :title="$t('dashboard.transitGateway.deletePropagation')"
                                @click="askDeletePropagation(p)"
                            >
                                <Trash2 :size="16" />
                            </button>
                        </div>
                    </template>
                </DataTable>
            </div>

            <div class="card info-card">
                <div class="rt-section-header">
                    <h3>
                        {{ $t('dashboard.transitGateway.staticRoutes') }}
                        <span class="count-chip">{{ selected.routes.length }}</span>
                    </h3>
                    <button class="btn btn-secondary btn-sm" @click="openAddRoute">
                        <Plus :size="14" /> {{ $t('dashboard.transitGateway.addRoute') }}
                    </button>
                </div>
                <p class="section-hint top">{{ $t('dashboard.transitGateway.staticRoutesHint') }}</p>
                <DataTable
                    :columns="routeColumns"
                    :rows="selected.routes"
                    row-key="id"
                    :empty-text="$t('dashboard.transitGateway.noStaticRoutes')"
                >
                    <template #cell-destination="{ row: r }">
                        <code class="mono">{{ r.destination }}</code>
                    </template>
                    <template #cell-type="{ row: r }">
                        <span class="badge" :class="routeTypeBadge(r.type)">{{ routeTypeText(t, te, r.type) }}</span>
                    </template>
                    <template #cell-target="{ row: r }">
                        <router-link
                            v-if="r.type === 'static' && r.attachment?.vpc?.id"
                            :to="{ name: 'vpc-detail', params: { id: r.attachment.vpc.id } }"
                            class="text-link"
                            >{{ r.attachment.vpc.name }}</router-link
                        >
                        <span v-else-if="r.type === 'blackhole'" class="text-secondary">{{
                            $t('dashboard.transitGateway.dropped')
                        }}</span>
                        <span v-else class="text-tertiary">-</span>
                    </template>
                    <template #cell-actions="{ row: r }">
                        <div class="row-actions">
                            <button
                                class="icon-btn-table icon-danger"
                                :title="$t('dashboard.transitGateway.deleteRoute')"
                                @click="askDeleteRoute(r)"
                            >
                                <Trash2 :size="16" />
                            </button>
                        </div>
                    </template>
                </DataTable>
            </div>
        </div>

        <!-- Create / rename a table -->
        <BaseModal
            :show="nameModal !== null"
            :title="
                nameModal?.table
                    ? $t('dashboard.transitGateway.renameRouteTable')
                    : $t('dashboard.transitGateway.createRouteTable')
            "
            :loading="nameSaving"
            form
            @close="closeNameModal"
            @submit="saveName"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                <input
                    v-model="nameForm"
                    type="text"
                    maxlength="32"
                    :class="['form-input', { 'input-error': !isTableNameValid }]"
                    :placeholder="$t('dashboard.transitGateway.routeTableNamePlaceholder')"
                />
                <div v-if="!isTableNameValid" class="field-error">{{ $t('messages.invalidHostname') }}</div>
            </div>
            <p v-if="!nameModal?.table" class="form-hint">{{ $t('dashboard.transitGateway.createRouteTableHint') }}</p>
            <template #footer>
                <div v-if="nameError" class="footer-error">{{ nameError }}</div>
                <button type="button" class="btn btn-secondary" :disabled="nameSaving" @click="closeNameModal">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="nameSaving || !nameForm">
                    {{
                        nameSaving
                            ? $t('messages.saving')
                            : nameModal?.table
                              ? $t('actions.save')
                              : $t('actions.create')
                    }}
                </button>
            </template>
        </BaseModal>

        <!-- Add a propagation -->
        <BaseModal
            :show="propagationModal"
            :title="$t('dashboard.transitGateway.addPropagationTitle', { table: selected?.name })"
            :loading="propagationSaving"
            size="lg"
            form
            @close="closePropagationModal"
            @submit="savePropagation"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.transitGateway.vpc') }} *</label>
                <select v-model="propagationForm.attachmentId" class="form-input">
                    <option v-for="a in propagationCandidates" :key="a.id" :value="a.id">
                        {{ a.vpc?.name || a.id }}
                    </option>
                </select>
                <div v-if="candidateSubnets.length" class="form-hint">
                    {{ $t('dashboard.transitGateway.vpcSubnets', { cidrs: candidateSubnets.join(', ') }) }}
                </div>
            </div>
            <div class="form-group">
                <label class="form-label"
                    >{{ $t('dashboard.transitGateway.prefixes') }} ({{ $t('dashboard.forms.optional') }})</label
                >
                <textarea
                    v-model="propagationForm.prefixes"
                    rows="3"
                    class="form-input mono"
                    :placeholder="$t('dashboard.transitGateway.prefixesPlaceholder')"
                ></textarea>
                <div class="form-hint">
                    {{ $t('dashboard.transitGateway.prefixesHint', { n: MAX_PROPAGATION_PREFIXES }) }}
                </div>
            </div>
            <template #footer>
                <div v-if="propagationError" class="footer-error">{{ propagationError }}</div>
                <button
                    type="button"
                    class="btn btn-secondary"
                    :disabled="propagationSaving"
                    @click="closePropagationModal"
                >
                    {{ $t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    class="btn btn-primary"
                    :disabled="propagationSaving || !propagationForm.attachmentId"
                >
                    {{ propagationSaving ? $t('messages.saving') : $t('actions.add') }}
                </button>
            </template>
        </BaseModal>

        <!-- Add a static route -->
        <BaseModal
            :show="routeModal"
            :title="$t('dashboard.transitGateway.addRouteTitle', { table: selected?.name })"
            :loading="routeSaving"
            size="lg"
            form
            @close="closeRouteModal"
            @submit="saveRoute"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.transitGateway.destination') }} *</label>
                <input
                    v-model="routeForm.destination"
                    type="text"
                    :class="['form-input', 'mono', { 'input-error': !!destinationError }]"
                    :placeholder="$t('dashboard.transitGateway.destinationPlaceholder')"
                />
                <div v-if="destinationError" class="field-error">{{ destinationError }}</div>
                <div v-else class="form-hint">{{ $t('dashboard.transitGateway.destinationHint') }}</div>
            </div>
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.transitGateway.target') }}</label>
                <div class="target-options">
                    <label class="target-option" :class="{ selected: routeForm.target === 'attachment' }">
                        <input v-model="routeForm.target" type="radio" value="attachment" />
                        <span>
                            <span class="target-title">{{ $t('dashboard.transitGateway.targetAttachment') }}</span>
                            <span class="target-desc">{{ $t('dashboard.transitGateway.targetAttachmentDesc') }}</span>
                        </span>
                    </label>
                    <label class="target-option" :class="{ selected: routeForm.target === 'blackhole' }">
                        <input v-model="routeForm.target" type="radio" value="blackhole" />
                        <span>
                            <span class="target-title">{{ $t('dashboard.transitGateway.routeTypes.blackhole') }}</span>
                            <span class="target-desc">{{ $t('dashboard.transitGateway.targetBlackholeDesc') }}</span>
                        </span>
                    </label>
                </div>
            </div>
            <div v-if="routeForm.target === 'attachment'" class="form-group">
                <label class="form-label">{{ $t('dashboard.transitGateway.vpc') }} *</label>
                <select v-model="routeForm.attachmentId" class="form-input">
                    <option value="" disabled>{{ $t('dashboard.transitGateway.selectVpc') }}</option>
                    <option v-for="a in usableAttachments" :key="a.id" :value="a.id">{{ a.vpc?.name || a.id }}</option>
                </select>
            </div>
            <template #footer>
                <div v-if="routeError" class="footer-error">{{ routeError }}</div>
                <button type="button" class="btn btn-secondary" :disabled="routeSaving" @click="closeRouteModal">
                    {{ $t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    class="btn btn-primary"
                    :disabled="
                        routeSaving ||
                        !routeForm.destination.trim() ||
                        !!destinationError ||
                        (routeForm.target === 'attachment' && !routeForm.attachmentId)
                    "
                >
                    {{ routeSaving ? $t('messages.saving') : $t('actions.add') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleteTarget !== null"
            :title="deleteTitle"
            :message="deleteMessage"
            :resource-name="deleteName"
            :loading="deleting"
            :error="deleteError"
            @close="closeDelete"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
/* .row-actions, .icon-btn-table, .badge-* are global (index.css) */

.rt-layout {
    display: grid;
    grid-template-columns: 280px minmax(0, 1fr);
    gap: var(--spacing-4);
    align-items: start;
}

/* Below 1024px the table list goes above the selected table */
@media (max-width: 1023px) {
    .rt-layout {
        grid-template-columns: minmax(0, 1fr);
    }
}

.rt-list {
    padding: var(--spacing-4);
}

.rt-list-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-3);
}

.rt-list-header h3 {
    margin: 0;
    white-space: nowrap;
    font-size: var(--font-size-base);
    font-weight: 600;
    color: var(--text-primary);
}

.rt-items {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-1);
}

.rt-item {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    width: 100%;
    padding: var(--spacing-2) var(--spacing-3);
    border: 1px solid transparent;
    border-radius: var(--radius-md);
    background: none;
    text-align: left;
    cursor: pointer;
    transition: var(--transition-base);
}

.rt-item:hover {
    background: var(--bg-secondary);
}

.rt-item.active {
    border-color: var(--primary-200);
    background: var(--primary-50);
}

.rt-item:focus-visible {
    outline: 2px solid var(--primary-100);
}

.rt-item-name {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    max-width: 100%;
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    color: var(--text-primary);
}

.rt-item-icon {
    flex-shrink: 0;
    color: var(--text-tertiary);
}

.rt-item.active .rt-item-icon {
    color: var(--primary-color);
}

.rt-item-text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.rt-item-meta {
    padding-left: 22px;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

.rt-detail {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
    min-width: 0;
}

.info-card {
    padding: var(--spacing-5);
}

.rt-section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-3);
    padding-bottom: var(--spacing-3);
    border-bottom: 1px solid var(--border-light);
}

/* Same look as the global .info-card h3 (accent bar included) */
.rt-section-header h3 {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    margin: 0;
    font-size: var(--font-size-base);
    font-weight: 600;
    color: var(--text-primary);
    word-break: break-all;
}

.key-value-list {
    display: flex;
    flex-direction: column;
}

.section-hint {
    margin: var(--spacing-3) 0 0;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}

.section-hint.top {
    margin: 0 0 var(--spacing-3);
}

.count-chip {
    padding: 1px 8px;
    border-radius: var(--radius-full);
    background: var(--bg-tertiary);
    color: var(--text-tertiary);
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-semibold);
}

.chip-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-2);
}

.vpc-chip {
    padding: 2px 10px;
    border-radius: var(--radius-full);
    background: var(--primary-50);
    color: var(--primary-700);
    font-size: var(--font-size-xs);
    text-decoration: none;
}

.vpc-chip:hover {
    text-decoration: underline;
}

.cidr-chip {
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
}

.mono {
    font-family: var(--font-family-mono);
}

.nowrap {
    white-space: nowrap;
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}

.text-link:hover {
    text-decoration: underline;
}

.empty-hint {
    padding: var(--spacing-3);
    font-size: var(--font-size-sm);
    color: var(--text-tertiary);
}

/* The copy button of the info rows, same look as the one of the title bar */
.copy-btn {
    display: inline-flex;
    align-items: center;
    padding: 2px;
    border: none;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-light);
    cursor: pointer;
}

.copy-btn:hover {
    color: var(--primary-color);
    background: var(--primary-50);
}

.copied-icon {
    color: var(--success-color);
}

/* Route target: two radio cards */
.target-options {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-3);
}

@media (max-width: 640px) {
    .target-options {
        grid-template-columns: 1fr;
    }
}

.target-option {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-2);
    padding: var(--spacing-3);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: border-color 0.15s;
}

.target-option:hover,
.target-option.selected {
    border-color: var(--primary-color);
}

.target-option.selected {
    background: var(--primary-50);
}

.target-option input {
    flex-shrink: 0;
    margin-top: 3px;
}

.target-title {
    display: block;
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    color: var(--text-primary);
}

.target-desc {
    display: block;
    margin-top: 2px;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-secondary);
}

.field-error {
    margin-top: 4px;
    font-size: var(--font-size-xs);
    color: var(--error-color);
}

.form-hint {
    margin: 4px 0 0;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
    word-break: break-word;
}
</style>
