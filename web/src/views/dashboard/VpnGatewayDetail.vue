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
    ChartLine,
    AlertTriangle,
} from 'lucide-vue-next'
import {
    vpnGatewaysApi,
    vpnConnectionsApi,
    vpnClientsApi,
    type VpnGateway,
    type VpnConnection,
    type VpnTunnel,
    type VpnPublicIp,
    type VpnClient,
    type VpnGatewayPatchPayload,
    type VpnPublicIpPayload,
} from '../../api/vpn'
import { subnetsApi, type Subnet } from '../../api/networks'
import { activitiesApi, type Activity } from '../../api/activities'
import { useToast } from '../../composables/useToast'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { quotaErrorMessage } from '../../utils/quotaError'
import { formatBytes } from '../../utils/format'
import { isValidName, isValidCIDRv4, hasDefaultRoute } from '../../utils/validation'
import { endpointName, isActiveActive, isSingleNode, removableEndpoint, tunnelEndpoints } from '../../utils/vpnEndpoint'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import DetailTabs from '../../components/base/DetailTabs.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import ActivityEntry from '../../components/activity/ActivityEntry.vue'
import VpnConnectionModal from '../../components/vpn/VpnConnectionModal.vue'
import VpnPeerConfigModal from '../../components/vpn/VpnPeerConfigModal.vue'
import VpnSecretValue from '../../components/vpn/VpnSecretValue.vue'
import VpnClientModal from '../../components/vpn/VpnClientModal.vue'
import VpnClientConfigBox from '../../components/vpn/VpnClientConfigBox.vue'
import VpnTrafficCharts from '../../components/vpn/VpnTrafficCharts.vue'
import VpnPublicAddressPicker from '../../components/vpn/VpnPublicAddressPicker.vue'
import { OPTION_LIST_LIMIT } from '../../api/listParams'

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

type TabId = 'overview' | 'connections' | 'clients' | 'monitoring' | 'activity'
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
    { id: 'monitoring', label: t('dashboard.vpnGateway.monitoringTab'), icon: ChartLine },
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

// HA mode: gateways created before HA modes existed have none and are active_standby
const isAA = computed(() => isActiveActive(gateway.value))
const haModeText = computed(() =>
    isAA.value ? t('dashboard.vpnGateway.haModeActiveActive') : t('dashboard.vpnGateway.haModeActiveStandby')
)
const haModeHint = computed(() =>
    isAA.value ? t('dashboard.vpnGateway.haModeActiveActiveHint') : t('dashboard.vpnGateway.haModeActiveStandbyHint')
)
const singleNode = computed(() => isSingleNode(gateway.value))

// Public addresses of the gateway; public_ip alone when the answer carries no list
const publicAddresses = computed<VpnPublicIp[]>(() => {
    const g = gateway.value
    if (!g) return []
    if (g.public_ips?.length) return g.public_ips
    return g.public_ip ? [{ endpoint: 'vip1', address: g.public_ip }] : []
})
// The address the gateway can still take, and the one it may release (the backend has the last word)
const addableEndpoint = computed(() => gateway.value?.addable_endpoint || '')
// Each address is named once there is more than one (including the one that can be added), or when the
// addresses belong to nodes
const nameAddresses = computed(() => publicAddresses.value.length > 1 || isAA.value || !!addableEndpoint.value)
const addressName = (endpoint: string) => endpointName(t, endpoint, gateway.value?.ha_mode)
const removableAddress = computed(() => (gateway.value ? removableEndpoint(gateway.value) : ''))
// Addresses change only on an available gateway or one in error: the backend refuses otherwise, since a
// change made while the gateway is being built would not reach its nodes
const addressChangeAllowed = computed(() => gateway.value?.status === 'available' || gateway.value?.status === 'error')
// The fixed address of a node of an active_active gateway (HA nodes card)
const nodeAddress = (hostid: number) => publicAddresses.value.find((p) => p.hostid === hostid && p.hostid >= 0)
// active_active: the VRRP master only matters for the client VPN address, when the gateway has one
const hasClientAddress = computed(() => publicAddresses.value.some((p) => p.endpoint === 'vip1'))
const nodeLabel = (node: { hostid: number; role: string }) => {
    const fixed = isAA.value ? nodeAddress(node.hostid) : undefined
    if (fixed) return addressName(fixed.endpoint)
    return node.role === 'MASTER' ? t('dashboard.vpnGateway.roleMaster') : t('dashboard.vpnGateway.roleBackup')
}

// Tunnels of a connection, primary first; tunnelsBySlot keeps the slot order (restart menu)
const tunnelCount = (conn: VpnConnection) => conn.tunnels?.length || 0
const orderedTunnels = (conn: VpnConnection): VpnTunnel[] =>
    [...(conn.tunnels || [])].sort(
        (a, b) => Number(a.priority !== 'primary') - Number(b.priority !== 'primary') || a.slot - b.slot
    )
