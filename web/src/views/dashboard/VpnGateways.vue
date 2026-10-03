<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { useRegionStore } from '../../stores/region'
import { vpcsApi, subnetsApi, type VPC, type Subnet } from '../../api/networks'
import { zonesApi, type Zone } from '../../api/zones'
import {
    vpnGatewaysApi,
    type VpnGateway,
    type VpnGatewayPayload,
    type VpnHaMode,
    type VpnPublicIpPayload,
} from '../../api/vpn'
import { endpointLabel, isActiveActive, isSingleNode } from '../../utils/vpnEndpoint'
import { isValidName, isValidCIDRv4, hasDefaultRoute } from '../../utils/validation'
import { quotaErrorMessage } from '../../utils/quotaError'
import { errorMessage } from '../../utils/error'
import { formatToMinute } from '../../utils/format'
import { ShieldCheck, Plus, Trash2, Search, RefreshCw, Check, Copy } from 'lucide-vue-next'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import VpnPublicAddressPicker from '../../components/vpn/VpnPublicAddressPicker.vue'
import { OPTION_LIST_LIMIT } from '../../api/listParams'

const { t, te } = useI18n()
const toast = useToast()
const region = useRegionStore()
const { copiedId, copyId } = useCopyId()

// Unknown states fall back to the raw value rather than showing an i18n key
const statusText = (status?: string) =>
    status && te(`dashboard.vpnGatewayStatus.${status}`) ? t(`dashboard.vpnGatewayStatus.${status}`) : status || '-'
// A paused gateway that is otherwise available shows as disabled; building, error and deleting win
const gatewayBadge = (gw: VpnGateway) =>
    gw.enabled === false && gw.status === 'available'
        ? { status: 'disabled', label: t('dashboard.vpnGateway.disabled') }
        : { status: gw.status, label: statusText(gw.status) }
// Gateways created before HA modes existed have no ha_mode: they are active_standby
const haModeText = (gw: VpnGateway) =>
    isActiveActive(gw) ? t('dashboard.vpnGateway.haModeActiveActive') : t('dashboard.vpnGateway.haModeActiveStandby')

const vpcs = ref<VPC[]>([])

