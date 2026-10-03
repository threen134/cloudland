<script setup lang="ts">
// A storage cluster (shared-storage-design.md §13.3): overview, hosts, disks, file systems, its CloudLand pools and
// its tasks. What can be changed follows what the kind supports (GET /storage_backends) and the mode: an imported
// cluster is used, never changed. Every change is a task; the page refreshes quietly while one runs.
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
    ArrowLeft,
    ServerCog,
    RefreshCw,
    Loader2,
    Copy,
    Check,
    Info,
    Server,
    HardDrive,
    Database,
    ListChecks,
    Layers,
    Plus,
    Trash2,
    Gauge,
    Shuffle,
    AlertTriangle,
} from 'lucide-vue-next'
import {
    storageClustersApi,
    TASK_LIVE_STATUSES,
    type StorageBackend,
    type StorageCluster,
    type StorageClusterDisk,
    type StorageClusterNode,
    type StorageClusterPool,
    type StorageFilesystem,
    type StorageTask,
} from '../../api/storageClusters'
import { storagePoolsApi, type StoragePoolTask } from '../../api/storagePools'
import { useToast } from '../../composables/useToast'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { formatBytes, formatDateTime } from '../../utils/format'
import {
    storageKindText,
    clusterStatusText,
    clusterHealthText,
    taskKindText,
    taskStatusText,
    taskVariant,
    roleText,
} from '../../utils/storageCluster'
import InfoRow from '../../components/base/InfoRow.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DetailTabs, { type Tab } from '../../components/base/DetailTabs.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import CapacityBar from '../../components/storage/CapacityBar.vue'
import StorageExpandModal from '../../components/storage/StorageExpandModal.vue'
import SharedPoolModal from '../../components/storage/SharedPoolModal.vue'

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const goBack = useGoBack('storage-clusters')
const { copiedId, copyId } = useCopyId()

const id = computed(() => String(route.params.id))
const cluster = ref<StorageCluster | null>(null)
const backends = ref<StorageBackend[]>([])
const tasks = ref<StorageTask[]>([])
const loading = ref(true)
const error = ref('')

const backend = computed(() => backends.value.find((b) => b.kind === cluster.value?.kind))
const caps = computed(() => backend.value?.capabilities)
const managed = computed(() => cluster.value?.mode === 'managed')
const ready = computed(() => cluster.value?.status === 'ready')
// Changing the structure needs a ready managed cluster no task holds
const canChange = computed(() => managed.value && ready.value && !cluster.value?.active_task)

const load = async (silent = false) => {
    if (!silent) loading.value = true
    try {
        const [c, b, tl] = await Promise.all([
            storageClustersApi.get(id.value),
            backends.value.length ? Promise.resolve(backends.value) : storageClustersApi.backends(),
            storageClustersApi.listTasks({ cluster_id: id.value, limit: 50 }),
        ])
        cluster.value = c
        backends.value = b
        tasks.value = tl.tasks || []
        error.value = ''
    } catch (err) {
        if (!silent || !cluster.value) error.value = errorMessage(err, t('messages.error'))
    } finally {
        loading.value = false
    }
}

// A task of the cluster runs: refresh quietly
const live = computed(
    () =>
        !!cluster.value?.active_task ||
        !!cluster.value?.active_pool_task ||
        tasks.value.some((task) => TASK_LIVE_STATUSES.includes(task.status)) ||
        ['deploying', 'deleting'].includes(cluster.value?.status || '')
)
let timer: ReturnType<typeof setInterval> | null = null
watch(live, (isLive) => {
    if (isLive && !timer) timer = setInterval(() => load(true), 5000)
    else if (!isLive && timer) {
        clearInterval(timer)
        timer = null
    }
})
onUnmounted(() => {
    if (timer) clearInterval(timer)
})
watch(id, () => load())
onMounted(() => load())