const tunnelsBySlot = (conn: VpnConnection): VpnTunnel[] => [...(conn.tunnels || [])].sort((a, b) => a.slot - b.slot)
const priorityText = (tun: VpnTunnel) =>
    tun.priority === 'standby' ? t('dashboard.vpnGateway.priorityStandby') : t('dashboard.vpnGateway.priorityPrimary')
// ecmp: every tunnel that is up carries traffic, so there is no primary / standby to show
const isEcmp = (conn: VpnConnection) => conn.traffic_policy === 'ecmp'
const showPriority = (conn: VpnConnection) => tunnelCount(conn) > 1 && !isEcmp(conn)
const trafficPolicyText = (conn: VpnConnection) =>
    isEcmp(conn) ? t('dashboard.vpnGateway.trafficPolicyEcmp') : t('dashboard.vpnGateway.trafficPolicyPreferred')
const degradedTitle = (conn: VpnConnection) => {
    if (conn.status !== 'degraded') return undefined
    return isEcmp(conn) ? t('dashboard.vpnGateway.degradedEcmpHint') : t('dashboard.vpnGateway.degradedHint')
}
// The local address of a tunnel is worth naming only when the gateway offers more than one
const showTunnelEndpoint = computed(() => tunnelEndpoints(publicAddresses.value, gateway.value?.ha_mode).length > 1)
const tunnelEndpointText = (tun: VpnTunnel) => endpointName(t, tun.endpoint, gateway.value?.ha_mode, true)
const restartTunnelText = (conn: VpnConnection, tun: VpnTunnel) => {
    const text = showPriority(conn)
        ? t('dashboard.vpnGateway.restartTunnelN', { n: tun.slot, role: priorityText(tun) })
        : t('dashboard.vpnGateway.restartTunnelPlain', { n: tun.slot })
    return tun.hostname ? `${text} · ${tun.hostname}` : text
}
// The BFD state is reported on the tunnel and inside the BGP report; either may be missing
const bfdState = (tun: VpnTunnel) => tun.bfd_state || tun.bgp?.bfd || ''
// BGP badge on a tunnel line: green once the session is up, amber while it is not, grey before any report
const bgpChipClass = (tun: VpnTunnel) => {
    if (!tun.bgp) return 'badge-secondary'
    return (tun.bgp.state || '').toLowerCase() === 'established' ? 'badge-success' : 'badge-warning'
}
const bgpChipTitle = (conn: VpnConnection, tun: VpnTunnel) => {
    const text = tun.bgp
        ? t('dashboard.vpnGateway.bgpStateTitle', { state: tun.bgp.state || '-' })
        : t('dashboard.vpnGateway.noBgpReport')
    const bfd = conn.bfd_enabled ? bfdState(tun) : ''
    return bfd ? `${text} · ${t('dashboard.vpnGateway.bfdStateShort', { state: bfd })}` : text
}
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

// Shared delete confirm modal (gateway, connection, client, public address). title / confirmLabel replace
// "Delete" where the action is not a deletion (releasing a public address)
const deleteModal = ref<{
    visible: boolean
    name: string
    message: string
    title?: string
    confirmLabel?: string
    loading: boolean
    error: string
    onConfirm: () => Promise<void>
}>({ visible: false, name: '', message: '', loading: false, error: '', onConfirm: async () => {} })

