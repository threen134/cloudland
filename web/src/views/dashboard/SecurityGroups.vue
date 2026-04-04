<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useSecurityGroup } from '../../composables/useSecurityGroup'
import { securityGroupsApi, vpcsApi, type SecurityGroup, type SecurityRule, type VPC } from '../../api/networks'
import { isValidName } from '../../utils/validation'

import { Shield, Plus, Trash2, ChevronDown, ChevronRight, Search, X, RefreshCw, Edit, ArrowUpDown, ArrowUp, ArrowDown, Network, HelpCircle, Check, Copy } from 'lucide-vue-next'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()
const { translateDescription } = useSecurityGroup()


const securityGroups = ref<SecurityGroup[]>([])
const loading = ref(false)
const expandedGroups = ref<string[]>([])
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newGroupForm = ref({
    name: '',
    description: '',
    vpc_id: '',
    is_default: false
})

const { t } = useI18n()
const toast = useToast()
const isNameValid = computed(() => isValidName(newGroupForm.value.name))

const { copiedId, copyId } = useCopyId()

const vpcs = ref<VPC[]>([])
const router = useRouter()

const toggleGroup = (id: string) => {
    const index = expandedGroups.value.indexOf(id)
    if (index === -1) {
        expandedGroups.value.push(id)
    } else {
        expandedGroups.value.splice(index, 1)
    }
}

const isExpanded = (id: string) => expandedGroups.value.includes(id)

type SortKey = 'name' | 'direction' | 'protocol' | 'port' | 'remote_cidr'
type SortOrder = 'asc' | 'desc'
const ruleSortKey = ref<SortKey>('direction')
const ruleSortOrder = ref<SortOrder>('asc')

type GroupFilter = { direction: string; protocol: string; keyword: string; show: boolean }
const groupFilters = ref<Record<string, GroupFilter>>({})

const getGroupFilter = (groupId: string): GroupFilter => {
    if (!groupFilters.value[groupId]) {
        groupFilters.value[groupId] = { direction: '', protocol: '', keyword: '', show: false }
    }
    return groupFilters.value[groupId]
}

const toggleGroupFilter = (groupId: string) => {
    const f = getGroupFilter(groupId)
    f.show = !f.show
    if (f.show) {
        if (!expandedGroups.value.includes(groupId)) {
            expandedGroups.value.push(groupId)
        }
    } else {
        f.direction = ''
        f.protocol = ''
        f.keyword = ''
    }
}

const toggleRuleSort = (key: SortKey) => {
    if (ruleSortKey.value === key) {
        ruleSortOrder.value = ruleSortOrder.value === 'asc' ? 'desc' : 'asc'
    } else {
        ruleSortKey.value = key
        ruleSortOrder.value = 'asc'
    }
}

const sortRules = (rules: SecurityRule[], groupId: string) => {
    const f = getGroupFilter(groupId)
    const filtered = rules.filter(r => {
        if (f.direction && r.direction !== f.direction) return false
        if (f.protocol && r.protocol !== f.protocol) return false
        if (f.keyword) {
            const kw = f.keyword.toLowerCase()
            if (![r.name, r.remote_cidr, r.protocol, r.direction].filter(Boolean).some(v => v!.toLowerCase().includes(kw))) return false
        }
        return true
    })
    return [...filtered].sort((a, b) => {
        const dir = ruleSortOrder.value === 'asc' ? 1 : -1
        switch (ruleSortKey.value) {
            case 'name':
                return dir * (a.name || '').localeCompare(b.name || '')
            case 'direction':
                return dir * a.direction.localeCompare(b.direction)
            case 'protocol':
                return dir * a.protocol.localeCompare(b.protocol)
            case 'port':
                return dir * ((a.port_min ?? -1) - (b.port_min ?? -1))
            case 'remote_cidr':
                return dir * (a.remote_cidr || '').localeCompare(b.remote_cidr || '')
            default:
                return 0
        }
    })
}

const fetchSecurityGroups = async () => {
    loading.value = true
    try {
        const [groupsResponse, vpcsResponse] = await Promise.all([
            securityGroupsApi.list(),
            vpcsApi.list()
        ])
        securityGroups.value = groupsResponse.security_groups || []
        vpcs.value = vpcsResponse.vpcs || []
    } catch (err) {
        console.error('API fetch failed:', err)
        securityGroups.value = []
        vpcs.value = []
    } finally {
        loading.value = false
    }
}

