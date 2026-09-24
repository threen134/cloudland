<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
    ArrowLeft,
    ShieldCheck,
    Network,
    Laptop,
    RefreshCw,
    Trash2,
    Plus,
    ChevronDown,
    Pencil,
    Check,
    Copy,
    Power,
    FileText,
    Activity as ActivityIcon,
} from 'lucide-vue-next'
import {
    vpnGatewaysApi,
    vpnConnectionsApi,
    vpnClientsApi,
    type VpnGateway,
    type VpnConnection,
    type VpnClient,
    type VpnGatewayPatchPayload,
} from '../../api/vpn'
import { activitiesApi, type Activity } from '../../api/activities'
import { useToast } from '../../composables/useToast'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { formatBytes } from '../../utils/format'
import { isValidName, isValidCIDRv4 } from '../../utils/validation'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import DetailTabs from '../../components/base/DetailTabs.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import ActivityEntry from '../../components/activity/ActivityEntry.vue'
import VpnConnectionModal from '../../components/vpn/VpnConnectionModal.vue'
import VpnClientModal from '../../components/vpn/VpnClientModal.vue'
import VpnClientConfigBox from '../../components/vpn/VpnClientConfigBox.vue'

const { t, te } = useI18n()
const toast = useToast()
const route = useRoute()
const router = useRouter()
const { copiedId, copyId } = useCopyId()
const goBack = useGoBack('vpn-gateways')
const gatewayId = route.params.id as string

const gateway = ref<VpnGateway | null>(null)
const loading = ref(true)
const error = ref('')

type TabId = 'overview' | 'connections' | 'clients' | 'activity'
const activeTab = ref<TabId>('overview')

const connections = computed(() => gateway.value?.connections || [])
const clients = computed(() => gateway.value?.clients || [])

const tabs = computed(() => [
    { id: 'overview', label: t('dashboard.vpnGateway.overview'), icon: ShieldCheck },
    {
        id: 'connections',
        label: t('dashboard.vpnGateway.siteConnections'),
        icon: Network,
        count: connections.value.length,
    },
    { id: 'clients', label: t('dashboard.vpnGateway.clientsTab'), icon: Laptop, count: clients.value.length },
    { id: 'activity', label: t('dashboard.vpnGateway.activity'), icon: ActivityIcon },
])

// ─── Formatting ──────────────────────────────────────────────────────────────
const gatewayStatusText = (status?: string) =>
    status && te(`dashboard.vpnGatewayStatus.${status}`) ? t(`dashboard.vpnGatewayStatus.${status}`) : status || '-'
// A paused gateway that is otherwise available shows as disabled; building, error and deleting win
const gatewayBadge = computed(() => {
    const g = gateway.value
    if (g && g.enabled === false && g.status === 'available') {
        return { status: 'disabled', label: t('dashboard.vpnGateway.disabled') }
    }
    return { status: g?.status || '', label: gatewayStatusText(g?.status) }
})
const connectionStatusText = (status?: string) =>
    status && te(`dashboard.vpnConnectionStatus.${status}`)
        ? t(`dashboard.vpnConnectionStatus.${status}`)
        : status || '-'
const routeModeText = (mode?: string) =>
    mode === 'bgp' ? t('dashboard.vpnGateway.routeModeBgp') : t('dashboard.vpnGateway.routeModeStatic')
const prefixSourceText = (source: string) =>
    te(`dashboard.vpnGateway.prefixSources.${source}`) ? t(`dashboard.vpnGateway.prefixSources.${source}`) : source
// clapi timestamps are "YYYY-MM-DD HH:mm:ss.ffffff": drop the fraction
const fmtTime = (value?: string) => (value ? value.replace(/\.\d+$/, '') : '-')
// Counters start at 0: show it as such rather than the '-' placeholder of formatBytes
const traffic = (bytes?: number) => (bytes ? formatBytes(bytes) : '0 B')
const shortKey = (key?: string) => (key && key.length > 16 ? `${key.slice(0, 8)}…${key.slice(-6)}` : key || '-')
const yesNo = (value: boolean) => (value ? t('messages.yes') : t('messages.no'))
const splitList = (value?: string) =>
    (value || '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)

// ─── Loading and polling ─────────────────────────────────────────────────────
// silent: background refresh that keeps the current data on failure
const fetchGateway = async (silent = false) => {
    if (!silent) {
        loading.value = true
        error.value = ''
    }
    try {
        gateway.value = await vpnGatewaysApi.get(gatewayId)
    } catch (err) {
        if (!silent) error.value = errorMessage(err, t('dashboard.vpnGateway.loadError'))
        else console.warn('VPN gateway refresh failed:', err)
    } finally {
        if (!silent) loading.value = false
    }
}

// Every 5 s while the gateway is being set up or torn down, every 10 s on the connections tab
// (tunnel state and BGP reports come from heartbeats), otherwise no polling
let pollTimer: ReturnType<typeof setInterval> | null = null
const stopPolling = () => {
    if (pollTimer) clearInterval(pollTimer)
    pollTimer = null
}
const schedulePolling = () => {
    stopPolling()
    const status = gateway.value?.status
    const interval =
        status === 'pending' || status === 'deleting' ? 5000 : activeTab.value === 'connections' ? 10000 : 0
    if (interval) pollTimer = setInterval(() => fetchGateway(true), interval)
}
watch([() => gateway.value?.status, activeTab], schedulePolling)
onUnmounted(stopPolling)

// ─── Title actions ───────────────────────────────────────────────────────────
const showActionMenu = ref(false)
const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}
const closeActionMenu = () => {
    showActionMenu.value = false
}

// Shared delete confirm modal (gateway, connection, client)
const deleteModal = ref<{
    visible: boolean
    name: string
    message: string
    loading: boolean
    error: string
    onConfirm: () => Promise<void>
}>({ visible: false, name: '', message: '', loading: false, error: '', onConfirm: async () => {} })

const openDeleteModal = (name: string, message: string, onConfirm: () => Promise<void>) => {
    deleteModal.value = { visible: true, name, message, loading: false, error: '', onConfirm }
}
const closeDeleteModal = () => {
    deleteModal.value.visible = false
}
const confirmDelete = async () => {
    deleteModal.value.loading = true
    deleteModal.value.error = ''
    try {
        await deleteModal.value.onConfirm()
        closeDeleteModal()
    } catch (err) {
        deleteModal.value.error = errorMessage(err, t('messages.error'))
    } finally {
        deleteModal.value.loading = false
    }
}

