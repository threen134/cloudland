<script setup lang="ts">
// "Storage" tab of a hypervisor (§8.3 of the local storage plan): the disks of the host, the storage pools set up
// on it, and the instances waiting for a pool. Every write asks for the host name to confirm, as the node formats
// disks or unmounts file systems.
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import {
    RefreshCw,
    Plus,
    Loader2,
    HardDrive,
    Wrench,
    Trash2,
    Undo2,
    AlertTriangle,
    BarChart3,
    PlusCircle,
    Replace,
} from 'lucide-vue-next'
import type { Hypervisor } from '../../api/hypervisors'
import {
    hostStorageApi,
    storagePoolsApi,
    type HostDisk,
    type HostPool,
    type PoolLayout,
    type StoragePool,
    type UsageEntry,
} from '../../api/storagePools'
import BaseModal from '../modals/BaseModal.vue'
import StatusBadge from '../base/StatusBadge.vue'
import CapacityBar from './CapacityBar.vue'
import { formatBytes } from '../../utils/format'
import { errorMessage } from '../../utils/error'
import { useToast } from '../../composables/useToast'
import type { StatusVariant } from '../../utils/status'

const props = defineProps<{ hypervisor: Hypervisor }>()

const { t, te } = useI18n()
const toast = useToast()

const disks = ref<HostDisk[]>([])
const pools = ref<HostPool[]>([])
const pending = ref<{ id: string; name: string }[]>([])
const allPools = ref<StoragePool[]>([])
const loading = ref(true)
const scanning = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const uuid = computed(() => props.hypervisor.uuid)
const hostname = computed(() => props.hypervisor.hostname)

const load = async (silent = false) => {
    if (!silent) loading.value = true
    try {
        const [d, p] = await Promise.all([hostStorageApi.disks(uuid.value), hostStorageApi.pools(uuid.value)])
        disks.value = d
        pools.value = p.storage_pools
        pending.value = p.pending_instances
    } catch (err) {
        if (!silent) toast.error(errorMessage(err, t('storage.loadFailed')))
    } finally {
        loading.value = false
    }
    schedulePoll()
}

// Commands run on the node in the background: keep refreshing while something is in progress
const busy = computed(
    () =>
        scanning.value ||
        pools.value.some(
            (p) =>
                ['creating', 'extending', 'removing'].includes(p.status) || (p.sync_percent > 0 && p.sync_percent < 100)
        )
)
const schedulePoll = () => {
    if (pollTimer) clearTimeout(pollTimer)
    pollTimer = busy.value ? setTimeout(() => load(true), 4000) : null
}

onMounted(async () => {
    await load()
    try {
        allPools.value = (await storagePoolsApi.list({ limit: 200 })).storage_pools
    } catch {
        allPools.value = []
    }
})
onBeforeUnmount(() => {
    if (pollTimer) clearTimeout(pollTimer)
})

// ---- disks ----

// The scan is a snapshot: a disk that went into a pool afterwards is shown as used by that pool
const poolOfDisk = computed(() => {
    const map = new Map<string, string>()
    for (const p of pools.value) for (const d of p.devices || []) map.set(d.id, p.storage_pool.name)
    return map
})
const effectiveState = (d: HostDisk) => (poolOfDisk.value.has(d.disk_id) ? 'in_use' : d.state)

const lastScan = computed(() => disks.value.reduce((latest, d) => (d.scanned_at > latest ? d.scanned_at : latest), ''))

const scan = async () => {
    scanning.value = true
    const before = lastScan.value
    try {
        await hostStorageApi.scanDisks(uuid.value)
        // The result arrives with the next callback of the node
        for (let i = 0; i < 20; i++) {
            await new Promise((r) => setTimeout(r, 3000))
            disks.value = await hostStorageApi.disks(uuid.value)
            if (lastScan.value && lastScan.value !== before) break
        }
    } catch (err) {
        toast.error(errorMessage(err, t('storage.scanFailed')))
    } finally {
        scanning.value = false
    }
}

const diskStateVariant = (state: string): StatusVariant => {
    switch (state) {
        case 'free':
            return 'success'
        case 'dirty':
        case 'cloudland_pool':
            return 'warning'
        case 'shared':
        case 'unknown_member':
            return 'error'
        default:
            return 'neutral'
    }
}
const diskStateText = (state: string) => (te(`storage.diskStates.${state}`) ? t(`storage.diskStates.${state}`) : state)

