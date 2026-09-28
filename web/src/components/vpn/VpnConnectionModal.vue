<script setup lang="ts">
// Create or edit a site-to-site IPsec connection of a VPN gateway.
//
// A connection has one to four tunnels (two on an active_active gateway: one per node). Each tunnel has its
// own local public address, peer address, peer identity and optionally its own pre-shared key; in BGP mode
// each tunnel also carries its own BGP session over a /30 link pair. With several tunnels the traffic
// policy says how they share the traffic: preferred (a primary, the others stand by in order) or ecmp
// (every tunnel that is up carries traffic). The rest of the form switches by route mode: static routing
// needs the remote networks, BGP needs the ASNs, the summary networks the peer may advertise and optionally
// BFD. IKE / ESP proposals, lifetimes, DPD, initiator and the local IKE identity live in a collapsed
// "advanced" section.
// psk, the tunnel keys and bgp_password are write-only: on edit an empty field keeps the stored secret.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import {
    vpnConnectionsApi,
    type VpnConnection,
    type VpnConnectionPayload,
    type VpnConnectionPatchPayload,
    type VpnDpdAction,
    type VpnEndpoint,
    type VpnPublicIp,
    type VpnRouteMode,
    type VpnTrafficPolicy,
    type VpnTunnel,
    type VpnTunnelPayload,
} from '../../api/vpn'
import { isValidName, isValidCIDRv4 } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import { endpointLabel, tunnelEndpoints } from '../../utils/vpnEndpoint'

const props = defineProps<{
    show: boolean
    gatewayId: string
    /** Edit this connection; omitted or null creates a new one */
    connection?: VpnConnection | null
    /** Public addresses of the gateway: each tunnel starts from one of its tunnel addresses */
    publicIps?: VpnPublicIp[]
    /** HA mode of the gateway: active_active runs at most one tunnel on each node */
    haMode?: string
}>()

const emit = defineEmits<{
    close: []
    /** Created or updated; the parent reloads the gateway */
    saved: [connection: VpnConnection]
}>()

const { t } = useI18n()

const IPV4 = /^(\d{1,3}\.){3}\d{1,3}$/
const BGP_PASSWORD = /^[A-Za-z0-9._-]{1,80}$/
const IKE_ID = /^[A-Za-z0-9._@:-]{1,128}$/
const ASN_MAX = 4294967295
// The most tunnels a connection has (active_standby: two local addresses times two peer addresses)
const MAX_TUNNELS = 4
const ENDPOINTS: VpnEndpoint[] = ['vip1', 'vip2', 'node1', 'node2']

// One tunnel of the form. Its pre-shared key is write-only: empty uses the connection's key on create
// and keeps the key stored for the slot on edit
interface TunnelForm {
    endpoint: VpnEndpoint
    remote_gateway: string
    remote_id: string
    psk: string
    // Edit only: remove the tunnel-specific key of this slot so it falls back to the connection's key
    clear_psk: boolean
    tunnel_local_ip: string
    tunnel_peer_ip: string
}

// Numbers are kept as number | null: null (or '' after clearing a number input) means "use the default"
interface ConnectionForm {
    name: string
    description: string
    route_mode: VpnRouteMode
    // How many of the tunnel entries are used
    tunnel_count: number
    // Only matters with more than one tunnel
    traffic_policy: VpnTrafficPolicy
    // Slot of the primary tunnel with the preferred policy; 0 lets clapi pick it (active_active: the
    // tunnel on the node that carries fewer primaries)
    primary_slot: number
    // Always MAX_TUNNELS entries in slot order; the first tunnel_count are used
    tunnels: TunnelForm[]
    local_cidrs: string
    remote_cidrs: string
    remote_summary_cidrs: string
    max_prefixes: number | null
    local_asn: number | null
    peer_asn: number | null
    bgp_password: string
    bgp_keepalive: number | null
    bgp_hold: number | null
    as_path_prepend: number | null
    bfd_enabled: boolean
    bfd_interval: number | null
    bfd_multiplier: number | null
    psk: string
    ike_proposal: string
    esp_proposal: string
    ike_lifetime: number | null
    esp_lifetime: number | null
    dpd_action: VpnDpdAction
    dpd_delay: number | null
    initiator: boolean
    local_id: string
}

const isActiveActive = computed(() => props.haMode === 'active_active')
// The addresses a tunnel can start from: vip1 / vip2, or the node addresses of an active_active gateway
const endpoints = computed(() => tunnelEndpoints(props.publicIps || [], props.haMode))
const maxTunnels = computed(() => (isActiveActive.value ? 2 : MAX_TUNNELS))
const countOptions = computed(() => Array.from({ length: maxTunnels.value }, (_, i) => i + 1))
const endpointOption = (p: VpnPublicIp) => `${endpointLabel(t, p, props.haMode, true)} · ${p.address}`
// Like clapi: the tunnels take the gateway's tunnel addresses in turn
const defaultEndpoint = (index: number): VpnEndpoint => {
    const list = endpoints.value
    if (list.length) return list[index % list.length].endpoint as VpnEndpoint
    if (isActiveActive.value) return index % 2 ? 'node2' : 'node1'
    return 'vip1'
}

const emptyTunnel = (endpoint: VpnEndpoint): TunnelForm => ({
    endpoint,
    remote_gateway: '',
    remote_id: '',
    psk: '',
    clear_psk: false,
    tunnel_local_ip: '',
    tunnel_peer_ip: '',
})

const emptyTunnels = () => Array.from({ length: MAX_TUNNELS }, (_, i) => emptyTunnel(defaultEndpoint(i)))