const openDeleteModal = (
    name: string,
    message: string,
    onConfirm: () => Promise<void>,
    labels: { title?: string; confirmLabel?: string } = {}
) => {
    deleteModal.value = { visible: true, name, message, ...labels, loading: false, error: '', onConfirm }
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

// ─── Public addresses ────────────────────────────────────────────────────────
// Add the addable address: the second floating IP of an active_standby gateway, or the client VPN address
// of an active_active one. Each takes one public IP of the quota
const publicSubnets = ref<Subnet[]>([])
const showAddAddressModal = ref(false)
const addingAddress = ref(false)
const addAddressError = ref('')
const addAddressForm = ref({ subnet_id: '', ip: '' })
// Remembered when the modal opens, so the text does not change under the user once the address is added
const addAddressEndpoint = ref('')

const fetchPublicSubnets = async () => {
    try {
        const res = await subnetsApi.list({ limit: OPTION_LIST_LIMIT })
        publicSubnets.value = (res.subnets || []).filter((s) => s.type === 'public')
    } catch (err) {
        console.error('Failed to fetch subnets:', err)
        publicSubnets.value = []
    }
}

const openAddAddress = () => {
    closeActionMenu()
    // Offered from the edit modal too, when the client VPN of an active_active gateway needs its address
    showEditModal.value = false
    addAddressEndpoint.value = addableEndpoint.value
    addAddressForm.value = { subnet_id: '', ip: '' }
    addAddressError.value = ''
    showAddAddressModal.value = true
    fetchPublicSubnets()
}

const handleAddAddress = async () => {
    const f = addAddressForm.value
    const payload: VpnPublicIpPayload = {}
    if (f.subnet_id) payload.public_subnet = { id: f.subnet_id }
    if (f.ip) payload.public_ip = f.ip
    addingAddress.value = true
    addAddressError.value = ''
    try {
        // The answer is the gateway detail with the new address
        gateway.value = await vpnGatewaysApi.addPublicIp(gatewayId, payload)
        showAddAddressModal.value = false
        toast.success(t('dashboard.vpnGateway.addPublicIpSuccess'))
    } catch (err) {
        addAddressError.value = quotaErrorMessage(err, t, te) || errorMessage(err, t('messages.error'))
    } finally {
        addingAddress.value = false
    }
}

// Release the removable address. clapi refuses while a tunnel still uses vip2 or the client VPN of an
// active_active gateway is on; the refusal shows in the confirm modal
const handleRemoveAddress = (p: VpnPublicIp) => {
    const message =
        p.endpoint === 'vip1'
            ? t('dashboard.vpnGateway.removeClientAddressWarning')
            : t('dashboard.vpnGateway.removePublicIpWarning')
    openDeleteModal(
        `${addressName(p.endpoint)} · ${p.address}`,
        message,
        async () => {
            await vpnGatewaysApi.removePublicIp(gatewayId, p.endpoint)
            toast.success(t('dashboard.vpnGateway.removePublicIpSuccess'))
            await fetchGateway(true)
        },
        { title: t('dashboard.vpnGateway.removePublicIp'), confirmLabel: t('actions.release') }
    )
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
// The client VPN of an active_active gateway runs on its own floating address (vip1), which the gateway may
// not have: clapi refuses to turn the client VPN on until that address is added
const clientNeedsAddress = computed(
    () => isAA.value && !gateway.value?.client_enabled && !publicAddresses.value.some((p) => p.endpoint === 'vip1')
)

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
    if (f.client_enabled && clientNeedsAddress.value) {
        editError.value = t('dashboard.vpnGateway.clientNeedsAddress')
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
        if (f.client_routes && hasDefaultRoute(f.client_routes)) {
            editError.value = t('dashboard.vpnGateway.clientRoutesNoDefault')
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
    // Below 1280 its badges move under the name (.route-inline) and the tunnel lines may wrap, so that the
    // table fits the ~780px of a 1024 viewport too
    { key: 'route_mode', label: t('dashboard.vpnGateway.routeMode'), hideBelow: 1280 },
    { key: 'tunnels', label: t('dashboard.vpnGateway.tunnels') },
    { key: 'status', label: t('dashboard.table.status') },
    // The table must fit the ~1110px a 1440 viewport leaves and the ~1040px of a 1366 one: the BGP state
    // is a badge on each tunnel line, the traffic sum goes below 1600 and the remote networks below 1440
    // (the expanded row has both)
    { key: 'remote', label: t('dashboard.vpnGateway.remoteNetworks'), hideBelow: 1440 },
    { key: 'traffic', label: t('dashboard.vpnGateway.traffic'), hideBelow: 1600 },
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
// Peer configuration: looked up by id so a background refresh of the gateway updates the open modal
const peerConfigId = ref('')
const peerConfigConnection = computed(() => connections.value.find((c) => c.id === peerConfigId.value) ?? null)
const onConnectionSaved = async () => {
    toast.success(t('messages.success'))
    await fetchGateway(true)
}

// With two tunnels the restart button opens a menu: all tunnels or one of them
const restartMenuId = ref('')
const toggleRestartMenu = (conn: VpnConnection) => {
    restartMenuId.value = restartMenuId.value === conn.id ? '' : conn.id
}

// slot: restart only that tunnel; omitted restarts every tunnel of the connection
const handleRestartConnection = async (conn: VpnConnection, slot?: number) => {
    restartMenuId.value = ''
    restartingId.value = conn.id
    try {
        await vpnConnectionsApi.restart(gatewayId, conn.id, slot)
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

// Local networks of a BGP connection that the gateway actually advertises to the peer (on any of its
// tunnels); a subnet without any instance is not advertised yet
const hasBgpReport = (conn: VpnConnection) => (conn.tunnels || []).some((tun) => !!tun.bgp)
const isAdvertised = (conn: VpnConnection, cidr: string) =>
    (conn.tunnels || []).some((tun) => (tun.bgp?.advertised || []).includes(cidr))

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
                            <span class="badge" :class="isAA ? 'badge-info' : 'badge-secondary'" :title="haModeHint">{{
                                haModeText
                            }}</span>
                            <span
                                v-if="singleNode"
                                class="badge badge-warning"
                                :title="$t('dashboard.vpnGateway.singleNodeNotice')"
                                >{{ $t('dashboard.vpnGateway.singleNode') }}</span
                            >
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
                                    v-if="addableEndpoint"
                                    class="dropdown-item"
                                    :disabled="!addressChangeAllowed"
                                    :title="
                                        addressChangeAllowed ? undefined : $t('dashboard.vpnGateway.addressChangeWait')
                                    "
                                    @click="openAddAddress"
                                >
                                    <Plus :size="14" /> {{ $t('dashboard.vpnGateway.addPublicIp') }}
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
            <div v-if="gateway.status === 'error' && gateway.status_reason" class="disabled-banner error-banner">
                <AlertTriangle :size="16" />
                <span>{{ $t('dashboard.vpnGateway.errorReason', { reason: gateway.status_reason }) }}</span>
            </div>
            <div v-if="singleNode" class="disabled-banner">
                <AlertTriangle :size="16" />
                <span>{{ $t('dashboard.vpnGateway.singleNodeNotice') }}</span>
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
                            <InfoRow :label="$t('dashboard.vpnGateway.haMode')">
                                <span
                                    class="badge"
                                    :class="isAA ? 'badge-info' : 'badge-secondary'"
                                    :title="haModeHint"
                                    >{{ haModeText }}</span
                                >
                            </InfoRow>
                            <!-- One row per public address: which one it is, the node of a fixed one, and the
                                 add / release actions for the one address a gateway can take or give back -->
                            <InfoRow
                                v-for="p in publicAddresses"
                                :key="p.endpoint"
                                :label="nameAddresses ? addressName(p.endpoint) : $t('dashboard.vpnGateway.publicIp')"
                                mono
                            >
                                <span class="nowrap-text">{{ p.address || '-' }}</span>
                                <span v-if="p.hostname" class="address-node">{{ p.hostname }}</span>
                                <button
                                    v-if="p.address"
                                    class="copy-btn"
                                    :title="$t('actions.copy')"
                                    :aria-label="$t('actions.copy')"
                                    @click="copyId(p.address, `ip-${p.endpoint}`)"
                                >
                                    <Check v-if="copiedId === `ip-${p.endpoint}`" :size="12" class="copied-icon" />
                                    <Copy v-else :size="12" />
                                </button>
                                <button
                                    v-if="p.endpoint === removableAddress"
                                    class="copy-btn address-remove"
                                    :title="$t('dashboard.vpnGateway.removePublicIp')"
                                    :aria-label="$t('dashboard.vpnGateway.removePublicIp')"
                                    :disabled="!addressChangeAllowed"
                                    @click="handleRemoveAddress(p)"
                                >
                                    <Trash2 :size="12" />
                                </button>
                            </InfoRow>
                            <InfoRow v-if="addableEndpoint" :label="addressName(addableEndpoint)">
                                <span class="text-tertiary">{{ $t('dashboard.vpnGateway.notAssigned') }}</span>
                                <button
                                    type="button"
                                    class="link-btn add-address-btn"
                                    :disabled="!addressChangeAllowed"
                                    :title="
                                        addressChangeAllowed ? undefined : $t('dashboard.vpnGateway.addressChangeWait')
                                    "
                                    @click="openAddAddress"
                                >
                                    <Plus :size="12" /> {{ $t('actions.add') }}
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
                        <p class="card-hint">{{ haModeHint }}</p>
                        <div class="key-value-list">
                            <InfoRow
                                v-if="!isAA || hasClientAddress"
                                :label="
                                    isAA
                                        ? $t('dashboard.vpnGateway.clientAddressHolder')
                                        : $t('dashboard.vpnGateway.masterNode')
                                "
                            >
                                <span>{{ gateway.master_hostname || '-' }}</span>
                                <span v-if="gateway.master_reported_at" class="text-tertiary text-xs">
                                    ({{ $t('dashboard.vpnGateway.reportedAt') }}
                                    {{ fmtTime(gateway.master_reported_at) }})
                                </span>
                            </InfoRow>
                            <InfoRow v-for="node in gateway.nodes || []" :key="node.hostid" :label="nodeLabel(node)">
                                <span>{{ node.hostname || `#${node.hostid}` }}</span>
                                <span v-if="node.master && (!isAA || hasClientAddress)" class="badge badge-success">{{
                                    isAA
                                        ? $t('dashboard.vpnGateway.clientVpnAddress')
                                        : $t('dashboard.vpnGateway.activeNode')
                                }}</span>
                                <!-- active_active: each node runs the tunnels of its own address -->
                                <code v-if="nodeAddress(node.hostid)" class="node-address">{{
                                    nodeAddress(node.hostid)?.address
                                }}</code>
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

                <DataTable :columns="connectionColumns" :rows="connections" row-key="id" expandable allow-overflow>
                    <template #empty>
                        <div>
                            <Network :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                            <p class="text-secondary">{{ $t('dashboard.vpnGateway.noConnections') }}</p>
                        </div>
                    </template>

                    <template #cell-name="{ row: conn }">
                        <div class="cell-name">{{ conn.name }}</div>
                        <div v-if="conn.description" class="cell-desc">{{ conn.description }}</div>
                        <div class="route-inline">
                            <span class="badge badge-secondary route-badge">{{ routeModeText(conn.route_mode) }}</span>
                            <span
                                v-if="isEcmp(conn) && tunnelCount(conn) > 1"
                                class="badge badge-info route-badge"
                                :title="$t('dashboard.vpnGateway.trafficPolicyEcmpHint')"
                                >{{ $t('dashboard.vpnGateway.trafficPolicyEcmpShort') }}</span
                            >
                        </div>
                    </template>
                    <template #cell-route_mode="{ row: conn }">
                        <div class="badge-row">
                            <span class="badge badge-secondary route-badge">{{ routeModeText(conn.route_mode) }}</span>
                            <span
                                v-if="isEcmp(conn) && tunnelCount(conn) > 1"
                                class="badge badge-info route-badge"
                                :title="$t('dashboard.vpnGateway.trafficPolicyEcmpHint')"
                                >{{ $t('dashboard.vpnGateway.trafficPolicyEcmpShort') }}</span
                            >
                        </div>
                    </template>
                    <!-- One line per tunnel, primary first: our address (and its node) -> peer address -->
                    <template #cell-tunnels="{ row: conn }">
                        <div class="tunnel-lines">
                            <div v-for="tun in orderedTunnels(conn)" :key="tun.id" class="tunnel-line">
                                <span
                                    v-if="showPriority(conn)"
                                    class="badge"
                                    :class="tun.priority === 'standby' ? 'badge-secondary' : 'badge-primary'"
                                    >{{ priorityText(tun) }}</span
                                >
                                <code class="mono text-sm">{{ tun.public_ip || '-' }}</code>
                                <span v-if="tun.hostname" class="text-tertiary text-xs">({{ tun.hostname }})</span>
                                <span class="text-tertiary">→</span>
                                <code v-if="tun.remote_gateway" class="mono text-sm">{{ tun.remote_gateway }}</code>
                                <span v-else class="text-tertiary text-sm">{{
                                    $t('dashboard.vpnGateway.responderOnly')
                                }}</span>
                                <StatusBadge
                                    v-if="tunnelCount(conn) > 1"
                                    dot-only
                                    :status="tun.status"
                                    :label="connectionStatusText(tun.status)"
                                />
                                <span
                                    v-if="conn.route_mode === 'bgp'"
                                    class="badge bgp-chip"
                                    :class="bgpChipClass(tun)"
                                    :title="bgpChipTitle(conn, tun)"
                                    >BGP</span
                                >
                            </div>
                        </div>
                    </template>
                    <template #cell-status="{ row: conn }">
                        <StatusBadge
                            :status="conn.status"
                            :label="connectionStatusText(conn.status)"
                            :title="degradedTitle(conn)"
                        />
                    </template>
                    <template #cell-remote="{ row: conn }">
                        <span class="mono text-sm">{{
                            (conn.route_mode === 'bgp' ? conn.remote_summary_cidrs : conn.remote_cidrs) || '-'
                        }}</span>
                    </template>
                    <template #cell-traffic="{ row: conn }">
                        <span class="text-sm nowrap-text"
                            >↓ {{ traffic(conn.bytes_in) }} · ↑ {{ traffic(conn.bytes_out) }}</span
                        >
                    </template>
                    <template #cell-actions="{ row: conn }">
                        <div class="row-actions" @click.stop>
                            <div v-if="tunnelCount(conn) > 1" class="action-dropdown">
                                <button
                                    class="icon-btn-table"
                                    :title="
                                        gateway.enabled === false
                                            ? $t('dashboard.vpnGateway.restartDisabled')
                                            : $t('actions.restart')
                                    "
                                    :disabled="restartingId === conn.id || gateway.enabled === false"
                                    @click="toggleRestartMenu(conn)"
                                >
                                    <RefreshCw :size="16" :class="{ spinning: restartingId === conn.id }" />
                                </button>
                                <Transition name="dropdown">
                                    <div v-if="restartMenuId === conn.id" class="dropdown-menu">
                                        <button class="dropdown-item" @click="handleRestartConnection(conn)">
                                            {{ $t('dashboard.vpnGateway.restartAllTunnels') }}
                                        </button>
                                        <div class="dropdown-divider"></div>
                                        <button
                                            v-for="tun in tunnelsBySlot(conn)"
                                            :key="tun.id"
                                            class="dropdown-item"
                                            @click="handleRestartConnection(conn, tun.slot)"
                                        >
                                            {{ restartTunnelText(conn, tun) }}
                                        </button>
                                    </div>
                                </Transition>
                                <div
                                    v-if="restartMenuId === conn.id"
                                    class="dropdown-backdrop"
                                    @click="restartMenuId = ''"
                                ></div>
                            </div>
                            <button
                                v-else
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
                                :title="$t('dashboard.vpnGateway.peerConfig.button')"
                                @click="peerConfigId = conn.id"
                            >
                                <FileText :size="16" />
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

                    <!-- Expanded row: one card per tunnel (with its BGP session), effective networks, IKE parameters -->
                    <template #expanded="{ row: conn }">
                        <div class="conn-panel">
                            <div class="tunnel-grid">
                                <div v-for="tun in orderedTunnels(conn)" :key="tun.id" class="tunnel-card">
                                    <div class="tunnel-card-head">
                                        <h4>{{ $t('dashboard.vpnGateway.tunnelN', { n: tun.slot }) }}</h4>
                                        <span
                                            v-if="showPriority(conn)"
                                            class="badge"
                                            :class="tun.priority === 'standby' ? 'badge-secondary' : 'badge-primary'"
                                            >{{ priorityText(tun) }}</span
                                        >
                                        <StatusBadge :status="tun.status" :label="connectionStatusText(tun.status)" />
                                    </div>
                                    <div v-if="tun.last_error" class="conn-error">
                                        <strong>{{ $t('dashboard.vpnGateway.lastError') }}:</strong>
                                        {{ tun.last_error }}
                                    </div>
                                    <div class="kv">
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.localAddress') }}</span>
                                            <span class="mono text-sm"
                                                >{{ tun.public_ip || '-' }}
                                                <span v-if="showTunnelEndpoint" class="text-tertiary"
                                                    >({{ tunnelEndpointText(tun) }})</span
                                                ></span
                                            >
                                        </div>
                                        <!-- active_active: the node that runs this tunnel -->
                                        <div v-if="tun.hostname" class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.node') }}</span>
                                            <span class="text-sm">{{ tun.hostname }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.peerAddress') }}</span>
                                            <span v-if="tun.remote_gateway" class="mono text-sm">{{
                                                tun.remote_gateway
                                            }}</span>
                                            <span v-else class="text-tertiary text-sm">{{
                                                $t('dashboard.vpnGateway.responderOnly')
                                            }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.identities') }}</span>
                                            <span class="mono text-sm"
                                                >{{ conn.local_id || '-' }} → {{ tun.remote_id || '-' }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.psk') }}</span>
                                            <VpnSecretValue v-if="tun.psk" :value="tun.psk" />
                                            <span v-else class="text-sm">{{
                                                tun.psk_set
                                                    ? $t('dashboard.vpnGateway.tunnelPskOwn')
                                                    : $t('dashboard.vpnGateway.tunnelPskShared')
                                            }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.establishedAt') }}</span>
                                            <span class="text-sm">{{ fmtTime(tun.established_at) }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.traffic') }}</span>
                                            <span class="text-sm"
                                                >↓ {{ traffic(tun.bytes_in) }} · ↑ {{ traffic(tun.bytes_out) }}</span
                                            >
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.ifId') }}</span>
                                            <span class="mono text-sm">{{ tun.if_id || '-' }}</span>
                                        </div>
                                    </div>

                                    <template v-if="conn.route_mode === 'bgp'">
                                        <h5 class="tunnel-sub">
                                            {{ $t('dashboard.vpnGateway.bgpReport') }}
                                            <span v-if="tun.bgp_reported_at" class="text-tertiary text-xs">
                                                ({{ $t('dashboard.vpnGateway.reportedAt') }}
                                                {{ fmtTime(tun.bgp_reported_at) }})
                                            </span>
                                        </h5>
                                        <div class="kv">
                                            <div class="kv-row">
                                                <span class="kv-label">{{
                                                    $t('dashboard.vpnGateway.bgpSession')
                                                }}</span>
                                                <span class="mono text-sm"
                                                    >AS{{ conn.local_asn }} {{ tun.tunnel_local_ip || '-' }} ↔ AS{{
                                                        conn.peer_asn
                                                    }}
                                                    {{ tun.tunnel_peer_ip || '-' }}</span
                                                >
                                            </div>
                                            <template v-if="tun.bgp">
                                                <div class="kv-row">
                                                    <span class="kv-label">{{
                                                        $t('dashboard.vpnGateway.bgpState')
                                                    }}</span>
                                                    <span>{{ tun.bgp.state || '-' }}</span>
                                                </div>
                                                <div class="kv-row">
                                                    <span class="kv-label">{{
                                                        $t('dashboard.vpnGateway.bgpUptime')
                                                    }}</span>
                                                    <span>{{ tun.bgp.uptime || '-' }}</span>
                                                </div>
                                                <div v-if="conn.bfd_enabled" class="kv-row">
                                                    <span class="kv-label">{{
                                                        $t('dashboard.vpnGateway.bfdState')
                                                    }}</span>
                                                    <span>{{ bfdState(tun) || '-' }}</span>
                                                </div>
                                                <div class="kv-row">
                                                    <span class="kv-label">{{
                                                        $t('dashboard.vpnGateway.prefixesReceived')
                                                    }}</span>
                                                    <span
                                                        >{{ tun.bgp.prefixes_received }} /
                                                        {{ conn.max_prefixes || '-' }}</span
                                                    >
                                                </div>
                                                <div class="kv-row">
                                                    <span class="kv-label">{{
                                                        $t('dashboard.vpnGateway.prefixesSent')
                                                    }}</span>
                                                    <span>{{ tun.bgp.prefixes_sent }}</span>
                                                </div>
                                                <div class="kv-row">
                                                    <span class="kv-label">{{
                                                        $t('dashboard.vpnGateway.accepted')
                                                    }}</span>
                                                    <div v-if="tun.bgp.accepted?.length" class="cidr-list">
                                                        <code
                                                            v-for="cidr in tun.bgp.accepted"
                                                            :key="cidr"
                                                            class="cidr-chip"
                                                            >{{ cidr }}</code
                                                        >
                                                    </div>
                                                    <span v-else>-</span>
                                                </div>
                                                <div v-if="tun.bgp.rejected?.length" class="rejected-box">
                                                    <div class="rejected-title">
                                                        {{ $t('dashboard.vpnGateway.rejected') }} ({{
                                                            tun.bgp.rejected.length
                                                        }})
                                                    </div>
                                                    <div class="cidr-list">
                                                        <code
                                                            v-for="cidr in tun.bgp.rejected"
                                                            :key="cidr"
                                                            class="cidr-chip chip-error"
                                                            >{{ cidr }}</code
                                                        >
                                                    </div>
                                                    <p class="rejected-hint">
                                                        {{ $t('dashboard.vpnGateway.rejectedHint') }}
                                                    </p>
                                                </div>
                                                <p v-if="tun.bgp.truncated" class="section-hint">
                                                    {{ $t('dashboard.vpnGateway.bgpTruncated') }}
                                                </p>
                                            </template>
                                            <span v-else class="text-tertiary text-sm">{{
                                                $t('dashboard.vpnGateway.noBgpReport')
                                            }}</span>
                                        </div>
                                    </template>
                                </div>
                            </div>

                            <div class="conn-grid">
                                <div class="conn-section">
                                    <h4>{{ $t('dashboard.vpnGateway.effectiveLocalCidrs') }}</h4>
                                    <div v-if="conn.effective_local_cidrs?.length" class="cidr-list">
                                        <span v-for="cidr in conn.effective_local_cidrs" :key="cidr" class="cidr-row">
                                            <code class="cidr-chip">{{ cidr }}</code>
                                            <span
                                                v-if="conn.route_mode === 'bgp' && hasBgpReport(conn)"
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
                                    <p v-if="conn.route_mode === 'bgp' && hasBgpReport(conn)" class="section-hint">
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
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.initiator') }}</span>
                                            <span>{{ yesNo(conn.initiator) }}</span>
                                        </div>
                                        <div class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.psk') }}</span>
                                            <VpnSecretValue v-if="conn.psk" :value="conn.psk" />
                                            <span v-else>{{
                                                conn.psk_set
                                                    ? $t('dashboard.vpnGateway.secretSet')
                                                    : $t('dashboard.vpnGateway.secretNotSet')
                                            }}</span>
                                        </div>
                                        <div v-if="conn.route_mode === 'bgp'" class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.bgpPassword') }}</span>
                                            <VpnSecretValue v-if="conn.bgp_password" :value="conn.bgp_password" />
                                            <span v-else>{{
                                                conn.bgp_password_set
                                                    ? $t('dashboard.vpnGateway.secretSet')
                                                    : $t('dashboard.vpnGateway.secretNotSet')
                                            }}</span>
                                        </div>
                                        <template v-if="conn.route_mode === 'bgp'">
                                            <div class="kv-row">
                                                <span class="kv-label">{{ $t('dashboard.vpnGateway.bfd') }}</span>
                                                <span class="text-sm">{{
                                                    conn.bfd_enabled
                                                        ? $t('dashboard.vpnGateway.bfdValue', {
                                                              interval: conn.bfd_interval,
                                                              multiplier: conn.bfd_multiplier,
                                                          })
                                                        : $t('dashboard.vpnGateway.disabledState')
                                                }}</span>
                                            </div>
                                            <div v-if="showPriority(conn)" class="kv-row">
                                                <span class="kv-label">{{
                                                    $t('dashboard.vpnGateway.asPathPrepend')
                                                }}</span>
                                                <span class="text-sm">{{ conn.as_path_prepend }}</span>
                                            </div>
                                        </template>
                                        <div v-if="tunnelCount(conn) > 1" class="kv-row">
                                            <span class="kv-label">{{ $t('dashboard.vpnGateway.trafficPolicy') }}</span>
                                            <span class="text-sm">{{ trafficPolicyText(conn) }}</span>
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

            <!-- ── Traffic history ── -->
            <VpnTrafficCharts
                v-else-if="activeTab === 'monitoring'"
                :gateway-id="gatewayId"
                :ipsec-enabled="gateway.ipsec_enabled"
                :client-enabled="gateway.client_enabled"
                :connections="connections"
            />

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
                <label class="checkbox-label" :class="{ 'is-disabled': clientNeedsAddress }">
                    <input v-model="editForm.client_enabled" type="checkbox" :disabled="clientNeedsAddress" />
                    {{ $t('dashboard.vpnGateway.clientEnabled') }}
                </label>
                <div class="form-hint">{{ $t('dashboard.vpnGateway.clientEnabledHint') }}</div>
                <!-- active_active without its client VPN address: add the address first, then turn the VPN on -->
                <div v-if="clientNeedsAddress" class="address-needed">
                    <span>{{ $t('dashboard.vpnGateway.clientNeedsAddress') }}</span>
                    <button
                        type="button"
                        class="btn btn-secondary btn-sm"
                        :disabled="editing || !addressChangeAllowed"
                        @click="openAddAddress"
                    >
                        <Plus :size="14" /> {{ $t('dashboard.vpnGateway.addPublicIp') }}
                    </button>
                </div>
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
                        placeholder="192.168.1.53"
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
            :public-ips="publicAddresses"
            :ha-mode="gateway?.ha_mode"
            @close="showConnectionModal = false"
            @saved="onConnectionSaved"
        />

        <VpnPeerConfigModal
            :show="!!peerConfigConnection"
            :gateway="gateway"
            :connection="peerConfigConnection"
            @close="peerConfigId = ''"
        />

        <!-- Add the public address the gateway can still take (addable_endpoint) -->
        <BaseModal
            :show="showAddAddressModal"
            :title="$t('dashboard.vpnGateway.addPublicIp')"
            :loading="addingAddress"
            size="lg"
            form
            @close="showAddAddressModal = false"
            @submit="handleAddAddress"
        >
            <p class="modal-intro">
                {{
                    addAddressEndpoint === 'vip1'
                        ? $t('dashboard.vpnGateway.addClientAddressHint')
                        : $t('dashboard.vpnGateway.addSecondAddressHint')
                }}
            </p>
            <VpnPublicAddressPicker
                v-model:subnet-id="addAddressForm.subnet_id"
                v-model:ip="addAddressForm.ip"
                :subnets="publicSubnets"
            />
            <div class="quota-note">{{ $t('dashboard.vpnGateway.publicIpQuotaUse', { n: 1 }) }}</div>
            <template #footer>
                <div v-if="addAddressError" class="modal-error footer-error">{{ addAddressError }}</div>
                <button
                    type="button"
                    class="btn btn-secondary"
                    :disabled="addingAddress"
                    @click="showAddAddressModal = false"
                >
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="addingAddress">
                    <span
                        v-if="addingAddress"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ addingAddress ? $t('messages.saving') : $t('dashboard.vpnGateway.addPublicIp') }}
                </button>
            </template>
        </BaseModal>

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
            :title="deleteModal.title"
            :confirm-label="deleteModal.confirmLabel"
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

/* Public addresses: the node of a fixed address, release / add actions */
.address-node {
    font-family: var(--font-family);
    font-size: var(--font-size-xs);
    font-weight: normal;
    color: var(--text-tertiary);
    white-space: nowrap;
}

/* An address or a traffic figure never breaks in the middle */
.nowrap-text {
    white-space: nowrap;
}

/* The global .copy-btn look only covers the title bar; the icon buttons of the info cards get the same */
.info-card .copy-btn {
    display: inline-flex;
    align-items: center;
    padding: 2px;
    border: none;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-light);
    cursor: pointer;
}

.info-card .copy-btn:hover:not(:disabled) {
    color: var(--primary-color);
    background: var(--primary-50);
}

.info-card .copy-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.info-card .copied-icon {
    color: var(--success-color);
}

.info-card .address-remove:hover:not(:disabled) {
    color: var(--error-color);
    background: var(--error-light);
}

.add-address-btn {
    display: inline-flex;
    align-items: center;
    gap: 2px;
}

.add-address-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.node-address {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

.card-hint {
    margin: calc(-1 * var(--spacing-2)) 0 var(--spacing-3);
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}

/* Route mode with the ECMP badge under it: side by side they made the column wider than the table has room for */
.badge-row {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
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
    white-space: nowrap;
}

/* the tunnel column takes the room: short cells must not wrap */
.route-badge {
    white-space: nowrap;
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

/* Tunnel lines in the table: one per tunnel, with its status and BGP badges */
.tunnel-lines {
    display: flex;
    flex-direction: column;
    gap: 4px;
}

.tunnel-line {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    min-height: 22px;
    white-space: nowrap;
}

/* Below 1280 the route mode column is hidden (connectionColumns): its badges sit under the name, and the
   tunnel lines may wrap */
.route-inline {
    display: none;
}

@media (max-width: 1279px) {
    .route-inline {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
        margin-top: 4px;
    }

    .tunnel-line {
        flex-wrap: wrap;
        row-gap: 2px;
        white-space: normal;
    }
}

/* BGP session of a tunnel line: the state is in the title, the full report in the expanded row */
.bgp-chip {
    padding: 0 6px;
    cursor: default;
}

/* Expanded connection row */
.conn-panel {
    padding: var(--spacing-4) var(--spacing-5);
    cursor: default;
}

.tunnel-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-5);
}

.tunnel-card {
    padding: var(--spacing-4);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: var(--bg-primary);
}

.tunnel-card-head {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-3);
}

.tunnel-card-head h4 {
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
}

.tunnel-sub {
    margin: var(--spacing-4) 0 var(--spacing-2);
    padding-top: var(--spacing-3);
    border-top: 1px solid var(--border-light);
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
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

.error-banner {
    background: var(--error-light);
    color: var(--error-dark);
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

.checkbox-label.is-disabled {
    color: var(--text-tertiary);
    cursor: not-allowed;
}

/* Edit modal: the client VPN of an active_active gateway waits for its address */
.address-needed {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-2) var(--spacing-3);
    margin-top: var(--spacing-2);
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: var(--font-size-xs);
    line-height: 1.5;
}

.address-needed span {
    flex: 1;
    min-width: 200px;
}

.modal-intro {
    margin: 0 0 var(--spacing-4);
    font-size: var(--font-size-sm);
    line-height: 1.6;
    color: var(--text-secondary);
}

.quota-note {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
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