const setMedia = async (disk: HostDisk, media: string) => {
    try {
        const updated = await hostStorageApi.setDiskMedia(uuid.value, disk.id, media)
        Object.assign(disk, updated)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.updateFailed')))
    }
}

// ---- pools ----

const hostPoolStatusVariant = (p: HostPool): StatusVariant => {
    switch (p.status) {
        case 'ready':
            return 'success'
        case 'degraded':
        case 'maintenance':
            return 'warning'
        case 'unavailable':
        case 'lost':
        case 'error':
            return 'error'
        default:
            return 'pending'
    }
}
const hostPoolStatusText = (status: string) =>
    te(`storage.hostPoolStatus.${status}`) ? t(`storage.hostPoolStatus.${status}`) : status
const layoutText = (layout: string) =>
    te(`storage.layouts.${layout}`) ? t(`storage.layouts.${layout}`) : layout || '-'
const membersText = (p: HostPool) => (p.devices || []).map((d) => d.name).join(', ') || '-'
// The pool reports healthy again after it was declared lost: it can be restored
const recovered = (p: HostPool) => p.status === 'lost' && ['ready', 'degraded'].includes(p.reported_status)

// ---- create / extend / replace ----

const showCreate = ref(false)
const createMode = ref<'create' | 'extend' | 'replace'>('create')
const targetPool = ref<HostPool | null>(null)
const form = ref({
    pool: '',
    layout: 'single' as PoolLayout,
    disks: [] as string[],
    wipe: false,
    allowMismatch: false,
    failedDisk: '',
    newDisk: '',
    confirm: '',
})
const submitting = ref(false)

const configuredPoolIds = computed(() => new Set(pools.value.map((p) => p.storage_pool.id)))
const poolChoices = computed(() =>
    allPools.value.filter((p) => !p.builtin && p.status === 'active' && !configuredPoolIds.value.has(p.id))
)
const selectablesDisks = computed(() =>
    disks.value.filter((d) => (d.state === 'free' || d.state === 'dirty') && !poolOfDisk.value.has(d.disk_id))
)
const chosenDisks = computed(() => disks.value.filter((d) => form.value.disks.includes(d.disk_id)))
// Replace keeps its disk in form.newDisk, not in form.disks: without it the submit guard would let a dirty disk
// through without a wipe and the host would refuse the request
const dirtyChosen = computed(() => {
    const list =
        createMode.value === 'replace' ? disks.value.filter((d) => d.disk_id === form.value.newDisk) : chosenDisks.value
    return list.filter((d) => d.state === 'dirty')
})
const poolMedia = computed(() => {
    const id = createMode.value === 'create' ? form.value.pool : targetPool.value?.storage_pool.id
    return allPools.value.find((p) => p.id === id)?.media || ''
})
const mismatched = computed(() => {
    const list =
        createMode.value === 'replace' ? disks.value.filter((d) => d.disk_id === form.value.newDisk) : chosenDisks.value
    const media = poolMedia.value
    const kinds = new Set(list.map((d) => d.media).filter(Boolean))
    return list.filter((d) => (media && d.media && d.media !== media) || kinds.size > 1)
})
const raidLayout = computed(
    () =>
        (createMode.value === 'create' && form.value.layout === 'raid1') ||
        (createMode.value === 'extend' && targetPool.value?.layout === 'raid1')
)
// Disks pair up in the order they were chosen; a pair gives the size of its smaller disk
const raidPairs = computed(() => {
    const list = form.value.disks.map((id) => disks.value.find((d) => d.disk_id === id)).filter(Boolean) as HostDisk[]
    const pairs: { a: HostDisk; b?: HostDisk; usable: number; wasted: number }[] = []
    for (let i = 0; i < list.length; i += 2) {
        const a = list[i]
        const b = list[i + 1]
        const usable = b ? Math.min(a.size_bytes, b.size_bytes) : 0
        const wasted = b ? Math.abs(a.size_bytes - b.size_bytes) : 0
        pairs.push({ a, b, usable, wasted })
    }
    return pairs
})
const diskCountError = computed(() => {
    const n = form.value.disks.length
    if (createMode.value === 'replace') return ''
    if (n === 0) return t('storage.selectDisksHint')
    if (createMode.value === 'create' && form.value.layout === 'single' && n !== 1) return t('storage.singleOneDisk')
    if (raidLayout.value && n % 2 !== 0) return t('storage.raidPairsHint')
    return ''
})
const canSubmit = computed(() => {
    if (form.value.confirm !== hostname.value) return false
    if (dirtyChosen.value.length && !form.value.wipe) return false
    if (mismatched.value.length && !form.value.allowMismatch) return false
    if (createMode.value === 'create' && !form.value.pool) return false
    if (createMode.value === 'replace') return !!form.value.failedDisk && !!form.value.newDisk
    return !diskCountError.value
})