// ---- tabs ----
type TabId = 'overview' | 'nodes' | 'disks' | 'filesystems' | 'pools' | 'tasks'
const activeTab = ref<TabId>((route.query.tab as TabId) || 'overview')
const tabs = computed<Tab[]>(() => {
    const list: Tab[] = [
        { id: 'overview', label: t('storage.clusterDetail.overview'), icon: Info },
        {
            id: 'nodes',
            label: t('storage.clusterDetail.nodes'),
            icon: Server,
            count: cluster.value?.nodes?.length ?? 0,
        },
    ]
    if (managed.value) {
        list.push({
            id: 'disks',
            label: t('storage.clusterDetail.disks'),
            icon: HardDrive,
            count: cluster.value?.disks?.length ?? 0,
        })
    }
    if (caps.value?.filesystems) {
        list.push({
            id: 'filesystems',
            label: t('storage.clusterDetail.filesystems'),
            icon: Layers,
            count: cluster.value?.filesystems?.length ?? 0,
        })
    }
    if (caps.value?.pools) {
        list.push({
            id: 'pools',
            label: t('storage.clusterDetail.pools'),
            icon: Database,
            count: cluster.value?.pools?.length ?? 0,
        })
    }
    list.push({ id: 'tasks', label: t('storage.cluster.tasksTab'), icon: ListChecks, count: tasks.value.length })
    return list
})
watch(activeTab, (tab) => router.replace({ query: { ...route.query, tab } }))

const openTask = (task?: StorageTask) => {
    if (task) router.push({ name: 'storage-task-detail', params: { id: task.id } })
}
const started = (task?: StorageTask) => {
    toast.success(t('storage.clusterDetail.taskStarted'))
    activeTab.value = 'tasks'
    load(true)
    return task
}

// ---- hosts ----
const nodeColumns = computed<Column[]>(() => [
    { key: 'host', label: t('storage.cluster.host') },
    { key: 'roles', label: t('storage.clusterDetail.roles') },
    { key: 'status', label: t('storage.status') },
    { key: 'state', label: t('storage.clusterDetail.state'), hideBelow: 1280 },
    { key: 'group', label: t('storage.clusterDetail.failureGroup'), hideBelow: 1440 },
    { key: 'mem', label: t('storage.clusterDetail.reservedMem'), hideBelow: 1280 },
    { key: 'actions', label: t('dashboard.table.actions'), width: '80px', align: 'right' },
])
const nodeStatusText = (s: string) =>
    te(`storage.clusterDetail.nodeStatus.${s}`) ? t(`storage.clusterDetail.nodeStatus.${s}`) : s
const nodeVariant = (s: string) =>
    s === 'active' ? 'success' : s === 'error' ? 'error' : s === 'down' ? 'warning' : 'pending'
const removingNode = ref<StorageClusterNode | null>(null)
const removeNodeForm = ref({ offline: false, confirm: '', purge: true })
const removeNodeBusy = ref(false)
const removeNodeError = ref('')
const openRemoveNode = (n: StorageClusterNode) => {
    removingNode.value = n
    removeNodeForm.value = { offline: false, confirm: '', purge: true }
    removeNodeError.value = ''
}
const removeNode = async () => {
    const n = removingNode.value
    if (!n || !cluster.value || !n.hypervisor.id) return
    removeNodeBusy.value = true
    removeNodeError.value = ''
    try {
        const f = removeNodeForm.value
        const task = await storageClustersApi.removeNode(cluster.value.id, n.hypervisor.id, {
            offline: f.offline || undefined,
            confirm: f.offline ? f.confirm : undefined,
            purge_packages: !f.offline && f.purge ? true : undefined,
        })
        removingNode.value = null
        started(task)
    } catch (err) {
        removeNodeError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        removeNodeBusy.value = false
    }
}

// ---- disks ----
const diskColumns = computed<Column[]>(() => [
    { key: 'host', label: t('storage.cluster.host') },
    { key: 'disk', label: t('storage.clusterDetail.disk') },
    { key: 'name', label: t('storage.clusterDetail.diskName') },
    { key: 'media', label: t('storage.media'), hideBelow: 1280 },
    { key: 'size', label: t('storage.size') },
    { key: 'usage', label: t('storage.clusterDetail.usage'), hideBelow: 1440 },
    { key: 'status', label: t('storage.status') },
    { key: 'actions', label: t('dashboard.table.actions'), width: '80px', align: 'right' },
])
const diskStatusText = (s: string) =>
    te(`storage.clusterDetail.diskStatus.${s}`) ? t(`storage.clusterDetail.diskStatus.${s}`) : s
