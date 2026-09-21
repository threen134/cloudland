<script setup lang="ts">
// 创建虚拟机的弹窗。从 InstanceList.vue 拆出来：那个文件到 3156 行，其中 1300 行
// 都是这一个弹窗（表单状态、网卡与安全组的联动、可选资源的拉取、提交与校验），
// 和列表本身没有任何共享状态，边界很干净。
//
// 用法：<CreateInstanceModal :show="visible" @close="visible = false" @created="reload()" />
// 打开时自行拉取镜像 / 规格 / VPC / 子网 / 安全组 / 密钥 / 可用区 / 计算节点。
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronUp, PlusCircle, MinusCircle, Eye, EyeOff, Shuffle, HelpCircle } from 'lucide-vue-next'
import { instancesApi, type CreateInstancePayload, type InterfacePayload } from '../../api/instances'
import { imagesApi, type Image } from '../../api/images'
import {
    vpcsApi,
    subnetsApi,
    securityGroupsApi,
    floatingIpsApi,
    type VPC,
    type Subnet,
    type SecurityGroup,
    type FloatingIP,
} from '../../api/networks'
import { keysApi, type SSHKey } from '../../api/keys'
import { flavorsApi, type Flavor } from '../../api/flavors'
import { zonesApi, type Zone } from '../../api/zones'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { isValidName } from '../../utils/validation'
import { quotaErrorMessage } from '../../utils/quotaError'
import { errorMessage } from '../../utils/error'
import { formatMemory } from '../../utils/format'
import { useToast } from '../../composables/useToast'
import { useAuthStore } from '../../stores/auth'
import BaseModal from '../modals/BaseModal.vue'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; created: [] }>()

const { t, te } = useI18n()
const toast = useToast()
const authStore = useAuthStore()
const isSystemAdmin = computed(() => authStore.user?.role === 'admin' || authStore.user?.is_superuser === true)

const creatingInstance = ref(false)
const createError = ref('')
const resourcesLoading = ref(false)

// 网卡说明的悬浮提示：跟着图标定位，所以位置要自己算
const ifaceTooltipVisible = ref(false)
const ifaceTooltipPos = ref({ bottom: '0px', right: '0px' })

const showIfaceHelp = (event: MouseEvent) => {
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
    ifaceTooltipPos.value = {
        bottom: `${window.innerHeight - rect.top + 8}px`,
        right: `${window.innerWidth - rect.right}px`,
    }
    ifaceTooltipVisible.value = true
}

const hideIfaceHelp = () => {
    ifaceTooltipVisible.value = false
}

const availableImages = ref<Image[]>([])
const availableFlavors = ref<Flavor[]>([])
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
    const pwd = Array.from(array, (b) => charset[b % charset.length]).join('')
    tempPassword.value = pwd
    confirmPassword.value = pwd
    showCreatePassword.value = true
}

const isHostnameValid = computed(() => isValidName(newInstanceForm.value.hostname))

const hypersForZone = computed(() => {
    const zone = newInstanceForm.value.zone
    if (!zone) return availableHypers.value
    return availableHypers.value.filter((h) => h.zone_name === zone)
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
    // 留空时由后端按镜像类型决定（Linux 22、Windows 3389）
    login_port: '' as number | '',
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
        security_group_ids: [] as string[],
    } as InterfaceForm,
    secondary_interfaces: [] as InterfaceForm[],
    advanced_expanded: false,
    general_expanded: true,
    primary_expanded: true,
})

const addSecondaryInterface = () => {
    const primaryVpcId =
        newInstanceForm.value.primary_interface.network_type === 'vpc'
            ? newInstanceForm.value.primary_interface.vpc_id
            : ''
    const newIface: InterfaceForm = {
        network_type: 'vpc',
        vpc_id: primaryVpcId,
        subnet_id: '',
        public_ip_id: '',
        ip_address: '',
        security_group_ids: [],
    }
    newInstanceForm.value.secondary_interfaces.push(newIface)
    autoSelectDefaultSGs(newIface)
}

const removeSecondaryInterface = (index: number) => {
    newInstanceForm.value.secondary_interfaces.splice(index, 1)
}

// 打开时重置表单并拉取可选资源（父组件只负责把 show 置 true）
const resetForm = () => {
    newInstanceForm.value = {
        hostname: '',
        image_id: '',
        flavor_id: '',
        zone: 'default',
        keys: [],
        root_passwd: '',
        login_port: '',
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
            security_group_ids: [],
        },
        secondary_interfaces: [],
        advanced_expanded: false,
        general_expanded: true,
        primary_expanded: true,
    }
}