const resetForm = () => {
    form.value = {
        pool: '',
        layout: 'single',
        disks: [],
        wipe: false,
        allowMismatch: false,
        failedDisk: '',
        newDisk: '',
        confirm: '',
    }
}
const openCreate = () => {
    resetForm()
    createMode.value = 'create'
    targetPool.value = null
    form.value.pool = poolChoices.value[0]?.id || ''
    showCreate.value = true
}
const openExtend = (p: HostPool) => {
    resetForm()
    createMode.value = 'extend'
    targetPool.value = p
    showCreate.value = true
}
const openReplace = (p: HostPool) => {
    resetForm()
    createMode.value = 'replace'
    targetPool.value = p
    showCreate.value = true
}
const toggleDisk = (id: string) => {
    const i = form.value.disks.indexOf(id)
    if (i >= 0) form.value.disks.splice(i, 1)
    else form.value.disks.push(id)
}

const submitCreate = async () => {
    if (!canSubmit.value) return
    submitting.value = true
    try {
        if (createMode.value === 'create') {
            await hostStorageApi.createPool(uuid.value, {
                storage_pool: { id: form.value.pool },
                layout: form.value.layout,
                disks: form.value.disks,
                wipe: form.value.wipe,
                allow_media_mismatch: form.value.allowMismatch,
                confirm: form.value.confirm,
            })
        } else if (createMode.value === 'extend' && targetPool.value) {
            await hostStorageApi.extendPool(uuid.value, targetPool.value.storage_pool.id, {
                disks: form.value.disks,
                wipe: form.value.wipe,
                allow_media_mismatch: form.value.allowMismatch,
                confirm: form.value.confirm,
            })
        } else if (targetPool.value) {
            await hostStorageApi.replaceDisk(uuid.value, targetPool.value.storage_pool.id, {
                failed_disk: form.value.failedDisk,
                new_disk: form.value.newDisk,
                wipe: form.value.wipe,
                allow_media_mismatch: form.value.allowMismatch,
                confirm: form.value.confirm,
            })
        }
        toast.success(t('storage.submitted'))
        showCreate.value = false
        await load(true)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    } finally {
        submitting.value = false
    }
}

// ---- remove / lost / restore / maintenance / adopt ----

const confirmAction = ref<'remove' | 'lost' | 'adopt' | null>(null)
const confirmPool = ref<HostPool | null>(null)
const confirmDisk = ref<HostDisk | null>(null)
const confirmText = ref('')
const removeForce = ref(false)
const offlineAck = ref(false)
const acting = ref(false)

const openConfirm = (action: 'remove' | 'lost', p: HostPool) => {
    confirmAction.value = action
    confirmPool.value = p
    confirmDisk.value = null
    confirmText.value = ''
    removeForce.value = false
    offlineAck.value = false
}
const openAdopt = (d: HostDisk) => {
    confirmAction.value = 'adopt'
    confirmDisk.value = d
    confirmPool.value = null
    confirmText.value = ''
}
const lostOffline = computed(() => confirmPool.value?.reason === 'node_offline')
const confirmReady = computed(
    () =>
        confirmText.value === hostname.value &&
        (confirmAction.value !== 'lost' || !lostOffline.value || offlineAck.value)
)

const runConfirm = async () => {
    if (!confirmReady.value) return
    acting.value = true
    try {
        if (confirmAction.value === 'remove' && confirmPool.value) {
            await hostStorageApi.removePool(
                uuid.value,
                confirmPool.value.storage_pool.id,
                confirmText.value,
                removeForce.value
            )
        } else if (confirmAction.value === 'lost' && confirmPool.value) {
            const r = await hostStorageApi.declareLost(
                uuid.value,
                confirmPool.value.storage_pool.id,
                confirmText.value,
                offlineAck.value
            )
            toast.success(t('storage.lostDone', { n: r.volumes }))
        } else if (confirmAction.value === 'adopt' && confirmDisk.value?.pool_uuid) {
            const pool = allPools.value.find((p) => p.id.startsWith(confirmDisk.value?.pool_uuid || '-'))
            await hostStorageApi.adopt(uuid.value, pool?.id || confirmDisk.value.pool_uuid, confirmText.value)
        }
        confirmAction.value = null
        await load(true)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    } finally {
        acting.value = false
    }
}

