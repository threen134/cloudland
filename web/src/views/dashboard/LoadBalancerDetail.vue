<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { loadBalancersApi, subnetsApi, type LoadBalancer, type Subnet, type ListenerPayload, type Listener, type Backend } from '../../api/networks'
import { useToast } from '../../composables/useToast'
import { ArrowLeft, GitFork, Trash2, Plus, ChevronDown, ChevronRight, Pencil } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import { useGoBack } from '../../composables/useGoBack'

const { t } = useI18n()
const toast = useToast()
const route = useRoute()
const router = useRouter()
const goBack = useGoBack('load-balancers')
const lbId = route.params.id as string

const lb = ref<LoadBalancer | null>(null)
const loading = ref(true)
const error = ref('')

// Action dropdown
const showActionMenu = ref(false)
const toggleActionMenu = () => { showActionMenu.value = !showActionMenu.value }
const closeActionMenu = () => { showActionMenu.value = false }

// Edit modal
const showEditModal = ref(false)
const editForm = ref({ name: '', description: '' })
const editing = ref(false)
const editError = ref('')

const openEditModal = () => {
    if (!lb.value) return
    editForm.value = { name: lb.value.name, description: lb.value.description || '' }
    editError.value = ''
    showEditModal.value = true
    closeActionMenu()
}

const handleEdit = async () => {
    editing.value = true
    editError.value = ''
    try {
        await loadBalancersApi.patch(lbId, { name: editForm.value.name, description: editForm.value.description })
        showEditModal.value = false
        toast.success(t('messages.success'))
        await fetchLB()
    } catch (err: any) {
        editError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        editing.value = false
    }
}

// Expanded listener rows
const expandedListeners = ref<Set<string>>(new Set())

const fetchLB = async () => {
    loading.value = true
    error.value = ''
    try {
        lb.value = await loadBalancersApi.get(lbId)
    } catch {
        error.value = t('dashboard.loadBalancerDetail.loadError')
    } finally {
        loading.value = false
    }
}

// ─── Shared delete confirm modal ─────────────────────────────────────────────
const deleteModal = ref<{
    visible: boolean
    name: string
    loading: boolean
    error: string
    onConfirm: () => Promise<void>
}>({ visible: false, name: '', loading: false, error: '', onConfirm: async () => {} })

const openDeleteModal = (name: string, onConfirm: () => Promise<void>) => {
    deleteModal.value = { visible: true, name, loading: false, error: '', onConfirm }
}
const closeDeleteModal = () => { deleteModal.value.visible = false }
const confirmDelete = async () => {
    deleteModal.value.loading = true
    deleteModal.value.error = ''
    try {
        await deleteModal.value.onConfirm()
        closeDeleteModal()
    } catch (err: any) {
        deleteModal.value.error = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        deleteModal.value.loading = false
    }
}

const handleDelete = () => {
    openDeleteModal(lb.value?.name || lbId, async () => {
        await loadBalancersApi.delete(lbId)
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'load-balancers' })
    })
}

const toggleListener = (id: string) => {
    const next = new Set(expandedListeners.value)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    expandedListeners.value = next
}

// Backend health comes from the haproxy health checks of the master node, refreshed by heartbeats
const getHealthVariant = (health?: string) => {
    if (health === 'up') return 'success' as const
    if (health === 'down') return 'error' as const
    return 'pending' as const
}
const getHealthLabel = (health?: string) => {
    if (health === 'up') return t('dashboard.loadBalancerDetail.healthUp')
    if (health === 'down') return t('dashboard.loadBalancerDetail.healthDown')
    return t('dashboard.loadBalancerDetail.healthUnknown')
}
const countHealthy = (listener: Listener) => (listener.backends || []).filter(b => b.health === 'up').length

// ─── Floating IP ────────────────────────────────────────────────────────────

const showFipModal = ref(false)
const publicSubnets = ref<Subnet[]>([])
const loadingSubnets = ref(false)
const fipForm = ref({ name: '', subnet_id: '', inbound: null as number | null, outbound: null as number | null })
const addingFip = ref(false)
const fipError = ref('')

