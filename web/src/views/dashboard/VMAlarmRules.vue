<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, Search, ShieldAlert, Link, RefreshCw, X, ChevronDown, ChevronRight, Monitor, Power } from 'lucide-vue-next'
import { vmAlarmRulesApi, VM_RULE_TYPES, type VMAlarmRuleGroup, type VMRuleType } from '../../api/vmAlarmRules'
import { alarmEventsApi } from '../../api/alarmEvents'
import { notificationsApi, type NotificationChannel } from '../../api/notifications'
import { instancesApi, type Instance } from '../../api/instances'
import { useRegionStore } from '../../stores/region'
import { useToast } from '../../composables/useToast'

const { t } = useI18n()
const toast = useToast()
const regionStore = useRegionStore()
const rules = ref<VMAlarmRuleGroup[]>([])
const loading = ref(false)
const errorMsg = ref('')
const searchQuery = ref('')
const page = ref(1)
const pageSize = ref(100)
const totalPages = ref(1)
const total = ref(0)

// Create modal
const showCreateModal = ref(false)
const createForm = ref({
    name: '',
    type: 'cpu' as VMRuleType,
    rules: [{ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' }] as Record<string, any>[],
    linkedvms: [] as string[],
})

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

// Expansion logic
const expandedRules = ref<string[]>([])
const toggleRule = (id: string) => {
    const idx = expandedRules.value.indexOf(id)
    if (idx === -1) expandedRules.value.push(id)
    else expandedRules.value.splice(idx, 1)
}
const isExpanded = (id: string) => expandedRules.value.includes(id)
const isNameValid = computed(() => /^[a-zA-Z][a-zA-Z0-9_]*$/.test(createForm.value.name))
const nameError = computed(() => {
    if (!createForm.value.name) return ''
    if (!/^[a-zA-Z]/.test(createForm.value.name)) return t('dashboard.vmAlarmRules.nameStartLetterError')
    if (!/^[a-zA-Z0-9_]*$/.test(createForm.value.name)) return t('dashboard.vmAlarmRules.nameCharsetError')
    return ''
})

const filteredRules = computed(() => {
    if (!searchQuery.value) return rules.value
    const q = searchQuery.value.toLowerCase()
    return rules.value.filter(r =>
        r.name.toLowerCase().includes(q) || r.uuid.toLowerCase().includes(q)
    )
})

const fetchRules = async () => {
    loading.value = true
    try {
        const types: VMRuleType[] = ['cpu', 'memory', 'bw']
        const results = await Promise.all(types.map(t => vmAlarmRulesApi.listRules(t)))
        
        const allRules: VMAlarmRuleGroup[] = []
        results.forEach((res, idx) => {
            const type = types[idx]
            const typeRules = (res.data.data || []).map(r => ({ ...r, type }))
            allRules.push(...typeRules)
        })
        
        rules.value = allRules
        total.value = allRules.length
        totalPages.value = 1
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
    }
    showCreateModal.value = true
    if (allVMs.value.length === 0) {
        vmsLoading.value = true
        try {
            const res = await instancesApi.fetchInstances()
            const data = res.data as any
            allVMs.value = Array.isArray(data) ? data : (data.instances || [])
        } catch (err) {
            console.error('Failed to fetch instances:', err)
        } finally {
            vmsLoading.value = false
        }
    }
}

const onTypeChange = () => {
    if (createForm.value.type === 'bw') {
        createForm.value.rules = [{ direction: 'in', name: '', limit: 80, duration: 5, level: 'warning' }]
    } else {
        createForm.value.rules = [{ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' }]
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

        let res: any
        if (createForm.value.type === 'cpu') {
            res = await vmAlarmRulesApi.createCPURule({ ...base, rules: createForm.value.rules as any })
        } else if (createForm.value.type === 'memory') {
            res = await vmAlarmRulesApi.createMemoryRule({ ...base, rules: createForm.value.rules as any })
        } else if (createForm.value.type === 'bw') {
            res = await vmAlarmRulesApi.createBWRule({ ...base, enable: true, rules: createForm.value.rules as any })
        }

        const ruleUuid = res.data?.data?.uuid || res.data?.data?.group_uuid

        // Link VMs if selected
        if (createForm.value.linkedvms && createForm.value.linkedvms.length > 0 && ruleUuid) {
            try {
                await vmAlarmRulesApi.linkRule(ruleUuid, createForm.value.linkedvms.map(id => ({ vm_uuid: id })))
            } catch (err) {
                console.error('Failed to link VMs after creation:', err)
                toast.warning(t('dashboard.vmAlarmRules.ruleCreatedButLinkFailed'))
            }
        }

        showCreateModal.value = false
        await fetchRules()
    } catch (err: any) {
        console.error('Failed to create rule:', err)
        errorMsg.value = err.response?.data?.error || t('messages.error')
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
    } catch (err: any) {
        toast.error(err.response?.data?.error || t('messages.operationFailed'))
    }
}

const executeDelete = async () => {
    if (!deleteTarget.value || !deleteTarget.value.type) return
    try {
        await vmAlarmRulesApi.deleteRule(deleteTarget.value.type, deleteTarget.value.rule_id)
        showDeleteModal.value = false
        deleteTarget.value = null
        await fetchRules()
    } catch (err: any) {
        console.error('Failed to delete rule:', err)
        errorMsg.value = err.response?.data?.error || t('messages.error')
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
            alarmEventsApi.getRuleChannels(rule.rule_id),
        ])
        allChannels.value = channelsRes.data.channels || []
        const bindings = (bindingsRes.data as any).bindings || []
        selectedChannelUuids.value = bindings.map((b: any) => b.channel_uuid)
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
        await alarmEventsApi.bindRuleChannels(bindTarget.value.rule_id, selectedChannelUuids.value)
        showBindModal.value = false
        toast.success(t('messages.success'))
    } catch (err: any) {
        const errCode = err.response?.data?.error
        if (errCode === 'channel_not_synced') {
            alert(t('dashboard.vmAlarmRules.channelNotSynced'))
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
    selectedVMUuids.value = rule.linkedvms ? [...rule.linkedvms] : []
    showBindVMsModal.value = true
    vmsLoading.value = true
    vmSearchQuery.value = ''
    try {
        const res = await instancesApi.fetchInstances()
        const data = res.data as any
        allVMs.value = Array.isArray(data) ? data : (data.instances || [])
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
    return allVMs.value.filter(vm => 
        (vm.hostname || vm.name || '').toLowerCase().includes(q) || 
        vm.id.toLowerCase().includes(q)
    )
})

const saveVMBindings = async () => {
    if (!bindVMsTarget.value) return
    linkVMsLoading.value = true
    try {
        const ruleId = bindVMsTarget.value.rule_id
        const currentVMs = bindVMsTarget.value.linkedvms || []
        
        const toLink = selectedVMUuids.value.filter(id => !currentVMs.includes(id))
        const toUnlink = currentVMs.filter(id => !selectedVMUuids.value.includes(id))
        
        if (toLink.length > 0) {
            await vmAlarmRulesApi.linkRule(ruleId, toLink.map(id => ({ vm_uuid: id })))
        }
        if (toUnlink.length > 0) {
            await vmAlarmRulesApi.unlinkRule(ruleId, toUnlink.map(id => ({ vm_uuid: id })))
        }
        
        toast.success(t('dashboard.vmAlarmRules.bindSuccess'))
        showBindVMsModal.value = false
        await fetchRules()
    } catch (err: any) {
        console.error('Failed to save VM bindings:', err)
        toast.error(err.response?.data?.error || t('messages.error'))
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

const prevPage = () => { if (page.value > 1) page.value-- }
const nextPage = () => { if (page.value < totalPages.value) page.value++ }

watch([page], fetchRules)
onMounted(fetchRules)
</script>

<template>
    <div class="vpc-list-container">
        <div class="page-header">
            <div class="search-wrapper">
                <div class="search-box">
                    <Search :size="16" class="search-icon" />
                    <input v-model="searchQuery" :placeholder="t('actions.search') + '...'" class="search-input" />
                </div>
            </div>
            <div class="header-actions">
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchRules" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreate">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </div>
        </div>

        <div v-if="errorMsg" class="error-banner" @click="errorMsg = ''">{{ errorMsg }}</div>

        <div v-if="loading" class="text-center" style="padding: 48px;">
            <div class="loading-spinner" style="margin: 0 auto;"></div>
        </div>

        <div v-else-if="filteredRules.length === 0" class="text-center text-secondary" style="padding: 48px;">
            <div class="empty-state">
                <ShieldAlert :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                <p>{{ t('messages.noData') }}</p>
            </div>
        </div>

        <div v-else class="rules-list">
                <div v-for="rule in filteredRules" :key="rule.uuid" class="card rule-card" :class="{ 'rule-card-expanded': isExpanded(rule.uuid) }">
                <div class="rule-header" @click="toggleRule(rule.uuid)">
                    <div class="rule-header-left">
                        <component :is="isExpanded(rule.uuid) ? ChevronDown : ChevronRight" :size="16" class="expand-icon" />
                        <div class="rule-info">
                            <div class="rule-name-row">
                                <span class="rule-name-text">{{ rule.name }}</span>
                            </div>
                            <div class="rule-id monospace">{{ rule.uuid }}</div>
                        </div>
                    </div>
                    <div class="rule-header-right">
                        <div class="rule-summary-stats">
                            <div class="rule-status-badges">
                                <span class="badge badge-secondary" style="text-transform: uppercase;">
                                    {{ rule.type ? t('dashboard.vmAlarmRules.ruleTypes.' + rule.type) : '-' }}
                                </span>
                                <span class="badge" :class="rule.enable ? 'badge-success' : 'status-stopped'">
                                    {{ rule.enable ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                                </span>
                            </div>
                            <span class="linked-badge">{{ t('dashboard.vmAlarmRules.linkedCount', { count: rule.linkedvms?.length || 0 }) }}</span>
                        </div>
                        <div class="rule-actions">
                            <button class="icon-btn-table" @click.stop="openBindVMs(rule)" :title="t('dashboard.vmAlarmRules.bindVMs')">
                                <Monitor :size="16" />
                            </button>
                            <button class="icon-btn-table" @click.stop="openBindChannels(rule)" :title="t('dashboard.vmAlarmRules.bindChannels')">
                                <Link :size="16" />
                            </button>
                            <button class="icon-btn-table" :class="{ 'text-success': rule.enable, 'text-secondary': !rule.enable }" @click.stop="toggleRuleStatus(rule)" :title="rule.enable ? t('actions.disable') : t('actions.enable')">
                                <Power :size="14" />
                            </button>
                            <button class="icon-btn-table" @click.stop="confirmDelete(rule)">
                                <Trash2 :size="16" />
                            </button>
                        </div>
                    </div>
                </div>

                <div v-show="isExpanded(rule.uuid)" class="rule-body">
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
                                                {{ r.direction ? t('dashboard.vmAlarmRules.directions.' + r.direction) : '-' }}
                                            </td>
                                            <td>
                                                <span class="badge" :class="'badge-' + (r.level === 'critical' ? 'error' : (r.level === 'warning' ? 'warning' : 'primary'))">
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
                            <div class="section-label">{{ t('dashboard.vmAlarmRules.linkedVMs') }} ({{ rule.linkedvms?.length || 0 }})</div>
                            <div class="linked-vms-chips">
                                <div v-for="vmId in rule.linkedvms" :key="vmId" class="vm-chip">
                                    {{ vmId }}
                                </div>
                                <div v-if="!rule.linkedvms || rule.linkedvms.length === 0" class="text-secondary" style="font-size: 12px;">
                                    {{ t('dashboard.vmAlarmRules.noLinkedVMs') }}
                                </div>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        </div>

        <div v-if="totalPages > 1" class="pagination">
            <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="prevPage">{{ t('dashboard.pagination.prev') }}</button>
            <span class="page-info">{{ page }} / {{ totalPages }} ({{ total }} {{ t('dashboard.overview.total') }})</span>
            <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="nextPage">{{ t('dashboard.pagination.next') }}</button>
        </div>

        <!-- Create Modal -->
        <Teleport to="body">
            <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
                <div class="modal-content card" style="max-width: 600px;">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.vmAlarmRules.createTitle', { type: t('dashboard.vmAlarmRules.ruleTypes.' + createForm.type) }) }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showCreateModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
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
                                <input v-model="createForm.name" class="form-input" :class="{ 'input-error': nameError }" required />
                                <div v-if="nameError" class="input-tip text-error">{{ nameError }}</div>
                            </div>
                            <div class="rules-section">
                                <div class="rules-header">
                                    <div class="rules-header-left">
                                        <label class="form-label mb-0">{{ t('dashboard.vmAlarmRules.thresholds') }}</label>
                                        <span class="input-tip ml-2">{{ t('dashboard.vmAlarmRules.thresholdHint') }}</span>
                                    </div>
                                    <button class="btn btn-ghost btn-sm" @click="addRuleRow">
                                        <Plus :size="14" /> {{ t('actions.add') }}
                                    </button>
                                </div>
                                <div class="rule-labels-row">
                                    <span v-if="createForm.type === 'bw'" class="rule-label-item" style="width: 120px;">{{ t('dashboard.table.direction') }}</span>
                                    <span class="rule-label-item" style="width: 120px;">{{ t('dashboard.vmAlarmRules.thresholdLimit') }}</span>
                                    <span class="rule-label-item" style="width: 120px;">{{ t('dashboard.vmAlarmRules.durationMin') }}</span>
                                    <span class="rule-label-item" style="width: 120px;">{{ t('dashboard.vmAlarmRules.ruleLevel') }}</span>
                                    <span class="rule-label-item" style="width: 32px;"></span>
                                </div>
                                <div v-for="(rule, idx) in createForm.rules" :key="idx" class="rule-row">
                                    <select v-if="createForm.type === 'bw'" v-model="rule.direction" class="form-input rule-input-sm">
                                        <option value="in">{{ t('dashboard.vmAlarmRules.directions.in') }}</option>
                                        <option value="out">{{ t('dashboard.vmAlarmRules.directions.out') }}</option>
                                    </select>
                                    <input v-model.number="rule.limit" type="number" class="form-input rule-input-sm" :placeholder="t('dashboard.forms.placeholder.limitPercentExample')" min="1" max="100" />
                                    <input v-model.number="rule.duration" type="number" class="form-input rule-input-sm" :placeholder="t('dashboard.vmAlarmRules.durationMin')" min="1" />
                                    <select v-model="rule.level" class="form-input rule-input-sm">
                                        <option value="critical">{{ t('dashboard.vmAlarmRules.levels.critical') }}</option>
                                        <option value="warning">{{ t('dashboard.vmAlarmRules.levels.warning') }}</option>
                                        <option value="info">{{ t('dashboard.vmAlarmRules.levels.info') }}</option>
                                    </select>
                                    <button v-if="createForm.rules.length > 1" class="btn btn-ghost btn-icon text-error" @click="removeRuleRow(idx)">
                                        <Trash2 :size="14" />
                                    </button>
                                </div>
                            </div>
                            <div class="form-group mt-2">
                                <label class="form-label">{{ t('dashboard.vmAlarmRules.bindVMs') }} ({{ t('actions.optional') }})</label>
                                <div class="vm-create-selection">
                                    <div v-if="vmsLoading" class="loading-spinner small"></div>
                                    <div v-else class="channel-list" style="max-height: 160px; border: 1px solid var(--border-light); border-radius: 6px; padding: 4px;">
                                        <label v-for="vm in allVMs" :key="vm.id" class="channel-item">
                                            <input type="checkbox" :value="vm.id" v-model="createForm.linkedvms" />
                                            <span class="channel-name">{{ vm.hostname || vm.name }}</span>
                                            <span class="badge badge-secondary">{{ vm.id.substring(0, 8) }}</span>
                                        </label>
                                        <div v-if="allVMs.length === 0" class="text-muted p-2">{{ t('messages.noNics') }}</div>
                                    </div>
                                </div>
                            </div>
                        </div>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showCreateModal = false">{{ t('actions.cancel') }}</button>
                        <button class="btn btn-primary" @click="submitCreate" :disabled="!createForm.name">{{ t('actions.save') }}</button>
                    </div>
                </div>
            </div>
        </Teleport>

        <!-- Delete Modal -->
        <Teleport to="body">
            <div v-if="showDeleteModal" class="modal-overlay" @click.self="showDeleteModal = false">
                <div class="modal-content card" style="max-width: 440px;">
                    <div class="modal-header">
                        <h3>{{ t('actions.confirmDelete') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showDeleteModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
                        <p>{{ t('dashboard.vmAlarmRules.deleteConfirm', { name: deleteTarget?.name }) }}</p>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showDeleteModal = false">{{ t('actions.cancel') }}</button>
                        <button class="btn btn-danger" @click="executeDelete">{{ t('actions.delete') }}</button>
                    </div>
                </div>
            </div>
        </Teleport>

        <!-- Bind Channels Modal -->
        <Teleport to="body">
            <div v-if="showBindModal" class="modal-overlay" @click.self="showBindModal = false">
                <div class="modal-content card" style="max-width: 480px;">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.vmAlarmRules.bindChannels') }} - {{ bindTarget?.name }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showBindModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
                        <div v-if="bindLoading" class="loading-spinner" style="margin: 20px auto;"></div>
                        <div v-else-if="allChannels.length === 0" class="text-muted">
                            {{ t('dashboard.vmAlarmRules.noChannels') }}
                        </div>
                        <div v-else class="channel-list">
                            <label v-for="ch in allChannels" :key="ch.uuid" class="channel-item" @click="toggleChannel(ch.uuid)">
                                <input type="checkbox" :checked="selectedChannelUuids.includes(ch.uuid)" />
                                <span class="channel-name">{{ ch.name }}</span>
                                <span class="badge" :class="ch.type === 'feishu' ? 'badge-info' : 'badge-secondary'">{{ ch.type }}</span>
                            </label>
                        </div>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showBindModal = false">{{ t('actions.cancel') }}</button>
                        <button class="btn btn-primary" @click="saveChannelBindings" :disabled="bindLoading">{{ t('actions.save') }}</button>
                    </div>
                </div>
            </div>
        </Teleport>

        <!-- Bind VMs Modal -->
        <Teleport to="body">
            <div v-if="showBindVMsModal" class="modal-overlay" @click.self="showBindVMsModal = false">
                <div class="modal-content card" style="max-width: 480px;">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.vmAlarmRules.bindVMs') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showBindVMsModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
                        <p class="text-secondary mb-4">{{ t('dashboard.vmAlarmRules.selectVMsToBind') }}</p>
                        <div class="search-box mb-4">
                            <Search :size="16" class="search-icon" />
                            <input v-model="vmSearchQuery" :placeholder="t('dashboard.vmAlarmRules.searchVMs')" class="search-input" />
                        </div>
                        <div v-if="vmsLoading" class="loading-spinner" style="margin: 20px auto;"></div>
                        <div v-else class="channel-list">
                            <label v-for="vm in filteredVMs" :key="vm.id" class="channel-item" @click="toggleVMSelection(vm.id)">
                                <input type="checkbox" :checked="selectedVMUuids.includes(vm.id)" />
                                <span class="channel-name">{{ vm.hostname || vm.name }}</span>
                                <span class="badge badge-secondary">{{ vm.id.substring(0, 8) }}</span>
                            </label>
                        </div>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showBindVMsModal = false">{{ t('actions.cancel') }}</button>
                        <button class="btn btn-primary" @click="saveVMBindings" :disabled="linkVMsLoading">{{ t('actions.save') }}</button>
                    </div>
                </div>
            </div>
        </Teleport>
    </div>
</template>

<style scoped>
.page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0;
    padding-right: 20px;
}

.search-wrapper {
    display: flex;
    gap: 8px;
    flex: 1;
    max-width: 560px;
}

.header-actions {
    display: flex;
    gap: 8px;
    align-items: center;
}

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

.search-icon { color: var(--gray-400); }

.search-input {
    border: none;
    background: transparent;
    width: 100%;
    height: 100%;
    font-size: 0.875rem;
    color: var(--text-primary);
}

.search-input:focus { outline: none; }

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

.table-card { padding: 0; overflow: hidden; }

.empty-state { display: flex; flex-direction: column; align-items: center; justify-content: center; }

.monospace { font-family: var(--font-family-mono, monospace); font-size: 12px; }

.rules-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
}

.rule-card {
    padding: 0;
    overflow: hidden;
    transition: all 0.2s;
}

.rule-card-expanded {
    border-color: var(--primary-color);
    box-shadow: var(--shadow-lg);
}

.rule-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    cursor: pointer;
    user-select: none;
}

.rule-header:hover {
    background: var(--bg-secondary);
}

.rule-header-left {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
}

.expand-icon {
    color: var(--text-tertiary);
    transition: transform 0.2s;
}

.rule-info {
    min-width: 0;
}

.rule-name-row {
    display: flex;
    align-items: center;
    gap: 8px;
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

.rule-header-right {
    display: flex;
    align-items: center;
    gap: 20px;
}

.rule-summary-stats {
    display: flex;
    align-items: center;
    gap: 16px;
}

.rule-status-badges {
    display: flex;
    gap: 4px;
}

.linked-badge {
    font-size: 12px;
    color: var(--text-tertiary);
    background: var(--bg-secondary);
    padding: 2px 8px;
    border-radius: 12px;
    white-space: nowrap;
}

.rule-actions {
    display: flex;
    gap: 4px;
}

.rule-body {
    border-top: 1px solid var(--border-light);
    background: var(--bg-secondary);
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
    font-family: var(--font-family-mono, monospace);
}

.actions-cell { display: flex; gap: 4px; align-items: center; }

.clickable-name {
    color: var(--primary-600);
    cursor: pointer;
    font-weight: 500;
}

.clickable-name:hover {
    text-decoration: underline;
}

.icon-btn-table {
    width: 32px; height: 32px; border-radius: 8px; border: none;
    background: transparent; color: var(--text-tertiary);
    display: flex; align-items: center; justify-content: center;
    cursor: pointer; transition: all 0.2s;
}

.icon-btn-table:hover { background-color: var(--bg-tertiary); color: var(--primary-color); }
.icon-btn-table.text-error:hover { background-color: var(--error-light); color: var(--error-dark); }
.text-error { color: var(--text-tertiary); }

.pagination { display: flex; align-items: center; justify-content: center; gap: 12px; padding: 16px 0; }
.page-info { font-size: 13px; color: var(--text-tertiary); }

.text-danger { color: var(--error-color); }
.text-center { text-align: center; }
.text-muted { color: var(--text-tertiary); }

.badge-secondary { background: var(--accent-purple-light); color: var(--accent-purple); }

.error-banner {
    background: var(--error-light); color: var(--error-dark); border: 1px solid var(--error-color);
    border-radius: var(--radius-sm); padding: 10px 14px; margin-bottom: var(--spacing-4);
    font-size: var(--font-size-sm); cursor: pointer;
}

/* Modal */
.modal-lg { max-width: 600px; }
.form-stack { display: flex; flex-direction: column; gap: 16px; }
.form-group { display: flex; flex-direction: column; }
.form-label { font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 4px; font-weight: 500; }

.form-input {
    width: 100%; padding: 8px 12px;
    border: 1px solid var(--border-light); border-radius: var(--radius-md);
    font-size: 0.875rem; background: var(--bg-primary); color: var(--text-primary);
}

.form-input:focus { outline: none; border-color: var(--primary-300); box-shadow: 0 0 0 2px var(--primary-100); }

.rules-section { margin-top: 4px; }
.rules-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.rules-header-left { display: flex; align-items: center; gap: 8px; }
.rule-labels-row { display: flex; gap: 8px; margin-bottom: 4px; padding: 0 4px; }
.rule-label-item { font-size: 11px; color: var(--text-tertiary); font-weight: 600; text-transform: uppercase; letter-spacing: 0.02em; }
.rule-row { display: flex; gap: 8px; margin-bottom: 6px; align-items: center; }
.rule-input-sm { width: 120px; }
.ml-2 { margin-left: 8px; }
.mb-0 { margin-bottom: 0; }
.mb-1 { margin-bottom: 4px; }
.input-tip { font-size: 11px; color: var(--text-tertiary); line-height: 1.2; }


.channel-list { max-height: 300px; overflow-y: auto; }
.channel-item { display: flex; align-items: center; gap: 8px; padding: 8px; cursor: pointer; border-radius: 4px; }
.channel-item:hover { background: var(--bg-hover, #f3f4f6); }
.channel-item input[type="checkbox"] { cursor: pointer; }
.channel-name { flex: 1; }

.btn-danger {
    background: var(--error-color); color: white; border: none;
    padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}
.btn-danger:hover { background: var(--error-dark); }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
