<script setup lang="ts">
// Hosts, their roles and the disks of the disk hosts of a storage cluster plan (shared-storage-design.md §6.3,
// §13.3), shared by the precheck, the creation wizard and the expansion dialogs. The roles and suggested roles come
// from the backend of the kind (GET /storage_backends, §4.5); the backend applies the role rules and refuses disks
// that are not free in a fresh scan, so the picker only guides.
//   mode "roles":  pick hosts and their roles, then disks of the hosts with the disk role (precheck, creation, adding
//                  hosts)
//   mode "hosts":  pick hosts only (importing a cluster: its hosts are clients)
//   mode "disks":  the hosts are given (members of a cluster), pick disks of them (adding disks)
import { ref, computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw, ScanLine } from 'lucide-vue-next'
import StatusBadge from '../base/StatusBadge.vue'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { hostStorageApi, type HostDisk } from '../../api/storagePools'
import type { StorageBackend, StorageRole } from '../../api/storageClusters'
import { roleText } from '../../utils/storageCluster'
import { formatBytes, formatDateTime } from '../../utils/format'
import { errorMessage } from '../../utils/error'
import type { StatusVariant } from '../../utils/status'

export interface HostPick {
    /** Roles of the selected hosts, by host UUID */
    roles: Record<string, StorageRole[]>
    /** Chosen disks: "<host uuid>/<disk id>" -> wipe */
    disks: Record<string, boolean>
}

const props = withDefaults(
    defineProps<{
        backend?: StorageBackend
        modelValue: HostPick
        mode?: 'roles' | 'hosts' | 'disks'
        /** Hosts not offered (members of the cluster, when adding hosts) */
        exclude?: string[]
        /** The only hosts offered (members of the cluster, when adding disks) */
        only?: string[]
        /**
         * The shared LUNs of a SAN are the disks to choose, and only those (GPFS shared disk layout): the same LUN
         * ticked under every host that is to serve it
         */
        sharedDisks?: boolean
        /** The LUNs (disk ids) the cluster uses already: a host only becomes one more server of them, never wipes them */
        usedDisks?: string[]
    }>(),
    { mode: 'roles', exclude: () => [], only: undefined, backend: undefined, sharedDisks: false, usedDisks: () => [] }
)
const emit = defineEmits<{ 'update:modelValue': [value: HostPick] }>()
const { t, te } = useI18n()

const hosts = ref<Hypervisor[]>([])
const hostsLoading = ref(false)
const hostsError = ref('')

interface DiskList {
    loading: boolean
    error: string
    disks: HostDisk[]
    scanning: boolean
}
const disks = ref<Record<string, DiskList>>({})
const scanTimers: ReturnType<typeof setTimeout>[] = []

const roles = computed(() => props.modelValue.roles)
const chosen = computed(() => props.modelValue.disks)
const update = (patch: Partial<HostPick>) => emit('update:modelValue', { ...props.modelValue, ...patch })

// Hosts a plan refuses: offline (10), being deployed (4) or failed to deploy (5)
const usable = (h: Hypervisor) => ![4, 5, 10].includes(h.status)

const offered = computed(() =>
    hosts.value.filter((h) => !props.exclude.includes(h.uuid) && (!props.only || props.only.includes(h.uuid)))
)
const selected = computed(() => offered.value.filter((h) => roles.value[h.uuid]))
const diskRole = computed(() => props.backend?.disk_role || '')
const diskHosts = computed(() => {
    if (props.mode === 'hosts' || !diskRole.value) return []
    if (props.mode === 'disks') return offered.value
    return selected.value.filter((h) => roles.value[h.uuid].includes(diskRole.value))
})
// Hints are written per kind and role; a kind without one shows none rather than a key
const hintOf = (key: string) => (te(key) ? t(key) : '')

const loadHosts = async () => {
    hostsLoading.value = true
    hostsError.value = ''
    try {
        const response = await hypervisorsApi.fetchHypervisors({ limit: 500 })
        hosts.value = (response.hypers || []).filter((h) => h.hostid >= 0)
    } catch (err) {
        hostsError.value = errorMessage(err, t('messages.error'))
    } finally {
        hostsLoading.value = false
    }
}
loadHosts()