const emptyForm = (): ConnectionForm => ({
    name: '',
    description: '',
    route_mode: 'static',
    tunnel_count: 1,
    traffic_policy: 'preferred',
    primary_slot: isActiveActive.value ? 0 : 1,
    tunnels: emptyTunnels(),
    local_cidrs: '',
    remote_cidrs: '',
    remote_summary_cidrs: '',
    max_prefixes: null,
    local_asn: null,
    peer_asn: null,
    bgp_password: '',
    bgp_keepalive: null,
    bgp_hold: null,
    as_path_prepend: null,
    bfd_enabled: false,
    bfd_interval: null,
    bfd_multiplier: null,
    psk: '',
    ike_proposal: '',
    esp_proposal: '',
    ike_lifetime: null,
    esp_lifetime: null,
    dpd_action: 'restart',
    dpd_delay: null,
    initiator: true,
    local_id: '',
})

// Stored zero means "not set" for the optional numbers
const orNull = (v: number | undefined) => (v ? v : null)

const tunnelsBySlot = (c?: VpnConnection | null): VpnTunnel[] => [...(c?.tunnels || [])].sort((a, b) => a.slot - b.slot)

const tunnelFromApi = (tun: VpnTunnel, index: number): TunnelForm => ({
    endpoint: ENDPOINTS.includes(tun.endpoint as VpnEndpoint) ? (tun.endpoint as VpnEndpoint) : defaultEndpoint(index),
    remote_gateway: tun.remote_gateway || '',
    remote_id: tun.remote_id || '',
    psk: '',
    clear_psk: false,
    tunnel_local_ip: tun.tunnel_local_ip || '',
    tunnel_peer_ip: tun.tunnel_peer_ip || '',
})

const fromConnection = (c: VpnConnection): ConnectionForm => {
    const slots = tunnelsBySlot(c).slice(0, MAX_TUNNELS)
    // ecmp stores every tunnel as primary: the first one then becomes the primary if the policy is switched
    const primaryIndex = slots.findIndex((s) => s.priority === 'primary')
    return {
        name: c.name,
        description: c.description || '',
        route_mode: c.route_mode === 'bgp' ? 'bgp' : 'static',
        tunnel_count: Math.max(1, slots.length),
        traffic_policy: c.traffic_policy === 'ecmp' ? 'ecmp' : 'preferred',
        primary_slot: primaryIndex >= 0 ? primaryIndex + 1 : 1,
        tunnels: Array.from({ length: MAX_TUNNELS }, (_, i) =>
            slots[i] ? tunnelFromApi(slots[i], i) : emptyTunnel(defaultEndpoint(i))
        ),
        local_cidrs: c.local_cidrs || '',
        remote_cidrs: c.remote_cidrs || '',
        remote_summary_cidrs: c.remote_summary_cidrs || '',
        max_prefixes: orNull(c.max_prefixes),
        local_asn: orNull(c.local_asn),
        peer_asn: orNull(c.peer_asn),
        bgp_password: '',
        bgp_keepalive: orNull(c.bgp_keepalive),
        bgp_hold: orNull(c.bgp_hold),
        // Zero is a real value here (no prepending)
        as_path_prepend: c.as_path_prepend ?? null,
        bfd_enabled: !!c.bfd_enabled,
        bfd_interval: orNull(c.bfd_interval),
        bfd_multiplier: orNull(c.bfd_multiplier),
        psk: '',
        ike_proposal: c.ike_proposal || '',
        esp_proposal: c.esp_proposal || '',
        ike_lifetime: orNull(c.ike_lifetime),
        esp_lifetime: orNull(c.esp_lifetime),
        dpd_action: (['restart', 'clear', 'none'] as const).includes(c.dpd_action as VpnDpdAction)
            ? (c.dpd_action as VpnDpdAction)
            : 'restart',
        dpd_delay: orNull(c.dpd_delay),
        initiator: c.initiator !== false,
        local_id: c.local_id || '',
    }
}

// A deep copy: the tunnel entries are edited in place, a shallow snapshot would follow the edits
const snapshot = (f: ConnectionForm): ConnectionForm => JSON.parse(JSON.stringify(f))

const form = ref<ConnectionForm>(emptyForm())
// Snapshot taken when the modal opens; on edit only the changed fields are sent
let original: ConnectionForm = emptyForm()

const isEdit = computed(() => !!props.connection)
const saving = ref(false)
const error = ref('')
const showAdvanced = ref(false)

const isNameValid = computed(() => isValidName(form.value.name))
const activeTunnels = computed(() => form.value.tunnels.slice(0, form.value.tunnel_count))
// The traffic policy of the form as it is sent: one tunnel has nothing to share
const effectivePolicy = (f: ConnectionForm): VpnTrafficPolicy => (f.tunnel_count > 1 ? f.traffic_policy : 'preferred')
const isEcmp = computed(() => effectivePolicy(form.value) === 'ecmp')
// Primary / standby exist only with several tunnels under the preferred policy
const hasStandby = computed(() => form.value.tunnel_count > 1 && !isEcmp.value)
const isPrimary = (index: number) => form.value.primary_slot === index + 1
// The primary / standby badge of a tunnel; none while clapi picks the primary
const showRole = computed(() => hasStandby.value && form.value.primary_slot > 0)
// Edit: whether the slot currently has a tunnel-specific key
const storedPskSet = (index: number) => !!tunnelsBySlot(props.connection)[index]?.psk_set
const tunnelCountHint = computed(() =>
    isActiveActive.value
        ? t('dashboard.vpnGateway.tunnelCountHintActiveActive')
        : t('dashboard.vpnGateway.tunnelCountHint')
)
// Example /30 link pair of tunnel i: 169.254.10.1/.2, .5/.6, .9/.10, .13/.14
const linkExample = (index: number, peer: boolean) => `169.254.10.${index * 4 + (peer ? 2 : 1)}`