const openFipModal = () => {
    fipForm.value = { name: '', subnet_id: publicSubnets.value[0]?.id || '', inbound: null, outbound: null }
    fipError.value = ''
    showFipModal.value = true
}

// Empty means no bandwidth limit
const isValidBandwidth = (v: number | null) => v === null || (Number.isInteger(v) && v >= 1 && v <= 20000)

const handleAddFip = async () => {
    if (!fipForm.value.name || !fipForm.value.subnet_id) {
        fipError.value = t('messages.fillRequired')
        return
    }
    // v-model.number yields '' after the input is cleared
    const inbound = (fipForm.value.inbound as unknown) === '' ? null : fipForm.value.inbound
    const outbound = (fipForm.value.outbound as unknown) === '' ? null : fipForm.value.outbound
    if (!isValidBandwidth(inbound) || !isValidBandwidth(outbound)) {
        fipError.value = t('dashboard.loadBalancerDetail.invalidBandwidth')
        return
    }
    addingFip.value = true
    fipError.value = ''
    try {
        await loadBalancersApi.addFloatingIp(lbId, {
            name: fipForm.value.name,
            public_subnet: { id: fipForm.value.subnet_id },
            ...(inbound !== null ? { inbound } : {}),
            ...(outbound !== null ? { outbound } : {})
        })
        showFipModal.value = false
        toast.success(t('messages.createSuccess'))
        await fetchLB()
    } catch (err: any) {
        fipError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        addingFip.value = false
    }
}

const handleDeleteFip = (fipId: string, fipName: string) => {
    openDeleteModal(fipName || fipId, async () => {
        await loadBalancersApi.deleteFloatingIp(lbId, fipId)
        toast.success(t('messages.deleteSuccess'))
        await fetchLB()
    })
}

// ─── Listener ───────────────────────────────────────────────────────────────

const showListenerModal = ref(false)
const listenerForm = ref({ name: '', mode: 'http', port: 80, cert: '', key: '' })
const addingListener = ref(false)
const listenerError = ref('')

const openListenerModal = () => {
    listenerForm.value = { name: '', mode: 'http', port: 80, cert: '', key: '' }
    listenerError.value = ''
    showListenerModal.value = true
}

const handleAddListener = async () => {
    if (!listenerForm.value.name || !listenerForm.value.port) {
        listenerError.value = t('messages.fillRequired')
        return
    }
    addingListener.value = true
    listenerError.value = ''
    try {
        const payload: ListenerPayload = {
            name: listenerForm.value.name,
            mode: listenerForm.value.mode as 'http' | 'tcp',
            port: listenerForm.value.port
        }
        if (listenerForm.value.cert) payload.cert = listenerForm.value.cert
        if (listenerForm.value.key) payload.key = listenerForm.value.key
        await loadBalancersApi.addListener(lbId, payload)
        showListenerModal.value = false
        toast.success(t('messages.createSuccess'))
        await fetchLB()
    } catch (err: any) {
        listenerError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        addingListener.value = false
    }
}

const handleDeleteListener = (listenerId: string, listenerName: string) => {
    openDeleteModal(listenerName || listenerId, async () => {
        await loadBalancersApi.deleteListener(lbId, listenerId)
        toast.success(t('messages.deleteSuccess'))
        const next = new Set(expandedListeners.value)
        next.delete(listenerId)
        expandedListeners.value = next
        await fetchLB()
    })
}

const showListenerEditModal = ref(false)
const listenerEditForm = ref({ id: '', name: '' })
const savingListener = ref(false)
const listenerEditError = ref('')

const openListenerEditModal = (listener: Listener) => {
    listenerEditForm.value = { id: listener.id, name: listener.name }
    listenerEditError.value = ''
    showListenerEditModal.value = true
}

const handleEditListener = async () => {
    if (!listenerEditForm.value.name) {
        listenerEditError.value = t('messages.fillRequired')
        return
    }
    savingListener.value = true
    listenerEditError.value = ''
    try {
        await loadBalancersApi.patchListener(lbId, listenerEditForm.value.id, { name: listenerEditForm.value.name })
        showListenerEditModal.value = false
        toast.success(t('messages.success'))
        await fetchLB()
    } catch (err: any) {
        listenerEditError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        savingListener.value = false
    }
}

