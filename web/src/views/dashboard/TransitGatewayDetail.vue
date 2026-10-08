<script setup lang="ts">
// A transit gateway (vpc-transit-gateway-plan.md §5 T4): the VPCs attached to it, its route tables and their
// effective routes, and how far each node got in applying the latest change. Attaching, detaching and every route
// change only queue node commands, so the page polls quietly while something is still being applied.
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
    ArrowLeft,
    Waypoints,
    Pencil,
    Trash2,
    RefreshCw,
    Copy,
    Check,
    Send,
    Plus,
    Unlink,
    AlertTriangle,
    Info,
    Layers,
    Table2,
    Route,
} from 'lucide-vue-next'
import {
    transitGatewaysApi,
    type TransitGateway,
    type TgwAttachment,
    type TgwRouteTable,
    type TgwNode,
    type TgwAttachmentRef,
} from '../../api/transitGateways'
import { useToast } from '../../composables/useToast'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { formatDateTime } from '../../utils/format'
import {
    tgwStatusText,
    attachmentStatusText,
    syncStatusText,
    nodeStatusText,
    syncStatusVariant,
    nodeStatusVariant,
    localizeTgwReason,
    BUSY_ATTACHMENT_STATUSES,
} from '../../utils/transitGateway'
import InfoRow from '../../components/base/InfoRow.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DetailTabs, { type Tab } from '../../components/base/DetailTabs.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import TransitGatewayEditModal from '../../components/transitGateway/TransitGatewayEditModal.vue'
import TgwAttachModal from '../../components/transitGateway/TgwAttachModal.vue'
import TgwRouteTables from '../../components/transitGateway/TgwRouteTables.vue'
import TgwEffectiveRoutes from '../../components/transitGateway/TgwEffectiveRoutes.vue'

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const goBack = useGoBack('transit-gateways')
const { copiedId, copyId } = useCopyId()

const id = computed(() => String(route.params.id))
const gateway = ref<TransitGateway | null>(null)
const attachments = ref<TgwAttachment[]>([])
const routeTables = ref<TgwRouteTable[]>([])
const loading = ref(true)
const error = ref('')

type TabId = 'overview' | 'attachments' | 'routeTables' | 'effective'
const activeTab = ref<TabId>('overview')
// Selected route table, shared by the route tables and the effective routes tabs
const selectedTableId = ref('')

const tabs = computed<Tab[]>(() => [
    { id: 'overview', label: t('dashboard.transitGateway.overview'), icon: Info },
    {
        id: 'attachments',
        label: t('dashboard.transitGateway.attachmentsTab'),
        icon: Layers,
        count: attachments.value.length,
    },
    {
        id: 'routeTables',
        label: t('dashboard.transitGateway.routeTables'),
        icon: Table2,
        count: routeTables.value.length,
    },
    { id: 'effective', label: t('dashboard.transitGateway.effectiveRoutes'), icon: Route },
])

// ─── Loading ─────────────────────────────────────────────────────────────────
// silent: background refresh that keeps the current data on failure
const fetchAll = async (silent = false) => {
    if (!silent) {
        loading.value = true
        error.value = ''
    }
    try {
        const [g, atts, tables] = await Promise.all([
            transitGatewaysApi.get(id.value),
            transitGatewaysApi.listAttachments(id.value),
            transitGatewaysApi.listRouteTables(id.value),
        ])
        gateway.value = g
        attachments.value = atts
        routeTables.value = tables
    } catch (err) {
        console.error('Failed to load transit gateway:', err)
        if (!silent || !gateway.value) error.value = errorMessage(err, t('dashboard.transitGateway.loadError'))
    } finally {
        loading.value = false
    }
}

// ─── Polling while the nodes apply a change ──────────────────────────────────
const POLL_INTERVAL_MS = 3000
// Give up ~3 minutes after the busy set last changed: an attachment stuck in a transitional state (a node went
// offline) should not keep the page polling forever
const MAX_POLLS = 60
let pollTimer: ReturnType<typeof setTimeout> | null = null
let polls = 0
let lastBusy = ''
let stopped = false

