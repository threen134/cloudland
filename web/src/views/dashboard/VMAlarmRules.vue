<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, Search, ShieldAlert, Link, RefreshCw, Monitor, Power, Check, Copy } from 'lucide-vue-next'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import {
    vmAlarmRulesApi,
    VM_RULE_TYPES,
    type VMAlarmRuleGroup,
    type VMRuleType,
    type CPURuleDetail,
    type MemoryRuleDetail,
    type BWRuleDetail,
    type CreateVMAlarmRuleResponse,
    type CreateBWRuleResponse,
} from '../../api/vmAlarmRules'
import { alarmEventsApi } from '../../api/alarmEvents'
import { notificationsApi, type NotificationChannel } from '../../api/notifications'
import { instancesApi, type Instance, type InstanceListResponse } from '../../api/instances'
import { errorMessage } from '../../utils/error'
import { useRegionStore } from '../../stores/region'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'

const { t } = useI18n()
const toast = useToast()
const regionStore = useRegionStore()
const rules = ref<VMAlarmRuleGroup[]>([])
const loading = ref(false)
const errorMsg = ref('')
const searchQuery = ref('')
const page = ref(1)
const pageSize = ref(20)

const { copiedId, copyId } = useCopyId()

/**
 * 创建表单里的一行阈值。三种规则类型共用同一张表单：CPU / 内存用 rule（比较符），
 * 带宽用 direction，其余字段相同，所以这里是并集而不是 VMAlarmRuleDetail 那个判别联合。
 */
interface RuleFormRow {
    name: string
    limit: number
    duration: number
    level: string
    rule?: string
    direction?: 'in' | 'out'
}

// Create modal
const showCreateModal = ref(false)
const createForm = ref({
    name: '',
    type: 'cpu' as VMRuleType,
    rules: [{ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' }] as RuleFormRow[],
    linkedvms: [] as string[],
    bwLinkedVMs: [] as { instance_id: string; target_device: string }[],
    linkedchannels: [] as string[],
})
const createChannelsLoading = ref(false)

// Delete modal
const showDeleteModal = ref(false)
const deleteTarget = ref<VMAlarmRuleGroup | null>(null)

// Bind channels modal
const showBindModal = ref(false)
const bindTarget = ref<VMAlarmRuleGroup | null>(null)
const allChannels = ref<NotificationChannel[]>([])
const selectedChannelUuids = ref<string[]>([])
const bindLoading = ref(false)

// VM Binding
const showBindVMsModal = ref(false)
const bindVMsTarget = ref<VMAlarmRuleGroup | null>(null)
const allVMs = ref<Instance[]>([])
const selectedVMUuids = ref<string[]>([])
const vmsLoading = ref(false)
const linkVMsLoading = ref(false)
const vmSearchQuery = ref('')

// BW NIC selection
const vmInterfaces = ref<Record<string, { id: string; name: string; ip_address?: string }[]>>({})
const vmInterfacesLoading = ref<Record<string, boolean>>({})
const expandedBWVMs = ref<string[]>([])

// 展开某一行时才去拉它绑定的通知渠道（展开态由 DataTable 维护）
const ruleChannels = ref<Record<string, NotificationChannel[]>>({})
const ruleChannelCounts = ref<Record<string, number>>({})

const onExpandRule = async (id: string) => {
    if (id in ruleChannels.value) return
    try {
        const [channelsRes, bindingsRes] = await Promise.all([
            notificationsApi.list(),
            alarmEventsApi.getRuleChannels(id),
        ])
        const allCh: NotificationChannel[] = channelsRes.channels || []
        const boundUuids: string[] = (bindingsRes.bindings || []).map((b) => b.channel_uuid)
        ruleChannels.value[id] = allCh.filter((c) => boundUuids.includes(c.uuid))
        ruleChannelCounts.value[id] = ruleChannels.value[id].length
    } catch (err) {
        console.error('Failed to load rule channels:', err)
        ruleChannels.value[id] = []
        ruleChannelCounts.value[id] = 0
    }
}
const isNameValid = computed(() => /^[a-zA-Z][a-zA-Z0-9_]*$/.test(createForm.value.name))
const nameError = computed(() => {
    if (!createForm.value.name) return ''
    if (!/^[a-zA-Z]/.test(createForm.value.name)) return t('dashboard.vmAlarmRules.nameStartLetterError')
    if (!/^[a-zA-Z0-9_]*$/.test(createForm.value.name)) return t('dashboard.vmAlarmRules.nameCharsetError')
    return ''
})

const getVMName = (id: string) => {
    const vm = allVMs.value.find((v) => v.id === id)
    if (!vm) return id.substring(0, 8)
    return vm.hostname || vm.name || id.substring(0, 8)
}

const getLinkedVMId = (entry: string | { instance_id: string; target_device: string }) =>
    typeof entry === 'string' ? entry : entry.instance_id

const filteredRules = computed(() => {
    if (!searchQuery.value) return rules.value
    const q = searchQuery.value.toLowerCase()
    return rules.value.filter((r) => {
        if (r.name.toLowerCase().includes(q) || r.uuid.toLowerCase().includes(q)) return true
        if (r.linkedvms) {
            return r.linkedvms.some((entry) => getVMName(getLinkedVMId(entry)).toLowerCase().includes(q))
        }
        return false
    })
})

const total = computed(() => filteredRules.value.length)
const pagedRules = computed(() =>
    filteredRules.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value)
)
// 搜索或每页条数变了，当前页可能已经越界
watch([searchQuery, pageSize], () => {
    page.value = 1
})

const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId') },
    { key: 'type', label: t('dashboard.table.type') },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'thresholds', label: t('dashboard.vmAlarmRules.thresholds'), align: 'center' },
    { key: 'vms', label: t('dashboard.vmAlarmRules.linkedVMs'), align: 'center' },
    { key: 'channels', label: t('dashboard.vmAlarmRules.linkedChannels'), align: 'center' },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const levelBadgeClass = (level: string) =>
    'badge-' + (level === 'critical' ? 'error' : level === 'warning' ? 'warning' : 'primary')

