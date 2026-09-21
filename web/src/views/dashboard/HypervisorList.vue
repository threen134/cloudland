<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch, type Directive } from 'vue'
import {
    hypervisorsApi,
    type Hypervisor,
    type HyperDeployPayload,
    type HyperPatchPayload,
    type HyperListResponse,
} from '../../api/hypervisors'
import { instancesApi, type Instance, type InstanceListResponse } from '../../api/instances'
import { zonesApi, type Zone, type ZoneListResponse } from '../../api/zones'
import { errorMessage } from '../../utils/error'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()
import {
    Search as SearchIcon,
    Server,
    Plus,
    Trash2,
    RefreshCw,
    Copy,
    Check,
    Loader2,
    HelpCircle,
    Pencil,
    Wrench,
    MoreVertical,
    SquareTerminal,
} from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useHostConsole } from '../../composables/useHostConsole'
import { formatMemory, formatDisk } from '../../utils/format'
import type { StatusVariant } from '../../utils/status'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

// 指令把监听器挂在元素自身上，卸载时再取下来，所以元素类型要带上这个附加属性
type ClickOutsideEl = HTMLElement & { clickOutsideEvent?: (event: Event) => void }

const vClickOutside: Directive<ClickOutsideEl, (event: Event) => void> = {
    mounted(el, binding) {
        el.clickOutsideEvent = (event: Event) => {
            if (!(el === event.target || el.contains(event.target as Node))) {
                binding.value(event)
            }
        }
        document.addEventListener('click', el.clickOutsideEvent)
    },
    unmounted(el) {
        if (el.clickOutsideEvent) document.removeEventListener('click', el.clickOutsideEvent)
    },
}

const { t, te } = useI18n()
const toast = useToast()
const hypervisorList = ref<Hypervisor[]>([])

// 虚拟机数量列的悬浮列表：按 host id 缓存，避免同一节点反复请求
const hoveredHyperId = ref<string | null>(null)
const hyperInstances = ref<Record<number, Instance[]>>({})
const hyperInstancesLoading = ref<Record<number, boolean>>({})

const loadHyperInstances = async (h: Hypervisor) => {
    hoveredHyperId.value = h.uuid
    if (!h.instance_count || hyperInstances.value[h.hostid] || hyperInstancesLoading.value[h.hostid]) return
    hyperInstancesLoading.value[h.hostid] = true
    try {
        // limit 取较大值：悬浮是为了看清都有哪些虚拟机，分页会让列表看起来缺失
        const resp = await instancesApi.fetchInstances({ hyper: h.hostid, limit: 200 })
        const data = resp as InstanceListResponse | Instance[]
        hyperInstances.value[h.hostid] = Array.isArray(data) ? data : data.instances || []
    } catch (err) {
        console.error('Failed to load instances of hypervisor:', err)
        hyperInstances.value[h.hostid] = []
    } finally {
        hyperInstancesLoading.value[h.hostid] = false
    }
}

// 缺键时 t() 返回键路径本身，必须用 te() 判断后再回退到原始状态串
const instanceStatusText = (status: string) => {
    const s = (status || '').toLowerCase()
    if (!s) return '-'
    const key = `dashboard.instanceStatus.${s}`
    return te(key) ? t(key) : status
}
const loading = ref(false)
const loadError = ref('')
const searchQuery = ref('')

// 列表是后端分页（offset/limit），前端排序只会打乱当前页，所以所有列都不排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId') },
    { key: 'hostIp', label: t('dashboard.table.hostIp') },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'instanceCount', label: t('dashboard.table.instanceCount') },
    { key: 'cpu', label: `${t('dashboard.table.vcpus')} (${t('dashboard.table.available')})` },
    { key: 'memory', label: `${t('dashboard.table.memory')} (${t('dashboard.table.available')})` },
    { key: 'disk', label: `${t('dashboard.table.disk')} (${t('dashboard.table.available')})` },
    { key: 'zone', label: t('dashboard.table.zone') },
    { key: 'actions', label: t('dashboard.table.actions'), width: '80px', align: 'center' },
])

// Pagination
const currentPage = ref(1)
const pageSize = ref(20)
const totalCount = ref(0)

// Deploy modal
const showDeployModal = ref(false)
const deploying = ref(false)
const deployResult = ref<Hypervisor | null>(null)
const deployForm = ref<HyperDeployPayload>({
    ip: '',
    hostname: '',
    network_device: 'eth0',
    vlan_device: '',
    private_vlan_device: '',
    dns_server: '8.8.8.8',
    domain: 'example.com',
    zone_name: '',
    virt_type: 'kvm-x86_64',
})
const zoneList = ref<Zone[]>([])
const copiedCmd = ref(false)

// Delete
const showDeleteConfirm = ref(false)
const deletingHyper = ref<Hypervisor | null>(null)
const deleting = ref(false)