// ─── Backend ─────────────────────────────────────────────────────────────────

const showBackendModal = ref(false)
const currentListenerId = ref('')
// Empty when adding, the backend ID when editing
const editingBackendId = ref('')
const backendForm = ref({ name: '', address: '', port: '', ssl: false })
const addingBackend = ref(false)
const backendError = ref('')

const openBackendModal = (listenerId: string) => {
    currentListenerId.value = listenerId
    editingBackendId.value = ''
    backendForm.value = { name: '', address: '', port: '', ssl: false }
    backendError.value = ''
    showBackendModal.value = true
}

const openBackendEditModal = (listenerId: string, backend: Backend) => {
    currentListenerId.value = listenerId
    editingBackendId.value = backend.id
    // Endpoint is IP:port
    const sep = backend.endpoint.lastIndexOf(':')
    backendForm.value = {
        name: backend.name || '',
        address: sep > 0 ? backend.endpoint.slice(0, sep) : backend.endpoint,
        port: sep > 0 ? backend.endpoint.slice(sep + 1) : '',
        ssl: !!backend.ssl
    }
    backendError.value = ''
    showBackendModal.value = true
}

const handleSaveBackend = async () => {
    if (!backendForm.value.name || !backendForm.value.address || !backendForm.value.port) {
        backendError.value = t('messages.fillRequired')
        return
    }
    const portNum = Number(backendForm.value.port)
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
        backendError.value = t('messages.invalidPort')
        return
    }
    addingBackend.value = true
    backendError.value = ''
    try {
        const payload = {
            name: backendForm.value.name,
            endpoint: `${backendForm.value.address}:${portNum}`,
            ssl: backendForm.value.ssl
        }
        if (editingBackendId.value) {
            await loadBalancersApi.patchBackend(lbId, currentListenerId.value, editingBackendId.value, payload)
            toast.success(t('messages.success'))
        } else {
            await loadBalancersApi.addBackend(lbId, currentListenerId.value, payload)
            toast.success(t('messages.createSuccess'))
        }
        showBackendModal.value = false
        await fetchLB()
    } catch (err: any) {
        backendError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        addingBackend.value = false
    }
}

const handleDeleteBackend = (listenerId: string, backendId: string, backendName: string) => {
    openDeleteModal(backendName || backendId, async () => {
        await loadBalancersApi.deleteBackend(lbId, listenerId, backendId)
        toast.success(t('messages.deleteSuccess'))
        await fetchLB()
    })
}

