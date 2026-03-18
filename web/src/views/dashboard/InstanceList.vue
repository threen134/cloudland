<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { useRouter } from 'vue-router'
import { instancesApi, type Instance } from '../../api/instances'
import { Play, Square, RotateCw, Trash2, Plus, Terminal, MoreVertical, Search, X, Check, Server, ChevronDown, ChevronUp, PlusCircle, MinusCircle, RefreshCw } from 'lucide-vue-next'

import { imagesApi, type Image } from '../../api/images'
import { vpcsApi, subnetsApi, securityGroupsApi, floatingIpsApi, type VPC, type Subnet, type SecurityGroup, type FloatingIP } from '../../api/networks'

import { keysApi, type SSHKey } from '../../api/keys'
import { flavorsApi } from '../../api/flavors'
import { zonesApi, type Zone } from '../../api/zones'
import { isValidName } from '../../utils/validation'
import DeleteModal from '../../components/modals/DeleteModal.vue'



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
const router = useRouter()
const { t } = useI18n()


const fetchInstances = async (showLoading: boolean = true) => {
    if (showLoading) loading.value = true
    try {
        const response = await instancesApi.fetchInstances()
        const data = response.data as any
        instanceList.value = Array.isArray(data) ? data : (data.instances || [])
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
    return instanceList.value.filter(inst => 
        inst.name.toLowerCase().includes(query) || 
        inst.id.toLowerCase().includes(query)
    )
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
        'pending': 'status-pending',
        'starting': 'status-pending',
        'stopping': 'status-pending',
        'error': 'status-error'
    }
    return statusMap[status?.toLowerCase()] || 'status-pending'
}