// The suggested roles of a newly selected host, from the backend: its default roles, plus each of the roles one
// host should have while no selected host has it yet (so the first host also administers)
const defaultRoles = (current: Record<string, StorageRole[]> = roles.value): StorageRole[] => {
    const b = props.backend
    if (props.mode === 'hosts') return ['client']
    if (!b) return []
    const has = (r: StorageRole) => Object.values(current).some((rs) => rs.includes(r))
    const wanted = new Set([...b.default_roles, ...b.default_once_roles.filter((r) => !has(r))])
    return b.roles.filter((r) => wanted.has(r))
}

const toggleHost = (h: Hypervisor) => {
    const next = { ...roles.value }
    if (next[h.uuid]) delete next[h.uuid]
    else next[h.uuid] = defaultRoles()
    update({ roles: next })
}

const toggleRole = (uuid: string, role: StorageRole) => {
    const current = roles.value[uuid] || []
    const updated = current.includes(role) ? current.filter((r) => r !== role) : [...current, role]
    update({ roles: { ...roles.value, [uuid]: (props.backend?.roles || []).filter((r) => updated.includes(r)) } })
}

// Another kind has other roles: start the selected hosts over
watch(
    () => props.backend?.kind,
    (kind, old) => {
        if (old === undefined || kind === old) return
        const next: Record<string, StorageRole[]> = {}
        for (const uuid of Object.keys(roles.value)) next[uuid] = defaultRoles(next)
        update({ roles: next, disks: {} })
    }
)

const loadDisks = async (uuid: string) => {
    disks.value = {
        ...disks.value,
        [uuid]: { loading: true, error: '', disks: disks.value[uuid]?.disks || [], scanning: false },
    }
    try {
        const list = await hostStorageApi.disks(uuid)
        disks.value = { ...disks.value, [uuid]: { loading: false, error: '', disks: list, scanning: false } }
    } catch (err) {
        disks.value = {
            ...disks.value,
            [uuid]: { loading: false, error: errorMessage(err, t('messages.error')), disks: [], scanning: false },
        }
    }
}

// Load the disks of a host once it gets the disk role; drop the disks of hosts that lost it
watch(
    diskHosts,
    (list) => {
        for (const h of list) {
            if (!disks.value[h.uuid]) loadDisks(h.uuid)
        }
        const keep = new Set(list.map((h) => h.uuid))
        const kept = Object.fromEntries(Object.entries(chosen.value).filter(([key]) => keep.has(key.split('/')[0])))
        if (Object.keys(kept).length !== Object.keys(chosen.value).length) update({ disks: kept })
    },
    { deep: true }
)

const scan = async (uuid: string) => {
    const entry = disks.value[uuid]
    if (entry) entry.scanning = true
    try {
        await hostStorageApi.scanDisks(uuid)
        // The scan runs on the host and reports back in about ten seconds
        scanTimers.push(setTimeout(() => loadDisks(uuid), 10000))
    } catch (err) {
        if (entry) {
            entry.scanning = false
            entry.error = errorMessage(err, t('messages.error'))
        }
    }
}

onUnmounted(() => scanTimers.forEach(clearTimeout))

const diskKey = (uuid: string, d: HostDisk) => `${uuid}/${d.disk_id}`
const selectable = (d: HostDisk) =>
    props.sharedDisks ? d.state === 'shared' : d.state === 'free' || d.state === 'dirty'
// A shared LUN may hold data the scan can not tell: it can be wiped like a dirty disk, unless the cluster uses it
// already (it holds the file system's data; the backend refuses wiping it)
const lunInUse = (d: HostDisk) => props.sharedDisks && props.usedDisks.includes(d.disk_id)
const wipeable = (d: HostDisk) => d.state === 'dirty' || (props.sharedDisks && d.state === 'shared' && !lunInUse(d))
const toggleDisk = (uuid: string, d: HostDisk) => {
    const key = diskKey(uuid, d)
    const next = { ...chosen.value }
    if (key in next) delete next[key]
    else next[key] = false
    update({ disks: next })
}
const setWipe = (uuid: string, d: HostDisk, wipe: boolean) => {
    update({ disks: { ...chosen.value, [diskKey(uuid, d)]: wipe } })
}

const diskStateVariant = (state: string): StatusVariant => {
    if (state === 'free' || (props.sharedDisks && state === 'shared')) return 'success'
    if (state === 'dirty') return 'warning'
    return 'neutral'
}
const diskStateText = (state: string) => (te(`storage.diskStates.${state}`) ? t(`storage.diskStates.${state}`) : state)
// A scan older than a day is refused by the backend
const staleScan = (list: HostDisk[]) =>
    list.length > 0 && Date.now() - new Date(list[0].scanned_at.replace(' ', 'T')).getTime() > 24 * 3600 * 1000