// Edit
const showEditModal = ref(false)
const editingHyper = ref<Hypervisor | null>(null)
const saving = ref(false)
const editForm = ref({
    status: 0,
    // 下拉框绑的是 zone uuid（见 resolveZoneIdByName 的说明）
    zone_id: '',
    cpu_over_rate: 1,
    mem_over_rate: 1,
    disk_over_rate: 1,
    remark: '',
})

// Maintain
const showMaintainModal = ref(false)
const maintainingHyper = ref<Hypervisor | null>(null)
const maintaining = ref(false)
const maintainForm = ref({
    migrate: true,
    target_hyper: -1,
})

const activeActionMenuId = ref<string | null>(null)
const toggleActionMenu = (uuid: string) => {
    activeActionMenuId.value = activeActionMenuId.value === uuid ? null : uuid
}
const closeActionMenu = () => {
    activeActionMenuId.value = null
}

const { copiedId, copyId } = useCopyId()
const { disabledReason: consoleDisabledReason, openHostConsole } = useHostConsole()
// 菜单项要先收起菜单再开终端；写成一个方法而不是模板里的两句，
// prettier 的 semi:false 会把内联的两句拆成两行并去掉分号，Vue 解析不了
const handleHostConsole = (uuid: string) => {
    closeActionMenu()
    openHostConsole(uuid)
}

const STATUS_MAP: Record<number, { labelKey: string; variant: StatusVariant }> = {
    0: { labelKey: 'disabled', variant: 'neutral' },
    1: { labelKey: 'active', variant: 'success' },
    2: { labelKey: 'maintaining', variant: 'warning' },
    4: { labelKey: 'deploying', variant: 'pending' },
    5: { labelKey: 'deployFailed', variant: 'error' },
}

const fetchHypervisors = async () => {
    loading.value = true
    loadError.value = ''
    try {
        const offset = (currentPage.value - 1) * pageSize.value
        const response = await hypervisorsApi.fetchHypervisors({
            offset,
            limit: pageSize.value,
            q: searchQuery.value || undefined,
        })
        const data = response as HyperListResponse | Hypervisor[]
        hypervisorList.value = Array.isArray(data) ? data : data.hypers || []
        totalCount.value = (Array.isArray(data) ? 0 : data.total) || hypervisorList.value.length
    } catch (error) {
        console.error('API fetch failed:', error)
        hypervisorList.value = []
        loadError.value = t('messages.error')
    } finally {
        loading.value = false
    }
}

// Re-fetch when region changes
watch(
    () => region.currentRegionId,
    (newId) => {
        if (newId) {
            fetchHypervisors()
        }
    }
)

const totalPages = computed(() => Math.max(1, Math.ceil(totalCount.value / pageSize.value)))

const goToPage = (page: number) => {
    if (page < 1 || page > totalPages.value) return
    currentPage.value = page
    fetchHypervisors()
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
const onSearchInput = () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
        currentPage.value = 1
        fetchHypervisors()
    }, 400)
}

const getStatusInfo = (status: number): { labelKey: string; variant: StatusVariant } => {
    return STATUS_MAP[status] || { labelKey: 'unknown', variant: 'neutral' }
}

// statusName 是后端返回的英文原文（active / maintaining …），只在该状态没有对应翻译时兜底。
// 此前模板写成 `status_name || getStatusLabel(...)`，英文原文非空就短路了，翻译永远不生效
const getStatusLabel = (status: number, statusName?: string) => {
    const info = getStatusInfo(status)
    if (info.labelKey === 'unknown') return statusName || t('dashboard.hypervisorStatus.unknown', { status })
    return t('dashboard.hypervisorStatus.' + info.labelKey)
}

// 进入维护模式只是「开始腾空」：虚拟机迁移是异步的，状态置位时可能一台都还没迁走。
// 光看「维护中」会让人以为可以断电了，这里用节点上剩余的虚拟机数量把两者区分开。
// 用剩余虚拟机数而不是进行中的迁移数：迁移失败时虚拟机会留在原地，按迁移数会错报「已腾空」
const getDrainHint = (h: Hypervisor) => {
    if (h.status !== 2) return ''
    const left = h.instance_count || 0
    return left > 0
        ? t('dashboard.hypervisorStatus.draining', { count: left })
        : t('dashboard.hypervisorStatus.drained')
}

const usagePercent = (used: number, total: number) => {
    if (total <= 0) return 0
    return Math.round((used / total) * 100)
}

// Deploy
const openDeployModal = async () => {
    deployForm.value = {
        ip: '',
        hostname: '',
        network_device: '',
        vlan_device: '',
        private_vlan_device: '',
        dns_server: '8.8.8.8',
        domain: 'example.com',
        zone_name: '',
        virt_type: 'kvm-x86_64',
    }
    deployResult.value = null
    showDeployModal.value = true
    try {
        const resp = await zonesApi.fetchZones()
        const data = resp as ZoneListResponse | Zone[]
        zoneList.value = Array.isArray(data) ? data : data.zones || []
    } catch {
        zoneList.value = []
    }
}