onMounted(async () => {
    fetchLB()
    loadingSubnets.value = true
    try {
        const res = await subnetsApi.list({ limit: 200 })
        publicSubnets.value = (res.subnets || []).filter(s => s.type === 'public')
    } catch {
        // subnets unavailable; FIP binding will be disabled
    } finally {
        loadingSubnets.value = false
    }
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
            <button class="btn btn-primary" @click="fetchLB">{{ $t('dashboard.loadBalancerDetail.retry') }}</button>
        </div>

        <div v-else-if="lb" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="resource-icon-lg">
                    <GitFork :size="24" />
                </div>
                <div class="title-info">
                    <h1>{{ lb.name }}</h1>
                    <div class="subtitle">
                        <span class="id-text">{{ lb.id }}</span>
                        <StatusBadge :status="lb.status || 'inactive'" />
                    </div>
                </div>
                <div class="title-actions">
                    <div class="action-dropdown">
                        <button class="btn btn-primary" @click="toggleActionMenu">
                            {{ $t('actions.actions') }} <ChevronDown :size="14" />
                        </button>
                        <Transition name="dropdown">
                            <div v-if="showActionMenu" class="dropdown-menu" @click="closeActionMenu">
                                <button class="dropdown-item" @click="openEditModal">
                                    <Pencil :size="14" /> {{ $t('actions.edit') }}
                                </button>
                                <div class="dropdown-divider"></div>
                                <button class="dropdown-item dropdown-item-danger" @click="handleDelete">
                                    <Trash2 :size="14" /> {{ $t('actions.delete') }}
                                </button>
                            </div>
                        </Transition>
                        <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
                    </div>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.loadBalancerDetail.generalInfo') }}</h3>
                    <div class="key-value-list">
                        <InfoRow :label="$t('dashboard.loadBalancerDetail.name')">{{ lb.name }}</InfoRow>
                        <InfoRow v-if="lb.description" :label="$t('dashboard.forms.description')">{{ lb.description }}</InfoRow>
                        <InfoRow :label="$t('dashboard.loadBalancerDetail.vpc')">
                            <router-link v-if="lb.vpc" :to="{ name: 'vpc-detail', params: { id: lb.vpc.id } }" class="text-link">
                                {{ lb.vpc.name }}
                            </router-link>
                            <span v-else>-</span>
                        </InfoRow>
                        <InfoRow :label="$t('dashboard.loadBalancerDetail.createdAt')">{{ lb.created_at || '-' }}</InfoRow>
                    </div>
                </div>

                <!-- Floating IPs -->
                <div class="card info-card">
                    <div class="card-section-header">
                        <h3>{{ $t('dashboard.loadBalancerDetail.floatingIps') }}</h3>
                        <button class="btn btn-primary btn-sm" @click="openFipModal">
                            <Plus :size="13" /> {{ $t('dashboard.loadBalancerDetail.bindFloatingIp') }}
                        </button>
                    </div>
                    <div v-if="!lb.floating_ips || lb.floating_ips.length === 0" class="empty-hint">
                        {{ $t('dashboard.loadBalancerDetail.noFloatingIps') }}
                    </div>
                    <div v-else class="fip-list">
                        <div v-for="fip in lb.floating_ips" :key="fip.id" class="fip-item">
                            <code class="fip-addr">{{ fip.fip_address || '-' }}</code>
                            <span class="fip-name text-secondary text-sm">{{ fip.name }}</span>
                            <button
                                class="btn btn-ghost btn-xs text-error"
                                @click="handleDeleteFip(fip.id, fip.name || fip.fip_address || fip.id)"
                            >
                                <Trash2 :size="13" />
                            </button>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Listeners -->
            <div class="card listeners-card">
                <div class="card-section-header" style="padding: var(--spacing-4) var(--spacing-5);">
                    <h3 style="margin:0">{{ $t('dashboard.loadBalancerDetail.listeners') }}</h3>
                    <button class="btn btn-primary btn-sm" @click="openListenerModal">
                        <Plus :size="13" /> {{ $t('dashboard.loadBalancerDetail.addListener') }}
                    </button>
                </div>

                <div class="table-responsive">
                    <table class="data-table">
                        <thead>
                            <tr>
                                <th style="width:32px"></th>
                                <th>{{ $t('dashboard.table.name') }}</th>
                                <th>{{ $t('dashboard.loadBalancerDetail.protocol') }}</th>
                                <th>{{ $t('dashboard.table.status') }}</th>
                                <th>{{ $t('dashboard.loadBalancerDetail.backends') }}</th>
                                <th>{{ $t('dashboard.table.actions') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-if="!lb.listeners?.length">
                                <td colspan="6" class="text-center text-secondary" style="padding:32px">
                                    {{ $t('messages.noListeners') }}
                                </td>
                            </tr>
                            <template v-else v-for="listener in lb.listeners" :key="listener.id">
                                <!-- Listener row -->
                                <tr class="listener-row" @click="toggleListener(listener.id)">
                                    <td>
                                        <ChevronDown v-if="expandedListeners.has(listener.id)" :size="14" class="text-secondary" />
                                        <ChevronRight v-else :size="14" class="text-secondary" />
                                    </td>
                                    <td>{{ listener.name }}</td>
                                    <td><code class="mono">{{ listener.mode.toUpperCase() }}:{{ listener.port }}</code></td>
                                    <td>
                                        <StatusBadge :status="listener.status" :label="listener.status || '-'" />
                                    </td>
                                    <td class="text-secondary text-sm">
                                        {{ listener.backends?.length || 0 }} {{ $t('dashboard.loadBalancerDetail.backends') }}
                                        <span v-if="listener.backends?.length">{{ $t('dashboard.loadBalancerDetail.healthyCount', { count: countHealthy(listener) }) }}</span>
                                    </td>
                                    <td @click.stop>
                                        <button
                                            class="btn btn-ghost btn-sm"
                                            :title="$t('actions.edit')"
                                            @click="openListenerEditModal(listener)"
                                        >
                                            <Pencil :size="14" />
                                        </button>
                                        <button
                                            class="btn btn-ghost btn-sm text-error"
                                            @click="handleDeleteListener(listener.id, listener.name)"
                                        >
                                            <Trash2 :size="14" />
                                        </button>
                                    </td>
                                </tr>
                                <!-- Backends sub-table -->
                                <tr v-if="expandedListeners.has(listener.id)" class="backends-row">
                                    <td colspan="6" style="padding:0">
                                        <div class="backends-panel">
                                            <div class="backends-header">
                                                <span class="text-secondary text-sm">{{ $t('dashboard.loadBalancerDetail.backends') }}</span>
                                                <button class="btn btn-secondary btn-sm" @click="openBackendModal(listener.id)">
                                                    <Plus :size="13" /> {{ $t('dashboard.loadBalancerDetail.addBackend') }}
                                                </button>
                                            </div>
                                            <div v-if="!listener.backends?.length" class="empty-hint" style="padding:12px 16px">
                                                {{ $t('dashboard.loadBalancerDetail.noBackends') }}
                                            </div>
                                            <table v-else class="backends-table">
                                                <thead>
                                                    <tr>
                                                        <th>{{ $t('dashboard.table.name') }}</th>
                                                        <th>{{ $t('dashboard.loadBalancerDetail.endpoint') }}</th>
                                                        <th>{{ $t('dashboard.loadBalancerDetail.backendSsl') }}</th>
                                                        <th>{{ $t('dashboard.loadBalancerDetail.health') }}</th>
                                                        <th></th>
                                                    </tr>
                                                </thead>
                                                <tbody>
                                                    <tr v-for="backend in listener.backends" :key="backend.id">
                                                        <td>{{ backend.name || '-' }}</td>
                                                        <td><code class="mono">{{ backend.endpoint }}</code></td>
                                                        <td>{{ backend.ssl ? $t('dashboard.loadBalancerDetail.sslOn') : $t('dashboard.loadBalancerDetail.sslOff') }}</td>
                                                        <td>
                                                            <StatusBadge :variant="getHealthVariant(backend.health)" :label="getHealthLabel(backend.health)" />
                                                        </td>
                                                        <td class="backend-actions">
                                                            <button
                                                                class="btn btn-ghost btn-sm"
                                                                :title="$t('actions.edit')"
                                                                @click="openBackendEditModal(listener.id, backend)"
                                                            >
                                                                <Pencil :size="13" />
                                                            </button>
                                                            <button
                                                                class="btn btn-ghost btn-sm text-error"
                                                                @click="handleDeleteBackend(listener.id, backend.id, backend.name || backend.endpoint)"
                                                            >
                                                                <Trash2 :size="13" />
                                                            </button>
                                                        </td>
                                                    </tr>
                                                </tbody>
                                            </table>
                                        </div>
                                    </td>
                                </tr>
                            </template>
                        </tbody>
                    </table>
                </div>
            </div>
        </div>

        <!-- Bind Floating IP Modal -->
        <BaseModal
            :show="showFipModal"
            :title="$t('dashboard.loadBalancerDetail.bindFloatingIp')"
            :loading="addingFip"
            form
            @close="showFipModal = false"
            @submit="handleAddFip"
        >
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                        <input v-model="fipForm.name" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.lbFipNameExample')" />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.loadBalancerDetail.publicSubnet') }} *</label>
                        <div v-if="loadingSubnets" class="text-secondary text-sm">{{ $t('messages.loading') }}</div>
                        <div v-else-if="publicSubnets.length === 0" class="text-secondary text-sm">{{ $t('dashboard.loadBalancerDetail.noPublicSubnets') }}</div>
                        <div v-else class="select-wrapper">
                            <select v-model="fipForm.subnet_id" class="form-input">
                                <option v-for="s in publicSubnets" :key="s.id" :value="s.id">
                                    {{ s.name }} ({{ s.network_cidr || s.network }})
                                </option>
                            </select>
                        </div>
                    </div>
                    <div class="form-row">
                        <div class="form-group form-group-grow">
                            <label class="form-label">{{ $t('dashboard.floatingIPDetail.inbound') }}</label>
                            <div class="input-with-suffix">
                                <input v-model.number="fipForm.inbound" type="number" min="1" max="20000" class="form-input" :placeholder="$t('dashboard.floatingIPDetail.inboundPlaceholder')" />
                                <span class="input-suffix">{{ $t('dashboard.floatingIPDetail.mbps') }}</span>
                            </div>
                        </div>
                        <div class="form-group form-group-grow">
                            <label class="form-label">{{ $t('dashboard.floatingIPDetail.outbound') }}</label>
                            <div class="input-with-suffix">
                                <input v-model.number="fipForm.outbound" type="number" min="1" max="20000" class="form-input" :placeholder="$t('dashboard.floatingIPDetail.outboundPlaceholder')" />
                                <span class="input-suffix">{{ $t('dashboard.floatingIPDetail.mbps') }}</span>
                            </div>
                        </div>
                    </div>
                    <div class="form-hint">{{ $t('dashboard.loadBalancerDetail.bandwidthHint') }}</div>

                    <div v-if="fipError" class="modal-error">{{ fipError }}</div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showFipModal = false" :disabled="addingFip">{{ $t('actions.cancel') }}</button>
                <button type="submit" class="btn btn-primary" :disabled="addingFip || loadingSubnets || publicSubnets.length === 0">
                    <span v-if="addingFip" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
                    {{ addingFip ? $t('dashboard.loadBalancerDetail.bindingFloatingIp') : $t('dashboard.loadBalancerDetail.bindFloatingIp') }}
                </button>
            </template>
        </BaseModal>

        <!-- Add Listener Modal -->
        <BaseModal
            :show="showListenerModal"
            :title="$t('dashboard.loadBalancerDetail.addListener')"
            :loading="addingListener"
            form
            @close="showListenerModal = false"
            @submit="handleAddListener"
        >
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                        <input v-model="listenerForm.name" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.lbListenerNameExample')" />
                    </div>
                    <div class="form-row">
                        <div class="form-group">
                            <label class="form-label">{{ $t('dashboard.loadBalancerDetail.mode') }} *</label>
                            <div class="select-wrapper">
                                <select v-model="listenerForm.mode" class="form-input">
                                    <option value="http">HTTP</option>
                                    <option value="tcp">TCP</option>
                                </select>
                            </div>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ $t('dashboard.loadBalancerDetail.port') }} *</label>
                            <input v-model.number="listenerForm.port" type="number" min="1" max="65535" class="form-input" placeholder="80" />
                        </div>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.loadBalancerDetail.cert') }}</label>
                        <textarea v-model="listenerForm.cert" class="form-input form-textarea" :placeholder="$t('dashboard.loadBalancerDetail.certHint')"></textarea>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.loadBalancerDetail.privateKey') }}</label>
                        <textarea v-model="listenerForm.key" class="form-input form-textarea" :placeholder="$t('dashboard.loadBalancerDetail.privateKeyHint')"></textarea>
                    </div>

                    <div v-if="listenerError" class="modal-error">{{ listenerError }}</div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showListenerModal = false" :disabled="addingListener">{{ $t('actions.cancel') }}</button>
                <button type="submit" class="btn btn-primary" :disabled="addingListener">
                    <span v-if="addingListener" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
                    {{ addingListener ? $t('dashboard.loadBalancerDetail.addingListener') : $t('dashboard.loadBalancerDetail.addListener') }}
                </button>
            </template>
        </BaseModal>

        <!-- Edit Listener Modal -->
        <BaseModal
            :show="showListenerEditModal"
            :title="$t('dashboard.loadBalancerDetail.editListener')"
            :loading="savingListener"
            form
            @close="showListenerEditModal = false"
            @submit="handleEditListener"
        >
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                        <input v-model="listenerEditForm.name" type="text" class="form-input" />
                    </div>

                    <div v-if="listenerEditError" class="modal-error">{{ listenerEditError }}</div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showListenerEditModal = false" :disabled="savingListener">{{ $t('actions.cancel') }}</button>
                <button type="submit" class="btn btn-primary" :disabled="savingListener || !listenerEditForm.name">
                    <span v-if="savingListener" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
                    {{ savingListener ? $t('messages.saving') : $t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <!-- Add / Edit Backend Modal -->
        <BaseModal
            :show="showBackendModal"
            :title="editingBackendId ? $t('dashboard.loadBalancerDetail.editBackend') : $t('dashboard.loadBalancerDetail.addBackend')"
            :loading="addingBackend"
            form
            @close="showBackendModal = false"
            @submit="handleSaveBackend"
        >
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                        <input v-model="backendForm.name" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.lbBackendNameExample')" />
                    </div>
                    <div class="form-row">
                        <div class="form-group form-group-grow">
                            <label class="form-label">{{ $t('dashboard.loadBalancerDetail.ipAddress') }} *</label>
                            <input v-model="backendForm.address" type="text" class="form-input" placeholder="192.168.1.10" />
                        </div>
                        <div class="form-group form-group-fixed">
                            <label class="form-label">{{ $t('dashboard.loadBalancerDetail.port') }} *</label>
                            <input v-model="backendForm.port" type="number" class="form-input" placeholder="8080" min="1" max="65535" />
                        </div>
                    </div>
                    <div class="form-group">
                        <label class="checkbox-label">
                            <input v-model="backendForm.ssl" type="checkbox" />
                            {{ $t('dashboard.loadBalancerDetail.backendSslLabel') }}
                        </label>
                        <div class="form-hint">{{ $t('dashboard.loadBalancerDetail.backendSslHint') }}</div>
                    </div>

                    <div v-if="backendError" class="modal-error">{{ backendError }}</div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showBackendModal = false" :disabled="addingBackend">{{ $t('actions.cancel') }}</button>
                <button type="submit" class="btn btn-primary" :disabled="addingBackend">
                    <span v-if="addingBackend" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
                    <template v-if="editingBackendId">{{ addingBackend ? $t('messages.saving') : $t('actions.save') }}</template>
                    <template v-else>{{ addingBackend ? $t('dashboard.loadBalancerDetail.addingBackend') : $t('dashboard.loadBalancerDetail.addBackend') }}</template>
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleteModal.visible"
            :resource-name="deleteModal.name"
            :loading="deleteModal.loading"
            :error="deleteModal.error"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />

        <!-- Edit Modal -->
        <BaseModal
            :show="showEditModal"
            :title="$t('actions.edit')"
            :loading="editing"
            form
            @close="showEditModal = false"
            @submit="handleEdit"
        >
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
                        <input v-model="editForm.name" type="text" class="form-input" />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
                        <input v-model="editForm.description" type="text" class="form-input" :placeholder="$t('messages.placeholderDescription')" />
                    </div>

                    <div v-if="editError" class="modal-error">{{ editError }}</div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showEditModal = false" :disabled="editing">{{ $t('actions.cancel') }}</button>
                <button type="submit" class="btn btn-primary" :disabled="editing || !editForm.name">
                    <span v-if="editing" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
                    {{ editing ? $t('messages.saving') : $t('actions.save') }}
                </button>
            </template>
        </BaseModal>
    </div>
</template>

<style scoped>
.detail-page {
    max-width: 1200px;
    margin: 0 auto;
}
.detail-header {
    margin-bottom: var(--spacing-4);
}
.loading-container, .error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px;
}

/* Title Bar */
.title-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
    padding: var(--spacing-6);
    margin-bottom: var(--spacing-6);
}
.resource-icon-lg {
    width: 48px;
    height: 48px;
    background: var(--bg-tertiary);
    color: var(--primary-color);
    border-radius: var(--radius-md);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
}
.title-info { flex: 1; }
.title-info h1 {
    font-size: var(--font-size-xl);
    font-weight: 600;
    margin: 0 0 4px 0;
    color: var(--text-primary);
}
.subtitle {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    font-size: var(--font-size-sm);
}
.id-text {
    font-family: var(--font-family-mono);
    color: var(--text-secondary);
}

