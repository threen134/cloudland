<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { floatingIpsApi, subnetsApi, type FloatingIP, type FloatingIPPayload, type Subnet } from '../../api/networks'
import { instancesApi, type Instance } from '../../api/instances'
import {
    Globe2,
    Plus,
    Link,
    Unlink,
    Trash2,
    Search,
    RefreshCw,
    Check,
    Copy,
    ChevronDown,
    ChevronUp,
} from 'lucide-vue-next'
import { useFloatingIP } from '../../composables/useFloatingIP'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import { useRegionStore } from '../../stores/region'
import { quotaErrorMessage } from '../../utils/quotaError'
import { errorMessage } from '../../utils/error'

// GET /addresses/:subnet 的返回项（api/networks.ts 的 listAddresses 尚未定型，这里先声明本页用到的字段）
interface SubnetAddress {
    address: string
    allocated?: boolean
    reserved?: boolean
}

const region = useRegionStore()

const { t, te } = useI18n()
const toast = useToast()
const { getTypeBadgeClass, getTypeLabel } = useFloatingIP()
const router = useRouter()

const { copiedId, copyId } = useCopyId()
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const showAdvanced = ref(false)

const newFipForm = ref({
    name: '',
    selectedSiteSubnetId: '',
    selectedPublicSubnetId: '',
    inbound: null as number | null,
    outbound: null as number | null,
    instanceId: '',
    publicIp: '',
    activationCount: null as number | null,
})

const siteSubnets = ref<Subnet[]>([])
const publicSubnets = ref<Subnet[]>([])
const instances = ref<Instance[]>([])
const subnetAddresses = ref<Record<string, SubnetAddress[]>>({})
const addressesLoading = ref<Record<string, boolean>>({})

const fetchSubnetAddresses = async (subnetId: string) => {
    if (!subnetId) return
    addressesLoading.value[subnetId] = true
    try {
        const response = await subnetsApi.listAddresses(subnetId)
        subnetAddresses.value[subnetId] = (response.addresses || []).filter(
            (a: SubnetAddress) => !a.allocated && !a.reserved
        )
    } catch (err) {
        console.error('Failed to fetch subnet addresses:', err)
    } finally {
        addressesLoading.value[subnetId] = false
    }
}