const fetchAllVMs = async () => {
    try {
        const res = await instancesApi.fetchInstances()
        const data = res as InstanceListResponse | Instance[]
        allVMs.value = Array.isArray(data) ? data : data.instances || []
    } catch (err) {
        console.error('Failed to fetch instances:', err)
    }
}

/**
 * 取某一类规则组的全部页。
 * CPU / 内存 / 带宽是三个独立的接口，没有合并接口，所以这一页做不到服务端分页——
 * 原先不传分页参数、又把 totalPages 写死成 1，结果每类只显示后端默认的前 20 条，
 * 多出来的规则组在界面上直接消失且没有任何提示。现在按页拉全，再在前端分页和搜索
 * （搜索覆盖的是全集，不是当前页）。规则组是每组织几十条的量级，拉全可以接受
 */
const PAGE_SIZE = 100
const MAX_PAGES = 20
const fetchAllOfType = async (type: VMRuleType): Promise<VMAlarmRuleGroup[]> => {
    const collected: VMAlarmRuleGroup[] = []
    for (let p = 1; p <= MAX_PAGES; p++) {
        const res = await vmAlarmRulesApi.listRules(type, { page: p, page_size: PAGE_SIZE })
        collected.push(...(res.data || []).map((r: VMAlarmRuleGroup) => ({ ...r, type })))
        const totalPages = res.meta?.total_pages ?? 1
        if (p >= totalPages || (res.data || []).length === 0) break
    }
    return collected
}

const fetchRules = async () => {
    loading.value = true
    errorMsg.value = ''
    try {
        const types: VMRuleType[] = ['cpu', 'memory', 'bw']
        const [, ...ruleResults] = await Promise.allSettled([
            allVMs.value.length === 0 ? fetchAllVMs() : Promise.resolve(),
            ...types.map((type) => fetchAllOfType(type)),
        ])

        const allRules: VMAlarmRuleGroup[] = []
        ruleResults.forEach((result, idx) => {
            if (result.status === 'fulfilled') {
                allRules.push(...(result.value as VMAlarmRuleGroup[]))
            } else {
                console.error(`Failed to fetch ${types[idx]} alarm rules:`, result.reason)
            }
        })

        rules.value = allRules

        // Pre-fetch channel counts for all rules in background
        Promise.allSettled(allRules.map((r) => alarmEventsApi.getRuleChannels(r.uuid))).then((results) => {
            results.forEach((result, idx) => {
                const uuid = allRules[idx].uuid
                if (result.status === 'fulfilled') {
                    const bindings = result.value.bindings || []
                    ruleChannelCounts.value[uuid] = bindings.length
                } else {
                    ruleChannelCounts.value[uuid] = 0
                }
            })
        })
    } catch (err) {
        console.error('Failed to fetch VM alarm rules:', err)
        errorMsg.value = t('messages.error')
    } finally {
        loading.value = false
    }
}