const restore = async (p: HostPool) => {
    try {
        const r = await hostStorageApi.restore(uuid.value, p.storage_pool.id)
        toast.success(t('storage.restoreDone', { n: r.volumes }))
        await load(true)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    }
}

const toggleMaintenance = async (p: HostPool) => {
    try {
        await hostStorageApi.setMaintenance(uuid.value, p.storage_pool.id, !p.maintenance)
        await load(true)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    }
}

// ---- usage ----

const usagePool = ref<HostPool | null>(null)
const usage = ref<UsageEntry[]>([])
const usageAt = ref('')
const usageLoading = ref(false)
const openUsage = async (p: HostPool) => {
    usagePool.value = p
    usage.value = p.usage || []
    usageAt.value = p.usage_at || ''
}
const scanUsage = async () => {
    if (!usagePool.value) return
    usageLoading.value = true
    const before = usageAt.value
    try {
        await hostStorageApi.scanUsage(uuid.value, usagePool.value.storage_pool.id)
        for (let i = 0; i < 30; i++) {
            await new Promise((r) => setTimeout(r, 3000))
            const r = await hostStorageApi.usage(uuid.value, usagePool.value.storage_pool.id)
            if (r.usage_at && r.usage_at !== before) {
                usage.value = r.usage
                usageAt.value = r.usage_at
                break
            }
        }
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    } finally {
        usageLoading.value = false
    }
}
</script>