// Pause / resume the whole gateway. Disabling asks first (every tunnel and client drops), enabling does not
const togglingEnabled = ref(false)
const showDisableModal = ref(false)
const disableError = ref('')
const setGatewayEnabled = async (enabled: boolean) => {
    togglingEnabled.value = true
    try {
        await vpnGatewaysApi.update(gatewayId, { enabled })
        toast.success(t(enabled ? 'dashboard.vpnGateway.enabledSuccess' : 'dashboard.vpnGateway.disabledSuccess'))
        await fetchGateway(true)
    } finally {
        togglingEnabled.value = false
    }
}
const openDisableModal = () => {
    closeActionMenu()
    disableError.value = ''
    showDisableModal.value = true
}
const confirmDisableGateway = async () => {
    disableError.value = ''
    try {
        await setGatewayEnabled(false)
        showDisableModal.value = false
    } catch (err) {
        disableError.value = errorMessage(err, t('messages.error'))
    }
}
const handleEnableGateway = async () => {
    closeActionMenu()
    try {
        await setGatewayEnabled(true)
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    }
}

const handleDeleteGateway = () => {
    closeActionMenu()
    openDeleteModal(gateway.value?.name || gatewayId, t('dashboard.vpnGateway.deleteWarning'), async () => {
        await vpnGatewaysApi.delete(gatewayId)
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'vpn-gateways' })
    })
}

// ─── Edit gateway ────────────────────────────────────────────────────────────
const showEditModal = ref(false)
const editing = ref(false)
const editError = ref('')
const editForm = ref({
    name: '',
    description: '',
    ipsec_enabled: true,
    client_enabled: false,
    client_cidr: '',
    client_port: null as number | null,
    client_dns: '',
    client_routes: '',
})
const isEditNameValid = computed(() => isValidName(editForm.value.name))
const IPV4 = /^(\d{1,3}\.){3}\d{1,3}$/

const openEditModal = () => {
    const g = gateway.value
    if (!g) return
    editForm.value = {
        name: g.name,
        description: g.description || '',
        ipsec_enabled: g.ipsec_enabled,
        client_enabled: g.client_enabled,
        client_cidr: g.client_cidr || '10.8.0.0/24',
        client_port: g.client_port || 51820,
        client_dns: g.client_dns || '',
        client_routes: g.client_routes || '',
    }
    editError.value = ''
    showEditModal.value = true
    closeActionMenu()
}

const handleEdit = async () => {
    const g = gateway.value
    if (!g) return
    const f = editForm.value
    if (!f.name || !isEditNameValid.value) {
        editError.value = t('messages.invalidHostname')
        return
    }
    // The same rules clapi applies, checked here so the message is translated
    if (!f.ipsec_enabled && !f.client_enabled) {
        editError.value = t('dashboard.vpnGateway.needOneAccess')
        return
    }
    if (!f.ipsec_enabled && g.ipsec_enabled && connections.value.length > 0) {
        editError.value = t('dashboard.vpnGateway.ipsecHasConnections')
        return
    }
    if (!f.client_enabled && g.client_enabled && clients.value.length > 0) {
        editError.value = t('dashboard.vpnGateway.clientHasClients')
        return
    }
    const port = (f.client_port as unknown) === '' ? null : f.client_port
    if (f.client_enabled) {
        if (!isValidCIDRv4(f.client_cidr)) {
            editError.value = t('dashboard.vpnGateway.invalidClientCidr')
            return
        }
        if (port !== null && (!Number.isInteger(port) || port < 1 || port > 65535)) {
            editError.value = t('messages.invalidPort')
            return
        }
        if (f.client_dns && !splitList(f.client_dns).every((d) => IPV4.test(d))) {
            editError.value = t('dashboard.vpnGateway.invalidDns')
            return
        }
    }
    // Only the changed fields go into the PATCH
    const payload: VpnGatewayPatchPayload = {}
    if (f.name !== g.name) payload.name = f.name
    if (f.description !== (g.description || '')) payload.description = f.description
    if (f.ipsec_enabled !== g.ipsec_enabled) payload.ipsec_enabled = f.ipsec_enabled
    if (f.client_enabled !== g.client_enabled) payload.client_enabled = f.client_enabled
    if (f.client_enabled) {
        if (f.client_cidr !== g.client_cidr) payload.client_cidr = f.client_cidr
        if (port !== null && port !== g.client_port) payload.client_port = port
        if (f.client_dns !== (g.client_dns || '')) payload.client_dns = f.client_dns
        if (f.client_routes !== (g.client_routes || '')) payload.client_routes = f.client_routes
    }
    editing.value = true
    editError.value = ''
    try {
        if (Object.keys(payload).length > 0) {
            await vpnGatewaysApi.update(gatewayId, payload)
            toast.success(t('messages.success'))
            await fetchGateway(true)
        }
        showEditModal.value = false
    } catch (err) {
        editError.value = errorMessage(err, t('messages.error'))
    } finally {
        editing.value = false
    }
}

