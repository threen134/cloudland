<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { subnetsApi, vpcsApi, type Subnet, type SubnetPayload, type VPC } from '../../api/networks'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import { useAuthStore } from '../../stores/auth'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()

import { Network, Plus, Trash2, Edit, Search, RefreshCw, Check, Copy, ChevronDown, HelpCircle } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const vpcs = ref<VPC[]>([])

const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const showAdvanced = ref(false)
const showNetworkConfig = ref(true)
const newSubnetForm = ref<SubnetPayload>({
    name: '',
    network_cidr: '',
    gateway: '',
    type: 'internal',
    dhcp: true,
    vpc: { id: '' },
    vlan: undefined,
    start_ip: '',
    end_ip: '',
    dns: '',
    base_domain: '',
    priority: undefined,
})

const { t } = useI18n()
const toast = useToast()
// 子网名后端是 max=64，比其他资源宽
const isNameValid = computed(() => isValidName(newSubnetForm.value.name, 64))

const { copiedId, copyId } = useCopyId()

// 排序在服务端做（sortField 是 subnets 表的真实列名）；
// IP 使用率是前端算的，VPC 名在 routers 表里，后端没有 join 排序，所以这两列不给排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'cidr', label: t('dashboard.table.cidr'), sortable: true, sortField: 'network' },
    { key: 'vlan', label: t('dashboard.table.rangeVlan'), sortable: true },
    { key: 'usage', label: t('dashboard.table.ipUsage') },
    { key: 'vpc', label: t('dashboard.table.vpc') },
    { key: 'type', label: t('dashboard.table.type'), sortable: true },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// 分页、搜索、排序都在服务端做