const openCreateModal = () => {
    newGroupForm.value = { name: '', description: '', vpc_id: '', is_default: false }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateGroup = async () => {
    createError.value = ''
    if (!newGroupForm.value.name) return
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    creating.value = true
    try {
        await securityGroupsApi.create({
            name: newGroupForm.value.name,
            description: newGroupForm.value.description,
            ...(newGroupForm.value.vpc_id ? { vpc: { id: newGroupForm.value.vpc_id } } : {}),
            is_default: newGroupForm.value.vpc_id ? newGroupForm.value.is_default : false
        })

        const response = await securityGroupsApi.list()
        securityGroups.value = response.security_groups || []

        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to create security group:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const filteredSecurityGroups = computed(() => {
    if (!searchQuery.value) return securityGroups.value
    const query = searchQuery.value.toLowerCase()
    return securityGroups.value.filter(group =>
        group.name.toLowerCase().includes(query) ||
        group.id.toLowerCase().includes(query)
    )
})

const WELL_KNOWN_PORTS: Record<number, string> = {
    20: 'FTP-Data', 21: 'FTP', 22: 'SSH', 23: 'Telnet', 25: 'SMTP',
    53: 'DNS', 67: 'DHCP', 68: 'DHCP', 80: 'HTTP', 110: 'POP3',
    119: 'NNTP', 123: 'NTP', 143: 'IMAP', 161: 'SNMP', 162: 'SNMP-Trap',
    389: 'LDAP', 443: 'HTTPS', 445: 'SMB', 465: 'SMTPS',
    514: 'Syslog', 587: 'SMTP', 636: 'LDAPS', 993: 'IMAPS', 995: 'POP3S',
    1433: 'MSSQL', 1521: 'Oracle', 2049: 'NFS', 3306: 'MySQL',
    3389: 'RDP', 5432: 'PostgreSQL', 5672: 'AMQP', 5900: 'VNC',
    6379: 'Redis', 8080: 'HTTP-Alt', 8443: 'HTTPS-Alt',
    9090: 'Prometheus', 9200: 'Elasticsearch', 27017: 'MongoDB',
}

const formatPort = (rule: SecurityRule) => {
    if (rule.protocol === 'icmp' || (rule.port_min != null && rule.port_min < 0)) {
        return '-'
    }
    if (rule.port_min === rule.port_max) {
        return rule.port_min?.toString() || t('dashboard.forms.placeholder.all')
    }
    if (rule.port_min === 1 && rule.port_max === 65535) {
        return t('dashboard.forms.placeholder.all')
    }
    return `${rule.port_min}-${rule.port_max}`
}

const getServiceName = (rule: SecurityRule): string | null => {
    if (rule.protocol === 'icmp' || rule.port_min == null || rule.port_min < 0) return null
    if (rule.port_min === rule.port_max && WELL_KNOWN_PORTS[rule.port_min]) {
        return WELL_KNOWN_PORTS[rule.port_min]
    }
    return null
}

const navigateToDetail = (group: SecurityGroup) => {
    router.push({ name: 'security-group-detail', params: { id: group.id } })
}

// --- Add/Edit Rule Modal ---
const ruleModalVisible = ref(false)
const ruleModalGroupId = ref('')
const editingRuleId = ref<string | null>(null)
const addingRule = ref(false)
const addRuleError = ref('')
const newRule = ref({
    name: '',
    direction: 'ingress',
    protocol: 'tcp',
    port_min: 80,
    port_max: 80,
    remote_cidr: '0.0.0.0/0'
})

const openAddRuleModal = (groupId: string) => {
    ruleModalGroupId.value = groupId
    editingRuleId.value = null
    newRule.value = { name: '', direction: 'ingress', protocol: 'tcp', port_min: 80, port_max: 80, remote_cidr: '0.0.0.0/0' }
    addRuleError.value = ''
    ruleModalVisible.value = true
}

const openEditRuleModal = (groupId: string, rule: SecurityRule) => {
    ruleModalGroupId.value = groupId
    editingRuleId.value = rule.id
    newRule.value = {
        name: rule.name || '',
        direction: rule.direction,
        protocol: rule.protocol,
        port_min: rule.protocol === 'icmp' ? 1 : (rule.port_min || 1),
        port_max: rule.protocol === 'icmp' ? 65535 : (rule.port_max || 65535),
        remote_cidr: rule.remote_cidr || ''
    }
    addRuleError.value = ''
    ruleModalVisible.value = true
}

const closeRuleModal = () => {
    ruleModalVisible.value = false
    addRuleError.value = ''
}

const handleSaveRule = async () => {
    addRuleError.value = ''
    if (newRule.value.protocol !== 'icmp' && newRule.value.port_min > newRule.value.port_max) {
        addRuleError.value = t('dashboard.securityGroupDetail.portRangeError')
        return
    }
    addingRule.value = true
    try {
        if (editingRuleId.value) {
            await securityGroupsApi.patchRule(ruleModalGroupId.value, editingRuleId.value, newRule.value as any)
        } else {
            await securityGroupsApi.addRule(ruleModalGroupId.value, newRule.value as any)
        }
        closeRuleModal()
        await fetchSecurityGroups()
        toast.success(editingRuleId.value ? t('messages.updateSuccess') : t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to save rule:', err)
        addRuleError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        addingRule.value = false
    }
}

// --- Delete Rule Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const ruleToDelete = ref<{ groupId: string; rule: SecurityRule } | null>(null)

const handleDeleteClick = (groupId: string, rule: SecurityRule) => {
    ruleToDelete.value = { groupId, rule }
    deleteModalVisible.value = true
}
const closeDeleteModal = () => {
    deleteModalVisible.value = false
    ruleToDelete.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!ruleToDelete.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await securityGroupsApi.deleteRule(ruleToDelete.value.groupId, ruleToDelete.value.rule.id)
        await fetchSecurityGroups()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete security rule:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

// --- Delete Security Group Modal ---
const deleteGroupModalVisible = ref(false)
const deletingGroup = ref(false)
const deleteGroupError = ref('')
const groupToDelete = ref<SecurityGroup | null>(null)

const handleDeleteGroupClick = (group: SecurityGroup) => {
    groupToDelete.value = group
    deleteGroupError.value = ''
    deleteGroupModalVisible.value = true
}

const closeDeleteGroupModal = () => {
    deleteGroupModalVisible.value = false
    groupToDelete.value = null
    deleteGroupError.value = ''
}

const confirmDeleteGroup = async () => {
    if (!groupToDelete.value) return
    deletingGroup.value = true
    deleteGroupError.value = ''
    try {
        await securityGroupsApi.delete(groupToDelete.value.id)
        await fetchSecurityGroups()
        closeDeleteGroupModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete security group:', error)
        deleteGroupError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingGroup.value = false
    }
}

// --- Interface Tooltip ---
const ifaceTooltipVisible = ref(false)
const ifaceTooltipGroup = ref<SecurityGroup | null>(null)
const ifaceTooltipStyle = ref({ top: '0px', left: '0px' })

const showIfaceTooltip = (event: MouseEvent, group: SecurityGroup) => {
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
    ifaceTooltipStyle.value = {
        top: `${rect.bottom + 8}px`,
        left: `${rect.right}px`
    }
    ifaceTooltipGroup.value = group
    ifaceTooltipVisible.value = true
}

const hideIfaceTooltip = () => {
    ifaceTooltipVisible.value = false
    ifaceTooltipGroup.value = null
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchSecurityGroups()
    }
})

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchSecurityGroups()
    }
})

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
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchSecurityGroups" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createSecurityGroup') }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="text-center" style="padding: 48px;">
      <div class="loading-spinner"></div>
    </div>

    <div v-else-if="filteredSecurityGroups.length === 0" class="text-center text-secondary" style="padding: 48px;">
       <div v-if="searchQuery">
          <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noResults') }}</p>
       </div>
       <div v-else>
          <Shield :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p class="text-secondary">{{ $t('messages.noSecurityGroups') }}</p>
       </div>
    </div>

    <div v-else class="security-groups-list">
      <div v-for="group in filteredSecurityGroups" :key="group.id" class="card sg-card">
        <div class="sg-header" @click="toggleGroup(group.id)">
          <div class="sg-header-left">
            <component :is="isExpanded(group.id) ? ChevronDown : ChevronRight" :size="16" class="expand-icon" />
            <div class="sg-info">
              <div class="sg-name-row">
                <span class="resource-link" @click.stop="navigateToDetail(group)">{{ group.name }}</span>
                <span v-if="group.is_default" class="badge badge-primary">{{ $t('dashboard.table.default') }}</span>
                <span class="sg-vpc-badge" v-if="group.vpc?.name">{{ group.vpc.name }}</span>
              </div>
              <div class="sg-description" v-if="group.description">{{ translateDescription(group.description) }}</div>
              <div class="resource-id-row">
                <span class="sg-id" :title="group.id">{{ group.id.slice(0, 8) }}...</span>
                <button class="copy-btn-mini" @click.stop.prevent="copyId(group.id)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                  <Check v-if="copiedId === group.id" :size="10" style="color: #10b981;" />
                  <Copy v-else :size="10" />
                </button>
              </div>
            </div>
          </div>
          <div class="sg-header-right">
            <span class="iface-count-badge" v-if="group.target_interfaces?.length"
              @mouseenter="showIfaceTooltip($event, group)"
              @mouseleave="hideIfaceTooltip">
              <Network :size="12" />
              {{ group.target_interfaces.length }} {{ $t('dashboard.securityGroupDetail.associatedInterfaces') }}
            </span>
            <span class="rule-count-badge">{{ group.security_rules?.length || 0 }} {{ $t('dashboard.table.securityRules') }}</span>
            <button :class="['btn btn-ghost btn-sm', { 'btn-filter-active': getGroupFilter(group.id).show }]" :title="$t('actions.filter')" @click.stop="toggleGroupFilter(group.id)">
              <Search :size="14" />
            </button>
            <button class="btn btn-ghost btn-sm" :title="$t('dashboard.buttons.addRule')" @click.stop="openAddRuleModal(group.id)">
              <Plus :size="14" />
            </button>
            <button
              class="btn btn-ghost btn-sm text-error"
              :title="$t('actions.delete')"
              :disabled="group.is_default"
              @click.stop="handleDeleteGroupClick(group)"
            >
              <Trash2 :size="14" />
            </button>
          </div>
        </div>

        <div v-show="isExpanded(group.id)" class="sg-rules">
          <!-- Per-group filter bar -->
          <div v-if="getGroupFilter(group.id).show" class="sg-filter-bar">
            <div class="select-wrapper sg-filter-select">
              <select v-model="getGroupFilter(group.id).direction" class="form-input form-input-sm">
                <option value="">{{ $t('dashboard.table.direction') }}: {{ $t('dashboard.forms.placeholder.all') }}</option>
                <option value="ingress">{{ $t('dashboard.table.ingress') }}</option>
                <option value="egress">{{ $t('dashboard.table.egress') }}</option>
              </select>
            </div>
            <div class="select-wrapper sg-filter-select">
              <select v-model="getGroupFilter(group.id).protocol" class="form-input form-input-sm">
                <option value="">{{ $t('dashboard.table.protocol') }}: {{ $t('dashboard.forms.placeholder.all') }}</option>
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
                <option value="icmp">ICMP</option>
              </select>
            </div>
            <div class="sg-filter-search">
              <Search :size="13" class="sg-filter-search-icon" />
              <input
                v-model="getGroupFilter(group.id).keyword"
                type="text"
                class="sg-filter-search-input"
                :placeholder="$t('dashboard.forms.placeholder.nameExample')"
              />
              <button v-if="getGroupFilter(group.id).keyword" class="filter-clear" @click="getGroupFilter(group.id).keyword = ''">
                <X :size="12" />
              </button>
            </div>
          </div>

          <table class="rules-table">
            <thead>
              <tr>
                <th class="sortable-th" @click="toggleRuleSort('name')">
                  {{ $t('dashboard.table.name') }}
                  <ArrowUp v-if="ruleSortKey === 'name' && ruleSortOrder === 'asc'" :size="12" />
                  <ArrowDown v-else-if="ruleSortKey === 'name' && ruleSortOrder === 'desc'" :size="12" />
                  <ArrowUpDown v-else :size="12" class="sort-idle" />
                </th>
                <th class="sortable-th" @click="toggleRuleSort('direction')">
                  {{ $t('dashboard.table.direction') }}
                  <ArrowUp v-if="ruleSortKey === 'direction' && ruleSortOrder === 'asc'" :size="12" />
                  <ArrowDown v-else-if="ruleSortKey === 'direction' && ruleSortOrder === 'desc'" :size="12" />
                  <ArrowUpDown v-else :size="12" class="sort-idle" />
                </th>
                <th class="sortable-th" @click="toggleRuleSort('protocol')">
                  {{ $t('dashboard.table.protocol') }}
                  <ArrowUp v-if="ruleSortKey === 'protocol' && ruleSortOrder === 'asc'" :size="12" />
                  <ArrowDown v-else-if="ruleSortKey === 'protocol' && ruleSortOrder === 'desc'" :size="12" />
                  <ArrowUpDown v-else :size="12" class="sort-idle" />
                </th>
                <th class="sortable-th" @click="toggleRuleSort('port')">
                  {{ $t('dashboard.table.ports') }}
                  <ArrowUp v-if="ruleSortKey === 'port' && ruleSortOrder === 'asc'" :size="12" />
                  <ArrowDown v-else-if="ruleSortKey === 'port' && ruleSortOrder === 'desc'" :size="12" />
                  <ArrowUpDown v-else :size="12" class="sort-idle" />
                </th>
                <th class="sortable-th" @click="toggleRuleSort('remote_cidr')">
                  {{ $t('dashboard.table.remote') }}
                  <ArrowUp v-if="ruleSortKey === 'remote_cidr' && ruleSortOrder === 'asc'" :size="12" />
                  <ArrowDown v-else-if="ruleSortKey === 'remote_cidr' && ruleSortOrder === 'desc'" :size="12" />
                  <ArrowUpDown v-else :size="12" class="sort-idle" />
                </th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!sortRules(group.security_rules, group.id).length">
                <td colspan="6" class="text-center text-secondary">{{ $t('messages.noData') }}</td>
              </tr>
              <tr v-else v-for="rule in sortRules(group.security_rules, group.id)" :key="rule.id">
                <td>{{ rule.name || '-' }}</td>
                <td>
                  <span :class="['direction-badge', rule.direction]">
                    {{ rule.direction === 'ingress' ? $t('dashboard.table.ingress') : $t('dashboard.table.egress') }}
                  </span>
                </td>
                <td class="protocol">{{ rule.protocol.toUpperCase() }}</td>
                <td class="port">{{ formatPort(rule) }} <span v-if="getServiceName(rule)" class="service-tag">{{ getServiceName(rule) }}</span></td>
                <td class="cidr">{{ rule.remote_cidr || $t('dashboard.forms.placeholder.none') }}</td>
                <td class="actions-cell">
                  <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="openEditRuleModal(group.id, rule)">
                    <Edit :size="14" />
                  </button>
                  <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(group.id, rule)">
                    <Trash2 :size="14" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Create Security Group Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createSecurityGroup') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>

        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input
              v-model="newGroupForm.name"
              type="text"
              :class="['form-input', { 'input-error': !isNameValid }]"
              :placeholder="$t('dashboard.forms.placeholder.sgNameExample')"
            />
            <div v-if="!isNameValid" class="text-error text-xs mt-1">
              {{ $t('messages.invalidHostname') }}
            </div>
          </div>

          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
            <input
              v-model="newGroupForm.description"
              type="text"
              class="form-input"
              :placeholder="$t('dashboard.forms.placeholder.none')"
            />
          </div>

          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.vpc') }} <span class="text-optional">({{ $t('dashboard.forms.optional') }})</span></label>
            <div class="select-wrapper">
                <select v-model="newGroupForm.vpc_id" class="form-input">
                    <option value="">{{ $t('dashboard.forms.placeholder.none') }}</option>
                    <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">
                        {{ vpc.name }} ({{ vpc.id }})
                    </option>
                </select>
            </div>
          </div>

          <div v-if="newGroupForm.vpc_id" class="form-group form-group-checkbox">
            <label class="checkbox-label">
              <input type="checkbox" v-model="newGroupForm.is_default" class="checkbox-input" />
              <span>{{ $t('dashboard.forms.setAsDefault') }}</span>
              <span class="help-icon-wrap">
                <HelpCircle :size="14" class="help-icon" />
                <span class="help-tooltip">{{ $t('dashboard.forms.setAsDefaultTooltip') }}</span>
              </span>
            </label>
          </div>
        </div>
        <div class="modal-footer" style="flex-direction: column; align-items: stretch; gap: var(--spacing-2);">
          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>
          <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2);">
            <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreateGroup" :disabled="creating">
              <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createSecurityGroup') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Add/Edit Rule Modal -->
    <div v-if="ruleModalVisible" class="modal-overlay" @click.self="closeRuleModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ editingRuleId ? $t('actions.edit') : $t('dashboard.buttons.addRule') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeRuleModal"><X :size="20" /></button>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.name') }}</label>
            <input v-model="newRule.name" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.nameExample')">
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.direction') }}</label>
            <div class="select-wrapper">
              <select v-model="newRule.direction" class="form-input">
                <option value="ingress">{{ $t('dashboard.table.ingress') }}</option>
                <option value="egress">{{ $t('dashboard.table.egress') }}</option>
              </select>
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.protocol') }}</label>
            <div class="select-wrapper">
              <select v-model="newRule.protocol" class="form-input">
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
                <option value="icmp">ICMP</option>
              </select>
            </div>
          </div>
          <div v-if="newRule.protocol !== 'icmp'" class="form-row">
            <div class="form-group flex-1">
              <label class="form-label">{{ $t('dashboard.table.portMin') }}</label>
              <input v-model.number="newRule.port_min" type="number" class="form-input" min="1" max="65535">
            </div>
            <div class="form-group flex-1">
              <label class="form-label">{{ $t('dashboard.table.portMax') }}</label>
              <input v-model.number="newRule.port_max" type="number" class="form-input" min="1" max="65535">
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.remoteCidr') }}</label>
            <input v-model="newRule.remote_cidr" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.cidrExample')">
          </div>
          <div v-if="addRuleError" class="text-error" style="margin-bottom:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ addRuleError }}
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeRuleModal" :disabled="addingRule">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleSaveRule" :disabled="addingRule">
            <span v-if="addingRule" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ addingRule ? (editingRuleId ? $t('messages.saving') : $t('messages.creating')) : (editingRuleId ? $t('actions.save') : $t('dashboard.buttons.addRule')) }}
          </button>
        </div>
      </div>
    </div>

    <!-- Delete Rule Confirmation Modal -->
    <div v-if="deleteModalVisible" class="modal-overlay" @click.self="closeDeleteModal">
      <div class="modal-content card" style="max-width: 460px;">
        <div class="modal-header">
          <h3>{{ $t('actions.delete') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeDeleteModal"><X :size="20" /></button>
        </div>
        <div class="modal-body">
          <div style="text-align:center;padding:var(--spacing-4) 0">
            <div style="width:64px;height:64px;border-radius:50%;background:var(--error-light);display:flex;align-items:center;justify-content:center;margin:0 auto var(--spacing-4);color:var(--error-color)"><Trash2 :size="32" /></div>
            <p style="color:var(--text-secondary);margin:0 0 var(--spacing-4)">{{ $t('dashboard.securityGroupDetail.deleteRuleConfirm') }}</p>
            <div style="background:var(--bg-secondary);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:var(--spacing-3) var(--spacing-4);text-align:left">
              <span style="font-size:var(--font-size-xs);color:var(--text-tertiary);text-transform:uppercase;letter-spacing:0.05em;display:block;margin-bottom:var(--spacing-1)">{{ $t('dashboard.deleteConfirm.resource') }}</span>
              <span style="font-weight:var(--font-weight-semibold);display:block">{{ ruleToDelete?.rule.protocol?.toUpperCase() }} : {{ ruleToDelete?.rule.port_min }}-{{ ruleToDelete?.rule.port_max }}</span>
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ ruleToDelete?.rule.id }}</span>
            </div>
            <div v-if="deleteError" class="text-error" style="margin-top:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
              {{ deleteError }}
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeDeleteModal" :disabled="deletingResource">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-danger" @click="confirmDelete" :disabled="deletingResource">
            <span v-if="deletingResource" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
            <Trash2 v-else :size="14" />
            {{ deletingResource ? $t('dashboard.deleteConfirm.deleting') : $t('actions.delete') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Delete Security Group Confirmation Modal -->
    <div v-if="deleteGroupModalVisible" class="modal-overlay" @click.self="closeDeleteGroupModal">
      <div class="modal-content card" style="max-width: 460px;">
        <div class="modal-header">
          <h3>{{ $t('actions.delete') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeDeleteGroupModal"><X :size="20" /></button>
        </div>
        <div class="modal-body">
          <div style="text-align:center;padding:var(--spacing-4) 0">
            <div style="width:64px;height:64px;border-radius:50%;background:var(--error-light);display:flex;align-items:center;justify-content:center;margin:0 auto var(--spacing-4);color:var(--error-color)"><Shield :size="32" /></div>
            <p style="color:var(--text-secondary);margin:0 0 var(--spacing-4)">{{ $t('dashboard.securityGroupDetail.deleteConfirm') }}</p>
            <div style="background:var(--bg-secondary);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:var(--spacing-3) var(--spacing-4);text-align:left">
              <span style="font-size:var(--font-size-xs);color:var(--text-tertiary);text-transform:uppercase;letter-spacing:0.05em;display:block;margin-bottom:var(--spacing-1)">{{ $t('dashboard.deleteConfirm.resource') }}</span>
              <span style="font-weight:var(--font-weight-semibold);display:block">{{ groupToDelete?.name }}</span>
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ groupToDelete?.id }}</span>
            </div>
            <div v-if="deleteGroupError" class="text-error" style="margin-top:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
              {{ deleteGroupError }}
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeDeleteGroupModal" :disabled="deletingGroup">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-danger" @click="confirmDeleteGroup" :disabled="deletingGroup">
            <span v-if="deletingGroup" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
            <Trash2 v-else :size="14" />
            {{ deletingGroup ? $t('dashboard.deleteConfirm.deleting') : $t('actions.delete') }}
          </button>
        </div>
      </div>
    </div>
  </div>

  <Teleport to="body">
    <div v-if="ifaceTooltipVisible && ifaceTooltipGroup" class="iface-tooltip" :style="ifaceTooltipStyle"
      @mouseenter="ifaceTooltipVisible = true" @mouseleave="hideIfaceTooltip">
      <div v-for="iface in ifaceTooltipGroup.target_interfaces" :key="iface.id" class="iface-tooltip-item">
        <span class="iface-tooltip-name" v-if="iface.name">{{ iface.name }}</span>
        <span class="iface-tooltip-ip">{{ iface.ip_address || '-' }}</span>
        <span class="iface-tooltip-instance" v-if="iface.from_instance">→ {{ iface.from_instance.hostname || iface.from_instance.id }}</span>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.header-actions {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
}

.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-4);
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

.security-groups-list {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-3);
}