// Server-side paging and search (see useListQuery); a VPC has at most one gateway, so there is no VPC filter. The VPC name and the node
// hostname are joined from other tables, so those columns are not sortable
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'vpc', label: t('dashboard.table.vpc') },
    { key: 'ha_mode', label: t('dashboard.vpnGateway.haMode'), hideBelow: 1024 },
    { key: 'public_ip', label: t('dashboard.vpnGateway.publicIp') },
    { key: 'status', label: t('dashboard.table.status'), sortable: true },
    { key: 'nodes', label: t('dashboard.vpnGateway.masterNode'), hideBelow: 1280 },
    { key: 'connections', label: t('dashboard.vpnGateway.connections'), align: 'center' },
    { key: 'clients', label: t('dashboard.vpnGateway.clients'), align: 'center' },
    { key: 'created_at', label: t('dashboard.table.createdAt'), sortable: true, hideBelow: 1024 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const {
    items: gateways,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchGateways,
    reload: reloadGateways,
} = useListQuery<VpnGateway>(
    async ({ offset, limit, query, order }) => {
        const response = await vpnGatewaysApi.list({
            offset,
            limit,
            order,
            query: query || undefined,
        })
        return { items: response.vpn_gateways || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

const fetchVpcs = async () => {
    try {
        const response = await vpcsApi.list({ limit: OPTION_LIST_LIMIT })
        vpcs.value = response.vpcs || []
    } catch (err) {
        console.error('Failed to fetch VPCs:', err)
        vpcs.value = []
    }
}

// ─── Create modal ────────────────────────────────────────────────────────────
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const publicSubnets = ref<Subnet[]>([])
// One entry per public address; empty strings let clapi pick the subnet / the address
interface PublicAddressForm {
    subnet_id: string
    ip: string
}
// Three entries, the most a gateway takes at creation (active_active with client VPN); how many are used
// depends on the HA mode (see activeAddresses)
const emptyAddresses = (): PublicAddressForm[] => [
    { subnet_id: '', ip: '' },
    { subnet_id: '', ip: '' },
    { subnet_id: '', ip: '' },
]
const emptyForm = () => ({
    name: '',
    description: '',
    vpc_id: '',
    zone: '',
    ha_mode: 'active_standby' as VpnHaMode,
    // active_standby only: two addresses let one connection build a primary and a standby tunnel towards a
    // single peer address
    address_count: 1 as 1 | 2,
    public_ips: emptyAddresses(),
    ipsec_enabled: true,
    client_enabled: false,
    client_cidr: '10.8.0.0/24',
    client_port: 51820 as number | null,
    client_dns: '',
    client_routes: '',
})
const newForm = ref(emptyForm())
const isNameValid = computed(() => isValidName(newForm.value.name))
const isClientCidrValid = computed(() => !newForm.value.client_enabled || isValidCIDRv4(newForm.value.client_cidr))
const IPV4 = /^(\d{1,3}\.){3}\d{1,3}$/
const isNewActiveActive = computed(() => newForm.value.ha_mode === 'active_active')

// Zones feed the zone dropdown of the create modal
const zones = ref<Zone[]>([])
const fetchZones = async () => {
    try {
        const res = await zonesApi.fetchZones({ limit: OPTION_LIST_LIMIT })
        zones.value = res.zones || []
    } catch (err) {
        console.error('Failed to fetch zones:', err)
        zones.value = []
    }
}

// The two HA modes with what each protects against (radio cards of the create modal)
const HA_MODES: Array<{ value: VpnHaMode; title: string; hint: string }> = [
    {
        value: 'active_standby',
        title: 'dashboard.vpnGateway.haModeActiveStandby',
        hint: 'dashboard.vpnGateway.haModeActiveStandbyHint',
    },
    {
        value: 'active_active',
        title: 'dashboard.vpnGateway.haModeActiveActive',
        hint: 'dashboard.vpnGateway.haModeActiveActiveHint',
    },
]

// The addresses the gateway takes, each one a public IP of the quota. active_standby: vip1 and optionally
// vip2. active_active: one fixed address per node, plus the floating address of the client VPN
const activeAddresses = computed(() => {
    const f = newForm.value
    const count = f.ha_mode === 'active_active' ? (f.client_enabled ? 3 : 2) : f.address_count
    return f.public_ips.slice(0, count)
})
const addressTitle = (index: number) => {
    if (!isNewActiveActive.value) return t('dashboard.vpnGateway.publicAddressN', { n: index + 1 })
    return index < 2 ? t('dashboard.vpnGateway.nodeN', { n: index + 1 }) : t('dashboard.vpnGateway.clientVpnAddress')
}
// The address picked for another entry is left out, so two entries cannot take the same address
const excludedAddresses = (index: number) =>
    activeAddresses.value.filter((a, i) => i !== index && a.ip).map((a) => a.ip)
// active_active: an entry without a subnet takes the subnet of the first one (clapi does the same for the
// entries it fills in itself)
const autoSubnetLabel = (index: number) =>
    isNewActiveActive.value && index > 0 ? t('dashboard.vpnGateway.publicSubnetSameAsFirst') : ''

const fetchPublicSubnets = async () => {
    try {
        const res = await subnetsApi.list({ limit: OPTION_LIST_LIMIT })
        publicSubnets.value = (res.subnets || []).filter((s) => s.type === 'public')
    } catch (err) {
        console.error('Failed to fetch subnets:', err)
        publicSubnets.value = []
    }
}

// A VPC has at most one gateway: those that already have one are marked and cannot be chosen
const vpcsWithGateway = ref<Set<string>>(new Set())
const availableVpcs = computed(() => vpcs.value.filter((vpc) => !vpcsWithGateway.value.has(vpc.id)))
const fetchVpcsWithGateway = async () => {
    try {
        const response = await vpnGatewaysApi.list({ limit: OPTION_LIST_LIMIT })
        vpcsWithGateway.value = new Set(
            (response.vpn_gateways || []).map((gw) => gw.vpc?.id).filter((id): id is string => !!id)
        )
    } catch (err) {
        console.error('Failed to fetch VPN gateways:', err)
        vpcsWithGateway.value = new Set()
    }
}

const openCreateModal = async () => {
    newForm.value = emptyForm()
    createError.value = ''
    createModalVisible.value = true
    fetchPublicSubnets()
    fetchZones()
    await fetchVpcsWithGateway()
    if (!newForm.value.vpc_id) newForm.value.vpc_id = availableVpcs.value[0]?.id || ''
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreate = async () => {
    createError.value = ''
    const form = newForm.value
    if (!form.name || !isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }
    if (!form.vpc_id) {
        createError.value = t('messages.vpcRequired')
        return
    }
    if (!form.ipsec_enabled && !form.client_enabled) {
        createError.value = t('dashboard.vpnGateway.needOneAccess')
        return
    }
    const addresses = activeAddresses.value
    if (addresses.some((a) => a.ip && !IPV4.test(a.ip))) {
        createError.value = t('dashboard.vpnGateway.invalidIp')
        return
    }
    const picked = addresses.map((a) => a.ip).filter(Boolean)
    if (new Set(picked).size !== picked.length) {
        createError.value = t('dashboard.vpnGateway.publicIpsMustDiffer')
        return
    }
    if (form.client_enabled) {
        if (!isValidCIDRv4(form.client_cidr)) {
            createError.value = t('dashboard.vpnGateway.invalidClientCidr')
            return
        }
        // v-model.number yields '' after the input is cleared
        const port = (form.client_port as unknown) === '' ? null : form.client_port
        if (port !== null && (!Number.isInteger(port) || port < 1 || port > 65535)) {
            createError.value = t('messages.invalidPort')
            return
        }
        if (form.client_dns && !form.client_dns.split(',').every((d) => IPV4.test(d.trim()))) {
            createError.value = t('dashboard.vpnGateway.invalidDns')
            return
        }
        if (form.client_routes && hasDefaultRoute(form.client_routes)) {
            createError.value = t('dashboard.vpnGateway.clientRoutesNoDefault')
            return
        }
    }

    creating.value = true
    try {
        const payload: VpnGatewayPayload = {
            name: form.name,
            vpc: { id: form.vpc_id },
            ipsec_enabled: form.ipsec_enabled,
            client_enabled: form.client_enabled,
        }
        if (form.description) payload.description = form.description
        if (form.zone) payload.zone = form.zone
        if (form.ha_mode === 'active_active') payload.ha_mode = 'active_active'
        if (addresses.length === 1) {
            // One address: the short form clapi has always accepted
            if (addresses[0].subnet_id) payload.public_subnet = { id: addresses[0].subnet_id }
            if (addresses[0].ip) payload.public_ip = addresses[0].ip
        } else {
            // An entry sent without a subnet lets clapi pick any public subnet: on an active_active gateway
            // it takes the subnet of the first entry instead, as the form says
            const firstSubnet = form.ha_mode === 'active_active' ? addresses[0].subnet_id : ''
            payload.public_ips = addresses.map((a) => {
                const entry: VpnPublicIpPayload = {}
                const subnetId = a.subnet_id || firstSubnet
                if (subnetId) entry.public_subnet = { id: subnetId }
                if (a.ip) entry.public_ip = a.ip
                return entry
            })
        }
        if (form.client_enabled) {
            payload.client_cidr = form.client_cidr
            if (form.client_port) payload.client_port = form.client_port
            if (form.client_dns) payload.client_dns = form.client_dns
            if (form.client_routes) payload.client_routes = form.client_routes
        }
        await vpnGatewaysApi.create(payload)
        // The list is sorted newest first: show the first page
        await reloadGateways()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create VPN gateway:', err)
        const code = (err as { response?: { data?: { error_code?: number } } })?.response?.data?.error_code
        if (code === 132005) {
            // ErrVpnGatewayExists: someone created one in the meantime
            createError.value = t('dashboard.vpnGateway.vpcHasGateway')
            fetchVpcsWithGateway()
        } else if (code === 132009) {
            // ErrVpnGatewayNeedsNodes: the zone has no available node, or one for an active-active gateway
            createError.value = t('dashboard.vpnGateway.zoneNeedsNodes')
        } else {
            createError.value = quotaErrorMessage(err, t, te) || errorMessage(err, t('messages.error'))
        }
    } finally {
        creating.value = false
    }
}

// ─── Delete modal ────────────────────────────────────────────────────────────
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<VpnGateway | null>(null)

const handleDeleteClick = (item: VpnGateway) => {
    resourceToDelete.value = item
    deleteError.value = ''
    deleteModalVisible.value = true
}
const closeDeleteModal = () => {
    deleteModalVisible.value = false
    resourceToDelete.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!resourceToDelete.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await vpnGatewaysApi.delete(resourceToDelete.value.id)
        await fetchGateways()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error) {
        console.error('Failed to delete VPN gateway:', error)
        deleteError.value = errorMessage(error, t('messages.error'))
    } finally {
        deletingResource.value = false
    }
}

onMounted(() => {
    fetchVpcs()
    // useListQuery only reloads on changes: the first page must be requested explicitly
    if (region.currentRegionId) {
        fetchGateways()
    }
})
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    :title="$t('actions.refresh')"
                    @click="fetchGateways()"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.createVpnGateway') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="gateways"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="fetchGateways()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <ShieldCheck :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p class="text-secondary">{{ $t('messages.noVpnGateways') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: gw }">
                <router-link :to="{ name: 'vpn-gateway-detail', params: { id: gw.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <ShieldCheck :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ gw.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="gw.id">{{ gw.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                    @click.stop.prevent="copyId(gw.id)"
                                >
                                    <Check v-if="copiedId === gw.id" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                            <div v-if="gw.description" class="resource-desc">{{ gw.description }}</div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-vpc="{ row: gw }">
                <router-link
                    v-if="gw.vpc"
                    :to="{ name: 'vpc-detail', params: { id: gw.vpc.id } }"
                    class="resource-link-static"
                >
                    {{ gw.vpc.name }}
                </router-link>
                <span v-else>-</span>
            </template>

            <template #cell-ha_mode="{ row: gw }">
                <span class="badge mode-badge" :class="isActiveActive(gw) ? 'badge-info' : 'badge-secondary'">{{
                    haModeText(gw)
                }}</span>
            </template>

            <!-- One line per address; with more than one, each says which it is (and the node of a fixed one) -->
            <template #cell-public_ip="{ row: gw }">
                <div v-if="gw.public_ips?.length" class="ip-list">
                    <div v-for="p in gw.public_ips" :key="p.endpoint" class="ip-line">
                        <code class="ip-address">{{ p.address }}</code>
                        <span v-if="gw.public_ips.length > 1 || isActiveActive(gw)" class="ip-endpoint">{{
                            endpointLabel(t, p, gw.ha_mode)
                        }}</span>
                    </div>
                </div>
                <code v-else class="ip-address">{{ gw.public_ip || '-' }}</code>
            </template>

            <template #cell-status="{ row: gw }">
                <StatusBadge
                    :status="gatewayBadge(gw).status"
                    :label="gatewayBadge(gw).label"
                    :title="gw.status === 'error' ? gw.status_reason : undefined"
                />
            </template>

            <template #cell-nodes="{ row: gw }">
                <span v-if="gw.master_hostname">{{ gw.master_hostname }}</span>
                <span v-else class="text-tertiary">-</span>
                <span v-if="(gw.nodes?.length || 0) > 1" class="text-tertiary text-xs">
                    {{ ' ' }}(+{{ (gw.nodes?.length || 1) - 1 }})
                </span>
                <span
                    v-if="isSingleNode(gw)"
                    class="badge badge-warning single-node"
                    :title="$t('dashboard.vpnGateway.singleNodeNotice')"
                    >{{ $t('dashboard.vpnGateway.singleNode') }}</span
                >
            </template>

            <template #cell-connections="{ row: gw }">{{ gw.connection_count ?? 0 }}</template>
            <template #cell-clients="{ row: gw }">{{ gw.client_count ?? 0 }}</template>

            <template #cell-created_at="{ row: gw }">
                <span class="cell-time" :title="gw.created_at">{{ formatToMinute(gw.created_at) }}</span>
            </template>

            <template #cell-actions="{ row: gw }">
                <div class="row-actions">
                    <button
                        class="icon-btn-table icon-danger"
                        :title="$t('actions.delete')"
                        :disabled="gw.status === 'deleting'"
                        @click="handleDeleteClick(gw)"
                    >
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>

            <template #footer>
                <PaginationBar
                    :page="page"
                    :page-size="pageSize"
                    :total="total"
                    @update:page="page = $event"
                    @update:page-size="pageSize = $event"
                />
            </template>
        </DataTable>

        <!-- Create modal -->
        <BaseModal
            :show="createModalVisible"
            :title="$t('dashboard.buttons.createVpnGateway')"
            :loading="creating"
            size="lg"
            form
            @close="closeCreateModal"
            @submit="handleCreate"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                <input
                    v-model="newForm.name"
                    type="text"
                    :class="['form-input', { 'input-error': !isNameValid }]"
                    :placeholder="$t('dashboard.forms.placeholder.vpnGatewayNameExample')"
                />
                <div v-if="!isNameValid" class="text-error text-xs mt-1">{{ $t('messages.invalidHostname') }}</div>
            </div>

            <div class="form-group">
                <label class="form-label"
                    >{{ $t('dashboard.forms.description') }}（{{ $t('dashboard.forms.optional') }}）</label
                >
                <input
                    v-model="newForm.description"
                    type="text"
                    class="form-input"
                    :placeholder="$t('messages.placeholderDescription')"
                />
            </div>

            <div class="form-row">
                <div class="form-group form-group-grow">
                    <label class="form-label">{{ $t('dashboard.forms.vpc') }} *</label>
                    <div class="select-wrapper">
                        <select v-model="newForm.vpc_id" class="form-input">
                            <option v-if="!availableVpcs.length" value="" disabled>
                                {{ $t('dashboard.vpnGateway.noVpcAvailable') }}
                            </option>
                            <option
                                v-for="vpc in vpcs"
                                :key="vpc.id"
                                :value="vpc.id"
                                :disabled="vpcsWithGateway.has(vpc.id)"
                            >
                                {{ vpc.name }} ({{ vpc.id.slice(0, 8) }}...){{
                                    vpcsWithGateway.has(vpc.id)
                                        ? ` · ${$t('dashboard.vpnGateway.vpcHasGatewayOption')}`
                                        : ''
                                }}
                            </option>
                        </select>
                    </div>
                    <div v-if="!availableVpcs.length" class="form-hint">
                        {{ $t('dashboard.vpnGateway.noVpcAvailableHint') }}
                    </div>
                </div>
                <div class="form-group form-group-grow">
                    <label class="form-label"
                        >{{ $t('dashboard.forms.zone') }}（{{ $t('dashboard.forms.optional') }}）</label
                    >
                    <div class="select-wrapper">
                        <select v-model="newForm.zone" class="form-input">
                            <option value="">{{ $t('dashboard.forms.zoneAuto') }}</option>
                            <option v-for="z in zones" :key="z.id" :value="z.name">
                                {{ z.name }}{{ z.default ? ` · ${$t('dashboard.forms.zoneDefaultTag')}` : '' }}
                            </option>
                        </select>
                    </div>
                </div>
            </div>

            <!-- HA mode: fixed at creation; it decides how many public addresses the gateway takes -->
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.vpnGateway.haMode') }}</label>
                <div class="mode-options">
                    <label
                        v-for="mode in HA_MODES"
                        :key="mode.value"
                        class="mode-option"
                        :class="{ selected: newForm.ha_mode === mode.value }"
                    >
                        <input v-model="newForm.ha_mode" type="radio" :value="mode.value" />
                        <span class="mode-option-body">
                            <span class="mode-option-title">{{ $t(mode.title) }}</span>
                            <span class="mode-option-desc">{{ $t(mode.hint) }}</span>
                        </span>
                    </label>
                </div>
            </div>

            <div v-if="!isNewActiveActive" class="form-group">
                <label class="form-label">{{ $t('dashboard.vpnGateway.publicAddressCount') }}</label>
                <div class="radio-group">
                    <label class="radio-label">
                        <input v-model="newForm.address_count" type="radio" :value="1" />
                        {{ $t('dashboard.vpnGateway.publicAddressOne') }}
                    </label>
                    <label class="radio-label">
                        <input v-model="newForm.address_count" type="radio" :value="2" />
                        {{ $t('dashboard.vpnGateway.publicAddressTwo') }}
                    </label>
                </div>
                <div v-if="newForm.address_count === 2" class="form-hint">
                    {{ $t('dashboard.vpnGateway.publicAddressTwoHint') }}
                </div>
            </div>
            <div v-else class="form-group">
                <label class="form-label">{{ $t('dashboard.vpnGateway.publicAddressCount') }}</label>
                <div class="form-hint form-hint-top">{{ $t('dashboard.vpnGateway.activeActiveAddressHint') }}</div>
            </div>

            <template v-for="(entry, i) in activeAddresses" :key="i">
                <div v-if="activeAddresses.length > 1" class="address-title">{{ addressTitle(i) }}</div>
                <VpnPublicAddressPicker
                    v-model:subnet-id="entry.subnet_id"
                    v-model:ip="entry.ip"
                    :subnets="publicSubnets"
                    :exclude="excludedAddresses(i)"
                    :auto-subnet-label="autoSubnetLabel(i)"
                />
            </template>
            <div class="quota-note">
                {{ $t('dashboard.vpnGateway.publicIpQuotaUse', { n: activeAddresses.length }) }}
            </div>

            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="newForm.ipsec_enabled" type="checkbox" />
                    {{ $t('dashboard.vpnGateway.ipsecEnabled') }}
                </label>
                <div class="form-hint">{{ $t('dashboard.vpnGateway.ipsecEnabledHint') }}</div>
            </div>

            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="newForm.client_enabled" type="checkbox" />
                    {{ $t('dashboard.vpnGateway.clientEnabled') }}
                </label>
                <div class="form-hint">{{ $t('dashboard.vpnGateway.clientEnabledHint') }}</div>
                <div v-if="isNewActiveActive" class="form-hint">
                    {{ $t('dashboard.vpnGateway.clientAddressActiveActiveHint') }}
                </div>
            </div>

            <template v-if="newForm.client_enabled">
                <div class="form-row">
                    <div class="form-group form-group-grow">
                        <label class="form-label">{{ $t('dashboard.vpnGateway.clientCidr') }} *</label>
                        <input
                            v-model="newForm.client_cidr"
                            type="text"
                            :class="['form-input', { 'input-error': !isClientCidrValid }]"
                            placeholder="10.8.0.0/24"
                        />
                        <div class="form-hint">{{ $t('dashboard.vpnGateway.clientCidrHint') }}</div>
                    </div>
                    <div class="form-group form-group-fixed">
                        <label class="form-label">{{ $t('dashboard.vpnGateway.clientPort') }}</label>
                        <input
                            v-model.number="newForm.client_port"
                            type="number"
                            min="1"
                            max="65535"
                            class="form-input"
                            placeholder="51820"
                        />
                    </div>
                </div>
                <div class="form-group">
                    <label class="form-label"
                        >{{ $t('dashboard.vpnGateway.clientDns') }}（{{ $t('dashboard.forms.optional') }}）</label
                    >
                    <input v-model="newForm.client_dns" type="text" class="form-input" placeholder="192.168.1.53" />
                    <div class="form-hint">{{ $t('dashboard.vpnGateway.clientDnsHint') }}</div>
                </div>
                <div class="form-group">
                    <label class="form-label"
                        >{{ $t('dashboard.vpnGateway.clientRoutes') }}（{{ $t('dashboard.forms.optional') }}）</label
                    >
                    <input
                        v-model="newForm.client_routes"
                        type="text"
                        class="form-input"
                        placeholder="10.0.1.0/24, 10.0.2.0/24"
                    />
                    <div class="form-hint">{{ $t('dashboard.vpnGateway.clientRoutesHint') }}</div>
                </div>
            </template>

            <template #footer>
                <div v-if="createError" class="modal-error footer-error">{{ createError }}</div>
                <button type="button" class="btn btn-secondary" :disabled="creating" @click="closeCreateModal">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="creating || !newForm.name || !newForm.vpc_id">
                    <span
                        v-if="creating"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createVpnGateway') }}
                </button>
            </template>
        </BaseModal>

        <!-- Delete confirmation: connections and clients go with the gateway -->
        <DeleteModal
            :show="deleteModalVisible"
            :message="$t('dashboard.vpnGateway.deleteWarning')"
            :resource-name="resourceToDelete?.name"
            :resource-id="resourceToDelete?.id"
            :loading="deletingResource"
            :error="deleteError"
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
/* .resource-info, .row-actions, .icon-btn-table are global (index.css) */

.resource-link {
    text-decoration: none;
    display: block;
    padding: 4px 0;
    border-radius: var(--radius-sm);
    transition: all 0.15s;
}

.resource-link:hover .resource-name {
    color: var(--primary-600);
    text-decoration: underline;
}

.resource-desc {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 2px;
}

.ip-address {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-sm);
}

/* A gateway with two public addresses lists them one per line */
.ip-list {
    display: flex;
    flex-direction: column;
    gap: 2px;
}

/* Endpoint name under an address when a gateway has several (Node 1 (work-02), Public address 2) */
.ip-line {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-2);
    white-space: nowrap;
}

.ip-endpoint {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

.mode-badge {
    white-space: nowrap;
}

/* HA mode: two radio cards with what each mode protects against */
.mode-options {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-3);
}

.mode-option {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-2);
    padding: var(--spacing-3);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: border-color 0.15s;
}

.mode-option:hover {
    border-color: var(--primary-color);
}

.mode-option.selected {
    border-color: var(--primary-color);
    background: var(--primary-50);
}

.mode-option input {
    margin-top: 3px;
    flex-shrink: 0;
}

.mode-option-body {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
}

.mode-option-title {
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    color: var(--text-primary);
}

.mode-option-desc {
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-secondary);
}

.form-hint-top {
    margin-top: 0;
}

.quota-note {
    margin: calc(-1 * var(--spacing-2)) 0 var(--spacing-4);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.address-title {
    margin-bottom: var(--spacing-2);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    color: var(--text-secondary);
}

.radio-group {
    display: flex;
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

/* Phones: the mode cards and the paired fields one above the other */
@media (max-width: 640px) {
    .mode-options {
        grid-template-columns: 1fr;
    }
}

@media (max-width: 480px) {
    .form-row {
        flex-direction: column;
        gap: 0;
    }

    .form-group-fixed {
        width: auto;
    }
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

.single-node {
    margin-left: var(--spacing-2);
}
</style>