// ─── Site-to-site connections ────────────────────────────────────────────────
const connectionColumns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.name') },
    { key: 'route_mode', label: t('dashboard.vpnGateway.routeMode') },
    { key: 'remote_gateway', label: t('dashboard.vpnGateway.remoteGateway') },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'remote', label: t('dashboard.vpnGateway.remoteNetworks'), hideBelow: 1280 },
    { key: 'traffic', label: t('dashboard.vpnGateway.traffic'), hideBelow: 1024 },
    { key: 'established_at', label: t('dashboard.vpnGateway.establishedAt'), hideBelow: 1280 },
    { key: 'bgp', label: t('dashboard.vpnGateway.bgpState'), hideBelow: 1024 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const showConnectionModal = ref(false)
const connectionToEdit = ref<VpnConnection | null>(null)
const restartingId = ref('')

const openAddConnection = () => {
    connectionToEdit.value = null
    showConnectionModal.value = true
}
const openEditConnection = (conn: VpnConnection) => {
    connectionToEdit.value = conn
    showConnectionModal.value = true
}
const onConnectionSaved = async () => {
    toast.success(t('messages.success'))
    await fetchGateway(true)
}

const handleRestartConnection = async (conn: VpnConnection) => {
    restartingId.value = conn.id
    try {
        await vpnConnectionsApi.restart(gatewayId, conn.id)
        toast.success(t('dashboard.vpnGateway.restartRequested'))
        await fetchGateway(true)
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        restartingId.value = ''
    }
}

const handleDeleteConnection = (conn: VpnConnection) => {
    openDeleteModal(conn.name, t('dashboard.deleteConfirm.message'), async () => {
        await vpnConnectionsApi.delete(gatewayId, conn.id)
        toast.success(t('messages.deleteSuccess'))
        await fetchGateway(true)
    })
}

// Local networks of a BGP connection that the gateway actually advertises to the peer;
// a subnet without any instance is not advertised yet
const isAdvertised = (conn: VpnConnection, cidr: string) => (conn.bgp?.advertised || []).includes(cidr)

// ─── WireGuard clients ───────────────────────────────────────────────────────
const clientColumns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.name') },
    { key: 'ip_address', label: t('dashboard.vpnGateway.clientAddress') },
    { key: 'public_key', label: t('dashboard.vpnGateway.publicKey'), hideBelow: 1024 },
    { key: 'enabled', label: t('dashboard.table.status') },
    { key: 'last_handshake_at', label: t('dashboard.vpnGateway.lastHandshake'), hideBelow: 1280 },
    { key: 'traffic', label: t('dashboard.vpnGateway.traffic'), hideBelow: 1024 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const showClientModal = ref(false)
const togglingClientId = ref('')

const onClientCreated = async () => {
    await fetchGateway(true)
}

const handleToggleClient = async (client: VpnClient) => {
    togglingClientId.value = client.id
    try {
        await vpnClientsApi.update(gatewayId, client.id, { enabled: !client.enabled })
        toast.success(t('messages.success'))
        await fetchGateway(true)
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        togglingClientId.value = ''
    }
}

const handleDeleteClient = (client: VpnClient) => {
    openDeleteModal(client.name, t('dashboard.deleteConfirm.message'), async () => {
        await vpnClientsApi.delete(gatewayId, client.id)
        toast.success(t('messages.deleteSuccess'))
        await fetchGateway(true)
    })
}

// Configuration template of an existing client (never contains a private key)
const showConfigModal = ref(false)
const configClient = ref<VpnClient | null>(null)
const configText = ref('')
const configLoading = ref(false)
const configError = ref('')

const openClientConfig = async (client: VpnClient) => {
    configClient.value = client
    configText.value = ''
    configError.value = ''
    showConfigModal.value = true
    configLoading.value = true
    try {
        const res = await vpnClientsApi.config(gatewayId, client.id)
        configText.value = res.config
    } catch (err) {
        configError.value = errorMessage(err, t('messages.error'))
    } finally {
        configLoading.value = false
    }
}

// ─── Activity ────────────────────────────────────────────────────────────────
const ACTIVITY_LIMIT = 50
const DAY_MS = 24 * 60 * 60 * 1000
const activities = ref<Activity[]>([])
const activitiesLoading = ref(false)
const activitiesLoadingMore = ref(false)
const activitiesError = ref('')
const activitiesCursor = ref('')
const activitiesLoaded = ref(false)

// The last 90 days (the maximum span the API accepts) of operations on this gateway
const activityRange = () => {
    const now = new Date()
    return { start: new Date(now.getTime() - 90 * DAY_MS).toISOString(), end: now.toISOString() }
}

const loadActivities = async () => {
    activitiesLoading.value = true
    activitiesError.value = ''
    try {
        const res = await activitiesApi.list({ ...activityRange(), resource_uuid: gatewayId, limit: ACTIVITY_LIMIT })
        activities.value = res.activities || []
        activitiesCursor.value = res.next_cursor || ''
        activitiesLoaded.value = true
    } catch (err) {
        activitiesError.value = errorMessage(err, t('dashboard.overview.activityLoadFailed'))
    } finally {
        activitiesLoading.value = false
    }
}

const loadMoreActivities = async () => {
    if (!activitiesCursor.value || activitiesLoadingMore.value) return
    activitiesLoadingMore.value = true
    try {
        const res = await activitiesApi.list({
            ...activityRange(),
            resource_uuid: gatewayId,
            limit: ACTIVITY_LIMIT,
            cursor: activitiesCursor.value,
        })
        activities.value.push(...(res.activities || []))
        activitiesCursor.value = res.next_cursor || ''
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        activitiesLoadingMore.value = false
    }
}

// Loaded on first visit of the tab, not with the gateway
watch(activeTab, (tab) => {
    if (tab === 'activity' && !activitiesLoaded.value) loadActivities()
})

onMounted(() => {
    fetchGateway()
})
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <div v-if="loading" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error" class="error-container card">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary" @click="fetchGateway()">{{ $t('actions.retry') }}</button>
        </div>

        <div v-else-if="gateway" class="detail-content">
            <!-- Title bar (global .title-bar styles) -->
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon">
                        <ShieldCheck :size="20" />
                    </div>
                    <div>
                        <h2 class="resource-title">
                            {{ gateway.name }}
                            <StatusBadge :status="gatewayBadge.status" :label="gatewayBadge.label" />
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
                        @click="fetchGateway(true)"
                    >
                        <RefreshCw :size="14" />
                    </button>
                    <div class="action-dropdown">
                        <button class="btn btn-secondary btn-sm" @click="toggleActionMenu">
                            {{ $t('actions.actions') }} <ChevronDown :size="14" />
                        </button>
                        <Transition name="dropdown">
                            <div v-if="showActionMenu" class="dropdown-menu" @click="closeActionMenu">
                                <button class="dropdown-item" @click="openEditModal">
                                    <Pencil :size="14" /> {{ $t('actions.edit') }}
                                </button>
                                <button
                                    class="dropdown-item"
                                    :disabled="gateway.status === 'deleting' || togglingEnabled"
                                    @click="gateway.enabled === false ? handleEnableGateway() : openDisableModal()"
                                >
                                    <Power :size="14" />
                                    {{
                                        gateway.enabled === false
                                            ? $t('dashboard.vpnGateway.enableGateway')
                                            : $t('dashboard.vpnGateway.disableGateway')
                                    }}
                                </button>
                                <div class="dropdown-divider"></div>
                                <button
                                    class="dropdown-item dropdown-item-danger"
                                    :disabled="gateway.status === 'deleting'"
                                    @click="handleDeleteGateway"
                                >
                                    <Trash2 :size="14" /> {{ $t('actions.delete') }}
                                </button>
                            </div>
                        </Transition>
                        <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
                    </div>
                </div>
            </div>

            <div v-if="gateway.enabled === false" class="disabled-banner">
                <Power :size="16" />
                <span>{{ $t('dashboard.vpnGateway.disabledNotice') }}</span>
                <button class="btn btn-secondary btn-sm" :disabled="togglingEnabled" @click="handleEnableGateway">
                    {{ $t('dashboard.vpnGateway.enableGateway') }}
                </button>
            </div>

            <DetailTabs v-model="activeTab" :tabs="tabs" />

            <!-- ── Overview ── -->
            <div v-if="activeTab === 'overview'">
                <div class="info-grid">
                    <div class="card info-card">
                        <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                        <div class="key-value-list">
                            <InfoRow :label="$t('dashboard.table.name')">{{ gateway.name }}</InfoRow>
                            <InfoRow v-if="gateway.description" :label="$t('dashboard.table.description')">{{
                                gateway.description
                            }}</InfoRow>
                            <InfoRow :label="$t('dashboard.table.status')">
                                <StatusBadge :status="gatewayBadge.status" :label="gatewayBadge.label" />
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.table.vpc')">
                                <router-link
                                    v-if="gateway.vpc"
                                    :to="{ name: 'vpc-detail', params: { id: gateway.vpc.id } }"
                                    class="text-link"
                                >
                                    {{ gateway.vpc.name }}
                                </router-link>
                                <span v-else>-</span>
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.vpnGateway.publicIp')" mono>
                                <span>{{ gateway.public_ip || '-' }}</span>
                                <button
                                    v-if="gateway.public_ip"
                                    class="copy-btn"
                                    :title="$t('actions.copy')"
                                    :aria-label="$t('actions.copy')"
                                    @click="copyId(gateway.public_ip, 'ip')"
                                >
                                    <Check v-if="copiedId === 'ip'" :size="12" class="copied-icon" />
                                    <Copy v-else :size="12" />
                                </button>
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.table.zone')">{{ gateway.zone || '-' }}</InfoRow>
                            <InfoRow :label="$t('dashboard.table.createdAt')">{{
                                fmtTime(gateway.created_at)
                            }}</InfoRow>
                        </div>
                    </div>

                    <div class="card info-card">
                        <h3>{{ $t('dashboard.vpnGateway.haNodes') }}</h3>
                        <div class="key-value-list">
                            <InfoRow :label="$t('dashboard.vpnGateway.masterNode')">
                                <span>{{ gateway.master_hostname || '-' }}</span>
                                <span v-if="gateway.master_reported_at" class="text-tertiary text-xs">
                                    ({{ $t('dashboard.vpnGateway.reportedAt') }}
                                    {{ fmtTime(gateway.master_reported_at) }})
                                </span>
                            </InfoRow>
                            <InfoRow
                                v-for="node in gateway.nodes || []"
                                :key="node.hostid"
                                :label="
                                    node.role === 'MASTER'
                                        ? $t('dashboard.vpnGateway.roleMaster')
                                        : $t('dashboard.vpnGateway.roleBackup')
                                "
                            >
                                <span>{{ node.hostname || `#${node.hostid}` }}</span>
                                <span v-if="node.master" class="badge badge-success">{{
                                    $t('dashboard.vpnGateway.activeNode')
                                }}</span>
                            </InfoRow>
                            <div v-if="!gateway.nodes?.length" class="empty-hint">
                                {{ $t('dashboard.vpnGateway.noNodes') }}
                            </div>
                        </div>
                    </div>

                    <div class="card info-card">
                        <h3>{{ $t('dashboard.vpnGateway.siteToSite') }}</h3>
                        <div class="key-value-list">
                            <InfoRow :label="$t('dashboard.vpnGateway.ipsecEnabled')">
                                <span
                                    class="badge"
                                    :class="gateway.ipsec_enabled ? 'badge-success' : 'badge-secondary'"
                                    >{{
                                        gateway.ipsec_enabled
                                            ? $t('dashboard.vpnGateway.enabledState')
                                            : $t('dashboard.vpnGateway.disabledState')
                                    }}</span
                                >
                            </InfoRow>
                            <InfoRow :label="$t('dashboard.vpnGateway.connections')">
                                <button type="button" class="link-btn" @click="activeTab = 'connections'">
                                    {{ connections.length }}
                                </button>
                            </InfoRow>
                        </div>
                    </div>

                    <div class="card info-card">
                        <h3>{{ $t('dashboard.vpnGateway.clientVpn') }}</h3>
                        <div class="key-value-list">
                            <InfoRow :label="$t('dashboard.vpnGateway.clientEnabled')">
                                <span
                                    class="badge"
                                    :class="gateway.client_enabled ? 'badge-success' : 'badge-secondary'"
                                    >{{
                                        gateway.client_enabled
                                            ? $t('dashboard.vpnGateway.enabledState')
                                            : $t('dashboard.vpnGateway.disabledState')
                                    }}</span
                                >
                            </InfoRow>
                            <template v-if="gateway.client_enabled">
                                <InfoRow :label="$t('dashboard.vpnGateway.clientCidr')" mono>{{
                                    gateway.client_cidr || '-'
                                }}</InfoRow>
                                <InfoRow :label="$t('dashboard.vpnGateway.clientPort')" mono>
                                    {{ gateway.client_protocol || 'wireguard' }}/{{ gateway.client_port || '-' }}
                                </InfoRow>
                                <InfoRow :label="$t('dashboard.vpnGateway.gatewayPublicKey')" mono>
                                    <span class="key-text" :title="gateway.client_public_key">{{
                                        shortKey(gateway.client_public_key)
                                    }}</span>
                                    <button
                                        v-if="gateway.client_public_key"
                                        class="copy-btn"
                                        :title="$t('actions.copy')"
                                        :aria-label="$t('actions.copy')"
                                        @click="copyId(gateway.client_public_key, 'pubkey')"
                                    >
                                        <Check v-if="copiedId === 'pubkey'" :size="12" class="copied-icon" />
                                        <Copy v-else :size="12" />
                                    </button>
                                </InfoRow>
                                <InfoRow :label="$t('dashboard.vpnGateway.clientDns')" mono>{{
                                    gateway.client_dns || '-'
                                }}</InfoRow>
                                <InfoRow :label="$t('dashboard.vpnGateway.clientRoutes')">
                                    <span v-if="gateway.client_routes" class="mono">{{ gateway.client_routes }}</span>
                                    <span v-else class="text-secondary">{{
                                        $t('dashboard.vpnGateway.allInternalSubnets')
                                    }}</span>
                                </InfoRow>
                                <InfoRow :label="$t('dashboard.vpnGateway.effectiveClientRoutes')">
                                    <div v-if="gateway.effective_client_routes?.length" class="cidr-list">
                                        <code
                                            v-for="cidr in gateway.effective_client_routes"
                                            :key="cidr"
                                            class="cidr-chip"
                                            >{{ cidr }}</code
                                        >
                                    </div>
                                    <span v-else>-</span>
                                </InfoRow>
                                <InfoRow :label="$t('dashboard.vpnGateway.clients')">
                                    <button type="button" class="link-btn" @click="activeTab = 'clients'">
                                        {{ clients.length }}
                                    </button>
                                </InfoRow>
                            </template>
                            <div v-else class="empty-hint">{{ $t('dashboard.vpnGateway.clientDisabledHint') }}</div>
                        </div>
                    </div>
                </div>

                <!-- Routed prefixes -->
                <div class="card table-card">
                    <div class="card-section-header">
                        <h3>{{ $t('dashboard.vpnGateway.routedPrefixes') }}</h3>
                    </div>
                    <div class="table-responsive">
                        <table class="data-table">
                            <thead>
                                <tr>
                                    <th>{{ $t('dashboard.vpnGateway.prefix') }}</th>
                                    <th>{{ $t('dashboard.vpnGateway.source') }}</th>
                                </tr>
                            </thead>
                            <tbody>
                                <tr v-if="!gateway.remote_prefixes?.length">
                                    <td colspan="2" class="text-center text-secondary" style="padding: 32px">
                                        {{ $t('dashboard.vpnGateway.noPrefixes') }}
                                    </td>
                                </tr>
                                <tr
                                    v-for="p in gateway.remote_prefixes || []"
                                    :key="`${p.source}-${p.ref_id}-${p.cidr}`"
                                >
                                    <td>
                                        <code class="mono">{{ p.cidr }}</code>
                                    </td>
                                    <td>{{ prefixSourceText(p.source) }}</td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>

            <!-- ── Site connections ── -->
            <div v-else-if="activeTab === 'connections'">
                <div class="tab-toolbar">
                    <span v-if="!gateway.ipsec_enabled" class="text-secondary text-sm">
                        {{ $t('dashboard.vpnGateway.ipsecDisabledHint') }}
                    </span>
                    <span v-else></span>
                    <button
                        class="btn btn-primary btn-sm"
                        :disabled="!gateway.ipsec_enabled"
                        @click="openAddConnection"
                    >
                        <Plus :size="14" /> {{ $t('dashboard.vpnGateway.addConnection') }}
                    </button>
                </div>

                <DataTable :columns="connectionColumns" :rows="connections" row-key="id" expandable>
                    <template #empty>
                        <div>
                            <Network :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                            <p class="text-secondary">{{ $t('dashboard.vpnGateway.noConnections') }}</p>
                        </div>
                    </template>

                    <template #cell-name="{ row: conn }">
                        <div class="cell-name">{{ conn.name }}</div>
                        <div v-if="conn.description" class="cell-desc">{{ conn.description }}</div>
                    </template>
                    <template #cell-route_mode="{ row: conn }">
                        <span class="badge badge-secondary">{{ routeModeText(conn.route_mode) }}</span>
                    </template>
                    <template #cell-remote_gateway="{ row: conn }">
                        <code v-if="conn.remote_gateway" class="mono">{{ conn.remote_gateway }}</code>
                        <span v-else class="text-tertiary">{{ $t('dashboard.vpnGateway.responderOnly') }}</span>
                    </template>
                    <template #cell-status="{ row: conn }">
                        <StatusBadge :status="conn.status" :label="connectionStatusText(conn.status)" />
                    </template>
                    <template #cell-remote="{ row: conn }">
                        <span class="mono text-sm">{{
                            (conn.route_mode === 'bgp' ? conn.remote_summary_cidrs : conn.remote_cidrs) || '-'
                        }}</span>
                    </template>
                    <template #cell-traffic="{ row: conn }">
                        <span class="text-sm">↓ {{ traffic(conn.bytes_in) }} · ↑ {{ traffic(conn.bytes_out) }}</span>
                    </template>
                    <template #cell-established_at="{ row: conn }">
                        <span class="cell-time">{{ fmtTime(conn.established_at) }}</span>
                    </template>
                    <template #cell-bgp="{ row: conn }">
                        <template v-if="conn.route_mode === 'bgp'">
                            <span v-if="conn.bgp" class="text-sm">{{ conn.bgp.state || '-' }}</span>
                            <span v-else class="text-tertiary text-sm">{{
                                $t('dashboard.vpnGateway.noBgpReport')
                            }}</span>
                        </template>
                        <span v-else class="text-tertiary">-</span>
                    </template>
                    <template #cell-actions="{ row: conn }">
                        <div class="row-actions" @click.stop>
                            <button
                                class="icon-btn-table"
                                :title="
                                    gateway.enabled === false
                                        ? $t('dashboard.vpnGateway.restartDisabled')
                                        : $t('actions.restart')
                                "
                                :disabled="restartingId === conn.id || gateway.enabled === false"
                                @click="handleRestartConnection(conn)"
                            >
                                <RefreshCw :size="16" :class="{ spinning: restartingId === conn.id }" />
                            </button>
                            <button
                                class="icon-btn-table"
                                :title="$t('actions.edit')"
                                @click="openEditConnection(conn)"
                            >
                                <Pencil :size="16" />
                            </button>
                            <button
                                class="icon-btn-table icon-danger"
                                :title="$t('actions.delete')"
                                @click="handleDeleteConnection(conn)"
                            >
                                <Trash2 :size="16" />
                            </button>
                        </div>
                    </template>

                    <!-- Expanded row: effective networks, BGP report, IKE parameters -->
                    <template #expanded="{ row: conn }">
                        <div class="conn-panel">
                            <div v-if="conn.last_error" class="conn-error">
                                <strong>{{ $t('dashboard.vpnGateway.lastError') }}:</strong> {{ conn.last_error }}
                            </div>

                            <div class="conn-grid">
                                <div class="conn-section">
                                    <h4>{{ $t('dashboard.vpnGateway.effectiveLocalCidrs') }}</h4>
                                    <div v-if="conn.effective_local_cidrs?.length" class="cidr-list">
                                        <span v-for="cidr in conn.effective_local_cidrs" :key="cidr" class="cidr-row">
                                            <code class="cidr-chip">{{ cidr }}</code>
                                            <span
                                                v-if="conn.route_mode === 'bgp' && conn.bgp"
                                                class="badge"
                                                :class="isAdvertised(conn, cidr) ? 'badge-success' : 'badge-secondary'"
                                                :title="
                                                    isAdvertised(conn, cidr)
                                                        ? ''
                                                        : $t('dashboard.vpnGateway.notAdvertisedHint')
                                                "
                                                >{{
                                                    isAdvertised(conn, cidr)
                                                        ? $t('dashboard.vpnGateway.advertised')
                                                        : $t('dashboard.vpnGateway.notAdvertised')
                                                }}</span
                                            >
                                        </span>
                                    </div>
                                    <span v-else class="text-tertiary text-sm">-</span>
                                    <p v-if="conn.route_mode === 'bgp' && conn.bgp" class="section-hint">
                                        {{ $t('dashboard.vpnGateway.notAdvertisedHint') }}
                                    </p>
                                    <h4 class="mt">
                                        {{
                                            conn.route_mode === 'bgp'
                                                ? $t('dashboard.vpnGateway.remoteSummaryCidrs')
                                                : $t('dashboard.vpnGateway.remoteCidrs')
                                        }}
                                    </h4>
                                    <div class="cidr-list">
                                        <code
                                            v-for="cidr in splitList(
                                                conn.route_mode === 'bgp'
                                                    ? conn.remote_summary_cidrs
                                                    : conn.remote_cidrs
                                            )"
                                            :key="cidr"
                                            class="cidr-chip"
                                            >{{ cidr }}</code
                                        >
                                    </div>
                                </div>

                                <div v-if="conn.route_mode === 'bgp'" class="conn-section">
                                    <h4>
                                        {{ $t('dashboard.vpnGateway.bgpReport') }}
                                        <span v-if="conn.bgp_reported_at" class="text-tertiary text-xs">
                                            ({{ $t('dashboard.vpnGateway.reportedAt') }}
                                            {{ fmtTime(conn.bgp_reported_at) }})
                                        </span>
                                    </h4>
                                    <div v-if="conn.bgp" class="kv">
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.bgpState') }}</span>
                                            <span>{{ conn.bgp.state || '-' }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.bgpUptime') }}</span>
                                            <span>{{ conn.bgp.uptime || '-' }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.bgpSession') }}</span>
                                            <span class="mono text-sm"
                                                >AS{{ conn.local_asn }} {{ conn.tunnel_local_ip }} ↔ AS{{
                                                    conn.peer_asn
                                                }}
                                                {{ conn.tunnel_peer_ip }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{
                                                $t('dashboard.vpnGateway.prefixesReceived')
                                            }}</span>
                                            <span
                                                >{{ conn.bgp.prefixes_received }} / {{ conn.max_prefixes || '-' }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.prefixesSent') }}</span>
                                            <span>{{ conn.bgp.prefixes_sent }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.accepted') }}</span>
                                            <div v-if="conn.bgp.accepted?.length" class="cidr-list">
                                                <code v-for="cidr in conn.bgp.accepted" :key="cidr" class="cidr-chip">{{
                                                    cidr
                                                }}</code>
                                            </div>
                                            <span v-else>-</span>
                                        </div>
                                        <div v-if="conn.bgp.rejected?.length" class="rejected-box">
                                            <div class="rejected-title">
                                                {{ $t('dashboard.vpnGateway.rejected') }} ({{
                                                    conn.bgp.rejected.length
                                                }})
                                            </div>
                                            <div class="cidr-list">
                                                <code
                                                    v-for="cidr in conn.bgp.rejected"
                                                    :key="cidr"
                                                    class="cidr-chip chip-error"
                                                    >{{ cidr }}</code
                                                >
                                            </div>
                                            <p class="rejected-hint">{{ $t('dashboard.vpnGateway.rejectedHint') }}</p>
                                        </div>
                                        <p v-if="conn.bgp.truncated" class="section-hint">
                                            {{ $t('dashboard.vpnGateway.bgpTruncated') }}
                                        </p>
                                    </div>
                                    <span v-else class="text-tertiary text-sm">{{
                                        $t('dashboard.vpnGateway.noBgpReport')
                                    }}</span>
                                </div>

                                <div class="conn-section">
                                    <h4>{{ $t('dashboard.vpnGateway.ikeDetails') }}</h4>
                                    <div class="kv">
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.ikeProposal') }}</span>
                                            <span class="mono text-sm"
                                                >{{
                                                    $t('dashboard.vpnGateway.ikeVersion', {
                                                        version: conn.ike_version || 2,
                                                    })
                                                }}
                                                {{ conn.ike_proposal || '-' }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.espProposal') }}</span>
                                            <span class="mono text-sm">{{ conn.esp_proposal || '-' }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.lifetimes') }}</span>
                                            <span class="text-sm"
                                                >{{ conn.ike_lifetime || '-' }} / {{ conn.esp_lifetime || '-' }}
                                                {{ $t('dashboard.vpnGateway.seconds') }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.dpd') }}</span>
                                            <span class="text-sm"
                                                >{{ conn.dpd_action || '-' }} / {{ conn.dpd_delay || '-' }}
                                                {{ $t('dashboard.vpnGateway.seconds') }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.identities') }}</span>
                                            <span class="mono text-sm"
                                                >{{ conn.local_id || '-' }} → {{ conn.remote_id || '-' }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.initiator') }}</span>
                                            <span>{{ yesNo(conn.initiator) }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.psk') }}</span>
                                            <span>{{
                                                conn.psk_set
                                                    ? $t('dashboard.vpnGateway.secretSet')
                                                    : $t('dashboard.vpnGateway.secretNotSet')
                                            }}</span>
                                        </div>
                                        <div v-if="conn.route_mode === 'bgp'" class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.bgpPassword') }}</span>
                                            <span>{{
                                                conn.bgp_password_set
                                                    ? $t('dashboard.vpnGateway.secretSet')
                                                    : $t('dashboard.vpnGateway.secretNotSet')
                                            }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.ifId') }}</span>
                                            <span class="mono text-sm">{{ conn.if_id || '-' }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.table.createdAt') }}</span>
                                            <span class="text-sm">{{ fmtTime(conn.created_at) }}</span>
                                        </div>
                                    </div>
                                </div>
                            </div>
                        </div>
                    </template>
                </DataTable>
            </div>

            <!-- ── Clients ── -->
            <div v-else-if="activeTab === 'clients'">
                <div class="tab-toolbar">
                    <span v-if="!gateway.client_enabled" class="text-secondary text-sm">
                        {{ $t('dashboard.vpnGateway.clientDisabledHint') }}
                    </span>
                    <span v-else></span>
                    <button
                        class="btn btn-primary btn-sm"
                        :disabled="!gateway.client_enabled"
                        @click="showClientModal = true"
                    >
                        <Plus :size="14" /> {{ $t('dashboard.vpnGateway.addClient') }}
                    </button>
                </div>

                <DataTable :columns="clientColumns" :rows="clients" row-key="id">
                    <template #empty>
                        <div>
                            <Laptop :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                            <p class="text-secondary">{{ $t('dashboard.vpnGateway.noClients') }}</p>
                        </div>
                    </template>

                    <template #cell-name="{ row: client }">
                        <div class="cell-name">{{ client.name }}</div>
                        <div v-if="client.description" class="cell-desc">{{ client.description }}</div>
                    </template>
                    <template #cell-ip_address="{ row: client }">
                        <code class="mono">{{ client.ip_address || '-' }}</code>
                    </template>
                    <template #cell-public_key="{ row: client }">
                        <span class="key-cell">
                            <code class="mono" :title="client.public_key">{{ shortKey(client.public_key) }}</code>
                            <button
                                class="copy-btn-mini"
                                :title="$t('actions.copy')"
                                :aria-label="$t('actions.copy')"
                                @click="copyId(client.public_key, `key-${client.id}`)"
                            >
                                <Check
                                    v-if="copiedId === `key-${client.id}`"
                                    :size="10"
                                    style="color: var(--success-color)"
                                />
                                <Copy v-else :size="10" />
                            </button>
                        </span>
                    </template>
                    <template #cell-enabled="{ row: client }">
                        <StatusBadge
                            :variant="client.enabled ? 'success' : 'neutral'"
                            :label="
                                client.enabled
                                    ? $t('dashboard.vpnGateway.enabledState')
                                    : $t('dashboard.vpnGateway.disabledState')
                            "
                        />
                    </template>
                    <template #cell-last_handshake_at="{ row: client }">
                        <span v-if="client.last_handshake_at" class="cell-time">{{
                            fmtTime(client.last_handshake_at)
                        }}</span>
                        <span v-else class="text-tertiary text-sm">{{
                            $t('dashboard.vpnGateway.neverConnected')
                        }}</span>
                    </template>
                    <template #cell-traffic="{ row: client }">
                        <span class="text-sm"
                            >↓ {{ traffic(client.bytes_in) }} · ↑ {{ traffic(client.bytes_out) }}</span
                        >
                    </template>
                    <template #cell-actions="{ row: client }">
                        <div class="row-actions">
                            <button
                                class="icon-btn-table"
                                :title="$t('dashboard.vpnGateway.showConfig')"
                                @click="openClientConfig(client)"
                            >
                                <FileText :size="16" />
                            </button>
                            <button
                                class="icon-btn-table"
                                :class="{ 'is-active': client.enabled }"
                                :title="client.enabled ? $t('actions.disable') : $t('actions.enable')"
                                :disabled="togglingClientId === client.id"
                                @click="handleToggleClient(client)"
                            >
                                <Power :size="16" />
                            </button>
                            <button
                                class="icon-btn-table icon-danger"
                                :title="$t('actions.delete')"
                                @click="handleDeleteClient(client)"
                            >
                                <Trash2 :size="16" />
                            </button>
                        </div>
                    </template>
                </DataTable>
            </div>

            <!-- ── Activity ── -->
            <div v-else-if="activeTab === 'activity'" class="card activity-card">
                <div v-if="activitiesLoading" class="loading-container">
                    <div class="loading-spinner"></div>
                </div>
                <div v-else-if="activitiesError" class="activity-state">
                    <p class="text-error">{{ activitiesError }}</p>
                    <button class="btn btn-secondary btn-sm" @click="loadActivities">{{ $t('actions.retry') }}</button>
                </div>
                <div v-else-if="activities.length === 0" class="activity-state text-secondary">
                    {{ $t('dashboard.vpnGateway.noActivity') }}
                </div>
                <template v-else>
                    <ul class="activity-list">
                        <ActivityEntry v-for="a in activities" :key="a.id" :activity="a" />
                    </ul>
                    <div v-if="activitiesCursor" class="activity-more">
                        <button
                            class="btn btn-secondary btn-sm"
                            :disabled="activitiesLoadingMore"
                            @click="loadMoreActivities"
                        >
                            {{
                                activitiesLoadingMore
                                    ? $t('dashboard.activityPage.loadingMore')
                                    : $t('dashboard.activityPage.loadMore')
                            }}
                        </button>
                    </div>
                </template>
            </div>
        </div>

        <!-- Edit gateway -->
        <BaseModal
            :show="showEditModal"
            :title="$t('dashboard.vpnGateway.editGateway')"
            :loading="editing"
            size="lg"
            form
            @close="showEditModal = false"
            @submit="handleEdit"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                <input
                    v-model="editForm.name"
                    type="text"
                    :class="['form-input', { 'input-error': !isEditNameValid }]"
                />
                <div v-if="!isEditNameValid" class="text-error form-hint">{{ $t('messages.invalidHostname') }}</div>
            </div>
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
                <input
                    v-model="editForm.description"
                    type="text"
                    class="form-input"
                    :placeholder="$t('messages.placeholderDescription')"
                />
            </div>
            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="editForm.ipsec_enabled" type="checkbox" />
                    {{ $t('dashboard.vpnGateway.ipsecEnabled') }}
                </label>
                <div class="form-hint">{{ $t('dashboard.vpnGateway.ipsecEnabledHint') }}</div>
            </div>
            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="editForm.client_enabled" type="checkbox" />
                    {{ $t('dashboard.vpnGateway.clientEnabled') }}
                </label>
                <div class="form-hint">{{ $t('dashboard.vpnGateway.clientEnabledHint') }}</div>
            </div>
            <template v-if="editForm.client_enabled">
                <div class="form-row">
                    <div class="form-group form-group-grow">
                        <label class="form-label">{{ $t('dashboard.vpnGateway.clientCidr') }} *</label>
                        <input
                            v-model="editForm.client_cidr"
                            type="text"
                            class="form-input mono"
                            placeholder="10.8.0.0/24"
                        />
                        <div class="form-hint">{{ $t('dashboard.vpnGateway.clientCidrChangeHint') }}</div>
                    </div>
                    <div class="form-group form-group-fixed">
                        <label class="form-label">{{ $t('dashboard.vpnGateway.clientPort') }}</label>
                        <input
                            v-model.number="editForm.client_port"
                            type="number"
                            min="1"
                            max="65535"
                            class="form-input"
                            placeholder="51820"
                        />
                    </div>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ $t('dashboard.vpnGateway.clientDns') }}</label>
                    <input
                        v-model="editForm.client_dns"
                        type="text"
                        class="form-input mono"
                        placeholder="10.0.0.2, 8.8.8.8"
                    />
                    <div class="form-hint">{{ $t('dashboard.vpnGateway.clientDnsHint') }}</div>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ $t('dashboard.vpnGateway.clientRoutes') }}</label>
                    <input
                        v-model="editForm.client_routes"
                        type="text"
                        class="form-input mono"
                        placeholder="10.0.1.0/24, 10.0.2.0/24"
                    />
                    <div class="form-hint">{{ $t('dashboard.vpnGateway.clientRoutesHint') }}</div>
                </div>
            </template>

            <template #footer>
                <div v-if="editError" class="modal-error footer-error">{{ editError }}</div>
                <button type="button" class="btn btn-secondary" :disabled="editing" @click="showEditModal = false">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="editing || !editForm.name">
                    <span
                        v-if="editing"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ editing ? $t('messages.saving') : $t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <VpnConnectionModal
            :show="showConnectionModal"
            :gateway-id="gatewayId"
            :connection="connectionToEdit"
            @close="showConnectionModal = false"
            @saved="onConnectionSaved"
        />

        <VpnClientModal
            :show="showClientModal"
            :gateway-id="gatewayId"
            @close="showClientModal = false"
            @created="onClientCreated"
        />

        <!-- Client configuration template -->
        <BaseModal
            :show="showConfigModal"
            :title="`${$t('dashboard.vpnGateway.clientConfig')} · ${configClient?.name || ''}`"
            size="lg"
            @close="showConfigModal = false"
        >
            <div v-if="configLoading" class="loading-container">
                <div class="loading-spinner"></div>
            </div>
            <div v-else-if="configError" class="modal-error">{{ configError }}</div>
            <VpnClientConfigBox
                v-else
                :config="configText"
                :file-name="configClient?.name || 'wg0'"
                private-key-missing
            />
            <template #footer>
                <button type="button" class="btn btn-primary" @click="showConfigModal = false">
                    {{ $t('actions.close') }}
                </button>
            </template>
        </BaseModal>

        <BaseModal
            :show="showDisableModal"
            :title="$t('dashboard.vpnGateway.disableTitle')"
            :loading="togglingEnabled"
            size="sm"
            @close="showDisableModal = false"
        >
            <p class="disable-warning">{{ $t('dashboard.vpnGateway.disableWarning') }}</p>
            <div v-if="disableError" class="modal-error">{{ disableError }}</div>
            <template #footer>
                <button
                    type="button"
                    class="btn btn-secondary"
                    :disabled="togglingEnabled"
                    @click="showDisableModal = false"
                >
                    {{ $t('actions.cancel') }}
                </button>
                <button
                    type="button"
                    class="btn btn-danger-outline"
                    :disabled="togglingEnabled"
                    @click="confirmDisableGateway"
                >
                    {{ $t('dashboard.vpnGateway.disableGateway') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleteModal.visible"
            :message="deleteModal.message"
            :resource-name="deleteModal.name"
            :loading="deleteModal.loading"
            :error="deleteModal.error"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
/* The error sits in the footer next to the buttons: in the body it ended up below the fold of the long
   forms and a failed submit looked like nothing happened */
.modal-error.footer-error {
    flex: 1;
    align-self: center;
    margin: 0;
}
/* .detail-header, .title-bar, .row-actions, .icon-btn-table, .badge-* are global (index.css) */

.loading-container,
.error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px;
    gap: var(--spacing-4);
}

.text-xs {
    font-size: var(--font-size-xs);
}

.text-sm {
    font-size: var(--font-size-sm);
}

.mono {
    font-family: var(--font-family-mono);
}

/* Info cards */
.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-5);
}

.info-card {
    padding: var(--spacing-5);
}

.info-card h3 {
    font-size: var(--font-size-base);
    font-weight: 600;
    margin: 0 0 var(--spacing-4) 0;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-3);
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}

.text-link:hover {
    text-decoration: underline;
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

.empty-hint {
    color: var(--text-tertiary);
    font-size: var(--font-size-sm);
    font-style: italic;
}

.key-text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.cidr-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-2);
}

.cidr-row {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
}

.cidr-chip {
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
}

.chip-error {
    background: var(--error-light);
    color: var(--error-dark);
}

/* Routed prefixes table */
.table-card {
    padding: 0;
    overflow: hidden;
    margin-bottom: var(--spacing-5);
}

.card-section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--spacing-4) var(--spacing-5);
}

.card-section-header h3 {
    margin: 0;
    font-size: var(--font-size-base);
    font-weight: 600;
}

.table-responsive {
    overflow-x: auto;
}

/* Tabs */
.tab-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-4);
}

.cell-name {
    font-weight: var(--font-weight-medium);
}

.cell-desc {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 2px;
}

.key-cell {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-1);
}

/* Expanded connection row */
.conn-panel {
    padding: var(--spacing-4) var(--spacing-5);
    cursor: default;
}

.conn-error {
    margin-bottom: var(--spacing-4);
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--error-light);
    color: var(--error-dark);
    font-size: var(--font-size-sm);
    word-break: break-word;
}

