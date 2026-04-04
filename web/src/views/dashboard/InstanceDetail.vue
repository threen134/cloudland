<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { instancesApi, type Instance } from '../../api/instances'
import { securityGroupsApi, type SecurityGroup } from '../../api/networks'
import MonitoringCharts from '../../components/monitoring/MonitoringCharts.vue'
import { vmAlarmRulesApi, VM_RULE_TYPES, type VMAlarmRuleGroup, type VMRuleType } from '../../api/vmAlarmRules'
import { ArrowLeft, Play, Square, RotateCw, Trash2, Server, Monitor, Cpu, HardDrive, MemoryStick, Network, Key, ExternalLink, Copy, Check, ShieldAlert, Link, Unlink, Eye, EyeOff, ChevronDown, KeyRound, RefreshCw, Maximize2, Pencil, Shuffle, Shield, Activity } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const instanceId = route.params.id as string

const instance = ref<Instance | null>(null)
const loading = ref(true)
const error = ref('')
const actionLoading = ref<string | null>(null)
const copiedField = ref<string | null>(null)
const showPassword = ref(false)
const showActionMenu = ref(false)
const activeTab = ref<'info' | 'monitoring' | 'alerts'>('info')

const toast = useToast()

const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}

const closeActionMenu = () => {
    showActionMenu.value = false
}

// --- Delete Confirmation Modal Logic ---
const deleteModalVisible = ref(false)
const deletingInstance = ref(false)
const deleteError = ref('')

const handleDeleteClick = () => {
    deleteModalVisible.value = true
}

const closeDeleteModal = () => {
    deleteModalVisible.value = false
    deleteError.value = ''
}

const confirmDelete = async () => {
    if (!instance.value) return
    deletingInstance.value = true
    deleteError.value = ''
    try {
        await instancesApi.deleteInstance(instanceId)
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'instances' })
    } catch (err: any) {
        console.error('Failed to delete instance:', err)
        deleteError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        deletingInstance.value = false
    }
}

const fetchInstance = async (showLoading: boolean = true) => {
    if (showLoading) {
        loading.value = true
        error.value = ''
    }
    try {
        const response = await instancesApi.getInstance(instanceId)
        const data = response.data as any
        instance.value = data.instance || data
    } catch (err) {
        console.error('Failed to fetch instance:', err)
        if (showLoading) error.value = t('dashboard.instanceDetail.loadError')
    } finally {
        if (showLoading) loading.value = false
    }
}

const handleAction = async (action: 'start' | 'stop' | 'restart' | 'hard_stop' | 'hard_restart' | 'pause' | 'resume') => {
    if (!instance.value) return

    actionLoading.value = action
    try {
        const hostname = instance.value.hostname
        switch (action) {
            case 'start':
                await instancesApi.startInstance(instanceId, hostname)
                break
            case 'stop':
                await instancesApi.stopInstance(instanceId, hostname)
                break
            case 'restart':
                await instancesApi.rebootInstance(instanceId, hostname)
                break
            case 'hard_stop':
                await instancesApi.hardStopInstance(instanceId, hostname)
                break
            case 'hard_restart':
                await instancesApi.hardRebootInstance(instanceId, hostname)
                break
            case 'pause':
                await instancesApi.pauseInstance(instanceId, hostname)
                break
            case 'resume':
                await instancesApi.resumeInstance(instanceId, hostname)
                break
        }

        let targetStableStates: string[] = []
        if (['start', 'restart', 'hard_restart', 'resume'].includes(action)) targetStableStates = ['running', 'active', 'error']
        if (['stop', 'hard_stop'].includes(action)) targetStableStates = ['stopped', 'shutoff', 'shut_off', 'error']
        if (action === 'pause') targetStableStates = ['paused', 'error']

        let attempts = 0
        const checkStatus = async () => {
            attempts++
            await fetchInstance(false)
            const currentStatus = instance.value?.status?.toLowerCase() || ''
            if (targetStableStates.includes(currentStatus) || attempts >= 15) {
                actionLoading.value = null
                if (currentStatus === 'error') {
                    toast.error(t('dashboard.instanceDetail.actionFailed', { action }))
                } else {
                    toast.success(t('dashboard.instanceDetail.actionSuccess', { action }))
                }
            } else {
                setTimeout(checkStatus, 3000)
            }
        }

        // Start polling after 2 seconds
        setTimeout(checkStatus, 2000)
    } catch (err: any) {
        console.error(`Failed to ${action} instance:`, err)
        actionLoading.value = null
        toast.error(err.response?.data?.error_message || err.message || t('messages.error'))
    }
}

const openConsole = async () => {
    // Open window immediately to avoid popup blockers
    const consoleWindow = window.open('about:blank', '_blank')
    if (!consoleWindow) {
        alert(t('dashboard.instanceDetail.popupBlocked'))
        return
    }

    try {
        const response = await instancesApi.getConsole(instanceId)
        const { console_host, console_port, console_path, token } = response.data

        const host = console_host
        const port = console_port || 443
        const path = console_path || 'websockify'
        const encrypt = port === 443

        const externalUrl = `https://novnc.com/noVNC/vnc.html?host=${host}&port=${port}&autoconnect=true&encrypt=${encrypt}&path=${path}?token=${token}`
        consoleWindow.location.href = externalUrl
    } catch (error: any) {
        console.error('Failed to get console info:', error)
        consoleWindow.close()
        alert(t('dashboard.instanceDetail.consoleError') + (error.response?.data?.error_message || error.message))
    }
}