.sg-card {
  padding: 0;
  overflow: hidden;
}

/* --- Card Header --- */
.sg-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--spacing-3) var(--spacing-4);
  cursor: pointer;
  transition: background var(--transition-fast);
}

.sg-header:hover {
  background: var(--hover-ui);
}

.sg-header-left {
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
  min-width: 0;
}

.expand-icon {
  flex-shrink: 0;
  color: var(--text-tertiary);
}

.sg-info {
  min-width: 0;
}

.sg-name-row {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  flex-wrap: wrap;
}

.sg-id {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  font-family: var(--font-family-mono);
  margin-top: 2px;
}
.sg-description {
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
  margin-top: 2px;
}

.sg-vpc-badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
  background: var(--gray-100);
  color: var(--text-secondary);
  border: 1px solid var(--border-light);
}

.text-optional {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  font-weight: 400;
}

.form-group-checkbox {
  padding-top: var(--spacing-1);
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  cursor: pointer;
  font-size: var(--font-size-sm);
  color: var(--text-primary);
}

.checkbox-input {
  width: 16px;
  height: 16px;
  cursor: pointer;
  accent-color: var(--primary);
}

.help-icon-wrap {
  position: relative;
  display: inline-flex;
  align-items: center;
  margin-left: 2px;
}