// "id:status" of every attachment still being applied, plus the sync state while some node lags behind
const busySignature = () => {
    const parts = attachments.value
        .filter((a) => BUSY_ATTACHMENT_STATUSES.includes(a.status))
        .map((a) => `${a.id}:${a.status}`)
        .sort()
    if (gateway.value?.sync_status === 'syncing') parts.push(`sync:${gateway.value.generation ?? 0}`)
    return parts.join(',')
}

const schedulePoll = () => {
    if (stopped || pollTimer) return
    const busy = busySignature()
    if (busy !== lastBusy) {
        lastBusy = busy
        polls = 0
    }
    if (!busy || polls >= MAX_POLLS) return
    pollTimer = setTimeout(async () => {
        pollTimer = null
        polls++
        try {
            await fetchAll(true)
        } finally {
            schedulePoll()
        }
    }, POLL_INTERVAL_MS)
}
watch([attachments, gateway], schedulePoll)
onUnmounted(() => {
    stopped = true
    if (pollTimer) clearTimeout(pollTimer)
})

// After a change: reload now, the poll follows the nodes from there
const refreshAfterChange = () => fetchAll(true)

// ─── Overview ────────────────────────────────────────────────────────────────
const nodes = computed<TgwNode[]>(() => gateway.value?.nodes || [])
const failedNodes = computed(() => nodes.value.filter((n) => n.status === 'error').length)
const asymmetricRoutes = computed(() => gateway.value?.asymmetric_routes || [])
const vpcLabel = (ref?: TgwAttachmentRef) => ref?.vpc?.name || ref?.vpc?.id?.slice(0, 8) || '?'
// Host names are for system admins only: the column is there when the answer carries them
const showHostColumn = computed(() => nodes.value.some((n) => !!n.hypervisor))
// index is the number the attachment reasons use for the node (members who are not system admins)
const nodeRows = computed(() => nodes.value.map((n, i) => ({ ...n, index: n.index || i + 1, key: `${i}` })))
const nodeColumns = computed<Column[]>(() => {
    const cols: Column[] = []
    if (showHostColumn.value) cols.push({ key: 'hypervisor', label: t('dashboard.table.hyper') })
    else cols.push({ key: 'index', label: t('dashboard.transitGateway.node') })
    cols.push(
        { key: 'status', label: t('dashboard.table.status') },
        { key: 'generation', label: t('dashboard.transitGateway.appliedGeneration') },
        { key: 'reason', label: t('dashboard.transitGateway.reason') }
    )
    return cols
})