<template>
    <div class="storage-tab">
        <!-- Pools -->
        <div class="info-card card">
            <div class="section-head">
                <h3 class="card-section-title">{{ t('storage.pools') }}</h3>
                <div class="section-actions">
                    <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load()">
                        <RefreshCw :size="14" :class="{ spinning: loading }" />
                    </button>
                    <button
                        type="button"
                        class="btn btn-primary btn-sm"
                        :disabled="!poolChoices.length"
                        :title="poolChoices.length ? '' : t('storage.noPoolToAdd')"
                        @click="openCreate"
                    >
                        <Plus :size="14" /> {{ t('storage.actions.addPool') }}
                    </button>
                </div>
            </div>
            <div class="table-wrap">
                <table class="data-table">
                    <thead>
                        <tr>
                            <th>{{ t('storage.pool') }}</th>
                            <th>{{ t('storage.layout') }}</th>
                            <th>{{ t('storage.members') }}</th>
                            <th>{{ t('storage.capacity') }}</th>
                            <th>{{ t('storage.status') }}</th>
                            <th>{{ t('storage.volumes') }}</th>
                            <th class="text-right">{{ t('dashboard.table.actions') }}</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-for="p in pools" :key="p.storage_pool.id">
                            <td>
                                <router-link
                                    :to="{ name: 'storage-pool-detail', params: { id: p.storage_pool.id } }"
                                    class="resource-link"
                                >
                                    {{ p.storage_pool.name }}
                                </router-link>
                                <div class="cell-sub">
                                    <span v-if="p.builtin">{{ t('storage.builtinNote') }}</span>
                                    <span v-else-if="p.media">{{ p.media.toUpperCase() }}</span>
                                </div>
                            </td>
                            <td>{{ p.builtin ? '-' : layoutText(p.layout) }}</td>
                            <td class="cell-members" :title="membersText(p)">{{ p.builtin ? '-' : membersText(p) }}</td>
                            <td>
                                <CapacityBar
                                    v-if="p.capacity_bytes"
                                    :capacity="p.capacity_bytes"
                                    :used="p.used_bytes"
                                    :allocated="p.allocated_bytes"
                                    :reserved="p.reserved_bytes"
                                />
                                <span v-else class="text-secondary">-</span>
                            </td>
                            <td>
                                <StatusBadge
                                    :variant="hostPoolStatusVariant(p)"
                                    :label="hostPoolStatusText(p.status)"
                                />
                                <div v-if="p.sync_percent > 0 && p.sync_percent < 100" class="cell-sub">
                                    {{ t('storage.syncing', { percent: p.sync_percent }) }}
                                </div>
                                <div v-if="p.reason" class="cell-sub cell-reason" :title="p.reason">{{ p.reason }}</div>
                                <div v-if="recovered(p)" class="cell-sub text-success">
                                    {{ t('storage.reportedRecovered') }}
                                </div>
                                <div v-if="p.storage_full_paused" class="cell-sub text-error">
                                    {{ t('storage.pausedFull', { n: p.storage_full_paused }) }}
                                </div>
                            </td>
                            <td>{{ p.volume_count }}</td>
                            <td>
                                <div class="row-actions">
                                    <button
                                        type="button"
                                        class="icon-btn-table"
                                        :title="t('storage.actions.usage')"
                                        @click="openUsage(p)"
                                    >
                                        <BarChart3 :size="16" />
                                    </button>
                                    <template v-if="!p.builtin">
                                        <button
                                            type="button"
                                            class="icon-btn-table"
                                            :title="t('storage.actions.extend')"
                                            :disabled="!['ready', 'degraded'].includes(p.status)"
                                            @click="openExtend(p)"
                                        >
                                            <PlusCircle :size="16" />
                                        </button>
                                        <button
                                            v-if="p.layout === 'raid1'"
                                            type="button"
                                            class="icon-btn-table"
                                            :title="t('storage.actions.replaceDisk')"
                                            :disabled="!['ready', 'degraded'].includes(p.status)"
                                            @click="openReplace(p)"
                                        >
                                            <Replace :size="16" />
                                        </button>
                                        <button
                                            type="button"
                                            class="icon-btn-table"
                                            :class="{ 'is-active': p.maintenance }"
                                            :title="
                                                p.maintenance
                                                    ? t('storage.actions.maintenanceOff')
                                                    : t('storage.actions.maintenanceOn')
                                            "
                                            :disabled="
                                                !['ready', 'degraded', 'unavailable', 'maintenance', 'lost'].includes(
                                                    p.status
                                                )
                                            "
                                            @click="toggleMaintenance(p)"
                                        >
                                            <Wrench :size="16" />
                                        </button>
                                        <button
                                            v-if="['unavailable', 'maintenance'].includes(p.status)"
                                            type="button"
                                            class="icon-btn-table icon-danger"
                                            :title="t('storage.actions.declareLost')"
                                            @click="openConfirm('lost', p)"
                                        >
                                            <AlertTriangle :size="16" />
                                        </button>
                                        <button
                                            v-if="recovered(p)"
                                            type="button"
                                            class="icon-btn-table"
                                            :title="t('storage.actions.restore')"
                                            @click="restore(p)"
                                        >
                                            <Undo2 :size="16" />
                                        </button>
                                        <button
                                            type="button"
                                            class="icon-btn-table icon-danger"
                                            :title="t('storage.actions.remove')"
                                            :disabled="['creating', 'extending', 'removing'].includes(p.status)"
                                            @click="openConfirm('remove', p)"
                                        >
                                            <Trash2 :size="16" />
                                        </button>
                                    </template>
                                </div>
                            </td>
                        </tr>
                        <tr v-if="!loading && !pools.length">
                            <td colspan="7" class="text-secondary text-center">{{ t('messages.noData') }}</td>
                        </tr>
                    </tbody>
                </table>
            </div>
            <p class="hint">{{ t('storage.alarmHint') }}</p>
        </div>

        <!-- Instances waiting for their pool -->
        <div v-if="pending.length" class="info-card card">
            <h3 class="card-section-title">{{ t('storage.pendingInstances') }}</h3>
            <p class="hint">{{ t('storage.pendingHint') }}</p>
            <div class="pending-list">
                <router-link
                    v-for="i in pending"
                    :key="i.id"
                    :to="{ name: 'instance-detail', params: { id: i.id } }"
                    class="resource-link"
                >
                    {{ i.name }}
                </router-link>
            </div>
        </div>

        <!-- Disks -->
        <div class="info-card card">
            <div class="section-head">
                <h3 class="card-section-title">{{ t('storage.disks') }}</h3>
                <div class="section-actions">
                    <span v-if="lastScan" class="text-secondary hint-inline">{{
                        t('storage.lastScan', { time: lastScan.slice(0, 16) })
                    }}</span>
                    <button type="button" class="btn btn-secondary btn-sm" :disabled="scanning" @click="scan">
                        <Loader2 v-if="scanning" :size="14" class="spinning" />
                        <HardDrive v-else :size="14" />
                        {{ scanning ? t('storage.scanning') : t('storage.scan') }}
                    </button>
                </div>
            </div>
            <div class="table-wrap">
                <table class="data-table">
                    <thead>
                        <tr>
                            <th>{{ t('storage.diskName') }}</th>
                            <th>{{ t('storage.diskId') }}</th>
                            <th>{{ t('storage.diskModel') }}</th>
                            <th>{{ t('storage.size') }}</th>
                            <th>{{ t('storage.media') }}</th>
                            <th>{{ t('storage.state') }}</th>
                            <th>{{ t('storage.pool') }}</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-for="d in disks" :key="d.id">
                            <td>
                                {{ d.name }}
                                <div class="cell-sub">{{ d.transport || '-' }}</div>
                            </td>
                            <td class="cell-id" :title="d.disk_id">{{ d.disk_id }}</td>
                            <td>
                                {{ d.model || '-' }}
                                <div class="cell-sub">{{ d.serial }}</div>
                            </td>
                            <td>{{ formatBytes(d.size_bytes) }}</td>
                            <td>
                                <select
                                    class="form-input media-select"
                                    :value="d.media_source === 'manual' ? d.media : ''"
                                    :title="
                                        t('storage.mediaDetected', { media: (d.detected_media || '-').toUpperCase() })
                                    "
                                    @change="setMedia(d, ($event.target as HTMLSelectElement).value)"
                                >
                                    <option value="">
                                        {{ t('storage.mediaAuto', { media: (d.detected_media || '-').toUpperCase() }) }}
                                    </option>
                                    <option value="ssd">SSD</option>
                                    <option value="hdd">HDD</option>
                                    <option value="nvme">NVMe</option>
                                </select>
                                <span v-if="d.media_source === 'manual'" class="badge badge-secondary">{{
                                    t('storage.mediaManual')
                                }}</span>
                            </td>
                            <td>
                                <StatusBadge
                                    :variant="diskStateVariant(effectiveState(d))"
                                    :label="diskStateText(effectiveState(d))"
                                />
                                <div v-if="poolOfDisk.has(d.disk_id) && d.state !== 'in_use'" class="cell-sub">
                                    {{ poolOfDisk.get(d.disk_id) }}
                                </div>
                                <div v-else-if="d.detail" class="cell-sub cell-reason" :title="d.detail">
                                    {{ d.detail }}
                                </div>
                            </td>
                            <td>
                                <template v-if="d.pool_uuid">
                                    {{ d.pool_name || d.pool_uuid.slice(0, 8) }}
                                    <div v-if="d.state === 'cloudland_pool'" class="cell-sub">
                                        {{
                                            t('storage.orphanInfo', {
                                                host: d.owner_hostid || '-',
                                                n: d.orphan_count || 0,
                                            })
                                        }}
                                        <button
                                            type="button"
                                            class="btn btn-secondary btn-sm adopt-btn"
                                            @click="openAdopt(d)"
                                        >
                                            {{ t('storage.actions.adopt') }}
                                        </button>
                                    </div>
                                </template>
                                <span v-else>-</span>
                            </td>
                        </tr>
                        <tr v-if="!loading && !disks.length">
                            <td colspan="7" class="text-secondary text-center">{{ t('storage.noDisks') }}</td>
                        </tr>
                    </tbody>
                </table>
            </div>
            <p class="hint">{{ t('storage.scanHint') }}</p>
        </div>

        <!-- Create / extend / replace -->
        <BaseModal
            :show="showCreate"
            :title="
                createMode === 'create'
                    ? t('storage.createTitle', { host: hostname })
                    : createMode === 'extend'
                      ? t('storage.extendTitle', { pool: targetPool?.storage_pool.name })
                      : t('storage.replaceTitle', { pool: targetPool?.storage_pool.name })
            "
            size="lg"
            form
            :loading="submitting"
            @close="showCreate = false"
            @submit="submitCreate"
        >
            <template v-if="createMode === 'create'">
                <div class="form-group">
                    <label class="form-label">{{ t('storage.pool') }}</label>
                    <select v-model="form.pool" class="form-input">
                        <option v-for="p in poolChoices" :key="p.id" :value="p.id">
                            {{ p.name }}{{ p.media ? ` (${p.media.toUpperCase()})` : '' }}
                        </option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.layout') }}</label>
                    <div class="radio-row">
                        <label v-for="l in ['single', 'linear', 'raid1']" :key="l" class="radio-label">
                            <input v-model="form.layout" type="radio" :value="l" /> {{ layoutText(l) }}
                        </label>
                    </div>
                    <p v-if="form.layout === 'linear'" class="hint text-warning">
                        {{ t('storage.linearNoRedundancy') }}
                    </p>
                </div>
            </template>
            <template v-if="createMode === 'replace'">
                <div class="form-group">
                    <label class="form-label">{{ t('storage.failedDisk') }}</label>
                    <select v-model="form.failedDisk" class="form-input">
                        <option v-for="d in targetPool?.devices || []" :key="d.id" :value="d.id">
                            {{ d.name }} · {{ d.array }} · {{ formatBytes(d.size_bytes) }}
                        </option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.newDisk') }}</label>
                    <select v-model="form.newDisk" class="form-input">
                        <option v-for="d in selectablesDisks" :key="d.disk_id" :value="d.disk_id">
                            {{ d.name }} · {{ formatBytes(d.size_bytes) }} · {{ (d.media || '-').toUpperCase() }} ·
                            {{ diskStateText(d.state) }}
                        </option>
                    </select>
                </div>
            </template>
            <div v-else class="form-group">
                <label class="form-label">{{ t('storage.selectDisks') }}</label>
                <div v-if="!selectablesDisks.length" class="hint">{{ t('storage.noFreeDisk') }}</div>
                <label v-for="d in selectablesDisks" :key="d.disk_id" class="disk-choice">
                    <input type="checkbox" :checked="form.disks.includes(d.disk_id)" @change="toggleDisk(d.disk_id)" />
                    <span class="disk-choice-name">{{ d.name }}</span>
                    <span>{{ formatBytes(d.size_bytes) }}</span>
                    <span :class="{ 'text-error': mismatched.includes(d) }">{{ (d.media || '-').toUpperCase() }}</span>
                    <StatusBadge :variant="diskStateVariant(d.state)" :label="diskStateText(d.state)" />
                    <span class="cell-id">{{ d.disk_id }}</span>
                </label>
                <p v-if="diskCountError" class="hint">{{ diskCountError }}</p>
                <div v-if="raidLayout && raidPairs.length" class="raid-pairs">
                    <div v-for="(pair, i) in raidPairs" :key="i" class="raid-pair">
                        {{ t('storage.raidPair', { n: i + 1 }) }}: {{ pair.a.name }} + {{ pair.b?.name || '?' }}
                        <template v-if="pair.b">
                            · {{ t('storage.pairUsable', { size: formatBytes(pair.usable) }) }}
                            <span v-if="pair.wasted > pair.usable * 0.01" class="text-warning">
                                · {{ t('storage.pairWaste', { size: formatBytes(pair.wasted) }) }}
                            </span>
                        </template>
                    </div>
                </div>
            </div>
            <div v-if="dirtyChosen.length" class="form-group">
                <label class="checkbox-label">
                    <input v-model="form.wipe" type="checkbox" />
                    {{ t('storage.wipe') }}
                </label>
                <p class="hint text-error">
                    {{ t('storage.wipeList', { disks: dirtyChosen.map((d) => d.name).join(', ') }) }}
                </p>
            </div>
            <div v-if="mismatched.length" class="form-group">
                <p class="hint text-error">
                    {{
                        t('storage.mediaMismatch', {
                            disks: mismatched.map((d) => d.name).join(', '),
                            media: (poolMedia || '-').toUpperCase(),
                        })
                    }}
                </p>
                <label class="checkbox-label">
                    <input v-model="form.allowMismatch" type="checkbox" />
                    {{ t('storage.allowMismatch') }}
                </label>
            </div>
            <div class="form-group">
                <label class="form-label">{{ t('storage.confirmHost', { host: hostname }) }}</label>
                <input v-model="form.confirm" type="text" class="form-input" :placeholder="hostname" />
            </div>
            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showCreate = false">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-danger" :disabled="!canSubmit || submitting">
                    <Loader2 v-if="submitting" :size="14" class="spinning" />
                    {{ t('storage.submitFormat') }}
                </button>
            </template>
        </BaseModal>

        <!-- Remove / declare lost / adopt -->
        <BaseModal
            :show="confirmAction !== null"
            :title="
                confirmAction === 'remove'
                    ? t('storage.removeTitle', { pool: confirmPool?.storage_pool.name })
                    : confirmAction === 'lost'
                      ? t('storage.lostTitle', { pool: confirmPool?.storage_pool.name })
                      : t('storage.adoptTitle', { pool: confirmDisk?.pool_name || confirmDisk?.pool_uuid })
            "
            form
            :loading="acting"
            @close="confirmAction = null"
            @submit="runConfirm"
        >
            <template v-if="confirmAction === 'remove'">
                <p class="hint">{{ t('storage.removeDesc') }}</p>
                <label class="checkbox-label">
                    <input v-model="removeForce" type="checkbox" />
                    {{ t('storage.removeForce') }}
                </label>
            </template>
            <template v-else-if="confirmAction === 'lost'">
                <p class="hint">{{ t('storage.lostDesc') }}</p>
                <template v-if="lostOffline">
                    <p class="hint text-error">{{ t('storage.lostOfflineWarn') }}</p>
                    <label class="checkbox-label">
                        <input v-model="offlineAck" type="checkbox" />
                        {{ t('storage.lostOfflineAck') }}
                    </label>
                </template>
            </template>
            <template v-else>
                <p class="hint">
                    {{
                        t('storage.adoptDesc', {
                            host: confirmDisk?.owner_hostid || '-',
                            n: confirmDisk?.orphan_count || 0,
                        })
                    }}
                </p>
            </template>
            <div class="form-group">
                <label class="form-label">{{ t('storage.confirmHost', { host: hostname }) }}</label>
                <input v-model="confirmText" type="text" class="form-input" :placeholder="hostname" />
            </div>
            <template #footer>
                <button type="button" class="btn btn-secondary" @click="confirmAction = null">
                    {{ t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    :class="confirmAction === 'adopt' ? 'btn btn-primary' : 'btn btn-danger'"
                    :disabled="!confirmReady || acting"
                >
                    <Loader2 v-if="acting" :size="14" class="spinning" />
                    {{ t('actions.confirm') }}
                </button>
            </template>
        </BaseModal>

        <!-- Usage -->
        <BaseModal
            :show="usagePool !== null"
            :title="t('storage.usageTitle', { pool: usagePool?.storage_pool.name })"
            size="xl"
            @close="usagePool = null"
        >
            <div class="usage-head">
                <span class="text-secondary">{{
                    usageAt ? t('storage.usageAt', { time: usageAt.slice(0, 16) }) : t('storage.usageNever')
                }}</span>
                <button type="button" class="btn btn-secondary btn-sm" :disabled="usageLoading" @click="scanUsage">
                    <Loader2 v-if="usageLoading" :size="14" class="spinning" />
                    {{ t('storage.usageScan') }}
                </button>
            </div>
            <table v-if="usage.length" class="data-table">
                <thead>
                    <tr>
                        <th>{{ t('storage.usageFile') }}</th>
                        <th>{{ t('storage.usageActual') }}</th>
                        <th>{{ t('storage.usageVirtual') }}</th>
                        <th>{{ t('storage.usageVolume') }}</th>
                        <th>{{ t('storage.usageInstance') }}</th>
                        <th>{{ t('storage.usageOwner') }}</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-for="u in usage" :key="u.path">
                        <td class="cell-id">{{ u.path }}</td>
                        <td>{{ formatBytes(u.bytes) }}</td>
                        <td>{{ formatBytes(u.size) }}</td>
                        <td>{{ u.volume_name || '-' }}</td>
                        <td>{{ u.instance || '-' }}</td>
                        <td>{{ u.owner || '-' }}</td>
                    </tr>
                </tbody>
            </table>
            <p v-else class="hint">{{ t('storage.usageEmpty') }}</p>
        </BaseModal>
    </div>
</template>

<style scoped>
.storage-tab {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-5);
}

.section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-3);
}