// 排序在服务端做（sortField 是数据库列名，和列 key 不一定同名）。
// 「挂载到」的虚拟机名在 instances 表里，浮动 IP 表上没有对应列，不提供排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.userName'), sortable: true },
    { key: 'ip', label: t('dashboard.table.ipAddress'), sortable: true, sortField: 'fip_address' },
    { key: 'type', label: t('dashboard.table.type'), sortable: true },
    { key: 'attachedTo', label: t('dashboard.table.attachedTo') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// 分页与搜索都在服务端做
const {
    items: floatingIps,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchFloatingIPs,
    reload: reloadFloatingIPs,
} = useListQuery<FloatingIP>(
    async ({ offset, limit, query, order }) => {
        const response = await floatingIpsApi.list({ offset, limit, order, query: query || undefined })
        return { items: response.floating_ips || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

// 新建弹窗里的子网下拉；原先随列表一起拉，分页后改为挂载时和打开弹窗时各拉一次
const fetchSubnetOptions = async () => {
    try {
        const subnetsResponse = await subnetsApi.list()
        const allSubnets = subnetsResponse.subnets || []
        siteSubnets.value = allSubnets.filter((s) => s.type === 'site')
        publicSubnets.value = allSubnets.filter((s) => s.type === 'public')
    } catch (err) {
        console.error('Failed to fetch subnets:', err)
        siteSubnets.value = []
        publicSubnets.value = []
    }
}

const fetchInstances = async () => {
    try {
        const response = await instancesApi.fetchInstances()
        instances.value = (response?.instances || []).filter(
            (inst: Instance) =>
                inst.vpc && inst.status !== 'provisioning' && inst.interfaces?.some((iface) => iface.is_primary)
        )
    } catch (err) {
        console.error('Failed to fetch instances:', err)
        instances.value = []
    }
}

const openCreateModal = () => {
    newFipForm.value = {
        name: '',
        selectedSiteSubnetId: '',
        selectedPublicSubnetId: '',
        inbound: null,
        outbound: null,
        instanceId: '',
        publicIp: '',
        activationCount: null,
    }
    subnetAddresses.value = {}
    showAdvanced.value = false
    createError.value = ''
    createModalVisible.value = true
    fetchInstances()
    fetchSubnetOptions()
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateIP = async () => {
    createError.value = ''
    const form = newFipForm.value

    if (!form.name || form.name.length < 2 || form.name.length > 32) {
        createError.value = t('dashboard.floatingIPDetail.nameRequired')
        return
    }

    creating.value = true
    try {
        const payload: FloatingIPPayload = {
            name: form.name,
        }

        if (form.selectedSiteSubnetId) {
            payload.site_subnets = [{ id: form.selectedSiteSubnetId }]
        }
        if (form.selectedPublicSubnetId) {
            payload.public_subnets = [{ id: form.selectedPublicSubnetId }]
        }
        if (form.inbound && form.inbound > 0) {
            payload.inbound = form.inbound
        }
        if (form.outbound && form.outbound > 0) {
            payload.outbound = form.outbound
        }
        if (form.instanceId) {
            payload.instance = { id: form.instanceId }
        }
        if (form.publicIp) {
            if (!/^(\d{1,3}\.){3}\d{1,3}$/.test(form.publicIp)) {
                createError.value = t('dashboard.floatingIPDetail.invalidIp')
                creating.value = false
                return
            }
            payload.public_ip = form.publicIp
        }
        if (form.activationCount && form.activationCount > 0) {
            payload.activation_count = form.activationCount
        }

        await floatingIpsApi.create(payload)

        // 列表按创建时间倒序，新建的在第一页
        await reloadFloatingIPs()

        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create floating IP:', err)
        createError.value = quotaErrorMessage(err, t, te) || errorMessage(err, t('messages.error'))
    } finally {
        creating.value = false
    }
}

const navigateToInstance = (instanceId: string) => {
    router.push({ name: 'instance-detail', params: { id: instanceId } })
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<FloatingIP | null>(null)

const handleDeleteClick = (item: FloatingIP) => {
    resourceToDelete.value = item
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
        await floatingIpsApi.delete(resourceToDelete.value.id)
        await fetchFloatingIPs()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error) {
        console.error('Failed to delete floating IP:', error)
        deleteError.value = errorMessage(error, t('messages.error'))
    } finally {
        deletingResource.value = false
    }
}

// --- Attach Modal ---
const attachModalVisible = ref(false)
const attaching = ref(false)
const attachError = ref('')
const fipToAttach = ref<FloatingIP | null>(null)
const selectedInstanceId = ref('')

const handleAttachClick = (fip: FloatingIP) => {
    fipToAttach.value = fip
    selectedInstanceId.value = ''
    attachError.value = ''
    attachModalVisible.value = true
    fetchInstances()
}

const closeAttachModal = () => {
    attachModalVisible.value = false
    fipToAttach.value = null
    attachError.value = ''
}

const confirmAttach = async () => {
    if (!fipToAttach.value || !selectedInstanceId.value) {
        attachError.value = t('dashboard.floatingIPDetail.selectInstance')
        return
    }
    attaching.value = true
    attachError.value = ''
    try {
        await floatingIpsApi.attach(fipToAttach.value.id, selectedInstanceId.value)
        await fetchFloatingIPs()
        closeAttachModal()
        toast.success(t('messages.attachSuccess') || t('messages.updateSuccess'))
    } catch (err) {
        console.error('Failed to attach floating IP:', err)
        attachError.value = errorMessage(err, t('messages.error'))
    } finally {
        attaching.value = false
    }
}

// --- Detach Confirmation Modal ---
const detachModalVisible = ref(false)
const detaching = ref(false)
const detachError = ref('')
const fipToDetach = ref<FloatingIP | null>(null)

const handleDetachClick = (fip: FloatingIP) => {
    fipToDetach.value = fip
    detachError.value = ''
    detachModalVisible.value = true
}

const closeDetachModal = () => {
    detachModalVisible.value = false
    fipToDetach.value = null
    detachError.value = ''
}

const confirmDetach = async () => {
    if (!fipToDetach.value) return
    detaching.value = true
    detachError.value = ''
    try {
        await floatingIpsApi.detach(fipToDetach.value.id)
        await fetchFloatingIPs()
        closeDetachModal()
        toast.success(t('messages.detachSuccess') || t('messages.updateSuccess'))
    } catch (err) {
        console.error('Failed to detach floating IP:', err)
        detachError.value = errorMessage(err, t('messages.error'))
    } finally {
        detaching.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchFloatingIPs()
        fetchSubnetOptions()
    }
})

watch(
    () => newFipForm.value.selectedSiteSubnetId,
    (newId) => {
        if (newId) {
            newFipForm.value.selectedPublicSubnetId = ''
            fetchSubnetAddresses(newId)
        }
    }
)

watch(
    () => newFipForm.value.selectedPublicSubnetId,
    (newId) => {
        if (newId) {
            newFipForm.value.selectedSiteSubnetId = ''
            fetchSubnetAddresses(newId)
        }
    }
)
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="() => fetchFloatingIPs()"
                    :title="$t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.createIp') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="floatingIps"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="() => fetchFloatingIPs()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <Globe2 :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noFloatingIPs') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: fip }">
                <router-link :to="{ name: 'floating-ip-detail', params: { id: fip.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <Globe2 :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ fip.name || $t('messages.unnamed') }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="fip.id">{{ fip.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(fip.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check v-if="copiedId === fip.id" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-ip="{ row: fip }">
                <div class="ip-address monospace">{{ fip.public_ip || fip.ip_address }}</div>
            </template>

            <template #cell-type="{ row: fip }">
                <span :class="['badge', getTypeBadgeClass(fip.type || '')]">
                    {{ getTypeLabel(fip.type || '') }}
                </span>
            </template>

            <template #cell-attachedTo="{ row: fip }">
                <span
                    v-if="fip.target_interface?.from_instance"
                    class="resource-link"
                    @click="navigateToInstance(fip.target_interface.from_instance.id)"
                >
                    {{ fip.target_interface.from_instance.hostname || $t('messages.unnamed') }}
                </span>
                <span v-else class="text-light">{{ $t('messages.notAttached') }}</span>
            </template>

            <template #cell-actions="{ row: fip }">
                <div class="actions">
                    <button
                        v-if="!fip.target_interface"
                        class="btn btn-ghost btn-sm"
                        :title="$t('actions.attach')"
                        :disabled="fip.type !== 'floating' && fip.type !== 'site'"
                        @click="(fip.type === 'floating' || fip.type === 'site') && handleAttachClick(fip)"
                    >
                        <Link :size="14" /> {{ $t('actions.attach') }}
                    </button>
                    <button
                        v-else
                        class="btn btn-ghost btn-sm"
                        :title="$t('actions.detach')"
                        :disabled="fip.type !== 'floating' && fip.type !== 'site'"
                        @click="(fip.type === 'floating' || fip.type === 'site') && handleDetachClick(fip)"
                    >
                        <Unlink :size="14" /> {{ $t('actions.detach') }}
                    </button>
                    <button
                        class="btn btn-ghost btn-sm text-error"
                        :title="$t('actions.release')"
                        :disabled="fip.type !== 'floating' && fip.type !== 'loadbalancer'"
                        @click="(fip.type === 'floating' || fip.type === 'loadbalancer') && handleDeleteClick(fip)"
                    >
                        <Trash2 :size="14" />
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

        <!-- Create Floating IP Modal -->
        <BaseModal
            :show="createModalVisible"
            :title="$t('dashboard.buttons.createIp')"
            size="lg"
            form
            :loading="creating"
            @close="closeCreateModal"
            @submit="handleCreateIP"
        >
            <p class="text-secondary mb-4">
                {{ $t('dashboard.floatingIPDetail.createDesc') }}
            </p>

            <!-- Name (required) -->
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.name') }} <span class="text-error">*</span></label>
                <input
                    id="ipName"
                    name="ipName"
                    v-model="newFipForm.name"
                    type="text"
                    class="form-input"
                    maxlength="32"
                    :placeholder="$t('dashboard.floatingIPDetail.namePlaceholder')"
                />
            </div>

            <!-- Bandwidth -->
            <div class="form-row">
                <div class="form-group form-group-half">
                    <label class="form-label">{{ $t('dashboard.floatingIPDetail.inbound') }}</label>
                    <div class="input-with-suffix">
                        <input
                            id="inbound"
                            name="inbound"
                            v-model.number="newFipForm.inbound"
                            type="number"
                            class="form-input"
                            min="1"
                            max="20000"
                            :placeholder="$t('dashboard.floatingIPDetail.inboundPlaceholder')"
                        />
                        <span class="input-suffix">{{ $t('dashboard.floatingIPDetail.mbps') }}</span>
                    </div>
                </div>
                <div class="form-group form-group-half">
                    <label class="form-label">{{ $t('dashboard.floatingIPDetail.outbound') }}</label>
                    <div class="input-with-suffix">
                        <input
                            id="outbound"
                            name="outbound"
                            v-model.number="newFipForm.outbound"
                            type="number"
                            class="form-input"
                            min="1"
                            max="20000"
                            :placeholder="$t('dashboard.floatingIPDetail.outboundPlaceholder')"
                        />
                        <span class="input-suffix">{{ $t('dashboard.floatingIPDetail.mbps') }}</span>
                    </div>
                </div>
            </div>

            <!-- Site Subnet -->
            <div class="form-group" v-if="siteSubnets.length > 0">
                <label class="form-label">{{ $t('dashboard.floatingIPDetail.subnetPool') }}</label>
                <div class="select-wrapper">
                    <select
                        id="siteSubnet"
                        name="siteSubnet"
                        v-model="newFipForm.selectedSiteSubnetId"
                        class="form-input"
                    >
                        <option value="">{{ $t('dashboard.floatingIPDetail.selectSubnet') }}</option>
                        <option v-for="subnet in siteSubnets" :key="subnet.id" :value="subnet.id">
                            {{ subnet.name }} ({{ subnet.network || subnet.network_cidr }})
                        </option>
                    </select>
                </div>
            </div>

            <!-- Public Subnet -->
            <div class="form-group" v-if="publicSubnets.length > 0">
                <label class="form-label">{{ $t('dashboard.floatingIPDetail.publicSubnet') }}</label>
                <div class="select-wrapper">
                    <select
                        id="publicSubnet"
                        name="publicSubnet"
                        v-model="newFipForm.selectedPublicSubnetId"
                        class="form-input"
                    >
                        <option value="">{{ $t('dashboard.floatingIPDetail.selectSubnet') }}</option>
                        <option v-for="subnet in publicSubnets" :key="subnet.id" :value="subnet.id">
                            {{ subnet.name }} ({{ subnet.network || subnet.network_cidr }})
                        </option>
                    </select>
                </div>
            </div>

            <!-- Public IP -->
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.floatingIPDetail.publicIp') }}</label>
                <div class="select-wrapper" v-if="newFipForm.selectedSiteSubnetId || newFipForm.selectedPublicSubnetId">
                    <select
                        id="publicIpSelect"
                        name="publicIp"
                        v-model="newFipForm.publicIp"
                        class="form-input"
                        :disabled="
                            addressesLoading[newFipForm.selectedSiteSubnetId || newFipForm.selectedPublicSubnetId]
                        "
                    >
                        <option value="">{{ $t('dashboard.floatingIPDetail.autoAllocate') || '自动分配' }}</option>
                        <option
                            v-for="addr in subnetAddresses[
                                newFipForm.selectedSiteSubnetId || newFipForm.selectedPublicSubnetId
                            ] || []"
                            :key="addr.address"
                            :value="addr.address.split('/')[0]"
                        >
                            {{ addr.address.split('/')[0] }}
                        </option>
                    </select>
                </div>
                <input
                    id="publicIpInput"
                    name="publicIp"
                    v-model="newFipForm.publicIp"
                    type="text"
                    class="form-input"
                    :placeholder="$t('dashboard.floatingIPDetail.publicIpPlaceholder')"
                />
            </div>

            <!-- Bind Instance -->
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.floatingIPDetail.bindInstance') }}</label>
                <div class="select-wrapper">
                    <select id="instanceId" name="instanceId" v-model="newFipForm.instanceId" class="form-input">
                        <option value="">{{ $t('dashboard.floatingIPDetail.selectInstance') }}</option>
                        <option v-for="inst in instances" :key="inst.id" :value="inst.id">
                            {{ inst.hostname || inst.name }} ({{ inst.ip_address || inst.id }})
                        </option>
                    </select>
                </div>
            </div>

            <!-- Advanced Options -->
            <div class="advanced-toggle" @click="showAdvanced = !showAdvanced">
                <span>{{ $t('dashboard.floatingIPDetail.advancedOptions') }}</span>
                <ChevronDown v-if="!showAdvanced" :size="16" />
                <ChevronUp v-else :size="16" />
            </div>

            <div v-if="showAdvanced" class="advanced-section">
                <!-- Activation Count -->
                <div class="form-group">
                    <label class="form-label">{{ $t('dashboard.floatingIPDetail.activationCount') }}</label>
                    <input
                        id="activationCount"
                        name="activationCount"
                        v-model.number="newFipForm.activationCount"
                        type="number"
                        class="form-input"
                        min="1"
                        max="64"
                        :placeholder="$t('dashboard.floatingIPDetail.activationCountPlaceholder')"
                    />
                </div>
            </div>

            <template #footer>
                <div class="modal-footer-stack">
                    <div
                        v-if="createError"
                        class="text-error"
                        style="
                            font-size: var(--font-size-sm);
                            background: var(--error-light);
                            padding: var(--spacing-2);
                            border-radius: var(--radius-sm);
                        "
                    >
                        {{ createError }}
                    </div>
                    <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2)">
                        <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">
                            {{ $t('actions.cancel') }}
                        </button>
                        <button type="submit" class="btn btn-primary" :disabled="creating">
                            <span
                                v-if="creating"
                                class="loading-spinner"
                                style="width: 16px; height: 16px; border-width: 2px"
                            ></span>
                            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createIp') }}
                        </button>
                    </div>
                </div>
            </template>
        </BaseModal>

        <!-- Attach Floating IP Modal -->
        <BaseModal
            :show="attachModalVisible"
            :title="$t('actions.attach') + ' - ' + (fipToAttach?.name || fipToAttach?.public_ip || '')"
            form
            :loading="attaching"
            @close="closeAttachModal"
            @submit="confirmAttach"
        >
            <div class="form-group">
                <label class="form-label"
                    >{{ $t('dashboard.floatingIPDetail.bindInstance') }} <span class="text-error">*</span></label
                >
                <div class="select-wrapper">
                    <select id="attachInstanceId" name="instanceId" v-model="selectedInstanceId" class="form-input">
                        <option value="">{{ $t('dashboard.floatingIPDetail.selectInstance') }}</option>
                        <option v-for="inst in instances" :key="inst.id" :value="inst.id">
                            {{ inst.hostname || inst.name }} ({{ inst.ip_address || inst.id }})
                        </option>
                    </select>
                </div>
            </div>

            <template #footer>
                <div class="modal-footer-stack">
                    <div
                        v-if="attachError"
                        class="text-error"
                        style="
                            font-size: var(--font-size-sm);
                            background: var(--error-light);
                            padding: var(--spacing-2);
                            border-radius: var(--radius-sm);
                        "
                    >
                        {{ attachError }}
                    </div>
                    <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2)">
                        <button type="button" class="btn btn-secondary" @click="closeAttachModal" :disabled="attaching">
                            {{ $t('actions.cancel') }}
                        </button>
                        <button type="submit" class="btn btn-primary" :disabled="attaching || !selectedInstanceId">
                            <span
                                v-if="attaching"
                                class="loading-spinner"
                                style="width: 16px; height: 16px; border-width: 2px"
                            ></span>
                            {{ attaching ? $t('messages.loading') : $t('actions.attach') }}
                        </button>
                    </div>
                </div>
            </template>
        </BaseModal>

        <!-- Detach Confirmation Modal -->
        <BaseModal
            :show="detachModalVisible"
            :title="$t('actions.detach') + ' - ' + (fipToDetach?.name || fipToDetach?.public_ip || '')"
            :loading="detaching"
            @close="closeDetachModal"
        >
            <p class="text-secondary">
                {{ $t('messages.confirmDetach') }}
            </p>

            <template #footer>
                <div class="modal-footer-stack">
                    <div
                        v-if="detachError"
                        class="text-error"
                        style="
                            font-size: var(--font-size-sm);
                            background: var(--error-light);
                            padding: var(--spacing-2);
                            border-radius: var(--radius-sm);
                        "
                    >
                        {{ detachError }}
                    </div>
                    <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2)">
                        <button class="btn btn-secondary" @click="closeDetachModal" :disabled="detaching">
                            {{ $t('actions.cancel') }}
                        </button>
                        <button class="btn btn-primary" @click="confirmDetach" :disabled="detaching">
                            <span
                                v-if="detaching"
                                class="loading-spinner"
                                style="width: 16px; height: 16px; border-width: 2px"
                            ></span>
                            {{ detaching ? $t('messages.loading') : $t('actions.detach') }}
                        </button>
                    </div>
                </div>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleteModalVisible"
            :resource-name="resourceToDelete?.public_ip || resourceToDelete?.ip_address"
            :resource-id="resourceToDelete?.id"
            :loading="deletingResource"
            :error="deleteError"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
