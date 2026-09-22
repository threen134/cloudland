<script setup lang="ts">
import { ref, onMounted, computed, watch, onUnmounted, type DirectiveBinding } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { instancesApi, type Instance } from '../../api/instances'
import {
    Play,
    Square,
    Pause,
    RotateCw,
    Trash2,
    Plus,
    SquareTerminal,
    MoreVertical,
    Search,
    Check,
    Copy,
    Monitor,
    RefreshCw,
    Eye,
    EyeOff,
    Pencil,
    KeyRound,
    Maximize2,
    Activity,
    Network,
    Globe,
} from 'lucide-vue-next'

import DeleteModal from '../../components/modals/DeleteModal.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import CreateInstanceModal from '../../components/instance/CreateInstanceModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import { useRegionStore } from '../../stores/region'
import { errorMessage } from '../../utils/error'
import { usageColor } from '../../utils/usageColor'
import { formatMemory } from '../../utils/format'

const region = useRegionStore()

// 指令把监听器挂在元素上，卸载时再取下来，所以元素类型要带上这个附加字段
type ClickOutsideEl = HTMLElement & { clickOutsideEvent?: (event: Event) => void }

const vClickOutside = {
    mounted(el: ClickOutsideEl, binding: DirectiveBinding<(event: Event) => void>) {
        el.clickOutsideEvent = (event: Event) => {
            if (!(el === event.target || el.contains(event.target as Node))) {
                binding.value(event)
            }
        }
        document.addEventListener('click', el.clickOutsideEvent)
    },
    unmounted(el: ClickOutsideEl) {
        if (el.clickOutsideEvent) document.removeEventListener('click', el.clickOutsideEvent)
    },
}

const actionLoading = ref<Record<string, string | null>>({})

const router = useRouter()
const { t, te } = useI18n()
const toast = useToast()

// A value is left undefined when Prometheus has no series for the VM (shown as "-", not 0%)
const instanceMetrics = ref<Record<string, { cpu?: number; memory?: number }>>({})
// "-" until the metrics arrive (or when their request failed): a made-up 0% reads as an idle VM
const usageText = (value: number | undefined, digits = 0) => (value === undefined ? '-' : `${value.toFixed(digits)}%`)
let metricsTimer: ReturnType<typeof setInterval> | null = null
// 电源操作后的状态轮询：可能同时有多台，统一在 onUnmounted 停止
const statusPollTimers = new Set<ReturnType<typeof setTimeout>>()
let unmounted = false

const fetchUsageMetrics = async () => {
    const ids = instances.value
        .filter((inst) => ['running', 'active'].includes(inst.status?.toLowerCase()))
        .map((inst) => inst.id)
    if (!ids.length) return

    try {
        const now = Math.floor(Date.now() / 1000)
        const start = (now - 3600).toString()
        const end = now.toString()

        const [cpuRes, memRes] = await Promise.all([
            instancesApi.getCPUMetrics({ id: ids, start, end, step: '60s' }),
            instancesApi.getMemoryMetrics({ id: ids, start, end, step: '60s' }),
        ])

        const newMetrics: Record<string, { cpu?: number; memory?: number }> = {}
        ids.forEach((id) => {
            // CPU: match by metric.uuid, one-dimensional values array [{time, value}]
            const cpuResult = cpuRes?.data?.result?.find((r) => r.metric?.uuid === id)
            const cpuValues = cpuResult?.values || []
            const lastCpu = cpuValues.length ? parseFloat(cpuValues[cpuValues.length - 1].value || '0') : undefined

            // Memory: match by metric.uuid, two-dimensional values array [totalValues[], usedValues[]]
            const memResult = memRes?.data?.result?.find((r) => r.metric?.uuid === id)
            let lastMem: number | undefined
            if ((memResult?.values?.length ?? 0) >= 2) {
                const totalValues = memResult!.values[0]
                const usedValues = memResult!.values[1]
                const total = totalValues.length ? parseFloat(totalValues[totalValues.length - 1].value || '0') : 0
                const used = usedValues.length ? parseFloat(usedValues[usedValues.length - 1].value || '0') : 0
                lastMem = total > 0 ? (used / total) * 100 : undefined
            }

            newMetrics[id] = { cpu: lastCpu, memory: lastMem }
        })
        instanceMetrics.value = { ...instanceMetrics.value, ...newMetrics }
    } catch (err) {
        console.error('Failed to fetch instance usage:', err)
    }
}

