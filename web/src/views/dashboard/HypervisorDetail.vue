<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import {
    hypervisorsApi,
    type Hypervisor,
    type HyperPatchPayload,
    type HyperListResponse,
    type HyperMaintainResponse,
} from '../../api/hypervisors'
import { instancesApi, type Instance, type InstanceListResponse } from '../../api/instances'
import { zonesApi, type Zone, type ZoneListResponse } from '../../api/zones'
import { errorMessage } from '../../utils/error'
import {
    ArrowLeft,
    Server,
    Copy,
    Check,
    Wrench,
    Loader2,
    ChevronDown,
    Pencil,
    Info,
    Activity,
    SquareTerminal,
    HardDrive,
} from 'lucide-vue-next'
import HostMonitoringCharts from '../../components/monitoring/HostMonitoringCharts.vue'
import HostStorageTab from '../../components/storage/HostStorageTab.vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useHostConsole } from '../../composables/useHostConsole'
import { formatMemory, formatDisk, formatBytes } from '../../utils/format'
import type { StatusVariant } from '../../utils/status'
import BaseModal from '../../components/modals/BaseModal.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import DetailTabs from '../../components/base/DetailTabs.vue'
import { useCopyId } from '../../composables/useCopyId'
import { useGoBack } from '../../composables/useGoBack'

const { t, te } = useI18n()
const toast = useToast()
const route = useRoute()
const { copiedId: copiedField, copyId: copyToClipboard } = useCopyId()
const goBack = useGoBack('hypervisors')
const hypervisor = ref<Hypervisor | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const saving = ref(false)
const editMode = ref(false)
const zoneList = ref<Zone[]>([])
const showActionMenu = ref(false)
const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}
const closeActionMenu = () => {
    showActionMenu.value = false
}
const { disabledReason: consoleDisabledReason, openHostConsole } = useHostConsole()
// 菜单项要先收起菜单再开终端；写成一个方法而不是模板里的两句，
// prettier 的 semi:false 会把内联的两句拆成两行并去掉分号，Vue 解析不了
const handleHostConsole = (uuid: string) => {
    closeActionMenu()
    openHostConsole(uuid)
}

const STATUS_MAP: Record<number, { label: string; variant: StatusVariant }> = {
    0: { label: 'dashboard.hypervisorStatus.disabled', variant: 'neutral' },
    1: { label: 'dashboard.hypervisorStatus.active', variant: 'success' },
    2: { label: 'dashboard.hypervisorStatus.maintaining', variant: 'warning' },
    4: { label: 'dashboard.hypervisorStatus.deploying', variant: 'pending' },
    5: { label: 'dashboard.hypervisorStatus.deployFailed', variant: 'error' },
}

const form = ref({
    status: 0 as number | undefined,
    // ⚠️ 存的是 zone 的 uuid：GET /zones 的 ResourceReference.id 就是 uuid，
    // PATCH /hypers 的 zone_id 现在收的也是 UUID（api/src/apis/hyper.go 的 ZoneID *string）
    zone_id: '' as string,
    cpu_over_rate: 1,
    mem_over_rate: 1,
    disk_over_rate: 1,
    remark: '',
})

// Maintain modal
const showMaintainModal = ref(false)
const maintaining = ref(false)
const maintainForm = ref({
    migrate: true,
    target_hyper: -1,
})

const activeTab = ref('overview')
const tabs = computed(() => [
    { id: 'overview', label: t('dashboard.table.overview'), icon: Info },
    { id: 'storage', label: t('storage.title'), icon: HardDrive },
    { id: 'monitor', label: t('dashboard.instanceDetail.resourceMonitoring'), icon: Activity },
])

// 该节点上的虚拟机列表（概览卡片），按 host id 过滤
const hyperInstances = ref<Instance[]>([])
const hyperInstancesLoading = ref(false)