const diskVariant = (s: string) => (s === 'active' ? 'success' : s === 'failed' ? 'error' : 'pending')
const removingDisk = ref<StorageClusterDisk | null>(null)
const removeDiskBusy = ref(false)
const removeDisk = async () => {
    const d = removingDisk.value
    if (!d || !cluster.value) return
    removeDiskBusy.value = true
    try {
        const task = await storageClustersApi.removeDisk(cluster.value.id, d.id)
        removingDisk.value = null
        started(task)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.cluster.actionFailed')))
    } finally {
        removeDiskBusy.value = false
    }
}
const attr = (attrs: Record<string, unknown> | undefined, key: string) => {
    const v = attrs?.[key]
    return v === undefined || v === null || v === '' ? '-' : String(v)
}

// ---- file systems ----
const fsColumns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.name') },
    { key: 'mount', label: t('storage.wizard.mountPoint') },
    { key: 'block', label: t('storage.wizard.blockSize'), hideBelow: 1280 },
    { key: 'replicas', label: t('storage.clusterDetail.replicas') },
    { key: 'capacity', label: t('storage.capacity') },
    { key: 'actions', label: t('dashboard.table.actions'), width: '80px', align: 'right' },
])
const rebalancing = ref<StorageFilesystem | null>(null)
const rebalanceBusy = ref(false)
const rebalance = async () => {
    if (!rebalancing.value || !cluster.value) return
    rebalanceBusy.value = true
    try {
        const task = await storageClustersApi.rebalance(cluster.value.id, rebalancing.value.name)
        rebalancing.value = null
        started(task)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.cluster.actionFailed')))
    } finally {
        rebalanceBusy.value = false
    }
}

// ---- pools ----
const poolColumns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.name') },
    { key: 'status', label: t('storage.status') },
    { key: 'media', label: t('storage.media'), hideBelow: 1280 },
    { key: 'path', label: t('storage.mountPath'), hideBelow: 1440 },
    { key: 'quota', label: t('storage.sharedPool.quotaShort') },
    { key: 'capacity', label: t('storage.capacity') },
    { key: 'actions', label: t('dashboard.table.actions'), width: '100px', align: 'right' },
])
const poolStatusText = (s: string) => (te(`storage.poolStatus.${s}`) ? t(`storage.poolStatus.${s}`) : s)
const showPoolCreate = ref(false)
const poolCreated = (result: StoragePoolTask) => {
    showPoolCreate.value = false
    started(result.task)
}
const quotaPool = ref<StorageClusterPool | null>(null)
const quotaGB = ref(0)
const quotaBusy = ref(false)
const quotaError = ref('')
const openQuota = (p: StorageClusterPool) => {
    quotaPool.value = p
    quotaGB.value = Math.round((p.quota_bytes || 0) / 1024 ** 3)
    quotaError.value = ''
}
const saveQuota = async () => {
    if (!quotaPool.value) return
    quotaBusy.value = true
    quotaError.value = ''
    try {
        const result = await storagePoolsApi.setQuota(quotaPool.value.id, Number(quotaGB.value) || 0)
        quotaPool.value = null
        started(result.task)
    } catch (err) {
        quotaError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        quotaBusy.value = false
    }
}
const deletingPool = ref<StorageClusterPool | null>(null)
const deletePoolBusy = ref(false)
const deletePool = async () => {
    if (!deletingPool.value) return
    deletePoolBusy.value = true
    try {
        const result = await storagePoolsApi.delete(deletingPool.value.id)
        deletingPool.value = null
        started(result?.task)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    } finally {
        deletePoolBusy.value = false
    }
}

// ---- tasks ----
const taskColumns = computed<Column[]>(() => [
    { key: 'kind', label: t('storage.cluster.task') },
    { key: 'status', label: t('storage.status') },
    { key: 'message', label: t('storage.cluster.message'), hideBelow: 1280 },
    { key: 'creator', label: t('storage.cluster.creator'), hideBelow: 1024 },
    { key: 'created_at', label: t('storage.cluster.createdAt') },
])