// 分页、搜索、排序都在服务端做
const {
    items: instances,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: loadInstances,
    reload: reloadInstances,
} = useListQuery<Instance>(
    async ({ offset, limit, query, order }) => {
        const response = await instancesApi.fetchInstances({ offset, limit, order, query: query || undefined })
        // 列表落地后再取指标（本页的虚拟机）
        setTimeout(fetchUsageMetrics, 500)
        return { items: response.instances || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

// 电源操作后的状态轮询、改名等场景要静默刷新：表格和刷新按钮都不进入 loading 态，
// 否则每 3 秒闪一次。fetchInstances(false) 的语义与改造前一致
const silentRefresh = ref(false)
const tableLoading = computed(() => loading.value && !silentRefresh.value)

const fetchInstances = async (showLoading: boolean = true) => {
    silentRefresh.value = !showLoading
    try {
        await loadInstances()
    } finally {
        silentRefresh.value = false
    }
}

// 列定义。排序在服务端做（sortField 是 instances 表的真实列名，和列 key 不一定同名）；
// 规格、镜像、IP 来自关联表，用量是前端算的，后端 ORDER BY 排不了，所以这些列不给排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true, sortField: 'hostname' },
    { key: 'flavor', label: t('dashboard.table.flavor') },
    // Image and usage are also on the detail page: they give way first on narrow screens so the table does not scroll sideways
    { key: 'image', label: t('dashboard.table.image'), hideBelow: 1280 },
    { key: 'ip', label: t('dashboard.table.ipAddress'), width: '200px' },
    { key: 'status', label: t('dashboard.table.status'), sortable: true },
    { key: 'usage', label: t('dashboard.overview.resourceUsage'), hideBelow: 1280 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const getStatusText = (status: string) => {
    const s = status?.toLowerCase()
    if (!s) return '-'
    // 状态键定义在 dashboard.instanceStatus；缺键时 t() 会返回键路径本身，
    // 所以必须用 te() 判断，否则界面上会直接显示 dashboard.instanceStatus.xxx
    const key = `dashboard.instanceStatus.${s}`
    return te(key) ? t(key) : status
}

// --- Action States and Dropdown ---
const activeActionMenuId = ref<string | null>(null)
const toggleActionMenu = (instanceId: string) => {
    activeActionMenuId.value = activeActionMenuId.value === instanceId ? null : instanceId
}
const closeActionMenu = () => {
    activeActionMenuId.value = null
}

const activeIpPopoverId = ref<string | null>(null)
const closeIpPopover = () => {
    activeIpPopoverId.value = null
}

const { copiedId, copyId } = useCopyId()

const handleAction = async (
    instance: Instance,
    action: 'start' | 'stop' | 'restart' | 'hard_stop' | 'hard_restart' | 'pause' | 'resume'
) => {
    actionLoading.value[instance.id] = action
    closeActionMenu()
    try {
        const hostname = instance.hostname
        switch (action) {
            case 'start':
                await instancesApi.startInstance(instance.id, hostname)
                break
            case 'stop':
                await instancesApi.stopInstance(instance.id, hostname)
                break
            case 'restart':
                await instancesApi.rebootInstance(instance.id, hostname)
                break
            case 'hard_stop':
                await instancesApi.hardStopInstance(instance.id, hostname)
                break
            case 'hard_restart':
                await instancesApi.hardRebootInstance(instance.id, hostname)
                break
            case 'pause':
                await instancesApi.pauseInstance(instance.id, hostname)
                break
            case 'resume':
                await instancesApi.resumeInstance(instance.id, hostname)
                break
        }

        let targetStableStates: string[] = []
        if (['start', 'restart', 'hard_restart', 'resume'].includes(action))
            targetStableStates = ['running', 'active', 'error']
        if (['stop', 'hard_stop'].includes(action)) targetStableStates = ['stopped', 'shutoff', 'shut_off', 'error']
        if (action === 'pause') targetStableStates = ['paused', 'error']

        let attempts = 0
        const checkStatus = async () => {
            attempts++
            await fetchInstances(false)
            if (unmounted) return
            const currentInstance = instances.value.find((i) => i.id === instance.id)
            const currentStatus = currentInstance?.status?.toLowerCase() || ''
            if (!currentInstance || targetStableStates.includes(currentStatus) || attempts >= 15) {
                actionLoading.value[instance.id] = null
                if (currentStatus === 'error') {
                    toast.error(
                        t('dashboard.instanceDetail.actionFailed', { action: t(`dashboard.instanceDetail.${action}`) })
                    )
                } else {
                    toast.success(
                        t('dashboard.instanceDetail.actionSuccess', { action: t(`dashboard.instanceDetail.${action}`) })
                    )
                }
            } else {
                statusPollTimers.add(setTimeout(checkStatus, 3000))
            }
        }

        statusPollTimers.add(setTimeout(checkStatus, 2000))
    } catch (error) {
        console.error(`Failed to ${action} instance:`, error)
        actionLoading.value[instance.id] = null
        toast.error(errorMessage(error, t('dashboard.userDetail.loadError')))
    }
}

// --- Rename Modal Logic ---
const renameModalVisible = ref(false)
const renameForm = ref({ hostname: '' })
const renameLoading = ref(false)
const renameError = ref('')
const selectedInstance = ref<Instance | null>(null)

const openRenameModal = (instance: Instance) => {
    selectedInstance.value = instance
    renameForm.value.hostname = instance.hostname || ''
    renameError.value = ''
    renameModalVisible.value = true
    closeActionMenu()
}

const confirmRename = async () => {
    if (!selectedInstance.value) return
    if (!renameForm.value.hostname.trim()) {
        renameError.value = t('dashboard.instanceDetail.hostnameRequired')
        return
    }
    renameLoading.value = true
    renameError.value = ''
    try {
        await instancesApi.renameInstance(selectedInstance.value.id, renameForm.value.hostname)
        await fetchInstances(false)
        renameModalVisible.value = false
        toast.success(t('dashboard.instanceDetail.renameSuccess'))
    } catch (err) {
        renameError.value = errorMessage(err, t('dashboard.userDetail.loadError'))
    } finally {
        renameLoading.value = false
    }
}

// --- Reset Password Modal Logic ---
const resetPasswordModalVisible = ref(false)
const resetPasswordForm = ref({ user_name: 'root', password: '', confirmPassword: '' })
const resetPasswordLoading = ref(false)
const resetPasswordError = ref('')
const showResetPassword = ref(false)

const openResetPasswordModal = (instance: Instance) => {
    selectedInstance.value = instance
    resetPasswordForm.value = { user_name: 'root', password: '', confirmPassword: '' }
    resetPasswordError.value = ''
    showResetPassword.value = false
    resetPasswordModalVisible.value = true
    closeActionMenu()
}

const generateRandomResetPassword = () => {
    const charset = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%&*'
    const array = new Uint8Array(16)
    crypto.getRandomValues(array)
    const pwd = Array.from(array, (b) => charset[b % charset.length]).join('')
    resetPasswordForm.value.password = pwd
    resetPasswordForm.value.confirmPassword = pwd
    showResetPassword.value = true
}

const confirmResetPassword = async () => {
    if (!selectedInstance.value) return
    if (resetPasswordForm.value.password.length < 8) {
        resetPasswordError.value = t('dashboard.instanceDetail.passwordMinLength')
        return
    }
    if (resetPasswordForm.value.password !== resetPasswordForm.value.confirmPassword) {
        resetPasswordError.value = t('dashboard.instanceDetail.passwordMismatch')
        return
    }
    resetPasswordLoading.value = true
    resetPasswordError.value = ''
    try {
        await instancesApi.setUserPassword(
            selectedInstance.value.id,
            resetPasswordForm.value.user_name,
            resetPasswordForm.value.password
        )
        resetPasswordModalVisible.value = false
        toast.success(t('dashboard.instanceDetail.resetPasswordSuccess'))
    } catch (err) {
        resetPasswordError.value = errorMessage(err, t('dashboard.userDetail.loadError'))
    } finally {
        resetPasswordLoading.value = false
    }
}

// --- Resize Modal Logic ---
const resizeModalVisible = ref(false)
const resizeForm = ref({ cpu: 0, memory: 0 })
const resizeLoading = ref(false)
const resizeError = ref('')

const openResizeModal = (instance: Instance) => {
    selectedInstance.value = instance
    resizeForm.value = {
        cpu: instance.cpu || 0,
        memory: instance.memory || 0,
    }
    resizeError.value = ''
    resizeModalVisible.value = true
    closeActionMenu()
}

const confirmResize = async () => {
    if (!selectedInstance.value) return
    if (resizeForm.value.cpu < 1 || resizeForm.value.memory < 1) {
        resizeError.value = t('dashboard.instanceDetail.resizeInvalid')
        return
    }
    resizeLoading.value = true
    resizeError.value = ''
    try {
        await instancesApi.resizeInstance(selectedInstance.value.id, resizeForm.value.cpu, resizeForm.value.memory)
        resizeModalVisible.value = false
        toast.success(t('dashboard.instanceDetail.resizeSuccess'))
        await fetchInstances(false)
    } catch (err) {
        resizeError.value = errorMessage(err, t('dashboard.userDetail.loadError'))
    } finally {
        resizeLoading.value = false
    }
}

// The console page embeds the bundled noVNC client and requests the console token itself.
// Opened without noopener so the new window inherits sessionStorage (login session).
const openConsole = (instance: Instance, type: 'vnc' | 'serial' = 'vnc') => {
    const name = type === 'serial' ? 'instance-serial-console' : 'instance-console'
    const url = router.resolve({ name, params: { id: instance.id } }).href
    if (!window.open(url, '_blank')) {
        toast.error(t('dashboard.instanceDetail.popupBlocked'))
    }
}

// --- Delete Confirmation Modal Logic ---
const deleteModalVisible = ref(false)
const deletingInstance = ref(false)
const deleteError = ref('')
const instanceToDelete = ref<Instance | null>(null)

const handleDeleteClick = (instance: Instance) => {
    instanceToDelete.value = instance
    deleteModalVisible.value = true
}

const closeDeleteModal = () => {
    deleteModalVisible.value = false
    instanceToDelete.value = null
    deleteError.value = ''
}

const confirmDelete = async () => {
    if (!instanceToDelete.value) return
    deletingInstance.value = true
    deleteError.value = ''
    try {
        await instancesApi.deleteInstance(instanceToDelete.value.id)
        await fetchInstances()
        closeDeleteModal()
        toast.success(t('dashboard.instanceDetail.actionSuccess', { action: t('dashboard.buttons.delete') }))
    } catch (error) {
        console.error('Failed to delete instance:', error)
        deleteError.value = errorMessage(error, t('dashboard.userDetail.loadError'))
    } finally {
        deletingInstance.value = false
    }
}

// 创建虚拟机的表单与提交逻辑已拆到 components/instance/CreateInstanceModal.vue
const createModalVisible = ref(false)
const openCreateModal = () => {
    createModalVisible.value = true
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchInstances()
        metricsTimer = setInterval(fetchUsageMetrics, 30000) // update every 30s
    }
})

// 列表本身由 useListQuery 的 watchSources 在切换区域时重载，这里只重置指标定时器
watch(
    () => region.currentRegionId,
    (newId) => {
        if (newId) {
            if (metricsTimer) clearInterval(metricsTimer)
            metricsTimer = setInterval(fetchUsageMetrics, 30000)
        }
    }
)

onUnmounted(() => {
    if (metricsTimer) clearInterval(metricsTimer)
    // 电源操作后的状态轮询每台最长 45 秒，不停掉会在离开后继续请求并弹出本页的提示
    unmounted = true
    statusPollTimers.forEach(clearTimeout)
    statusPollTimers.clear()
})
</script>

<template>
    <div>
        <!-- Toast Notification removed (using global ToastContainer) -->

        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="fetchInstances()"
                    :title="t('dashboard.regions')"
                >
                    <RefreshCw :size="14" :class="{ spinning: tableLoading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ t('dashboard.buttons.createInstance') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            allow-overflow
            :columns="columns"
            :rows="instances"
            row-key="id"
            :loading="tableLoading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="fetchInstances()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ t('dashboard.table.noResults') }}</p>
                </div>
                <div v-else>
                    <Monitor :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p class="text-secondary">{{ t('dashboard.table.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: instance }">
                <router-link :to="{ name: 'instance-detail', params: { id: instance.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <Monitor :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ instance.hostname }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="instance.id">{{ instance.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(instance.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check
                                        v-if="copiedId === instance.id"
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

            <template #cell-flavor="{ row: instance }">
                <div class="specs-display">
                    <div class="specs-main">
                        {{ instance.cpu }}C /
                        {{ formatMemory(instance.memory).replace(' GB', 'G').replace(' MB', 'M') }}
                    </div>
                    <div v-if="instance.flavor" class="specs-sub">
                        {{ instance.flavor }}
                    </div>
                </div>
            </template>

            <template #cell-image="{ row: instance }">{{ instance.image?.name || '-' }}</template>

            <template #cell-ip="{ row: instance }">
                <div class="ip-display-wrapper" @mouseleave="closeIpPopover">
                    <!-- Show first 2 interfaces inline -->
                    <div class="ip-list">
                        <div v-for="(iface, idx) in (instance.interfaces || []).slice(0, 2)" :key="idx" class="ip-item">
                            <code class="ip-address">{{ iface.ip_address?.split('/')[0] || '-' }}</code>
                            <span
                                v-if="iface.floating_ips?.find((f) => f.type?.toLowerCase() !== 'native')?.fip_address"
                                class="fip-inline"
                            >
                                <Globe :size="10" />
                                <code>{{
                                    iface.floating_ips
                                        ?.find((f) => f.type?.toLowerCase() !== 'native')
                                        ?.fip_address.split('/')[0]
                                }}</code>
                            </span>
                        </div>
                    </div>

                    <!-- +more badge & Popover if > 2 interfaces -->
                    <div v-if="instance.interfaces && instance.interfaces.length > 2" class="more-ips-trigger">
                        <button class="badge badge-multi-iface clickable" @mouseenter="activeIpPopoverId = instance.id">
                            <Network :size="10" />+{{ instance.interfaces.length - 2 }}
                            {{ t('dashboard.instanceDetail.more').toLowerCase() }}
                        </button>

                        <Transition name="fade">
                            <div v-if="activeIpPopoverId === instance.id" class="ip-popover card shadow-lg" @click.stop>
                                <div class="popover-header">
                                    <Activity :size="12" /> {{ t('dashboard.instanceDetail.interfaceDetails') }} ({{
                                        instance.interfaces.length
                                    }})
                                </div>
                                <table class="mini-data-table">
                                    <thead>
                                        <tr>
                                            <th>{{ t('dashboard.instanceDetail.interface') }}</th>
                                            <th>{{ t('dashboard.forms.subnet') }}</th>
                                            <th>{{ t('dashboard.table.private') }} IP</th>
                                            <th>FIP</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        <tr v-for="(iface, idx) in instance.interfaces" :key="idx">
                                            <td>
                                                <span class="text-xs font-mono">eth{{ idx }}</span>
                                            </td>
                                            <td>
                                                <span class="text-xs text-secondary">{{
                                                    iface.subnet?.name ||
                                                    iface.subnet?.id?.substring(0, 8) + '...' ||
                                                    '-'
                                                }}</span>
                                            </td>
                                            <td>
                                                <code class="text-xs">{{
                                                    iface.ip_address?.split('/')[0] || '-'
                                                }}</code>
                                            </td>
                                            <td>
                                                <code
                                                    v-if="iface.floating_ips && iface.floating_ips.length > 0"
                                                    class="text-xs text-primary"
                                                >
                                                    {{
                                                        iface.floating_ips
                                                            .find((f) => f.type?.toLowerCase() !== 'native')
                                                            ?.fip_address?.split('/')[0] || '-'
                                                    }}
                                                </code>
                                                <span v-else class="text-xs text-secondary">-</span>
                                            </td>
                                        </tr>
                                    </tbody>
                                </table>
                            </div>
                        </Transition>
                    </div>
                </div>
            </template>

            <template #cell-status="{ row: instance }">
                <StatusBadge :status="instance.status" :label="getStatusText(instance.status)" />
            </template>

            <template #cell-usage="{ row: instance }">
                <div
                    class="instance-usage-summary"
                    v-if="['running', 'active'].includes(instance.status?.toLowerCase())"
                >
                    <div class="usage-mini-item" :title="'CPU: ' + usageText(instanceMetrics[instance.id]?.cpu, 1)">
                        <span class="usage-label">CPU</span>
                        <div class="usage-progress-bg">
                            <div
                                class="usage-progress-bar"
                                :style="{
                                    width: (instanceMetrics[instance.id]?.cpu || 0) + '%',
                                    background: usageColor(instanceMetrics[instance.id]?.cpu || 0),
                                }"
                            ></div>
                        </div>
                        <span class="usage-value">{{ usageText(instanceMetrics[instance.id]?.cpu) }}</span>
                    </div>
                    <div class="usage-mini-item" :title="'MEM: ' + usageText(instanceMetrics[instance.id]?.memory, 1)">
                        <span class="usage-label">MEM</span>
                        <div class="usage-progress-bg">
                            <div
                                class="usage-progress-bar"
                                :style="{
                                    width: (instanceMetrics[instance.id]?.memory || 0) + '%',
                                    background: usageColor(instanceMetrics[instance.id]?.memory || 0),
                                }"
                            ></div>
                        </div>
                        <span class="usage-value">{{ usageText(instanceMetrics[instance.id]?.memory) }}</span>
                    </div>
                </div>
                <span v-else class="text-secondary text-xs">-</span>
            </template>

            <template #cell-actions="{ row: instance }">
                <div class="row-actions">
                    <!-- 菜单展开时把这一格抬到上层：原先是给整行加 .active-row -->
                    <div
                        class="action-dropdown"
                        style="position: relative"
                        :class="{ 'active-row': activeActionMenuId === instance.id }"
                    >
                        <button
                            class="icon-btn-table"
                            @click.stop="toggleActionMenu(instance.id)"
                            :title="t('dashboard.instanceDetail.more')"
                        >
                            <MoreVertical :size="16" />
                        </button>
                        <Transition name="dropdown">
                            <div
                                v-if="activeActionMenuId === instance.id"
                                class="dropdown-menu dropdown-menu-right"
                                @click.stop
                                v-click-outside="closeActionMenu"
                            >
                                <button
                                    v-if="['stopped', 'shutoff', 'shut_off'].includes(instance.status?.toLowerCase())"
                                    class="dropdown-item"
                                    @click="handleAction(instance, 'start')"
                                    :disabled="!!actionLoading[instance.id]"
                                >
                                    <Play :size="14" /> {{ t('dashboard.instanceDetail.start') }}
                                </button>
                                <button
                                    v-else
                                    class="dropdown-item"
                                    @click="handleAction(instance, 'stop')"
                                    :disabled="!!actionLoading[instance.id]"
                                >
                                    <Square :size="14" /> {{ t('dashboard.instanceDetail.stop') }}
                                </button>
                                <button
                                    class="dropdown-item"
                                    @click="handleAction(instance, 'restart')"
                                    :disabled="
                                        !!actionLoading[instance.id] ||
                                        !['running', 'active'].includes(instance.status?.toLowerCase())
                                    "
                                >
                                    <RotateCw :size="14" /> {{ t('dashboard.instanceDetail.restart') }}
                                </button>

                                <button class="dropdown-item" @click="openConsole(instance)">
                                    <Monitor :size="14" /> {{ t('dashboard.instanceDetail.console') }}
                                </button>
                                <button class="dropdown-item" @click="openConsole(instance, 'serial')">
                                    <SquareTerminal :size="14" /> {{ t('dashboard.console.serial.title') }}
                                </button>

                                <div class="dropdown-divider"></div>

                                <button
                                    class="dropdown-item"
                                    @click="handleAction(instance, 'hard_stop')"
                                    :disabled="
                                        !!actionLoading[instance.id] ||
                                        ['stopped', 'shutoff', 'shut_off', 'paused'].includes(
                                            instance.status?.toLowerCase()
                                        )
                                    "
                                >
                                    <Square :size="14" /> {{ t('dashboard.instanceDetail.hardStop') }}
                                </button>
                                <button
                                    class="dropdown-item"
                                    @click="handleAction(instance, 'hard_restart')"
                                    :disabled="
                                        !!actionLoading[instance.id] ||
                                        ['stopped', 'shutoff', 'shut_off'].includes(instance.status?.toLowerCase())
                                    "
                                >
                                    <RefreshCw :size="14" /> {{ t('dashboard.instanceDetail.hardRestart') }}
                                </button>
                                <button
                                    v-if="instance.status?.toLowerCase() !== 'paused'"
                                    class="dropdown-item"
                                    @click="handleAction(instance, 'pause')"
                                    :disabled="
                                        !!actionLoading[instance.id] || instance.status?.toLowerCase() !== 'running'
                                    "
                                >
                                    <Pause :size="14" /> {{ t('dashboard.instanceDetail.pause') }}
                                </button>
                                <button
                                    v-else
                                    class="dropdown-item"
                                    @click="handleAction(instance, 'resume')"
                                    :disabled="!!actionLoading[instance.id]"
                                >
                                    <Play :size="14" /> {{ t('dashboard.instanceDetail.resume') }}
                                </button>

                                <div class="dropdown-divider"></div>

                                <button class="dropdown-item" @click="openRenameModal(instance)">
                                    <Pencil :size="14" /> {{ t('dashboard.instanceDetail.rename') }}
                                </button>
                                <button class="dropdown-item" @click="openResetPasswordModal(instance)">
                                    <KeyRound :size="14" /> {{ t('dashboard.instanceDetail.resetPassword') }}
                                </button>
                                <button class="dropdown-item" @click="openResizeModal(instance)">
                                    <Maximize2 :size="14" /> {{ t('dashboard.instanceDetail.resize') }}
                                </button>

                                <div class="dropdown-divider"></div>

                                <button class="dropdown-item dropdown-item-danger" @click="handleDeleteClick(instance)">
                                    <Trash2 :size="14" /> {{ t('dashboard.instanceDetail.delete') }}
                                </button>
                            </div>
                        </Transition>
                    </div>
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

        <CreateInstanceModal
            :show="createModalVisible"
            @close="createModalVisible = false"
            @created="reloadInstances()"
        />

        <!-- Rename Modal -->
        <BaseModal
            :show="renameModalVisible"
            :title="t('dashboard.instanceDetail.rename')"
            size="sm"
            :loading="renameLoading"
            form
            @close="renameModalVisible = false"
            @submit="confirmRename"
        >
            <div class="form-group">
                <label class="form-label">{{ t('dashboard.instanceDetail.hostname') }}</label>
                <input
                    id="renameHostname"
                    name="hostname"
                    v-model="renameForm.hostname"
                    type="text"
                    class="form-input"
                    :placeholder="t('dashboard.table.hostname')"
                />
            </div>
            <div v-if="renameError" class="text-error mt-2">{{ renameError }}</div>

            <template #footer>
                <button type="button" class="btn btn-ghost" @click="renameModalVisible = false">
                    {{ t('dashboard.buttons.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="renameLoading">
                    <span v-if="renameLoading" class="loading-spinner small"></span>
                    {{ t('dashboard.buttons.confirm') }}
                </button>
            </template>
        </BaseModal>

        <!-- Reset Password Modal -->
        <BaseModal
            :show="resetPasswordModalVisible"
            :title="t('dashboard.instanceDetail.resetPassword')"
            :loading="resetPasswordLoading"
            form
            @close="resetPasswordModalVisible = false"
            @submit="confirmResetPassword"
        >
            <div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.instanceDetail.userName') }}</label>
                    <input v-model="resetPasswordForm.user_name" type="text" class="form-input" disabled />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.instanceDetail.rootPassword') }}</label>
                    <div class="password-input-wrapper">
                        <input
                            id="resetPassword"
                            name="password"
                            v-model="resetPasswordForm.password"
                            :type="showResetPassword ? 'text' : 'password'"
                            class="form-input"
                            :placeholder="t('dashboard.instanceDetail.passwordPlaceholder')"
                            autocomplete="new-password"
                        />
                        <button type="button" class="password-toggle" @click="showResetPassword = !showResetPassword">
                            <Eye v-if="!showResetPassword" :size="16" />
                            <EyeOff v-else :size="16" />
                        </button>
                    </div>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.instanceDetail.confirmPassword') }}</label>
                    <input
                        id="confirmResetPassword"
                        name="confirmPassword"
                        v-model="resetPasswordForm.confirmPassword"
                        :type="showResetPassword ? 'text' : 'password'"
                        class="form-input"
                        :placeholder="t('dashboard.instanceDetail.confirmPassword')"
                        autocomplete="new-password"
                    />
                </div>
                <div class="mt-2">
                    <button
                        type="button"
                        class="btn btn-ghost btn-sm text-primary"
                        @click="generateRandomResetPassword"
                    >
                        <RefreshCw :size="14" /> {{ t('dashboard.instanceDetail.generatePassword') }}
                    </button>
                </div>
                <div v-if="resetPasswordError" class="text-error mt-3">{{ resetPasswordError }}</div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-ghost" @click="resetPasswordModalVisible = false">
                    {{ t('dashboard.buttons.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="resetPasswordLoading">
                    <span v-if="resetPasswordLoading" class="loading-spinner small"></span>
                    {{ t('dashboard.buttons.confirm') }}
                </button>
            </template>
        </BaseModal>

        <!-- Resize Modal -->
        <BaseModal
            :show="resizeModalVisible"
            :title="t('dashboard.instanceDetail.resize')"
            :loading="resizeLoading"
            form
            @close="resizeModalVisible = false"
            @submit="confirmResize"
        >
            <div class="form-row">
                <div class="form-group">
                    <label class="form-label">CPU ({{ t('dashboard.overview.cpuUnit') }})</label>
                    <input v-model.number="resizeForm.cpu" type="number" class="form-input" min="1" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.overview.memory') }} (MB)</label>
                    <input v-model.number="resizeForm.memory" type="number" class="form-input" min="128" step="128" />
                </div>
            </div>
            <div v-if="resizeError" class="text-error mt-2">{{ resizeError }}</div>

            <template #footer>
                <button type="button" class="btn btn-ghost" @click="resizeModalVisible = false">
                    {{ t('dashboard.buttons.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="resizeLoading">
                    <span v-if="resizeLoading" class="loading-spinner small"></span>
                    {{ t('dashboard.buttons.confirm') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleteModalVisible"
            :resource-name="instanceToDelete?.name || instanceToDelete?.hostname"
            :resource-id="instanceToDelete?.id"
            :loading="deletingInstance"
            :error="deleteError"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
.dropdown-menu {
    position: absolute;
    top: 100%;
    right: 0;
    z-index: var(--z-dropdown);
    min-width: 180px;
    padding: 8px;
    margin-top: 4px;
    background: white;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
}

.dropdown-menu-right {
    right: 0;
}

.dropdown-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    border: none;
    background: transparent;
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
    transition: background 0.2s;
}

.dropdown-item:hover {
    background: var(--bg-secondary);
}

.dropdown-item:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.dropdown-item-danger {
    color: var(--error-color);
}

.dropdown-item-danger:hover {
    background: var(--error-light);
}

.dropdown-divider {
    height: 1px;
    background: var(--border-light);
    margin: 8px 0;
}

.dropdown-enter-active,
.dropdown-leave-active {
    transition:
        opacity 0.2s,
        transform 0.2s;
}

.dropdown-enter-from,
.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-8px);
}

.password-input-wrapper {
    position: relative;
    display: flex;
    align-items: center;
}

.password-toggle {
    position: absolute;
    right: 8px;
    background: transparent;
    border: none;
    color: var(--text-light);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
}

.active-row {
    position: relative;
    z-index: 10;
}

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

.flavor-info {
    display: flex;
    flex-direction: column;
}

/* Standardized specs use resource-info pattern */

.ip-address {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-sm);
}

.ip-display-wrapper {
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 8px;
}

.ip-list {
    display: flex;
    flex-direction: column;
    gap: 3px;
}

.ip-item {
    display: flex;
    align-items: center;
    gap: 6px;
}

.fip-inline {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    color: var(--primary-600);
    font-size: var(--font-size-sm);
}

.more-ips-trigger {
    position: relative;
}

.clickable {
    cursor: pointer;
}

.badge-multi-iface {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    background-color: var(--primary-600);
    color: var(--text-inverse);
    border: none;
    border-radius: 4px;
    font-size: 0.7rem;
    font-weight: 600;
    padding: 2px 6px;
}

.badge-multi-iface:hover {
    background-color: var(--primary-700);
}

.ip-popover {
    position: absolute;
    top: 100%;
    left: 0;
    z-index: var(--z-tooltip);
    margin-top: 8px;
    width: 340px;
    padding: 0;
    background: white;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    overflow: hidden;
    box-shadow: var(--shadow-lg);
}

.popover-header {
    background: var(--bg-secondary);
    padding: 8px 12px;
    font-size: 11px;
    font-weight: 600;
    color: var(--text-secondary);
    border-bottom: 1px solid var(--border-light);
    display: flex;
    align-items: center;
    gap: 6px;
}

.fade-enter-active,
.fade-leave-active {
    transition:
        opacity 0.2s,
        transform 0.2s;
}

.fade-enter-from,
.fade-leave-to {
    opacity: 0;
    transform: translateY(-5px);
}

.btn:disabled {
    opacity: 0.3;
    cursor: not-allowed;
}

/* Modal Styles */

.form-group {
    margin-bottom: var(--spacing-4);
}

.form-label {
    display: block;
    margin-bottom: var(--spacing-2);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
}

.form-input,
.multiple-select {
    height: 100px;
    background-image: none;
    padding-right: 12px;
}

.sub-modal {
    z-index: var(--z-modal-nested);
}
.password-modal {
    max-width: 400px;
}

.password-input-wrapper {
    position: relative;
    display: flex;
    align-items: center;
}

.password-input-wrapper .form-input {
    padding-right: 32px;
}

.btn-xs {
    padding: 2px 8px;
    font-size: 0.75rem;
}
.justify-start {
    justify-content: flex-start !important;
}
.gap-4 {
    gap: 16px;
}

.form-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-4);
}

@keyframes tooltipFadeIn {
    from {
        opacity: 0;
        transform: scale(0.95) translateY(10px);
    }
    to {
        opacity: 1;
        transform: scale(1) translateY(0);
    }
}

.mt-2 {
    margin-top: 8px;
}

/* Switch styling */

.text-xs {
    font-size: 0.75rem;
}
.text-secondary {
    color: var(--text-light);
}

.input-error {
    border-color: var(--error-color) !important;
}

.input-error:focus {
    box-shadow: 0 0 0 2px var(--error-light) !important;
}

.btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
    filter: grayscale(100%);
}

.loading-spinner.small {
    width: 14px;
    height: 14px;
    border-width: 2px;
}

/* Toast Notification removed */
.instance-usage-summary {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 80px;
    padding: 2px 0;
}

.usage-mini-item {
    display: flex;
    align-items: center;
    gap: 6px;
}

.usage-label {
    font-size: 10px;
    font-weight: 600;
    color: var(--text-tertiary);
    width: 24px;
    flex-shrink: 0;
}

/* The percentage used to be only in the hover title, yet it is the actual information; the bar
   just shows high / low at a glance. Show the number, the bar becomes secondary */
.usage-value {
    font-size: 11px;
    color: var(--text-secondary);
    font-variant-numeric: tabular-nums;
    width: 30px;
    text-align: right;
    flex-shrink: 0;
}

.usage-progress-bg {
    flex: 1;
    height: 4px;
    background: var(--bg-tertiary);
    border-radius: 2px;
    overflow: hidden;
}

.usage-progress-bar {
    height: 100%;
    border-radius: 2px;
    transition: width 0.3s ease;
}

.text-xs {
    font-size: 11px;
}
</style>