const fetchHyperInstances = async (hostid: number) => {
    hyperInstancesLoading.value = true
    try {
        const resp = await instancesApi.fetchInstances({ hyper: hostid, limit: 200 })
        const data = resp as InstanceListResponse | Instance[]
        hyperInstances.value = Array.isArray(data) ? data : data.instances || []
    } catch (err) {
        console.error('Failed to load instances of hypervisor:', err)
        hyperInstances.value = []
    } finally {
        hyperInstancesLoading.value = false
    }
}

// 缺键时 t() 返回键路径本身，必须用 te() 判断后再回退到原始状态串
const instanceStatusText = (status: string) => {
    const s = (status || '').toLowerCase()
    if (!s) return '-'
    const key = `dashboard.instanceStatus.${s}`
    return te(key) ? t(key) : status
}

// 进入维护模式只是「开始腾空」，虚拟机迁移是异步的；仅凭「维护中」看不出能否断电。
// 这里用本页已经拉取的虚拟机列表长度判断（详情接口不返回 instance_count），
// 且用剩余虚拟机数而非进行中的迁移数——迁移失败时虚拟机会留在原地
const drainHint = computed(() => {
    if (hypervisor.value?.status !== 2) return ''
    const left = hyperInstances.value.length
    return left > 0
        ? t('dashboard.hypervisorStatus.draining', { count: left })
        : t('dashboard.hypervisorStatus.drained')
})

// created_at 形如 "2026-09-16 00:36:12.343038"，不带时区标记；直接截到分钟展示，
// 交给 Date 解析在不同浏览器和时区下会出现偏移
const formatInstanceTime = (value?: string) => (value ? String(value).slice(0, 16) : '-')

const instanceStatusVariant = (status: string): StatusVariant => {
    const s = (status || '').toLowerCase()
    if (s === 'running' || s === 'active' || s === 'migrated') return 'success'
    if (s === 'error' || s === 'unknown' || s === 'rollback') return 'error'
    if (s === 'shut_off' || s === 'shutoff' || s === 'stopped' || s === 'deleted') return 'neutral'
    return 'warning'
}

const fetchHypervisorDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const uuid = route.params.id as string
        hypervisor.value = await hypervisorsApi.getHypervisor(uuid)
        syncForm()
        // 详情返回后才知道 host id，虚拟机列表随后单独拉取
        if (hypervisor.value?.hostid !== undefined) {
            await fetchHyperInstances(hypervisor.value.hostid)
        }
    } catch (err) {
        console.error('Failed to fetch hypervisor detail:', err)
        error.value = errorMessage(err, t('dashboard.hypervisorDetail.loadError'))
    } finally {
        loading.value = false
    }
}

// 返回的是 zone uuid，见 form.zone_id 的说明
const currentZoneId = computed(() => {
    if (!hypervisor.value) return ''
    const match = zoneList.value.find((z) => z.name === hypervisor.value!.zone_name)
    return match ? match.id : ''
})

const syncForm = () => {
    if (!hypervisor.value) return
    form.value = {
        status: hypervisor.value.status,
        zone_id: currentZoneId.value,
        cpu_over_rate: hypervisor.value.cpu_over_rate || 1,
        mem_over_rate: hypervisor.value.mem_over_rate || 1,
        disk_over_rate: hypervisor.value.disk_over_rate || 1,
        remark: hypervisor.value.remark || '',
    }
}

const getStatusVariant = (status: number): StatusVariant => STATUS_MAP[status]?.variant ?? 'neutral'

// statusName 是后端返回的英文原文（active / maintaining …），只在该状态没有对应翻译时兜底。
// 此前模板写成 `status_name || t(...)`，英文原文非空就短路了，翻译永远不生效
const getStatusLabel = (status: number, statusName?: string) => {
    const info = STATUS_MAP[status]
    return info ? t(info.label) : statusName || `Unknown(${status})`
}

const toggleEdit = async () => {
    closeActionMenu()
    editMode.value = true
    try {
        const resp = await zonesApi.fetchZones()
        const data = resp as ZoneListResponse | Zone[]
        zoneList.value = Array.isArray(data) ? data : data.zones || []
    } catch {
        zoneList.value = []
    }
    syncForm()
}