const goBack = () => {
    router.back()
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

// --- Alarm Rules Integration ---
interface LinkedRule {
    type: VMRuleType
    typeLabel: string
    uuid: string
    name: string
    enable: boolean
    rules?: any[]
}

const expandedRules = ref<Set<string>>(new Set())
const toggleRuleExpand = (uuid: string) => {
    if (expandedRules.value.has(uuid)) {
        expandedRules.value.delete(uuid)
    } else {
        expandedRules.value.add(uuid)
    }
}

const linkedRules = ref<LinkedRule[]>([])
const alarmLoading = ref(false)
const alarmError = ref('')
const showLinkModal = ref(false)
const availableRules = ref<LinkedRule[]>([])
const linkLoading = ref(false)
const unlinkLoading = ref<string | null>(null)

const getSeverityClass = (level: string) => {
    const l = (level || '').toLowerCase()
    return {
        'badge-critical': l === 'critical',
        'badge-warning': l === 'warning',
        'badge-info': l === 'info'
    }
}

const fetchLinkedRules = async () => {
    alarmLoading.value = true
    const linked: LinkedRule[] = []
    try {
        const results = await Promise.all(
            VM_RULE_TYPES.map(rt =>
                vmAlarmRulesApi.listRules(rt.value, { page: 1, page_size: 1000 })
                    .then(res => ({ type: rt.value, label: rt.label, data: res.data.data || [] }))
                    .catch(() => ({ type: rt.value, label: rt.label, data: [] as VMAlarmRuleGroup[] }))
            )
        )
        for (const { type, label, data } of results) {
            for (const rule of data) {
                if (rule.linkedvms?.includes(instanceId)) {
                    linked.push({
                        type: type as VMRuleType,
                        typeLabel: t('dashboard.vmAlarmRules.ruleTypes.' + type),
                        uuid: rule.uuid,
                        name: rule.name,
                        enable: rule.enable,
                        rules: rule.rules || [],
                    })
                }
            }
        }
    } catch (err) {
        console.error('Failed to fetch alarm rules:', err)
        alarmError.value = t('messages.error')
    } finally {
        linkedRules.value = linked
        alarmLoading.value = false
    }
}

const openLinkModal = async () => {
    linkLoading.value = true
    showLinkModal.value = true
    const available: LinkedRule[] = []
    try {
        const results = await Promise.all(
            VM_RULE_TYPES.map(rt =>
                vmAlarmRulesApi.listRules(rt.value, { page: 1, page_size: 1000 })
                    .then(res => ({ type: rt.value, label: rt.label, data: res.data.data || [] }))
                    .catch(() => ({ type: rt.value, label: rt.label, data: [] as VMAlarmRuleGroup[] }))
            )
        )
        for (const { type, label, data } of results) {
            for (const rule of data) {
                if (!rule.linkedvms?.includes(instanceId)) {
                    available.push({
                        type: type as VMRuleType,
                        typeLabel: t('dashboard.vmAlarmRules.ruleTypes.' + type),
                        uuid: rule.uuid,
                        name: rule.name,
                        enable: rule.enable,
                    })
                }
            }
        }
    } catch (err) {
        console.error('Failed to fetch available rules:', err)
    } finally {
        availableRules.value = available
        linkLoading.value = false
    }
}

const linkRule = async (rule: LinkedRule) => {
    try {
        await vmAlarmRulesApi.linkRule(rule.uuid, [{ vm_uuid: instanceId }])
        showLinkModal.value = false
        await fetchLinkedRules()
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to link rule:', err)
        alarmError.value = err.response?.data?.error || t('messages.error')
    }
}

const unlinkRule = async (rule: LinkedRule) => {
    unlinkLoading.value = rule.uuid
    try {
        await vmAlarmRulesApi.unlinkRule(rule.uuid, [{ vm_uuid: instanceId }])
        await fetchLinkedRules()
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to unlink rule:', err)
        alarmError.value = err.response?.data?.error || t('messages.error')
    } finally {
        unlinkLoading.value = null
    }
}

// --- Reset Password Modal ---
const showResetPasswordModal = ref(false)
const resetPasswordForm = ref({ user_name: 'root', password: '', confirmPassword: '' })
const resetPasswordLoading = ref(false)
const resetPasswordError = ref('')
const showResetPassword = ref(false)

const generateRandomPassword = () => {
    const charset = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%&*'
    const array = new Uint8Array(16)
    crypto.getRandomValues(array)
    const pwd = Array.from(array, b => charset[b % charset.length]).join('')
    resetPasswordForm.value.password = pwd
    resetPasswordForm.value.confirmPassword = pwd
    showResetPassword.value = true
}

const openResetPasswordModal = () => {
    resetPasswordForm.value = { user_name: 'root', password: '', confirmPassword: '' }
    resetPasswordError.value = ''
    showResetPassword.value = false
    showResetPasswordModal.value = true
}

const confirmResetPassword = async () => {
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
        await instancesApi.setUserPassword(instanceId, resetPasswordForm.value.user_name, resetPasswordForm.value.password)
        showResetPasswordModal.value = false
        toast.success(t('dashboard.instanceDetail.resetPasswordSuccess'))
        await fetchInstance(false)
    } catch (err: any) {
        resetPasswordError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        resetPasswordLoading.value = false
    }
}

// --- Resize Modal ---
const showResizeModal = ref(false)
const resizeForm = ref({ cpu: 0, memory: 0 })
const resizeLoading = ref(false)
const resizeError = ref('')

const openResizeModal = () => {
    resizeForm.value = {
        cpu: instance.value?.cpu || 0,
        memory: instance.value?.memory || 0,
    }
    resizeError.value = ''
    showResizeModal.value = true
}

const confirmResize = async () => {
    if (resizeForm.value.cpu < 1 || resizeForm.value.memory < 1) {
        resizeError.value = t('dashboard.instanceDetail.resizeInvalid')
        return
    }
    resizeLoading.value = true
    resizeError.value = ''
    try {
        await instancesApi.resizeInstance(instanceId, resizeForm.value.cpu, resizeForm.value.memory)
        showResizeModal.value = false
        toast.success(t('dashboard.instanceDetail.resizeSuccess'))
        await fetchInstance(false)
    } catch (err: any) {
        resizeError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        resizeLoading.value = false
    }
}

// --- Rename Modal ---
const showRenameModal = ref(false)
const renameForm = ref({ hostname: '' })
const renameLoading = ref(false)
const renameError = ref('')

const openRenameModal = () => {
    renameForm.value.hostname = instance.value?.hostname || ''
    renameError.value = ''
    showRenameModal.value = true
}

const confirmRename = async () => {
    if (!renameForm.value.hostname.trim()) {
        renameError.value = t('dashboard.instanceDetail.hostnameRequired')
        return
    }
    renameLoading.value = true
    renameError.value = ''
    try {
        await instancesApi.renameInstance(instanceId, renameForm.value.hostname)
        showRenameModal.value = false
        toast.success(t('dashboard.instanceDetail.renameSuccess'))
        await fetchInstance(false)
    } catch (err: any) {
        renameError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        renameLoading.value = false
    }
}

const getStatusClass = (status: string) => {
    const statusMap: Record<string, string> = {
        'running': 'status-running',
        'active': 'status-running',
        'stopped': 'status-stopped',
        'shutoff': 'status-stopped',
        'shut_off': 'status-stopped',
        'paused': 'status-paused',
        'provisioning': 'status-pending',
        'starting': 'status-pending',
        'stopping': 'status-pending',
        'deleting': 'status-pending',
        'error': 'status-error'
    }
    return statusMap[status?.toLowerCase()] || 'status-pending'
}

const getStatusText = (status: string) => {
    const key = status?.toLowerCase().replace(/ /g, '_')
    const translated = t(`dashboard.instanceStatus.${key}`)
    return translated === `dashboard.instanceStatus.${key}` ? status : translated
}