.help-icon {
  color: var(--text-tertiary);
  cursor: default;
  flex-shrink: 0;
}

.help-tooltip {
  display: none;
  position: absolute;
  bottom: calc(100% + 6px);
  left: 50%;
  transform: translateX(-50%);
  background: var(--gray-800, #1f2937);
  color: #fff;
  font-size: var(--font-size-xs);
  font-weight: 400;
  line-height: 1.5;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  white-space: normal;
  width: 220px;
  text-align: left;
  pointer-events: none;
  z-index: 100;
  box-shadow: 0 2px 8px rgba(0,0,0,0.18);
}

.help-tooltip::after {
  content: '';
  position: absolute;
  top: 100%;
  left: 50%;
  transform: translateX(-50%);
  border: 5px solid transparent;
  border-top-color: var(--gray-800, #1f2937);
}

.help-icon-wrap:hover .help-tooltip {
  display: block;
}

.sg-header-right {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  flex-shrink: 0;
}

.rule-count-badge {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  white-space: nowrap;
}

.iface-count-badge {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  white-space: nowrap;
  cursor: default;
}


/* --- Rules Table --- */
.sg-rules {
  border-top: 1px solid var(--border-subtle);
  background: var(--gray-50);
}

.sg-filter-bar {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  padding: var(--spacing-2) var(--spacing-4);
  border-bottom: 1px solid var(--border-light);
  background: var(--bg-secondary, #f9fafb);
}

.sg-filter-select {
  width: 150px;
  flex-shrink: 0;
}

.sg-filter-search {
  position: relative;
  display: flex;
  align-items: center;
  flex: 1;
  max-width: 240px;
}

.sg-filter-search-icon {
  position: absolute;
  left: 9px;
  color: var(--text-tertiary);
  pointer-events: none;
}

.sg-filter-search-input {
  width: 100%;
  padding: 6px 28px 6px 30px;
  border: 1px solid var(--border-light);
  border-radius: 6px;
  background: var(--bg-primary);
  color: var(--text-primary);
  font-size: var(--font-size-sm);
  outline: none;
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
}

.sg-filter-search-input:focus {
  border-color: var(--primary-color);
  box-shadow: 0 0 0 3px var(--primary-100);
}

.sg-filter-search-input::placeholder {
  color: var(--text-light);
}

.filter-clear {
  position: absolute;
  right: 8px;
  background: none;
  border: none;
  padding: 0;
  color: var(--text-tertiary);
  cursor: pointer;
  display: flex;
  align-items: center;
}

.filter-clear:hover {
  color: var(--text-primary);
}

.btn-filter-active {
  background: var(--primary-50);
  color: var(--primary-color);
}

.rules-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-size-sm);
}

.rules-table th {
  text-align: left;
  padding: var(--spacing-2) var(--spacing-4);
  font-weight: var(--font-weight-medium);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  background: var(--gray-100);
}

.rules-table td {
  padding: var(--spacing-2) var(--spacing-4);
  border-bottom: 1px solid var(--border-subtle);
}

.rules-table tr:last-child td {
  border-bottom: none;
}

.actions-cell {
  white-space: nowrap;
  text-align: right;
}

.direction-badge {
  display: inline-block;
  padding: var(--spacing-1) var(--spacing-2);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  text-transform: uppercase;
}

.direction-badge.ingress {
  background: var(--success-light);
  color: var(--success-dark);
}

.direction-badge.egress {
  background: var(--info-light);
  color: var(--info-dark);
}

.protocol {
  font-family: var(--font-family-mono);
  font-weight: var(--font-weight-medium);
}

.port {
  font-family: var(--font-family-mono);
}

.cidr {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
}

.sortable-th {
  cursor: pointer;
  user-select: none;
}

.sortable-th:hover {
  color: var(--primary-color);
}

.sortable-th svg {
  vertical-align: middle;
  margin-left: 4px;
}

.sort-idle {
  opacity: 0.3;
}

.service-tag {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 600;
  background: var(--primary-50);
  color: var(--primary-700);
  margin-left: 6px;
  vertical-align: middle;
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */
.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 20px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    transition: background var(--transition-base);
}
.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.resource-link {
  color: var(--primary-600);
  cursor: pointer;
  font-weight: var(--font-weight-semibold);
  font-size: var(--font-size-sm);
}

.resource-link:hover {
  text-decoration: underline;
}

.form-row {
  display: flex;
  gap: 16px;
}
.flex-1 { flex: 1; }
</style>

<style>
.iface-tooltip {
  position: fixed;
  transform: translateX(-100%);
  background: var(--gray-900);
  color: var(--gray-100);
  border-radius: var(--radius-md);
  padding: 8px 12px;
  font-size: var(--font-size-xs);
  white-space: nowrap;
  z-index: 1000;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
}

.iface-tooltip-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 3px 0;
}

.iface-tooltip-item + .iface-tooltip-item {
  border-top: 1px solid var(--gray-700);
}

.iface-tooltip-ip {
  font-family: var(--font-family-mono);
}

.iface-tooltip-name {
  font-weight: 500;
  color: var(--gray-200);
}

.iface-tooltip-instance {
  color: var(--gray-400);
}
</style>
