<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { securityGroupsApi, type SecurityGroup, type SecurityRule } from '../../api/networks'
import { ArrowLeft, Shield, Trash2, Plus, X, Edit, ArrowUpDown, ArrowUp, ArrowDown, Network, Server } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const toast = useToast()
const groupId = route.params.id as string

const group = ref<SecurityGroup | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)
const addingRule = ref(false)
const editingRuleId = ref<string | null>(null)
const addRuleError = ref('')

const isEditingName = ref(false)
const newName = ref('')
const isEditingDesc = ref(false)
const newDesc = ref('')
const savingInfo = ref(false)

const showAddRuleModal = ref(false)
const newRule = ref({
    name: '',
    direction: 'ingress',
    protocol: 'tcp',
    port_min: 80,
    port_max: 80,
    remote_cidr: '0.0.0.0/0'
})

type SortKey = 'name' | 'direction' | 'protocol' | 'port' | 'remote_cidr'
type SortOrder = 'asc' | 'desc'
const sortKey = ref<SortKey>('direction')
const sortOrder = ref<SortOrder>('asc')

const toggleSort = (key: SortKey) => {
    if (sortKey.value === key) {
        sortOrder.value = sortOrder.value === 'asc' ? 'desc' : 'asc'
    } else {
        sortKey.value = key
        sortOrder.value = 'asc'
    }
}

const sortedRules = computed(() => {
    const rules = group.value?.security_rules || []
    return [...rules].sort((a, b) => {
        const dir = sortOrder.value === 'asc' ? 1 : -1
        switch (sortKey.value) {
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
})

const fetchGroup = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await securityGroupsApi.get(groupId)
        group.value = response
    } catch (err) {
        console.error('Failed to fetch security group:', err)
        error.value = t('dashboard.securityGroupDetail.loadError')
    } finally {
        loading.value = false
    }
}

const handleDelete = async () => {
    if (!confirm(t('dashboard.securityGroupDetail.deleteConfirm'))) return
    
    deleting.value = true
    try {
        await securityGroupsApi.delete(groupId)
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'security-groups' })
    } catch (err) {
        console.error('Failed to delete security group:', err)
        alert(t('messages.error'))
        deleting.value = false
    }
}

const handleDeleteRule = async (ruleId: string) => {
    if (!confirm(t('dashboard.securityGroupDetail.deleteRuleConfirm'))) return
    try {
        await securityGroupsApi.deleteRule(groupId, ruleId)
        await fetchGroup()
        toast.success(t('messages.deleteSuccess'))
    } catch (err) {
        console.error('Failed to delete rule:', err)
    }
}

const openAddRuleModal = () => {
    editingRuleId.value = null
    newRule.value = {
        name: '',
        direction: 'ingress',
        protocol: 'tcp',
        port_min: 80,
        port_max: 80,
        remote_cidr: '0.0.0.0/0'
    }
    showAddRuleModal.value = true
}

const openEditRuleModal = (rule: SecurityRule) => {
    editingRuleId.value = rule.id
    newRule.value = {
        name: rule.name || '',
        direction: rule.direction,
        protocol: rule.protocol,
        port_min: rule.protocol === 'icmp' ? 1 : (rule.port_min || 1),
        port_max: rule.protocol === 'icmp' ? 65535 : (rule.port_max || 65535),
        remote_cidr: rule.remote_cidr || ''
    }
    showAddRuleModal.value = true
}