/** A dirty disk can only be used when it is wiped */
const missingWipe = computed(() =>
    diskHosts.value.some((h) =>
        (disks.value[h.uuid]?.disks || []).some((d) => {
            const key = diskKey(h.uuid, d)
            return key in chosen.value && d.state === 'dirty' && !chosen.value[key]
        })
    )
)

/** The hosts and disks as a request takes them */
const payload = () => ({
    nodes: selected.value.map((h) => ({ hypervisor: h.uuid, roles: roles.value[h.uuid] })),
    disks: Object.entries(chosen.value).map(([key, wipe]) => {
        const slash = key.indexOf('/')
        return { hypervisor: key.slice(0, slash), disk_id: key.slice(slash + 1), wipe }
    }),
})

/** The selected hosts, for summaries */
const selectedHosts = computed(() => selected.value)
const hostByUUID = (uuid: string) => hosts.value.find((h) => h.uuid === uuid)

defineExpose({ missingWipe, payload, selectedHosts, hostByUUID, loadHosts })
</script>

<template>
    <div class="host-picker">
        <div v-if="mode !== 'disks'" class="form-group">
            <label class="form-label">{{ t('storage.cluster.hostsLabel') }}</label>
            <span class="form-hint">{{
                mode === 'hosts' ? t('storage.cluster.importHostsHint') : t('storage.cluster.hostsHint')
            }}</span>
            <div v-if="hostsLoading" class="muted-line">{{ t('storage.cluster.loadingHosts') }}</div>
            <div v-else-if="hostsError" class="text-error">{{ hostsError }}</div>
            <div v-else-if="offered.length === 0" class="muted-line">{{ t('storage.cluster.noHosts') }}</div>
            <div v-else class="host-list">
                <div
                    v-for="h in offered"
                    :key="h.uuid"
                    class="host-row"
                    :class="{ selected: !!roles[h.uuid], disabled: !usable(h) }"
                >
                    <label class="host-name">
                        <input
                            type="checkbox"
                            :checked="!!roles[h.uuid]"
                            :disabled="!usable(h)"
                            @change="toggleHost(h)"
                        />
                        <span>
                            <span class="name">{{ h.hostname }}</span>
                            <span class="sub">{{ h.host_ip || '-' }} · {{ h.status_name }}</span>
                        </span>
                    </label>
                    <div v-if="roles[h.uuid] && mode === 'roles'" class="role-chips">
                        <button
                            v-for="r in backend?.roles || []"
                            :key="r"
                            type="button"
                            class="role-chip"
                            :class="{ active: roles[h.uuid].includes(r) }"
                            :aria-pressed="roles[h.uuid].includes(r)"
                            :title="hintOf(`storage.cluster.roleHints.${r}`)"
                            @click="toggleRole(h.uuid, r)"
                        >
                            {{ roleText(t, te, r) }}
                        </button>
                    </div>
                </div>
            </div>
        </div>

        <div v-if="diskHosts.length" class="form-group">
            <label class="form-label">{{ t('storage.cluster.disksLabel') }}</label>
            <span class="form-hint">{{ t('storage.cluster.disksHint', { role: roleText(t, te, diskRole) }) }}</span>
            <div v-for="h in diskHosts" :key="h.uuid" class="disk-host">
                <div class="disk-host-header">
                    <span class="disk-host-name">{{ h.hostname }}</span>
                    <span class="disk-host-actions">
                        <button
                            type="button"
                            class="btn btn-secondary btn-sm"
                            :disabled="disks[h.uuid]?.scanning"
                            :title="t('storage.cluster.scanHint')"
                            @click="scan(h.uuid)"
                        >
                            <ScanLine :size="14" />
                            {{
                                disks[h.uuid]?.scanning ? t('storage.cluster.scanning') : t('storage.cluster.scanDisks')
                            }}
                        </button>
                        <button
                            type="button"
                            class="btn btn-secondary btn-sm btn-icon"
                            :title="t('actions.refresh')"
                            @click="loadDisks(h.uuid)"
                        >
                            <RefreshCw :size="14" :class="{ spinning: disks[h.uuid]?.loading }" />
                        </button>
                    </span>
                </div>
                <div v-if="disks[h.uuid]?.error" class="text-error">{{ disks[h.uuid].error }}</div>
                <div
                    v-else-if="disks[h.uuid] && !disks[h.uuid].loading && disks[h.uuid].disks.length === 0"
                    class="muted-line"
                >
                    {{ t('storage.cluster.noDisks') }}
                </div>
                <div
                    v-for="d in disks[h.uuid]?.disks || []"
                    :key="d.disk_id"
                    class="disk-row"
                    :class="{ disabled: !selectable(d) }"
                >
                    <label class="disk-main" :title="selectable(d) ? d.disk_id : d.detail || d.disk_id">
                        <input
                            type="checkbox"
                            :checked="diskKey(h.uuid, d) in chosen"
                            :disabled="!selectable(d)"
                            @change="toggleDisk(h.uuid, d)"
                        />
                        <span class="disk-text">
                            <span class="name">{{ d.name }} · {{ formatBytes(d.size_bytes) }}</span>
                            <span class="sub">{{ d.model ? `${d.model} · ` : '' }}{{ d.disk_id }}</span>
                        </span>
                    </label>
                    <span class="disk-media">{{ (d.media || '-').toUpperCase() }}</span>
                    <StatusBadge :variant="diskStateVariant(d.state)" :label="diskStateText(d.state)" />
                    <label
                        v-if="wipeable(d) && diskKey(h.uuid, d) in chosen"
                        class="checkbox-inline wipe"
                        :title="d.detail"
                    >
                        <input
                            type="checkbox"
                            :checked="chosen[diskKey(h.uuid, d)]"
                            @change="setWipe(h.uuid, d, ($event.target as HTMLInputElement).checked)"
                        />
                        {{ t('storage.cluster.wipe') }}
                    </label>
                    <span v-else-if="lunInUse(d) && diskKey(h.uuid, d) in chosen" class="wipe text-secondary">{{
                        t('storage.cluster.lunInUse')
                    }}</span>
                </div>
                <div
                    v-if="disks[h.uuid]?.disks?.length"
                    class="scanned-at"
                    :class="{ stale: staleScan(disks[h.uuid].disks) }"
                >
                    {{ t('storage.cluster.scannedAt', { time: formatDateTime(disks[h.uuid].disks[0].scanned_at) }) }}
                    <span v-if="staleScan(disks[h.uuid].disks)"> · {{ t('storage.cluster.scanStale') }}</span>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.host-picker {
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

.muted-line {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    padding: var(--spacing-2) 0;
}

.host-list {
    margin-top: var(--spacing-2);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    max-height: 280px;
    overflow-y: auto;
}

.host-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-light);
}