const {
    items: subnets,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchSubnets,
    reload: reloadSubnets,
} = useListQuery<Subnet>(
    async ({ offset, limit, query, order }) => {
        const response = await subnetsApi.list({ offset, limit, order, query: query || undefined })
        return { items: response.subnets || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

// 新建子网弹窗里的 VPC 下拉；打开弹窗时也会重新拉一次
const fetchVpcs = async () => {
    try {
        const response = await vpcsApi.list()
        vpcs.value = response.vpcs || []
        if (vpcs.value.length > 0 && !newSubnetForm.value.vpc?.id) {
            newSubnetForm.value.vpc = { id: vpcs.value[0].id }
        }
    } catch (err) {
        console.error('Failed to fetch VPCs:', err)
    }
}

const openCreateModal = async () => {
    newSubnetForm.value = {
        name: '',
        network_cidr: '',
        gateway: '',
        type: 'internal',
        dhcp: true,
        vpc: { id: '' },
        vlan: undefined,
        start_ip: '',
        end_ip: '',
        dns: '',
        base_domain: '',
        priority: undefined,
    }
    showAdvanced.value = false
    await fetchVpcs()
    createModalVisible.value = true
}

const authStore = useAuthStore()
const isSystemAdmin = computed(() => authStore.user?.role === 'admin' || authStore.user?.is_superuser)
const requiresVpc = computed(() => newSubnetForm.value.type === 'internal')

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateSubnet = async () => {
    createError.value = ''
    if (!newSubnetForm.value.name || !newSubnetForm.value.network_cidr) {
        createError.value = t('messages.fillNameAndCidr')
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }
    if (requiresVpc.value && !newSubnetForm.value.vpc?.id) {
        createError.value = t('messages.vpcRequiredForInternal')
        return
    }

    if (
        (newSubnetForm.value.type === 'public' || newSubnetForm.value.type === 'private') &&
        (!newSubnetForm.value.vlan || newSubnetForm.value.vlan < 1 || newSubnetForm.value.vlan > 4094)
    ) {
        createError.value = t('messages.vlanRequiredRange')
        return
    }

    // Build clean payload, omit empty optional fields
    const payload: SubnetPayload = {
        name: newSubnetForm.value.name,
        network_cidr: newSubnetForm.value.network_cidr,
        type: newSubnetForm.value.type,
        dhcp: newSubnetForm.value.dhcp,
    }
    if (newSubnetForm.value.gateway) payload.gateway = newSubnetForm.value.gateway
    if (requiresVpc.value && newSubnetForm.value.vpc?.id) payload.vpc = newSubnetForm.value.vpc
    if (newSubnetForm.value.start_ip) payload.start_ip = newSubnetForm.value.start_ip
    if (newSubnetForm.value.end_ip) payload.end_ip = newSubnetForm.value.end_ip
    if (newSubnetForm.value.dns) payload.dns = newSubnetForm.value.dns
    if (newSubnetForm.value.base_domain) payload.base_domain = newSubnetForm.value.base_domain
    if (newSubnetForm.value.vlan) payload.vlan = newSubnetForm.value.vlan
    if (newSubnetForm.value.priority != null) payload.priority = newSubnetForm.value.priority

    creating.value = true
    try {
        await subnetsApi.create(payload)
        // 列表按创建时间倒序，新建的在第一页
        await reloadSubnets()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create subnet:', err)
        createError.value = errorMessage(err, t('messages.error'))
    } finally {
        creating.value = false
    }
}

const getTypeClass = (type: string) => {
    const map: Record<string, string> = {
        public: 'badge-success',
        internal: 'badge-primary',
        private: 'badge-info',
        site: 'badge-warning',
    }
    return map[type] || 'badge-gray'
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<Subnet | null>(null)

const handleDeleteClick = (item: Subnet) => {
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
        await subnetsApi.delete(resourceToDelete.value.id)
        await fetchSubnets()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error) {
        console.error('Failed to delete subnet:', error)
        deleteError.value = errorMessage(error, t('messages.error'))
    } finally {
        deletingResource.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchSubnets()
        fetchVpcs()
    }
})
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="() => fetchSubnets()"
                    :title="$t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.createSubnet') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="subnets"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="() => fetchSubnets()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <Network :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noSubnets') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: subnet }">
                <router-link :to="{ name: 'subnet-detail', params: { id: subnet.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <Network :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ subnet.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="subnet.id">{{ subnet.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(subnet.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check
                                        v-if="copiedId === subnet.id"
                                        :size="10"
                                        style="color: var(--success-color)"
                                    />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-cidr="{ row: subnet }">
                <div class="cidr-group">
                    <code class="cidr">{{ subnet.network || subnet.network_cidr }}</code>
                    <div class="text-xs text-light" style="margin-top: 2px">
                        {{ $t('dashboard.table.gateway') }}: {{ subnet.gateway || '-' }}
                    </div>
                </div>
            </template>

            <template #cell-vlan="{ row: subnet }">
                <div class="vlan-info">
                    <span v-if="subnet.vlan">
                        <span
                            class="badge badge-gray text-xs"
                            :title="subnet.vlan > 4094 ? $t('dashboard.table.vxlan') : $t('dashboard.table.vlan')"
                        >
                            {{ subnet.vlan }}
                        </span>
                    </span>
                </div>
            </template>

            <template #cell-usage="{ row: subnet }">
                <div class="usage-stats">
                    <div class="text-xs">
                        <span class="text-primary font-bold">{{ subnet.allocated_count }}</span> /
                        <span class="text-success">{{ subnet.available_count }}</span> /
                        <span>{{ subnet.total_count }}</span>
                    </div>
                    <div
                        class="usage-progress"
                        style="
                            width: 100px;
                            height: 4px;
                            background: var(--gray-100);
                            border-radius: 2px;
                            margin-top: 4px;
                            overflow: hidden;
                        "
                    >
                        <div
                            :style="{
                                width: ((subnet.allocated_count || 0) / (subnet.total_count || 1)) * 100 + '%',
                                background: 'var(--primary-color)',
                                height: '100%',
                            }"
                        ></div>
                    </div>
                </div>
            </template>

            <template #cell-vpc="{ row: subnet }">
                <div v-if="subnet.vpc" class="vpc-cell">
                    <router-link :to="{ name: 'vpc-detail', params: { id: subnet.vpc.id } }" class="vpc-link">
                        {{ subnet.vpc.name }}
                    </router-link>
                </div>
                <span v-else class="text-light italic text-xs">{{
                    $t('dashboard.table.standalone') || 'Standalone'
                }}</span>
            </template>

            <template #cell-type="{ row: subnet }">
                <span :class="['badge', getTypeClass(subnet.type || '')]">
                    {{ $t('dashboard.subnetTypes.' + (subnet.type || 'internal')) }}
                </span>
            </template>

            <template #cell-actions="{ row: subnet }">
                <div class="actions">
                    <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')">
                        <Edit :size="14" />
                    </button>
                    <button
                        class="btn btn-ghost btn-sm text-error"
                        :title="$t('actions.delete')"
                        @click="handleDeleteClick(subnet)"
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

        <!-- Create Subnet Modal -->
        <BaseModal
            :show="createModalVisible"
            :title="$t('dashboard.buttons.createSubnet')"
            size="lg"
            :loading="creating"
            form
            @close="closeCreateModal"
            @submit="handleCreateSubnet"
        >
            <!-- Section 1 & 2: Network Configuration -->
            <div class="form-section">
                <div class="form-section-title form-section-toggle" @click="showNetworkConfig = !showNetworkConfig">
                    {{ $t('dashboard.forms.sections.networkConfig') }}
                    <ChevronDown :size="14" :class="['chevron-icon', { 'chevron-open': showNetworkConfig }]" />
                </div>
                <div v-if="showNetworkConfig" class="form-section-body">
                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.table.name') }} *</label>
                        <input
                            v-model="newSubnetForm.name"
                            type="text"
                            :class="['form-input', { 'input-error': !isNameValid }]"
                            :placeholder="$t('dashboard.forms.placeholder.subnetNameExample')"
                        />
                        <div v-if="!isNameValid" class="text-error text-xs mt-1">
                            {{ $t('messages.invalidHostname') }}
                        </div>
                    </div>

                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.table.type') }}</label>
                        <select v-model="newSubnetForm.type" class="form-input">
                            <option value="internal">{{ $t('dashboard.subnetTypes.internal') }}</option>
                            <option v-if="isSystemAdmin" value="public">
                                {{ $t('dashboard.subnetTypes.public') }}
                            </option>
                            <option v-if="isSystemAdmin" value="private">
                                {{ $t('dashboard.subnetTypes.private') }}
                            </option>
                            <option value="site">{{ $t('dashboard.subnetTypes.site') }}</option>
                        </select>
                    </div>

                    <div class="form-group">
                        <label class="form-label">{{ $t('dashboard.forms.cidr') }} *</label>
                        <input
                            v-model="newSubnetForm.network_cidr"
                            type="text"
                            class="form-input"
                            :placeholder="$t('dashboard.forms.placeholder.cidrExample')"
                        />
                    </div>

                    <div class="form-group" v-if="requiresVpc">
                        <label class="form-label">{{ $t('dashboard.forms.vpc') }} *</label>
                        <select v-model="newSubnetForm.vpc!.id" class="form-input">
                            <option value="" disabled>{{ $t('dashboard.forms.placeholder.selectVpc') }}</option>
                            <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">
                                {{ vpc.name }} ({{ vpc.id }})
                            </option>
                        </select>
                    </div>

                    <div class="form-group" v-if="newSubnetForm.type === 'public' || newSubnetForm.type === 'private'">
                        <label class="form-label">{{ $t('dashboard.forms.vlan') }} *</label>
                        <input
                            v-model.number="newSubnetForm.vlan"
                            type="number"
                            class="form-input"
                            :placeholder="$t('dashboard.forms.placeholder.vlanExample')"
                            min="1"
                            max="4094"
                        />
                        <div class="form-hint">{{ $t('dashboard.vpcDetail.vlanPublicHint') }}</div>
                    </div>

                    <div class="form-row">
                        <div class="form-group flex-1">
                            <label class="form-label">{{ $t('dashboard.forms.gateway') }}</label>
                            <input
                                v-model="newSubnetForm.gateway"
                                type="text"
                                class="form-input"
                                :placeholder="$t('dashboard.forms.placeholder.gatewayExample')"
                            />
                        </div>
                        <div class="form-group flex-1">
                            <label class="form-label">
                                {{ $t('dashboard.table.dhcp') }}
                                <span class="tooltip-wrapper">
                                    <HelpCircle :size="13" class="help-icon" />
                                    <span class="tooltip-text">{{ $t('dashboard.forms.dhcpTooltip') }}</span>
                                </span>
                            </label>
                            <div class="toggle-group">
                                <label class="toggle-switch">
                                    <input type="checkbox" v-model="newSubnetForm.dhcp" />
                                    <span class="toggle-slider"></span>
                                </label>
                                <span class="toggle-label">{{
                                    newSubnetForm.dhcp
                                        ? $t('dashboard.alarmActions.enabled')
                                        : $t('dashboard.alarmActions.disabled')
                                }}</span>
                            </div>
                        </div>
                    </div>

                    <div class="form-row">
                        <div class="form-group flex-1">
                            <label class="form-label">{{ $t('dashboard.forms.dns') }}</label>
                            <input
                                v-model="newSubnetForm.dns"
                                type="text"
                                class="form-input"
                                :placeholder="$t('dashboard.forms.placeholder.dnsExample')"
                            />
                        </div>
                        <div class="form-group flex-1">
                            <label class="form-label">{{ $t('dashboard.forms.baseDomain') }}</label>
                            <input
                                v-model="newSubnetForm.base_domain"
                                type="text"
                                class="form-input"
                                :placeholder="$t('dashboard.forms.placeholder.domainExample')"
                            />
                        </div>
                    </div>
                </div>
            </div>

            <!-- Section 3: Advanced Options (collapsible) -->
            <div class="form-section">
                <div class="form-section-title form-section-toggle" @click="showAdvanced = !showAdvanced">
                    {{ $t('dashboard.forms.sections.advanced') }}
                    <ChevronDown :size="14" :class="['chevron-icon', { 'chevron-open': showAdvanced }]" />
                </div>
                <div v-if="showAdvanced" class="form-section-body">
                    <div class="form-row">
                        <div class="form-group flex-1">
                            <label class="form-label">{{ $t('dashboard.forms.startIp') }}</label>
                            <input
                                v-model="newSubnetForm.start_ip"
                                type="text"
                                class="form-input"
                                :placeholder="$t('dashboard.forms.placeholder.ipExample')"
                            />
                        </div>
                        <div class="form-group flex-1">
                            <label class="form-label">{{ $t('dashboard.forms.endIp') }}</label>
                            <input
                                v-model="newSubnetForm.end_ip"
                                type="text"
                                class="form-input"
                                :placeholder="$t('dashboard.forms.placeholder.ipExample')"
                            />
                        </div>
                    </div>
                    <div class="form-row">
                        <div
                            class="form-group flex-1"
                            v-if="newSubnetForm.type !== 'public' && newSubnetForm.type !== 'private'"
                        >
                            <label class="form-label">
                                {{
                                    newSubnetForm.type === 'internal'
                                        ? $t('dashboard.table.vxlan')
                                        : $t('dashboard.forms.vlan')
                                }}
                            </label>
                            <input
                                v-model.number="newSubnetForm.vlan"
                                type="number"
                                class="form-input"
                                :placeholder="$t('dashboard.forms.placeholder.auto')"
                                min="1"
                                max="16777215"
                            />
                            <div class="form-hint">{{ $t('dashboard.vpcDetail.vlanHint') }}</div>
                        </div>
                        <div class="form-group flex-1" v-if="newSubnetForm.type === 'public'">
                            <label class="form-label">{{ $t('dashboard.forms.priority') }}</label>
                            <input
                                v-model.number="newSubnetForm.priority"
                                type="number"
                                class="form-input"
                                :placeholder="$t('dashboard.forms.placeholder.numberExample')"
                                min="0"
                                max="100000"
                            />
                            <div class="form-hint">{{ $t('dashboard.forms.priorityHint') }}</div>
                        </div>
                    </div>
                </div>
            </div>

            <div
                v-if="createError"
                class="text-error"
                style="
                    margin-top: var(--spacing-4);
                    font-size: var(--font-size-sm);
                    background: var(--error-light);
                    padding: var(--spacing-2);
                    border-radius: var(--radius-sm);
                "
            >
                {{ createError }}
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="creating">
                    <span
                        v-if="creating"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createSubnet') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleteModalVisible"
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

.cidr,
.ip {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-sm);
}