const handleAddRule = async () => {
    addRuleError.value = ''
    addingRule.value = true
    try {
        if (editingRuleId.value) {
            await securityGroupsApi.patchRule(groupId, editingRuleId.value, newRule.value as any)
        } else {
            await securityGroupsApi.addRule(groupId, newRule.value as any)
        }
        showAddRuleModal.value = false
        await fetchGroup()
        toast.success(editingRuleId.value ? t('messages.updateSuccess') : t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to save rule:', err)
        addRuleError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        addingRule.value = false
    }
}

const startEditName = () => {
    newName.value = group.value?.name || ''
    isEditingName.value = true
}

const saveInfo = async () => {
    const payload: { name?: string; description?: string } = {}
    if (isEditingName.value && newName.value && newName.value !== group.value?.name) {
        payload.name = newName.value
    }
    if (isEditingDesc.value && newDesc.value !== group.value?.description) {
        payload.description = newDesc.value
    }

    if (Object.keys(payload).length === 0) {
        isEditingName.value = false
        isEditingDesc.value = false
        return
    }

    savingInfo.value = true
    try {
        await securityGroupsApi.patch(groupId, payload)
        if (group.value) {
            if (payload.name) group.value.name = payload.name
            if (payload.description !== undefined) group.value.description = payload.description
        }
        isEditingName.value = false
        isEditingDesc.value = false
        toast.success(t('messages.updateSuccess'))
    } catch (err) {
        console.error('Failed to update info:', err)
        alert(t('messages.error'))
    } finally {
        savingInfo.value = false
    }
}

const startEditDesc = () => {
    newDesc.value = group.value?.description || ''
    isEditingDesc.value = true
}

const goBack = () => {
    router.back()
}

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

onMounted(fetchGroup)
</script>

<template>
    <div class="detail-page">
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
            <button class="btn btn-primary" @click="fetchGroup">{{ $t('actions.retry') }}</button>
        </div>

        <div v-else-if="group" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="resource-icon">
                    <Shield :size="24" />
                </div>
                <div class="title-info">
                    <h1>{{ group.name }}</h1>
                    <div class="subtitle">
                        <span class="id-text">{{ group.id }}</span>
                        <span v-if="group.is_default" class="badge badge-primary">{{ $t('dashboard.table.default') }}</span>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-secondary" @click="startEditName" style="margin-right: var(--spacing-2)">
                        <Edit :size="16" /> {{ $t('actions.edit') }}
                    </button>
                    <button class="btn btn-danger" @click="handleDelete" :disabled="deleting || group.is_default">
                        <Trash2 :size="16" /> {{ deleting ? $t('messages.deleting') : $t('actions.delete') }}
                    </button>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                     <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.name') }}</span>
                            <div v-if="isEditingName" class="edit-name-group">
                                <input v-model="newName" type="text" class="form-input form-input-sm" @keyup.enter="saveInfo" @keyup.esc="isEditingName = false">
                                <button class="btn btn-primary btn-sm" @click="saveInfo" :disabled="savingInfo">{{ $t('actions.save') }}</button>
                                <button class="btn btn-ghost btn-sm" @click="isEditingName = false" :disabled="savingInfo">{{ $t('actions.cancel') }}</button>
                            </div>
                            <div v-else class="value-with-edit">
                                <span class="value">{{ group.name }}</span>
                                <button class="btn-icon-link" @click="startEditName"><Edit :size="12" /></button>
                            </div>
                        </div>
                        <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.description') }}</span>
                            <div v-if="isEditingDesc" class="edit-name-group">
                                <input v-model="newDesc" type="text" class="form-input form-input-sm" @keyup.enter="saveInfo" @keyup.esc="isEditingDesc = false">
                                <button class="btn btn-primary btn-sm" @click="saveInfo" :disabled="savingInfo">{{ $t('actions.save') }}</button>
                                <button class="btn btn-ghost btn-sm" @click="isEditingDesc = false" :disabled="savingInfo">{{ $t('actions.cancel') }}</button>
                            </div>
                            <div v-else class="value-with-edit">
                                <span class="value">{{ group.description || '-' }}</span>
                                <button class="btn-icon-link" @click="startEditDesc"><Edit :size="12" /></button>
                            </div>
                        </div>
                        <div class="kv-item">
                              <span class="label">{{ $t('dashboard.table.vpc') }}</span>
                             <span class="value" v-if="group.vpc">
                                <router-link :to="{name: 'vpc-detail', params: {id: group.vpc.id}}" class="text-link">
                                    {{ group.vpc.name }}
                                </router-link>
                            </span>
                            <span class="value" v-else>-</span>
                        </div>
                        <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.createdAt') }}</span>
                            <span class="value">{{ group.created_at || '-' }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Associated Interfaces -->
            <div class="card interfaces-card">
                <div class="card-header">
                    <h3>{{ $t('dashboard.securityGroupDetail.associatedInterfaces') }}</h3>
                </div>
                <div class="table-responsive">
                    <table class="data-table">
                        <thead>
                            <tr>
                                <th>{{ $t('dashboard.table.name') }}</th>
                                <th>{{ $t('dashboard.securityGroupDetail.ipAddress') }}</th>
                                <th>{{ $t('dashboard.securityGroupDetail.instance') }}</th>
                                <th>{{ $t('dashboard.floatingIPDetail.interfaceId') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-if="!group.target_interfaces?.length">
                                <td colspan="4" class="text-center text-secondary">{{ $t('dashboard.securityGroupDetail.noInterfaces') }}</td>
                            </tr>
                            <tr v-else v-for="iface in group.target_interfaces" :key="iface.id">
                                <td>{{ iface.name || '-' }}</td>
                                <td>
                                    <div class="iface-cell">
                                        <Network :size="14" class="text-secondary" />
                                        <span class="mono">{{ iface.ip_address || '-' }}</span>
                                    </div>
                                </td>
                                <td>
                                    <div v-if="iface.from_instance" class="iface-cell">
                                        <Server :size="14" class="text-secondary" />
                                        <router-link :to="{ name: 'instance-detail', params: { id: iface.from_instance.id } }" class="text-link">
                                            {{ iface.from_instance.hostname || iface.from_instance.id }}
                                        </router-link>
                                    </div>
                                    <span v-else class="text-secondary">-</span>
                                </td>
                                <td class="mono text-secondary">{{ iface.id }}</td>
                            </tr>
                        </tbody>
                    </table>
                </div>
            </div>

            <!-- Rules -->
            <div class="card rules-card">
                <div class="card-header">
                     <h3>{{ $t('dashboard.table.securityRules') }}</h3>
                     <button class="btn btn-primary btn-sm" @click="openAddRuleModal">
                        <Plus :size="14" /> {{ $t('dashboard.buttons.addRule') }}
                    </button>
                </div>
                <div class="table-responsive">
                    <table class="data-table">
                        <thead>
                            <tr>
                                 <th class="sortable-th" @click="toggleSort('name')">
                                     {{ $t('dashboard.table.name') }}
                                     <ArrowUp v-if="sortKey === 'name' && sortOrder === 'asc'" :size="12" />
                                     <ArrowDown v-else-if="sortKey === 'name' && sortOrder === 'desc'" :size="12" />
                                     <ArrowUpDown v-else :size="12" class="sort-idle" />
                                 </th>
                                 <th class="sortable-th" @click="toggleSort('direction')">
                                     {{ $t('dashboard.table.direction') }}
                                     <ArrowUp v-if="sortKey === 'direction' && sortOrder === 'asc'" :size="12" />
                                     <ArrowDown v-else-if="sortKey === 'direction' && sortOrder === 'desc'" :size="12" />
                                     <ArrowUpDown v-else :size="12" class="sort-idle" />
                                 </th>
                                 <th class="sortable-th" @click="toggleSort('protocol')">
                                     {{ $t('dashboard.table.protocol') }}
                                     <ArrowUp v-if="sortKey === 'protocol' && sortOrder === 'asc'" :size="12" />
                                     <ArrowDown v-else-if="sortKey === 'protocol' && sortOrder === 'desc'" :size="12" />
                                     <ArrowUpDown v-else :size="12" class="sort-idle" />
                                 </th>
                                 <th class="sortable-th" @click="toggleSort('port')">
                                     {{ $t('dashboard.table.portRange') }}
                                     <ArrowUp v-if="sortKey === 'port' && sortOrder === 'asc'" :size="12" />
                                     <ArrowDown v-else-if="sortKey === 'port' && sortOrder === 'desc'" :size="12" />
                                     <ArrowUpDown v-else :size="12" class="sort-idle" />
                                 </th>
                                 <th class="sortable-th" @click="toggleSort('remote_cidr')">
                                     {{ $t('dashboard.table.remoteCidr') }}
                                     <ArrowUp v-if="sortKey === 'remote_cidr' && sortOrder === 'asc'" :size="12" />
                                     <ArrowDown v-else-if="sortKey === 'remote_cidr' && sortOrder === 'desc'" :size="12" />
                                     <ArrowUpDown v-else :size="12" class="sort-idle" />
                                 </th>
                                 <th>{{ $t('dashboard.table.actions') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-if="!sortedRules.length">
                                 <td colspan="6" class="text-center text-secondary">{{ $t('messages.noData') }}</td>
                            </tr>
                            <tr v-else v-for="rule in sortedRules" :key="rule.id">
                                <td>{{ rule.name || '-' }}</td>
                                <td>
                                    <span :class="['direction-badge', rule.direction]">
                                         {{ rule.direction === 'ingress' ? $t('dashboard.table.ingress') : $t('dashboard.table.egress') }}
                                    </span>
                                </td>
                                <td class="mono">{{ rule.protocol.toUpperCase() }}</td>
                                <td class="mono">{{ formatPort(rule) }} <span v-if="getServiceName(rule)" class="service-tag">{{ getServiceName(rule) }}</span></td>
                                <td class="mono">{{ rule.remote_cidr }}</td>
                                <td>
                                    <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="openEditRuleModal(rule)">
                                        <Edit :size="14" />
                                    </button>
                                    <button class="btn btn-ghost btn-sm text-error" @click="handleDeleteRule(rule.id)">
                                        <Trash2 :size="14" />
                                    </button>
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
            </div>
        </div>

        <!-- Add Rule Modal -->
        <div v-if="showAddRuleModal" class="modal-overlay" @click.self="showAddRuleModal = false">
             <div class="modal-content card">
                <div class="modal-header">
                     <h3>{{ editingRuleId ? $t('actions.edit') : $t('dashboard.buttons.addRule') }}</h3>
                    <button class="btn btn-ghost btn-sm icon-btn" @click="showAddRuleModal = false"><X :size="20" /></button>
                </div>
                <div class="modal-body">
                    <div class="form-group">
                         <label class="form-label">{{ $t('dashboard.table.name') }}</label>
                         <input v-model="newRule.name" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.nameExample')">
                    </div>
                    <div class="form-group">
                         <label class="form-label">{{ $t('dashboard.table.direction') }}</label>
                        <select v-model="newRule.direction" class="form-input">
                             <option value="ingress">{{ $t('dashboard.table.ingress') }}</option>
                            <option value="egress">{{ $t('dashboard.table.egress') }}</option>
                        </select>
                    </div>
                    <div class="form-group">
                         <label class="form-label">{{ $t('dashboard.table.protocol') }}</label>
                        <select v-model="newRule.protocol" class="form-input">
                            <option value="tcp">TCP</option>
                            <option value="udp">UDP</option>
                            <option value="icmp">ICMP</option>
                        </select>
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
                     <button class="btn btn-secondary" @click="showAddRuleModal = false">{{ $t('actions.cancel') }}</button>
                    <button class="btn btn-primary" @click="handleAddRule" :disabled="addingRule">
                          {{ addingRule ? (editingRuleId ? $t('messages.saving') : $t('messages.creating')) : (editingRuleId ? $t('actions.save') : $t('dashboard.buttons.addRule')) }}
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
    align-items: center;
    gap: var(--spacing-4);
    padding: var(--spacing-6);
    margin-bottom: var(--spacing-6);
}

.resource-icon {
    width: 48px;
    height: 48px;
    background: var(--bg-tertiary);
    color: var(--primary-color);
    border-radius: var(--radius-md);
    display: flex;
    align-items: center;
    justify-content: center;
}

.title-info {
    flex: 1;
}

.title-info h1 {
    font-size: var(--font-size-xl);
    font-weight: 600;
    margin: 0 0 4px 0;
    color: var(--text-primary);
}

.subtitle {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    font-size: var(--font-size-sm);
}

.id-text {
    font-family: var(--font-family-mono);
    color: var(--text-secondary);
}

.badge {
    background: var(--primary-50);
    color: var(--primary-700);
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 11px;
    font-weight: 600;
}

/* Info Grid */
.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-6);
}

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
}

.kv-item .value {
    color: var(--text-primary);
    font-weight: 500;
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}

.text-link:hover {
    text-decoration: underline;
}

.value-with-edit {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.btn-icon-link {
    background: none;
    border: none;
    padding: 0;
    color: var(--text-tertiary);
    cursor: pointer;
    display: flex;
    align-items: center;
    transition: color var(--transition-fast);
}

.btn-icon-link:hover {
    color: var(--primary-color);
}

/* Rules Card */
.rules-card {
    padding: 0;
    overflow: hidden;
}

.card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--spacing-4) var(--spacing-5);
    border-bottom: 1px solid var(--border-light);
}

.card-header h3 {
    margin: 0;
    font-size: var(--font-size-md);
}

.table-responsive {
    overflow-x: auto;
}

.direction-badge {
    display: inline-block;
    padding: 2px 6px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
}

.direction-badge.ingress { background: var(--success-50); color: var(--success-dark); }
.direction-badge.egress { background: var(--info-50); color: var(--info-dark); }

.mono {
    font-family: var(--font-family-mono);
}

.sortable-th {
    cursor: pointer;
    user-select: none;
    display: table-cell;
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
    font-family: var(--font-family-base);
    background: var(--primary-50);
    color: var(--primary-700);
    margin-left: 6px;
    vertical-align: middle;
}

/* Modal */
.form-group {
    margin-bottom: 16px;
}

.form-row {
    display: flex;
    gap: 16px;
}
.flex-1 { flex: 1; }

.form-label {
    display: block;
    margin-bottom: 6px;
    font-size: 14px;
    font-weight: 500;
    color: var(--text-secondary);
}

.form-input {
    width: 100%;
    padding: 8px 12px;
    border: 1px solid var(--border-light);
    border-radius: 6px;
     background: var(--bg-primary);
    color: var(--text-primary);
}

/* Interfaces Card */
.interfaces-card {
    padding: 0;
    overflow: hidden;
    margin-top: var(--spacing-4);
}

.iface-cell {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}
</style>