const cancelEdit = () => {
    editMode.value = false
    syncForm()
}

const handleSave = async () => {
    if (!hypervisor.value) return
    saving.value = true
    try {
        const payload: HyperPatchPayload = {}
        if (form.value.status !== hypervisor.value.status) payload.status = form.value.status
        // 下拉里不再有「-」：后端不支持清空可用区，选它保存必然 400
        if (form.value.zone_id !== currentZoneId.value) payload.zone_id = form.value.zone_id
        if (form.value.cpu_over_rate !== hypervisor.value.cpu_over_rate)
            payload.cpu_over_rate = Number(form.value.cpu_over_rate)
        if (form.value.mem_over_rate !== hypervisor.value.mem_over_rate)
            payload.mem_over_rate = Number(form.value.mem_over_rate)
        if (form.value.disk_over_rate !== hypervisor.value.disk_over_rate)
            payload.disk_over_rate = Number(form.value.disk_over_rate)
        if (form.value.remark !== (hypervisor.value.remark || '')) payload.remark = form.value.remark

        await hypervisorsApi.updateHypervisor(hypervisor.value.uuid, payload)
        await fetchHypervisorDetail()
        editMode.value = false
        toast.success(t('messages.success'))
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        saving.value = false
    }
}

// 维护模式可选的目标节点：本页只持有当前节点，打开对话框时按需拉取列表；
// 排除当前节点本身（迁到自己后端会直接跳过），只列活动状态的节点
const maintainTargetOptions = ref<Hypervisor[]>([])

// Maintain
const openMaintainModal = async () => {
    closeActionMenu()
    maintainForm.value = { migrate: true, target_hyper: -1 }
    showMaintainModal.value = true
    try {
        const resp = await hypervisorsApi.fetchHypervisors()
        const data = resp as HyperListResponse | Hypervisor[]
        const list = Array.isArray(data) ? data : data.hypers || []
        maintainTargetOptions.value = list.filter((h) => h.status === 1 && h.hostid !== hypervisor.value?.hostid)
    } catch (err) {
        console.error('Failed to load hypervisors for maintenance target:', err)
        maintainTargetOptions.value = []
    }
}

const maintainResults = ref<HyperMaintainResponse['instances']>([])

const handleMaintain = async () => {
    if (!hypervisor.value) return
    maintaining.value = true
    try {
        const resp = await hypervisorsApi.maintainHypervisor(hypervisor.value.uuid, maintainForm.value)
        showMaintainModal.value = false
        maintainResults.value = resp.instances || []
        const skipped = maintainResults.value.filter((r) => r.status === 'not_doing').length
        if (skipped) toast.warning(t('storage.maintainSkipped', { n: skipped }))
        else toast.success(t('messages.success'))
        await fetchHypervisorDetail()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        maintaining.value = false
    }
}

onMounted(fetchHypervisorDetail)
</script>