/* Info Grid */
.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-6);
}
.info-card { padding: var(--spacing-5); }
.info-card h3 {
    font-size: var(--font-size-base);
    font-weight: 600;
    margin: 0 0 var(--spacing-4) 0;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-3);
}
.card-section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-4);
}
.card-section-header h3 {
    border-bottom: none;
    padding-bottom: 0;
    margin-bottom: 0;
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
.text-link:hover { text-decoration: underline; }
.empty-hint {
    color: var(--text-tertiary);
    font-size: var(--font-size-sm);
    font-style: italic;
}

/* Floating IPs */
.fip-list { display: flex; flex-direction: column; gap: var(--spacing-2); }
.fip-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    padding: var(--spacing-2) var(--spacing-3);
    background: var(--bg-secondary);
    border-radius: var(--radius-sm);
}
.fip-addr {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-sm);
    flex-shrink: 0;
}
.fip-name { flex: 1; }

/* Listeners */
.listeners-card { padding: 0; overflow: hidden; margin-bottom: var(--spacing-6); }
.table-responsive { overflow-x: auto; }
.listener-row { cursor: pointer; }
.listener-row:hover { background: var(--bg-secondary); }
.backends-row { background: var(--bg-secondary); }

/* Backends panel */
.backends-panel {
    border-top: 1px solid var(--border-light);
    padding: var(--spacing-3) var(--spacing-5);
}
.backends-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-2);
}
.backends-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--font-size-sm);
}
.backends-table th {
    text-align: left;
    padding: 6px 12px;
    color: var(--text-secondary);
    font-weight: 500;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
}
.backends-table td {
    padding: 6px 12px;
    border-top: 1px solid var(--border-light);
}
.backends-table tr:first-child td { border-top: none; }

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
.form-group-grow { flex: 1; }
.form-group-fixed { width: 120px; flex-shrink: 0; }
.form-textarea {
    min-height: 80px;
    resize: vertical;
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
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
.input-with-suffix {
    position: relative;
    display: flex;
    align-items: center;
}
.input-with-suffix .form-input { padding-right: 52px; }
.input-suffix {
    position: absolute;
    right: 12px;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    pointer-events: none;
}
.backend-actions { white-space: nowrap; text-align: right; }

/* Action Dropdown */
.action-dropdown { position: relative; }
.dropdown-backdrop { position: fixed; inset: 0; z-index: 9; }
.dropdown-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 160px;
    background: var(--bg-primary, #fff);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0,0,0,.12);
    padding: 4px 0;
    z-index: 10;
}
.dropdown-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 14px;
    border: none;
    background: none;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
    transition: background 0.15s;
    text-align: left;
}
.dropdown-item:hover:not(:disabled) { background: var(--bg-hover, #f3f4f6); }
.dropdown-item:disabled { opacity: 0.4; cursor: not-allowed; }
.dropdown-item-danger { color: var(--error-color, #ef4444); }
.dropdown-item-danger:hover:not(:disabled) { background: #fef2f2; }
.dropdown-divider { height: 1px; background: var(--border-light); margin: 4px 0; }
.dropdown-enter-active, .dropdown-leave-active { transition: opacity 0.12s, transform 0.12s; }
.dropdown-enter-from, .dropdown-leave-to { opacity: 0; transform: translateY(-4px); }

/* Misc */
.mono { font-family: var(--font-family-mono); }
.btn-xs { padding: 2px 6px; font-size: 11px; }
</style>