const handleDeploy = async () => {
    if (!deployForm.value.ip || !deployForm.value.hostname || !deployForm.value.network_device) return
    deploying.value = true
    try {
        const resp = await hypervisorsApi.deployHypervisor(deployForm.value)
        deployResult.value = resp
    } catch (err) {
        toast.error(errorMessage(err, t('messages.deployFailed')))
    } finally {
        deploying.value = false
    }
}

const copyDeployCommand = () => {
    if (deployResult.value?.deploy_command) {
        navigator.clipboard.writeText(deployResult.value.deploy_command)
        copiedCmd.value = true
        setTimeout(() => {
            copiedCmd.value = false
        }, 2000)
    }
}

const closeDeployModal = () => {
    showDeployModal.value = false
    if (deployResult.value) fetchHypervisors()
}

// Delete
const confirmDelete = (h: Hypervisor) => {
    deletingHyper.value = h
    showDeleteConfirm.value = true
    closeActionMenu()
}

const handleDelete = async () => {
    if (!deletingHyper.value) return
    deleting.value = true
    try {
        await hypervisorsApi.deleteHypervisor(deletingHyper.value.uuid)
        showDeleteConfirm.value = false
        deletingHyper.value = null
        await fetchHypervisors()
        toast.success(t('messages.deleteSuccess'))
    } catch (err) {
        toast.error(errorMessage(err, t('messages.deleteFailed')))
    } finally {
        deleting.value = false
    }
}

// ⚠️ 这里存的是 zone 的 uuid：GET /zones 的 ResourceReference.id 就是 uuid，
// 而 PATCH /hypers 的 zone_id 要的是数据库自增 ID（后端 binding `*int64,min=1`），两边对不上
const editingZoneId = ref<string>('')

const resolveZoneIdByName = (name: string): string => {
    const match = zoneList.value.find((z) => z.name === name)
    return match ? match.id : ''
}

// Edit logic
const openEditModal = async (h: Hypervisor) => {
    editingHyper.value = h
    closeActionMenu()

    if (zoneList.value.length === 0) {
        try {
            const resp = await zonesApi.fetchZones()
            const data = resp as ZoneListResponse | Zone[]
            zoneList.value = Array.isArray(data) ? data : data.zones || []
        } catch {
            zoneList.value = []
        }
    }

    editingZoneId.value = resolveZoneIdByName(h.zone_name)
    editForm.value = {
        status: h.status,
        zone_id: editingZoneId.value,
        cpu_over_rate: h.cpu_over_rate || 1,
        mem_over_rate: h.mem_over_rate || 1,
        disk_over_rate: h.disk_over_rate || 1,
        remark: h.remark || '',
    }
    showEditModal.value = true
}

const handleEditSave = async () => {
    if (!editingHyper.value) return
    saving.value = true
    try {
        const payload: HyperPatchPayload = {}
        if (editForm.value.status !== editingHyper.value.status) payload.status = editForm.value.status
        // 只在真的换了可用区时才发。下拉里不再有「-」：后端不支持清空可用区
        // （zone_id 收的是 UUID，传 0 或空串都是 400），提供这个选项只会让保存必然失败
        if (editForm.value.zone_id !== editingZoneId.value) payload.zone_id = editForm.value.zone_id
        if (editForm.value.cpu_over_rate !== editingHyper.value.cpu_over_rate)
            payload.cpu_over_rate = Number(editForm.value.cpu_over_rate)
        if (editForm.value.mem_over_rate !== editingHyper.value.mem_over_rate)
            payload.mem_over_rate = Number(editForm.value.mem_over_rate)
        if (editForm.value.disk_over_rate !== editingHyper.value.disk_over_rate)
            payload.disk_over_rate = Number(editForm.value.disk_over_rate)
        if (editForm.value.remark !== (editingHyper.value.remark || '')) payload.remark = editForm.value.remark

        await hypervisorsApi.updateHypervisor(editingHyper.value.uuid, payload)
        showEditModal.value = false
        toast.success(t('messages.success'))
        await fetchHypervisors()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        saving.value = false
    }
}

// 维护模式可选的目标节点：排除正在进入维护的节点本身（迁到自己没有意义，后端会直接跳过），
// 只列活动状态的节点（维护中 / 已禁用 / 部署失败的节点不能接收虚拟机）
const maintainTargetOptions = computed(() =>
    hypervisorList.value.filter((h) => h.status === 1 && h.hostid !== maintainingHyper.value?.hostid)
)