.vpc-name {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
}

.text-success {
    color: var(--success-color);
}

.actions {
    display: flex;
    justify-content: center;
    gap: var(--spacing-2);
}

.cidr-group {
    display: flex;
    flex-direction: column;
}

.text-xs {
    font-size: 0.75rem;
}
.text-light {
    color: var(--text-light);
}
.text-primary {
    color: var(--primary-color);
}
.text-success {
    color: var(--success-color);
}
.font-bold {
    font-weight: 700;
}
.monospace {
    font-family: var(--font-family-mono);
}
.mt-1 {
    margin-top: 4px;
}
.italic {
    font-style: italic;
}

.vpc-link {
    color: var(--primary-600);
    text-decoration: none;
    font-weight: 500;
}

.vpc-link:hover {
    text-decoration: underline;
}

.usage-stats {
    display: flex;
    flex-direction: column;
}

.form-section {
    margin-bottom: var(--spacing-4);
}

.form-section:last-child {
    margin-bottom: 0;
}

.form-section-title {
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-semibold);
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-bottom: var(--spacing-3);
    padding-bottom: var(--spacing-2);
    border-bottom: 1px solid var(--border-light);
}

.form-section-toggle {
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: space-between;
    user-select: none;
}

.form-section-toggle:hover {
    color: var(--primary-color);
}

