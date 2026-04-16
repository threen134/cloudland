<script setup lang="ts">
import { ref, onMounted, computed, watch, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { instancesApi, type Instance } from '../../api/instances'
import { Play, Square, RotateCw, Trash2, Plus, Terminal, MoreVertical, Search, X, Check, Copy, Monitor, ChevronDown, ChevronUp, PlusCircle, MinusCircle, RefreshCw, Cpu, HardDrive, Eye, EyeOff, Shuffle, Pencil, KeyRound, Maximize2, Server, Activity, Network, Globe, HelpCircle } from 'lucide-vue-next'

import { imagesApi, type Image } from '../../api/images'
import { vpcsApi, subnetsApi, securityGroupsApi, floatingIpsApi, type VPC, type Subnet, type SecurityGroup, type FloatingIP } from '../../api/networks'

import { keysApi, type SSHKey } from '../../api/keys'
import { flavorsApi } from '../../api/flavors'
import { zonesApi, type Zone } from '../../api/zones'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { isValidName } from '../../utils/validation'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import { useRegionStore } from '../../stores/region'
import { useAuthStore } from '../../stores/auth'

const region = useRegionStore()
const authStore = useAuthStore()
const isSystemAdmin = computed(() => authStore.user?.role === 'admin' || authStore.user?.is_superuser === true)



const vClickOutside = {
  mounted(el: any, binding: any) {
    el.clickOutsideEvent = (event: Event) => {
      if (!(el === event.target || el.contains(event.target))) {
        binding.value(event)
      }
    }
    document.addEventListener('click', el.clickOutsideEvent)
  },
  unmounted(el: any) {
    document.removeEventListener('click', el.clickOutsideEvent)
  }
}

const instanceList = ref<Instance[]>([])
const loading = ref(false)
const actionLoading = ref<Record<string, string | null>>({})
const searchQuery = ref('')

const ifaceTooltipVisible = ref(false)
const ifaceTooltipPos = ref({ bottom: '0px', right: '0px' })

const showIfaceHelp = (event: MouseEvent) => {
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
    ifaceTooltipPos.value = {
        bottom: `${window.innerHeight - rect.top + 8}px`,
        right: `${window.innerWidth - rect.right}px`
    }
    ifaceTooltipVisible.value = true
}

const hideIfaceHelp = () => {
    ifaceTooltipVisible.value = false
}

const router = useRouter()
const { t } = useI18n()
const toast = useToast()


const instanceMetrics = ref<Record<string, { cpu: number, memory: number }>>({})
let metricsTimer: any = null

const fetchUsageMetrics = async () => {
    const ids = filteredInstances.value
        .filter(inst => ['running', 'active'].includes(inst.status?.toLowerCase()))
        .map(inst => inst.id)
    if (!ids.length) return

    try {
        const now = Math.floor(Date.now() / 1000)
        const start = (now - 3600).toString()
        const end = now.toString()

        const [cpuRes, memRes] = await Promise.all([
            instancesApi.getCPUMetrics({ id: ids, start, end, step: '60s' }),
            instancesApi.getMemoryMetrics({ id: ids, start, end, step: '60s' })
        ])

        const newMetrics: Record<string, { cpu: number, memory: number }> = {}
        ids.forEach(id => {
            // CPU: match by metric.uuid, one-dimensional values array [{time, value}]
            const cpuResult = cpuRes.data?.data?.result?.find((r: any) => r.metric?.uuid === id)
            const cpuValues = cpuResult?.values || []
            const lastCpu = cpuValues.length
                ? parseFloat(cpuValues[cpuValues.length - 1].value || 0)
                : 0

            // Memory: match by metric.uuid, two-dimensional values array [totalValues[], usedValues[]]
            const memResult = memRes.data?.data?.result?.find((r: any) => r.metric?.uuid === id)
            let lastMem = 0
            if (memResult?.values?.length >= 2) {
                const totalValues = memResult.values[0]
                const usedValues = memResult.values[1]
                const total = totalValues.length ? parseFloat(totalValues[totalValues.length - 1].value || 0) : 0
                const used = usedValues.length ? parseFloat(usedValues[usedValues.length - 1].value || 0) : 0
                lastMem = total > 0 ? (used / total) * 100 : 0
            }

            newMetrics[id] = { cpu: lastCpu, memory: lastMem }
        })
        instanceMetrics.value = { ...instanceMetrics.value, ...newMetrics }
    } catch (err) {
        console.error('Failed to fetch instance usage:', err)
    }
}

const fetchInstances = async (showLoading: boolean = true) => {
    if (showLoading) loading.value = true
    try {
        const response = await instancesApi.fetchInstances()
        const data = response.data as any
        instanceList.value = Array.isArray(data) ? data : (data.instances || [])
        
        // Start fetching metrics after full list is loaded
        setTimeout(fetchUsageMetrics, 500)
    } catch (error) {
        console.error('API fetch failed:', error)
        instanceList.value = []
    } finally {
        if (showLoading) loading.value = false
    }
}

const filteredInstances = computed(() => {
    if (!searchQuery.value) return instanceList.value
    const query = searchQuery.value.toLowerCase()
    return instanceList.value.filter(inst => {
        const nameMatch = (inst.name?.toLowerCase() || '').includes(query)
        const hostnameMatch = (inst.hostname?.toLowerCase() || '').includes(query)
        const idMatch = (inst.id?.toLowerCase() || '').includes(query)
        const ipMatch = (inst.ip_address?.toLowerCase() || '').includes(query)
        const interfaceIpMatch = inst.interfaces?.some(iface => (iface.ip_address?.toLowerCase() || '').includes(query))
        const fipMatch = inst.interfaces?.some(iface => iface.floating_ips?.some((fip: any) => 
            fip.type?.toLowerCase() !== 'native' && (fip.fip_address?.toLowerCase() || '').includes(query)
        ))
        
        const vpcMatch = (inst.vpc?.name?.toLowerCase() || '').includes(query)
        const imageMatch = (inst.image?.name?.toLowerCase() || '').includes(query)
        
        return nameMatch || hostnameMatch || idMatch || ipMatch || interfaceIpMatch || fipMatch || vpcMatch || imageMatch
    })
})

const navigateToDetail = (instance: Instance) => {
    router.push({ name: 'instance-detail', params: { id: instance.id } })
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
    const s = status?.toLowerCase()
    if (!s) return '-'
    // Note: status keys are defined in dashboard.instanceStatus
    return t(`dashboard.instanceStatus.${s}`) || status
}

const formatMemory = (mb: number) => {
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(0)} GB`
    }
    return `${mb} MB`
}

const getIPAddress = (instance: Instance) => {
    const primaryIface = instance.interfaces?.find(iface => iface.is_primary) || instance.interfaces?.[0]
    const addr = primaryIface?.ip_address
    if (!addr) return '-'
    return addr.split('/')[0]
}

const getFloatingIP = (instance: Instance) => {
    const primaryIface = instance.interfaces?.find(iface => iface.is_primary) || instance.interfaces?.[0]
    if (primaryIface?.floating_ips && primaryIface.floating_ips.length > 0) {
        const fip = primaryIface.floating_ips.find((f: any) => f.type?.toLowerCase() !== 'native')
        if (fip && fip.fip_address) {
            return fip.fip_address.split('/')[0]
        }
    }
    return null
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

const handleAction = async (instance: Instance, action: 'start' | 'stop' | 'restart' | 'hard_stop' | 'hard_restart' | 'pause' | 'resume') => {
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
        if (['start', 'restart', 'hard_restart', 'resume'].includes(action)) targetStableStates = ['running', 'active', 'error']
        if (['stop', 'hard_stop'].includes(action)) targetStableStates = ['stopped', 'shutoff', 'shut_off', 'error']
        if (action === 'pause') targetStableStates = ['paused', 'error']

        let attempts = 0
        const checkStatus = async () => {
            attempts++
            await fetchInstances(false)
            const currentInstance = instanceList.value.find(i => i.id === instance.id)
            const currentStatus = currentInstance?.status?.toLowerCase() || ''
            if (!currentInstance || targetStableStates.includes(currentStatus) || attempts >= 15) {
                actionLoading.value[instance.id] = null
                if (currentStatus === 'error') {
                    toast.error(t('dashboard.instanceDetail.actionFailed', { action: t(`dashboard.instanceDetail.${action}`) }))
                } else {
                    toast.success(t('dashboard.instanceDetail.actionSuccess', { action: t(`dashboard.instanceDetail.${action}`) }))
                }
            } else {
                setTimeout(checkStatus, 3000)
            }
        }
        
        setTimeout(checkStatus, 2000)
    } catch (error: any) {
        console.error(`Failed to ${action} instance:`, error)
        actionLoading.value[instance.id] = null
        toast.error(error.response?.data?.error_message || error.message || t('dashboard.userDetail.loadError'))
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
    } catch (err: any) {
        renameError.value = err.response?.data?.error_message || err.message || t('dashboard.userDetail.loadError')
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
    const pwd = Array.from(array, b => charset[b % charset.length]).join('')
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
        await instancesApi.setUserPassword(selectedInstance.value.id, resetPasswordForm.value.user_name, resetPasswordForm.value.password)
        resetPasswordModalVisible.value = false
        toast.success(t('dashboard.instanceDetail.resetPasswordSuccess'))
    } catch (err: any) {
        resetPasswordError.value = err.response?.data?.error_message || err.message || t('dashboard.userDetail.loadError')
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
    } catch (err: any) {
        resizeError.value = err.response?.data?.error_message || err.message || t('dashboard.userDetail.loadError')
    } finally {
        resizeLoading.value = false
    }
}

const openConsole = async (instance: Instance) => {
    // Open window immediately to avoid popup blockers
    const consoleWindow = window.open('about:blank', '_blank')
    if (!consoleWindow) {
        toast.error(t('dashboard.instanceDetail.popupBlocked'))
        return
    }

    try {
        const response = await instancesApi.getConsole(instance.id)
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
        toast.error(t('dashboard.instanceDetail.consoleError', { error: error.response?.data?.error_message || error.message }))
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
    } catch (error: any) {
        console.error('Failed to delete instance:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('dashboard.userDetail.loadError')
    } finally {
        deletingInstance.value = false
    }
}

// --- Create Modal Logic ---

const createModalVisible = ref(false)
const creatingInstance = ref(false)
const createError = ref('')
const resourcesLoading = ref(false)

const availableImages = ref<Image[]>([])
const availableFlavors = ref<any[]>([])
const availableVPCs = ref<VPC[]>([])
const availableSubnets = ref<Subnet[]>([])
const availableSecurityGroups = ref<SecurityGroup[]>([])
const availableKeys = ref<SSHKey[]>([])
const availableFloatingIps = ref<FloatingIP[]>([])
const availableZones = ref<Zone[]>([])
const availableHypers = ref<Hypervisor[]>([])
const sshKeysDropdownOpen = ref(false)

const tempPassword = ref('')
const confirmPassword = ref('')
const enablePassword = ref(false)
const showCreatePassword = ref(false)
const enableSSHKeys = ref(false)

const generateRandomPassword = () => {
    const charset = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%&*'
    const array = new Uint8Array(16)
    crypto.getRandomValues(array)
    const pwd = Array.from(array, b => charset[b % charset.length]).join('')
    tempPassword.value = pwd
    confirmPassword.value = pwd
    showCreatePassword.value = true
}

const isHostnameValid = computed(() => isValidName(newInstanceForm.value.hostname))

const hypersForZone = computed(() => {
    const zone = newInstanceForm.value.zone
    if (!zone) return availableHypers.value
    return availableHypers.value.filter(h => h.zone_name === zone)
})



interface InterfaceForm {
    network_type: 'private' | 'vpc' | 'public'
    vpc_id: string
    subnet_id: string
    public_ip_id: string
    ip_address: string
    security_group_ids: string[]
}

const newInstanceForm = ref({
    hostname: '',
    image_id: '',
    flavor_id: '',
    zone: 'default',
    keys: [] as string[],
    root_passwd: '',
    login_port: 22,
    count: 1,
    userdata_type: 'plain',
    userdata: '',
    nested_enable: false,
    hypervisor: null as string | null,
    primary_interface: {
        network_type: 'vpc' as const,
        vpc_id: '',
        subnet_id: '',
        public_ip_id: '',
        ip_address: '',
        security_group_ids: [] as string[]
    } as InterfaceForm,
    secondary_interfaces: [] as InterfaceForm[],
    advanced_expanded: false,
    general_expanded: true,
    primary_expanded: true
})

const addSecondaryInterface = () => {
    const primaryVpcId = newInstanceForm.value.primary_interface.network_type === 'vpc'
        ? newInstanceForm.value.primary_interface.vpc_id
        : ''
    const newIface: InterfaceForm = {
        network_type: 'vpc',
        vpc_id: primaryVpcId,
        subnet_id: '',
        public_ip_id: '',
        ip_address: '',
        security_group_ids: []
    }
    newInstanceForm.value.secondary_interfaces.push(newIface)
    autoSelectDefaultSGs(newIface)
}

const removeSecondaryInterface = (index: number) => {
    newInstanceForm.value.secondary_interfaces.splice(index, 1)
}

const openCreateModal = () => {
    newInstanceForm.value = {
        hostname: '',
        image_id: '',
        flavor_id: '',
        zone: 'default',
        keys: [],
        root_passwd: '',
        login_port: 22,
        count: 1,
        userdata_type: 'plain',
        userdata: '',
        nested_enable: false,
        hypervisor: null,
        primary_interface: {
            network_type: 'vpc',
            vpc_id: '',
            subnet_id: '',
            public_ip_id: '',
            ip_address: '',
            security_group_ids: []
        },
        secondary_interfaces: [],
        advanced_expanded: false,
        general_expanded: true,
        primary_expanded: true
    }
    createModalVisible.value = true
    fetchResources()
}

const closeCreateModal = () => {
    createModalVisible.value = false
    sshKeysDropdownOpen.value = false
    enablePassword.value = false
    enableSSHKeys.value = false
    showCreatePassword.value = false
    tempPassword.value = ''
    confirmPassword.value = ''
    createError.value = ''
}

const handlePasswordCheckboxChange = () => {
    if (!enablePassword.value) {
        newInstanceForm.value.root_passwd = ''
        tempPassword.value = ''
        confirmPassword.value = ''
    }
}

const handleSSHKeysCheckboxChange = () => {
    if (!enableSSHKeys.value) {
        newInstanceForm.value.keys = []
    }
}

watch([tempPassword, confirmPassword], ([newPass, newConfirm]) => {
    if (newPass && newPass === newConfirm) {
        newInstanceForm.value.root_passwd = newPass
    } else {
        newInstanceForm.value.root_passwd = ''
    }
})

const fetchResources = async () => {

    resourcesLoading.value = true
    subnetAddresses.value = {}
    try {
        const [imgsRes, vpcsRes, sgsRes, keysRes, subnetsRes, fipsRes, flavorsRes, zonesRes] = await Promise.all([

            imagesApi.fetchImages(),
            vpcsApi.list(),
            securityGroupsApi.list(),
            keysApi.fetchKeys(),
            subnetsApi.list(),
            floatingIpsApi.list(),
            flavorsApi.fetchFlavors(),
            zonesApi.fetchZones()
        ])


        availableImages.value = (imgsRes.data as any).images || []
        availableVPCs.value = vpcsRes.vpcs || []
        availableSecurityGroups.value = sgsRes.security_groups || []
        availableKeys.value = (keysRes.data as any).keys || []
        availableSubnets.value = subnetsRes.subnets || []
        availableFloatingIps.value = fipsRes.floating_ips || []
        
        availableFlavors.value = (flavorsRes.data as any).flavors || flavorsRes.data || []
        availableZones.value = (zonesRes.data as any).zones || zonesRes.data || []

        // Set default zone if available
        if (availableZones.value.length > 0) {
            newInstanceForm.value.zone = String(availableZones.value[0].name || availableZones.value[0].id)
        }

        if (isSystemAdmin.value) {
            try {
                const hypersRes = await hypervisorsApi.fetchHypervisors({ limit: 500 })
                availableHypers.value = hypersRes.data.hypers || []
            } catch (err) {
                console.error('Failed to fetch hypervisors:', err)
                availableHypers.value = []
            }
        } else {
            availableHypers.value = []
        }


        // Auto-select first VPC and its first subnet if available
        if (availableVPCs.value.length > 0) {
            newInstanceForm.value.primary_interface.vpc_id = availableVPCs.value[0].id
            const subnets = availableSubnets.value.filter(s => s.vpc?.id === availableVPCs.value[0].id)
            if (subnets.length > 0) {
                newInstanceForm.value.primary_interface.subnet_id = subnets[0].id
                fetchSubnetAddresses(subnets[0].id)
            }
            autoSelectDefaultSGs(newInstanceForm.value.primary_interface)
        }
    } catch (err) {
        console.error('Error fetching resources:', err)
    } finally {
        resourcesLoading.value = false
    }
}

const getFilteredSubnets = (vpcId: string) => {
    if (!vpcId) return []
    return availableSubnets.value.filter(s => s.vpc?.id === vpcId)
}

const getPrivateSubnets = () => {
    return availableSubnets.value.filter(s => s.type?.toLowerCase() === 'private')
}

const getPublicSubnets = () => {
    return availableSubnets.value.filter(s => s.type?.toLowerCase() === 'public')
}

const getUsedVlans = (excludeSecondaryIndex: number): Set<number> => {
    const usedVlans = new Set<number>()
    const primarySubnetId = newInstanceForm.value.primary_interface.subnet_id
    if (primarySubnetId) {
        const sub = availableSubnets.value.find(s => s.id === primarySubnetId)
        if (sub?.vlan) usedVlans.add(sub.vlan)
    }
    newInstanceForm.value.secondary_interfaces.forEach((iface, i) => {
        if (i === excludeSecondaryIndex) return
        if (iface.subnet_id) {
            const sub = availableSubnets.value.find(s => s.id === iface.subnet_id)
            if (sub?.vlan) usedVlans.add(sub.vlan)
        }
    })
    return usedVlans
}

const getFilteredSubnetsForSecondary = (vpcId: string, excludeIndex: number) => {
    if (!vpcId) return []
    const usedVlans = getUsedVlans(excludeIndex)
    return availableSubnets.value.filter(s =>
        s.vpc?.id === vpcId && (!s.vlan || !usedVlans.has(s.vlan))
    )
}

const getPrivateSubnetsForSecondary = (excludeIndex: number) => {
    const usedVlans = getUsedVlans(excludeIndex)
    return availableSubnets.value.filter(s =>
        s.type?.toLowerCase() === 'private' && (!s.vlan || !usedVlans.has(s.vlan))
    )
}

const getPublicSubnetsForSecondary = (excludeIndex: number) => {
    const usedVlans = getUsedVlans(excludeIndex)
    return availableSubnets.value.filter(s =>
        s.type?.toLowerCase() === 'public' && (!s.vlan || !usedVlans.has(s.vlan))
    )
}

const getAvailablePublicIps = () => {
    return availableFloatingIps.value.filter(fip => !fip.target_interface && !fip.interface)
}

const getFilteredSecurityGroups = (vpcId?: string) => {
    if (!vpcId) {
        // For private/public networks, show security groups not associated with any VPC
        return availableSecurityGroups.value.filter(sg => !sg.vpc || !sg.vpc.id)
    }
    return availableSecurityGroups.value.filter(sg => sg.vpc?.id === vpcId)
}

const handleNetworkTypeChange = (iface: InterfaceForm, isSecondary = false) => {
    iface.subnet_id = ''
    iface.public_ip_id = ''
    iface.ip_address = ''
    iface.security_group_ids = []
    if (isSecondary && iface.network_type === 'vpc') {
        iface.vpc_id = newInstanceForm.value.primary_interface.network_type === 'vpc'
            ? newInstanceForm.value.primary_interface.vpc_id
            : ''
    } else {
        iface.vpc_id = ''
    }
    autoSelectDefaultSGs(iface)
}

const subnetAddresses = ref<Record<string, any[]>>({})
const addressesLoading = ref<Record<string, boolean>>({})

const fetchSubnetAddresses = async (subnetId: string) => {
    if (!subnetId) return
    // Always fetch fresh addresses when requested to avoid empty cache issues
    addressesLoading.value[subnetId] = true
    try {
        const response = await subnetsApi.listAddresses(subnetId)
        // Filter for available IPs
        subnetAddresses.value[subnetId] = (response.addresses || []).filter(a => !a.allocated && !a.reserved)
    } catch (err) {
        console.error('Failed to fetch subnet addresses:', err)
    } finally {
        addressesLoading.value[subnetId] = false
    }
}

const handleSubnetChange = (iface: InterfaceForm) => {
    iface.ip_address = ''
    if (iface.subnet_id) {
        fetchSubnetAddresses(iface.subnet_id)
    }
}

const handleVpcChange = (iface: InterfaceForm) => {
    iface.subnet_id = ''
    iface.security_group_ids = []
    autoSelectDefaultSGs(iface)
}

watch(
    () => newInstanceForm.value.primary_interface.vpc_id,
    (newVpcId) => {
        for (const iface of newInstanceForm.value.secondary_interfaces) {
            if (iface.network_type === 'vpc') {
                iface.vpc_id = newVpcId
                iface.subnet_id = ''
                iface.ip_address = ''
                iface.security_group_ids = []
                autoSelectDefaultSGs(iface)
            }
        }
    }
)

watch(
    () => newInstanceForm.value.primary_interface.network_type,
    (newType) => {
        if (newType === 'vpc') {
            for (const iface of newInstanceForm.value.secondary_interfaces) {
                if (iface.network_type !== 'vpc') {
                    iface.network_type = 'vpc'
                    iface.vpc_id = newInstanceForm.value.primary_interface.vpc_id
                    iface.subnet_id = ''
                    iface.ip_address = ''
                    iface.security_group_ids = []
                    autoSelectDefaultSGs(iface)
                }
            }
        }
    }
)

watch(
    () => newInstanceForm.value.zone,
    () => {
        const current = newInstanceForm.value.hypervisor
        if (current != null && !hypersForZone.value.some(h => h.uuid === current)) {
            newInstanceForm.value.hypervisor = null
        }
    }
)

const autoSelectDefaultSGs = (iface: InterfaceForm) => {
    const vpcId = iface.network_type === 'vpc' ? iface.vpc_id : undefined
    const filtered = getFilteredSecurityGroups(vpcId)
    iface.security_group_ids = filtered
        .filter(sg => {
            const name = (sg.name || '').toLowerCase()
            return name.includes('native') || name.includes('default')
        })
        .map(sg => sg.id)
}

const handleCreateInstance = async () => {
    const form = newInstanceForm.value
    createError.value = ''
    if (!form.hostname || !form.image_id || !form.flavor_id) {
        createError.value = t('dashboard.overview.imageActions.fillRequired')
        return
    }

    if (!isHostnameValid.value) {
        createError.value = t('dashboard.instanceDetail.invalidHostname')
        return
    }

    // Note: password is optional - if not provided, the backend generates a random one

    // Validation for primary interface
    const pi = form.primary_interface
    if (pi.network_type === 'vpc' && (!pi.vpc_id || !pi.subnet_id)) {
        createError.value = t('dashboard.instanceDetail.primaryVpcSubnetRequired')
        return
    }
    if (pi.network_type === 'private' && !pi.subnet_id) {
        createError.value = t('dashboard.instanceDetail.isolatedSubnetRequired')
        return
    }
    if (pi.network_type === 'public' && !pi.public_ip_id && !pi.subnet_id) {
        createError.value = t('dashboard.instanceDetail.publicSubnetRequired')
        return
    }

    // Validation for secondary interfaces
    for (let i = 0; i < form.secondary_interfaces.length; i++) {
        const si = form.secondary_interfaces[i]
        if (si.network_type === 'public' && !si.public_ip_id && !si.subnet_id) {
            createError.value = t('dashboard.instanceDetail.secondaryPublicSubnetRequired', { index: i + 1 })
            return
        }
        if (si.network_type === 'vpc' && (!si.vpc_id || !si.subnet_id)) {
            createError.value = t('dashboard.instanceDetail.secondaryVpcSubnetRequired', { index: i + 1 })
            return
        }
    }

    // Check for VLAN conflicts across all interfaces
    const allSubnetIds = [
        form.primary_interface.subnet_id,
        ...form.secondary_interfaces.map(si => si.subnet_id)
    ].filter(Boolean)
    const vlans = allSubnetIds.map(id => availableSubnets.value.find(s => s.id === id)?.vlan).filter(Boolean)
    if (vlans.length !== new Set(vlans).size) {
        createError.value = t('dashboard.instanceDetail.vlanConflict')
        return
    }

    creatingInstance.value = true
    try {
        const mapInterface = (iface: InterfaceForm) => {
            const payload: any = {
                security_groups: iface.security_group_ids.map(id => ({ id }))
            }
            if (iface.network_type === 'public' && iface.public_ip_id) {
                payload.public_addresses = [{ id: iface.public_ip_id }]
            } else {
                if (iface.subnet_id) {
                    payload.subnet = { id: iface.subnet_id }
                }
                if (iface.ip_address) {
                    payload.ip_address = iface.ip_address.split('/')[0]
                }
            }
            return payload
        }

        const payload: any = {
            hostname: form.hostname,
            image: { id: form.image_id },
            flavor: form.flavor_id,
            zone: form.zone,
            count: form.count,
            primary_interface: mapInterface(form.primary_interface),
            secondary_interfaces: form.secondary_interfaces.map(mapInterface),
            nested_enable: form.nested_enable,
            userdata_type: form.userdata_type,
            userdata: form.userdata,
            login_port: form.login_port,
            keys: form.keys.map(id => ({ id }))
        }

        // Add VPC reference if primary interface is VPC type
        if (form.primary_interface.network_type === 'vpc' && form.primary_interface.vpc_id) {
            payload.vpc = { id: form.primary_interface.vpc_id }
        }

        if (form.root_passwd) {
            payload.root_passwd = form.root_passwd
        }

        if (isSystemAdmin.value && form.hypervisor != null) {
            payload.hypervisor = form.hypervisor
        }

        await instancesApi.createInstance(payload)
        await fetchInstances()
        closeCreateModal()
        toast.success(t('dashboard.instanceDetail.actionSuccess', { action: t('dashboard.buttons.createInstance') }))
    } catch (err: any) {
        console.error('Failed to create instance:', err)
        const detail = err.response?.data?.detail
        if (detail?.error === 'quota_exceeded') {
            createError.value = t('dashboard.instanceDetail.quotaExceeded', {
                resource: detail.resource,
                region: detail.region,
                requested: detail.requested,
                available: detail.available,
                limit: detail.limit
            })
        } else {
            createError.value = err.response?.data?.error_message || detail?.message || err.message || t('dashboard.floatingIPDetail.loadError')
        }
    } finally {
        creatingInstance.value = false
    }
}


onMounted(() => {
    if (region.currentRegionId) {
        fetchInstances()
        metricsTimer = setInterval(fetchUsageMetrics, 30000) // update every 30s
    }
})

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchInstances()
        if (metricsTimer) clearInterval(metricsTimer)
        metricsTimer = setInterval(fetchUsageMetrics, 30000)
    }
})

onUnmounted(() => {
    if (metricsTimer) clearInterval(metricsTimer)
})
</script>

<template>
  <div>
    <!-- Toast Notification removed (using global ToastContainer) -->

    <div class="page-header">
      <div class="search-wrapper">
        <label class="search-box">
          <Search :size="16" class="search-icon" />
          <input 
            id="searchQuery"
            name="searchQuery"
            type="text" 
            v-model="searchQuery"
            :placeholder="t('marketplace.searchPlaceholder')" 
            class="search-input"
          />
        </label>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchInstances()" :title="t('dashboard.regions')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ t('dashboard.buttons.createInstance') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('dashboard.table.nameId') }}</th>
            <th>{{ t('dashboard.table.flavor') }}</th>
            <th>{{ t('dashboard.table.image') }}</th>
            <th>{{ t('dashboard.table.ipAddress') }}</th>
            <th>{{ t('dashboard.table.status') }}</th>
            <th>{{ t('dashboard.overview.resourceUsage') }}</th>
            <th>{{ t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredInstances.length === 0">
            <td colspan="7" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                   <p>{{ t('dashboard.table.noResults') }}</p>
               </div>
               <div v-else>
                  <Monitor :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                   <p class="text-secondary">{{ t('dashboard.table.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="instance in filteredInstances" :key="instance.id" :class="{'active-row': activeActionMenuId === instance.id}">
            <td>
              <router-link :to="{ name: 'instance-detail', params: { id: instance.id } }" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon">
                    <Monitor :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ instance.hostname }}</div>
                    <div class="resource-id-row">
                      <span class="resource-id" :title="instance.id">{{ instance.id.slice(0, 8) }}...</span>
                      <button class="copy-btn-mini" @click.stop.prevent="copyId(instance.id)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                        <Check v-if="copiedId === instance.id" :size="10" style="color: #10b981;" />
                        <Copy v-else :size="10" />
                      </button>
                    </div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <div class="specs-display">
                <div class="specs-main">{{ instance.cpu }}C / {{ formatMemory(instance.memory).replace(' GB', 'G').replace(' MB', 'M') }}</div>
                <div v-if="instance.flavor" class="specs-sub">
                  {{ typeof instance.flavor === 'string' ? instance.flavor : instance.flavor.name }}
                </div>
              </div>
            </td>
            <td>{{ instance.image?.name || '-' }}</td>
            <td @mouseleave="closeIpPopover">
              <div class="ip-display-wrapper">
                <!-- Show first 2 interfaces inline -->
                <div class="ip-list">
                  <div v-for="(iface, idx) in (instance.interfaces || []).slice(0, 2)" :key="idx" class="ip-item">
                    <code class="ip-address">{{ iface.ip_address?.split('/')[0] || '-' }}</code>
                    <span v-if="iface.floating_ips?.find((f: any) => f.type?.toLowerCase() !== 'native')?.fip_address" class="fip-inline">
                      <Globe :size="10" />
                      <code>{{ iface.floating_ips.find((f: any) => f.type?.toLowerCase() !== 'native').fip_address.split('/')[0] }}</code>
                    </span>
                  </div>
                </div>

                <!-- +more badge & Popover if > 2 interfaces -->
                <div v-if="instance.interfaces && instance.interfaces.length > 2" class="more-ips-trigger">
                  <button
                    class="badge badge-multi-iface clickable"
                    @mouseenter="activeIpPopoverId = instance.id"
                  >
                    <Network :size="10" />+{{ instance.interfaces.length - 2 }} {{ t('dashboard.instanceDetail.more').toLowerCase() }}
                  </button>

                  <Transition name="fade">
                    <div v-if="activeIpPopoverId === instance.id" class="ip-popover card shadow-lg" @click.stop>
                      <div class="popover-header">
                        <Activity :size="12" /> {{ t('dashboard.instanceDetail.interfaceDetails') }} ({{ instance.interfaces.length }})
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
                            <td><span class="text-xs font-mono">eth{{ idx }}</span></td>
                            <td><span class="text-xs text-secondary">{{ iface.subnet?.name || (iface.subnet?.id?.substring(0,8) + '...') || '-' }}</span></td>
                            <td><code class="text-xs">{{ iface.ip_address?.split('/')[0] || '-' }}</code></td>
                            <td>
                              <code v-if="iface.floating_ips && iface.floating_ips.length > 0" class="text-xs text-primary">
                                {{ iface.floating_ips.find((f: any) => f.type?.toLowerCase() !== 'native')?.fip_address?.split('/')[0] || '-' }}
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
            </td>
            <td>
              <span :class="['badge', getStatusClass(instance.status)]">
                {{ getStatusText(instance.status) }}
              </span>
            </td>
            <td>
               <div class="instance-usage-summary" v-if="['running', 'active'].includes(instance.status?.toLowerCase())">
                 <div class="usage-mini-item" :title="'CPU: ' + (instanceMetrics[instance.id]?.cpu || 0).toFixed(1) + '%'">
                   <span class="usage-label">CPU</span>
                   <div class="usage-progress-bg">
                     <div class="usage-progress-bar cpu-bar" :style="{ width: (instanceMetrics[instance.id]?.cpu || 0) + '%' }"></div>
                   </div>
                 </div>
                 <div class="usage-mini-item" :title="'MEM: ' + (instanceMetrics[instance.id]?.memory || 0).toFixed(1) + '%'">
                   <span class="usage-label">MEM</span>
                   <div class="usage-progress-bg">
                     <div class="usage-progress-bar mem-bar" :style="{ width: (instanceMetrics[instance.id]?.memory || 0) + '%' }"></div>
                   </div>
                 </div>
               </div>
               <span v-else class="text-secondary text-xs">-</span>
            </td>
            <td class="actions-usage-cell">
               <div class="actions">
                 <div class="action-dropdown" style="position: relative;">
                    <button class="btn btn-ghost btn-sm" @click.stop="toggleActionMenu(instance.id)" :title="t('dashboard.instanceDetail.more')">
                        <MoreVertical :size="14" />
                    </button>
                    <Transition name="dropdown">
                        <div v-if="activeActionMenuId === instance.id" class="dropdown-menu dropdown-menu-right" @click.stop v-click-outside="closeActionMenu">
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
                                :disabled="!!actionLoading[instance.id] || !['running', 'active'].includes(instance.status?.toLowerCase())"
                            >
                                <RotateCw :size="14" /> {{ t('dashboard.instanceDetail.restart') }}
                            </button>
                            
                            <button class="dropdown-item" @click="openConsole(instance)">
                                <Terminal :size="14" /> {{ t('dashboard.instanceDetail.console') }}
                            </button>

                            <div class="dropdown-divider"></div>
                            
                            <button
                                class="dropdown-item"
                                @click="handleAction(instance, 'hard_stop')"
                                :disabled="!!actionLoading[instance.id] || ['stopped', 'shutoff', 'shut_off', 'paused'].includes(instance.status?.toLowerCase())"
                            >
                                <Square :size="14" /> {{ t('dashboard.instanceDetail.hardStop') }}
                            </button>
                            <button
                                class="dropdown-item"
                                @click="handleAction(instance, 'hard_restart')"
                                :disabled="!!actionLoading[instance.id] || ['stopped', 'shutoff', 'shut_off'].includes(instance.status?.toLowerCase())"
                            >
                                <RefreshCw :size="14" /> {{ t('dashboard.instanceDetail.hardRestart') }}
                            </button>
                            <button
                                v-if="instance.status?.toLowerCase() !== 'paused'"
                                class="dropdown-item"
                                @click="handleAction(instance, 'pause')"
                                :disabled="!!actionLoading[instance.id] || instance.status?.toLowerCase() !== 'running'"
                            >
                                <span style="font-size: 14px; width: 14px; display: inline-block; text-align: center;">⏸</span> {{ t('dashboard.instanceDetail.pause') }}
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
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Instance Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card" style="max-width: 600px;">
        <div class="modal-header">
          <h3>{{ t('dashboard.buttons.createInstance') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body" v-if="resourcesLoading">
            <div class="loading-spinner" style="margin: 40px auto;"></div>
        </div>

        <div class="modal-body scrollable-options" v-else>
          <!-- Basic Info -->
          <div class="form-section">
            <div class="section-header collapsible-header" @click="newInstanceForm.general_expanded = !newInstanceForm.general_expanded">
              <div class="section-title mb-0">{{ t('dashboard.instanceDetail.generalInfo') }}</div>
              <ChevronDown v-if="!newInstanceForm.general_expanded" :size="18" />
              <ChevronUp v-else :size="18" />
            </div>

            <div v-if="newInstanceForm.general_expanded" class="section-content mt-3">
              <div class="form-group">
                  <label class="form-label" for="hostname">{{ t('dashboard.table.hostname') }} <span class="text-error">*</span></label>
                   <input 
                       id="hostname"
                       name="hostname"
                       v-model="newInstanceForm.hostname" 
                       type="text" 
                       :class="['form-input', { 'input-error': !isHostnameValid }]"
                       :placeholder="t('dashboard.forms.placeholder.nameExample')" 
                   />
                   <div v-if="!isHostnameValid" class="text-error text-xs mt-1">
                       {{ t('dashboard.instanceDetail.invalidHostname') }}
                   </div>

              </div>

              <div class="form-row">
                  <div class="form-group">
                      <label class="form-label" for="image_id">{{ t('dashboard.forms.image') }} <span class="text-error">*</span></label>
                      <select id="image_id" name="image_id" v-model="newInstanceForm.image_id" class="form-select">
                          <option value="" disabled>{{ t('dashboard.forms.placeholder.selectImage') }}</option>
                          <option v-for="img in availableImages" :key="img.id" :value="img.id">
                              {{ img.name }}
                          </option>
                      </select>
                  </div>
                  <div class="form-group">
                      <label class="form-label" for="flavor_id">{{ t('dashboard.forms.flavor') }} <span class="text-error">*</span></label>
                      <select id="flavor_id" name="flavor_id" v-model="newInstanceForm.flavor_id" class="form-select">
                          <option value="" disabled>{{ t('dashboard.forms.placeholder.selectFlavor') }}</option>
                          <option v-for="f in availableFlavors" :key="f.name || f.id" :value="f.name || f.id">
                              {{ f.name }} ({{ f.vcpus || f.cpu || 0 }} vCPU, {{ formatMemory(f.ram || f.memory || 0) }} RAM, {{ f.disk || 0 }} GB Disk)
                          </option>
                      </select>
                  </div>
              </div>

              <div class="form-row">
                  <div class="form-group">
                       <label class="form-label">{{ t('dashboard.forms.zone') }}</label>
                       <select id="zone" name="zone" v-model="newInstanceForm.zone" class="form-select">
                           <option v-for="z in availableZones" :key="z.id" :value="z.name || z.id">
                               {{ z.name || z.id }}
                           </option>
                           <option v-if="availableZones.length === 0" value="default">{{ t('dashboard.forms.placeholder.none') }}</option>
                       </select>

                  </div>
                  <div class="form-group">
                       <label class="form-label">{{ t('dashboard.instanceDetail.count') }}</label>
                      <input id="instanceCount" name="instanceCount" v-model.number="newInstanceForm.count" type="number" class="form-input" min="1" max="16" />
                  </div>
              </div>

              <div class="form-group">
                  <div class="checkbox-label" style="cursor: default; width: fit-content;">
                      <input type="checkbox" v-model="enableSSHKeys" @change="handleSSHKeysCheckboxChange" style="cursor: pointer;">
                     <span class="form-label mb-0" style="cursor: default;">{{ t('dashboard.instanceDetail.sshLogin') }} <span class="text-error" v-if="!newInstanceForm.root_passwd">*</span></span>
                </div>
                  
                  <div v-if="enableSSHKeys" class="multi-select-container mt-2" v-click-outside="() => sshKeysDropdownOpen = false">
                      <div class="multi-select-trigger" @click="sshKeysDropdownOpen = !sshKeysDropdownOpen">
                           <span v-if="newInstanceForm.keys.length === 0" class="placeholder">{{ t('dashboard.forms.placeholder.selectSSHKey') }}</span>
                          <span v-else class="selected-count">{{ t('dashboard.instanceDetail.selectedCount', { count: newInstanceForm.keys.length }) }}</span>
                          <ChevronDown :size="16" />
                      </div>
                      <div v-if="sshKeysDropdownOpen" class="multi-select-dropdown">
                          <div class="checkbox-group compact no-border">
                              <label v-for="k in availableKeys" :key="k.id" class="checkbox-label p-2 hover-bg">
                                  <input type="checkbox" :value="k.id" v-model="newInstanceForm.keys">
                                  <span>{{ k.name }}</span>
                              </label>
                               <div v-if="availableKeys.length === 0" class="p-2 text-secondary text-xs">{{ t('dashboard.table.noData') }}</div>
                          </div>
                      </div>
                  </div>
              </div>

              <div class="form-group">
                  <div class="checkbox-label" style="cursor: default; width: fit-content;">
                      <input type="checkbox" v-model="enablePassword" @change="handlePasswordCheckboxChange" style="cursor: pointer;">
                     <span class="form-label mb-0" style="cursor: default;">{{ t('dashboard.instanceDetail.setPassword') }} <span class="text-error" v-if="newInstanceForm.keys.length === 0">*</span></span>
                </div>
                  
                  <div v-if="enablePassword" class="password-inline-fields mt-3">
                      <div style="margin-bottom: 8px;">
                          <button type="button" class="btn btn-sm btn-outline" @click="generateRandomPassword">
                              <Shuffle :size="14" style="margin-right: 4px;" />
                              {{ t('dashboard.instanceDetail.generatePassword') }}
                          </button>
                      </div>
                      <div class="form-row">
                          <div class="form-col">
                               <label class="form-label text-xs">{{ t('dashboard.instanceDetail.rootPassword') }}</label>
                               <div class="password-input-wrapper">
                                  <input id="rootPassword" name="rootPassword" v-model="tempPassword" :type="showCreatePassword ? 'text' : 'password'" class="form-input" :placeholder="t('dashboard.instanceDetail.rootPassword')" autocomplete="new-password" />
                                  <button type="button" class="password-toggle-btn" @click="showCreatePassword = !showCreatePassword">
                                      <Eye v-if="!showCreatePassword" :size="14" />
                                      <EyeOff v-else :size="14" />
                                  </button>
                               </div>
                          </div>
                          <div class="form-col">
                               <label class="form-label text-xs">{{ t('dashboard.instanceDetail.confirmPassword') }}</label>
                               <div class="password-input-wrapper">
                                  <input id="confirmPassword" name="confirmPassword" v-model="confirmPassword" :type="showCreatePassword ? 'text' : 'password'" class="form-input" :placeholder="t('dashboard.instanceDetail.confirmPassword')" autocomplete="new-password" />
                                  <button type="button" class="password-toggle-btn" @click="showCreatePassword = !showCreatePassword">
                                      <Eye v-if="!showCreatePassword" :size="14" />
                                      <EyeOff v-else :size="14" />
                                  </button>
                               </div>
                          </div>
                      </div>
                      <div v-if="tempPassword && confirmPassword && tempPassword !== confirmPassword" class="text-error text-xs mt-1">
                           {{ t('dashboard.instanceDetail.passwordMismatch') }}
                      </div>
                      <div v-else-if="tempPassword && confirmPassword === tempPassword" class="text-success text-xs mt-1">
                          {{ t('dashboard.instanceDetail.passwordSuccess') }}
                      </div>
                  </div>
              </div>

              <div class="form-group">
                   <label class="form-label">{{ t('dashboard.instanceDetail.networkType') }}</label>
                  <div class="radio-group">
                      <label class="radio-label">
                          <input type="radio" value="vpc" v-model="newInstanceForm.primary_interface.network_type" @change="handleNetworkTypeChange(newInstanceForm.primary_interface)">
                          <span>{{ t('dashboard.instanceDetail.networkTypes.vpc') }}</span>
                      </label>
                      <label class="radio-label">
                          <input type="radio" value="public" v-model="newInstanceForm.primary_interface.network_type" @change="handleNetworkTypeChange(newInstanceForm.primary_interface)">
                          <span>{{ t('dashboard.instanceDetail.networkTypes.public') }}</span>
                      </label>
                      <label class="radio-label">
                          <input type="radio" value="private" v-model="newInstanceForm.primary_interface.network_type" @change="handleNetworkTypeChange(newInstanceForm.primary_interface)">
                          <span>{{ t('dashboard.instanceDetail.networkTypes.private') }}</span>
                      </label>
                  </div>
              </div>

              <!-- VPC Type Subnets -->
              <div class="form-row three-col" v-if="newInstanceForm.primary_interface.network_type === 'vpc'">
                  <div class="form-group">
                       <label class="form-label">{{ t('dashboard.forms.vpc') }}</label>
                      <select v-model="newInstanceForm.primary_interface.vpc_id" class="form-select" @change="handleVpcChange(newInstanceForm.primary_interface)">
                           <option value="" disabled>{{ t('dashboard.forms.placeholder.selectVpc') }}</option>
                          <option v-for="vpc in availableVPCs" :key="vpc.id" :value="vpc.id">{{ vpc.name }}</option>
                      </select>
                  </div>
                  <div class="form-group">
                       <label class="form-label">{{ t('dashboard.forms.subnet') }}</label>
                      <select v-model="newInstanceForm.primary_interface.subnet_id" class="form-select" :disabled="!newInstanceForm.primary_interface.vpc_id" @change="handleSubnetChange(newInstanceForm.primary_interface)">
                           <option value="" disabled>{{ t('dashboard.forms.placeholder.selectSubnet') }}</option>
                          <option v-for="sub in getFilteredSubnets(newInstanceForm.primary_interface.vpc_id)" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ t('dashboard.forms.placeholder.availableIp') }}: {{ sub.available_count ?? 0 }}</option>
                      </select>
                  </div>
                  <div class="form-group">
                      <label class="form-label">{{ t('dashboard.table.ipAddress') }}</label>
                      <select v-model="newInstanceForm.primary_interface.ip_address" class="form-select" :disabled="!newInstanceForm.primary_interface.subnet_id || addressesLoading[newInstanceForm.primary_interface.subnet_id]">
                          <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }}</option>
                          <option v-for="addr in (subnetAddresses[newInstanceForm.primary_interface.subnet_id] || [])" :key="addr.address" :value="addr.address">{{ addr.address.split('/')[0] }}</option>
                      </select>
                  </div>
              </div>

               <!-- Public Network -->
              <div class="form-row" v-else-if="newInstanceForm.primary_interface.network_type === 'public'">
                  <div class="form-group">
                      <label class="form-label">{{ t('dashboard.forms.publicSubnet') }}</label>
                      <select v-model="newInstanceForm.primary_interface.subnet_id" class="form-select" @change="handleSubnetChange(newInstanceForm.primary_interface)">
                          <option value="" disabled>{{ t('dashboard.forms.placeholder.selectSubnet') }}</option>
                          <option v-for="sub in getPublicSubnets()" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ t('dashboard.forms.placeholder.availableIp') }}: {{ sub.available_count ?? 0 }}</option>
                      </select>
                  </div>
                  <div class="form-group">
                      <label class="form-label">{{ t('dashboard.table.ipAddress') }}</label>
                      <select v-model="newInstanceForm.primary_interface.ip_address" class="form-select" :disabled="!newInstanceForm.primary_interface.subnet_id || addressesLoading[newInstanceForm.primary_interface.subnet_id]">
                          <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }}</option>
                          <option v-for="addr in (subnetAddresses[newInstanceForm.primary_interface.subnet_id] || [])" :key="addr.address" :value="addr.address">{{ addr.address.split('/')[0] }}</option>
                      </select>
                  </div>
              </div>

              <!-- Private Subnets -->
              <div class="form-row" v-else-if="newInstanceForm.primary_interface.network_type === 'private'">
                  <div class="form-group">
                       <label class="form-label">{{ t('dashboard.forms.subnet') }}</label>
                      <select v-model="newInstanceForm.primary_interface.subnet_id" class="form-select" @change="handleSubnetChange(newInstanceForm.primary_interface)">
                          <option value="" disabled>{{ t('dashboard.forms.placeholder.selectSubnet') }}</option>
                          <option v-for="sub in getPrivateSubnets()" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ t('dashboard.forms.placeholder.availableIp') }}: {{ sub.available_count ?? 0 }}</option>
                      </select>
                  </div>
                  <div class="form-group">
                      <label class="form-label">{{ t('dashboard.table.ipAddress') }}</label>
                      <select v-model="newInstanceForm.primary_interface.ip_address" class="form-select" :disabled="!newInstanceForm.primary_interface.subnet_id || addressesLoading[newInstanceForm.primary_interface.subnet_id]">
                          <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }}</option>
                          <option v-for="addr in (subnetAddresses[newInstanceForm.primary_interface.subnet_id] || [])" :key="addr.address" :value="addr.address">{{ addr.address.split('/')[0] }}</option>
                      </select>
                  </div>
              </div>

              <div class="form-group">
                   <label class="form-label">{{ t('dashboard.forms.securityGroups') }} ({{ t('dashboard.forms.optional') }})</label>
                  <div class="checkbox-group compact">
                      <label v-for="sg in getFilteredSecurityGroups(newInstanceForm.primary_interface.network_type === 'vpc' ? newInstanceForm.primary_interface.vpc_id : undefined)" :key="sg.id" class="checkbox-label">
                          <input type="checkbox" :value="sg.id" v-model="newInstanceForm.primary_interface.security_group_ids">
                          <span>{{ sg.name }}</span>
                      </label>
                  </div>
                  <div v-if="getFilteredSecurityGroups(newInstanceForm.primary_interface.network_type === 'vpc' ? newInstanceForm.primary_interface.vpc_id : undefined).length === 0" class="text-secondary text-xs mt-1">
                       {{ t('dashboard.table.noResults') }}
                  </div>
              </div>

              <div class="secondary-interfaces-list" v-if="newInstanceForm.secondary_interfaces.length > 0">
                <div v-for="(iface, idx) in newInstanceForm.secondary_interfaces" :key="idx" class="form-section secondary-section">
                    <div class="section-header">
                        <div class="section-title" style="display:flex;align-items:center;gap:6px;">
                            {{ t('dashboard.instanceDetail.secondaryInterfaceLabel', { index: idx + 1 }) }}
                            <span class="iface-help-icon" @mouseenter="showIfaceHelp($event)" @mouseleave="hideIfaceHelp">
                                <HelpCircle :size="14" />
                            </span>
                        </div>
                        <button class="btn btn-ghost btn-sm text-error remove-btn" @click="removeSecondaryInterface(idx)">
                            <MinusCircle :size="16" />
                        </button>
                    </div>
                    
                    <div class="form-group">
                        <div class="radio-group mini">
                            <label class="radio-label">
                                <input type="radio" value="vpc" v-model="iface.network_type" @change="handleNetworkTypeChange(iface, true)">
                                <span>{{ t('dashboard.instanceDetail.networkTypes.vpc') }}</span>
                            </label>
                            <label class="radio-label" :class="{ 'opacity-40': newInstanceForm.primary_interface.network_type === 'vpc' }">
                                <input type="radio" value="public" v-model="iface.network_type" @change="handleNetworkTypeChange(iface, true)" :disabled="newInstanceForm.primary_interface.network_type === 'vpc'">
                                <span>{{ t('dashboard.instanceDetail.networkTypes.public') }}</span>
                            </label>
                            <label class="radio-label" :class="{ 'opacity-40': newInstanceForm.primary_interface.network_type === 'vpc' }">
                                <input type="radio" value="private" v-model="iface.network_type" @change="handleNetworkTypeChange(iface, true)" :disabled="newInstanceForm.primary_interface.network_type === 'vpc'">
                                <span>{{ t('dashboard.instanceDetail.networkTypes.private') }}</span>
                            </label>
                        </div>
                    </div>

                    <div class="form-row three-col" v-if="iface.network_type === 'vpc'">
                        <div class="form-group">
                            <select v-model="iface.vpc_id" class="form-select" :disabled="newInstanceForm.primary_interface.network_type === 'vpc'" :title="newInstanceForm.primary_interface.network_type === 'vpc' ? t('dashboard.instanceDetail.secondaryIface.vpcShare') : ''" @change="handleVpcChange(iface)">
                                <option value="" disabled>{{ t('dashboard.forms.placeholder.selectVpc') }}</option>
                                <option v-for="vpc in availableVPCs" :key="vpc.id" :value="vpc.id">{{ vpc.name }}</option>
                            </select>
                        </div>
                        <div class="form-group">
                            <select v-model="iface.subnet_id" class="form-select" :disabled="!iface.vpc_id" @change="handleSubnetChange(iface)">
                                <option value="" disabled>{{ t('dashboard.forms.placeholder.selectSubnet') }}</option>
                                <option v-for="sub in getFilteredSubnetsForSecondary(iface.vpc_id, idx)" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ t('dashboard.forms.placeholder.availableIp') }}: {{ sub.available_count ?? 0 }}</option>
                            </select>
                        </div>
                        <div class="form-group">
                            <select v-model="iface.ip_address" class="form-select" :disabled="!iface.subnet_id || addressesLoading[iface.subnet_id]">
                                <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }} IP</option>
                                <option v-for="addr in (subnetAddresses[iface.subnet_id] || [])" :key="addr.address" :value="addr.address">{{ addr.address.split('/')[0] }}</option>
                            </select>
                        </div>
                    </div>
                    <div class="form-row" v-else-if="iface.network_type === 'public'">
                        <div class="form-group">
                            <select v-model="iface.subnet_id" class="form-select" @change="handleSubnetChange(iface)">
                                <option value="" disabled>{{ t('dashboard.forms.placeholder.selectPublicSubnet') }}</option>
                                <option v-for="sub in getPublicSubnetsForSecondary(idx)" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ t('dashboard.forms.placeholder.availableIp') }}: {{ sub.available_count ?? 0 }}</option>
                            </select>
                        </div>
                        <div class="form-group">
                            <select v-model="iface.ip_address" class="form-select" :disabled="!iface.subnet_id || addressesLoading[iface.subnet_id]">
                                <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }} IP</option>
                                <option v-for="addr in (subnetAddresses[iface.subnet_id] || [])" :key="addr.address" :value="addr.address">{{ addr.address.split('/')[0] }}</option>
                            </select>
                        </div>
                    </div>

                    <div class="form-row" v-else-if="iface.network_type === 'private'">
                        <div class="form-group">
                            <select v-model="iface.subnet_id" class="form-select" @change="handleSubnetChange(iface)">
                                <option value="" disabled>{{ t('dashboard.forms.placeholder.selectSubnet') }}</option>
                                <option v-for="sub in getPrivateSubnetsForSecondary(idx)" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ t('dashboard.forms.placeholder.availableIp') }}: {{ sub.available_count ?? 0 }}</option>
                            </select>
                        </div>
                        <div class="form-group">
                            <select v-model="iface.ip_address" class="form-select" :disabled="!iface.subnet_id || addressesLoading[iface.subnet_id]">
                                <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }} IP</option>
                                <option v-for="addr in (subnetAddresses[iface.subnet_id] || [])" :key="addr.address" :value="addr.address">{{ addr.address.split('/')[0] }}</option>
                            </select>
                        </div>
                    </div>

                    <div class="form-group mt-2">
                        <label class="form-label text-xs">{{ t('dashboard.forms.securityGroups') }} ({{ t('dashboard.forms.optional') }})</label>
                        <div class="checkbox-group compact">
                            <label v-for="sg in getFilteredSecurityGroups(iface.network_type === 'vpc' ? iface.vpc_id : undefined)" :key="sg.id" class="checkbox-label">
                                <input type="checkbox" :value="sg.id" v-model="iface.security_group_ids">
                                <span>{{ sg.name }}</span>
                            </label>
                        </div>
                    </div>
                </div>
              </div>

              <div class="add-interface-row">
                <button class="btn btn-outline btn-sm w-full" @click="addSecondaryInterface" :disabled="newInstanceForm.secondary_interfaces.length >= 7">
                    <PlusCircle :size="14" /> {{ t('dashboard.buttons.addSecondaryInterface') }}
                </button>
              </div>
            </div>
          </div>

          <!-- Advanced Options Expandable -->
          <div class="advanced-section">
            <div class="advanced-trigger" @click="newInstanceForm.advanced_expanded = !newInstanceForm.advanced_expanded">
                <div class="trigger-label">
                    <Server :size="16" />
                    <span>{{ t('dashboard.instanceDetail.advancedOptions') }}</span>
                </div>
                <ChevronDown v-if="!newInstanceForm.advanced_expanded" :size="20" />
                <ChevronUp v-else :size="20" />
            </div>

            <div class="advanced-content" v-if="newInstanceForm.advanced_expanded">
                <div class="form-group" v-if="isSystemAdmin">
                    <label class="form-label">{{ t('dashboard.instanceDetail.pinHypervisor') }}</label>
                    <select id="pinHypervisor" name="pinHypervisor" v-model="newInstanceForm.hypervisor" class="form-select">
                        <option :value="null">{{ t('dashboard.instanceDetail.hypervisorAuto') }}</option>
                        <option v-for="h in hypersForZone" :key="h.uuid" :value="h.uuid">
                            {{ h.hostname }} ({{ h.status_name }})
                        </option>
                    </select>
                    <small class="form-hint">{{ t('dashboard.instanceDetail.hypervisorHint') }}</small>
                </div>

                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.instanceDetail.loginPort') }}</label>
                    <input id="loginPort" name="loginPort" v-model.number="newInstanceForm.login_port" type="number" class="form-input" :placeholder="t('dashboard.instanceDetail.loginPortPlaceholder')" />
                </div>

                <div class="form-group">
                    <div class="flex-row">
                        <label class="form-label mb-0">{{ t('dashboard.instanceDetail.nestedVirtualization') }}</label>
                        <label class="switch">
                            <input id="nestedVirtualization" name="nestedVirtualization" type="checkbox" v-model="newInstanceForm.nested_enable">
                            <span class="slider"></span>
                        </label>
                    </div>
                </div>

                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.instanceDetail.userDataType') }}</label>
                    <select id="userdataType" name="userdataType" v-model="newInstanceForm.userdata_type" class="form-select">
                        <option value="plain">{{ t('dashboard.instanceDetail.plain') }}</option>
                        <option value="base64">Base64</option>
                    </select>
                </div>

                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.instanceDetail.userData') }}</label>
                    <textarea id="userdata" name="userdata" v-model="newInstanceForm.userdata" class="form-textarea" rows="4" :placeholder="t('dashboard.instanceDetail.userDataPlaceholder')"></textarea>
                </div>
            </div>
          </div>
        </div>

        <div class="modal-footer" style="flex-direction: column; align-items: stretch; gap: var(--spacing-2);">
          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>
          <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2);">
            <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creatingInstance">{{ t('marketplace.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreateInstance" :disabled="creatingInstance">
              <span v-if="creatingInstance" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creatingInstance ? t('dashboard.overview.loadingOverview') : t('dashboard.buttons.createInstance') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Rename Modal -->
    <div v-if="renameModalVisible" class="modal-overlay" @click.self="renameModalVisible = false" style="z-index: 1001;">
      <div class="modal-content card" style="max-width: 400px;">
        <div class="modal-header">
          <h3>{{ t('dashboard.instanceDetail.rename') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="renameModalVisible = false">
            <X :size="20" />
          </button>
        </div>
        <div class="modal-body">
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
        </div>
        <div class="modal-footer">
          <button class="btn btn-ghost" @click="renameModalVisible = false">{{ t('dashboard.buttons.cancel') }}</button>
          <button class="btn btn-primary" @click="confirmRename" :disabled="renameLoading">
            <span v-if="renameLoading" class="loading-spinner small"></span>
            {{ t('dashboard.buttons.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Reset Password Modal -->
    <div v-if="resetPasswordModalVisible" class="modal-overlay" @click.self="resetPasswordModalVisible = false" style="z-index: 1001;">
      <div class="modal-content card" style="max-width: 450px;">
        <div class="modal-header">
          <h3>{{ t('dashboard.instanceDetail.resetPassword') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="resetPasswordModalVisible = false">
            <X :size="20" />
          </button>
        </div>
        <div class="modal-body">
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
              <button class="password-toggle" @click="showResetPassword = !showResetPassword">
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
            <button class="btn btn-ghost btn-sm text-primary" @click="generateRandomResetPassword">
              <RefreshCw :size="14" /> {{ t('dashboard.instanceDetail.generatePassword') }}
            </button>
          </div>
          <div v-if="resetPasswordError" class="text-error mt-3">{{ resetPasswordError }}</div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-ghost" @click="resetPasswordModalVisible = false">{{ t('dashboard.buttons.cancel') }}</button>
          <button class="btn btn-primary" @click="confirmResetPassword" :disabled="resetPasswordLoading">
            <span v-if="resetPasswordLoading" class="loading-spinner small"></span>
            {{ t('dashboard.buttons.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Resize Modal -->
    <div v-if="resizeModalVisible" class="modal-overlay" @click.self="resizeModalVisible = false" style="z-index: 1001;">
      <div class="modal-content card" style="max-width: 450px;">
        <div class="modal-header">
          <h3>{{ t('dashboard.instanceDetail.resize') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="resizeModalVisible = false">
            <X :size="20" />
          </button>
        </div>
        <div class="modal-body">
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
        </div>
        <div class="modal-footer">
          <button class="btn btn-ghost" @click="resizeModalVisible = false">{{ t('dashboard.buttons.cancel') }}</button>
          <button class="btn btn-primary" @click="confirmResize" :disabled="resizeLoading">
            <span v-if="resizeLoading" class="loading-spinner small"></span>
            {{ t('dashboard.buttons.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="ifaceTooltipVisible" class="iface-help-tooltip"
           :style="{ position: 'fixed', bottom: ifaceTooltipPos.bottom, right: ifaceTooltipPos.right, zIndex: '9999' }">
          <div class="iface-help-title">
              <HelpCircle :size="14" />
              {{ t('dashboard.instanceDetail.secondaryIface.title') }}
          </div>
          <ul class="iface-help-list">
              <li>{{ t('dashboard.instanceDetail.secondaryIface.limit') }}</li>
              <li>{{ t('dashboard.instanceDetail.secondaryIface.vlan') }}</li>
              <li>{{ t('dashboard.instanceDetail.secondaryIface.vpcLimit') }}</li>
              <li>{{ t('dashboard.instanceDetail.secondaryIface.publicLimit') }}</li>
              <li>{{ t('dashboard.instanceDetail.secondaryIface.publicVlan') }}</li>
              <li>{{ t('dashboard.instanceDetail.secondaryIface.siteLimit') }}</li>
          </ul>
      </div>
    </Teleport>

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
    z-index: 10000;
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
    color: var(--text-main);
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

.dropdown-enter-active, .dropdown-leave-active {
    transition: opacity 0.2s, transform 0.2s;
}

.dropdown-enter-from, .dropdown-leave-to {
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

.modal-footer {
    display: flex;
    justify-content: flex-end;
    gap: 12px;
    padding-top: 24px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0;
  padding-right: 20px;
}

.search-wrapper {
  flex: 1;
  max-width: 400px;
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

.table-card {
  padding: 0;
  overflow: visible;
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
  color: #fff;
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
  z-index: 100;
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

.mini-data-table {
  width: 100%;
  border-collapse: collapse;
}

.mini-data-table th {
  text-align: left;
  padding: 6px 12px;
  font-size: 10px;
  color: var(--gray-500);
  background: var(--bg-secondary);
  border-bottom: 1px solid var(--border-light);
}

.mini-data-table td {
  padding: 6px 12px;
  border-bottom: 1px solid var(--border-light);
}

.mini-data-table tr:last-child td {
  border-bottom: none;
}

.fade-enter-active, .fade-leave-active {
  transition: opacity 0.2s, transform 0.2s;
}

.fade-enter-from, .fade-leave-to {
  opacity: 0;
  transform: translateY(-5px);
}



.actions {
  display: flex;
  gap: var(--spacing-02);
}

.text-error {
  color: var(--error-color);
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

.form-input, .form-select {
    width: 100%;
    padding: 8px 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
}

.form-select {
    appearance: none;
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='%23888' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpolyline points='6 9 12 15 18 9'%3E%3C/polyline%3E%3C/svg%3E");
    background-repeat: no-repeat;
    background-position: right 12px center;
    padding-right: 36px;
}

.multiple-select {
    height: 100px;
    background-image: none;
    padding-right: 12px;
}

.multi-select-container {
    position: relative;
    width: 100%;
}

.multi-select-trigger {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 8px 12px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    cursor: pointer;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
}

.multi-select-trigger .placeholder {
    color: var(--text-tertiary);
}

.multi-select-dropdown {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    z-index: 10;
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-lg);
    max-height: 200px;
    overflow-y: auto;
}

.no-border { border: none !important; }
.p-2 { padding: 8px !important; }
.hover-bg:hover { background: var(--bg-secondary); }

.sub-modal { z-index: 1100; }
.password-modal { max-width: 400px; }

.password-input-wrapper {
    position: relative;
    display: flex;
    align-items: center;
}

.password-input-wrapper .form-input {
    padding-right: 32px;
}

.password-toggle-btn {
    position: absolute;
    right: 8px;
    background: none;
    border: none;
    padding: 2px;
    cursor: pointer;
    color: var(--text-light);
    display: inline-flex;
    align-items: center;
    transition: color 0.15s;
}

.password-toggle-btn:hover {
    color: var(--primary-color);
}
.text-success { color: var(--success-color); }
.btn-xs { padding: 2px 8px; font-size: 0.75rem; }
.justify-start { justify-content: flex-start !important; }
.gap-4 { gap: 16px; }

.form-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-4);
}

.form-row.three-col {
    grid-template-columns: repeat(3, 1fr);
}

.form-section {

    margin-bottom: 24px;
    padding: 16px;
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-lg);
}

.section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0px;
}

.iface-help-icon {
    position: relative;
    display: inline-flex;
    align-items: center;
    cursor: pointer;
    color: var(--text-secondary, #888);
    transition: all 0.2s;
}

.iface-help-icon:hover {
    z-index: 10000;
}

.iface-help-tooltip {
    position: fixed;
    z-index: 9999;
    width: max-content;
    max-width: 420px;
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-lg);
    padding: 16px;
    box-shadow: 0 10px 40px rgba(0, 0, 0, 0.15);
    font-size: 12px;
    font-weight: normal;
    color: var(--text-primary);
    line-height: 1.6;
    white-space: normal;
    text-align: left;
    animation: tooltipFadeIn 0.2s cubic-bezier(0.16, 1, 0.3, 1);
    transform-origin: bottom right;
}

@keyframes tooltipFadeIn {
    from { opacity: 0; transform: scale(0.95) translateY(10px); }
    to { opacity: 1; transform: scale(1) translateY(0); }
}

.iface-help-tooltip::before {
    content: '';
    position: absolute;
    right: 4px;
    bottom: -6px;
    top: auto;
    width: 12px;
    height: 12px;
    background: var(--bg-primary);
    border-right: 1px solid var(--border-light);
    border-bottom: 1px solid var(--border-light);
    transform: rotate(45deg);
}

.iface-help-title {
    font-weight: 700;
    font-size: 14px;
    margin-bottom: 12px;
    color: var(--primary-color);
    display: flex;
    align-items: center;
    gap: 8px;
    border-bottom: 1px solid var(--border-light);
    padding-bottom: 8px;
}

.iface-help-list {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 8px;
}

.iface-help-list li {
    position: relative;
    padding-left: 20px;
    color: var(--text-secondary);
}

.iface-help-list li::before {
    content: '';
    position: absolute;
    left: 4px;
    top: 7px;
    width: 6px;
    height: 2px;
    background: var(--primary-color);
    border-radius: 2px;
    opacity: 0.6;
}

.collapsible-header {
    cursor: pointer;
    transition: all 0.2s;
    user-select: none;
}

.collapsible-header:hover {
    opacity: 0.8;
}

.section-content {
    animation: fadeIn 0.25s ease-out;
}


.section-title {
    font-size: 0.9rem;
    font-weight: 600;
    color: var(--text-primary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-bottom: 12px;
}

.secondary-section {

    border-style: dashed;
    background: var(--bg-secondary);
}

.radio-group {
    display: flex;
    gap: 16px;
    margin-bottom: 16px;
}

.radio-group.mini {
    gap: 8px;
    margin-bottom: 8px;
}

.radio-label {
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    font-size: 0.875rem;
    color: var(--text-secondary);
}

.radio-label input[type="radio"] {
    accent-color: var(--primary-color);
}

.checkbox-group.compact {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 4px;
    padding: 6px;
}

.mt-2 { margin-top: 8px; }
.mb-0 { margin-bottom: 0 !important; }
.w-full { width: 100%; }

.form-col {
    display: flex;
    flex-direction: column;
}

.add-interface-row {
    margin: 16px 0 24px;
}


.advanced-section {
    border-top: 1px solid var(--border-light);
    padding-top: 16px;
}

.advanced-trigger {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px;
    background: var(--bg-secondary);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: all 0.2s;
}

.advanced-trigger:hover {
    background: var(--bg-tertiary);
}

.trigger-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 500;
    color: var(--text-primary);
}

.advanced-content {
    padding: 16px 12px;
    animation: fadeIn 0.25s ease-out;
}

.form-textarea {
    width: 100%;
    padding: 10px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
    color: var(--text-primary);
    font-family: var(--font-family-mono);
    font-size: 0.8rem;
    resize: vertical;
}

.flex-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
}

/* Switch styling */
.switch {
  position: relative;
  display: inline-block;
  width: 44px;
  height: 24px;
}

.switch input {
  opacity: 0;
  width: 0;
  height: 0;
}

.slider {
  position: absolute;
  cursor: pointer;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: var(--gray-300);
  transition: .4s;
  border-radius: 24px;
}

.slider:before {
  position: absolute;
  content: "";
  height: 18px;
  width: 18px;
  left: 3px;
  bottom: 3px;
  background-color: white;
  transition: .4s;
  border-radius: 50%;
}

input:checked + .slider {
  background-color: var(--primary-color);
}

input:focus + .slider {
  box-shadow: 0 0 1px var(--primary-color);
}

input:checked + .slider:before {
  transform: translateX(20px);
}

.btn-outline {
    background: transparent;
    border: 1px solid var(--border-light);
    color: var(--text-secondary);
}

.btn-outline:hover {
    background: var(--bg-secondary);
    border-color: var(--primary-color);
    color: var(--primary-color);
}

.text-xs { font-size: 0.75rem; }
.text-secondary { color: var(--text-light); }
.mt-1 { margin-top: 4px; }

.checkbox-group {

    display: flex;
    flex-direction: column;
    gap: 8px;
    max-height: 150px;
    overflow-y: auto;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    padding: 8px;
    background: var(--bg-secondary);
}

.checkbox-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: var(--font-size-sm);
    cursor: pointer;
}

.checkbox-label input[type="checkbox"] {
    accent-color: var(--primary-color);
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

.header-actions { display: flex; gap: 8px; align-items: center; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

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

.cpu-bar { background: var(--primary-color); }
.mem-bar { background: var(--accent-purple); }

.actions-usage-cell {
    text-align: right;
    white-space: nowrap;
}

.text-xs {
    font-size: 11px;
}
</style>



