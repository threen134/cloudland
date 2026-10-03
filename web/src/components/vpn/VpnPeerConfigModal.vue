<script setup lang="ts">
// Peer configuration of a site connection: everything the administrator of the peer device needs to
// configure its side, built from the gateway and connection data already on the page (no extra request).
// The same structure renders the modal and the plain text that is copied or downloaded. The API returns
// the keys only to members with write permission: they are masked here and included in the copied text;
// read-only members get a note instead.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, Check, Download, AlertTriangle } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import VpnSecretValue from './VpnSecretValue.vue'
import { useCopyId } from '../../composables/useCopyId'
import { endpointName } from '../../utils/vpnEndpoint'
import type { VpnConnection, VpnGateway, VpnTunnel } from '../../api/vpn'

const props = defineProps<{
    show: boolean
    gateway: VpnGateway | null
    connection: VpnConnection | null
}>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const { copiedId, copyId } = useCopyId()
const k = (key: string, params?: Record<string, unknown>) => t(`dashboard.vpnGateway.peerConfig.${key}`, params ?? {})

interface Row {
    label: string
    value: string
    note?: string
    // A key: masked on screen, in clear in the copied text
    secret?: boolean
}
interface TunnelRow {
    title: string
    rows: Row[]
}
interface Section {
    title: string
    rows?: Row[]
    items?: string[]
    warnings?: string[]
}

// Readable names of the proposal tokens, as peer devices usually spell them
const cipherNames: Record<string, string> = {
    aes128: 'AES-128',
    aes192: 'AES-192',
    aes256: 'AES-256',
    aes128gcm16: 'AES-128-GCM',
    aes256gcm16: 'AES-256-GCM',
    aes128gcm: 'AES-128-GCM',
    aes256gcm: 'AES-256-GCM',
    '3des': '3DES',
    chacha20poly1305: 'ChaCha20-Poly1305',
    sha1: 'SHA-1',
    sha256: 'SHA-256',
    sha384: 'SHA-384',
    sha512: 'SHA-512',
    md5: 'MD5',
}
const dhGroups: Record<string, number> = {
    modp1024: 2,
    modp1536: 5,
    modp2048: 14,
    modp3072: 15,
    modp4096: 16,
    modp6144: 17,
    modp8192: 18,
    ecp256: 19,
    ecp384: 20,
    ecp521: 21,
    curve25519: 31,
    x25519: 31,
}

/** "aes256-sha256-modp2048" -> readable parts and the DH group (0 when the proposal has none) */
const decodeProposal = (proposal: string) => {
    const first = (proposal || '').split(',')[0].trim()
    const parts: string[] = []
    let dh = 0
    for (const token of first.split('-').filter(Boolean)) {
        if (dhGroups[token]) {
            dh = dhGroups[token]
            parts.push(`DH ${dh}`)
        } else if (cipherNames[token]) {
            parts.push(cipherNames[token])
        } else if (token.startsWith('prf')) {
            parts.push(`PRF ${cipherNames[token.slice(3)] ?? token.slice(3)}`)
        } else if (token !== 'esn' && token !== 'noesn') {
            parts.push(token)
        }
    }
    return { text: parts.join(', '), dh }
}

const proposalValue = (proposal: string) => {
    const decoded = decodeProposal(proposal)
    return decoded.text && decoded.text !== proposal ? k('withNote', { value: proposal, note: decoded.text }) : proposal
}

const ipNumber = (ip: string) => ip.split('.').reduce((acc, part) => acc * 256 + (Number(part) || 0), 0)