/* .resource-info etc. are global from index.css */

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

.actions {
    display: flex;
    justify-content: center;
    gap: var(--spacing-2);
}

.text-error {
    color: var(--error-color);
}

.monospace {
    font-family: var(--font-family-mono);
}

.spinning {
    animation: spin 1s linear infinite;
}
@keyframes spin {
    to {
        transform: rotate(360deg);
    }
}

/* 底部按钮区需要纵向堆叠（错误提示在按钮上方），.modal-footer 属于 BaseModal，这里用一层包裹元素 */
.modal-footer-stack {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: var(--spacing-2);
    width: 100%;
}

.form-row {
    display: flex;
    gap: var(--spacing-4);
}

.form-group-half {
    flex: 1;
}

.input-with-suffix {
    position: relative;
    display: flex;
    align-items: center;
}

.input-with-suffix .form-input {
    padding-right: 50px;
}

.input-suffix {
    position: absolute;
    right: 12px;
    color: var(--gray-400);
    font-size: var(--font-size-sm);
    pointer-events: none;
}

.advanced-toggle {
    display: flex;
    align-items: center;
    gap: 4px;
    cursor: pointer;
    color: var(--primary-600);
    font-size: var(--font-size-sm);
    padding: var(--spacing-2) 0;
    user-select: none;
}

.advanced-toggle:hover {
    color: var(--primary-700);
}

.advanced-section {
    padding-top: var(--spacing-2);
    border-top: 1px solid var(--border-light);
    margin-top: var(--spacing-2);
}
</style>