.host-row:last-child {
    border-bottom: none;
}

.host-row.selected {
    background: var(--bg-secondary);
}

.host-row.disabled {
    opacity: 0.55;
}

.host-name,
.disk-main {
    display: flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    min-width: 0;
}

.host-name .name,
.disk-text .name {
    display: block;
    font-size: var(--font-size-sm);
    font-weight: 500;
}

.host-name .sub,
.disk-text .sub {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.role-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    justify-content: flex-end;
}

.role-chip {
    padding: 2px 10px;
    font-size: var(--font-size-xs);
    border: 1px solid var(--border-default);
    border-radius: 999px;
    background: var(--bg-primary);
    color: var(--text-secondary);
    cursor: pointer;
}

.role-chip.active {
    border-color: var(--primary-color);
    background: var(--primary-light);
    color: var(--primary-color);
    font-weight: 500;
}

.disk-host {
    margin-top: var(--spacing-2);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    padding: 8px 12px;
}

.disk-host-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 6px;
}

.disk-host-name {
    font-weight: 600;
    font-size: var(--font-size-sm);
}

.disk-host-actions {
    display: flex;
    gap: 6px;
}

.disk-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 56px auto auto;
    align-items: center;
    gap: var(--spacing-3);
    padding: 6px 0;
    border-top: 1px solid var(--border-light);
}

.disk-row.disabled {
    opacity: 0.6;
}

.disk-text {
    min-width: 0;
}

.disk-media {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.wipe {
    font-size: var(--font-size-xs);
}

.scanned-at {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    padding-top: 6px;
}

.scanned-at.stale {
    color: var(--warning-dark);
}
</style>