const closeCreateModal = () => {
    emit('close')
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
            zonesApi.fetchZones(),
        ])

        availableImages.value = imgsRes.images || []
        availableVPCs.value = vpcsRes.vpcs || []
        availableSecurityGroups.value = sgsRes.security_groups || []
        availableKeys.value = keysRes.keys || []
        availableSubnets.value = subnetsRes.subnets || []
        availableFloatingIps.value = fipsRes.floating_ips || []

        availableFlavors.value = flavorsRes.flavors || []
        availableZones.value = zonesRes.zones || []

        // Set default zone if available
        if (availableZones.value.length > 0) {
            newInstanceForm.value.zone = String(availableZones.value[0].name || availableZones.value[0].id)
        }

        if (isSystemAdmin.value) {
            try {
                const hypersRes = await hypervisorsApi.fetchHypervisors({ limit: 500 })
                availableHypers.value = hypersRes.hypers || []
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
            const subnets = availableSubnets.value.filter((s) => s.vpc?.id === availableVPCs.value[0].id)
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
    return availableSubnets.value.filter((s) => s.vpc?.id === vpcId)
}

const getPrivateSubnets = () => {
    return availableSubnets.value.filter((s) => s.type?.toLowerCase() === 'private')
}

const getPublicSubnets = () => {
    return availableSubnets.value.filter((s) => s.type?.toLowerCase() === 'public')
}

const getUsedVlans = (excludeSecondaryIndex: number): Set<number> => {
    const usedVlans = new Set<number>()
    const primarySubnetId = newInstanceForm.value.primary_interface.subnet_id
    if (primarySubnetId) {
        const sub = availableSubnets.value.find((s) => s.id === primarySubnetId)
        if (sub?.vlan) usedVlans.add(sub.vlan)
    }
    newInstanceForm.value.secondary_interfaces.forEach((iface, i) => {
        if (i === excludeSecondaryIndex) return
        if (iface.subnet_id) {
            const sub = availableSubnets.value.find((s) => s.id === iface.subnet_id)
            if (sub?.vlan) usedVlans.add(sub.vlan)
        }
    })
    return usedVlans
}

const getFilteredSubnetsForSecondary = (vpcId: string, excludeIndex: number) => {
    if (!vpcId) return []
    const usedVlans = getUsedVlans(excludeIndex)
    return availableSubnets.value.filter((s) => s.vpc?.id === vpcId && (!s.vlan || !usedVlans.has(s.vlan)))
}

const getPrivateSubnetsForSecondary = (excludeIndex: number) => {
    const usedVlans = getUsedVlans(excludeIndex)
    return availableSubnets.value.filter(
        (s) => s.type?.toLowerCase() === 'private' && (!s.vlan || !usedVlans.has(s.vlan))
    )
}

const getPublicSubnetsForSecondary = (excludeIndex: number) => {
    const usedVlans = getUsedVlans(excludeIndex)
    return availableSubnets.value.filter(
        (s) => s.type?.toLowerCase() === 'public' && (!s.vlan || !usedVlans.has(s.vlan))
    )
}

const getFilteredSecurityGroups = (vpcId?: string) => {
    if (!vpcId) {
        // For private/public networks, show security groups not associated with any VPC
        return availableSecurityGroups.value.filter((sg) => !sg.vpc || !sg.vpc.id)
    }
    return availableSecurityGroups.value.filter((sg) => sg.vpc?.id === vpcId)
}

const handleNetworkTypeChange = (iface: InterfaceForm, isSecondary = false) => {
    iface.subnet_id = ''
    iface.public_ip_id = ''
    iface.ip_address = ''
    iface.security_group_ids = []
    if (isSecondary && iface.network_type === 'vpc') {
        iface.vpc_id =
            newInstanceForm.value.primary_interface.network_type === 'vpc'
                ? newInstanceForm.value.primary_interface.vpc_id
                : ''
    } else {
        iface.vpc_id = ''
    }
    autoSelectDefaultSGs(iface)
}

// GET /addresses/:subnet 返回的地址项（api/src/apis/address.go 的 AddressResponse）。
// networks.ts 的 listAddresses 目前把 addresses 声明为 any[]，这里按实际用到的字段收窄
interface SubnetAddress {
    address: string
    allocated?: boolean
    reserved?: boolean
}

const subnetAddresses = ref<Record<string, SubnetAddress[]>>({})
const addressesLoading = ref<Record<string, boolean>>({})

const fetchSubnetAddresses = async (subnetId: string) => {
    if (!subnetId) return
    // Always fetch fresh addresses when requested to avoid empty cache issues
    addressesLoading.value[subnetId] = true
    try {
        const response = await subnetsApi.listAddresses(subnetId)
        // Filter for available IPs
        subnetAddresses.value[subnetId] = (response.addresses || []).filter((a) => !a.allocated && !a.reserved)
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
        if (current != null && !hypersForZone.value.some((h) => h.uuid === current)) {
            newInstanceForm.value.hypervisor = null
        }
    }
)

const autoSelectDefaultSGs = (iface: InterfaceForm) => {
    const vpcId = iface.network_type === 'vpc' ? iface.vpc_id : undefined
    const filtered = getFilteredSecurityGroups(vpcId)
    iface.security_group_ids = filtered
        .filter((sg) => {
            const name = (sg.name || '').toLowerCase()
            return name.includes('native') || name.includes('default')
        })
        .map((sg) => sg.id)
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
        ...form.secondary_interfaces.map((si) => si.subnet_id),
    ].filter(Boolean)
    const vlans = allSubnetIds.map((id) => availableSubnets.value.find((s) => s.id === id)?.vlan).filter(Boolean)
    if (vlans.length !== new Set(vlans).size) {
        createError.value = t('dashboard.instanceDetail.vlanConflict')
        return
    }

    creatingInstance.value = true
    try {
        const mapInterface = (iface: InterfaceForm): InterfacePayload => {
            const payload: InterfacePayload = {
                security_groups: iface.security_group_ids.map((id) => ({ id })),
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

        const payload: CreateInstancePayload = {
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
            // 留空不传，由后端按镜像类型决定（Linux 22、Windows 3389）
            login_port: form.login_port || undefined,
            keys: form.keys.map((id) => ({ id })),
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
        // 列表按创建时间倒序，新建的在第一页
        emit('created')
        closeCreateModal()
        toast.success(t('dashboard.instanceDetail.actionSuccess', { action: t('dashboard.buttons.createInstance') }))
    } catch (err) {
        console.error('Failed to create instance:', err)
        // detail 是对象时（cpgateway 的结构化错误）errorMessage 取不到，单独取一层 detail.message；
        // clapi 的 error_message 与 cpgateway 的 detail 不会同时出现，所以顺序不影响结果
        const detail = (err as { response?: { data?: { detail?: { message?: unknown } } } })?.response?.data?.detail
        const detailMessage = typeof detail?.message === 'string' ? detail.message : ''
        createError.value =
            quotaErrorMessage(err, t, te) ||
            detailMessage ||
            errorMessage(err, t('dashboard.floatingIPDetail.loadError'))
    } finally {
        creatingInstance.value = false
    }
}

// 每次打开都重来一遍：表单留着上次的内容比空表单更容易误操作
watch(
    () => props.show,
    (show) => {
        if (show) {
            resetForm()
            createError.value = ''
            fetchResources()
        }
    }
)
</script>

<template>
    <!-- Create Instance Modal -->
    <BaseModal
        :show="show"
        :title="t('dashboard.buttons.createInstance')"
        size="lg"
        :loading="creatingInstance"
        @close="closeCreateModal"
    >
        <div v-if="resourcesLoading">
            <div class="loading-spinner" style="margin: 40px auto"></div>
        </div>

        <div v-else>
            <!-- Basic Info -->
            <div class="form-section">
                <div
                    class="section-header collapsible-header"
                    @click="newInstanceForm.general_expanded = !newInstanceForm.general_expanded"
                >
                    <div class="section-title mb-0">{{ t('dashboard.instanceDetail.generalInfo') }}</div>
                    <ChevronDown v-if="!newInstanceForm.general_expanded" :size="18" />
                    <ChevronUp v-else :size="18" />
                </div>

                <div v-if="newInstanceForm.general_expanded" class="section-content mt-3">
                    <div class="form-group">
                        <label class="form-label" for="hostname"
                            >{{ t('dashboard.table.hostname') }} <span class="text-error">*</span></label
                        >
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
                            <label class="form-label" for="image_id"
                                >{{ t('dashboard.forms.image') }} <span class="text-error">*</span></label
                            >
                            <select
                                id="image_id"
                                name="image_id"
                                v-model="newInstanceForm.image_id"
                                class="form-select"
                            >
                                <option value="" disabled>
                                    {{ t('dashboard.forms.placeholder.selectImage') }}
                                </option>
                                <option v-for="img in availableImages" :key="img.id" :value="img.id">
                                    {{ img.name }}
                                </option>
                            </select>
                        </div>
                        <div class="form-group">
                            <label class="form-label" for="flavor_id"
                                >{{ t('dashboard.forms.flavor') }} <span class="text-error">*</span></label
                            >
                            <select
                                id="flavor_id"
                                name="flavor_id"
                                v-model="newInstanceForm.flavor_id"
                                class="form-select"
                            >
                                <option value="" disabled>
                                    {{ t('dashboard.forms.placeholder.selectFlavor') }}
                                </option>
                                <option v-for="f in availableFlavors" :key="f.name" :value="f.name">
                                    {{ f.name }} ({{ f.cpu }} vCPU,
                                    {{ formatMemory(f.memory) }} RAM, {{ f.disk || 0 }} GB
                                    {{ t('dashboard.table.disk') }})
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
                                <option v-if="availableZones.length === 0" value="default">
                                    {{ t('dashboard.forms.placeholder.none') }}
                                </option>
                            </select>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.instanceDetail.count') }}</label>
                            <input
                                id="instanceCount"
                                name="instanceCount"
                                v-model.number="newInstanceForm.count"
                                type="number"
                                class="form-input"
                                min="1"
                                max="16"
                            />
                        </div>
                    </div>

                    <div class="form-group">
                        <div class="checkbox-label" style="cursor: default; width: fit-content">
                            <input
                                type="checkbox"
                                v-model="enableSSHKeys"
                                @change="handleSSHKeysCheckboxChange"
                                style="cursor: pointer"
                            />
                            <span class="form-label mb-0" style="cursor: default"
                                >{{ t('dashboard.instanceDetail.sshLogin') }}
                                <span class="text-error" v-if="!newInstanceForm.root_passwd">*</span></span
                            >
                        </div>

                        <div
                            v-if="enableSSHKeys"
                            class="multi-select-container mt-2"
                            v-click-outside="() => (sshKeysDropdownOpen = false)"
                        >
                            <div class="multi-select-trigger" @click="sshKeysDropdownOpen = !sshKeysDropdownOpen">
                                <span v-if="newInstanceForm.keys.length === 0" class="placeholder">{{
                                    t('dashboard.forms.placeholder.selectSSHKey')
                                }}</span>
                                <span v-else class="selected-count">{{
                                    t('dashboard.instanceDetail.selectedCount', {
                                        count: newInstanceForm.keys.length,
                                    })
                                }}</span>
                                <ChevronDown :size="16" />
                            </div>
                            <div v-if="sshKeysDropdownOpen" class="multi-select-dropdown">
                                <div class="checkbox-group compact no-border">
                                    <label v-for="k in availableKeys" :key="k.id" class="checkbox-label p-2 hover-bg">
                                        <input type="checkbox" :value="k.id" v-model="newInstanceForm.keys" />
                                        <span>{{ k.name }}</span>
                                    </label>
                                    <div v-if="availableKeys.length === 0" class="p-2 text-secondary text-xs">
                                        {{ t('dashboard.table.noData') }}
                                    </div>
                                </div>
                            </div>
                        </div>
                    </div>

                    <div class="form-group">
                        <div class="checkbox-label" style="cursor: default; width: fit-content">
                            <input
                                type="checkbox"
                                v-model="enablePassword"
                                @change="handlePasswordCheckboxChange"
                                style="cursor: pointer"
                            />
                            <span class="form-label mb-0" style="cursor: default"
                                >{{ t('dashboard.instanceDetail.setPassword') }}
                                <span class="text-error" v-if="newInstanceForm.keys.length === 0">*</span></span
                            >
                        </div>

                        <div v-if="enablePassword" class="password-inline-fields mt-3">
                            <div style="margin-bottom: 8px">
                                <button type="button" class="btn btn-sm btn-outline" @click="generateRandomPassword">
                                    <Shuffle :size="14" style="margin-right: 4px" />
                                    {{ t('dashboard.instanceDetail.generatePassword') }}
                                </button>
                            </div>
                            <div class="form-row">
                                <div class="form-col">
                                    <label class="form-label text-xs">{{
                                        t('dashboard.instanceDetail.rootPassword')
                                    }}</label>
                                    <div class="password-input-wrapper">
                                        <input
                                            id="rootPassword"
                                            name="rootPassword"
                                            v-model="tempPassword"
                                            :type="showCreatePassword ? 'text' : 'password'"
                                            class="form-input"
                                            :placeholder="t('dashboard.instanceDetail.rootPassword')"
                                            autocomplete="new-password"
                                        />
                                        <button
                                            type="button"
                                            class="password-toggle-btn"
                                            @click="showCreatePassword = !showCreatePassword"
                                        >
                                            <Eye v-if="!showCreatePassword" :size="14" />
                                            <EyeOff v-else :size="14" />
                                        </button>
                                    </div>
                                </div>
                                <div class="form-col">
                                    <label class="form-label text-xs">{{
                                        t('dashboard.instanceDetail.confirmPassword')
                                    }}</label>
                                    <div class="password-input-wrapper">
                                        <input
                                            id="confirmPassword"
                                            name="confirmPassword"
                                            v-model="confirmPassword"
                                            :type="showCreatePassword ? 'text' : 'password'"
                                            class="form-input"
                                            :placeholder="t('dashboard.instanceDetail.confirmPassword')"
                                            autocomplete="new-password"
                                        />
                                        <button
                                            type="button"
                                            class="password-toggle-btn"
                                            @click="showCreatePassword = !showCreatePassword"
                                        >
                                            <Eye v-if="!showCreatePassword" :size="14" />
                                            <EyeOff v-else :size="14" />
                                        </button>
                                    </div>
                                </div>
                            </div>
                            <div
                                v-if="tempPassword && confirmPassword && tempPassword !== confirmPassword"
                                class="text-error text-xs mt-1"
                            >
                                {{ t('dashboard.instanceDetail.passwordMismatch') }}
                            </div>
                            <div
                                v-else-if="tempPassword && confirmPassword === tempPassword"
                                class="text-success text-xs mt-1"
                            >
                                {{ t('dashboard.instanceDetail.passwordSuccess') }}
                            </div>
                        </div>
                    </div>

                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.instanceDetail.networkType') }}</label>
                        <div class="radio-group">
                            <label class="radio-label">
                                <input
                                    type="radio"
                                    value="vpc"
                                    v-model="newInstanceForm.primary_interface.network_type"
                                    @change="handleNetworkTypeChange(newInstanceForm.primary_interface)"
                                />
                                <span>{{ t('dashboard.instanceDetail.networkTypes.vpc') }}</span>
                            </label>
                            <label class="radio-label">
                                <input
                                    type="radio"
                                    value="public"
                                    v-model="newInstanceForm.primary_interface.network_type"
                                    @change="handleNetworkTypeChange(newInstanceForm.primary_interface)"
                                />
                                <span>{{ t('dashboard.instanceDetail.networkTypes.public') }}</span>
                            </label>
                            <label class="radio-label">
                                <input
                                    type="radio"
                                    value="private"
                                    v-model="newInstanceForm.primary_interface.network_type"
                                    @change="handleNetworkTypeChange(newInstanceForm.primary_interface)"
                                />
                                <span>{{ t('dashboard.instanceDetail.networkTypes.private') }}</span>
                            </label>
                        </div>
                    </div>

                    <!-- VPC Type Subnets -->
                    <div class="form-row three-col" v-if="newInstanceForm.primary_interface.network_type === 'vpc'">
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.forms.vpc') }}</label>
                            <select
                                v-model="newInstanceForm.primary_interface.vpc_id"
                                class="form-select"
                                @change="handleVpcChange(newInstanceForm.primary_interface)"
                            >
                                <option value="" disabled>{{ t('dashboard.forms.placeholder.selectVpc') }}</option>
                                <option v-for="vpc in availableVPCs" :key="vpc.id" :value="vpc.id">
                                    {{ vpc.name }}
                                </option>
                            </select>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.forms.subnet') }}</label>
                            <select
                                v-model="newInstanceForm.primary_interface.subnet_id"
                                class="form-select"
                                :disabled="!newInstanceForm.primary_interface.vpc_id"
                                @change="handleSubnetChange(newInstanceForm.primary_interface)"
                            >
                                <option value="" disabled>
                                    {{ t('dashboard.forms.placeholder.selectSubnet') }}
                                </option>
                                <option
                                    v-for="sub in getFilteredSubnets(newInstanceForm.primary_interface.vpc_id)"
                                    :key="sub.id"
                                    :value="sub.id"
                                >
                                    {{ sub.name }} ({{ sub.network || sub.network_cidr }}) -
                                    {{ t('dashboard.forms.placeholder.availableIp') }}:
                                    {{ sub.available_count ?? 0 }}
                                </option>
                            </select>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.table.ipAddress') }}</label>
                            <select
                                v-model="newInstanceForm.primary_interface.ip_address"
                                class="form-select"
                                :disabled="
                                    !newInstanceForm.primary_interface.subnet_id ||
                                    addressesLoading[newInstanceForm.primary_interface.subnet_id]
                                "
                            >
                                <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }}</option>
                                <option
                                    v-for="addr in subnetAddresses[newInstanceForm.primary_interface.subnet_id] || []"
                                    :key="addr.address"
                                    :value="addr.address"
                                >
                                    {{ addr.address.split('/')[0] }}
                                </option>
                            </select>
                        </div>
                    </div>

                    <!-- Public Network -->
                    <div class="form-row" v-else-if="newInstanceForm.primary_interface.network_type === 'public'">
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.forms.publicSubnet') }}</label>
                            <select
                                v-model="newInstanceForm.primary_interface.subnet_id"
                                class="form-select"
                                @change="handleSubnetChange(newInstanceForm.primary_interface)"
                            >
                                <option value="" disabled>
                                    {{ t('dashboard.forms.placeholder.selectSubnet') }}
                                </option>
                                <option v-for="sub in getPublicSubnets()" :key="sub.id" :value="sub.id">
                                    {{ sub.name }} ({{ sub.network || sub.network_cidr }}) -
                                    {{ t('dashboard.forms.placeholder.availableIp') }}:
                                    {{ sub.available_count ?? 0 }}
                                </option>
                            </select>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.table.ipAddress') }}</label>
                            <select
                                v-model="newInstanceForm.primary_interface.ip_address"
                                class="form-select"
                                :disabled="
                                    !newInstanceForm.primary_interface.subnet_id ||
                                    addressesLoading[newInstanceForm.primary_interface.subnet_id]
                                "
                            >
                                <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }}</option>
                                <option
                                    v-for="addr in subnetAddresses[newInstanceForm.primary_interface.subnet_id] || []"
                                    :key="addr.address"
                                    :value="addr.address"
                                >
                                    {{ addr.address.split('/')[0] }}
                                </option>
                            </select>
                        </div>
                    </div>

                    <!-- Private Subnets -->
                    <div class="form-row" v-else-if="newInstanceForm.primary_interface.network_type === 'private'">
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.forms.subnet') }}</label>
                            <select
                                v-model="newInstanceForm.primary_interface.subnet_id"
                                class="form-select"
                                @change="handleSubnetChange(newInstanceForm.primary_interface)"
                            >
                                <option value="" disabled>
                                    {{ t('dashboard.forms.placeholder.selectSubnet') }}
                                </option>
                                <option v-for="sub in getPrivateSubnets()" :key="sub.id" :value="sub.id">
                                    {{ sub.name }} ({{ sub.network || sub.network_cidr }}) -
                                    {{ t('dashboard.forms.placeholder.availableIp') }}:
                                    {{ sub.available_count ?? 0 }}
                                </option>
                            </select>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('dashboard.table.ipAddress') }}</label>
                            <select
                                v-model="newInstanceForm.primary_interface.ip_address"
                                class="form-select"
                                :disabled="
                                    !newInstanceForm.primary_interface.subnet_id ||
                                    addressesLoading[newInstanceForm.primary_interface.subnet_id]
                                "
                            >
                                <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }}</option>
                                <option
                                    v-for="addr in subnetAddresses[newInstanceForm.primary_interface.subnet_id] || []"
                                    :key="addr.address"
                                    :value="addr.address"
                                >
                                    {{ addr.address.split('/')[0] }}
                                </option>
                            </select>
                        </div>
                    </div>

                    <div class="form-group">
                        <label class="form-label"
                            >{{ t('dashboard.forms.securityGroups') }} ({{ t('dashboard.forms.optional') }})</label
                        >
                        <div class="checkbox-group compact">
                            <label
                                v-for="sg in getFilteredSecurityGroups(
                                    newInstanceForm.primary_interface.network_type === 'vpc'
                                        ? newInstanceForm.primary_interface.vpc_id
                                        : undefined
                                )"
                                :key="sg.id"
                                class="checkbox-label"
                            >
                                <input
                                    type="checkbox"
                                    :value="sg.id"
                                    v-model="newInstanceForm.primary_interface.security_group_ids"
                                />
                                <span>{{ sg.name }}</span>
                            </label>
                        </div>
                        <div
                            v-if="
                                getFilteredSecurityGroups(
                                    newInstanceForm.primary_interface.network_type === 'vpc'
                                        ? newInstanceForm.primary_interface.vpc_id
                                        : undefined
                                ).length === 0
                            "
                            class="text-secondary text-xs mt-1"
                        >
                            {{ t('dashboard.table.noResults') }}
                        </div>
                    </div>

                    <div class="secondary-interfaces-list" v-if="newInstanceForm.secondary_interfaces.length > 0">
                        <div
                            v-for="(iface, idx) in newInstanceForm.secondary_interfaces"
                            :key="idx"
                            class="form-section secondary-section"
                        >
                            <div class="section-header">
                                <div class="section-title" style="display: flex; align-items: center; gap: 6px">
                                    {{ t('dashboard.instanceDetail.secondaryInterfaceLabel', { index: idx + 1 }) }}
                                    <span
                                        class="iface-help-icon"
                                        @mouseenter="showIfaceHelp($event)"
                                        @mouseleave="hideIfaceHelp"
                                    >
                                        <HelpCircle :size="14" />
                                    </span>
                                </div>
                                <button
                                    class="btn btn-ghost btn-sm text-error remove-btn"
                                    @click="removeSecondaryInterface(idx)"
                                >
                                    <MinusCircle :size="16" />
                                </button>
                            </div>

                            <div class="form-group">
                                <div class="radio-group mini">
                                    <label class="radio-label">
                                        <input
                                            type="radio"
                                            value="vpc"
                                            v-model="iface.network_type"
                                            @change="handleNetworkTypeChange(iface, true)"
                                        />
                                        <span>{{ t('dashboard.instanceDetail.networkTypes.vpc') }}</span>
                                    </label>
                                    <label
                                        class="radio-label"
                                        :class="{
                                            'opacity-40': newInstanceForm.primary_interface.network_type === 'vpc',
                                        }"
                                    >
                                        <input
                                            type="radio"
                                            value="public"
                                            v-model="iface.network_type"
                                            @change="handleNetworkTypeChange(iface, true)"
                                            :disabled="newInstanceForm.primary_interface.network_type === 'vpc'"
                                        />
                                        <span>{{ t('dashboard.instanceDetail.networkTypes.public') }}</span>
                                    </label>
                                    <label
                                        class="radio-label"
                                        :class="{
                                            'opacity-40': newInstanceForm.primary_interface.network_type === 'vpc',
                                        }"
                                    >
                                        <input
                                            type="radio"
                                            value="private"
                                            v-model="iface.network_type"
                                            @change="handleNetworkTypeChange(iface, true)"
                                            :disabled="newInstanceForm.primary_interface.network_type === 'vpc'"
                                        />
                                        <span>{{ t('dashboard.instanceDetail.networkTypes.private') }}</span>
                                    </label>
                                </div>
                            </div>

                            <div class="form-row three-col" v-if="iface.network_type === 'vpc'">
                                <div class="form-group">
                                    <select
                                        v-model="iface.vpc_id"
                                        class="form-select"
                                        :disabled="newInstanceForm.primary_interface.network_type === 'vpc'"
                                        :title="
                                            newInstanceForm.primary_interface.network_type === 'vpc'
                                                ? t('dashboard.instanceDetail.secondaryIface.vpcShare')
                                                : ''
                                        "
                                        @change="handleVpcChange(iface)"
                                    >
                                        <option value="" disabled>
                                            {{ t('dashboard.forms.placeholder.selectVpc') }}
                                        </option>
                                        <option v-for="vpc in availableVPCs" :key="vpc.id" :value="vpc.id">
                                            {{ vpc.name }}
                                        </option>
                                    </select>
                                </div>
                                <div class="form-group">
                                    <select
                                        v-model="iface.subnet_id"
                                        class="form-select"
                                        :disabled="!iface.vpc_id"
                                        @change="handleSubnetChange(iface)"
                                    >
                                        <option value="" disabled>
                                            {{ t('dashboard.forms.placeholder.selectSubnet') }}
                                        </option>
                                        <option
                                            v-for="sub in getFilteredSubnetsForSecondary(iface.vpc_id, idx)"
                                            :key="sub.id"
                                            :value="sub.id"
                                        >
                                            {{ sub.name }} ({{ sub.network || sub.network_cidr }}) -
                                            {{ t('dashboard.forms.placeholder.availableIp') }}:
                                            {{ sub.available_count ?? 0 }}
                                        </option>
                                    </select>
                                </div>
                                <div class="form-group">
                                    <select
                                        v-model="iface.ip_address"
                                        class="form-select"
                                        :disabled="!iface.subnet_id || addressesLoading[iface.subnet_id]"
                                    >
                                        <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }} IP</option>
                                        <option
                                            v-for="addr in subnetAddresses[iface.subnet_id] || []"
                                            :key="addr.address"
                                            :value="addr.address"
                                        >
                                            {{ addr.address.split('/')[0] }}
                                        </option>
                                    </select>
                                </div>
                            </div>
                            <div class="form-row" v-else-if="iface.network_type === 'public'">
                                <div class="form-group">
                                    <select
                                        v-model="iface.subnet_id"
                                        class="form-select"
                                        @change="handleSubnetChange(iface)"
                                    >
                                        <option value="" disabled>
                                            {{ t('dashboard.forms.placeholder.selectPublicSubnet') }}
                                        </option>
                                        <option
                                            v-for="sub in getPublicSubnetsForSecondary(idx)"
                                            :key="sub.id"
                                            :value="sub.id"
                                        >
                                            {{ sub.name }} ({{ sub.network || sub.network_cidr }}) -
                                            {{ t('dashboard.forms.placeholder.availableIp') }}:
                                            {{ sub.available_count ?? 0 }}
                                        </option>
                                    </select>
                                </div>
                                <div class="form-group">
                                    <select
                                        v-model="iface.ip_address"
                                        class="form-select"
                                        :disabled="!iface.subnet_id || addressesLoading[iface.subnet_id]"
                                    >
                                        <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }} IP</option>
                                        <option
                                            v-for="addr in subnetAddresses[iface.subnet_id] || []"
                                            :key="addr.address"
                                            :value="addr.address"
                                        >
                                            {{ addr.address.split('/')[0] }}
                                        </option>
                                    </select>
                                </div>
                            </div>

                            <div class="form-row" v-else-if="iface.network_type === 'private'">
                                <div class="form-group">
                                    <select
                                        v-model="iface.subnet_id"
                                        class="form-select"
                                        @change="handleSubnetChange(iface)"
                                    >
                                        <option value="" disabled>
                                            {{ t('dashboard.forms.placeholder.selectSubnet') }}
                                        </option>
                                        <option
                                            v-for="sub in getPrivateSubnetsForSecondary(idx)"
                                            :key="sub.id"
                                            :value="sub.id"
                                        >
                                            {{ sub.name }} ({{ sub.network || sub.network_cidr }}) -
                                            {{ t('dashboard.forms.placeholder.availableIp') }}:
                                            {{ sub.available_count ?? 0 }}
                                        </option>
                                    </select>
                                </div>
                                <div class="form-group">
                                    <select
                                        v-model="iface.ip_address"
                                        class="form-select"
                                        :disabled="!iface.subnet_id || addressesLoading[iface.subnet_id]"
                                    >
                                        <option value="">{{ t('dashboard.forms.placeholder.autoAllocate') }} IP</option>
                                        <option
                                            v-for="addr in subnetAddresses[iface.subnet_id] || []"
                                            :key="addr.address"
                                            :value="addr.address"
                                        >
                                            {{ addr.address.split('/')[0] }}
                                        </option>
                                    </select>
                                </div>
                            </div>

                            <div class="form-group mt-2">
                                <label class="form-label text-xs"
                                    >{{ t('dashboard.forms.securityGroups') }} ({{
                                        t('dashboard.forms.optional')
                                    }})</label
                                >
                                <div class="checkbox-group compact">
                                    <label
                                        v-for="sg in getFilteredSecurityGroups(
                                            iface.network_type === 'vpc' ? iface.vpc_id : undefined
                                        )"
                                        :key="sg.id"
                                        class="checkbox-label"
                                    >
                                        <input type="checkbox" :value="sg.id" v-model="iface.security_group_ids" />
                                        <span>{{ sg.name }}</span>
                                    </label>
                                </div>
                            </div>
                        </div>
                    </div>

                    <div class="add-interface-row">
                        <button
                            class="btn btn-outline btn-sm w-full"
                            @click="addSecondaryInterface"
                            :disabled="newInstanceForm.secondary_interfaces.length >= 7"
                        >
                            <PlusCircle :size="14" /> {{ t('dashboard.buttons.addSecondaryInterface') }}
                        </button>
                    </div>
                </div>
            </div>

            <!-- Advanced Options Expandable -->
            <div class="advanced-section">
                <div
                    class="advanced-trigger"
                    @click="newInstanceForm.advanced_expanded = !newInstanceForm.advanced_expanded"
                >
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
                        <select
                            id="pinHypervisor"
                            name="pinHypervisor"
                            v-model="newInstanceForm.hypervisor"
                            class="form-select"
                        >
                            <option :value="null">{{ t('dashboard.instanceDetail.hypervisorAuto') }}</option>
                            <option v-for="h in hypersForZone" :key="h.uuid" :value="h.uuid">
                                {{ h.hostname }} ({{ h.status_name }})
                            </option>
                        </select>
                        <small class="form-hint">{{ t('dashboard.instanceDetail.hypervisorHint') }}</small>
                    </div>

                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.instanceDetail.loginPort') }}</label>
                        <input
                            id="loginPort"
                            name="loginPort"
                            v-model.number="newInstanceForm.login_port"
                            type="number"
                            class="form-input"
                            :placeholder="t('dashboard.instanceDetail.loginPortPlaceholder')"
                        />
                        <small class="form-hint">{{ t('dashboard.instanceDetail.loginPortHint') }}</small>
                    </div>

                    <div class="form-group">
                        <div class="flex-row">
                            <label class="form-label mb-0">{{
                                t('dashboard.instanceDetail.nestedVirtualization')
                            }}</label>
                            <label class="switch">
                                <input
                                    id="nestedVirtualization"
                                    name="nestedVirtualization"
                                    type="checkbox"
                                    v-model="newInstanceForm.nested_enable"
                                />
                                <span class="slider"></span>
                            </label>
                        </div>
                    </div>

                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.instanceDetail.userDataType') }}</label>
                        <select
                            id="userdataType"
                            name="userdataType"
                            v-model="newInstanceForm.userdata_type"
                            class="form-select"
                        >
                            <option value="plain">{{ t('dashboard.instanceDetail.plain') }}</option>
                            <option value="base64">Base64</option>
                        </select>
                    </div>

                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.instanceDetail.userData') }}</label>
                        <textarea
                            id="userdata"
                            name="userdata"
                            v-model="newInstanceForm.userdata"
                            class="form-textarea"
                            rows="4"
                            :placeholder="t('dashboard.instanceDetail.userDataPlaceholder')"
                        ></textarea>
                    </div>
                </div>
            </div>
        </div>

        <div v-if="createError" class="modal-error text-error">
            {{ createError }}
        </div>

        <template #footer>
            <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creatingInstance">
                {{ t('actions.cancel') }}
            </button>
            <button type="button" class="btn btn-primary" @click="handleCreateInstance" :disabled="creatingInstance">
                <span
                    v-if="creatingInstance"
                    class="loading-spinner"
                    style="width: 16px; height: 16px; border-width: 2px"
                ></span>
                {{ creatingInstance ? t('dashboard.overview.loadingOverview') : t('dashboard.buttons.createInstance') }}
            </button>
        </template>
    </BaseModal>

    <Teleport to="body">
        <div
            v-if="ifaceTooltipVisible"
            class="iface-help-tooltip"
            :style="{
                position: 'fixed',
                bottom: ifaceTooltipPos.bottom,
                right: ifaceTooltipPos.right,
                zIndex: 'var(--z-tooltip)',
            }"
        >
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
</template>