// ─── Attachments ─────────────────────────────────────────────────────────────
const attachmentColumns = computed<Column[]>(() => [
    { key: 'vpc', label: t('dashboard.transitGateway.vpc') },
    { key: 'route_table', label: t('dashboard.transitGateway.routeTable') },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'subnets', label: t('dashboard.transitGateway.subnets') },
    { key: 'link', label: t('dashboard.transitGateway.linkAddresses'), hideBelow: 1440 },
    { key: 'created_at', label: t('dashboard.table.createdAt'), hideBelow: 1600 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const attachModalVisible = ref(false)
const onAttached = async () => {
    attachModalVisible.value = false
    await refreshAfterChange()
}

// Change the route table of an attachment
const routeTableTarget = ref<TgwAttachment | null>(null)
const routeTableChoice = ref('')
const routeTableSaving = ref(false)
const routeTableError = ref('')
const sortedTables = computed(() => [...routeTables.value].sort((a, b) => Number(b.is_default) - Number(a.is_default)))
const openRouteTableModal = (att: TgwAttachment) => {
    routeTableTarget.value = att
    routeTableChoice.value = att.route_table?.id || ''
    routeTableError.value = ''
}
const closeRouteTableModal = () => {
    routeTableTarget.value = null
    routeTableError.value = ''
}
const saveRouteTable = async () => {
    const att = routeTableTarget.value
    if (!att || !routeTableChoice.value) return
    routeTableSaving.value = true
    routeTableError.value = ''
    try {
        await transitGatewaysApi.updateAttachment(id.value, att.id, routeTableChoice.value)
        toast.success(t('messages.updateSuccess'))
        closeRouteTableModal()
        await refreshAfterChange()
    } catch (err) {
        console.error('Failed to change the route table:', err)
        routeTableError.value = errorMessage(err, t('messages.error'))
    } finally {
        routeTableSaving.value = false
    }
}

// Detach
const detachTarget = ref<TgwAttachment | null>(null)
const detaching = ref(false)
const detachError = ref('')
const openDetach = (att: TgwAttachment) => {
    detachTarget.value = att
    detachError.value = ''
}
const closeDetach = () => {
    detachTarget.value = null
    detachError.value = ''
}
const confirmDetach = async () => {
    const att = detachTarget.value
    if (!att) return
    detaching.value = true
    detachError.value = ''
    try {
        await transitGatewaysApi.detach(id.value, att.id)
        toast.success(t('dashboard.transitGateway.detachStarted'))
        closeDetach()
        await refreshAfterChange()
    } catch (err) {
        console.error('Failed to detach VPC:', err)
        detachError.value = errorMessage(err, t('messages.error'))
    } finally {
        detaching.value = false
    }
}

// ─── Title actions ───────────────────────────────────────────────────────────
const resyncing = ref(false)
const handleResync = async () => {
    if (!gateway.value) return
    resyncing.value = true
    try {
        gateway.value = await transitGatewaysApi.resync(gateway.value.id)
        toast.success(t('dashboard.transitGateway.resyncStarted'))
        await refreshAfterChange()
    } catch (err) {
        console.error('Failed to resync transit gateway:', err)
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        resyncing.value = false
    }
}

const editing = ref<TransitGateway | null>(null)
const onEdited = async () => {
    editing.value = null
    await fetchAll(true)
}

const deleteBlockedReason = computed(() => {
    const n = gateway.value?.attachment_count ?? attachments.value.length
    return n > 0 ? t('dashboard.transitGateway.deleteHasAttachments', { n }) : ''
})
const showDelete = ref(false)
const deleting = ref(false)
const deleteError = ref('')
const openDelete = () => {
    deleteError.value = ''
    showDelete.value = true
}
const confirmDelete = async () => {
    if (!gateway.value) return
    deleting.value = true
    deleteError.value = ''
    try {
        await transitGatewaysApi.delete(gateway.value.id)
        showDelete.value = false
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'transit-gateways' })
    } catch (err) {
        console.error('Failed to delete transit gateway:', err)
        // 409 (133003): a VPC was attached since the page was loaded
        deleteError.value = errorMessage(err, t('messages.error'))
    } finally {
        deleting.value = false
    }
}