watch(
    () => props.show,
    (show) => {
        if (!show) return
        error.value = ''
        showAdvanced.value = false
        form.value = props.connection ? fromConnection(props.connection) : emptyForm()
        original = snapshot(form.value)
    }
)

// Fewer tunnels than the chosen primary: the first one becomes the primary
watch(
    () => form.value.tunnel_count,
    (count) => {
        if (form.value.primary_slot > count) form.value.primary_slot = 1
    }
)

// v-model.number yields '' once a number input is cleared
const num = (v: number | null): number | null => ((v as unknown) === '' || v === null ? null : v)

const cidrListValid = (value: string) =>
    value
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
        .every(isValidCIDRv4)

const inRange = (v: number | null, min: number, max: number) =>
    v === null || (Number.isInteger(v) && v >= min && v <= max)

const pskValid = (psk: string) => psk.length >= 8 && psk.length <= 128 && !/[^\x21-\x7e]|["'\\]/.test(psk)

// The /30 network of an IPv4 address: the BGP link pairs of the tunnels must not share one
const slash30 = (ip: string) => {
    const parts = ip.trim().split('.').map(Number)
    return `${parts[0]}.${parts[1]}.${parts[2]}.${parts[3] & 252}`
}

const allDifferent = (values: string[]) => new Set(values).size === values.length

// The tunnel rules clapi applies, checked here so the message is translated
const validateTunnels = (f: ConnectionForm): string | null => {
    const tunnels = f.tunnels.slice(0, f.tunnel_count)
    for (const tun of tunnels) {
        const peer = tun.remote_gateway.trim()
        const remoteId = tun.remote_id.trim()
        if (peer && !IPV4.test(peer)) return t('dashboard.vpnGateway.invalidIp')
        if (remoteId && !IKE_ID.test(remoteId)) return t('dashboard.vpnGateway.invalidIkeId')
        if (!peer) {
            // Without a peer address the tunnel waits for the peer, which must then be told apart by its identity
            if (!remoteId) return t('dashboard.vpnGateway.remoteIdRequired')
            if (f.initiator) return t('dashboard.vpnGateway.responderNotInitiator')
        }
        if (!tun.clear_psk && tun.psk && !pskValid(tun.psk)) return t('dashboard.vpnGateway.invalidPsk')
        if (
            f.route_mode === 'bgp' &&
            (!IPV4.test(tun.tunnel_local_ip.trim()) || !IPV4.test(tun.tunnel_peer_ip.trim()))
        ) {
            return t('dashboard.vpnGateway.invalidTunnelIp')
        }
    }
    if (tunnels.length < 2) return null
    // active_active: one tunnel per node
    if (isActiveActive.value && !allDifferent(tunnels.map((tun) => tun.endpoint))) {
        return t('dashboard.vpnGateway.tunnelsOnDifferentNodes')
    }
    if (!allDifferent(tunnels.map((tun) => `${tun.endpoint}|${tun.remote_gateway.trim()}`))) {
        return t('dashboard.vpnGateway.tunnelsMustDiffer')
    }
    if (
        f.route_mode === 'bgp' &&
        (!allDifferent(tunnels.map((tun) => slash30(tun.tunnel_local_ip))) ||
            !allDifferent(tunnels.map((tun) => tun.tunnel_peer_ip.trim())))
    ) {
        return t('dashboard.vpnGateway.tunnelLinksMustDiffer')
    }
    return null
}

const validate = (): string | null => {
    const f = form.value
    if (!f.name || !isNameValid.value) return t('messages.invalidHostname')
    const tunnelProblem = validateTunnels(f)
    if (tunnelProblem) return tunnelProblem
    if (f.local_cidrs && !cidrListValid(f.local_cidrs)) return t('dashboard.vpnGateway.invalidCidrList')
    if (f.route_mode === 'static') {
        if (!f.remote_cidrs.trim() || !cidrListValid(f.remote_cidrs))
            return t('dashboard.vpnGateway.remoteCidrsRequired')
    } else {
        if (!num(f.local_asn) || !num(f.peer_asn)) return t('dashboard.vpnGateway.asnRequired')
        if (!inRange(num(f.local_asn), 1, ASN_MAX) || !inRange(num(f.peer_asn), 1, ASN_MAX)) {
            return t('dashboard.vpnGateway.invalidAsn')
        }
        if (!f.remote_summary_cidrs.trim() || !cidrListValid(f.remote_summary_cidrs)) {
            return t('dashboard.vpnGateway.remoteSummaryRequired')
        }
        if (f.bgp_password && !BGP_PASSWORD.test(f.bgp_password)) return t('dashboard.vpnGateway.invalidBgpPassword')
        if (!inRange(num(f.max_prefixes), 1, 100000))
            return t('dashboard.vpnGateway.invalidRange', { min: 1, max: 100000 })
        if (!inRange(num(f.bgp_keepalive), 1, 3600))
            return t('dashboard.vpnGateway.invalidRange', { min: 1, max: 3600 })
        if (!inRange(num(f.bgp_hold), 3, 10800)) return t('dashboard.vpnGateway.invalidRange', { min: 3, max: 10800 })
        if (f.bfd_enabled) {
            if (!inRange(num(f.bfd_interval), 300, 60000))
                return t('dashboard.vpnGateway.invalidRange', { min: 300, max: 60000 })
            if (!inRange(num(f.bfd_multiplier), 2, 50))
                return t('dashboard.vpnGateway.invalidRange', { min: 2, max: 50 })
        }
        if (hasStandby.value && !inRange(num(f.as_path_prepend), 0, 10))
            return t('dashboard.vpnGateway.invalidRange', { min: 0, max: 10 })
    }
    if (!isEdit.value && !f.psk) return t('dashboard.vpnGateway.pskRequired')
    if (f.psk && !pskValid(f.psk)) return t('dashboard.vpnGateway.invalidPsk')
    if (!inRange(num(f.ike_lifetime), 300, 604800))
        return t('dashboard.vpnGateway.invalidRange', { min: 300, max: 604800 })
    if (!inRange(num(f.esp_lifetime), 300, 86400))
        return t('dashboard.vpnGateway.invalidRange', { min: 300, max: 86400 })
    if (!inRange(num(f.dpd_delay), 5, 3600)) return t('dashboard.vpnGateway.invalidRange', { min: 5, max: 3600 })
    if (f.local_id && !IKE_ID.test(f.local_id)) return t('dashboard.vpnGateway.invalidIkeId')
    return null
}

// The tunnel list as the API takes it, in slot order with an explicit endpoint. The priority is left out
// with ecmp (clapi makes every tunnel primary) and when clapi picks the primary (primary_slot 0). A typed
// key is sent; clear_psk sends an empty key (fall back to the connection's); otherwise no key, which keeps
// the key stored for the slot on update
const tunnelsPayload = (f: ConnectionForm): VpnTunnelPayload[] => {
    const multi = f.tunnel_count > 1
    const ecmp = effectivePolicy(f) === 'ecmp'
    return f.tunnels.slice(0, f.tunnel_count).map((tun, i) => {
        const entry: VpnTunnelPayload = { endpoint: tun.endpoint }
        if (!multi) entry.priority = 'primary'
        else if (!ecmp && f.primary_slot > 0) entry.priority = f.primary_slot === i + 1 ? 'primary' : 'standby'
        if (tun.remote_gateway.trim()) entry.remote_gateway = tun.remote_gateway.trim()
        if (tun.remote_id.trim()) entry.remote_id = tun.remote_id.trim()
        if (f.route_mode === 'bgp') {
            entry.tunnel_local_ip = tun.tunnel_local_ip.trim()
            entry.tunnel_peer_ip = tun.tunnel_peer_ip.trim()
        }
        if (tun.clear_psk) entry.psk = ''
        else if (tun.psk) entry.psk = tun.psk
        return entry
    })
}

// Everything the form holds, as the API payload (empty strings and null numbers left out)
const toPayload = (f: ConnectionForm): VpnConnectionPayload => {
    // Assembled as a plain record so the optional keys can be filled by name
    const p: Record<string, unknown> = {
        name: f.name,
        route_mode: f.route_mode,
        initiator: f.initiator,
        dpd_action: f.dpd_action,
        traffic_policy: effectivePolicy(f),
        tunnels: tunnelsPayload(f),
    }
    const str = (key: keyof VpnConnectionPayload, value: string) => {
        if (value.trim()) p[key] = value.trim()
    }
    const int = (key: keyof VpnConnectionPayload, value: number | null) => {
        const v = num(value)
        if (v !== null) p[key] = v
    }
    str('description', f.description)
    str('local_cidrs', f.local_cidrs)
    str('ike_proposal', f.ike_proposal)
    str('esp_proposal', f.esp_proposal)
    str('local_id', f.local_id)
    int('ike_lifetime', f.ike_lifetime)
    int('esp_lifetime', f.esp_lifetime)
    int('dpd_delay', f.dpd_delay)
    if (f.route_mode === 'static') {
        str('remote_cidrs', f.remote_cidrs)
    } else {
        str('remote_summary_cidrs', f.remote_summary_cidrs)
        int('local_asn', f.local_asn)
        int('peer_asn', f.peer_asn)
        int('max_prefixes', f.max_prefixes)
        int('bgp_keepalive', f.bgp_keepalive)
        int('bgp_hold', f.bgp_hold)
        p.bfd_enabled = f.bfd_enabled
        if (f.bfd_enabled) {
            int('bfd_interval', f.bfd_interval)
            int('bfd_multiplier', f.bfd_multiplier)
        }
        // The standby tunnels advertise with the prepended path; ecmp has no standby
        if (f.tunnel_count > 1 && effectivePolicy(f) === 'preferred') int('as_path_prepend', f.as_path_prepend)
    }
    return p as unknown as VpnConnectionPayload
}

// Fields whose value differs from the snapshot; description and the comma lists may legitimately be
// cleared, so those are compared as strings and sent even when empty
const CLEARABLE: Array<keyof ConnectionForm> = [
    'description',
    'local_cidrs',
    'remote_cidrs',
    'remote_summary_cidrs',
    'ike_proposal',
    'esp_proposal',
    'local_id',
]

// Optional numbers: a cleared input cannot unset the stored value (an omitted field keeps it), so
// only a new non-empty value is sent
const NUMERIC: Array<keyof ConnectionForm> = [
    'max_prefixes',
    'local_asn',
    'peer_asn',
    'bgp_keepalive',
    'bgp_hold',
    'as_path_prepend',
    'bfd_interval',
    'bfd_multiplier',
    'ike_lifetime',
    'esp_lifetime',
    'dpd_delay',
]

// Secrets are added by submit; the tunnel fields and the traffic policy are compared below
const SKIPPED: Array<keyof ConnectionForm> = [
    'psk',
    'bgp_password',
    'tunnel_count',
    'traffic_policy',
    'primary_slot',
    'tunnels',
]

const MODE_FIELDS = [
    'remote_cidrs',
    'remote_summary_cidrs',
    'local_asn',
    'peer_asn',
    'max_prefixes',
    'bgp_keepalive',
    'bgp_hold',
    'bfd_enabled',
    'bfd_interval',
    'bfd_multiplier',
    'as_path_prepend',
] as const

const toPatch = (f: ConnectionForm): VpnConnectionPatchPayload => {
    const full = toPayload(f) as unknown as Record<string, unknown>
    const patch: Record<string, unknown> = {}
    for (const key of Object.keys(f) as Array<keyof ConnectionForm>) {
        if (SKIPPED.includes(key)) continue
        const before = original[key]
        const after = f[key]
        if (NUMERIC.includes(key)) {
            const value = num(after as number | null)
            if (value !== num(before as number | null) && value !== null) patch[key] = value
        } else if (after !== before) {
            if (CLEARABLE.includes(key)) patch[key] = String(after).trim()
            else if (key in full) patch[key] = full[key]
        }
    }
    // Switching the mode must carry the fields of the new mode even when they were typed before opening
    if (f.route_mode !== original.route_mode) {
        for (const key of MODE_FIELDS) {
            if (key in full) patch[key] = full[key]
        }
    }
    if (effectivePolicy(f) !== effectivePolicy(original)) patch.traffic_policy = effectivePolicy(f)
    // tunnels replaces the stored list slot by slot, so it goes as a whole once anything in it changed
    // (a new tunnel key, a cleared one, the primary, the count, the policy, or the link pairs of a new
    // route mode)
    const tunnels = tunnelsPayload(f)
    if (f.route_mode !== original.route_mode || JSON.stringify(tunnels) !== JSON.stringify(tunnelsPayload(original))) {
        patch.tunnels = tunnels
    }
    return patch as VpnConnectionPatchPayload
}

const submit = async () => {
    const problem = validate()
    if (problem) {
        error.value = problem
        return
    }
    saving.value = true
    error.value = ''
    try {
        let saved: VpnConnection
        if (props.connection) {
            const patch = toPatch(form.value)
            if (form.value.psk) patch.psk = form.value.psk
            if (form.value.bgp_password) patch.bgp_password = form.value.bgp_password
            saved = await vpnConnectionsApi.update(props.gatewayId, props.connection.id, patch)
        } else {
            const payload = toPayload(form.value)
            payload.psk = form.value.psk
            if (form.value.bgp_password) payload.bgp_password = form.value.bgp_password
            saved = await vpnConnectionsApi.create(props.gatewayId, payload)
        }
        emit('saved', saved)
        emit('close')
    } catch (err) {
        error.value = errorMessage(err, t('messages.error'))
    } finally {
        saving.value = false
    }
}

const close = () => {
    if (saving.value) return
    emit('close')
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="isEdit ? t('dashboard.vpnGateway.editConnection') : t('dashboard.vpnGateway.addConnection')"
        :loading="saving"
        size="lg"
        form
        @close="close"
        @submit="submit"
    >
        <div class="form-group">
            <label class="form-label">{{ t('dashboard.forms.name') }} *</label>
            <input
                v-model="form.name"
                type="text"
                :class="['form-input', { 'input-error': !isNameValid }]"
                :placeholder="t('dashboard.forms.placeholder.vpnConnectionNameExample')"
            />
            <div v-if="!isNameValid" class="text-error form-hint">{{ t('messages.invalidHostname') }}</div>
        </div>

        <div class="form-group">
            <label class="form-label"
                >{{ t('dashboard.forms.description') }}（{{ t('dashboard.forms.optional') }}）</label
            >
            <input
                v-model="form.description"
                type="text"
                class="form-input"
                :placeholder="t('messages.placeholderDescription')"
            />
        </div>

        <div class="form-row">
            <div class="form-group form-group-grow">
                <label class="form-label">{{ t('dashboard.vpnGateway.psk') }}{{ isEdit ? '' : ' *' }}</label>
                <input
                    v-model="form.psk"
                    type="password"
                    class="form-input mono"
                    autocomplete="new-password"
                    :placeholder="isEdit && connection?.psk_set ? '••••••••' : ''"
                />
                <div class="form-hint">
                    {{ isEdit ? t('dashboard.vpnGateway.pskKeepHint') : t('dashboard.vpnGateway.pskHint') }}
                </div>
            </div>
            <div class="form-group form-group-fixed">
                <label class="form-label">{{ t('dashboard.vpnGateway.routeMode') }}</label>
                <div class="select-wrapper">
                    <select v-model="form.route_mode" class="form-input">
                        <option value="static">{{ t('dashboard.vpnGateway.routeModeStatic') }}</option>
                        <option value="bgp">{{ t('dashboard.vpnGateway.routeModeBgp') }}</option>
                    </select>
                </div>
            </div>
        </div>

        <!-- Tunnels: how many, and how they share the traffic -->
        <div class="form-group">
            <label class="form-label">{{ t('dashboard.vpnGateway.tunnelCount') }}</label>
            <div class="radio-group">
                <label v-for="n in countOptions" :key="n" class="radio-label">
                    <input v-model="form.tunnel_count" type="radio" :value="n" />
                    {{ n }}
                </label>
            </div>
            <div class="form-hint">{{ tunnelCountHint }}</div>
        </div>

        <div v-if="form.tunnel_count > 1" class="form-group">
            <label class="form-label">{{ t('dashboard.vpnGateway.trafficPolicy') }}</label>
            <div class="radio-group">
                <label class="radio-label">
                    <input v-model="form.traffic_policy" type="radio" value="preferred" />
                    {{ t('dashboard.vpnGateway.trafficPolicyPreferred') }}
                </label>
                <label class="radio-label">
                    <input v-model="form.traffic_policy" type="radio" value="ecmp" />
                    {{ t('dashboard.vpnGateway.trafficPolicyEcmp') }}
                </label>
            </div>
            <div class="form-hint">
                {{
                    isEcmp
                        ? t('dashboard.vpnGateway.trafficPolicyEcmpHint')
                        : t('dashboard.vpnGateway.trafficPolicyPreferredHint')
                }}
            </div>
        </div>

        <!-- ecmp has no primary: every tunnel that is up carries traffic -->
        <div v-if="hasStandby" class="form-group">
            <label class="form-label">{{ t('dashboard.vpnGateway.primaryTunnel') }}</label>
            <div class="radio-group">
                <label v-if="isActiveActive" class="radio-label">
                    <input v-model="form.primary_slot" type="radio" :value="0" />
                    {{ t('dashboard.vpnGateway.primaryAuto') }}
                </label>
                <label v-for="n in form.tunnel_count" :key="n" class="radio-label">
                    <input v-model="form.primary_slot" type="radio" :value="n" />
                    {{ t('dashboard.vpnGateway.tunnelN', { n }) }}
                </label>
            </div>
        </div>

        <div v-if="hasStandby && form.route_mode === 'static'" class="mode-notice">
            {{ t('dashboard.vpnGateway.staticDualHint') }}
        </div>

        <div v-for="(tun, i) in activeTunnels" :key="i" class="tunnel-block">
            <div class="tunnel-block-head">
                <span class="tunnel-title">{{ t('dashboard.vpnGateway.tunnelN', { n: i + 1 }) }}</span>
                <span v-if="showRole" class="badge" :class="isPrimary(i) ? 'badge-primary' : 'badge-secondary'">{{
                    isPrimary(i) ? t('dashboard.vpnGateway.priorityPrimary') : t('dashboard.vpnGateway.priorityStandby')
                }}</span>
            </div>

            <div class="form-row">
                <!-- The local address: vip1 / vip2, or the node (and its address) of an active_active gateway -->
                <div v-if="endpoints.length > 1" class="form-group form-group-grow">
                    <label class="form-label">{{
                        isActiveActive ? t('dashboard.vpnGateway.node') : t('dashboard.vpnGateway.localAddress')
                    }}</label>
                    <div class="select-wrapper">
                        <select v-model="tun.endpoint" class="form-input">
                            <option v-for="p in endpoints" :key="p.endpoint" :value="p.endpoint">
                                {{ endpointOption(p) }}
                            </option>
                        </select>
                    </div>
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.peerAddress') }}</label>
                    <input
                        v-model="tun.remote_gateway"
                        type="text"
                        class="form-input mono"
                        :placeholder="`198.51.100.${i + 1}`"
                    />
                    <div class="form-hint">{{ t('dashboard.vpnGateway.remoteGatewayHint') }}</div>
                </div>
            </div>

            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.remoteId') }}</label>
                    <input
                        v-model="tun.remote_id"
                        type="text"
                        class="form-input mono"
                        :placeholder="t('dashboard.forms.placeholder.domainExample')"
                    />
                    <div class="form-hint">{{ t('dashboard.vpnGateway.peerIdHint') }}</div>
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.tunnelPsk') }}</label>
                    <input
                        v-model="tun.psk"
                        type="password"
                        class="form-input mono"
                        autocomplete="new-password"
                        :disabled="tun.clear_psk"
                        :placeholder="
                            isEdit && storedPskSet(i)
                                ? t('dashboard.vpnGateway.tunnelPskKeepPlaceholder')
                                : t('dashboard.vpnGateway.tunnelPskPlaceholder')
                        "
                    />
                    <label v-if="isEdit && storedPskSet(i)" class="checkbox-label checkbox-small">
                        <input v-model="tun.clear_psk" type="checkbox" />
                        {{ t('dashboard.vpnGateway.tunnelPskClear') }}
                    </label>
                </div>
            </div>

            <!-- BGP: the session of this tunnel runs over its own /30 pair -->
            <template v-if="form.route_mode === 'bgp'">
                <div class="form-row">
                    <div class="form-group form-group-grow">
                        <label class="form-label">{{ t('dashboard.vpnGateway.tunnelLocalIp') }} *</label>
                        <input
                            v-model="tun.tunnel_local_ip"
                            type="text"
                            class="form-input mono"
                            :placeholder="linkExample(i, false)"
                        />
                    </div>
                    <div class="form-group form-group-grow">
                        <label class="form-label">{{ t('dashboard.vpnGateway.tunnelPeerIp') }} *</label>
                        <input
                            v-model="tun.tunnel_peer_ip"
                            type="text"
                            class="form-input mono"
                            :placeholder="linkExample(i, true)"
                        />
                    </div>
                </div>
                <div class="form-hint form-hint-block">
                    {{
                        i === 0
                            ? t('dashboard.vpnGateway.tunnelIpHint')
                            : t('dashboard.vpnGateway.tunnelIpHint2', {
                                  local: linkExample(i, false),
                                  peer: linkExample(i, true),
                              })
                    }}
                </div>
            </template>
        </div>

        <div class="form-group">
            <label class="form-label">{{ t('dashboard.vpnGateway.localCidrs') }}</label>
            <input
                v-model="form.local_cidrs"
                type="text"
                class="form-input mono"
                placeholder="10.0.1.0/24, 10.0.2.0/24"
            />
            <div class="form-hint">{{ t('dashboard.vpnGateway.localCidrsHint') }}</div>
        </div>

        <!-- Static routing -->
        <div v-if="form.route_mode === 'static'" class="form-group">
            <label class="form-label">{{ t('dashboard.vpnGateway.remoteCidrs') }} *</label>
            <input
                v-model="form.remote_cidrs"
                type="text"
                class="form-input mono"
                placeholder="192.168.10.0/24, 192.168.20.0/24"
            />
            <div class="form-hint">{{ t('dashboard.vpnGateway.remoteCidrsHint') }}</div>
        </div>

        <!-- BGP routing -->
        <template v-else>
            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.localAsn') }} *</label>
                    <input
                        v-model.number="form.local_asn"
                        type="number"
                        min="1"
                        :max="ASN_MAX"
                        class="form-input"
                        placeholder="65001"
                    />
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.peerAsn') }} *</label>
                    <input
                        v-model.number="form.peer_asn"
                        type="number"
                        min="1"
                        :max="ASN_MAX"
                        class="form-input"
                        placeholder="65002"
                    />
                </div>
            </div>

            <div class="form-group">
                <label class="form-label">{{ t('dashboard.vpnGateway.remoteSummaryCidrs') }} *</label>
                <input
                    v-model="form.remote_summary_cidrs"
                    type="text"
                    class="form-input mono"
                    placeholder="192.168.0.0/16"
                />
                <div class="form-hint">{{ t('dashboard.vpnGateway.remoteSummaryCidrsHint') }}</div>
            </div>

            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.maxPrefixes') }}</label>
                    <input
                        v-model.number="form.max_prefixes"
                        type="number"
                        min="1"
                        max="100000"
                        class="form-input"
                        :placeholder="t('dashboard.vpnGateway.defaultHint')"
                    />
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.bgpPassword') }}</label>
                    <input
                        v-model="form.bgp_password"
                        type="password"
                        class="form-input mono"
                        autocomplete="new-password"
                        :placeholder="isEdit && connection?.bgp_password_set ? '••••••••' : ''"
                    />
                    <div class="form-hint">
                        {{ isEdit ? t('dashboard.vpnGateway.keepCurrent') : t('dashboard.vpnGateway.bgpPasswordHint') }}
                    </div>
                </div>
            </div>

            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.bgpKeepalive') }}</label>
                    <div class="input-with-suffix">
                        <input
                            v-model.number="form.bgp_keepalive"
                            type="number"
                            min="1"
                            max="3600"
                            class="form-input"
                            :placeholder="t('dashboard.vpnGateway.defaultHint')"
                        />
                        <span class="input-suffix">{{ t('dashboard.vpnGateway.seconds') }}</span>
                    </div>
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.bgpHold') }}</label>
                    <div class="input-with-suffix">
                        <input
                            v-model.number="form.bgp_hold"
                            type="number"
                            min="3"
                            max="10800"
                            class="form-input"
                            :placeholder="t('dashboard.vpnGateway.defaultHint')"
                        />
                        <span class="input-suffix">{{ t('dashboard.vpnGateway.seconds') }}</span>
                    </div>
                </div>
            </div>

            <!-- Standby tunnels advertise with the local ASN repeated so the peer prefers the primary -->
            <div v-if="hasStandby" class="form-group">
                <label class="form-label">{{ t('dashboard.vpnGateway.asPathPrepend') }}</label>
                <input
                    v-model.number="form.as_path_prepend"
                    type="number"
                    min="0"
                    max="10"
                    class="form-input input-narrow"
                    :placeholder="t('dashboard.vpnGateway.defaultHint')"
                />
                <div class="form-hint">{{ t('dashboard.vpnGateway.asPathPrependHint') }}</div>
            </div>

            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="form.bfd_enabled" type="checkbox" />
                    {{ t('dashboard.vpnGateway.bfdEnabled') }}
                </label>
                <div class="form-hint">{{ t('dashboard.vpnGateway.bfdHint') }}</div>
            </div>
            <div v-if="form.bfd_enabled" class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.bfdInterval') }}</label>
                    <div class="input-with-suffix">
                        <input
                            v-model.number="form.bfd_interval"
                            type="number"
                            min="300"
                            max="60000"
                            class="form-input"
                            :placeholder="t('dashboard.vpnGateway.defaultHint')"
                        />
                        <span class="input-suffix">{{ t('dashboard.vpnGateway.milliseconds') }}</span>
                    </div>
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.bfdMultiplier') }}</label>
                    <input
                        v-model.number="form.bfd_multiplier"
                        type="number"
                        min="2"
                        max="50"
                        class="form-input"
                        :placeholder="t('dashboard.vpnGateway.defaultHint')"
                    />
                </div>
            </div>
        </template>

        <!-- Advanced: proposals, lifetimes, DPD, initiator, local IKE identity -->
        <button type="button" class="advanced-toggle" @click="showAdvanced = !showAdvanced">
            <component :is="showAdvanced ? ChevronDown : ChevronRight" :size="14" />
            {{ t('dashboard.vpnGateway.advanced') }}
        </button>
        <div v-if="showAdvanced" class="advanced-section">
            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.ikeProposal') }}</label>
                    <input
                        v-model="form.ike_proposal"
                        type="text"
                        class="form-input mono"
                        placeholder="aes256-sha256-modp2048"
                    />
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.espProposal') }}</label>
                    <input
                        v-model="form.esp_proposal"
                        type="text"
                        class="form-input mono"
                        placeholder="aes256-sha256-modp2048"
                    />
                </div>
            </div>
            <div class="form-hint form-hint-block">{{ t('dashboard.vpnGateway.proposalHint') }}</div>

            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.ikeLifetime') }}</label>
                    <div class="input-with-suffix">
                        <input
                            v-model.number="form.ike_lifetime"
                            type="number"
                            min="300"
                            max="604800"
                            class="form-input"
                            :placeholder="t('dashboard.vpnGateway.defaultHint')"
                        />
                        <span class="input-suffix">{{ t('dashboard.vpnGateway.seconds') }}</span>
                    </div>
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.espLifetime') }}</label>
                    <div class="input-with-suffix">
                        <input
                            v-model.number="form.esp_lifetime"
                            type="number"
                            min="300"
                            max="86400"
                            class="form-input"
                            :placeholder="t('dashboard.vpnGateway.defaultHint')"
                        />
                        <span class="input-suffix">{{ t('dashboard.vpnGateway.seconds') }}</span>
                    </div>
                </div>
            </div>

            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.dpdAction') }}</label>
                    <div class="select-wrapper">
                        <select v-model="form.dpd_action" class="form-input">
                            <option value="restart">{{ t('dashboard.vpnGateway.dpdActionRestart') }}</option>
                            <option value="clear">{{ t('dashboard.vpnGateway.dpdActionClear') }}</option>
                            <option value="none">{{ t('dashboard.vpnGateway.dpdActionNone') }}</option>
                        </select>
                    </div>
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.dpdDelay') }}</label>
                    <div class="input-with-suffix">
                        <input
                            v-model.number="form.dpd_delay"
                            type="number"
                            min="5"
                            max="3600"
                            class="form-input"
                            :placeholder="t('dashboard.vpnGateway.defaultHint')"
                        />
                        <span class="input-suffix">{{ t('dashboard.vpnGateway.seconds') }}</span>
                    </div>
                </div>
            </div>

            <div class="form-group">
                <label class="form-label">{{ t('dashboard.vpnGateway.localId') }}</label>
                <input
                    v-model="form.local_id"
                    type="text"
                    class="form-input mono"
                    :placeholder="t('dashboard.forms.placeholder.domainExample')"
                />
                <div class="form-hint">{{ t('dashboard.vpnGateway.localIdHint') }}</div>
            </div>

            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="form.initiator" type="checkbox" />
                    {{ t('dashboard.vpnGateway.initiator') }}
                </label>
                <div class="form-hint">{{ t('dashboard.vpnGateway.initiatorHint') }}</div>
            </div>
        </div>

        <template #footer>
            <div v-if="error" class="modal-error footer-error">{{ error }}</div>
            <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.name">
                <span v-if="saving" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px"></span>
                <template v-if="isEdit">{{ saving ? t('messages.saving') : t('actions.save') }}</template>
                <template v-else>{{
                    saving ? t('messages.creating') : t('dashboard.vpnGateway.addConnection')
                }}</template>
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
/* The error sits in the footer next to the buttons: in the body it ended up below the fold of the long
   forms and a failed submit looked like nothing happened */
.modal-error.footer-error {
    flex: 1;
    align-self: center;
    margin: 0;
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
    width: 160px;
    flex-shrink: 0;
}

/* Phones: the paired fields one above the other */
@media (max-width: 560px) {
    .form-row {
        flex-direction: column;
        gap: 0;
    }

    .form-group-fixed {
        width: auto;
    }

    .tunnel-block {
        padding: var(--spacing-3) var(--spacing-3) 0;
    }
}

.form-hint {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 4px;
}

/* A hint that belongs to a whole row rather than one field: pull it up under the row */
.form-hint-block {
    margin-top: calc(-1 * var(--spacing-3));
    margin-bottom: var(--spacing-4);
}

.mono {
    font-family: var(--font-family-mono);
}

.checkbox-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
}

/* The "remove the tunnel key" checkbox under the key input */
.checkbox-small {
    margin-top: 6px;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.radio-group {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-5);
}

.radio-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
}

.input-narrow {
    max-width: 200px;
}

/* One box per tunnel */
.tunnel-block {
    margin-bottom: var(--spacing-4);
    padding: var(--spacing-4) var(--spacing-4) 0;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
}

.tunnel-block-head {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-3);
}

.tunnel-title {
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
}

.mode-notice {
    margin-bottom: var(--spacing-4);
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: var(--font-size-xs);
    line-height: 1.6;
}

.input-with-suffix {
    position: relative;
    display: flex;
    align-items: center;
}

.input-with-suffix .form-input {
    padding-right: 52px;
}

.input-suffix {
    position: absolute;
    right: 12px;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    pointer-events: none;
}

.advanced-toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-1);
    margin: var(--spacing-2) 0 var(--spacing-3);
    padding: 0;
    border: none;
    background: none;
    color: var(--primary-600);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
}

.advanced-toggle:hover {
    text-decoration: underline;
}

.advanced-section {
    padding: var(--spacing-4);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
}

.modal-error {
    margin-top: var(--spacing-4);
    font-size: var(--font-size-sm);
    color: var(--error-color);
    background: var(--error-light);
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-sm);
}
</style>