.section-head .card-section-title {
    margin-bottom: 0;
}

.section-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.table-wrap {
    overflow-x: auto;
}

.cell-sub {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: 2px;
}

.cell-reason {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.cell-id {
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-xs);
}

.cell-members {
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.media-select {
    width: auto;
    min-width: 120px;
    padding: 4px 8px;
    font-size: var(--font-size-xs);
}

.hint {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: var(--spacing-2);
}

.hint-inline {
    font-size: var(--font-size-xs);
}

.pending-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-3);
}

.radio-row {
    display: flex;
    gap: var(--spacing-4);
}

.radio-label,
.checkbox-label {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.disk-choice {
    display: grid;
    grid-template-columns: 20px 80px 90px 60px auto 1fr;
    align-items: center;
    gap: 8px;
    padding: 6px 0;
    border-bottom: 1px solid var(--border-light);
    font-size: var(--font-size-sm);
    cursor: pointer;
}

.disk-choice-name {
    font-weight: var(--font-weight-medium);
}

.raid-pairs {
    margin-top: var(--spacing-2);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.adopt-btn {
    margin-left: 6px;
    padding: 2px 8px;
}

.usage-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: var(--spacing-3);
}

.text-warning {
    color: var(--warning-dark, var(--warning-color));
}

.text-success {
    color: var(--success-color);
}
</style>