onMounted(() => fetchAll())
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" />
                <span>{{ $t('actions.back') }}</span>
            </button>
        </div>

        <div v-if="loading && !gateway" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error && !gateway" class="error-container card">
            <Waypoints :size="48" style="opacity: 0.3" />
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary btn-sm" @click="fetchAll()">{{ $t('actions.retry') }}</button>
        </div>

        <template v-else-if="gateway">
            <!-- Title bar (global .title-bar styles) -->
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon"><Waypoints :size="20" /></div>
                    <div>
                        <h2 class="resource-title">
                            {{ gateway.name }}
                            <StatusBadge :status="gateway.status" :label="tgwStatusText(t, te, gateway.status)" />
                            <StatusBadge
                                v-if="gateway.sync_status && gateway.sync_status !== 'synced'"
                                :variant="syncStatusVariant(gateway.sync_status)"
                                :label="syncStatusText(t, te, gateway.sync_status)"
                            />
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ gateway.id }}</span>
                            <button
                                class="copy-btn"
                                :title="$t('actions.copy')"
                                :aria-label="$t('actions.copy')"
                                @click="copyId(gateway.id, 'id')"
                            >
                                <Check v-if="copiedId === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <button
                        class="btn btn-secondary btn-sm btn-icon"
                        :title="$t('actions.refresh')"
                        @click="fetchAll(true)"
                    >
                        <RefreshCw :size="14" />
                    </button>
                    <button
                        class="btn btn-secondary btn-sm"
                        :disabled="resyncing"
                        :title="$t('dashboard.transitGateway.resyncHint')"
                        @click="handleResync"
                    >
                        <Send :size="14" /> {{ $t('dashboard.transitGateway.resync') }}
                    </button>
                    <button class="btn btn-secondary btn-sm" @click="editing = gateway">
                        <Pencil :size="14" /> {{ $t('actions.edit') }}
                    </button>
                    <button
                        class="btn btn-danger-outline btn-sm"
                        :disabled="!!deleteBlockedReason"
                        :title="deleteBlockedReason || undefined"
                        @click="openDelete"
                    >
                        <Trash2 :size="14" /> {{ $t('actions.delete') }}
                    </button>
                </div>
            </div>

            <!-- A node failed to apply the latest change -->
            <div v-if="gateway.sync_status === 'error'" class="notice-banner error-banner" role="status">
                <AlertTriangle :size="16" class="notice-icon" />
                <span class="notice-text">{{
                    $t('dashboard.transitGateway.syncErrorNotice', { n: failedNodes })
                }}</span>
                <button class="btn btn-secondary btn-sm" :disabled="resyncing" @click="handleResync">
                    {{ $t('dashboard.transitGateway.resync') }}
                </button>
            </div>

            <!-- Route tables propagated one way only: the replies have no way back -->
            <div v-if="asymmetricRoutes.length" class="notice-banner warning-banner" role="status">
                <AlertTriangle :size="16" class="notice-icon" />
                <div class="notice-text">
                    <div>{{ $t('dashboard.transitGateway.asymmetricNotice') }}</div>
                    <ul class="asym-list">
                        <li v-for="(p, i) in asymmetricRoutes" :key="i">
                            {{
                                $t('dashboard.transitGateway.asymmetricPair', {
                                    from: vpcLabel(p.from),
                                    to: vpcLabel(p.to),
                                })
                            }}
                        </li>
                    </ul>
                </div>
            </div>

            <DetailTabs v-model="activeTab" :tabs="tabs" />

            <!-- ── Overview ── -->
            <div v-if="activeTab === 'overview'">
                <div class="info-grid">
                    <div class="card info-card">
                        <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                        <div class="key-value-list">
                            <InfoRow :label="$t('dashboard.table.name')">{{ gateway.name }}</InfoRow>
                            <InfoRow :label="$t('dashboard.table.description')">{{
                                gateway.description || '-'
                            }}</InfoRow>
                            <InfoRow :label="$t('dashboard.table.status')">
                                <StatusBadge :status="gateway.status" :label="tgwStatusText(t, te, gateway.status)" />
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.transitGateway.attachedVpcs')">
                                <button type="button" class="link-btn" @click="activeTab = 'attachments'">
                                    {{ gateway.attachment_count ?? attachments.length }}
                                </button>
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.transitGateway.routeTables')">
                                <button type="button" class="link-btn" @click="activeTab = 'routeTables'">
                                    {{ routeTables.length }}
                                </button>
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.table.createdAt')">{{
                                formatDateTime(gateway.created_at)
                            }}</InfoRow>
                            <InfoRow :label="$t('dashboard.table.updatedAt')">{{
                                formatDateTime(gateway.updated_at)
                            }}</InfoRow>
                        </div>
                    </div>

                    <div class="card info-card">
                        <h3>{{ $t('dashboard.transitGateway.syncTitle') }}</h3>
                        <div class="key-value-list">
                            <InfoRow :label="$t('dashboard.transitGateway.syncStatus')">
                                <StatusBadge
                                    :variant="syncStatusVariant(gateway.sync_status)"
                                    :label="syncStatusText(t, te, gateway.sync_status)"
                                />
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.transitGateway.generation')" mono>{{
                                gateway.generation ?? 0
                            }}</InfoRow>
                            <InfoRow :label="$t('dashboard.transitGateway.nodeCount')">{{ nodes.length }}</InfoRow>
                        </div>
                        <p class="card-hint">{{ $t('dashboard.transitGateway.syncHint') }}</p>
                    </div>
                </div>

                <div class="card info-card section-card">
                    <h3>{{ $t('dashboard.transitGateway.nodes') }} ({{ nodes.length }})</h3>
                    <DataTable :columns="nodeColumns" :rows="nodeRows" row-key="key">
                        <template #empty>
                            <div>
                                <Waypoints :size="40" style="opacity: 0.3; margin-bottom: 12px" />
                                <p class="text-secondary">{{ $t('dashboard.transitGateway.noNodes') }}</p>
                            </div>
                        </template>
                        <template #cell-hypervisor="{ row: n }">{{ n.hypervisor || '-' }}</template>
                        <template #cell-index="{ row: n }">{{
                            $t('dashboard.transitGateway.nodeN', { n: n.index })
                        }}</template>
                        <template #cell-status="{ row: n }">
                            <StatusBadge
                                :variant="nodeStatusVariant(n.status)"
                                :label="nodeStatusText(t, te, n.status)"
                            />
                        </template>
                        <template #cell-generation="{ row: n }">
                            <span class="mono">{{ n.generation || '-' }}</span>
                        </template>
                        <template #cell-reason="{ row: n }">
                            <span v-if="n.reason" class="cell-reason wide text-error" :title="n.reason">{{
                                n.reason
                            }}</span>
                            <span v-else class="text-tertiary">-</span>
                        </template>
                    </DataTable>
                </div>

                <div class="notice-banner info-banner">
                    <Info :size="16" class="notice-icon" />
                    <span class="notice-text">{{ $t('dashboard.transitGateway.securityGroupHint') }}</span>
                </div>
            </div>

            <!-- ── Attachments ── -->
            <div v-else-if="activeTab === 'attachments'" class="attachments-tab">
                <div class="tab-toolbar">
                    <span class="tab-hint">{{ $t('dashboard.transitGateway.attachmentsHint') }}</span>
                    <button class="btn btn-primary btn-sm" @click="attachModalVisible = true">
                        <Plus :size="14" /> {{ $t('dashboard.transitGateway.attachVpc') }}
                    </button>
                </div>

                <DataTable :columns="attachmentColumns" :rows="attachments" row-key="id">
                    <template #empty>
                        <div class="empty-block">
                            <Layers :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                            <p class="text-secondary">{{ $t('dashboard.transitGateway.noAttachments') }}</p>
                            <p class="text-tertiary empty-hint">{{ $t('dashboard.transitGateway.emptyHint') }}</p>
                        </div>
                    </template>

                    <template #cell-vpc="{ row: a }">
                        <router-link
                            v-if="a.vpc?.id"
                            :to="{ name: 'vpc-detail', params: { id: a.vpc.id } }"
                            class="text-link cell-strong"
                            >{{ a.vpc.name || a.vpc.id.slice(0, 8) }}</router-link
                        >
                        <span v-else class="text-tertiary">-</span>
                    </template>

                    <template #cell-route_table="{ row: a }">
                        <span class="nowrap">{{ a.route_table?.name || '-' }}</span>
                    </template>

                    <template #cell-status="{ row: a }">
                        <span :title="localizeTgwReason(t, a.status_reason) || undefined">
                            <StatusBadge :status="a.status" :label="attachmentStatusText(t, te, a.status)" />
                        </span>
                        <div
                            v-if="a.status_reason"
                            class="cell-reason text-error"
                            :title="localizeTgwReason(t, a.status_reason)"
                        >
                            {{ localizeTgwReason(t, a.status_reason) }}
                        </div>
                    </template>

                    <template #cell-subnets="{ row: a }">
                        <div v-if="a.subnets?.length" class="chip-list">
                            <code v-for="cidr in a.subnets" :key="cidr" class="cidr-chip">{{ cidr }}</code>
                        </div>
                        <span v-else class="text-tertiary">{{ $t('dashboard.transitGateway.noSubnets') }}</span>
                    </template>

                    <template #cell-link="{ row: a }">
                        <span class="mono link-addr" :title="$t('dashboard.transitGateway.linkAddressesHint')">
                            {{ a.router_address || '-' }} ↔ {{ a.gateway_address || '-' }}
                        </span>
                    </template>

                    <template #cell-created_at="{ row: a }">
                        <span class="cell-time">{{ formatDateTime(a.created_at) }}</span>
                    </template>

                    <template #cell-actions="{ row: a }">
                        <div class="row-actions">
                            <button
                                class="icon-btn-table"
                                :title="
                                    a.status === 'detaching'
                                        ? $t('dashboard.transitGateway.busyDetaching')
                                        : $t('dashboard.transitGateway.changeRouteTable')
                                "
                                :disabled="a.status === 'detaching'"
                                @click="openRouteTableModal(a)"
                            >
                                <Pencil :size="16" />
                            </button>
                            <button
                                class="icon-btn-table icon-danger"
                                :title="
                                    a.status === 'detaching'
                                        ? $t('dashboard.transitGateway.detachAgain')
                                        : $t('dashboard.transitGateway.detach')
                                "
                                @click="openDetach(a)"
                            >
                                <Unlink :size="16" />
                            </button>
                        </div>
                    </template>
                </DataTable>
            </div>

            <!-- ── Route tables ── -->
            <TgwRouteTables
                v-else-if="activeTab === 'routeTables'"
                v-model="selectedTableId"
                :gateway-id="gateway.id"
                :route-tables="routeTables"
                :attachments="attachments"
                @changed="refreshAfterChange"
            />

            <!-- ── Effective routes ── -->
            <TgwEffectiveRoutes
                v-else-if="activeTab === 'effective'"
                v-model="selectedTableId"
                :gateway-id="gateway.id"
                :route-tables="routeTables"
            />
        </template>

        <TransitGatewayEditModal :gateway="editing" @close="editing = null" @saved="onEdited" />

        <TgwAttachModal
            v-if="gateway"
            :show="attachModalVisible"
            :gateway-id="gateway.id"
            :route-tables="routeTables"
            :attachments="attachments"
            @close="attachModalVisible = false"
            @attached="onAttached"
        />

        <!-- Change the route table of an attachment -->
        <BaseModal
            :show="routeTableTarget !== null"
            :title="$t('dashboard.transitGateway.changeRouteTableTitle', { vpc: routeTableTarget?.vpc?.name })"
            :loading="routeTableSaving"
            form
            @close="closeRouteTableModal"
            @submit="saveRouteTable"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.transitGateway.routeTable') }}</label>
                <select v-model="routeTableChoice" class="form-input">
                    <option v-for="rt in sortedTables" :key="rt.id" :value="rt.id">
                        {{ rt.name }}{{ rt.is_default ? ` · ${$t('dashboard.transitGateway.defaultTag')}` : '' }}
                    </option>
                </select>
                <div class="form-hint">{{ $t('dashboard.transitGateway.changeRouteTableHint') }}</div>
            </div>
            <template #footer>
                <div v-if="routeTableError" class="footer-error">{{ routeTableError }}</div>
                <button
                    type="button"
                    class="btn btn-secondary"
                    :disabled="routeTableSaving"
                    @click="closeRouteTableModal"
                >
                    {{ $t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    class="btn btn-primary"
                    :disabled="
                        routeTableSaving || !routeTableChoice || routeTableChoice === routeTableTarget?.route_table?.id
                    "
                >
                    {{ routeTableSaving ? $t('messages.saving') : $t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="detachTarget !== null"
            :title="$t('dashboard.transitGateway.detachTitle')"
            :message="$t('dashboard.transitGateway.detachMessage', { vpc: detachTarget?.vpc?.name || '-' })"
            :resource-name="detachTarget?.vpc?.name"
            :resource-id="detachTarget?.vpc?.id"
            :confirm-label="$t('dashboard.transitGateway.detach')"
            :loading="detaching"
            :error="detachError"
            @close="closeDetach"
            @confirm="confirmDetach"
        />

        <DeleteModal
            :show="showDelete"
            :resource-name="gateway?.name"
            :resource-id="gateway?.id"
            :loading="deleting"
            :error="deleteError"
            @close="showDelete = false"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
/* .detail-header, .title-bar, .row-actions, .icon-btn-table, .badge-* are global (index.css) */

.loading-container,
.error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-4);
    padding: 60px;
}

.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-4);
}

