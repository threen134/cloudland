<script setup lang="ts">
// Create or edit a site-to-site IPsec connection of a VPN gateway.
//
// The form switches by route mode: static routing needs the remote networks, BGP needs the ASNs,
// the tunnel /30 pair and the summary networks the peer may advertise. IKE / ESP proposals,
// lifetimes, DPD, initiator and IKE identities live in a collapsed "advanced" section.
// psk and bgp_password are write-only: on edit an empty field keeps the stored secret.
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
    type VpnRouteMode,
} from '../../api/vpn'
import { isValidName, isValidCIDRv4 } from '../../utils/validation'
import { errorMessage } from '../../utils/error'

const props = defineProps<{
    show: boolean
    gatewayId: string
    /** Edit this connection; omitted or null creates a new one */
    connection?: VpnConnection | null
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

// Numbers are kept as number | null: null (or '' after clearing a number input) means "use the default"
interface ConnectionForm {
    name: string
    description: string
    remote_gateway: string
    route_mode: VpnRouteMode
    local_cidrs: string
    remote_cidrs: string
    remote_summary_cidrs: string
    max_prefixes: number | null
    local_asn: number | null
    peer_asn: number | null
    tunnel_local_ip: string
    tunnel_peer_ip: string
    bgp_password: string
    bgp_keepalive: number | null
    bgp_hold: number | null
    psk: string
    ike_proposal: string
    esp_proposal: string
    ike_lifetime: number | null
    esp_lifetime: number | null
    dpd_action: VpnDpdAction
    dpd_delay: number | null
    initiator: boolean
    local_id: string
    remote_id: string
}

const emptyForm = (): ConnectionForm => ({
    name: '',
    description: '',
    remote_gateway: '',
    route_mode: 'static',
    local_cidrs: '',
    remote_cidrs: '',
    remote_summary_cidrs: '',
    max_prefixes: null,
    local_asn: null,
    peer_asn: null,
    tunnel_local_ip: '',
    tunnel_peer_ip: '',
    bgp_password: '',
    bgp_keepalive: null,
    bgp_hold: null,
    psk: '',
    ike_proposal: '',
    esp_proposal: '',
    ike_lifetime: null,
    esp_lifetime: null,
    dpd_action: 'restart',
    dpd_delay: null,
    initiator: true,
    local_id: '',
    remote_id: '',
})

// Stored zero means "not set" for the optional numbers
const orNull = (v: number | undefined) => (v ? v : null)

const fromConnection = (c: VpnConnection): ConnectionForm => ({
    name: c.name,
    description: c.description || '',
    remote_gateway: c.remote_gateway || '',
    route_mode: c.route_mode === 'bgp' ? 'bgp' : 'static',
    local_cidrs: c.local_cidrs || '',
    remote_cidrs: c.remote_cidrs || '',
    remote_summary_cidrs: c.remote_summary_cidrs || '',
    max_prefixes: orNull(c.max_prefixes),
    local_asn: orNull(c.local_asn),
    peer_asn: orNull(c.peer_asn),
    tunnel_local_ip: c.tunnel_local_ip || '',
    tunnel_peer_ip: c.tunnel_peer_ip || '',
    bgp_password: '',
    bgp_keepalive: orNull(c.bgp_keepalive),
    bgp_hold: orNull(c.bgp_hold),
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
    remote_id: c.remote_id || '',
})

const form = ref<ConnectionForm>(emptyForm())
// Snapshot taken when the modal opens; on edit only the changed fields are sent
let original: ConnectionForm = emptyForm()

const isEdit = computed(() => !!props.connection)
const saving = ref(false)
const error = ref('')
const showAdvanced = ref(false)

const isNameValid = computed(() => isValidName(form.value.name))

watch(
    () => props.show,
    (show) => {
        if (!show) return
        error.value = ''
        showAdvanced.value = false
        form.value = props.connection ? fromConnection(props.connection) : emptyForm()
        original = { ...form.value }
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

const validate = (): string | null => {
    const f = form.value
    if (!f.name || !isNameValid.value) return t('messages.invalidHostname')
    if (f.remote_gateway && !IPV4.test(f.remote_gateway.trim())) return t('dashboard.vpnGateway.invalidIp')
    if (f.local_cidrs && !cidrListValid(f.local_cidrs)) return t('dashboard.vpnGateway.invalidCidrList')
    if (f.route_mode === 'static') {
        if (!f.remote_cidrs.trim() || !cidrListValid(f.remote_cidrs))
            return t('dashboard.vpnGateway.remoteCidrsRequired')
    } else {
        if (!num(f.local_asn) || !num(f.peer_asn)) return t('dashboard.vpnGateway.asnRequired')
        if (!inRange(num(f.local_asn), 1, ASN_MAX) || !inRange(num(f.peer_asn), 1, ASN_MAX)) {
            return t('dashboard.vpnGateway.invalidAsn')
        }
        if (!IPV4.test(f.tunnel_local_ip.trim()) || !IPV4.test(f.tunnel_peer_ip.trim())) {
            return t('dashboard.vpnGateway.invalidTunnelIp')
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
    }
    if (!isEdit.value && !f.psk) return t('dashboard.vpnGateway.pskRequired')
    if (f.psk && (f.psk.length < 8 || f.psk.length > 128 || /[^\x21-\x7e]|["'\\]/.test(f.psk))) {
        return t('dashboard.vpnGateway.invalidPsk')
    }
    if (!inRange(num(f.ike_lifetime), 300, 604800))
        return t('dashboard.vpnGateway.invalidRange', { min: 300, max: 604800 })
    if (!inRange(num(f.esp_lifetime), 300, 86400))
        return t('dashboard.vpnGateway.invalidRange', { min: 300, max: 86400 })
    if (!inRange(num(f.dpd_delay), 5, 3600)) return t('dashboard.vpnGateway.invalidRange', { min: 5, max: 3600 })
    if (f.local_id && !IKE_ID.test(f.local_id)) return t('dashboard.vpnGateway.invalidIkeId')
    if (f.remote_id && !IKE_ID.test(f.remote_id)) return t('dashboard.vpnGateway.invalidIkeId')
    return null
}

// Everything the form holds, as the API payload (empty strings and null numbers left out)
const toPayload = (f: ConnectionForm): VpnConnectionPayload => {
    // Assembled as a plain record so the optional keys can be filled by name
    const p: Record<string, unknown> = {
        name: f.name,
        route_mode: f.route_mode,
        initiator: f.initiator,
        dpd_action: f.dpd_action,
    }
    const str = (key: keyof VpnConnectionPayload, value: string) => {
        if (value.trim()) p[key] = value.trim()
    }
    const int = (key: keyof VpnConnectionPayload, value: number | null) => {
        const v = num(value)
        if (v !== null) p[key] = v
    }
    str('description', f.description)
    str('remote_gateway', f.remote_gateway)
    str('local_cidrs', f.local_cidrs)
    str('ike_proposal', f.ike_proposal)
    str('esp_proposal', f.esp_proposal)
    str('local_id', f.local_id)
    str('remote_id', f.remote_id)
    int('ike_lifetime', f.ike_lifetime)
    int('esp_lifetime', f.esp_lifetime)
    int('dpd_delay', f.dpd_delay)
    if (f.route_mode === 'static') {
        str('remote_cidrs', f.remote_cidrs)
    } else {
        str('remote_summary_cidrs', f.remote_summary_cidrs)
        str('tunnel_local_ip', f.tunnel_local_ip)
        str('tunnel_peer_ip', f.tunnel_peer_ip)
        int('local_asn', f.local_asn)
        int('peer_asn', f.peer_asn)
        int('max_prefixes', f.max_prefixes)
        int('bgp_keepalive', f.bgp_keepalive)
        int('bgp_hold', f.bgp_hold)
    }
    return p as unknown as VpnConnectionPayload
}

// Fields whose value differs from the snapshot; description and the comma lists may legitimately be
// cleared, so those are compared as strings and sent even when empty
const CLEARABLE: Array<keyof ConnectionForm> = [
    'description',
    'remote_gateway',
    'local_cidrs',
    'remote_cidrs',
    'remote_summary_cidrs',
    'ike_proposal',
    'esp_proposal',
    'local_id',
    'remote_id',
]

// Optional numbers: a cleared input cannot unset the stored value (an omitted field keeps it), so
// only a new non-empty value is sent
const NUMERIC: Array<keyof ConnectionForm> = [
    'max_prefixes',
    'local_asn',
    'peer_asn',
    'bgp_keepalive',
    'bgp_hold',
    'ike_lifetime',
    'esp_lifetime',
    'dpd_delay',
]

const MODE_FIELDS = [
    'remote_cidrs',
    'remote_summary_cidrs',
    'tunnel_local_ip',
    'tunnel_peer_ip',
    'local_asn',
    'peer_asn',
    'max_prefixes',
    'bgp_keepalive',
    'bgp_hold',
] as const

const toPatch = (f: ConnectionForm): VpnConnectionPatchPayload => {
    const full = toPayload(f) as unknown as Record<string, unknown>
    const patch: Record<string, unknown> = {}
    for (const key of Object.keys(f) as Array<keyof ConnectionForm>) {
        if (key === 'psk' || key === 'bgp_password') continue
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
                <label class="form-label">{{ t('dashboard.vpnGateway.remoteGateway') }}</label>
                <input v-model="form.remote_gateway" type="text" class="form-input mono" placeholder="198.51.100.1" />
                <div class="form-hint">{{ t('dashboard.vpnGateway.remoteGatewayHint') }}</div>
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

        <div class="form-group">
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
            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.tunnelLocalIp') }} *</label>
                    <input
                        v-model="form.tunnel_local_ip"
                        type="text"
                        class="form-input mono"
                        placeholder="169.254.10.1"
                    />
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.tunnelPeerIp') }} *</label>
                    <input
                        v-model="form.tunnel_peer_ip"
                        type="text"
                        class="form-input mono"
                        placeholder="169.254.10.2"
                    />
                </div>
            </div>
            <div class="form-hint form-hint-block">{{ t('dashboard.vpnGateway.tunnelIpHint') }}</div>

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
        </template>

        <!-- Advanced: proposals, lifetimes, DPD, initiator, IKE identities -->
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

            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.localId') }}</label>
                    <input
                        v-model="form.local_id"
                        type="text"
                        class="form-input mono"
                        :placeholder="t('dashboard.forms.placeholder.domainExample')"
                    />
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ t('dashboard.vpnGateway.remoteId') }}</label>
                    <input
                        v-model="form.remote_id"
                        type="text"
                        class="form-input mono"
                        :placeholder="t('dashboard.forms.placeholder.domainExample')"
                    />
                </div>
            </div>
            <div class="form-hint form-hint-block">{{ t('dashboard.vpnGateway.idHint') }}</div>

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