<template>
    <div class="vpc-detail">
        <!-- Header -->
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <!-- Loading State -->
        <div v-if="loading" class="loading-container">
            <div class="loading-spinner"></div>
            <p class="loading-text">{{ t('messages.loading') }}</p>
        </div>

        <!-- Error State -->
        <div v-else-if="error" class="error-container card">
            <Server :size="48" style="opacity: 0.3; margin-bottom: 16px" />
            <p class="text-secondary">{{ error }}</p>
            <button class="btn btn-primary btn-sm" @click="fetchHypervisorDetail" style="margin-top: 12px">
                {{ t('actions.refresh') }}
            </button>
        </div>

        <!-- Detail Content -->
        <div v-else-if="hypervisor" class="detail-content">
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon">
                        <Server :size="20" />
                    </div>
                    <div>
                        <h2 class="resource-title">
                            {{ hypervisor.hostname }}
                            <StatusBadge
                                :variant="getStatusVariant(hypervisor.status)"
                                :label="getStatusLabel(hypervisor.status, hypervisor.status_name)"
                            />
                            <span
                                v-if="hypervisor.status === 2"
                                :class="['drain-hint', hyperInstances.length ? 'drain-hint-warn' : 'drain-hint-done']"
                                >{{ drainHint }}</span
                            >
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ hypervisor.uuid }}</span>
                            <button
                                class="copy-btn"
                                @click="copyToClipboard(hypervisor.uuid, 'uuid')"
                                :title="t('messages.copied')"
                            >
                                <Check v-if="copiedField === 'uuid'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <div class="action-dropdown">
                        <button class="btn btn-secondary btn-sm" @click="toggleActionMenu">
                            {{ t('actions.actions') }} <ChevronDown :size="14" />
                        </button>
                        <Transition name="dropdown">
                            <div v-if="showActionMenu" class="dropdown-menu">
                                <button v-if="hypervisor.status === 1" class="dropdown-item" @click="openMaintainModal">
                                    <Wrench :size="14" /> {{ t('dashboard.hypervisorActions.maintain') }}
                                </button>
                                <button class="dropdown-item" @click="toggleEdit">
                                    <Pencil :size="14" /> {{ t('actions.edit') }}
                                </button>
                                <button
                                    class="dropdown-item"
                                    :disabled="!!consoleDisabledReason(hypervisor.status)"
                                    :title="consoleDisabledReason(hypervisor.status)"
                                    @click="handleHostConsole(hypervisor.uuid)"
                                >
                                    <SquareTerminal :size="14" /> {{ t('dashboard.hypervisorActions.console') }}
                                </button>
                            </div>
                        </Transition>
                        <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
                    </div>
                </div>
            </div>

            <!-- Tabs -->
            <DetailTabs v-model="activeTab" :tabs="tabs" />

            <!-- Info Sections -->
            <div v-if="activeTab === 'overview'">
                <div class="info-grid">
                    <!-- Basic Info Card -->
                    <div class="info-card card">
                        <h3 class="card-section-title">{{ t('dashboard.table.overview') }}</h3>
                        <div class="info-rows">
                            <InfoRow :label="t('dashboard.table.hostname')">{{ hypervisor.hostname }}</InfoRow>
                            <InfoRow :label="t('dashboard.table.hostIp')" mono>{{ hypervisor.host_ip }}</InfoRow>
                            <InfoRow :label="t('dashboard.table.routeIp') || 'Route IP'" mono>{{
                                hypervisor.route_ip || '-'
                            }}</InfoRow>
                            <InfoRow :label="t('dashboard.hypervisorDeploy.virtType')">{{
                                hypervisor.virt_type
                            }}</InfoRow>
                            <InfoRow :label="t('dashboard.table.zone')">{{ hypervisor.zone_name || '-' }}</InfoRow>
                        </div>
                    </div>

                    <!-- Resources Card -->
                    <div class="info-card card">
                        <h3 class="card-section-title">{{ t('dashboard.table.resourcesCapacity') }}</h3>
                        <div class="info-rows">
                            <InfoRow :label="t('dashboard.table.vcpus')">
                                <span>
                                    {{ hypervisor.cpu }} / {{ hypervisor.cpu_total }}
                                    {{ t('specs.cores_plain') || 'cores' }}
                                    <span class="avail-badge">{{ t('dashboard.table.available') }}</span>
                                </span>
                            </InfoRow>
                            <InfoRow :label="t('dashboard.table.memory')">
                                <span>
                                    {{ formatMemory(hypervisor.memory) }} / {{ formatMemory(hypervisor.memory_total) }}
                                    <span class="avail-badge">{{ t('dashboard.table.available') }}</span>
                                </span>
                            </InfoRow>
                            <InfoRow :label="t('dashboard.table.disk')">
                                <span>
                                    {{ formatDisk(hypervisor.disk_allocated) }} /
                                    {{ formatDisk(hypervisor.disk_total) }}
                                    <span class="avail-badge">{{ t('storage.allocatedOfTotal') }}</span>
                                </span>
                            </InfoRow>
                            <InfoRow v-for="p in hypervisor.storage_pools || []" :key="p.uuid" :label="`  · ${p.name}`">
                                <button type="button" class="link-btn" @click="activeTab = 'storage'">
                                    {{ p.allocated_bytes ? formatBytes(p.allocated_bytes) : '0 B' }} /
                                    {{ formatBytes(p.capacity_bytes) }} ({{ t('storage.used') }}
                                    {{ Math.round(p.usage_ratio * 100) }}%) ·
                                    {{
                                        te(`storage.hostPoolStatus.${p.status}`)
                                            ? t(`storage.hostPoolStatus.${p.status}`)
                                            : p.status
                                    }}
                                </button>
                            </InfoRow>
                        </div>
                    </div>
                </div>

                <div v-if="maintainResults.length" class="info-card card" style="margin-bottom: var(--spacing-5)">
                    <h3 class="card-section-title">{{ t('storage.maintainResults') }}</h3>
                    <div v-for="r in maintainResults" :key="r.instance.id" class="maintain-row">
                        <span class="maintain-name">{{ r.instance.name }}</span>
                        <StatusBadge
                            :variant="r.status === 'migrating' ? 'pending' : 'error'"
                            :label="
                                r.status === 'migrating'
                                    ? t('dashboard.instanceStatus.migrating')
                                    : t('dashboard.migrationStatus.not_doing')
                            "
                        />
                        <span v-if="r.reason" class="text-secondary maintain-reason">{{ r.reason }}</span>
                    </div>
                </div>

                <!-- 该节点上的虚拟机 -->
                <div class="info-card card" style="margin-bottom: var(--spacing-5)">
                    <h3 class="card-section-title">
                        {{ t('dashboard.table.instancesOnHyper') }}
                        <span class="hyper-vm-count">{{ hyperInstances.length }}</span>
                    </h3>
                    <div v-if="hyperInstancesLoading" class="text-secondary" style="font-size: 0.875rem">
                        {{ t('messages.loading') }}
                    </div>
                    <div v-else-if="!hyperInstances.length" class="text-secondary" style="font-size: 0.875rem">
                        {{ t('messages.noData') }}
                    </div>
                    <div v-else class="hyper-vm-list">
                        <div class="hyper-vm-head">
                            <span class="hyper-vm-name">{{ t('dashboard.table.hostname') }}</span>
                            <span class="hyper-vm-os">{{ t('dashboard.table.image') }}</span>
                            <span class="hyper-vm-spec">{{ t('dashboard.table.flavor') }}</span>
                            <span class="hyper-vm-time">{{ t('dashboard.table.createdAt') }}</span>
                            <span class="hyper-vm-badge">{{ t('dashboard.table.status') }}</span>
                        </div>
                        <router-link
                            v-for="inst in hyperInstances"
                            :key="inst.id"
                            :to="{ name: 'instance-detail', params: { id: inst.id } }"
                            class="hyper-vm-item"
                        >
                            <span class="hyper-vm-name" :title="inst.hostname">{{ inst.hostname }}</span>
                            <span class="hyper-vm-os" :title="inst.image?.name">{{ inst.image?.name || '-' }}</span>
                            <span class="hyper-vm-spec"
                                >{{ inst.cpu }}C / {{ formatMemory(inst.memory) }} / {{ formatDisk(inst.disk) }}</span
                            >
                            <span class="hyper-vm-time">{{ formatInstanceTime(inst.created_at) }}</span>
                            <StatusBadge
                                class="hyper-vm-badge"
                                :variant="instanceStatusVariant(inst.status)"
                                :label="instanceStatusText(inst.status)"
                            />
                        </router-link>
                    </div>
                </div>

                <!-- Overcommit Card -->
                <div class="info-card card" style="margin-bottom: var(--spacing-5)">
                    <h3 class="card-section-title">{{ t('dashboard.table.overcommit') }}</h3>

                    <div class="info-rows">
                        <InfoRow :label="t('dashboard.table.cpuOverCommit')">{{ hypervisor.cpu_over_rate }}x</InfoRow>
                        <InfoRow :label="t('dashboard.table.memOverCommit')">{{ hypervisor.mem_over_rate }}x</InfoRow>
                        <InfoRow :label="t('dashboard.table.diskOverCommit')">{{ hypervisor.disk_over_rate }}x</InfoRow>
                        <InfoRow v-if="hypervisor.remark" :label="t('dashboard.table.remark')">{{
                            hypervisor.remark
                        }}</InfoRow>
                    </div>
                </div>
            </div>

            <HostStorageTab v-else-if="activeTab === 'storage'" :hypervisor="hypervisor" />

            <!-- Monitoring Content -->
            <div v-else-if="activeTab === 'monitor'" class="monitor-section">
                <HostMonitoringCharts :hostname="hypervisor.hostname" />
            </div>
        </div>

        <!-- Edit Overcommit Modal -->
        <BaseModal
            :show="editMode"
            :title="`${t('actions.edit')} ${t('dashboard.table.overcommit')}`"
            form
            :loading="saving"
            @close="cancelEdit"
            @submit="handleSave"
        >
            <div class="form-grid" style="grid-template-columns: 1fr 1fr; gap: 16px">
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.status') }}</label>
                    <select v-model="form.status" class="form-select">
                        <option :value="0">{{ t('dashboard.hypervisorStatus.disabled') }}</option>
                        <option :value="1">{{ t('dashboard.hypervisorStatus.active') }}</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.zone') }}</label>
                    <select v-model="form.zone_id" class="form-select">
                        <option v-for="z in zoneList" :key="z.id" :value="z.id">{{ z.name }}</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.cpuOverCommit') }}</label>
                    <input type="number" step="0.1" min="1" v-model="form.cpu_over_rate" class="form-select" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.memOverCommit') }}</label>
                    <input type="number" step="0.1" min="1" v-model="form.mem_over_rate" class="form-select" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.diskOverCommit') }}</label>
                    <input type="number" step="0.1" min="1" v-model="form.disk_over_rate" class="form-select" />
                </div>
                <div class="form-group" style="grid-column: span 2">
                    <label class="form-label">{{ t('dashboard.table.remark') }}</label>
                    <input type="text" v-model="form.remark" class="form-select" />
                </div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="cancelEdit" :disabled="saving">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="saving">
                    <Loader2 v-if="saving" :size="14" class="spinning" />
                    {{ saving ? t('messages.loading') : t('actions.save') }}
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
                {{ t('dashboard.hypervisorActions.maintainDesc', { hostname: hypervisor?.hostname }) }}
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
    </div>