.info-card {
    padding: var(--spacing-5);
}

.info-card h3 {
    margin: 0 0 var(--spacing-4) 0;
    padding-bottom: var(--spacing-3);
    border-bottom: 1px solid var(--border-light);
    font-size: var(--font-size-base);
    font-weight: 600;
    color: var(--text-primary);
}

.section-card {
    margin-bottom: var(--spacing-4);
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.card-hint {
    margin: var(--spacing-3) 0 0;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}

.notice-banner {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-4);
    padding: var(--spacing-3) var(--spacing-4);
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    line-height: 1.6;
}

.notice-icon {
    flex-shrink: 0;
}

.notice-text {
    flex: 1;
    min-width: 0;
}

.error-banner {
    background: var(--error-light);
    color: var(--error-dark);
}

.warning-banner {
    align-items: flex-start;
    background: var(--warning-light);
    color: var(--warning-dark);
}

.warning-banner .notice-icon {
    margin-top: 3px;
}

.asym-list {
    margin: var(--spacing-1) 0 0;
    padding-left: var(--spacing-5);
}

.info-banner {
    align-items: flex-start;
    background: var(--info-light);
    color: var(--info-dark);
}

.info-banner .notice-icon {
    margin-top: 3px;
}

.tab-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-4);
}

.tab-hint {
    font-size: var(--font-size-sm);
    line-height: 1.5;
    color: var(--text-secondary);
}

/* Short column titles ("Route table") must not wrap: the subnets column takes the room */
.attachments-tab :deep(.data-table th) {
    white-space: nowrap;
}

.link-btn {
    padding: 0;
    border: none;
    background: none;
    color: var(--primary-600);
    font: inherit;
    cursor: pointer;
}

.link-btn:hover {
    text-decoration: underline;
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}

.text-link:hover {
    text-decoration: underline;
}

.cell-strong {
    font-weight: var(--font-weight-medium);
    white-space: nowrap;
}

.nowrap {
    white-space: nowrap;
}

.mono {
    font-family: var(--font-family-mono);
}

/* A failure reason stays on one line, the full text is in the tooltip */
.cell-reason {
    display: block;
    max-width: 260px;
    margin-top: 4px;
    overflow: hidden;
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
}

/* The node table has the room for a longer reason */
.cell-reason.wide {
    max-width: 560px;
    margin-top: 0;
}

.chip-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-1);
}

.cidr-chip {
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
    white-space: nowrap;
}

.link-addr {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    white-space: nowrap;
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

.form-hint {
    margin-top: 4px;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}
</style>