const isEcmp = computed(() => props.connection?.traffic_policy === 'ecmp')
const isBgp = computed(() => props.connection?.route_mode === 'bgp')
const tunnels = computed<VpnTunnel[]>(() => [...(props.connection?.tunnels ?? [])].sort((a, b) => a.slot - b.slot))
const primary = computed(() => tunnels.value.find((tun) => tun.priority === 'primary') ?? tunnels.value[0])
const splitCidrs = (value?: string) =>
    (value || '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
const localNets = computed(() => props.connection?.effective_local_cidrs ?? splitCidrs(props.connection?.local_cidrs))
const remoteNets = computed(() => splitCidrs(props.connection?.remote_cidrs))

const roleText = (tun: VpnTunnel) => {
    if (isEcmp.value) return k('roleShared')
    return tun.priority === 'primary' ? k('rolePrimary') : k('roleStandby')
}
const localAddress = (tun: VpnTunnel) => {
    const name = endpointName(t, tun.endpoint, props.gateway?.ha_mode, true)
    return tun.public_ip ? k('withNote', { value: tun.public_ip, note: name }) : name
}

// The key a tunnel uses: its own one, else the connection's (empty when the caller cannot read keys)
const tunnelKey = (tun: VpnTunnel) => (tun.psk_set ? tun.psk : props.connection?.psk) || ''
// The keys are in the text only when every tunnel's key came back
const keysIncluded = computed(() => tunnels.value.length > 0 && tunnels.value.every((tun) => !!tunnelKey(tun)))

const tunnelRows = computed<TunnelRow[]>(() =>
    tunnels.value.map((tun) => {
        const rows: Row[] = [
            { label: k('colLocalAddr'), value: localAddress(tun) },
            { label: k('colRemoteAddr'), value: tun.remote_gateway || k('anyAddress') },
            { label: k('colLocalId'), value: props.connection?.local_id || tun.public_ip || '-' },
            { label: k('colRemoteId'), value: tun.remote_id || tun.remote_gateway || '-' },
        ]
        const key = tunnelKey(tun)
        if (key) rows.push({ label: k('colPsk'), value: key, secret: true })
        if (isBgp.value) {
            rows.push(
                { label: k('colLocalInner'), value: tun.tunnel_local_ip ? `${tun.tunnel_local_ip}/30` : '-' },
                { label: k('colPeerInner'), value: tun.tunnel_peer_ip ? `${tun.tunnel_peer_ip}/30` : '-' }
            )
        }
        const name = t('dashboard.vpnGateway.tunnelN', { n: tun.slot })
        const title = tunnels.value.length > 1 ? k('withNote', { value: name, note: roleText(tun) }) : name
        return { title, rows }
    })
)

const pskNote = computed(() => {
    if (keysIncluded.value) return k('keysIncluded')
    const own = tunnels.value.filter((tun) => tun.psk_set).map((tun) => tun.slot)
    return own.length ? `${k('pskHidden')} ${k('pskPerTunnel', { list: own.join(k('listSep')) })}` : k('pskHidden')
})

const sections = computed<Section[]>(() => {
    const conn = props.connection
    if (!conn) return []
    const esp = decodeProposal(conn.esp_proposal)
    const out: Section[] = []

    out.push({
        title: k('sectionIke'),
        rows: [
            { label: k('ikeVersion'), value: `IKEv${conn.ike_version || 2}` },
            { label: k('auth'), value: k('authPsk') },
            { label: k('ikeProposal'), value: proposalValue(conn.ike_proposal) },
            { label: k('espProposal'), value: proposalValue(conn.esp_proposal) },
            { label: k('pfs'), value: esp.dh ? k('pfsOn', { group: esp.dh }) : k('pfsOff') },
            { label: k('ikeLifetime'), value: k('secondsValue', { n: conn.ike_lifetime }) },
            { label: k('espLifetime'), value: k('secondsValue', { n: conn.esp_lifetime }) },
            { label: k('mode'), value: k('modeTunnel') },
            { label: k('natt'), value: k('nattAuto') },
            { label: k('fragmentation'), value: k('fragmentationOn') },
            { label: k('mtu'), value: k('mtuValue') },
            { label: k('dpd'), value: k('dpdValue', { n: conn.dpd_delay }) },
            { label: k('initiator'), value: conn.initiator ? k('initiatorUs') : k('initiatorPeer') },
        ],
    })

    const routing: Row[] = [
        {
            label: k('routeMode'),
            value: isBgp.value ? t('dashboard.vpnGateway.routeModeBgp') : t('dashboard.vpnGateway.routeModeStatic'),
        },
    ]
    if (isBgp.value) {
        routing.push(
            { label: k('trafficSelectors'), value: k('bgpTsValue'), note: k('bgpTsNote') },
            { label: k('localAsn'), value: String(conn.local_asn) },
            { label: k('peerAsn'), value: String(conn.peer_asn) },
            { label: k('timers'), value: k('timersValue', { keepalive: conn.bgp_keepalive, hold: conn.bgp_hold }) },
            conn.bgp_password
                ? { label: k('bgpPassword'), value: conn.bgp_password, secret: true }
                : {
                      label: k('bgpPassword'),
                      value: conn.bgp_password_set ? k('bgpPasswordSet') : k('bgpPasswordNone'),
                  },
            {
                label: k('bfd'),
                value: conn.bfd_enabled
                    ? k('bfdValue', { interval: conn.bfd_interval, multiplier: conn.bfd_multiplier })
                    : k('bfdOff'),
            },
            { label: k('advertised'), value: localNets.value.join(', ') || '-' },
            {
                label: k('acceptedRange'),
                value: splitCidrs(conn.remote_summary_cidrs).join(', ') || '-',
                note: k('acceptedRangeNote'),
            },
            { label: k('maxPrefixes'), value: k('maxPrefixesValue', { n: conn.max_prefixes }) }
        )
    } else {
        const pairs: string[] = []
        for (const l of localNets.value) for (const r of remoteNets.value) pairs.push(`${l} ↔ ${r}`)
        routing.push(
            { label: k('localNets'), value: localNets.value.join(', ') || '-' },
            { label: k('remoteNets'), value: remoteNets.value.join(', ') || '-' },
            { label: k('trafficSelectors'), value: pairs.join(k('listSep')) || '-', note: k('staticTsNote') }
        )
    }
    out.push({ title: k('sectionRouting'), rows: routing })

    if (tunnels.value.length > 1) {
        const items: string[] = []
        if (isEcmp.value) {
            items.push(k('ecmp'))
        } else if (primary.value) {
            items.push(
                k('primaryIs', {
                    n: primary.value.slot,
                    local: primary.value.public_ip || '-',
                    remote: primary.value.remote_gateway || k('anyAddress'),
                })
            )
            if (!isBgp.value) items.push(k('primaryStatic'))
            else if (conn.as_path_prepend > 0) items.push(k('primaryBgp', { n: conn.as_path_prepend }))
            else items.push(k('primaryBgpNoPrepend'))
        }
        out.push({ title: k('sectionPrimary'), items })
    }

    const advice: string[] = []
    const warnings: string[] = []
    if (isBgp.value && !conn.bfd_enabled && conn.bgp_hold > 9)
        advice.push(k('adviceBgpTimers', { hold: conn.bgp_hold }))
    if (!isBgp.value) advice.push(k('adviceStatic'))
    if (props.gateway?.ha_mode === 'active_active' && tunnels.value.length > 1) {
        advice.push(k('adviceActiveActive'))
    } else if (new Set(tunnels.value.map((tun) => tun.public_ip).filter(Boolean)).size > 1) {
        advice.push(k('adviceTwoLocal'))
    }
    // Only with static routing: over BGP the peer follows the AS path prepending, whatever its addresses
    const remotes = [...new Set(tunnels.value.map((tun) => tun.remote_gateway).filter(Boolean))]
    if (!isBgp.value && !isEcmp.value && remotes.length > 1 && primary.value?.remote_gateway) {
        const smallest = remotes.reduce((a, b) => (ipNumber(b) < ipNumber(a) ? b : a))
        advice.push(k('adviceSmallest', { addr: smallest }))
        if (primary.value.remote_gateway !== smallest) {
            warnings.push(k('adviceSmallestWarn', { current: primary.value.remote_gateway, smallest }))
        }
    }
    if (!conn.initiator) advice.push(k('adviceResponder'))
    const anyPeer = tunnels.value.filter((tun) => !tun.remote_gateway).map((tun) => tun.slot)
    if (anyPeer.length) advice.push(k('adviceAnyPeer', { list: anyPeer.join(k('listSep')) }))
    advice.push(k('adviceSecurityGroup'))
    out.push({ title: k('sectionAdvice'), items: advice, warnings })
    return out
})

// Plain text for copy and download
const plainText = computed(() => {
    if (!props.connection) return ''
    const pair = (label: string, value: string) => k('pair', { label, value })
    const lines: string[] = [
        k('textTitle'),
        pair(k('gatewayLabel'), props.gateway?.name ?? '-'),
        pair(k('connectionLabel'), props.connection.name),
        '',
        k('intro'),
        pskNote.value,
        '',
        k('heading', { title: k('sectionTunnels') }),
    ]
    for (const tun of tunnelRows.value) {
        lines.push(tun.title)
        for (const row of tun.rows) lines.push(`  ${pair(row.label, row.value)}`)
    }
    for (const section of sections.value) {
        lines.push('', k('heading', { title: section.title }))
        for (const row of section.rows ?? []) {
            lines.push(`  ${pair(row.label, row.value)}`)
            if (row.note) lines.push(`    ${row.note}`)
        }
        for (const w of section.warnings ?? []) lines.push(`  ! ${w}`)
        for (const item of section.items ?? []) lines.push(`  - ${item}`)
    }
    return lines.join('\n') + '\n'
})

const fileName = computed(() => {
    const base = `${props.gateway?.name ?? 'vpn'}-${props.connection?.name ?? 'connection'}-peer`
    return `${base.replace(/[^A-Za-z0-9_.-]/g, '_')}.txt`
})

const download = () => {
    const blob = new Blob([plainText.value], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = fileName.value
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="k('title', { name: connection?.name ?? '' })"
        size="xl"
        content-class="peer-config-modal"
        @close="emit('close')"
    >
        <div v-if="connection" class="peer-config">
            <p class="peer-intro">{{ k('intro') }}</p>
            <p class="peer-note" :class="{ 'peer-note-warn': keysIncluded }">{{ pskNote }}</p>

            <h4 class="peer-section-title">{{ k('sectionTunnels') }}</h4>
            <div class="tunnel-grid">
                <div v-for="tun in tunnelRows" :key="tun.title" class="tunnel-card">
                    <div class="tunnel-title">{{ tun.title }}</div>
                    <dl class="kv">
                        <template v-for="row in tun.rows" :key="row.label">
                            <dt>{{ row.label }}</dt>
                            <dd>
                                <VpnSecretValue v-if="row.secret" :value="row.value" />
                                <template v-else>{{ row.value }}</template>
                            </dd>
                        </template>
                    </dl>
                </div>
            </div>

            <template v-for="section in sections" :key="section.title">
                <h4 class="peer-section-title">{{ section.title }}</h4>
                <dl v-if="section.rows" class="kv kv-wide">
                    <template v-for="row in section.rows" :key="row.label">
                        <dt>{{ row.label }}</dt>
                        <dd>
                            <VpnSecretValue v-if="row.secret" :value="row.value" />
                            <template v-else>{{ row.value }}</template>
                            <span v-if="row.note" class="kv-note">{{ row.note }}</span>
                        </dd>
                    </template>
                </dl>
                <div v-for="w in section.warnings ?? []" :key="w" class="peer-warning">
                    <AlertTriangle :size="16" />
                    <span>{{ w }}</span>
                </div>
                <ul v-if="section.items?.length" class="peer-list">
                    <li v-for="item in section.items" :key="item">{{ item }}</li>
                </ul>
            </template>
        </div>

        <template #footer>
            <div class="peer-footer">
                <button type="button" class="btn btn-secondary" @click="copyId(plainText, 'peer')">
                    <Check v-if="copiedId === 'peer'" :size="14" class="copied-icon" />
                    <Copy v-else :size="14" />
                    {{ copiedId === 'peer' ? t('messages.copied') : k('copyAll') }}
                </button>
                <button type="button" class="btn btn-secondary" @click="download">
                    <Download :size="14" />
                    {{ k('download') }}
                </button>
                <button type="button" class="btn btn-primary" @click="emit('close')">{{ t('actions.close') }}</button>
            </div>
        </template>
    </BaseModal>
</template>

<style scoped>
/* The buttons wrap on a phone instead of running off the left edge */
.peer-footer {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: var(--spacing-3);
    width: 100%;
}
.peer-config {
    display: flex;
    flex-direction: column;
    gap: 8px;
}
.peer-intro {
    margin: 0;
    color: var(--text-secondary);
    font-size: 0.875rem;
}
.peer-note {
    margin: 0;
    padding: 8px 12px;
    border-radius: 6px;
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-size: 0.8125rem;
}
.peer-note-warn {
    background: var(--warning-light);
    color: var(--warning-dark);
}
.peer-section-title {
    margin: 12px 0 4px;
    font-size: 0.9375rem;
    font-weight: 600;
    color: var(--text-primary);
}
.tunnel-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(320px, 100%), 1fr));
    gap: 12px;
}
.tunnel-card {
    border: 1px solid var(--border-default);
    border-radius: 8px;
    padding: 10px 12px;
}
.tunnel-title {
    font-weight: 600;
    font-size: 0.875rem;
    margin-bottom: 6px;
}
.kv {
    display: grid;
    grid-template-columns: 112px 1fr;
    gap: 4px 12px;
    margin: 0;
    font-size: 0.8125rem;
}
.kv-wide {
    grid-template-columns: 144px 1fr;
}
.kv dt {
    color: var(--text-secondary);
}
.kv dd {
    margin: 0;
    color: var(--text-primary);
    word-break: break-word;
}
.kv-note {
    display: block;
    color: var(--text-tertiary);
    font-size: 0.75rem;
    margin-top: 2px;
}
.peer-warning {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    padding: 8px 12px;
    border-radius: 6px;
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: 0.8125rem;
}
.peer-list {
    margin: 0;
    padding-left: 20px;
    font-size: 0.8125rem;
    color: var(--text-primary);
}
.peer-list li + li {
    margin-top: 4px;
}
@media (max-width: 768px) {
    .kv,
    .kv-wide {
        grid-template-columns: 1fr;
    }
    .kv dt {
        margin-top: 4px;
    }
}
</style>