// Maintain logic
const openMaintainModal = (h: Hypervisor) => {
    maintainingHyper.value = h
    maintainForm.value = { migrate: true, target_hyper: -1 }
    showMaintainModal.value = true
    closeActionMenu()
}

const handleMaintain = async () => {
    if (!maintainingHyper.value) return
    maintaining.value = true
    try {
        await hypervisorsApi.maintainHypervisor(maintainingHyper.value.uuid, maintainForm.value)
        showMaintainModal.value = false
        toast.success(t('messages.success'))
        await fetchHypervisors()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        maintaining.value = false
    }
}

// 退出维护模式：把状态改回活动。维护状态由控制面单方面置位、心跳不会再覆盖它
// （见 rpcs/hyper_status.go），所以必须提供出口，否则节点进了维护模式就只能去编辑对话框里改
const showExitMaintainModal = ref(false)
const exitingMaintain = ref(false)
const exitMaintainHyper = ref<Hypervisor | null>(null)

const exitMaintain = (h: Hypervisor) => {
    closeActionMenu()
    exitMaintainHyper.value = h
    showExitMaintainModal.value = true
}

const confirmExitMaintain = async () => {
    const h = exitMaintainHyper.value
    if (!h) return
    exitingMaintain.value = true
    try {
        await hypervisorsApi.updateHypervisor(h.uuid, { status: 1 })
        showExitMaintainModal.value = false
        exitMaintainHyper.value = null
        toast.success(t('messages.success'))
        await fetchHypervisors()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        exitingMaintain.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchHypervisors()
    }
})

