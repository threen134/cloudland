<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { securityGroupsApi, type SecurityGroup, type SecurityRule } from '../../api/networks'
import { ArrowLeft, Shield, Trash2, Plus, X } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const groupId = route.params.id as string

const group = ref<SecurityGroup | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)
const addingRule = ref(false)
const addRuleError = ref('')

const showAddRuleModal = ref(false)
const newRule = ref({
    direction: 'ingress',
    protocol: 'tcp',
    port_min: 80,
    port_max: 80,
    remote_cidr: '0.0.0.0/0'
})

const fetchGroup = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await securityGroupsApi.get(groupId)
        group.value = response
    } catch (err) {
        console.error('Failed to fetch security group:', err)
        error.value = 'Failed to load security group details.'
    } finally {
        loading.value = false
    }
}

const handleDelete = async () => {
    if (!confirm('Are you sure you want to delete this security group? This action cannot be undone.')) return
    
    deleting.value = true
    try {
        await securityGroupsApi.delete(groupId)
        router.push({ name: 'security-groups' })
    } catch (err) {
        console.error('Failed to delete security group:', err)
        alert('Failed to delete security group.')
        deleting.value = false
    }
}

const handleDeleteRule = async (ruleId: string) => {
    if (!confirm('Delete this rule?')) return
    try {
        await securityGroupsApi.deleteRule(groupId, ruleId)
        await fetchGroup()
    } catch (err) {
        console.error('Failed to delete rule:', err)
    }
}

const handleAddRule = async () => {
    addRuleError.value = ''
    addingRule.value = true
    try {
        await securityGroupsApi.addRule(groupId, newRule.value as any)
        showAddRuleModal.value = false
        await fetchGroup()
    } catch (err: any) {
        console.error('Failed to add rule:', err)
        addRuleError.value = err.response?.data?.error_message || err.message || 'Failed to add rule.'
    } finally {
        addingRule.value = false
    }
}

const goBack = () => {
    router.back()
}

const formatPort = (rule: SecurityRule) => {
    if (rule.port_min === rule.port_max) {
        return rule.port_min?.toString() || 'All'
    }
    if (rule.port_min === 1 && rule.port_max === 65535) {
        return 'All'
    }
    return `${rule.port_min}-${rule.port_max}`
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
                        <span v-if="group.is_default" class="badge badge-primary">{{ $t('dashboard.securityGroups.default') }}</span>
                    </div>
                </div>
                <div class="title-actions">
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
                            <span class="value">{{ group.name }}</span>
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

            <!-- Rules -->
            <div class="card rules-card">
                <div class="card-header">
                     <h3>{{ $t('dashboard.table.securityRules') }}</h3>
                     <button class="btn btn-primary btn-sm" @click="showAddRuleModal = true">
                        <Plus :size="14" /> {{ $t('dashboard.buttons.addRule') }}
                    </button>
                </div>
                <div class="table-responsive">
                    <table class="data-table">
                        <thead>
                            <tr>
                                 <th>{{ $t('dashboard.table.direction') }}</th>
                                 <th>{{ $t('dashboard.table.protocol') }}</th>
                                 <th>{{ $t('dashboard.table.portRange') }}</th>
                                 <th>{{ $t('dashboard.table.remoteCidr') }}</th>
                                 <th>{{ $t('dashboard.table.actions') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-if="!group.security_rules?.length">
                                 <td colspan="5" class="text-center text-secondary">{{ $t('messages.noData') }}</td>
                            </tr>
                            <tr v-else v-for="rule in group.security_rules" :key="rule.id">
                                <td>
                                    <span :class="['direction-badge', rule.direction]">
                                         {{ rule.direction === 'ingress' ? $t('dashboard.table.ingress') : $t('dashboard.table.egress') }}
                                    </span>
                                </td>
                                <td class="mono">{{ rule.protocol.toUpperCase() }}</td>
                                <td class="mono">{{ formatPort(rule) }}</td>
                                <td class="mono">{{ rule.remote_cidr }}</td>
                                <td>
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
                     <h3>{{ $t('dashboard.buttons.addRule') }}</h3>
                    <button class="btn btn-ghost btn-sm icon-btn" @click="showAddRuleModal = false"><X :size="20" /></button>
                </div>
                <div class="modal-body">
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
                    <div class="form-row">
                        <div class="form-group flex-1">
                             <label class="form-label">{{ $t('dashboard.table.portMin') }}</label>
                            <input v-model.number="newRule.port_min" type="number" class="form-input">
                        </div>
                        <div class="form-group flex-1">
                             <label class="form-label">{{ $t('dashboard.table.portMax') }}</label>
                            <input v-model.number="newRule.port_max" type="number" class="form-input">
                        </div>
                    </div>
                    <div class="form-group">
                         <label class="form-label">{{ $t('dashboard.table.remoteCidr') }}</label>
                        <input v-model="newRule.remote_cidr" type="text" class="form-input" placeholder="0.0.0.0/0">
                    </div>
                    <div v-if="addRuleError" class="text-error" style="margin-bottom:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
                        {{ addRuleError }}
                    </div>
                </div>
                 <div class="modal-footer">
                     <button class="btn btn-secondary" @click="showAddRuleModal = false">{{ $t('actions.cancel') }}</button>
                    <button class="btn btn-primary" @click="handleAddRule" :disabled="addingRule">
                          {{ addingRule ? $t('dashboard.migrationForm.starting') : $t('dashboard.buttons.addRule') }}
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
</style>