const openCreate = async () => {
    createForm.value = {
        name: '',
        type: 'cpu',
        rules: [{ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' }],
        linkedvms: [],
        bwLinkedVMs: [],
        linkedchannels: [],
    }
    showCreateModal.value = true
    if (allVMs.value.length === 0) {
        vmsLoading.value = true
        try {
            const res = await instancesApi.fetchInstances()
            const data = res as InstanceListResponse | Instance[]
            allVMs.value = Array.isArray(data) ? data : data.instances || []
        } catch (err) {
            console.error('Failed to fetch instances:', err)
        } finally {
            vmsLoading.value = false
        }
    }
    if (allChannels.value.length === 0) {
        createChannelsLoading.value = true
        try {
            const res = await notificationsApi.list()
            allChannels.value = res.channels || []
        } catch (err) {
            console.error('Failed to fetch channels:', err)
        } finally {
            createChannelsLoading.value = false
        }
    }
}

const onTypeChange = () => {
    if (createForm.value.type === 'bw') {
        createForm.value.rules = [{ direction: 'in', name: '', limit: 80, duration: 5, level: 'warning' }]
        createForm.value.bwLinkedVMs = []
    } else {
        createForm.value.rules = [{ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' }]
    }
}

const isBWVMChecked = (vmId: string) => createForm.value.bwLinkedVMs.some((v) => v.instance_id === vmId)

const isBWNICChecked = (vmId: string, nicName: string) =>
    createForm.value.bwLinkedVMs.some((v) => v.instance_id === vmId && v.target_device === nicName)

const toggleBWVM = async (vmId: string) => {
    const expanded = expandedBWVMs.value.includes(vmId)
    if (expanded) {
        // 收起：移除展开状态及该 VM 所有已选网卡
        expandedBWVMs.value = expandedBWVMs.value.filter((id) => id !== vmId)
        createForm.value.bwLinkedVMs = createForm.value.bwLinkedVMs.filter((v) => v.instance_id !== vmId)
        return
    }
    // 展开：拉取网卡列表（缓存），不自动选中任何网卡
    expandedBWVMs.value.push(vmId)
    if (!vmInterfaces.value[vmId]) {
        vmInterfacesLoading.value[vmId] = true
        try {
            const res = await instancesApi.getInterfaces(vmId)
            vmInterfaces.value[vmId] = (res.interfaces || []).map((i) => ({
                id: i.id,
                name: i.name || '',
                ip_address: i.ip_address,
            }))
        } catch {
            vmInterfaces.value[vmId] = []
        } finally {
            vmInterfacesLoading.value[vmId] = false
        }
    }
}

const toggleBWNIC = (vmId: string, nicName: string) => {
    const idx = createForm.value.bwLinkedVMs.findIndex((v) => v.instance_id === vmId && v.target_device === nicName)
    if (idx >= 0) {
        createForm.value.bwLinkedVMs.splice(idx, 1)
    } else {
        createForm.value.bwLinkedVMs.push({ instance_id: vmId, target_device: nicName })
    }
}

const submitCreate = async () => {
    try {
        if (!isNameValid.value) return
        const regionUuid = regionStore.currentRegionId || ''

        const base = {
            name: createForm.value.name,
            region_id: regionUuid,
        }

        // 每条阈值的 name 既不显示也不参与生成 Prometheus 规则（组名、alert 名、rule_id
        // 都由「类型_owner_组UUID_序号」推导），所以不让用户填，按组名加序号补一个，
        // 只为让 clapi 的日志和数据库行可读——原先一直是空串
        createForm.value.rules.forEach((row, i) => {
            row.name = `${createForm.value.name}_${i}`
        })

        // 表单行是三种规则类型的并集，提交时按当前类型收窄（见 RuleFormRow）
        let res: CreateVMAlarmRuleResponse | CreateBWRuleResponse | undefined
        if (createForm.value.type === 'cpu') {
            res = await vmAlarmRulesApi.createCPURule({ ...base, rules: createForm.value.rules as CPURuleDetail[] })
        } else if (createForm.value.type === 'memory') {
            res = await vmAlarmRulesApi.createMemoryRule({
                ...base,
                rules: createForm.value.rules as MemoryRuleDetail[],
            })
        } else if (createForm.value.type === 'bw') {
            res = await vmAlarmRulesApi.createBWRule({
                ...base,
                enable: true,
                rules: createForm.value.rules as BWRuleDetail[],
                linkedvms: createForm.value.bwLinkedVMs.length > 0 ? createForm.value.bwLinkedVMs : undefined,
            })
        }

        // 带宽接口返回 uuid，CPU / 内存接口返回 group_uuid
        const resData = res?.data
        const ruleUuid = resData && ('uuid' in resData ? resData.uuid : resData.group_uuid)

        // Link VMs if selected (cpu/memory only; bw links are handled in createBWRule)
        if (
            createForm.value.type !== 'bw' &&
            createForm.value.linkedvms &&
            createForm.value.linkedvms.length > 0 &&
            ruleUuid
        ) {
            try {
                await vmAlarmRulesApi.linkRule(
                    ruleUuid,
                    createForm.value.linkedvms.map((id) => ({ vm_uuid: id }))
                )
            } catch (err) {
                console.error('Failed to link VMs after creation:', err)
                toast.warning(t('dashboard.vmAlarmRules.ruleCreatedButLinkFailed'))
            }
        }

        // Bind channels if selected
        if (createForm.value.linkedchannels.length > 0 && ruleUuid) {
            try {
                await alarmEventsApi.bindRuleChannels(ruleUuid, createForm.value.linkedchannels)
            } catch (err) {
                console.error('Failed to bind channels after creation:', err)
            }
        }

        showCreateModal.value = false
        await fetchRules()
    } catch (err) {
        console.error('Failed to create rule:', err)
        errorMsg.value = errorMessage(err, t('messages.error'))
    }
}

const confirmDelete = (rule: VMAlarmRuleGroup) => {
    deleteTarget.value = rule
    showDeleteModal.value = true
}

const toggleRuleStatus = async (rule: VMAlarmRuleGroup) => {
    try {
        if (rule.enable) {
            await vmAlarmRulesApi.disableRule(rule.uuid)
            rule.enable = false
            toast.success(t('messages.disabledSuccess'))
        } else {
            await vmAlarmRulesApi.enableRule(rule.uuid)
            rule.enable = true
            toast.success(t('messages.enabledSuccess'))
        }
    } catch (err) {
        toast.error(errorMessage(err, t('messages.operationFailed')))
    }
}

const executeDelete = async () => {
    if (!deleteTarget.value || !deleteTarget.value.type) return
    try {
        await vmAlarmRulesApi.deleteRule(deleteTarget.value.type, deleteTarget.value.uuid)
        showDeleteModal.value = false
        deleteTarget.value = null
        await fetchRules()
    } catch (err) {
        console.error('Failed to delete rule:', err)
        errorMsg.value = errorMessage(err, t('messages.error'))
    }
}

// --- Channel binding ---
const openBindChannels = async (rule: VMAlarmRuleGroup) => {
    bindTarget.value = rule
    bindLoading.value = true
    showBindModal.value = true
    try {
        const [channelsRes, bindingsRes] = await Promise.all([
            notificationsApi.list(),
            alarmEventsApi.getRuleChannels(rule.uuid),
        ])
        allChannels.value = channelsRes.channels || []
        const bindings = bindingsRes.bindings || []
        selectedChannelUuids.value = bindings.map((b) => b.channel_uuid)
    } catch (err) {
        console.error('Failed to load channels:', err)
        allChannels.value = []
        selectedChannelUuids.value = []
    } finally {
        bindLoading.value = false
    }
}

const saveChannelBindings = async () => {
    if (!bindTarget.value) return
    try {
        await alarmEventsApi.bindRuleChannels(bindTarget.value.uuid, selectedChannelUuids.value)
        const uuid = bindTarget.value.uuid
        const bound = allChannels.value.filter((c) => selectedChannelUuids.value.includes(c.uuid))
        ruleChannels.value[uuid] = bound
        ruleChannelCounts.value[uuid] = bound.length
        showBindModal.value = false
        toast.success(t('messages.success'))
    } catch (err) {
        const errCode = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        if (errCode === 'channel_not_synced') {
            toast.error(t('dashboard.vmAlarmRules.channelNotSynced'))
        } else {
            console.error('Failed to bind channels:', err)
        }
    }
}

const toggleChannel = (uuid: string) => {
    const idx = selectedChannelUuids.value.indexOf(uuid)
    if (idx >= 0) {
        selectedChannelUuids.value.splice(idx, 1)
    } else {
        selectedChannelUuids.value.push(uuid)
    }
}

// --- VM Binding ---
const openBindVMs = async (rule: VMAlarmRuleGroup) => {
    bindVMsTarget.value = rule
    selectedVMUuids.value = rule.linkedvms ? rule.linkedvms.map((e) => getLinkedVMId(e)) : []
    showBindVMsModal.value = true
    vmsLoading.value = true
    vmSearchQuery.value = ''
    try {
        const res = await instancesApi.fetchInstances()
        const data = res as InstanceListResponse | Instance[]
        allVMs.value = Array.isArray(data) ? data : data.instances || []
    } catch (err) {
        console.error('Failed to fetch instances:', err)
        allVMs.value = []
    } finally {
        vmsLoading.value = false
    }
}

const toggleVMSelection = (uuid: string) => {
    const idx = selectedVMUuids.value.indexOf(uuid)
    if (idx > -1) selectedVMUuids.value.splice(idx, 1)
    else selectedVMUuids.value.push(uuid)
}

const filteredVMs = computed(() => {
    if (!vmSearchQuery.value) return allVMs.value
    const q = vmSearchQuery.value.toLowerCase()
    return allVMs.value.filter(
        (vm) => (vm.hostname || vm.name || '').toLowerCase().includes(q) || vm.id.toLowerCase().includes(q)
    )
})

const saveVMBindings = async () => {
    if (!bindVMsTarget.value) return
    linkVMsLoading.value = true
    try {
        const groupUuid = bindVMsTarget.value.uuid
        const currentVMIds = (bindVMsTarget.value.linkedvms || []).map((e) => getLinkedVMId(e))

        const toLink = selectedVMUuids.value.filter((id) => !currentVMIds.includes(id))
        const toUnlink = currentVMIds.filter((id) => !selectedVMUuids.value.includes(id))

        if (toLink.length > 0) {
            await vmAlarmRulesApi.linkRule(
                groupUuid,
                toLink.map((id) => ({ vm_uuid: id }))
            )
        }
        if (toUnlink.length > 0) {
            await vmAlarmRulesApi.unlinkRule(
                groupUuid,
                toUnlink.map((id) => ({ vm_uuid: id }))
            )
        }

        toast.success(t('dashboard.vmAlarmRules.bindSuccess'))
        showBindVMsModal.value = false
        await fetchRules()
    } catch (err) {
        console.error('Failed to save VM bindings:', err)
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        linkVMsLoading.value = false
    }
}

const addRuleRow = () => {
    if (createForm.value.type === 'bw') {
        createForm.value.rules.push({ direction: 'out', name: '', limit: 80, duration: 5, level: 'warning' })
    } else {
        createForm.value.rules.push({ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' })
    }
}

const removeRuleRow = (index: number) => {
    if (createForm.value.rules.length > 1) {
        createForm.value.rules.splice(index, 1)
    }
}

onMounted(fetchRules)
</script>

<template>
    <div class="vpc-list-container">
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchRules" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreate">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </template>
        </PageToolbar>

        <div v-if="errorMsg" class="error-banner" @click="errorMsg = ''">{{ errorMsg }}</div>

        <DataTable
            :columns="columns"
            :rows="pagedRules"
            row-key="uuid"
            :loading="loading"
            expandable
            @expand="onExpandRule"
        >
            <template #empty>
                <ShieldAlert :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                <p>{{ searchQuery ? t('messages.noResults') : t('messages.noData') }}</p>
            </template>

            <template #cell-name="{ row: rule }">
                <div class="rule-name-text">{{ rule.name }}</div>
                <div class="resource-id-row">
                    <span class="rule-id monospace" :title="rule.uuid">{{ rule.uuid.slice(0, 8) }}...</span>
                    <button
                        class="copy-btn-mini"
                        @click.stop.prevent="copyId(rule.uuid)"
                        :title="t('actions.copy')"
                        :aria-label="t('actions.copy')"
                    >
                        <Check v-if="copiedId === rule.uuid" :size="10" style="color: var(--success-color)" />
                        <Copy v-else :size="10" />
                    </button>
                </div>
            </template>

            <template #cell-type="{ row: rule }">
                <span class="badge badge-secondary" style="text-transform: uppercase">
                    {{ rule.type ? t('dashboard.vmAlarmRules.ruleTypes.' + rule.type) : '-' }}
                </span>
            </template>

            <template #cell-status="{ row: rule }">
                <StatusBadge
                    :status="rule.enable ? 'enabled' : 'disabled'"
                    :label="rule.enable ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled')"
                />
            </template>

            <template #cell-thresholds="{ row: rule }">{{ rule.rules?.length || 0 }}</template>
            <template #cell-vms="{ row: rule }">{{ rule.linkedvms?.length || 0 }}</template>
            <template #cell-channels="{ row: rule }">{{ ruleChannelCounts[rule.uuid] ?? '…' }}</template>

            <template #cell-actions="{ row: rule }">
                <div class="actions-cell">
                    <button
                        class="icon-btn-table"
                        @click.stop="openBindVMs(rule)"
                        :title="t('dashboard.vmAlarmRules.bindVMs')"
                    >
                        <Monitor :size="16" />
                    </button>
                    <button
                        class="icon-btn-table"
                        @click.stop="openBindChannels(rule)"
                        :title="t('dashboard.vmAlarmRules.bindChannels')"
                    >
                        <Link :size="16" />
                    </button>
                    <button
                        class="icon-btn-table"
                        :class="rule.enable ? 'text-success' : 'text-secondary'"
                        @click.stop="toggleRuleStatus(rule)"
                        :title="rule.enable ? t('actions.disable') : t('actions.enable')"
                    >
                        <Power :size="14" />
                    </button>
                    <button class="icon-btn-table" @click.stop="confirmDelete(rule)" :title="t('actions.delete')">
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>

            <template #expanded="{ row: rule }">
                <div class="rule-body-content">
                    <div v-if="rule.rules && rule.rules.length > 0" class="thresholds-section">
                        <div class="section-label">{{ t('dashboard.vmAlarmRules.thresholds') }}</div>
                        <div class="inner-table-wrapper">
                            <table class="inner-table">
                                <thead>
                                    <tr>
                                        <th v-if="rule.type === 'bw'">{{ t('dashboard.table.direction') }}</th>
                                        <th>{{ t('dashboard.vmAlarmRules.ruleLevel') }}</th>
                                        <th>{{ t('dashboard.vmAlarmRules.thresholdLimit') }}</th>
                                        <th>{{ t('dashboard.vmAlarmRules.durationMin') }}</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    <tr v-for="(r, idx) in rule.rules" :key="idx">
                                        <td v-if="rule.type === 'bw'">
                                            <!-- rules 是 CPU / 内存 / 带宽三种明细的联合类型，只有带宽有 direction -->
                                            {{
                                                'direction' in r
                                                    ? t('dashboard.vmAlarmRules.directions.' + r.direction)
                                                    : '-'
                                            }}
                                        </td>
                                        <td>
                                            <span class="badge" :class="levelBadgeClass(r.level)">
                                                {{ t('dashboard.vmAlarmRules.levels.' + r.level) }}
                                            </span>
                                        </td>
                                        <td>{{ r.limit }}{{ rule.type === 'bw' ? ' ' + t('specs.mbps') : '%' }}</td>
                                        <td>{{ r.duration }}</td>
                                    </tr>
                                </tbody>
                            </table>
                        </div>
                    </div>

                    <div class="vms-section">
                        <div class="section-label">
                            {{ t('dashboard.vmAlarmRules.linkedVMs') }} ({{ rule.linkedvms?.length || 0 }})
                        </div>
                        <div class="linked-vms-chips">
                            <div
                                v-for="entry in rule.linkedvms"
                                :key="getLinkedVMId(entry)"
                                class="vm-chip"
                                :title="getLinkedVMId(entry)"
                            >
                                {{ getVMName(getLinkedVMId(entry)) }}
                                <span class="badge badge-secondary" style="margin-left: 4px; font-size: 10px">{{
                                    getLinkedVMId(entry).substring(0, 8)
                                }}</span>
                            </div>
                            <div
                                v-if="!rule.linkedvms || rule.linkedvms.length === 0"
                                class="text-secondary"
                                style="font-size: 12px"
                            >
                                {{ t('dashboard.vmAlarmRules.noLinkedVMs') }}
                            </div>
                        </div>
                    </div>

                    <div class="vms-section">
                        <div class="section-label">
                            {{ t('dashboard.vmAlarmRules.linkedChannels') }} ({{
                                (ruleChannels[rule.uuid] || []).length
                            }})
                        </div>
                        <div class="linked-vms-chips">
                            <div v-for="ch in ruleChannels[rule.uuid]" :key="ch.uuid" class="vm-chip" :title="ch.uuid">
                                {{ ch.name }}
                                <span
                                    class="badge badge-secondary"
                                    style="margin-left: 4px; font-size: 10px; text-transform: uppercase"
                                    >{{ ch.type }}</span
                                >
                            </div>
                            <div
                                v-if="!ruleChannels[rule.uuid] || ruleChannels[rule.uuid].length === 0"
                                class="text-secondary"
                                style="font-size: 12px"
                            >
                                {{ t('dashboard.vmAlarmRules.noLinkedChannels') }}
                            </div>
                        </div>
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

        <!-- Create Modal -->
        <BaseModal
            :show="showCreateModal"
            :title="
                t('dashboard.vmAlarmRules.createTitle', {
                    type: t('dashboard.vmAlarmRules.ruleTypes.' + createForm.type),
                })
            "
            size="lg"
            form
            @close="showCreateModal = false"
            @submit="submitCreate"
        >
            <div class="form-stack">
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.type') }}</label>
                    <select v-model="createForm.type" class="form-input" @change="onTypeChange">
                        <option v-for="rt in VM_RULE_TYPES" :key="rt.value" :value="rt.value">
                            {{ t('dashboard.vmAlarmRules.ruleTypes.' + rt.value) }}
                        </option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.name') }}</label>
                    <input
                        v-model="createForm.name"
                        class="form-input"
                        :class="{ 'input-error': nameError }"
                        required
                    />
                    <div v-if="nameError" class="input-tip input-tip-error">{{ nameError }}</div>
                </div>
                <div class="rules-section">
                    <div class="rules-header">
                        <div class="rules-header-left">
                            <label class="form-label mb-0">{{ t('dashboard.vmAlarmRules.thresholds') }}</label>
                            <span class="input-tip ml-2">{{ t('dashboard.vmAlarmRules.thresholdHint') }}</span>
                        </div>
                        <button type="button" class="btn btn-ghost btn-sm" @click="addRuleRow">
                            <Plus :size="14" /> {{ t('actions.add') }}
                        </button>
                    </div>
                    <div class="rule-labels-row">
                        <span v-if="createForm.type === 'bw'" class="rule-label-item" style="width: 120px">{{
                            t('dashboard.table.direction')
                        }}</span>
                        <span class="rule-label-item" style="width: 120px">{{
                            t('dashboard.vmAlarmRules.thresholdLimit')
                        }}</span>
                        <span class="rule-label-item" style="width: 120px">{{
                            t('dashboard.vmAlarmRules.durationMin')
                        }}</span>
                        <span class="rule-label-item" style="width: 120px">{{
                            t('dashboard.vmAlarmRules.ruleLevel')
                        }}</span>
                        <span class="rule-label-item" style="width: 32px"></span>
                    </div>
                    <div v-for="(rule, idx) in createForm.rules" :key="idx" class="rule-row">
                        <select
                            v-if="createForm.type === 'bw'"
                            v-model="rule.direction"
                            class="form-input rule-input-sm"
                        >
                            <option value="in">{{ t('dashboard.vmAlarmRules.directions.in') }}</option>
                            <option value="out">{{ t('dashboard.vmAlarmRules.directions.out') }}</option>
                        </select>
                        <input
                            v-model.number="rule.limit"
                            type="number"
                            class="form-input rule-input-sm"
                            :placeholder="t('dashboard.forms.placeholder.limitPercentExample')"
                            min="1"
                            max="100"
                        />
                        <input
                            v-model.number="rule.duration"
                            type="number"
                            class="form-input rule-input-sm"
                            :placeholder="t('dashboard.vmAlarmRules.durationMin')"
                            min="1"
                        />
                        <select v-model="rule.level" class="form-input rule-input-sm">
                            <option value="critical">{{ t('dashboard.vmAlarmRules.levels.critical') }}</option>
                            <option value="warning">{{ t('dashboard.vmAlarmRules.levels.warning') }}</option>
                            <option value="info">{{ t('dashboard.vmAlarmRules.levels.info') }}</option>
                        </select>
                        <button
                            v-if="createForm.rules.length > 1"
                            type="button"
                            class="btn btn-ghost btn-icon icon-danger"
                            @click="removeRuleRow(idx)"
                        >
                            <Trash2 :size="14" />
                        </button>
                    </div>
                </div>
                <div class="form-group mt-2">
                    <label class="form-label"
                        >{{ t('dashboard.vmAlarmRules.bindVMs') }} ({{ t('dashboard.forms.optional') }})</label
                    >
                    <div class="vm-create-selection">
                        <div v-if="vmsLoading" class="loading-spinner small"></div>
                        <!-- BW 类型：每个 VM 展开选择网卡 -->
                        <div
                            v-else-if="createForm.type === 'bw'"
                            class="channel-list"
                            style="
                                max-height: 260px;
                                border: 1px solid var(--border-light);
                                border-radius: 6px;
                                padding: 4px;
                            "
                        >
                            <div v-for="vm in allVMs" :key="vm.id">
                                <!-- VM 行 -->
                                <div class="channel-item" style="gap: 8px">
                                    <input
                                        type="checkbox"
                                        :checked="expandedBWVMs.includes(vm.id)"
                                        @change="toggleBWVM(vm.id)"
                                    />
                                    <span class="channel-name" style="flex: 1">{{ vm.hostname || vm.name }}</span>
                                    <span
                                        v-if="isBWVMChecked(vm.id)"
                                        class="badge badge-primary"
                                        style="font-size: 10px"
                                        >{{
                                            createForm.bwLinkedVMs.filter((v) => v.instance_id === vm.id).length
                                        }}
                                        NIC</span
                                    >
                                    <span
                                        v-if="vmInterfacesLoading[vm.id]"
                                        class="loading-spinner"
                                        style="width: 12px; height: 12px"
                                    ></span>
                                </div>
                                <!-- 网卡子列表 -->
                                <div
                                    v-if="expandedBWVMs.includes(vm.id) && vmInterfaces[vm.id]"
                                    style="padding-left: 24px"
                                >
                                    <label
                                        v-for="nic in vmInterfaces[vm.id]"
                                        :key="nic.name"
                                        class="channel-item"
                                        style="gap: 8px; font-size: 12px"
                                    >
                                        <input
                                            type="checkbox"
                                            :checked="isBWNICChecked(vm.id, nic.name)"
                                            @change="toggleBWNIC(vm.id, nic.name)"
                                        />
                                        <span class="channel-name">{{ nic.name }}</span>
                                        <span v-if="nic.ip_address" class="badge badge-secondary">{{
                                            nic.ip_address
                                        }}</span>
                                    </label>
                                </div>
                            </div>
                            <div v-if="allVMs.length === 0" class="text-muted p-2">{{ t('messages.noNics') }}</div>
                        </div>
                        <!-- CPU / Memory 类型 -->
                        <div
                            v-else
                            class="channel-list"
                            style="
                                max-height: 160px;
                                border: 1px solid var(--border-light);
                                border-radius: 6px;
                                padding: 4px;
                            "
                        >
                            <label v-for="vm in allVMs" :key="vm.id" class="channel-item">
                                <input type="checkbox" :value="vm.id" v-model="createForm.linkedvms" />
                                <span class="channel-name">{{ vm.hostname || vm.name }}</span>
                                <span class="badge badge-secondary">{{ vm.id.substring(0, 8) }}</span>
                            </label>
                            <div v-if="allVMs.length === 0" class="text-muted p-2">{{ t('messages.noNics') }}</div>
                        </div>
                    </div>
                </div>
                <div class="form-group mt-2">
                    <label class="form-label"
                        >{{ t('dashboard.vmAlarmRules.linkedChannels') }} ({{ t('dashboard.forms.optional') }})</label
                    >
                    <div class="vm-create-selection">
                        <div v-if="createChannelsLoading" class="loading-spinner small"></div>
                        <div
                            v-else
                            class="channel-list"
                            style="
                                max-height: 160px;
                                border: 1px solid var(--border-light);
                                border-radius: 6px;
                                padding: 4px;
                            "
                        >
                            <label v-for="ch in allChannels" :key="ch.uuid" class="channel-item">
                                <input type="checkbox" :value="ch.uuid" v-model="createForm.linkedchannels" />
                                <span class="channel-name">{{ ch.name }}</span>
                                <span class="badge badge-secondary" style="text-transform: uppercase">{{
                                    ch.type
                                }}</span>
                            </label>
                            <div v-if="allChannels.length === 0" class="text-muted p-2">
                                {{ t('dashboard.vmAlarmRules.noLinkedChannels') }}
                            </div>
                        </div>
                    </div>
                </div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showCreateModal = false">
                    {{ t('actions.cancel') }}
                </button>
                <!-- 禁用条件必须与 submitCreate 的守卫一致：原先只判断非空，名字含连字符这类
                     不合规则时按钮仍可点，而 submitCreate 直接 return，点下去毫无反应 -->
                <button type="submit" class="btn btn-primary" :disabled="!isNameValid">
                    {{ t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <!-- Delete Modal -->
        <DeleteModal
            :show="showDeleteModal"
            :title="t('actions.confirmDelete')"
            :message="t('dashboard.vmAlarmRules.deleteConfirm', { name: deleteTarget?.name })"
            @close="showDeleteModal = false"
            @confirm="executeDelete"
        />

        <!-- Bind Channels Modal -->
        <BaseModal
            :show="showBindModal"
            :title="`${t('dashboard.vmAlarmRules.bindChannels')} - ${bindTarget?.name ?? ''}`"
            @close="showBindModal = false"
        >
            <div v-if="bindLoading" class="loading-spinner" style="margin: 20px auto"></div>
            <div v-else-if="allChannels.length === 0" class="text-muted">
                {{ t('dashboard.vmAlarmRules.noChannels') }}
            </div>
            <div v-else class="channel-list">
                <label v-for="ch in allChannels" :key="ch.uuid" class="channel-item">
                    <input
                        type="checkbox"
                        :checked="selectedChannelUuids.includes(ch.uuid)"
                        @change="toggleChannel(ch.uuid)"
                    />
                    <span class="channel-name">{{ ch.name }}</span>
                    <span class="badge" :class="ch.type === 'feishu' ? 'badge-info' : 'badge-secondary'">
                        {{
                            ch.type === 'feishu'
                                ? t('dashboard.notificationFeishu')
                                : ch.type === 'webhook'
                                  ? t('dashboard.notificationCustomWebhook')
                                  : ch.type
                        }}
                    </span>
                </label>
            </div>

            <template #footer>
                <button class="btn btn-secondary" @click="showBindModal = false">{{ t('actions.cancel') }}</button>
                <button class="btn btn-primary" @click="saveChannelBindings" :disabled="bindLoading">
                    {{ t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <!-- Bind VMs Modal -->
        <BaseModal
            :show="showBindVMsModal"
            :title="t('dashboard.vmAlarmRules.bindVMs')"
            @close="showBindVMsModal = false"
        >
            <p class="text-secondary mb-4">{{ t('dashboard.vmAlarmRules.selectVMsToBind') }}</p>
            <div class="search-box mb-4">
                <Search :size="16" class="search-icon" />
                <input
                    v-model="vmSearchQuery"
                    :placeholder="t('dashboard.vmAlarmRules.searchVMs')"
                    class="search-input"
                />
            </div>
            <div v-if="vmsLoading" class="loading-spinner" style="margin: 20px auto"></div>
            <div v-else class="channel-list">
                <label v-for="vm in filteredVMs" :key="vm.id" class="channel-item" @click="toggleVMSelection(vm.id)">
                    <input type="checkbox" :checked="selectedVMUuids.includes(vm.id)" />
                    <span class="channel-name">{{ vm.hostname || vm.name }}</span>
                    <span class="badge badge-secondary">{{ vm.id.substring(0, 8) }}</span>
                </label>
            </div>

            <template #footer>
                <button class="btn btn-secondary" @click="showBindVMsModal = false">{{ t('actions.cancel') }}</button>
                <button class="btn btn-primary" @click="saveVMBindings" :disabled="linkVMsLoading">
                    {{ t('actions.save') }}
                </button>
            </template>
        </BaseModal>
    </div>
</template>

<style scoped>
/* 绑定虚拟机弹窗里的搜索框 */
.search-box {
    display: flex;
    align-items: center;
    gap: 10px;
    background: var(--bg-secondary);
    padding: 0 12px;
    height: 40px;
    border-radius: var(--radius-md);
    border: 1px solid var(--border-light);
    transition: all 0.2s;
    flex: 1;
}

.search-box:focus-within {
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.search-icon {
    color: var(--gray-400);
}

.search-input {
    border: none;
    background: transparent;
    width: 100%;
    height: 100%;
    font-size: 0.875rem;
    color: var(--text-primary);
}

.search-input:focus {
    outline: none;
}

.filter-select {
    height: 40px;
    padding: 0 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    background: var(--bg-secondary);
    color: var(--text-primary);
    min-width: 130px;
}

.table-card {
    padding: 0;
    overflow: hidden;
}

.monospace {
    font-family: var(--font-family-mono, monospace);
    font-size: 12px;
}

.rule-name-text {
    font-weight: 600;
    font-size: 14px;
    color: var(--text-primary);
}

.rule-id {
    color: var(--text-tertiary);
    margin-top: 2px;
}

.rule-body-content {
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 20px;
}

.section-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-bottom: 8px;
}

.inner-table-wrapper {
    border: 1px solid var(--border-light);
    border-radius: 8px;
    overflow: hidden;
    background: var(--bg-primary);
}

.inner-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
}

.inner-table th {
    text-align: left;
    padding: 8px 12px;
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    font-weight: 500;
    border-bottom: 1px solid var(--border-light);
}

.inner-table td {
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-light);
}

.inner-table tr:last-child td {
    border-bottom: none;
}

.linked-vms-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
}

.vm-chip {
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    padding: 2px 10px;
    border-radius: 4px;
    font-size: 12px;
}

.actions-cell {
    display: flex;
    gap: 4px;
    align-items: center;
    justify-content: center;
}

.clickable-name {
    color: var(--primary-600);
    cursor: pointer;
    font-weight: 500;
}

.clickable-name:hover {
    text-decoration: underline;
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
    color: var(--primary-color);
}
.icon-btn-table.icon-danger:hover {
    background-color: var(--error-light);
    color: var(--error-dark);
}
/* 删除类图标按钮：静止态灰、hover 变红（见上面的 :hover 规则）。
   原先这个类叫 .text-error，与全局「错误文字为红色」的语义相反 */
.icon-danger {
    color: var(--text-tertiary);
}

.text-center {
    text-align: center;
}
.text-muted {
    color: var(--text-tertiary);
}

.badge-secondary {
    background: var(--accent-purple-light);
    color: var(--accent-purple);
}

.error-banner {
    background: var(--error-light);
    color: var(--error-dark);
    border: 1px solid var(--error-color);
    border-radius: var(--radius-sm);
    padding: 10px 14px;
    margin-bottom: var(--spacing-4);
    font-size: var(--font-size-sm);
    cursor: pointer;
}

/* Modal */
.form-stack {
    display: flex;
    flex-direction: column;
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
    font-weight: 500;
}

.form-input {
    width: 100%;
    padding: 8px 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    background: var(--bg-primary);
    color: var(--text-primary);
}

.form-input:focus {
    outline: none;
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.rules-section {
    margin-top: 4px;
}
.rules-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 8px;
}
.rules-header-left {
    display: flex;
    align-items: center;
    gap: 8px;
}
.rule-labels-row {
    display: flex;
    gap: 8px;
    margin-bottom: 4px;
    padding: 0 4px;
}
.rule-label-item {
    font-size: 11px;
    color: var(--text-tertiary);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.02em;
}
.rule-row {
    display: flex;
    gap: 8px;
    margin-bottom: 6px;
    align-items: center;
}
.rule-input-sm {
    width: 120px;
}
.ml-2 {
    margin-left: 8px;
}
.mb-0 {
    margin-bottom: 0;
}
.mb-1 {
    margin-bottom: 4px;
}
.input-tip {
    font-size: 11px;
    color: var(--text-tertiary);
    line-height: 1.2;
}

/* 校验失败的提示。原先复用的是本文件里的 .text-error，而那个类在这里被当成
   「删除按钮静止态的灰」（hover 才变红，见 .icon-btn-table.text-error:hover），
   加上 .input-tip 本身也是灰的且定义在后面，错误提示一直是灰色的 */
.input-tip-error {
    color: var(--error-color);
}

.channel-list {
    max-height: 300px;
    overflow-y: auto;
}
.channel-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px;
    cursor: pointer;
    border-radius: 4px;
}
.channel-item:hover {
    background: var(--bg-hover, var(--gray-100));
}
.channel-item input[type='checkbox'] {
    cursor: pointer;
}
.channel-name {
    flex: 1;
}

</style>