// 搜索防抖定时器：组件卸载后不应再触发请求
onUnmounted(() => {
    if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
    <div class="vpc-list-container">
        <PageToolbar
            :search="searchQuery"
            @update:search="
                (v) => {
                    searchQuery = v
                    onSearchInput()
                }
            "
        >
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="fetchHypervisors"
                    :title="t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openDeployModal">
                    <Plus :size="14" />
                    <span>{{ t('dashboard.hypervisorActions.deploy') }}</span>
                </button>
            </template>
        </PageToolbar>

        <DataTable
            allow-overflow
            :columns="columns"
            :rows="hypervisorList"
            row-key="uuid"
            :loading="loading"
            :error="loadError"
            @retry="fetchHypervisors"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ t('messages.noResults') }}</p>
                </div>
                <div v-else class="empty-state">
                    <Server :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p>{{ t('messages.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: h }">
                <router-link :to="{ name: 'hypervisor-detail', params: { id: h.uuid } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <Server :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ h.hostname }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="h.uuid">{{ h.uuid.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(h.uuid)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check v-if="copiedId === h.uuid" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-hostIp="{ row: h }"
                ><code class="mono-value">{{ h.host_ip }}</code></template
            >

            <template #cell-status="{ row: h }">
                <StatusBadge
                    :variant="getStatusInfo(h.status).variant"
                    :label="getStatusLabel(h.status, h.status_name)"
                />
                <span
                    v-if="h.status === 2"
                    :class="['drain-hint', h.instance_count ? 'drain-hint-warn' : 'drain-hint-done']"
                    >{{ getDrainHint(h) }}</span
                >
            </template>

            <template #cell-instanceCount="{ row: h }">
                <span class="vm-count-cell" @mouseenter="loadHyperInstances(h)" @mouseleave="hoveredHyperId = null">
                    <span class="vm-count-badge" :class="{ 'vm-count-zero': !h.instance_count }">{{
                        h.instance_count || 0
                    }}</span>
                    <div v-if="hoveredHyperId === h.uuid && h.instance_count" class="vm-tooltip">
                        <div v-if="hyperInstancesLoading[h.hostid]" class="vm-tooltip-empty">
                            {{ t('messages.loading') }}
                        </div>
                        <template v-else>
                            <div v-for="inst in hyperInstances[h.hostid] || []" :key="inst.id" class="vm-tooltip-row">
                                <span class="vm-tooltip-name">{{ inst.hostname }}</span>
                                <span class="vm-tooltip-status">{{ instanceStatusText(inst.status) }}</span>
                            </div>
                            <div v-if="!(hyperInstances[h.hostid] || []).length" class="vm-tooltip-empty">
                                {{ t('messages.noData') }}
                            </div>
                        </template>
                    </div>
                </span>
            </template>

            <template #cell-cpu="{ row: h }">
                <div class="usage-cell">
                    <span>{{ h.cpu }} / {{ h.cpu_total }}</span>
                    <div class="usage-bar">
                        <div class="usage-fill" :style="{ width: usagePercent(h.cpu, h.cpu_total) + '%' }"></div>
                    </div>
                </div>
            </template>

            <template #cell-memory="{ row: h }">
                <div class="usage-cell">
                    <span>{{ formatMemory(h.memory) }} / {{ formatMemory(h.memory_total) }}</span>
                    <div class="usage-bar">
                        <div class="usage-fill" :style="{ width: usagePercent(h.memory, h.memory_total) + '%' }"></div>
                    </div>
                </div>
            </template>

            <template #cell-disk="{ row: h }">
                <div class="usage-cell">
                    <span>{{ formatDisk(h.disk) }} / {{ formatDisk(h.disk_total) }}</span>
                    <div class="usage-bar">
                        <div class="usage-fill" :style="{ width: usagePercent(h.disk, h.disk_total) + '%' }"></div>
                    </div>
                </div>
            </template>

            <template #cell-zone="{ row: h }">{{ h.zone_name || '-' }}</template>

            <template #cell-actions="{ row: h }">
                <div class="action-dropdown">
                    <button class="icon-btn-table" @click.stop="toggleActionMenu(h.uuid)" :title="t('actions.actions')">
                        <MoreVertical :size="16" />
                    </button>
                    <Transition name="dropdown">
                        <div
                            v-if="activeActionMenuId === h.uuid"
                            class="dropdown-menu dropdown-menu-right"
                            @click.stop
                            v-click-outside="closeActionMenu"
                        >
                            <button class="dropdown-item" @click="openEditModal(h)">
                                <Pencil :size="14" /> {{ t('actions.edit') }}
                            </button>
                            <button v-if="h.status === 1" class="dropdown-item" @click="openMaintainModal(h)">
                                <Wrench :size="14" /> {{ t('dashboard.hypervisorActions.maintain') }}
                            </button>
                            <button v-if="h.status === 2" class="dropdown-item" @click="exitMaintain(h)">
                                <Wrench :size="14" /> {{ t('dashboard.hypervisorActions.exitMaintain') }}
                            </button>
                            <button
                                class="dropdown-item"
                                :disabled="!!consoleDisabledReason(h.status)"
                                :title="consoleDisabledReason(h.status)"
                                @click="handleHostConsole(h.uuid)"
                            >
                                <SquareTerminal :size="14" /> {{ t('dashboard.hypervisorActions.console') }}
                            </button>
                            <div class="dropdown-divider"></div>
                            <button class="dropdown-item text-error" @click="confirmDelete(h)">
                                <Trash2 :size="14" /> {{ t('actions.delete') }}
                            </button>
                        </div>
                    </Transition>
                </div>
            </template>

            <template #footer>
                <PaginationBar
                    :page="currentPage"
                    :page-size="pageSize"
                    :total="totalCount"
                    @update:page="goToPage"
                    @update:page-size="
                        (size) => {
                            pageSize = size
                            currentPage = 1
                            fetchHypervisors()
                        }
                    "
                />
            </template>
        </DataTable>

        <!-- Deploy Modal -->
        <BaseModal
            :show="showDeployModal"
            :title="t('dashboard.hypervisorActions.deploy')"
            size="lg"
            :form="!deployResult"
            @close="closeDeployModal"
            @submit="handleDeploy"
        >
            <!-- Deploy Form -->
            <div v-if="!deployResult">
                <div class="form-grid">
                    <div class="form-group">
                        <label class="form-label">IP *</label>
                        <input
                            type="text"
                            v-model="deployForm.ip"
                            class="form-input"
                            :placeholder="t('dashboard.forms.placeholder.ipExample')"
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.table.hostname') }} *</label>
                        <input
                            type="text"
                            v-model="deployForm.hostname"
                            class="form-input"
                            :placeholder="t('dashboard.forms.placeholder.hostnameExample')"
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">
                            {{ t('dashboard.hypervisorDeploy.networkDevice') }} *
                            <span class="tooltip-wrapper">
                                <HelpCircle :size="14" class="help-icon" />
                                <span class="tooltip-text">{{
                                    t('dashboard.hypervisorDeploy.tooltips.networkDevice')
                                }}</span>
                            </span>
                        </label>
                        <input
                            type="text"
                            v-model="deployForm.network_device"
                            class="form-input"
                            :placeholder="t('dashboard.forms.placeholder.netDeviceExample')"
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">
                            {{ t('dashboard.hypervisorDeploy.vlanDevice') }}
                            <span class="tooltip-wrapper">
                                <HelpCircle :size="14" class="help-icon" />
                                <span class="tooltip-text">{{
                                    t('dashboard.hypervisorDeploy.tooltips.vlanDevice')
                                }}</span>
                            </span>
                        </label>
                        <input
                            type="text"
                            v-model="deployForm.vlan_device"
                            class="form-input"
                            :placeholder="
                                deployForm.network_device || t('dashboard.forms.placeholder.netDeviceExample')
                            "
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">
                            {{ t('dashboard.hypervisorDeploy.privateVlanDevice') }}
                            <span class="tooltip-wrapper">
                                <HelpCircle :size="14" class="help-icon" />
                                <span class="tooltip-text">{{
                                    t('dashboard.hypervisorDeploy.tooltips.privateVlanDevice')
                                }}</span>
                            </span>
                        </label>
                        <input
                            type="text"
                            v-model="deployForm.private_vlan_device"
                            class="form-input"
                            :placeholder="
                                deployForm.vlan_device ||
                                deployForm.network_device ||
                                t('dashboard.forms.placeholder.netDeviceExample')
                            "
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.hypervisorDeploy.dnsServer') }}</label>
                        <input
                            type="text"
                            v-model="deployForm.dns_server"
                            class="form-input"
                            :placeholder="t('dashboard.forms.placeholder.dnsExample')"
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.hypervisorDeploy.domain') }}</label>
                        <input
                            type="text"
                            v-model="deployForm.domain"
                            class="form-input"
                            :placeholder="t('dashboard.forms.placeholder.domainExample')"
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.table.zone') }}</label>
                        <select v-model="deployForm.zone_name" class="form-input">
                            <option value="">{{ t('dashboard.hypervisorDeploy.autoZone') }}</option>
                            <option v-for="z in zoneList" :key="z.name" :value="z.name">{{ z.name }}</option>
                        </select>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.hypervisorDeploy.virtType') }}</label>
                        <select v-model="deployForm.virt_type" class="form-input">
                            <option value="kvm-x86_64">kvm-x86_64</option>
                            <option value="kvm-aarch64">kvm-aarch64</option>
                        </select>
                    </div>
                </div>
            </div>

            <!-- Deploy Result -->
            <div v-else>
                <div class="deploy-success">
                    <Check :size="32" style="color: var(--success-color); margin-bottom: 12px" />
                    <p style="font-weight: 600; margin-bottom: 16px">{{ t('dashboard.hypervisorDeploy.created') }}</p>
                    <p style="font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 16px">
                        {{ t('dashboard.hypervisorDeploy.runCommand') }}
                    </p>
                    <div class="deploy-cmd-box">
                        <pre>{{ deployResult.deploy_command }}</pre>
                        <button type="button" class="copy-cmd-btn" @click="copyDeployCommand">
                            <Check v-if="copiedCmd" :size="14" style="color: var(--success-color)" />
                            <Copy v-else :size="14" />
                        </button>
                    </div>
                </div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="closeDeployModal">
                    {{ deployResult ? t('actions.close') : t('actions.cancel') }}
                </button>
                <button
                    v-if="!deployResult"
                    type="submit"
                    class="btn btn-primary"
                    :disabled="deploying || !deployForm.ip || !deployForm.hostname || !deployForm.network_device"
                >
                    {{ deploying ? t('messages.loading') : t('dashboard.hypervisorActions.deploy') }}
                </button>
            </template>
        </BaseModal>

        <!-- Delete Confirm Modal -->
        <DeleteModal
            :show="showDeleteConfirm"
            :title="t('dashboard.hypervisorActions.deleteTitle')"
            :message="t('dashboard.hypervisorActions.deleteConfirm', { hostname: deletingHyper?.hostname })"
            :loading="deleting"
            @close="showDeleteConfirm = false"
            @confirm="handleDelete"
        />

        <!-- Edit Modal -->
        <BaseModal
            :show="showEditModal"
            :title="t('actions.edit') + ' - ' + (editingHyper?.hostname || '')"
            form
            :loading="saving"
            @close="showEditModal = false"
            @submit="handleEditSave"
        >
            <div class="form-grid" style="grid-template-columns: 1fr 1fr; gap: 16px">
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.status') }}</label>
                    <select v-model="editForm.status" class="form-input">
                        <option :value="0">{{ t('dashboard.hypervisorStatus.disabled') }}</option>
                        <option :value="1">{{ t('dashboard.hypervisorStatus.active') }}</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.zone') }}</label>
                    <select v-model="editForm.zone_id" class="form-input">
                        <!-- 占位项，不可选：后端不支持清空可用区（zone_id 收 UUID，传 0 或空串都是 400） -->
                        <option :value="0" disabled>-</option>
                        <option v-for="z in zoneList" :key="z.id" :value="z.id">{{ z.name }}</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.cpuOverCommit') }}</label>
                    <input type="number" step="0.1" min="1" v-model="editForm.cpu_over_rate" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.memOverCommit') }}</label>
                    <input type="number" step="0.1" min="1" v-model="editForm.mem_over_rate" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.diskOverCommit') }}</label>
                    <input type="number" step="0.1" min="1" v-model="editForm.disk_over_rate" class="form-input" />
                </div>
                <div class="form-group" style="grid-column: span 2">
                    <label class="form-label">{{ t('dashboard.table.remark') }}</label>
                    <input
                        type="text"
                        v-model="editForm.remark"
                        class="form-input"
                        :placeholder="t('dashboard.table.remark')"
                    />
                </div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showEditModal = false" :disabled="saving">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="saving">
                    <Loader2 v-if="saving" :size="14" class="spinning" />
                    {{ saving ? t('messages.saving') : t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <!-- Maintain Modal -->
        <BaseModal
            :show="showMaintainModal"
            :title="t('dashboard.hypervisorActions.maintainTitle')"
            form
            :loading="maintaining"
            @close="showMaintainModal = false"
            @submit="handleMaintain"
        >
            <p style="margin-bottom: 16px; color: var(--text-secondary); font-size: 0.875rem">
                {{ t('dashboard.hypervisorActions.maintainDesc', { hostname: maintainingHyper?.hostname }) }}
            </p>
            <div class="form-group" style="margin-bottom: 16px">
                <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer">
                    <input type="checkbox" v-model="maintainForm.migrate" />
                    {{ t('dashboard.hypervisorActions.migrateInstances') }}
                </label>
            </div>
            <div v-if="maintainForm.migrate" class="form-group">
                <label class="form-label">{{ t('dashboard.hypervisorActions.targetHyper') }}</label>
                <select v-model="maintainForm.target_hyper" class="form-select" style="width: 100%">
                    <option :value="-1">{{ t('dashboard.migrationForm.autoSelect') }}</option>
                    <option v-for="hyp in maintainTargetOptions" :key="hyp.uuid" :value="hyp.hostid">
                        {{ hyp.hostname }} ({{ hyp.hostid }})
                    </option>
                </select>
                <span style="font-size: 0.75rem; color: var(--text-light); margin-top: 4px">{{
                    t('dashboard.hypervisorActions.targetHyperHint')
                }}</span>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showMaintainModal = false">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-warning" :disabled="maintaining">
                    <Loader2 v-if="maintaining" :size="14" class="spinning" />
                    {{ maintaining ? t('messages.loading') : t('dashboard.hypervisorActions.maintain') }}
                </button>
            </template>
        </BaseModal>

        <!-- 退出维护模式确认 -->
        <BaseModal
            :show="showExitMaintainModal"
            :title="t('dashboard.hypervisorActions.exitMaintain')"
            size="sm"
            :loading="exitingMaintain"
            @close="showExitMaintainModal = false"
        >
            <p style="margin: 0; color: var(--text-secondary); font-size: 0.875rem">
                {{ t('dashboard.hypervisorActions.exitMaintainConfirm', { hostname: exitMaintainHyper?.hostname }) }}
            </p>

            <template #footer>
                <button
                    type="button"
                    class="btn btn-secondary"
                    :disabled="exitingMaintain"
                    @click="showExitMaintainModal = false"
                >
                    {{ t('actions.cancel') }}
                </button>
                <button type="button" class="btn btn-primary" :disabled="exitingMaintain" @click="confirmExitMaintain">
                    <Loader2 v-if="exitingMaintain" :size="14" class="spinning" />
                    {{ exitingMaintain ? t('messages.loading') : t('actions.confirm') }}
                </button>
            </template>
        </BaseModal>
    </div>
</template>

<style scoped>
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

.mono-value {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
}

/* 维护模式的腾空进度提示：还有虚拟机时是「别断电」的警告，必须显眼 */
.drain-hint {
    display: inline-block;
    margin-top: 4px;
    padding: 1px 8px;
    border-radius: 10px;
    font-size: 0.6875rem;
    font-weight: 600;
    white-space: nowrap;
}

.drain-hint-warn {
    background: var(--warning-light);
    color: var(--warning-dark);
    border: 1px solid var(--warning-color);
}

.drain-hint-done {
    background: var(--success-light);
    color: var(--success-dark);
}

/* 虚拟机数量列与悬浮列表 */
.vm-count-cell {
    position: relative;
    display: inline-block;
    cursor: default;
}

.vm-count-badge {
    display: inline-block;
    min-width: 28px;
    padding: 2px 8px;
    text-align: center;
    border-radius: 10px;
    background: var(--primary-50, var(--accent-purple-light));
    color: var(--primary-600, var(--accent-purple));
    font-size: 0.8125rem;
    font-weight: 600;
}

.vm-count-badge.vm-count-zero {
    background: var(--gray-100, var(--gray-100));
    color: var(--text-light, var(--gray-400));
    font-weight: 400;
}

.vm-tooltip {
    position: absolute;
    top: 100%;
    left: 0;
    z-index: var(--z-tooltip);
    margin-top: 6px;
    min-width: 200px;
    max-height: 260px;
    overflow-y: auto;
    padding: 6px 0;
    background: var(--bg-card, var(--bg-primary));
    border: 1px solid var(--border-color, var(--gray-200));
    border-radius: 6px;
    box-shadow: 0 6px 16px rgba(0, 0, 0, 0.12);
}

.vm-tooltip-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 4px 12px;
    font-size: 0.8125rem;
    white-space: nowrap;
}

.vm-tooltip-name {
    color: var(--text-primary, var(--gray-900));
}

.vm-tooltip-status {
    color: var(--text-secondary, var(--gray-500));
    font-size: 0.75rem;
}

.vm-tooltip-empty {
    padding: 6px 12px;
    font-size: 0.8125rem;
    color: var(--text-secondary, var(--gray-500));
}

.usage-cell {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: var(--font-size-xs);
    min-width: 120px;
}

.usage-bar {
    height: 4px;
    background: var(--gray-100);
    border-radius: 2px;
    overflow: hidden;
}

.usage-fill {
    height: 100%;
    background: var(--primary-color);
    border-radius: 2px;
    transition: width 0.3s;
}

.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
}

.icon-btn-table {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    border: none;
    background: transparent;
    color: var(--text-tertiary);
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: all 0.2s;
}

.icon-btn-table:hover {
    background-color: var(--bg-tertiary);
    color: var(--primary-500);
}
.icon-btn-table.text-error:hover {
    background-color: var(--error-light);
    color: var(--error-color);
}

/* Modal */
.form-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 16px;
}

.form-group {
    display: flex;
    flex-direction: column;
}

.form-label {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: 4px;
    font-weight: 500;
}

:deep(.modal-body) {
    overflow: visible !important;
}

.help-icon {
    color: var(--text-tertiary);
    opacity: 0.7;
    transition: opacity 0.2s;
}

.tooltip-wrapper {
    position: relative;
    display: inline-flex;
    cursor: help;
}

.tooltip-text {
    display: none;
    position: absolute;
    bottom: calc(100% + 8px);
    left: -10px;
    background: var(--gray-800);
    color: white;
    padding: 8px 12px;
    border-radius: 4px;
    font-size: 0.75rem;
    font-weight: 400;
    width: 220px;
    line-height: 1.4;
    z-index: var(--z-tooltip);
    box-shadow: 0 10px 15px -3px rgba(0, 0, 0, 0.4);
    pointer-events: none;
    white-space: normal;
    text-align: left;
}

/* Tooltip arrow */
.tooltip-text::after {
    content: '';
    position: absolute;
    top: 100%;
    left: 17px;
    margin-left: -5px;
    border-width: 5px;
    border-style: solid;
    border-color: var(--gray-800) transparent transparent transparent;
}

.tooltip-wrapper:hover .tooltip-text {
    display: block;
}

.tooltip-wrapper:hover .help-icon {
    opacity: 1;
    color: var(--primary-500);
}

/* Action Dropdown */
.action-dropdown {
    position: relative;
    display: flex;
    justify-content: center;
}

.dropdown-menu {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
    min-width: 160px;
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-lg);
    padding: 4px 0;
    z-index: var(--z-dropdown);
}

.dropdown-menu-right {
    right: 0;
    left: auto;
}

.dropdown-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    border: none;
    background: none;
    font-size: 0.8125rem;
    color: var(--text-primary);
    cursor: pointer;
    transition: background 0.15s;
    text-align: left;
}

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-tertiary);
}