// ---- expansion, deletion ----
const expandMode = ref<'nodes' | 'disks' | null>(null)
const expanded = (task: StorageTask) => {
    expandMode.value = null
    started(task)
}
const showDelete = ref(false)
const deleteForm = ref({ confirm: '', purge: true })
const deleteBusy = ref(false)
const deleteError = ref('')
const openDelete = () => {
    deleteForm.value = { confirm: '', purge: true }
    deleteError.value = ''
    showDelete.value = true
}
const removeCluster = async () => {
    if (!cluster.value) return
    deleteBusy.value = true
    deleteError.value = ''
    try {
        const task = await storageClustersApi.remove(
            cluster.value.id,
            deleteForm.value.confirm,
            managed.value && deleteForm.value.purge
        )
        showDelete.value = false
        started(task)
    } catch (err) {
        deleteError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        deleteBusy.value = false
    }
}
const activeTaskOf = (taskId?: string) => tasks.value.find((task) => task.id === taskId)
const modeText = (m?: string) => (m && te(`storage.cluster.modes.${m}`) ? t(`storage.cluster.modes.${m}`) : m || '-')
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" />
                <span>{{ t('actions.back') }}</span>
            </button>
        </div>
        <div v-if="loading && !cluster" class="loading-container"><Loader2 :size="24" class="spinning" /></div>
        <div v-else-if="error" class="error-container">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-secondary btn-sm" @click="() => load()">{{ t('actions.retry') }}</button>
        </div>
        <template v-else-if="cluster">
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon"><ServerCog :size="20" /></div>
                    <div>
                        <h2 class="resource-title">
                            {{ cluster.name }}
                            <StatusBadge :status="cluster.status" :label="clusterStatusText(t, te, cluster.status)" />
                            <span class="badge badge-secondary">{{ storageKindText(t, te, cluster.kind) }}</span>
                            <span class="badge badge-secondary">{{ modeText(cluster.mode) }}</span>
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ cluster.id }}</span>
                            <button class="copy-btn" :title="t('actions.copy')" @click="copyId(cluster.id)">
                                <Check v-if="copiedId === cluster.id" :size="14" />
                                <Copy v-else :size="14" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-secondary btn-sm" :title="t('actions.refresh')" @click="() => load()">
                        <RefreshCw :size="14" :class="{ spinning: loading }" />
                    </button>
                    <button
                        v-if="caps?.add_nodes && managed"
                        class="btn btn-secondary btn-sm"
                        :disabled="!canChange"
                        @click="expandMode = 'nodes'"
                    >
                        <Plus :size="14" /> {{ t('storage.clusterDetail.addNodes') }}
                    </button>
                    <button
                        v-if="caps?.add_disks && managed"
                        class="btn btn-secondary btn-sm"
                        :disabled="!canChange"
                        @click="expandMode = 'disks'"
                    >
                        <Plus :size="14" /> {{ t('storage.clusterDetail.addDisks') }}
                    </button>
                    <button
                        class="btn btn-sm btn-danger-outline"
                        :disabled="!!cluster.active_task || cluster.status === 'deleting'"
                        @click="openDelete"
                    >
                        <Trash2 :size="14" /> {{ managed ? t('actions.delete') : t('storage.clusterDetail.forget') }}
                    </button>
                </div>
            </div>

            <div v-if="cluster.active_task || cluster.active_pool_task" class="banner banner-info">
                <ListChecks :size="16" />
                <span>{{ t('storage.clusterDetail.taskHolds') }}</span>
                <router-link
                    v-for="tid in [cluster.active_task, cluster.active_pool_task].filter(Boolean)"
                    :key="tid"
                    :to="{ name: 'storage-task-detail', params: { id: tid } }"
                    class="resource-link"
                    >{{
                        taskKindText(t, te, activeTaskOf(tid)?.kind) !== '-'
                            ? taskKindText(t, te, activeTaskOf(tid)?.kind)
                            : tid?.slice(0, 8)
                    }}
                    <template v-if="activeTaskOf(tid)">
                        · {{ taskStatusText(t, te, activeTaskOf(tid)?.status) }}</template
                    ></router-link
                >
            </div>
            <div v-if="cluster.unsupported" class="banner banner-warning">
                <AlertTriangle :size="16" />
                <span>{{ t('storage.clusterDetail.unsupportedBanner') }}</span>
            </div>

            <DetailTabs v-model="activeTab" :tabs="tabs" />

            <!-- Overview -->
            <div v-if="activeTab === 'overview'" class="info-card card">
                <div class="info-rows">
                    <InfoRow :label="t('storage.cluster.kind')">{{ storageKindText(t, te, cluster.kind) }}</InfoRow>
                    <InfoRow :label="t('storage.cluster.mode')">{{ modeText(cluster.mode) }}</InfoRow>
                    <InfoRow v-if="cluster.layout" :label="t('storage.clusterDetail.layout')">{{
                        cluster.layout
                    }}</InfoRow>
                    <InfoRow v-if="cluster.version" :label="t('storage.clusterDetail.version')">{{
                        cluster.version
                    }}</InfoRow>
                    <InfoRow v-if="cluster.cluster_ref" :label="t('storage.clusterDetail.clusterRef')">
                        <span class="mono-cell">{{ cluster.cluster_ref }}</span>
                    </InfoRow>
                    <InfoRow :label="t('storage.status')">
                        <StatusBadge :status="cluster.status" :label="clusterStatusText(t, te, cluster.status)" />
                    </InfoRow>
                    <InfoRow :label="t('storage.cluster.health')">
                        <StatusBadge :status="cluster.health" :label="clusterHealthText(t, te, cluster.health)" />
                    </InfoRow>
                    <InfoRow :label="t('storage.clusterDetail.counts')">{{
                        t('storage.clusterDetail.countsValue', {
                            nodes: cluster.node_count,
                            disks: cluster.disk_count,
                            pools: cluster.pool_count,
                        })
                    }}</InfoRow>
                    <InfoRow v-if="cluster.capacity_bytes" :label="t('storage.capacity')">
                        <CapacityBar
                            :capacity="cluster.capacity_bytes"
                            :used="cluster.capacity_bytes - cluster.free_bytes"
                            :allocated="cluster.allocated_bytes"
                        />
                    </InfoRow>
                    <InfoRow v-if="cluster.description" :label="t('dashboard.table.description')">{{
                        cluster.description
                    }}</InfoRow>
                    <InfoRow :label="t('dashboard.table.createdAt')">{{ formatDateTime(cluster.created_at) }}</InfoRow>
                </div>
            </div>

            <!-- Hosts -->
            <DataTable
                v-else-if="activeTab === 'nodes'"
                :columns="nodeColumns"
                :rows="cluster.nodes || []"
                :row-key="(n: StorageClusterNode) => n.hypervisor.id || n.hypervisor.name || ''"
            >
                <template #cell-host="{ row: n }">
                    <span class="resource-name">{{ n.hypervisor.name || '-' }}</span>
                </template>
                <template #cell-roles="{ row: n }">{{
                    n.roles.map((r: string) => roleText(t, te, r)).join(', ')
                }}</template>
                <template #cell-status="{ row: n }">
                    <StatusBadge :variant="nodeVariant(n.status)" :label="nodeStatusText(n.status)" />
                    <div v-if="n.reason" class="sub-line" :title="n.reason">{{ n.reason }}</div>
                </template>
                <template #cell-state="{ row: n }">{{ n.state || '-' }}</template>
                <template #cell-group="{ row: n }">{{ attr(n.attrs, 'failure_group') }}</template>
                <template #cell-mem="{ row: n }">{{ n.reserved_mem_mb ? `${n.reserved_mem_mb} MiB` : '-' }}</template>
                <template #cell-actions="{ row: n }">
                    <div class="row-actions">
                        <button
                            v-if="caps?.remove_node && managed"
                            class="icon-btn-table icon-danger"
                            :title="t('storage.clusterDetail.removeNode')"
                            :disabled="!canChange"
                            @click="openRemoveNode(n)"
                        >
                            <Trash2 :size="16" />
                        </button>
                    </div>
                </template>
            </DataTable>

            <!-- Disks -->
            <DataTable
                v-else-if="activeTab === 'disks'"
                :columns="diskColumns"
                :rows="cluster.disks || []"
                row-key="id"
            >
                <template #cell-host="{ row: d }">{{ d.hypervisor.name || '-' }}</template>
                <template #cell-disk="{ row: d }">
                    <span class="mono-cell" :title="d.disk_id">{{ d.disk_id }}</span>
                    <div class="sub-line">{{ d.serial }}</div>
                </template>
                <template #cell-name="{ row: d }">{{ d.name || '-' }}</template>
                <template #cell-media="{ row: d }">{{ (d.media || '-').toUpperCase() }}</template>
                <template #cell-size="{ row: d }">{{ formatBytes(d.size_bytes) }}</template>
                <template #cell-usage="{ row: d }"
                    >{{ attr(d.attrs, 'usage') }} · {{ attr(d.attrs, 'gpfs_pool') }}</template
                >
                <template #cell-status="{ row: d }">
                    <StatusBadge :variant="diskVariant(d.status)" :label="diskStatusText(d.status)" />
                    <div v-if="d.reason" class="sub-line" :title="d.reason">{{ d.reason }}</div>
                </template>
                <template #cell-actions="{ row: d }">
                    <div class="row-actions">
                        <button
                            v-if="caps?.remove_disk"
                            class="icon-btn-table icon-danger"
                            :title="t('storage.clusterDetail.removeDisk')"
                            :disabled="!canChange"
                            @click="removingDisk = d"
                        >
                            <Trash2 :size="16" />
                        </button>
                    </div>
                </template>
            </DataTable>

            <!-- File systems -->
            <DataTable
                v-else-if="activeTab === 'filesystems'"
                :columns="fsColumns"
                :rows="cluster.filesystems || []"
                row-key="id"
            >
                <template #cell-name="{ row: f }"
                    ><span class="resource-name">{{ f.name }}</span></template
                >
                <template #cell-mount="{ row: f }"
                    ><span class="mono-cell">{{ f.mount_point }}</span></template
                >
                <template #cell-block="{ row: f }">{{ f.block_size || '-' }}</template>
                <template #cell-replicas="{ row: f }">{{
                    f.data_replicas
                        ? t('storage.clusterDetail.replicasValue', { data: f.data_replicas, meta: f.meta_replicas })
                        : '-'
                }}</template>
                <template #cell-capacity="{ row: f }">
                    <CapacityBar
                        v-if="f.capacity_bytes"
                        :capacity="f.capacity_bytes"
                        :used="f.capacity_bytes - f.free_bytes"
                    />
                    <span v-else class="text-secondary">-</span>
                </template>
                <template #cell-actions="{ row: f }">
                    <div class="row-actions">
                        <button
                            v-if="caps?.rebalance && managed"
                            class="icon-btn-table"
                            :title="t('storage.clusterDetail.rebalance')"
                            :disabled="!canChange"
                            @click="rebalancing = f"
                        >
                            <Shuffle :size="16" />
                        </button>
                    </div>
                </template>
            </DataTable>

            <!-- Pools -->
            <template v-else-if="activeTab === 'pools'">
                <div class="tab-actions">
                    <button
                        class="btn btn-primary btn-sm"
                        :disabled="!ready || !!cluster.active_pool_task"
                        @click="showPoolCreate = true"
                    >
                        <Plus :size="14" /> {{ t('storage.sharedPool.create') }}
                    </button>
                </div>
                <DataTable :columns="poolColumns" :rows="cluster.pools || []" row-key="id">
                    <template #empty>
                        <div class="empty-state">
                            <Database :size="40" style="opacity: 0.2; margin-bottom: 12px" />
                            <p>{{ t('storage.clusterDetail.noPools') }}</p>
                        </div>
                    </template>
                    <template #cell-name="{ row: p }">
                        <router-link
                            :to="{ name: 'storage-pool-detail', params: { id: p.id } }"
                            class="resource-link"
                            >{{ p.name }}</router-link
                        >
                    </template>
                    <template #cell-status="{ row: p }">
                        <StatusBadge :status="p.status" :label="poolStatusText(p.status)" />
                    </template>
                    <template #cell-media="{ row: p }">{{ p.media ? p.media.toUpperCase() : '-' }}</template>
                    <template #cell-path="{ row: p }"
                        ><span class="mono-cell">{{ p.mount_path || '-' }}</span></template
                    >
                    <template #cell-quota="{ row: p }">{{
                        p.quota_bytes ? formatBytes(p.quota_bytes) : t('storage.sharedPool.noQuota')
                    }}</template>
                    <template #cell-capacity="{ row: p }">
                        <CapacityBar
                            v-if="p.capacity_bytes"
                            :capacity="p.capacity_bytes"
                            :used="p.used_bytes"
                            :allocated="p.allocated_bytes"
                        />
                        <span v-else class="text-secondary">-</span>
                    </template>
                    <template #cell-actions="{ row: p }">
                        <div class="row-actions">
                            <button
                                v-if="managed"
                                class="icon-btn-table"
                                :title="t('storage.sharedPool.setQuota')"
                                :disabled="!['active', 'disabled'].includes(p.status) || !!cluster.active_pool_task"
                                @click="openQuota(p)"
                            >
                                <Gauge :size="16" />
                            </button>
                            <button
                                class="icon-btn-table icon-danger"
                                :title="t('actions.delete')"
                                :disabled="['creating', 'deleting'].includes(p.status) || !!cluster.active_pool_task"
                                @click="deletingPool = p"
                            >
                                <Trash2 :size="16" />
                            </button>
                        </div>
                    </template>
                </DataTable>
            </template>

            <!-- Tasks -->
            <DataTable v-else-if="activeTab === 'tasks'" :columns="taskColumns" :rows="tasks" row-key="id">
                <template #empty>
                    <div class="empty-state">
                        <p>{{ t('storage.cluster.noTasks') }}</p>
                    </div>
                </template>
                <template #cell-kind="{ row: task }">
                    <a href="#" class="resource-link" @click.prevent="openTask(task)">{{
                        taskKindText(t, te, task.kind)
                    }}</a>
                    <div class="sub-line">{{ task.id.slice(0, 8) }}</div>
                </template>
                <template #cell-status="{ row: task }">
                    <StatusBadge :variant="taskVariant(task.status)" :label="taskStatusText(t, te, task.status)" />
                </template>
                <template #cell-message="{ row: task }">
                    <span class="message-cell" :title="task.message">{{ task.message || '-' }}</span>
                </template>
                <template #cell-creator="{ row: task }">{{ task.creator || '-' }}</template>
                <template #cell-created_at="{ row: task }">{{ formatDateTime(task.created_at) }}</template>
            </DataTable>
        </template>

        <StorageExpandModal
            :show="expandMode !== null"
            :mode="expandMode || 'nodes'"
            :cluster="cluster"
            :backend="backend"
            @close="expandMode = null"
            @created="expanded"
        />
        <SharedPoolModal
            :show="showPoolCreate"
            :cluster="cluster"
            @close="showPoolCreate = false"
            @created="poolCreated"
        />

        <BaseModal
            :show="removingNode !== null"
            :title="t('storage.clusterDetail.removeNodeTitle', { host: removingNode?.hypervisor.name })"
            :loading="removeNodeBusy"
            form
            @close="removingNode = null"
            @submit="removeNode"
        >
            <div class="form-stack">
                <p class="intro">{{ t('storage.clusterDetail.removeNodeIntro') }}</p>
                <label class="checkbox-inline">
                    <input v-model="removeNodeForm.offline" type="checkbox" />
                    {{ t('storage.clusterDetail.offline') }}
                </label>
                <span class="form-hint">{{ t('storage.clusterDetail.offlineHint') }}</span>
                <div v-if="removeNodeForm.offline" class="form-group">
                    <label class="form-label">{{
                        t('storage.confirmHost', { host: removingNode?.hypervisor.name })
                    }}</label>
                    <input v-model="removeNodeForm.confirm" type="text" class="form-input" />
                </div>
                <label v-else class="checkbox-inline">
                    <input v-model="removeNodeForm.purge" type="checkbox" />
                    {{ t('storage.clusterDetail.purge') }}
                </label>
            </div>
            <template #footer>
                <span v-if="removeNodeError" class="footer-error text-error">{{ removeNodeError }}</span>
                <button type="button" class="btn btn-secondary" @click="removingNode = null">
                    {{ t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    class="btn btn-danger"
                    :disabled="
                        removeNodeBusy ||
                        (removeNodeForm.offline && removeNodeForm.confirm !== removingNode?.hypervisor.name)
                    "
                >
                    {{ t('storage.clusterDetail.removeNode') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="removingDisk !== null"
            :title="t('storage.clusterDetail.removeDisk')"
            :message="
                t('storage.clusterDetail.removeDiskMessage', {
                    disk: removingDisk?.name || removingDisk?.disk_id,
                    host: removingDisk?.hypervisor.name,
                })
            "
            :loading="removeDiskBusy"
            @close="removingDisk = null"
            @confirm="removeDisk"
        />
        <DeleteModal
            :show="rebalancing !== null"
            :title="t('storage.clusterDetail.rebalance')"
            :message="t('storage.clusterDetail.rebalanceMessage', { fs: rebalancing?.name })"
            :confirm-label="t('storage.clusterDetail.rebalance')"
            :loading="rebalanceBusy"
            @close="rebalancing = null"
            @confirm="rebalance"
        />
        <DeleteModal
            :show="deletingPool !== null"
            :title="t('storage.deletePoolTitle')"
            :message="t('storage.sharedPool.deleteMessage', { name: deletingPool?.name })"
            :loading="deletePoolBusy"
            @close="deletingPool = null"
            @confirm="deletePool"
        />

        <BaseModal
            :show="quotaPool !== null"
            :title="t('storage.sharedPool.setQuotaTitle', { name: quotaPool?.name })"
            :loading="quotaBusy"
            size="sm"
            form
            @close="quotaPool = null"
            @submit="saveQuota"
        >
            <div class="form-group">
                <label class="form-label">{{ t('storage.sharedPool.quota') }}</label>
                <input v-model="quotaGB" type="number" min="0" step="1" class="form-input" />
                <span class="form-hint">{{ t('storage.sharedPool.quotaHint') }}</span>
            </div>
            <template #footer>
                <span v-if="quotaError" class="footer-error text-error">{{ quotaError }}</span>
                <button type="button" class="btn btn-secondary" @click="quotaPool = null">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="quotaBusy">{{ t('actions.save') }}</button>
            </template>
        </BaseModal>

        <BaseModal
            :show="showDelete"
            :title="
                managed
                    ? t('storage.clusterDetail.deleteTitle', { name: cluster?.name })
                    : t('storage.clusterDetail.forgetTitle', { name: cluster?.name })
            "
            :loading="deleteBusy"
            form
            @close="showDelete = false"
            @submit="removeCluster"
        >
            <div class="form-stack">
                <p class="intro">
                    {{ managed ? t('storage.clusterDetail.deleteIntro') : t('storage.clusterDetail.forgetIntro') }}
                </p>
                <ul v-if="managed && cluster?.disks?.length" class="wipe-list">
                    <li v-for="d in cluster.disks" :key="d.id">
                        {{ d.hypervisor.name }} · {{ d.disk_id }} ({{ formatBytes(d.size_bytes) }})
                    </li>
                </ul>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.confirmPool', { name: cluster?.name }) }}</label>
                    <input v-model="deleteForm.confirm" type="text" class="form-input" />
                </div>
                <label v-if="managed" class="checkbox-inline">
                    <input v-model="deleteForm.purge" type="checkbox" />
                    {{ t('storage.clusterDetail.purge') }}
                </label>
            </div>
            <template #footer>
                <span v-if="deleteError" class="footer-error text-error">{{ deleteError }}</span>
                <button type="button" class="btn btn-secondary" @click="showDelete = false">
                    {{ t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    class="btn btn-danger"
                    :disabled="deleteBusy || deleteForm.confirm !== cluster?.name"
                >
                    {{ managed ? t('actions.delete') : t('storage.clusterDetail.forget') }}
                </button>
            </template>
        </BaseModal>
    </div>
</template>

<style scoped>
.banner {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    padding: 10px 14px;
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-3);
    font-size: var(--font-size-sm);
}

.banner-info {
    background: var(--primary-light);
    color: var(--primary-dark);
}

.banner-warning {
    background: var(--warning-light);
    color: var(--warning-dark);
}

.banner .resource-link + .resource-link::before {
    content: '·';
    margin-right: 8px;
}

.tab-actions {
    display: flex;
    justify-content: flex-end;
    margin-bottom: var(--spacing-3);
}

.sub-line {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    max-width: 320px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.mono-cell {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    word-break: break-all;
}

.message-cell {
    display: inline-block;
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
    color: var(--text-secondary);
}

.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: var(--spacing-5);
}

.form-stack {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.intro {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.form-hint {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: -4px;
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.wipe-list {
    margin: 0;
    padding-left: 20px;
    font-size: var(--font-size-xs);
    color: var(--error-color);
    max-height: 160px;
    overflow-y: auto;
}

.footer-error {
    margin-right: auto;
    font-size: var(--font-size-sm);
}
</style>