const formatMemory = (mb: number) => {
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(0)} GB`
    }
    return `${mb} MB`
}

const getIPAddress = (instance: Instance) => {
    return instance.interfaces?.[0]?.ip_address || '-'
}

const handleAction = async (instance: Instance, action: 'start' | 'stop' | 'restart') => {
    actionLoading.value[instance.id] = action
    try {
        switch (action) {
            case 'start':
                await instancesApi.startInstance(instance.id, instance.hostname)
                break
            case 'stop':
                await instancesApi.stopInstance(instance.id, instance.hostname)
                break
            case 'restart':
                await instancesApi.rebootInstance(instance.id, instance.hostname)
                break
        }
        
        let targetStableStates: string[] = []
        if (action === 'start' || action === 'restart') targetStableStates = ['running', 'active', 'error']
        if (action === 'stop') targetStableStates = ['stopped', 'shutoff', 'shut_off', 'error']

        let attempts = 0
        const checkStatus = async () => {
            attempts++
            await fetchInstances(false)
            const currentInstance = instanceList.value.find(i => i.id === instance.id)
            const currentStatus = currentInstance?.status?.toLowerCase() || ''
            if (!currentInstance || targetStableStates.includes(currentStatus) || attempts >= 15) {
                actionLoading.value[instance.id] = null
            } else {
                setTimeout(checkStatus, 3000)
            }
        }
        
        setTimeout(checkStatus, 2000)
    } catch (error) {
        console.error(`Failed to ${action} instance:`, error)
        actionLoading.value[instance.id] = null
    }
}

const openConsole = async (instance: Instance) => {
    // Open window immediately to avoid popup blockers
    const consoleWindow = window.open('about:blank', '_blank')
    if (!consoleWindow) {
        alert('Popup blocked! Please allow popups for this site.')
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
        alert('Failed to get console info: ' + (error.response?.data?.error_message || error.message))
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
    } catch (error: any) {
        console.error('Failed to delete instance:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
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
const sshKeysDropdownOpen = ref(false)

const tempPassword = ref('')
const confirmPassword = ref('')
const enablePassword = ref(false)
const enableSSHKeys = ref(false)

const isHostnameValid = computed(() => isValidName(newInstanceForm.value.hostname))



interface InterfaceForm {
    network_type: 'isolated' | 'vpc' | 'public'
    vpc_id: string
    subnet_id: string
    public_ip_id: string
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
    primary_interface: {
        network_type: 'vpc' as const,
        vpc_id: '',
        subnet_id: '',
        public_ip_id: '',
        security_group_ids: [] as string[]
    } as InterfaceForm,
    secondary_interfaces: [] as InterfaceForm[],
    advanced_expanded: false,
    general_expanded: true,
    primary_expanded: true
})

const addSecondaryInterface = () => {
    const newIface: InterfaceForm = {
        network_type: 'vpc',
        vpc_id: '',
        subnet_id: '',
        public_ip_id: '',
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
        primary_interface: {
            network_type: 'vpc',
            vpc_id: '',
            subnet_id: '',
            public_ip_id: '',
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
            newInstanceForm.value.zone = availableZones.value[0].name || availableZones.value[0].id
        }


        // Auto-select first VPC and its first subnet if available
        if (availableVPCs.value.length > 0) {
            newInstanceForm.value.primary_interface.vpc_id = availableVPCs.value[0].id
            const subnets = availableSubnets.value.filter(s => s.vpc?.id === availableVPCs.value[0].id)
            if (subnets.length > 0) {
                newInstanceForm.value.primary_interface.subnet_id = subnets[0].id
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

const getIsolatedSubnets = () => {
    return availableSubnets.value.filter(s => (!s.vpc || !s.vpc.id) && s.type?.toLowerCase() !== 'public')
}

const getPublicSubnets = () => {
    return availableSubnets.value.filter(s => s.type?.toLowerCase() === 'public')
}

const getAvailablePublicIps = () => {
    return availableFloatingIps.value.filter(fip => !fip.target_interface && !fip.interface)
}

const getFilteredSecurityGroups = (vpcId?: string) => {
    if (!vpcId) {
        // For isolated/public networks, show security groups not associated with any VPC
        return availableSecurityGroups.value.filter(sg => !sg.vpc || !sg.vpc.id)
    }
    return availableSecurityGroups.value.filter(sg => sg.vpc?.id === vpcId)
}

const handleNetworkTypeChange = (iface: InterfaceForm) => {
    iface.vpc_id = ''
    iface.subnet_id = ''
    iface.public_ip_id = ''
    iface.security_group_ids = []
    autoSelectDefaultSGs(iface)
}

const handleVpcChange = (iface: InterfaceForm) => {
    iface.subnet_id = ''
    iface.security_group_ids = []
    autoSelectDefaultSGs(iface)
}

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
        createError.value = 'Please fill in Hostname, Image, and Flavor.'
        return
    }

    if (!isHostnameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    // Validation for Credentials
    if (form.keys.length === 0 && !form.root_passwd) {
        createError.value = 'Please provide either an SSH Key or a Root Password for instance login.'
        return
    }

    // Validation for primary interface
    const pi = form.primary_interface
    if (pi.network_type === 'vpc' && (!pi.vpc_id || !pi.subnet_id)) {
        createError.value = 'Please select VPC and Subnet for primary interface.'
        return
    }
    if (pi.network_type === 'isolated' && !pi.subnet_id) {
        createError.value = 'Please select Subnet for isolated primary interface.'
        return
    }
    if (pi.network_type === 'public' && !pi.public_ip_id && !pi.subnet_id) {
        createError.value = 'Please select Public Subnet for auto-allocation.'
        return
    }

    // Validation for secondary interfaces
    for (let i = 0; i < form.secondary_interfaces.length; i++) {
        const si = form.secondary_interfaces[i]
        if (si.network_type === 'public' && !si.public_ip_id && !si.subnet_id) {
            createError.value = `Please select Public Subnet for secondary interface #${i + 1} auto-allocation.`
            return
        }
        if (si.network_type === 'vpc' && (!si.vpc_id || !si.subnet_id)) {
            createError.value = `Please select VPC and Subnet for secondary interface #${i + 1}.`
            return
        }
    }

    creatingInstance.value = true
    try {
        const mapInterface = (iface: InterfaceForm) => {
            const payload: any = {
                security_groups: iface.security_group_ids.map(id => ({ id }))
            }
            if (iface.network_type === 'public') {
                if (iface.public_ip_id) {
                    payload.public_addresses = [{ id: iface.public_ip_id }]
                } else if (iface.subnet_id) {
                    payload.subnet = { id: iface.subnet_id }
                }
            } else {
                payload.subnet = { id: iface.subnet_id }
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

        await instancesApi.createInstance(payload)
        await fetchInstances()
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create instance:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creatingInstance.value = false
    }
}


onMounted(() => fetchInstances())
</script>

<template>
  <div>
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <Search :size="16" class="search-icon" />
          <input 
            type="text" 
            v-model="searchQuery"
            :placeholder="$t('actions.search') + '...'" 
            class="search-input"
          />
        </div>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchInstances()" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createInstance') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.flavor') }}</th>
            <th>{{ $t('dashboard.table.image') }}</th>
            <th>{{ $t('dashboard.table.ipAddress') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.vpc') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
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
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <p>{{ $t('messages.noInstances') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="instance in filteredInstances" :key="instance.id">
            <td>
              <div class="instance-info clickable" @click="navigateToDetail(instance)">
                <div class="resource-icon">
                   <Server :size="16" />
                </div>
                <div>
                   <div class="instance-name resource-link">{{ instance.hostname }}</div>
                   <div class="instance-id">{{ instance.id }}</div>
                </div>
              </div>
            </td>
            <td>
              <div class="flavor-info" v-if="instance.flavor">
                <span class="flavor-name">{{ instance.flavor.name }}</span>
                <span class="flavor-specs">
                  {{ instance.flavor.cpu }} vCPU • {{ formatMemory(instance.flavor.memory) }}
                </span>
              </div>
              <span v-else class="text-light">-</span>
            </td>
            <td>{{ instance.image?.name || '-' }}</td>
            <td>
              <code class="ip-address">{{ getIPAddress(instance) }}</code>
            </td>
            <td>
              <span :class="['badge', getStatusClass(instance.status)]">
                {{ instance.status }}
              </span>
            </td>
            <td>{{ instance.vpc?.name || '-' }}</td>
            <td>
              <div class="actions">
                <button 
                  v-if="['stopped', 'shutoff', 'shut_off'].includes(instance.status?.toLowerCase())"
                  class="btn btn-ghost btn-sm" 
                  :title="$t('actions.start')"
                  @click="handleAction(instance, 'start')"
                  :disabled="!!actionLoading[instance.id]"
                >
                  <span v-if="actionLoading[instance.id] === 'start'" class="loading-spinner small"></span>
                  <Play v-else :size="14" />
                </button>
                <button 
                  v-else
                  class="btn btn-ghost btn-sm" 
                  :title="$t('actions.stop')"
                  @click="handleAction(instance, 'stop')"
                  :disabled="!!actionLoading[instance.id]"
                >
                  <span v-if="actionLoading[instance.id] === 'stop'" class="loading-spinner small"></span>
                  <Square v-else :size="14" />
                </button>
                <button 
                  class="btn btn-ghost btn-sm" 
                  :title="$t('actions.restart')"
                  @click="handleAction(instance, 'restart')"
                  :disabled="!!actionLoading[instance.id] || instance.status !== 'running'"
                >
                  <span v-if="actionLoading[instance.id] === 'restart'" class="loading-spinner small"></span>
                  <RotateCw v-else :size="14" />
                </button>
                <button 
                  class="btn btn-ghost btn-sm" 
                  :title="$t('actions.console')"
                  @click="openConsole(instance)"
                >
                  <img src="/images/vnc.svg" alt="VNC" width="14" height="14" />
                </button>
                <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(instance)">
                  <Trash2 :size="14" />
                </button>
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
          <h3>{{ $t('dashboard.buttons.createInstance') }}</h3>
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
              <div class="section-title mb-0">{{ $t('dashboard.forms.sections.general') }}</div>
              <ChevronDown v-if="!newInstanceForm.general_expanded" :size="18" />
              <ChevronUp v-else :size="18" />
            </div>

            <div v-if="newInstanceForm.general_expanded" class="section-content mt-3">
              <div class="form-group">
                  <label class="form-label">{{ $t('dashboard.table.hostname') }} <span class="text-error">*</span></label>
                   <input 
                       v-model="newInstanceForm.hostname" 
                       type="text" 
                       :class="['form-input', { 'input-error': !isHostnameValid }]"
                       :placeholder="$t('dashboard.forms.placeholder.nameExample')" 
                   />
                   <div v-if="!isHostnameValid" class="text-error text-xs mt-1">
                       {{ $t('messages.invalidHostname') }}
                   </div>

              </div>

              <div class="form-row">
                  <div class="form-group">
                      <label class="form-label">{{ $t('dashboard.forms.image') }} <span class="text-error">*</span></label>
                      <select v-model="newInstanceForm.image_id" class="form-select">
                          <option value="" disabled>{{ $t('dashboard.forms.placeholder.selectImage') }}</option>
                          <option v-for="img in availableImages" :key="img.id" :value="img.id">
                              {{ img.name }}
                          </option>
                      </select>
                  </div>
                  <div class="form-group">
                      <label class="form-label">{{ $t('dashboard.forms.flavor') }} <span class="text-error">*</span></label>
                      <select v-model="newInstanceForm.flavor_id" class="form-select">
                          <option value="" disabled>{{ $t('dashboard.forms.placeholder.selectFlavor') }}</option>
                          <option v-for="f in availableFlavors" :key="f.name || f.id" :value="f.name || f.id">
                              {{ f.name }} ({{ f.vcpus || f.cpu || 0 }} vCPU, {{ formatMemory(f.ram || f.memory || 0) }} RAM, {{ f.disk || 0 }} GB Disk)
                          </option>
                      </select>
                  </div>
              </div>

              <div class="form-row">
                  <div class="form-group">
                       <label class="form-label">{{ $t('dashboard.table.zone') }}</label>
                       <select v-model="newInstanceForm.zone" class="form-select">
                           <option v-for="z in availableZones" :key="z.id" :value="z.name || z.id">
                               {{ z.name || z.id }}
                           </option>
                           <option v-if="availableZones.length === 0" value="default">{{ $t('dashboard.forms.placeholder.none') }}</option>
                       </select>

                  </div>
                  <div class="form-group">
                       <label class="form-label">{{ $t('dashboard.forms.count') }}</label>
                      <input v-model.number="newInstanceForm.count" type="number" class="form-input" min="1" max="16" />
                  </div>
              </div>

              <div class="form-group">
                  <label class="checkbox-label">
                      <input type="checkbox" v-model="enableSSHKeys" @change="handleSSHKeysCheckboxChange">
                     <span class="form-label mb-0">{{ $t('dashboard.forms.useSSHKeys') }} <span class="text-error" v-if="!newInstanceForm.root_passwd">*</span></span>
                </label>
                  
                  <div v-if="enableSSHKeys" class="multi-select-container mt-2" v-click-outside="() => sshKeysDropdownOpen = false">
                      <div class="multi-select-trigger" @click="sshKeysDropdownOpen = !sshKeysDropdownOpen">
                           <span v-if="newInstanceForm.keys.length === 0" class="placeholder">{{ $t('dashboard.forms.placeholder.selectSSHKey') || 'Select SSH Keys' }}</span>
                          <span v-else class="selected-count">{{ newInstanceForm.keys.length }} Keys Selected</span>
                          <ChevronDown :size="16" />
                      </div>
                      <div v-if="sshKeysDropdownOpen" class="multi-select-dropdown">
                          <div class="checkbox-group compact no-border">
                              <label v-for="k in availableKeys" :key="k.id" class="checkbox-label p-2 hover-bg">
                                  <input type="checkbox" :value="k.id" v-model="newInstanceForm.keys">
                                  <span>{{ k.name }}</span>
                              </label>
                               <div v-if="availableKeys.length === 0" class="p-2 text-secondary text-xs">{{ $t('messages.noData') }}</div>
                          </div>
                      </div>
                  </div>
              </div>

              <div class="form-group">
                  <label class="checkbox-label">
                      <input type="checkbox" v-model="enablePassword" @change="handlePasswordCheckboxChange">
                     <span class="form-label mb-0">{{ $t('dashboard.forms.setRootPassword') }} <span class="text-error" v-if="newInstanceForm.keys.length === 0">*</span></span>
                </label>
                  
                  <div v-if="enablePassword" class="password-inline-fields mt-3">
                      <div class="form-row">
                          <div class="form-col">
                               <label class="form-label text-xs">{{ $t('auth.password') }}</label>
                              <input v-model="tempPassword" type="password" class="form-input" placeholder="Enter password" />
                          </div>
                          <div class="form-col">
                               <label class="form-label text-xs">{{ $t('auth.confirmPassword') }}</label>
                              <input v-model="confirmPassword" type="password" class="form-input" placeholder="Confirm password" />
                          </div>
                      </div>
                      <div v-if="tempPassword && confirmPassword && tempPassword !== confirmPassword" class="text-error text-xs mt-1">
                           {{ $t('auth.passwordMismatch') }}
                      </div>
                      <div v-else-if="tempPassword && confirmPassword === tempPassword" class="text-success text-xs mt-1">
                          Password confirmed.
                      </div>
                  </div>
              </div>
            </div>
          </div>

          <div class="form-section">
            <div class="section-header collapsible-header" @click="newInstanceForm.primary_expanded = !newInstanceForm.primary_expanded">
               <div class="section-title mb-0">{{ $t('dashboard.forms.sections.primaryNetwork') }}</div>
              <ChevronDown v-if="!newInstanceForm.primary_expanded" :size="18" />
              <ChevronUp v-else :size="18" />
            </div>

            <div v-if="newInstanceForm.primary_expanded" class="section-content mt-3">
              <div class="form-group">
                   <label class="form-label">{{ $t('dashboard.forms.networkType') }}</label>
                  <div class="radio-group">
                      <label class="radio-label">
                          <input type="radio" value="vpc" v-model="newInstanceForm.primary_interface.network_type" @change="handleNetworkTypeChange(newInstanceForm.primary_interface)">
                          <span>VPC</span>
                      </label>
                      <label class="radio-label">
                          <input type="radio" value="isolated" v-model="newInstanceForm.primary_interface.network_type" @change="handleNetworkTypeChange(newInstanceForm.primary_interface)">
                          <span>Isolated</span>
                      </label>
                      <label class="radio-label">
                          <input type="radio" value="public" v-model="newInstanceForm.primary_interface.network_type" @change="handleNetworkTypeChange(newInstanceForm.primary_interface)">
                          <span>Public</span>
                      </label>
                  </div>
              </div>

              <!-- VPC Type Subnets -->
              <div class="form-row" v-if="newInstanceForm.primary_interface.network_type === 'vpc'">
                  <div class="form-group">
                       <label class="form-label">{{ $t('dashboard.table.vpc') }}</label>
                      <select v-model="newInstanceForm.primary_interface.vpc_id" class="form-select" @change="handleVpcChange(newInstanceForm.primary_interface)">
                           <option value="" disabled>{{ $t('dashboard.forms.placeholder.selectVpc') }}</option>
                          <option v-for="vpc in availableVPCs" :key="vpc.id" :value="vpc.id">{{ vpc.name }}</option>
                      </select>
                  </div>
                  <div class="form-group">
                       <label class="form-label">{{ $t('dashboard.table.subnet') }}</label>
                      <select v-model="newInstanceForm.primary_interface.subnet_id" class="form-select" :disabled="!newInstanceForm.primary_interface.vpc_id">
                           <option value="" disabled>{{ $t('dashboard.forms.placeholder.selectSubnet') }}</option>
                          <option v-for="sub in getFilteredSubnets(newInstanceForm.primary_interface.vpc_id)" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ sub.available_count ?? 0 }} Available IPs</option>
                      </select>
                  </div>
              </div>

              <!-- Isolated Subnets -->
              <div class="form-group" v-else-if="newInstanceForm.primary_interface.network_type === 'isolated'">
                   <label class="form-label">{{ $t('dashboard.forms.isolatedSubnet') || 'Isolated Subnet' }}</label>
                  <select v-model="newInstanceForm.primary_interface.subnet_id" class="form-select">
                      <option value="" disabled>Select Isolated Subnet</option>
                      <option v-for="sub in getIsolatedSubnets()" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ sub.available_count ?? 0 }} Available IPs</option>
                  </select>
              </div>

              <!-- Public Network -->
              <div class="form-row" v-else-if="newInstanceForm.primary_interface.network_type === 'public'">
                  <div class="form-group">
                       <label class="form-label">{{ $t('dashboard.overview.publicIp') }}</label>
                      <select v-model="newInstanceForm.primary_interface.public_ip_id" class="form-select">
                           <option value="">{{ $t('dashboard.forms.placeholder.autoAllocate') || 'Auto-allocate' }}</option>
                          <option v-for="fip in getAvailablePublicIps()" :key="fip.id" :value="fip.id">{{ fip.ip_address }}</option>
                      </select>
                  </div>
                  <div class="form-group">
                       <label class="form-label">{{ $t('dashboard.forms.publicSubnet') || 'Public Subnet' }}</label>
                      <select v-model="newInstanceForm.primary_interface.subnet_id" class="form-select">
                          <option value="" disabled>Select Public Subnet</option>
                          <option v-for="sub in getPublicSubnets()" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ sub.available_count ?? 0 }} Available IPs</option>
                      </select>
                  </div>
              </div>

              <div class="form-group">
                   <label class="form-label">{{ $t('dashboard.forms.securityGroupsOptional') || 'Security Groups (Optional)' }}</label>
                  <div class="checkbox-group compact">
                      <label v-for="sg in getFilteredSecurityGroups(newInstanceForm.primary_interface.network_type === 'vpc' ? newInstanceForm.primary_interface.vpc_id : undefined)" :key="sg.id" class="checkbox-label">
                          <input type="checkbox" :value="sg.id" v-model="newInstanceForm.primary_interface.security_group_ids">
                          <span>{{ sg.name }}</span>
                      </label>
                  </div>
                  <div v-if="getFilteredSecurityGroups(newInstanceForm.primary_interface.network_type === 'vpc' ? newInstanceForm.primary_interface.vpc_id : undefined).length === 0" class="text-secondary text-xs mt-1">
                       {{ $t('messages.noResults') }}
                  </div>
              </div>
            </div>
          </div>

          <!-- Secondary Interfaces -->
          <div class="secondary-interfaces-list" v-if="newInstanceForm.secondary_interfaces.length > 0">
            <div v-for="(iface, idx) in newInstanceForm.secondary_interfaces" :key="idx" class="form-section secondary-section">
                <div class="section-header">
                    <div class="section-title">Secondary Interface #{{ idx + 1 }}</div>
                    <button class="btn btn-ghost btn-sm text-error remove-btn" @click="removeSecondaryInterface(idx)">
                        <MinusCircle :size="16" />
                    </button>
                </div>
                
                <div class="form-group">
                    <div class="radio-group mini">
                        <label class="radio-label">
                            <input type="radio" value="vpc" v-model="iface.network_type" @change="handleNetworkTypeChange(iface)">
                            <span>VPC</span>
                        </label>
                        <label class="radio-label">
                            <input type="radio" value="isolated" v-model="iface.network_type" @change="handleNetworkTypeChange(iface)">
                            <span>Isolated</span>
                        </label>
                        <label class="radio-label">
                            <input type="radio" value="public" v-model="iface.network_type" @change="handleNetworkTypeChange(iface)">
                            <span>Public</span>
                        </label>
                    </div>
                </div>

                <div class="form-row" v-if="iface.network_type === 'vpc'">
                    <select v-model="iface.vpc_id" class="form-select" @change="handleVpcChange(iface)">
                        <option value="" disabled>Select VPC</option>
                        <option v-for="vpc in availableVPCs" :key="vpc.id" :value="vpc.id">{{ vpc.name }}</option>
                    </select>
                    <select v-model="iface.subnet_id" class="form-select" :disabled="!iface.vpc_id">
                        <option value="" disabled>Select Subnet</option>
                        <option v-for="sub in getFilteredSubnets(iface.vpc_id)" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ sub.available_count ?? 0 }} Available IPs</option>
                    </select>
                </div>
                <div class="form-group" v-else-if="iface.network_type === 'isolated'">
                    <select v-model="iface.subnet_id" class="form-select">
                        <option value="" disabled>Select Isolated Subnet</option>
                        <option v-for="sub in getIsolatedSubnets()" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ sub.available_count ?? 0 }} Available IPs</option>
                    </select>
                </div>
                <!-- Public Network for Secondary -->
                <div class="form-row" v-else-if="iface.network_type === 'public'">
                    <select v-model="iface.public_ip_id" class="form-select">
                        <option value="">Auto-allocate</option>
                        <option v-for="fip in getAvailablePublicIps()" :key="fip.id" :value="fip.id">{{ fip.ip_address }}</option>
                    </select>
                    <select v-model="iface.subnet_id" class="form-select">
                        <option value="" disabled>Select Public Subnet</option>
                        <option v-for="sub in getPublicSubnets()" :key="sub.id" :value="sub.id">{{ sub.name }} ({{ sub.network || sub.network_cidr }}) - {{ sub.available_count ?? 0 }} Available IPs</option>
                    </select>
                </div>

                <div class="form-group mt-2">
                    <label class="form-label text-xs">Security Groups (Optional)</label>
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
                <PlusCircle :size="14" /> Add Secondary Interface
            </button>
          </div>

          <!-- Advanced Options Expandable -->
          <div class="advanced-section">
            <div class="advanced-trigger" @click="newInstanceForm.advanced_expanded = !newInstanceForm.advanced_expanded">
                <div class="trigger-label">
                    <Server :size="16" />
                    <span>Advanced Options</span>
                </div>
                <ChevronDown v-if="!newInstanceForm.advanced_expanded" :size="20" />
                <ChevronUp v-else :size="20" />
            </div>

            <div class="advanced-content" v-if="newInstanceForm.advanced_expanded">
                <div class="form-group">
                    <label class="form-label">System Port</label>
                    <input v-model.number="newInstanceForm.login_port" type="number" class="form-input" placeholder="Login Port (default 22)" />
                </div>

                <div class="form-group">
                    <div class="flex-row">
                        <label class="form-label mb-0">Enable Nested Virtualization</label>
                        <label class="switch">
                            <input type="checkbox" v-model="newInstanceForm.nested_enable">
                            <span class="slider"></span>
                        </label>
                    </div>
                </div>

                <div class="form-group">
                    <label class="form-label">User Data Type</label>
                    <select v-model="newInstanceForm.userdata_type" class="form-select">
                        <option value="plain">Plain Text</option>
                        <option value="base64">Base64</option>
                    </select>
                </div>

                <div class="form-group">
                    <label class="form-label">User Data</label>
                    <textarea v-model="newInstanceForm.userdata" class="form-textarea" rows="4" placeholder="#!/bin/bash..."></textarea>
                </div>
            </div>
          </div>
          <div v-if="createError" class="modal-body" style="padding-top: 0; padding-bottom: 0;">
            <div class="text-error" style="margin-bottom:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
              {{ createError }}
            </div>
          </div>
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creatingInstance">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateInstance" :disabled="creatingInstance">
            <span v-if="creatingInstance" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creatingInstance ? $t('messages.creating') : $t('dashboard.buttons.createInstance') }}
          </button>
        </div>
      </div>
    </div>

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
  overflow: hidden;
}