const formatMemory = (mb?: number) => {
    if (!mb) return '-'
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(0)} ${t('specs.gb')}`
    }
    return `${mb} ${t('specs.mb')}`
}

const navigateToVolume = (volumeId: string) => {
    router.push({ name: 'volume-detail', params: { id: volumeId } })
}

const navigateToSecurityGroup = (sgId: string) => {
    router.push({ name: 'security-group-detail', params: { id: sgId } })
}

const navigateToSubnet = (subnetId: string) => {
    router.push({ name: 'subnet-detail', params: { id: subnetId } })
}

const navigateToVPC = (vpcId: string) => {
    router.push({ name: 'vpc-detail', params: { id: vpcId } })
}

// --- Edit Security Groups Modal ---
interface EditingInterface {
    id: string
    name?: string
    currentSgs: { id: string; name: string }[]
}

const showEditSgModal = ref(false)
const editingSgIface = ref<EditingInterface | null>(null)
const availableSecgroups = ref<SecurityGroup[]>([])
const sgListTruncated = ref(false)
const selectedSgIds = ref<Set<string>>(new Set())
const editSgLoading = ref(false)
const editSgError = ref('')

const openEditSgModal = async (iface: NonNullable<Instance['interfaces']>[number]) => {
    const vpcId = instance.value?.vpc?.id
    if (!vpcId) {
        toast.error(t('dashboard.instanceDetail.sgRequiresVpc'))
        return
    }
    editingSgIface.value = {
        id: iface.id,
        name: iface.name,
        currentSgs: iface.security_groups || [],
    }
    selectedSgIds.value = new Set((iface.security_groups || []).map(sg => sg.id))
    editSgError.value = ''
    sgListTruncated.value = false
    editSgLoading.value = true
    showEditSgModal.value = true
    try {
        const pageSize = 200
        const result = await securityGroupsApi.list({ limit: pageSize, vpc_id: vpcId })
        availableSecgroups.value = result.security_groups || []
        sgListTruncated.value = result.total > pageSize
    } catch (err: any) {
        editSgError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        editSgLoading.value = false
    }
}

const toggleSg = (sgId: string) => {
    if (selectedSgIds.value.has(sgId)) {
        selectedSgIds.value.delete(sgId)
    } else {
        selectedSgIds.value.add(sgId)
    }
    selectedSgIds.value = new Set(selectedSgIds.value)
}

const confirmEditSg = async () => {
    if (!editingSgIface.value) return
    editSgLoading.value = true
    editSgError.value = ''
    try {
        const payload = { security_groups: [...selectedSgIds.value].map(id => ({ id })) }
        await instancesApi.patchInterface(instanceId, editingSgIface.value.id, payload)
        showEditSgModal.value = false
        toast.success(t('dashboard.instanceDetail.securityGroupsUpdated'))
        await fetchInstance(false)
    } catch (err: any) {
        editSgError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        editSgLoading.value = false
    }
}

onMounted(() => {
    fetchInstance()
    fetchLinkedRules()
})
</script>

<template>
    <div class="detail-page">
        <!-- Toast Notification removed -->

        <!-- Back Button -->
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
            <button class="btn btn-primary" @click="() => fetchInstance()">{{ $t('actions.retry') }}</button>
        </div>

        <div v-else-if="instance" class="detail-content">
            <!-- Title Bar with Actions -->
            <div class="title-bar card">
                <div class="title-info">
                    <div class="title-icon">
                        <Monitor :size="28" />
                    </div>
                    <div>
                        <h2 class="instance-title">
                            {{ instance.hostname }}
                            <span :class="['badge', getStatusClass(instance.status)]">{{ getStatusText(instance.status) }}</span>
                        </h2>
                        <div class="instance-id-row">
                            <span class="instance-id">{{ instance.id }}</span>
                            <button class="copy-btn" @click="copyToClipboard(instance.id, 'id')" :title="copiedField === 'id' ? $t('messages.copied') : $t('dashboard.instanceDetail.copyId')">
                                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <div class="action-dropdown">
                        <button class="btn btn-primary" @click="toggleActionMenu">
                            {{ $t('actions.actions') }} <ChevronDown :size="14" />
                        </button>
                        <Transition name="dropdown">
                            <div v-if="showActionMenu" class="dropdown-menu" @click="closeActionMenu">
                                <button
                                    v-if="['stopped', 'shutoff', 'shut_off'].includes(instance.status?.toLowerCase())"
                                    class="dropdown-item"
                                    @click="handleAction('start')"
                                    :disabled="!!actionLoading"
                                >
                                    <span v-if="actionLoading === 'start'" class="loading-spinner small"></span>
                                    <Play v-else :size="14" />
                                    {{ actionLoading === 'start' ? $t('dashboard.instanceDetail.starting') : $t('actions.start') }}
                                </button>
                                <button
                                    v-else
                                    class="dropdown-item"
                                    @click="handleAction('stop')"
                                    :disabled="!!actionLoading"
                                >
                                    <span v-if="actionLoading === 'stop'" class="loading-spinner small"></span>
                                    <Square v-else :size="14" />
                                    {{ actionLoading === 'stop' ? $t('dashboard.instanceDetail.stopping') : $t('actions.stop') }}
                                </button>
                                <button
                                    class="dropdown-item"
                                    @click="handleAction('restart')"
                                    :disabled="!!actionLoading || instance.status !== 'running'"
                                >
                                    <span v-if="actionLoading === 'restart'" class="loading-spinner small"></span>
                                    <RotateCw v-else :size="14" />
                                    {{ actionLoading === 'restart' ? $t('dashboard.instanceDetail.rebooting') : $t('actions.restart') }}
                                </button>
                                <div class="dropdown-divider"></div>
                                <button
                                    class="dropdown-item"
                                    @click="handleAction('hard_stop')"
                                    :disabled="!!actionLoading || ['stopped', 'shutoff', 'shut_off', 'paused'].includes(instance.status?.toLowerCase())"
                                >
                                    <span v-if="actionLoading === 'hard_stop'" class="loading-spinner small"></span>
                                    <Square v-else :size="14" />
                                    {{ actionLoading === 'hard_stop' ? $t('dashboard.instanceDetail.stopping') : $t('dashboard.instanceDetail.hardStop') }}
                                </button>
                                <button
                                    class="dropdown-item"
                                    @click="handleAction('hard_restart')"
                                    :disabled="!!actionLoading || ['stopped', 'shutoff', 'shut_off'].includes(instance.status?.toLowerCase())"
                                >
                                    <span v-if="actionLoading === 'hard_restart'" class="loading-spinner small"></span>
                                    <RefreshCw v-else :size="14" />
                                    {{ actionLoading === 'hard_restart' ? $t('dashboard.instanceDetail.rebooting') : $t('dashboard.instanceDetail.hardRestart') }}
                                </button>
                                <button
                                    v-if="instance.status?.toLowerCase() !== 'paused'"
                                    class="dropdown-item"
                                    @click="handleAction('pause')"
                                    :disabled="!!actionLoading || instance.status?.toLowerCase() !== 'running'"
                                >
                                    <span v-if="actionLoading === 'pause'" class="loading-spinner small"></span>
                                    <span v-else style="font-size: 14px;">⏸</span>
                                    {{ actionLoading === 'pause' ? $t('dashboard.instanceDetail.pausing') : $t('dashboard.instanceDetail.pause') }}
                                </button>
                                <button
                                    v-if="instance.status?.toLowerCase() === 'paused'"
                                    class="dropdown-item"
                                    @click="handleAction('resume')"
                                    :disabled="!!actionLoading"
                                >
                                    <span v-if="actionLoading === 'resume'" class="loading-spinner small"></span>
                                    <Play v-else :size="14" />
                                    {{ actionLoading === 'resume' ? $t('dashboard.instanceDetail.resuming') : $t('dashboard.instanceDetail.resume') }}
                                </button>
                                <div class="dropdown-divider"></div>
                                <button class="dropdown-item" @click="openConsole">
                                    <img src="/images/vnc.svg" alt="VNC" width="14" height="14" /> {{ $t('actions.console') }}
                                </button>
                                <div class="dropdown-divider"></div>
                                <button class="dropdown-item" @click="openRenameModal">
                                    <Pencil :size="14" /> {{ $t('dashboard.instanceDetail.rename') }}
                                </button>
                                <button class="dropdown-item" @click="openResetPasswordModal">
                                    <KeyRound :size="14" /> {{ $t('dashboard.instanceDetail.resetPassword') }}
                                </button>
                                <button class="dropdown-item" @click="openResizeModal">
                                    <Maximize2 :size="14" /> {{ $t('actions.resize') }}
                                </button>
                                <div class="dropdown-divider"></div>
                                <button class="dropdown-item dropdown-item-danger" @click="handleDeleteClick">
                                    <Trash2 :size="14" /> {{ $t('actions.delete') }}
                                </button>
                            </div>
                        </Transition>
                        <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
                    </div>
                </div>
            </div>
            <!-- Tabs Navigation -->
            <div class="card tabs-nav-container">
                <div class="tabs-nav">
                    <button 
                        :class="['tab-btn', { active: activeTab === 'info' }]"
                        @click="activeTab = 'info'"
                    >
                        <Server :size="16" /> {{ $t('dashboard.instanceDetail.generalInfo') }}
                    </button>
                    <button 
                        :class="['tab-btn', { active: activeTab === 'monitoring' }]"
                        @click="activeTab = 'monitoring'"
                    >
                        <Activity :size="16" /> {{ $t('dashboard.instanceDetail.resourceMonitoring') }}
                    </button>
                    <button 
                        :class="['tab-btn', { active: activeTab === 'alerts' }]"
                        @click="activeTab = 'alerts'"
                    >
                        <ShieldAlert :size="16" /> {{ t('dashboard.instanceDetail.monitoringAlerts') }}
                    </button>
                </div>
            </div>

            <!-- Two-Column Layout (Info Tab) -->
            <div v-if="activeTab === 'info'" class="two-col-layout">
                <!-- Left Column: General Info + Storage -->
                <div class="col-stack">
                    <div class="card info-card">
                        <h3>{{ $t('dashboard.instanceDetail.generalInfo') }}</h3>
                        <div class="key-value-list">
                            <div class="kv-item">
                                <span class="label">{{ $t('dashboard.table.createdAt') }}</span>
                                <span class="value">{{ instance.created_at || '-' }}</span>
                            </div>
                            <div class="kv-item">
                                <span class="label">{{ $t('dashboard.table.zone') }}</span>
                                <span class="value">{{ instance.zone || '-' }}</span>
                            </div>
                            <div class="kv-item">
                                <span class="label">{{ $t('dashboard.table.hyper') }}</span>
                                <span class="value">{{ instance.hypervisor || '-' }}</span>
                            </div>
                            <div class="kv-item" v-if="instance.root_passwd">
                                <span class="label">{{ $t('dashboard.instanceDetail.rootPassword') }}</span>
                                <span class="value password-value">
                                    <span class="mono">{{ showPassword ? instance.root_passwd : '••••••••' }}</span>
                                    <button class="icon-btn-inline" @click="showPassword = !showPassword" :title="showPassword ? $t('dashboard.instanceDetail.hidePassword') : $t('dashboard.instanceDetail.showPassword')">
                                        <Eye v-if="!showPassword" :size="14" />
                                        <EyeOff v-else :size="14" />
                                    </button>
                                    <button class="icon-btn-inline" @click="copyToClipboard(instance.root_passwd, 'root_passwd')" :title="$t('dashboard.instanceDetail.copyPassword')">
                                        <Check v-if="copiedField === 'root_passwd'" :size="14" class="copied-icon" />
                                        <Copy v-else :size="14" />
                                    </button>
                                </span>
                            </div>
                        </div>
                    </div>

                    <div class="card info-card">
                        <h3>{{ $t('dashboard.instanceDetail.specs') }}</h3>
                        <div class="key-value-list">
                            <div class="kv-item" v-if="instance.flavor">
                                <span class="label">{{ $t('dashboard.table.flavor') }}</span>
                                <span class="value">{{ typeof instance.flavor === 'string' ? instance.flavor : instance.flavor.name }}</span>
                            </div>
                            <div class="kv-item">
                                <span class="label"><Cpu :size="14" /> {{ $t('dashboard.instanceDetail.cpu') }}</span>
                                <span class="value">{{ instance.cpu || (instance.flavor && typeof instance.flavor === 'object' ? instance.flavor.cpu : '-') }}</span>
                            </div>
                            <div class="kv-item">
                                <span class="label"><MemoryStick :size="14" /> {{ $t('dashboard.instanceDetail.ram') }}</span>
                                <span class="value">{{ formatMemory(instance.memory || (instance.flavor && typeof instance.flavor === 'object' ? instance.flavor.memory : undefined)) }}</span>
                            </div>
                            <div class="kv-item">
                                <span class="label"><HardDrive :size="14" /> {{ $t('dashboard.instanceDetail.disk') }}</span>
                                <span class="value">{{ instance.disk || (instance.flavor && typeof instance.flavor === 'object' ? instance.flavor.disk : '-') }} {{ t('specs.gb') }}</span>
                            </div>
                            <div class="kv-item">
                                <span class="label"><HardDrive :size="14" /> {{ $t('dashboard.instanceDetail.image') }}</span>
                                <span class="value">{{ instance.image?.name || '-' }}</span>
                            </div>
                        </div>
                    </div>

                    <div class="card info-card">
                        <h3>{{ $t('dashboard.instanceDetail.storage') }}</h3>
                        <div class="key-value-list">
                            <div v-if="!instance.volumes?.length" class="text-secondary empty-hint">
                                {{ $t('messages.noVolumesAttached') }}
                            </div>
                            <div v-else v-for="volume in instance.volumes" :key="volume.id" class="kv-item">
                                <span class="label">
                                    <HardDrive :size="14" />
                                    {{ volume.target || volume.device || $t('dashboard.instanceDetail.volume') }}
                                    <span v-if="volume.booting || volume.boot_index === 0" class="status-badge status-success mini-badge">{{ $t('dashboard.instanceDetail.boot') }}</span>
                                </span>
                                <span class="value">
                                    <span v-if="volume.size" class="mono volume-size">{{ volume.size }} {{ t('specs.gb') }}</span>
                                    <a href="#" @click.prevent="navigateToVolume(volume.id)" class="resource-link">
                                        {{ volume.name || volume.id.substring(0, 8) }} <ExternalLink :size="12" class="inline-icon" />
                                    </a>
                                </span>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Right Column: Network + SSH Keys -->
                <div class="col-stack">
                    <div class="card info-card">
                        <h3>{{ $t('dashboard.instanceDetail.network') }}</h3>
                        <div class="key-value-list">
                            <div class="kv-item" v-if="instance.vpc?.name">
                                <span class="label">{{ $t('dashboard.table.vpc') }}</span>
                                <span class="value">
                                    <a v-if="instance.vpc?.id" href="#" @click.prevent="navigateToVPC(instance.vpc.id)" class="resource-link">
                                        {{ instance.vpc.name }}
                                    </a>
                                    <span v-else>{{ instance.vpc?.name || '-' }}</span>
                                </span>
                            </div>

                            <div v-if="!instance.interfaces?.length" class="text-secondary empty-hint">
                                {{ $t('messages.noNics') }}
                            </div>
                            <template v-else>
                                <div v-for="(iface, index) in instance.interfaces" :key="iface.id" class="interface-block" :class="{ 'interface-separator': index > 0 }">
                                    <div class="kv-item interface-header">
                                        <span class="label interface-name">
                                            <Network :size="14" /> {{ iface.name || $t('dashboard.instanceDetail.interface') }}
                                            <span v-if="iface.is_primary" class="status-badge status-running mini-badge">{{ $t('dashboard.instanceDetail.primary') }}</span>
                                        </span>
                                    </div>
                                    <div class="kv-item interface-detail">
                                        <span class="label">{{ $t('dashboard.table.subnet') }}</span>
                                        <span class="value">
                                            <a v-if="iface.subnet?.id" href="#" @click.prevent="navigateToSubnet(iface.subnet.id)" class="resource-link">
                                                {{ iface.subnet.name }}
                                            </a>
                                            <span v-else>{{ iface.subnet?.name || '-' }}</span>
                                        </span>
                                    </div>
                                    <div class="kv-item interface-detail">
                                        <span class="label">{{ $t('dashboard.table.ipAddress') }}</span>
                                        <span class="value mono">{{ iface.ip_address ? iface.ip_address.split('/')[0] : '-' }}</span>
                                    </div>
                                    <div class="kv-item interface-detail">
                                        <span class="label">{{ $t('dashboard.instanceDetail.macAddress') }}</span>
                                        <span class="value mono">{{ iface.mac_address || '-' }}</span>
                                    </div>
                                    <template v-for="fip in iface.floating_ips" :key="fip.id">
                                        <div v-if="(fip.ip_address || fip.fip_address) !== iface.ip_address" class="kv-item interface-detail">
                                            <span class="label">{{ $t('dashboard.instanceDetail.floatingIp') }}</span>
                                            <span class="value mono">{{ fip.ip_address || fip.fip_address || '-' }}</span>
                                        </div>
                                        <div v-if="fip.vlan" class="kv-item interface-detail">
                                            <span class="label">{{ $t('dashboard.instanceDetail.fipVlan') }}</span>
                                            <span class="value">{{ fip.vlan }}</span>
                                        </div>
                                    </template>
                                    <div class="kv-item interface-detail">
                                        <span class="label">{{ $t('dashboard.securityGroups') }}</span>
                                        <span class="value sg-value-row">
                                            <template v-if="iface.security_groups?.length">
                                                <template v-for="(sg, sgIndex) in iface.security_groups" :key="sg.id">
                                                    <a href="#" @click.prevent="navigateToSecurityGroup(sg.id)" class="resource-link">{{ sg.name || sg.id.substring(0, 8) }}</a><span v-if="sgIndex < iface.security_groups.length - 1">, </span>
                                                </template>
                                            </template>
                                            <span v-else class="text-secondary">-</span>
                                            <button class="btn-icon-inline" @click="openEditSgModal(iface)" :aria-label="$t('dashboard.instanceDetail.editSecurityGroups')" :title="$t('dashboard.instanceDetail.editSecurityGroups')">
                                                <Pencil :size="12" />
                                            </button>
                                        </span>
                                    </div>
                                </div>
                            </template>
                        </div>
                    </div>

                    <div class="card info-card">
                        <h3>{{ $t('dashboard.sshKeys') }}</h3>
                        <div class="key-value-list">
                            <div v-if="!instance.keys?.length" class="kv-item">
                                <span class="label"><Key :size="14" /> {{ $t('dashboard.instanceDetail.keyPair') }}</span>
                                <span class="value">-</span>
                            </div>
                            <div v-else v-for="key in instance.keys" :key="key.id" class="kv-item">
                                <span class="label"><Key :size="14" /> {{ $t('dashboard.instanceDetail.keyPair') }}</span>
                                <span class="value">{{ key.name }}</span>
                            </div>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Monitoring Charts (Monitoring Tab) -->
            <MonitoringCharts 
                v-if="activeTab === 'monitoring'"
                :instance-id="instance.id" 
                :interfaces="instance.interfaces || []" 
                :volumes="instance.volumes || []" 
            />

            <!-- Monitoring & Alerts (Alerts Tab) -->
            <div v-if="activeTab === 'alerts'" class="card alarm-card">
                <div class="alarm-card-header">
                    <h3><ShieldAlert :size="16" /> {{ t('dashboard.instanceDetail.monitoringAlerts') }}</h3>
                    <button class="btn btn-ghost btn-sm" @click="openLinkModal">
                        <Link :size="14" /> {{ t('dashboard.instanceDetail.linkRule') }}
                    </button>
                </div>
                <div v-if="alarmError" class="alarm-error-banner" @click="alarmError = ''">{{ alarmError }}</div>
                <div v-if="alarmLoading" class="text-secondary empty-hint">{{ $t('messages.loading') }}</div>
                <div v-else-if="linkedRules.length === 0" class="text-secondary empty-hint">
                    {{ t('dashboard.instanceDetail.noLinkedRules') }}
                </div>
                <table v-else class="alarm-table">
                    <thead>
                        <tr>
                            <th>{{ t('dashboard.table.name') }}</th>
                            <th>{{ t('dashboard.instanceDetail.ruleType') }}</th>

                            <th>{{ t('dashboard.table.status') }}</th>
                            <th></th>
                        </tr>
                    </thead>
                    <tbody>
                        <template v-for="rule in linkedRules" :key="rule.uuid">
                            <tr :class="{ 'row-expanded': expandedRules.has(rule.uuid) }">
                                <td class="expand-cell">
                                    <button class="btn-icon-sm" @click="toggleRuleExpand(rule.uuid)">
                                        <ChevronDown :size="14" :class="{ 'icon-rotate': expandedRules.has(rule.uuid) }" />
                                    </button>
                                    {{ rule.name }}
                                </td>
                                <td><span class="badge badge-secondary">{{ rule.typeLabel }}</span></td>
                                <td>
                                    <span class="badge" :class="rule.enable ? 'badge-success' : 'badge-muted'">
                                        {{ rule.enable ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                                    </span>
                                </td>
                                <td>
                                    <div class="table-actions">
                                        <button class="btn btn-ghost btn-sm text-danger" @click="unlinkRule(rule)" :disabled="unlinkLoading === rule.uuid">
                                            <Unlink :size="14" /> {{ t('dashboard.instanceDetail.unlink') }}
                                        </button>
                                    </div>
                                </td>
                            </tr>
                            <tr v-if="expandedRules.has(rule.uuid)" class="expansion-row">
                                <td colspan="4" class="expansion-cell-full">
                                    <div class="expansion-wrapper">
                                        <table class="inner-table">
                                            <thead>
                                                <tr>
                                                    <th v-if="rule.type === 'bw'">{{ t('dashboard.table.direction') }}</th>
                                                        <th>{{ t('dashboard.vmAlarmRules.thresholdLimit') }}</th>
                                                        <th>{{ t('dashboard.vmAlarmRules.durationMin') }}</th>
                                                        <th>{{ t('dashboard.vmAlarmRules.ruleLevel') }}</th>
                                                    </tr>
                                                </thead>
                                                <tbody>
                                                    <tr v-for="(detail, idx) in rule.rules" :key="idx">
                                                        <td v-if="rule.type === 'bw'">
                                                            {{ detail.direction ? t('dashboard.vmAlarmRules.directions.' + detail.direction) : '-' }}
                                                        </td>
                                                        <td>
                                                            <span v-if="rule.type === 'bw'">{{ (detail.limit / 1024 / 1024).toFixed(0) }} {{ t('specs.mbps') }}</span>
                                                            <span v-else>{{ detail.limit }}%</span>
                                                        </td>
                                                        <td>{{ detail.duration }} {{ t('dashboard.alarm.minutes') }}</td>
                                                        <td>
                                                            <span v-if="detail.level" class="badge" :class="getSeverityClass(detail.level)">
                                                                {{ t('dashboard.vmAlarmRules.levels.' + detail.level.toLowerCase()) }}
                                                            </span>
                                                            <span v-else>-</span>
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

            <!-- Link Rule Modal -->
            <div v-if="showLinkModal" class="modal-overlay" @click.self="showLinkModal = false">
                <div class="modal-content">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.instanceDetail.linkRule') }}</h3>
                    </div>
                    <div class="modal-body">
                        <div v-if="linkLoading" class="text-secondary" style="padding: 12px 0;">{{ $t('messages.loading') }}</div>
                        <div v-else-if="availableRules.length === 0" class="text-secondary" style="padding: 12px 0;">
                            {{ t('dashboard.instanceDetail.noAvailableRules') }}
                        </div>
                        <div v-else class="rule-pick-list">
                            <div v-for="rule in availableRules" :key="rule.uuid" class="rule-pick-item" @click="linkRule(rule)">
                                <div class="rule-pick-info">
                                    <span class="rule-pick-name">{{ rule.name }}</span>
                                    <span class="badge badge-secondary">{{ rule.typeLabel }}</span>
                                </div>
                                <Link :size="14" class="rule-pick-icon" />
                            </div>
                        </div>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary btn-sm" @click="showLinkModal = false">{{ t('actions.cancel') }}</button>
                    </div>
                </div>
            </div>
        </div>

        <DeleteModal
            :show="deleteModalVisible"
            :resource-name="instance?.hostname"
            :resource-id="instance?.id"
            :loading="deletingInstance"
            :error="deleteError"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />

        <!-- Edit Security Groups Modal -->
        <div v-if="showEditSgModal" class="modal-overlay" @click.self="showEditSgModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3><Shield :size="16" /> {{ $t('dashboard.instanceDetail.editSecurityGroupsTitle', { name: editingSgIface?.name || editingSgIface?.id?.substring(0, 8) }) }}</h3>
                </div>
                <div class="modal-body">
                    <div v-if="editSgError" class="modal-error">{{ editSgError }}</div>
                    <div v-if="sgListTruncated" class="modal-warning">{{ $t('dashboard.instanceDetail.sgListTruncated') }}</div>
                    <div v-if="editSgLoading && availableSecgroups.length === 0" class="text-secondary empty-hint">{{ $t('messages.loading') }}</div>
                    <div v-else-if="availableSecgroups.length === 0" class="text-secondary empty-hint">{{ $t('dashboard.instanceDetail.noSecurityGroupsAvailable') }}</div>
                    <div v-else class="sg-checkbox-list">
                        <label v-for="sg in availableSecgroups" :key="sg.id" class="sg-checkbox-item">
                            <input type="checkbox" :checked="selectedSgIds.has(sg.id)" @change="toggleSg(sg.id)" />
                            <span class="sg-checkbox-name">{{ sg.name }}</span>
                            <span v-if="sg.is_default" class="status-badge status-running mini-badge">{{ $t('dashboard.instanceDetail.primary') }}</span>
                        </label>
                    </div>
                    <div v-if="selectedSgIds.size === 0 && !editSgLoading" class="sg-empty-hint">{{ $t('dashboard.instanceDetail.sgEmptyWillUseDefault') }}</div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-ghost" @click="showEditSgModal = false">{{ $t('actions.cancel') }}</button>
                    <button class="btn btn-primary" @click="confirmEditSg" :disabled="editSgLoading">
                        {{ editSgLoading ? $t('messages.loading') : $t('actions.save') }}
                    </button>
                </div>
            </div>
        </div>

        <!-- Reset Password Modal -->
        <div v-if="showResetPasswordModal" class="modal-overlay" @click.self="showResetPasswordModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3>{{ $t('dashboard.instanceDetail.resetPassword') }}</h3>
                </div>
                <div class="modal-body">
                    <div v-if="resetPasswordError" class="modal-error">{{ resetPasswordError }}</div>
                    <div class="form-group">
                        <label>{{ $t('dashboard.instanceDetail.userName') }}</label>
                        <input v-model="resetPasswordForm.user_name" type="text" class="form-input" />
                    </div>
                    <div class="form-group">
                        <label>{{ $t('auth.password') }}</label>
                        <div class="password-input-wrapper">
                            <input v-model="resetPasswordForm.password" :type="showResetPassword ? 'text' : 'password'" class="form-input" :placeholder="$t('dashboard.instanceDetail.passwordPlaceholder')" />
                            <button class="password-toggle-btn" type="button" @click="showResetPassword = !showResetPassword">
                                <Eye v-if="!showResetPassword" :size="14" />
                                <EyeOff v-else :size="14" />
                            </button>
                        </div>
                    </div>
                    <div class="form-group">
                        <label>{{ $t('dashboard.instanceDetail.confirmPassword') }}</label>
                        <div class="password-input-wrapper">
                            <input v-model="resetPasswordForm.confirmPassword" :type="showResetPassword ? 'text' : 'password'" class="form-input" />
                            <button class="password-toggle-btn" type="button" @click="showResetPassword = !showResetPassword">
                                <Eye v-if="!showResetPassword" :size="14" />
                                <EyeOff v-else :size="14" />
                            </button>
                        </div>
                    </div>
                    <button class="btn btn-ghost btn-sm" type="button" @click="generateRandomPassword" style="margin-top: 4px;">
                        <Shuffle :size="14" /> {{ $t('dashboard.instanceDetail.generatePassword') }}
                    </button>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary btn-sm" @click="showResetPasswordModal = false">{{ $t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm" @click="confirmResetPassword" :disabled="resetPasswordLoading">
                        <span v-if="resetPasswordLoading" class="loading-spinner small"></span>
                        {{ $t('actions.confirm') }}
                    </button>
                </div>
            </div>
        </div>

        <!-- Resize Modal -->
        <div v-if="showResizeModal" class="modal-overlay" @click.self="showResizeModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3>{{ $t('actions.resize') }}</h3>
                </div>
                <div class="modal-body">
                    <div v-if="resizeError" class="modal-error">{{ resizeError }}</div>
                    <div class="form-group">
                        <label>{{ $t('dashboard.instanceDetail.cpuLabel') }}</label>
                        <input v-model.number="resizeForm.cpu" type="number" min="1" class="form-input" />
                    </div>
                    <div class="form-group">
                        <label>{{ $t('dashboard.instanceDetail.ramLabel') }}</label>
                        <input v-model.number="resizeForm.memory" type="number" min="1" class="form-input" />
                    </div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary btn-sm" @click="showResizeModal = false">{{ $t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm" @click="confirmResize" :disabled="resizeLoading">
                        <span v-if="resizeLoading" class="loading-spinner small"></span>
                        {{ $t('actions.confirm') }}
                    </button>
                </div>
            </div>
        </div>

        <!-- Rename Modal -->
        <div v-if="showRenameModal" class="modal-overlay" @click.self="showRenameModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3>{{ $t('dashboard.instanceDetail.rename') }}</h3>
                </div>
                <div class="modal-body">
                    <div v-if="renameError" class="modal-error">{{ renameError }}</div>
                    <div class="form-group">
                        <label>{{ $t('dashboard.instanceDetail.hostname') }}</label>
                        <input v-model="renameForm.hostname" type="text" class="form-input" />
                    </div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary btn-sm" @click="showRenameModal = false">{{ $t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm" @click="confirmRename" :disabled="renameLoading">
                        <span v-if="renameLoading" class="loading-spinner small"></span>
                        {{ $t('actions.confirm') }}
                    </button>
                </div>
            </div>
        </div>
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
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-5);
    flex-wrap: wrap;
    gap: var(--spacing-3);
}

.title-info {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
}

.title-icon {
    width: 52px;
    height: 52px;
    border-radius: var(--radius-lg);
    background: linear-gradient(135deg, var(--primary-50), var(--primary-100));
    color: var(--primary-color);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
}

.instance-title {
    margin: 0 0 4px 0;
    font-size: var(--font-size-xl);
    font-weight: var(--font-weight-semibold);
    color: var(--primary-color);
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

.instance-title .badge {
    font-size: var(--font-size-xs);
    font-weight: 500;
    vertical-align: middle;
}

.instance-id-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.instance-id {
    font-size: var(--font-size-xs);
    color: var(--text-light);
    font-family: var(--font-family-mono);
}

.title-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

/* Action Dropdown */
.action-dropdown {
    position: relative;
}

.dropdown-backdrop {
    position: fixed;
    inset: 0;
    z-index: 9;
}

.dropdown-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 180px;
    background: var(--bg-primary, #fff);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
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

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-hover, #f3f4f6);
}

.dropdown-item:disabled {
    opacity: 0.4;
    cursor: not-allowed;
}

.dropdown-item-danger {
    color: var(--error-color, #ef4444);
}

.dropdown-item-danger:hover:not(:disabled) {
    background: #fef2f2;
}

.dropdown-divider {
    height: 1px;
    background: var(--border-light);
    margin: 4px 0;
}

.dropdown-enter-active {
    transition: opacity 0.15s, transform 0.15s;
}

.dropdown-leave-active {
    transition: opacity 0.1s, transform 0.1s;
}

.dropdown-enter-from {
    opacity: 0;
    transform: translateY(-4px);
}

.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-4px);
}

/* Two-Column Layout */
.two-col-layout {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-4);
    align-items: start;
}

.col-stack {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
}

@media (max-width: 768px) {
    .two-col-layout {
        grid-template-columns: 1fr;
    }
}

/* Info Cards */
.info-card {
    padding: var(--spacing-5);
}

.info-card h3 {
    font-size: var(--font-size-md);
    font-weight: 600;
    margin: 0 0 var(--spacing-4) 0;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-3);
}

.section-title-divider {
    margin-top: var(--spacing-5) !important;
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.kv-item {
    display: flex;
    justify-content: space-between;
    font-size: var(--font-size-sm);
}

.kv-item .label {
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    gap: 6px;
}

.kv-item .value {
    color: var(--text-primary);
    font-weight: 500;
    text-align: right;
}

.value.mono, .mono {
    font-family: var(--font-family-mono);
}

.empty-hint {
    font-size: 13px;
    padding: 4px 0;
}

/* Password */
.password-value {
    display: flex;
    align-items: center;
    gap: 6px;
}

.icon-btn-inline {
    background: none;
    border: none;
    padding: 2px;
    cursor: pointer;
    color: var(--text-light);
    display: inline-flex;
    align-items: center;
    transition: color 0.15s;
}

.icon-btn-inline:hover {
    color: var(--primary-color);
}

.copy-btn {
    background: none;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    padding: 2px 5px;
    cursor: pointer;
    color: var(--text-light);
    display: inline-flex;
    align-items: center;
    transition: all 0.15s;
}

.copy-btn:hover {
    color: var(--primary-color);
    border-color: var(--primary-200);
    background: var(--primary-50);
}

.copied-icon {
    color: var(--success-color);
}

/* Storage */
.volume-size {
    margin-right: 8px;
    color: var(--text-secondary);
}

.inline-icon {
    display: inline;
    margin-left: 2px;
}

.mini-badge {
    margin-left: 8px;
    font-size: 10px;
    padding: 0 4px;
}

/* Network */
.interface-block {
    margin-top: 8px;
}

.interface-separator {
    margin-top: 16px;
    padding-top: 16px;
    border-top: 1px dashed var(--border-light);
}

.interface-header {
    margin-bottom: 8px;
}

.interface-name {
    color: var(--primary-color) !important;
    font-weight: 500;
}

.interface-detail .label {
    padding-left: 20px;
}

/* Resource Links */
.resource-link {
    color: var(--primary-600);
    cursor: pointer;
    text-decoration: none;
    transition: color 0.2s;
}

.resource-link:hover {
    color: var(--primary-700);
    text-decoration: underline;
}

/* Buttons */
.btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    transition: all var(--transition-base);
}

.btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
    filter: grayscale(100%);
}

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 6px 10px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    transition: background var(--transition-base);
}

.btn-danger:hover {
    background: var(--error-dark);
}

.loading-spinner.small {
    width: 14px;
    height: 14px;
    border-width: 2px;
}

/* Alarm Card */
.alarm-card {
    padding: var(--spacing-5);
    margin-bottom: var(--spacing-6);
}

.alarm-card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-3);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-3);
}

.alarm-card-header h3 {
    font-size: var(--font-size-md);
    font-weight: 600;
    margin: 0;
    color: var(--text-primary);
    display: flex;
    align-items: center;
    gap: 6px;
}

.alarm-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--font-size-sm);
}

.alarm-table th {
    text-align: left;
    padding: var(--spacing-3) var(--spacing-4);
    color: var(--text-tertiary);
    font-size: var(--font-size-xs);
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    background: var(--bg-secondary);
    border-bottom: 1px solid var(--border-light);
}

.alarm-table td {
    padding: var(--spacing-3) var(--spacing-4);
    border-bottom: 1px solid var(--border-light);
    color: var(--text-primary);
}

.alarm-table tr:last-child td {
    border-bottom: none;
}

.badge-critical { background: var(--error-light); color: var(--error-dark); }
.badge-warning { background: var(--warning-light); color: var(--warning-dark); }
.badge-info { background: var(--info-light); color: var(--info-dark); }
.badge-success { background: var(--success-light); color: var(--success-dark); }
.badge-muted { background: var(--bg-tertiary); color: var(--text-tertiary); }
.badge-secondary { background: var(--accent-purple-light); color: var(--accent-purple); }
.text-danger { color: var(--error-color); }

.expand-cell {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.btn-icon-sm {
    background: none;
    border: none;
    padding: 2px;
    cursor: pointer;
    color: var(--text-tertiary);
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all 0.2s;
}

.btn-icon-sm:hover {
    background: var(--bg-tertiary);
    color: var(--primary-color);
}

.icon-rotate {
    transform: rotate(180deg);
}

.table-actions {
    display: flex;
    justify-content: flex-end;
}

.row-expanded td {
    border-bottom: none !important;
}

.expansion-cell-full {
    padding: 0 !important;
    background: var(--bg-primary);
}

.expansion-wrapper {
    padding: var(--spacing-4) var(--spacing-6) var(--spacing-5) 42px;
    width: 100%;
}

.section-label {
    font-size: 11px;
    font-weight: 600;
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-bottom: var(--spacing-3);
    display: block;
}

.inner-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    overflow: hidden;
}

.inner-table tr:hover {
    background-color: var(--bg-secondary);
}

.inner-table th {
    text-align: left;
    padding: 8px 12px;
    background: var(--bg-tertiary);
    color: var(--text-tertiary);
    font-weight: 600;
    font-size: 11px;
    border-bottom: 1px solid var(--border-light);
    text-transform: uppercase;
    letter-spacing: 0.05em;
}

.inner-table td {
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-light);
    color: var(--text-primary);
}

.inner-table tr:last-child td {
    border-bottom: none;
}

.alarm-error-banner {
    background: var(--error-light);
    color: var(--error-dark);
    border: 1px solid var(--error-light);
    border-radius: var(--radius-md);
    padding: var(--spacing-3) var(--spacing-4);
    margin-bottom: var(--spacing-4);
    font-size: var(--font-size-sm);
    cursor: pointer;
}

/* Link Rule Modal */
.rule-pick-list {
    max-height: 320px;
    overflow-y: auto;
}

.rule-pick-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 12px;
    cursor: pointer;
    border-radius: var(--radius-md);
    transition: background 0.15s;
}

.rule-pick-item:hover {
    background: var(--bg-hover, #f3f4f6);
}

.rule-pick-info {
    display: flex;
    align-items: center;
    gap: 8px;
}

.rule-pick-name {
    font-weight: 500;
}

.rule-pick-icon {
    color: var(--primary-color);
    opacity: 0;
    transition: opacity 0.15s;
}

.rule-pick-item:hover .rule-pick-icon {
    opacity: 1;
}

/* Form elements in modals */
.form-group {
    margin-bottom: var(--spacing-3);
}

.form-group label {
    display: block;
    font-size: var(--font-size-sm);
    font-weight: 500;
    color: var(--text-secondary);
    margin-bottom: 4px;
}

.form-input {
    width: 100%;
    padding: 8px 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    background: var(--bg-primary, #fff);
    color: var(--text-primary);
    box-sizing: border-box;
}

.form-input:focus {
    outline: none;
    border-color: var(--primary-color);
    box-shadow: 0 0 0 2px var(--primary-50);
}

.modal-error {
    background: #fef2f2;
    color: #dc2626;
    border: 1px solid #fecaca;
    border-radius: 6px;
    padding: 8px 12px;
    margin-bottom: 12px;
    font-size: 13px;
}

.password-input-wrapper {
    position: relative;
    display: flex;
    align-items: center;
}

.password-input-wrapper .form-input {
    padding-right: 36px;
}

.password-toggle-btn {
    position: absolute;
    right: 8px;
    background: none;
    border: none;
    cursor: pointer;
    color: var(--text-light);
    padding: 2px;
    display: flex;
    align-items: center;
}

.password-toggle-btn:hover {
    color: var(--primary-color);
}



/* Security Group inline edit */
.sg-value-row {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
}

.btn-icon-inline {
    background: none;
    border: none;
    padding: 2px;
    cursor: pointer;
    color: var(--text-light);
    display: inline-flex;
    align-items: center;
    transition: color 0.15s;
    flex-shrink: 0;
}

.btn-icon-inline:hover {
    color: var(--primary-color);
}

.sg-checkbox-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-height: 300px;
    overflow-y: auto;
}

.sg-checkbox-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: background 0.15s;
}

.sg-checkbox-item:hover {
    background: var(--bg-secondary);
}

.sg-checkbox-item input[type="checkbox"] {
    cursor: pointer;
}

.sg-checkbox-name {
    flex: 1;
    font-size: var(--font-size-sm);
}

.modal-warning {
    padding: var(--spacing-3) var(--spacing-4);
    background: var(--warning-light);
    border: 1px solid var(--warning-light);
    border-radius: var(--radius-md);
    color: var(--warning-dark);
    font-size: var(--font-size-sm);
    margin-bottom: var(--spacing-4);
}

.sg-empty-hint {
    margin-top: 10px;
    padding: 6px 10px;
    background: var(--bg-secondary);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

/* Tabs Navigation Styles */
.tabs-nav-container {
    padding: 0;
    margin-bottom: var(--spacing-6);
    overflow: hidden;
    border: none;
    background: transparent;
    box-shadow: none;
}

.tabs-nav {
    display: flex;
    padding: 0;
    border-bottom: 1px solid var(--border-light);
    background: transparent;
}

.tab-btn {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    padding: var(--spacing-4) var(--spacing-6);
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    font-size: var(--font-size-sm);
    font-weight: 500;
    color: var(--text-secondary);
    cursor: pointer;
    transition: all 0.2s;
    margin-bottom: -1px;
}

.tab-btn:hover {
    color: var(--text-primary);
}

.tab-btn.active {
    color: var(--primary-color);
    border-bottom-color: var(--primary-color);
    background: var(--bg-primary);
    border-top-left-radius: var(--radius-md);
    border-top-right-radius: var(--radius-md);
}

.alarm-card {
    margin-top: var(--spacing-4);
}
</style>