<style scoped>
.modal-error {
    margin-top: var(--spacing-4);
    font-size: var(--font-size-sm);
    background: var(--error-light);
    padding: var(--spacing-2);
    border-radius: var(--radius-sm);
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
.form-select {
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
    z-index: var(--z-dropdown);
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-lg);
    max-height: 200px;
    overflow-y: auto;
}
.no-border {
    border: none !important;
}
.p-2 {
    padding: 8px !important;
}
.hover-bg:hover {
    background: var(--bg-secondary);
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
.text-success {
    color: var(--success-color);
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
    z-index: var(--z-tooltip);
}
.iface-help-tooltip {
    position: fixed;
    z-index: var(--z-tooltip);
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
.radio-label input[type='radio'] {
    accent-color: var(--primary-color);
}
.checkbox-group.compact {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 4px;
    padding: 6px;
}
.mb-0 {
    margin-bottom: 0 !important;
}
.w-full {
    width: 100%;
}
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
    transition: 0.4s;
    border-radius: 24px;
}
.slider:before {
    position: absolute;
    content: '';
    height: 18px;
    width: 18px;
    left: 3px;
    bottom: 3px;
    background-color: white;
    transition: 0.4s;
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
.mt-1 {
    margin-top: 4px;
}
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
.checkbox-label input[type='checkbox'] {
    accent-color: var(--primary-color);
}

/* 下面这些类在列表页也在用，两边各留一份（scoped 样式不能共享） */
.password-input-wrapper {
    position: relative;
    display: flex;
    align-items: center;
}
.text-error {
    color: var(--error-color);
}
.btn:disabled {
    opacity: 0.3;
    cursor: not-allowed;
}
.form-group {
    margin-bottom: var(--spacing-4);
}
.form-label {
    display: block;
    margin-bottom: var(--spacing-2);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
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
.form-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-4);
}
.mt-2 {
    margin-top: 8px;
}
.text-xs {
    font-size: 0.75rem;
}
.text-secondary {
    color: var(--text-light);
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
.text-xs {
    font-size: 11px;
}
</style>