.conn-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: var(--spacing-5);
}

.conn-section h4 {
    margin: 0 0 var(--spacing-3);
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
}

.conn-section h4.mt {
    margin-top: var(--spacing-4);
}

.disabled-banner {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-4);
    padding: var(--spacing-3) var(--spacing-4);
    border-radius: var(--radius-md);
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: var(--font-size-sm);
}

.disabled-banner span {
    flex: 1;
}

.disable-warning {
    margin: 0;
    line-height: 1.6;
    color: var(--text-secondary);
}

.section-hint {
    margin: var(--spacing-2) 0 0;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

.kv {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-2);
}

.kv-row {
    display: grid;
    grid-template-columns: 128px minmax(0, 1fr);
    column-gap: var(--spacing-3);
    align-items: baseline;
    font-size: var(--font-size-sm);
}

.kv-label {
    color: var(--text-secondary);
}

.rejected-box {
    margin-top: var(--spacing-2);
    padding: var(--spacing-3);
    border: 1px solid var(--error-color);
    border-radius: var(--radius-sm);
    background: var(--error-light);
}

.rejected-title {
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--error-dark);
    margin-bottom: var(--spacing-2);
}

.rejected-hint {
    margin: var(--spacing-2) 0 0;
    font-size: var(--font-size-xs);
    color: var(--error-dark);
    line-height: 1.5;
}