</template>

<style scoped>
.maintain-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    padding: 6px 0;
    font-size: var(--font-size-sm);
}

.maintain-name {
    min-width: 160px;
    font-weight: var(--font-weight-medium);
}

.maintain-reason {
    font-size: var(--font-size-xs);
}

.link-btn {
    background: none;
    border: none;
    padding: 0;
    color: var(--primary-color);
    cursor: pointer;
    font-size: inherit;
}

.detail-header {
    margin-bottom: var(--spacing-4);
}

.loading-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 80px 0;
}

.loading-text {
    margin-top: var(--spacing-3);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
}

.error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px 20px;
    text-align: center;
}
.copied-icon {
    color: var(--success-color);
}

/* Action Dropdown */
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
    min-width: 180px;
    background: var(--bg-primary, var(--bg-primary));
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
    padding: 4px 0;
    z-index: var(--z-dropdown);
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

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-hover, var(--gray-100));
}

.dropdown-item:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.dropdown-enter-active {
    transition:
        opacity 0.15s,
        transform 0.15s;
}

.dropdown-leave-active {
    transition:
        opacity 0.1s,
        transform 0.1s;
}

.dropdown-enter-from {
    opacity: 0;
    transform: translateY(-4px);
}

.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-4px);
}

.info-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-5);
}