.chevron-icon {
    transition: transform 0.2s;
}

.chevron-open {
    transform: rotate(180deg);
}

.form-section-body {
    animation: fadeIn 0.2s ease-out;
    padding-top: var(--spacing-2);
}

.form-hint {
    font-size: 0.7rem;
    color: var(--text-light);
    margin-top: 4px;
}

.tooltip-wrapper {
    position: relative;
    display: inline-flex;
    align-items: center;
    margin-left: 4px;
    cursor: help;
}

.help-icon {
    color: var(--text-secondary);
    transition: color 0.15s;
}

.tooltip-wrapper:hover .help-icon {
    color: var(--primary-color);
}

.tooltip-text {
    visibility: hidden;
    opacity: 0;
    position: absolute;
    bottom: calc(100% + 8px);
    left: 0;
    background: var(--gray-900);
    color: var(--text-inverse);
    font-size: 0.75rem;
    font-weight: normal;
    text-transform: none;
    letter-spacing: normal;
    line-height: 1.4;
    padding: 8px 12px;
    border-radius: var(--radius-md);
    white-space: normal;
    width: 260px;
    z-index: var(--z-tooltip);
    transition:
        opacity 0.15s,
        visibility 0.15s;
    pointer-events: none;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
}

.tooltip-text::after {
    content: '';
    position: absolute;
    top: 100%;
    left: 16px;
    border: 5px solid transparent;
    border-top-color: var(--gray-900);
}

.tooltip-wrapper:hover .tooltip-text {
    visibility: visible;
    opacity: 1;
}

@keyframes fadeIn {
    from {
        opacity: 0;
    }
    to {
        opacity: 1;
    }
}
</style>