/* Activity tab */
.activity-card {
    padding: var(--spacing-5);
}

.activity-list {
    list-style: none;
    margin: 0;
    padding: 0;
}

.activity-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--spacing-3);
    padding: var(--spacing-8) 0;
    font-size: var(--font-size-sm);
}

.activity-more {
    display: flex;
    justify-content: center;
    padding-top: var(--spacing-4);
    margin-top: var(--spacing-4);
    border-top: 1px solid var(--border-light);
}

/* Modals */
.modal-error {
    margin-top: var(--spacing-4);
    font-size: var(--font-size-sm);
    color: var(--error-color);
    background: var(--error-light);
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-sm);
}

.form-row {
    display: flex;
    gap: var(--spacing-3);
}

.form-group-grow {
    flex: 1;
    min-width: 0;
}

.form-group-fixed {
    width: 140px;
    flex-shrink: 0;
}

.form-hint {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 4px;
}

.checkbox-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
}

/* Action dropdown */
.action-dropdown {
    position: relative;
}

.dropdown-backdrop {
    position: fixed;
    inset: 0;
    z-index: var(--z-dropdown-backdrop);
}

.dropdown-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 160px;
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
    padding: 4px 0;
    z-index: var(--z-dropdown);
}

.dropdown-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    width: 100%;
    padding: 8px 14px;
    border: none;
    background: none;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
}

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-secondary);
}

.dropdown-item:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.dropdown-item-danger {
    color: var(--error-color);
}

.dropdown-divider {
    height: 1px;
    margin: 4px 0;
    background: var(--border-light);
}

.dropdown-enter-active,
.dropdown-leave-active {
    transition:
        opacity 0.15s,
        transform 0.15s;
}

.dropdown-enter-from,
.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-4px);
}
</style>