.info-card {
    padding: var(--spacing-5);
}

/* 维护模式的腾空进度提示：还有虚拟机时是「别断电」的警告，必须显眼 */
.drain-hint {
    margin-left: var(--spacing-2);
    padding: 2px 10px;
    border-radius: 10px;
    font-size: 0.75rem;
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

/* 节点上的虚拟机列表 */
.hyper-vm-count {
    margin-left: 8px;
    padding: 1px 8px;
    border-radius: 10px;
    background: var(--primary-50);
    color: var(--primary-600);
    font-size: 0.75rem;
    font-weight: 600;
}

.hyper-vm-list {
    display: flex;
    flex-direction: column;
}

/* 单行表格式布局。用 grid 而非 flex：flex 的剩余空间会被设了 grow 的列全部吸走，
   导致前几列过宽、后几列挤在一起；grid 按比例给每一列分配，各行也天然对齐 */
.hyper-vm-head,
.hyper-vm-item {
    display: grid;
    grid-template-columns:
        minmax(110px, 1.1fr)
        minmax(150px, 1.5fr)
        minmax(150px, 1.4fr)
        minmax(120px, 1.1fr)
        minmax(64px, auto);
    align-items: center;
    gap: var(--spacing-3);
    padding: 10px 8px;
    white-space: nowrap;
}

.hyper-vm-head {
    font-size: 0.75rem;
    color: var(--text-light, var(--gray-400));
    border-bottom: 1px solid var(--border-default);
    padding-bottom: 6px;
}

.hyper-vm-item {
    border-bottom: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    text-decoration: none;
    color: inherit;
    transition: background var(--transition-fast);
}

.hyper-vm-item:last-child {
    border-bottom: none;
}

.hyper-vm-item:hover {
    background: var(--gray-50, var(--gray-50));
}

/* 每个单元格都要能收缩并省略，否则长内容会把整列撑开、破坏对齐 */
.hyper-vm-head > span,
.hyper-vm-item > span {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
}

.hyper-vm-name {
    font-size: 0.875rem;
    font-weight: 500;
}

.hyper-vm-item .hyper-vm-name {
    color: var(--primary-color);
}

.hyper-vm-os {
    font-size: 0.8125rem;
    color: var(--text-primary);
}

.hyper-vm-spec,
.hyper-vm-time {
    font-size: 0.8125rem;
    color: var(--text-secondary);
}

.hyper-vm-badge {
    justify-self: end;
}

/* 窄屏放弃列对齐，改为自动换行，避免横向滚动；表头失去意义故隐藏 */
@media (max-width: 720px) {
    .hyper-vm-head {
        display: none;
    }

    .hyper-vm-item {
        display: flex;
        flex-wrap: wrap;
        white-space: normal;
        gap: 4px var(--spacing-3);
    }

    .hyper-vm-name {
        flex: 1 0 100%;
    }

    .hyper-vm-badge {
        margin-left: auto;
    }
}

.card-section-title {
    margin: 0 0 var(--spacing-4) 0;
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-semibold);
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding-bottom: var(--spacing-3);
    border-bottom: 1px solid var(--border-light);
}

.info-rows {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.avail-badge {
    font-family: var(--font-family);
    background: rgba(16, 185, 129, 0.1);
    color: var(--success-color);
    padding: 1px 6px;
    border-radius: 4px;
    font-size: 0.65rem;
    font-weight: 600;
    margin-left: 6px;
    vertical-align: middle;
}

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
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: 4px;
}

.form-select {
    width: 100%;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    padding: 8px;
    font-size: 0.875rem;
    background: var(--bg-primary);
    color: var(--text-primary);
}

.form-select:focus {
    outline: none;
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

/* Modal */
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

.monitor-section {
    animation: fadeIn 0.3s ease-out;
}

@keyframes fadeIn {
    from {
        opacity: 0;
        transform: translateY(10px);
    }
    to {
        opacity: 1;
        transform: translateY(0);
    }
}

@media (max-width: 768px) {
    .info-grid,
    .form-grid {
        grid-template-columns: 1fr;
    }
}
</style>