.dropdown-item:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.dropdown-item.text-error {
    color: var(--error-color);
}

.dropdown-item.text-error:hover {
    background: var(--error-light);
}

.dropdown-divider {
    height: 1px;
    background: var(--border-light);
    margin: 4px 0;
}

.dropdown-enter-active,
.dropdown-leave-active {
    transition: all 0.2s ease;
}

.dropdown-enter-from,
.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-8px);
}

.btn-warning {
    background: var(--warning-color);
    color: white;
    border: none;
    padding: 8px 16px;
    border-radius: var(--radius-md);
    cursor: pointer;
    font-weight: 500;
}

.btn-warning:hover {
    background: var(--warning-dark);
}
.btn-warning:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.form-input:focus {
    outline: none;
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.deploy-success {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
}

.deploy-cmd-box {
    position: relative;
    width: 100%;
    background: var(--bg-tertiary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    padding: 12px 40px 12px 12px;
}

.deploy-cmd-box pre {
    margin: 0;
    font-size: 0.75rem;
    font-family: var(--font-family-mono);
    white-space: pre-wrap;
    word-break: break-all;
    text-align: left;
}

.copy-cmd-btn {
    position: absolute;
    top: 8px;
    right: 8px;
    background: none;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    padding: 4px 6px;
    cursor: pointer;
    color: var(--text-light);
    display: flex;
    align-items: center;
}

.copy-cmd-btn:hover {
    background: var(--bg-secondary);
}

.spinning {
    animation: spin 1s linear infinite;
}
@keyframes spin {
    to {
        transform: rotate(360deg);
    }
}
</style>
