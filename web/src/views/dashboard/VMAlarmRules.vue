<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, Search, ShieldAlert, Link, RefreshCw, X, ChevronDown, ChevronRight } from 'lucide-vue-next'
import { vmAlarmRulesApi, VM_RULE_TYPES, type VMAlarmRuleGroup, type VMRuleType } from '../../api/vmAlarmRules'
import { alarmEventsApi } from '../../api/alarmEvents'
import { notificationsApi, type NotificationChannel } from '../../api/notifications'
import { useAuthStore } from '../../stores/auth'
import { useRegionStore } from '../../stores/region'

const { t } = useI18n()
const auth = useAuthStore()
const regionStore = useRegionStore()
const rules = ref<VMAlarmRuleGroup[]>([])
const loading = ref(false)
const errorMsg = ref('')
const searchQuery = ref('')
const page = ref(1)
const pageSize = ref(100) // Increase pageSize to fetch more rules at once
const totalPages = ref(1)
const total = ref(0)

// Create modal
const showCreateModal = ref(false)
const createForm = ref({
    name: '',
    rule_id: '',
    type: 'cpu' as VMRuleType,
    rules: [{ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' }] as Record<string, any>[],
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

// Expansion logic
const expandedRules = ref<string[]>([])
const toggleRule = (id: string) => {
    const idx = expandedRules.value.indexOf(id)
    if (idx === -1) expandedRules.value.push(id)
    else expandedRules.value.splice(idx, 1)
}
const isExpanded = (id: string) => expandedRules.value.includes(id)

const filteredRules = computed(() => {
    if (!searchQuery.value) return rules.value
    const q = searchQuery.value.toLowerCase()
    return rules.value.filter(r =>
        r.name.toLowerCase().includes(q) || r.rule_id.toLowerCase().includes(q)
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

const openCreate = () => {
    createForm.value = {
        name: '',
        rule_id: '',
        type: 'cpu',
        rules: [{ name: '', limit: 80, duration: 5, rule: 'gt', level: 'warning' }],
    }
    showCreateModal.value = true
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
        const owner = auth.user?.username || ''
        const regionUuid = regionStore.currentRegionId || ''

        const base = {
            name: createForm.value.name,
            owner,
            rule_id: createForm.value.rule_id || createForm.value.name.replace(/\s+/g, '_').toLowerCase(),
            region_id: regionUuid,
        }

        if (createForm.value.type === 'cpu') {
            await vmAlarmRulesApi.createCPURule({ ...base, rules: createForm.value.rules as any })
        } else if (createForm.value.type === 'memory') {
            await vmAlarmRulesApi.createMemoryRule({ ...base, rules: createForm.value.rules as any })
        } else if (createForm.value.type === 'bw') {
            await vmAlarmRulesApi.createBWRule({ ...base, enable: true, rules: createForm.value.rules as any })
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

const saveBindings = async () => {
    if (!bindTarget.value) return
    try {
        await alarmEventsApi.bindRuleChannels(bindTarget.value.rule_id, selectedChannelUuids.value)
        showBindModal.value = false
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
            <div v-for="rule in filteredRules" :key="rule.rule_id" class="card rule-card" :class="{ 'rule-card-expanded': isExpanded(rule.rule_id) }">
                <div class="rule-header" @click="toggleRule(rule.rule_id)">
                    <div class="rule-header-left">
                        <component :is="isExpanded(rule.rule_id) ? ChevronDown : ChevronRight" :size="16" class="expand-icon" />
                        <div class="rule-info">
                            <div class="rule-name-row">
                                <span class="rule-name-text">{{ rule.name }}</span>
                                <span class="badge badge-secondary" style="text-transform: uppercase;">
                                    {{ rule.type ? t('dashboard.vmAlarmRules.ruleTypes.' + rule.type) : '-' }}
                                </span>
                                <span class="badge" :class="rule.enable ? 'badge-success' : 'status-stopped'">
                                    {{ rule.enable ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                                </span>
                            </div>
                            <div class="rule-id monospace">{{ rule.rule_id }}</div>
                        </div>
                    </div>
                    <div class="rule-header-right">
                        <div class="rule-summary-stats">
                            <div class="level-badges">
                                <span v-for="(r, idx) in (rule.rules || [])" :key="idx"
                                    class="badge" :class="'badge-' + (r.level === 'critical' ? 'error' : (r.level === 'warning' ? 'warning' : 'primary'))"
                                    style="margin-right: 4px;">
                                    {{ t('dashboard.vmAlarmRules.levels.' + r.level) }}
                                </span>
                            </div>
                            <span class="linked-badge">{{ t('dashboard.vmAlarmRules.linkedCount', { count: rule.linkedvms?.length || 0 }) }}</span>
                        </div>
                        <div class="rule-actions">
                            <button class="icon-btn-table" @click.stop="openBindChannels(rule)" :title="t('dashboard.vmAlarmRules.bindChannels')">
                                <Link :size="16" />
                            </button>
                            <button class="icon-btn-table text-error" @click.stop="confirmDelete(rule)">
                                <Trash2 :size="16" />
                            </button>
                        </div>
                    </div>
                </div>

                <div v-show="isExpanded(rule.rule_id)" class="rule-body">
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
                        <h3>{{ t('dashboard.vmAlarmRules.createTitle', { type: createForm.type.toUpperCase() }) }}</h3>
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
                                <input v-model="createForm.name" class="form-input" required />
                            </div>
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.vmAlarmRules.ruleId') }}</label>
                                <input v-model="createForm.rule_id" class="form-input" :placeholder="t('dashboard.vmAlarmRules.ruleIdPlaceholder')" />
                            </div>
                            <div class="rules-section">
                                <div class="rules-header">
                                    <label class="form-label">{{ t('dashboard.vmAlarmRules.thresholds') }}</label>
                                    <button class="btn btn-ghost btn-sm" @click="addRuleRow">
                                        <Plus :size="14" /> {{ t('actions.add') }}
                                    </button>
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
                        <button class="btn btn-primary" @click="saveBindings" :disabled="bindLoading">{{ t('actions.save') }}</button>
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

.level-badges {
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
.rule-row { display: flex; gap: 8px; margin-bottom: 6px; align-items: center; }
.rule-input-sm { width: 120px; }

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