.instance-info {
  display: flex;
  align-items: center;
  gap: 12px;
}

.resource-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  background-color: var(--primary-50);
  color: var(--primary-600);
  border-radius: 6px;
}

.instance-name {
  font-weight: var(--font-weight-medium);
  color: var(--primary-color);
}

.resource-link {
  color: var(--primary-600);
  font-weight: 500;
  cursor: pointer;
}

.resource-link:hover {
  text-decoration: underline;
}

.clickable {
  cursor: pointer;
}

.instance-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
}

.flavor-info {
  display: flex;
  flex-direction: column;
}

.flavor-name {
  font-weight: var(--font-weight-medium);
  font-size: var(--font-size-sm);
}

.flavor-specs {
  font-size: var(--font-size-xs);
  color: var(--text-light);
}

.ip-address {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-sm);
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
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  backdrop-filter: blur(4px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  animation: fadeIn 0.2s ease-out;
}

.modal-content {
  width: 100%;
  max-width: 500px;
  background: var(--bg-primary);
  border: 1px solid var(--border-light);
  box-shadow: var(--shadow-xl);
  animation: slideUp 0.3s cubic-bezier(0.16, 1, 0.3, 1);
  display: flex;
  flex-direction: column;
  max-height: 90vh;
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-4);
  padding: var(--spacing-4);
  border-bottom: 1px solid var(--border-light);
}

.modal-header h3 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.modal-body {
  padding: var(--spacing-4);
  overflow-y: auto;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--spacing-3);
  padding: var(--spacing-4);
  border-top: 1px solid var(--border-light);
}

.icon-btn {
  padding: 4px;
}

@keyframes fadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}

@keyframes slideUp {
  from { transform: translateY(20px); opacity: 0; }
  to { transform: translateY(0); opacity: 1; }
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
.text-success { color: var(--success-color); }
.btn-xs { padding: 2px 8px; font-size: 0.75rem; }
.justify-start { justify-content: flex-start !important; }
.gap-4 { gap: 16px; }

.form-row {



    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-4);
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
</style>



