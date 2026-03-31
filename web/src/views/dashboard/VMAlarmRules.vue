<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, Search, ShieldAlert, Link, RefreshCw, X } from 'lucide-vue-next'
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
    level: 'warning',
    type: 'cpu' as VMRuleType,
    rules: [{ name: '', limit: 80, duration: 5, rule: 'gt' }] as Record<string, any>[],
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
        level: 'warning',
        type: 'cpu',
        rules: [{ name: '', limit: 80, duration: 5, rule: 'gt' }],
    }
    showCreateModal.value = true
}

const onTypeChange = () => {
    if (createForm.value.type === 'bw') {
        createForm.value.rules = [{ direction: 'in', name: '', limit: 80, duration: 5 }]
    } else {
        createForm.value.rules = [{ name: '', limit: 80, duration: 5, rule: 'gt' }]
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
            level: createForm.value.level,
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
        createForm.value.rules.push({ direction: 'out', name: '', limit: 80, duration: 5 })
    } else {
        createForm.value.rules.push({ name: '', limit: 80, duration: 5, rule: 'gt' })
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

        <div class="card table-card">
            <table class="data-table">
                <thead>
                    <tr>
                        <th>{{ t('dashboard.table.name') }}</th>
                        <th>{{ t('dashboard.table.type') }}</th>
                        <th>{{ t('dashboard.vmAlarmRules.ruleId') }}</th>
                        <th>{{ t('dashboard.vmAlarmRules.level') }}</th>
                        <th>{{ t('dashboard.vmAlarmRules.linkedVMs') }}</th>
                        <th>{{ t('dashboard.table.status') }}</th>
                        <th>{{ t('dashboard.table.actions') }}</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-if="loading">
                        <td colspan="7" class="text-center">
                            <div class="loading-spinner" style="margin: 20px auto;"></div>
                        </td>
                    </tr>
                    <tr v-else-if="filteredRules.length === 0">
                        <td colspan="7" class="text-center text-secondary" style="padding: 48px;">
                            <div class="empty-state">
                                <ShieldAlert :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                                <p>{{ t('messages.noData') }}</p>
                            </div>
                        </td>
                    </tr>
                    <tr v-else v-for="rule in filteredRules" :key="rule.rule_id">
                        <td>{{ rule.name }}</td>
                        <td>
                            <span class="badge badge-secondary" style="text-transform: uppercase;">
                                {{ rule.type ? t('dashboard.vmAlarmRules.ruleTypes.' + rule.type) : '-' }}
                            </span>
                        </td>
                        <td class="monospace">{{ rule.rule_id }}</td>
                        <td>
                            <span class="badge" :class="'badge-' + rule.level">
                                {{ t('dashboard.vmAlarmRules.levels.' + rule.level) }}
                            </span>
                        </td>
                        <td>{{ t('dashboard.vmAlarmRules.linkedCount', { count: rule.linkedvms?.length || 0 }) }}</td>
                        <td>
                            <span class="badge" :class="rule.enable ? 'badge-success' : 'badge-muted'">
                                {{ rule.enable ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                            </span>
                        </td>
                        <td class="actions-cell">
                            <button class="icon-btn-table" @click="openBindChannels(rule)" :title="t('dashboard.vmAlarmRules.bindChannels')">
                                <Link :size="16" />
                            </button>
                            <button class="icon-btn-table text-error" @click="confirmDelete(rule)">
                                <Trash2 :size="16" />
                            </button>
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>

        <!-- Pagination -->
        <div v-if="totalPages > 1" class="pagination">
            <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="prevPage">Prev</button>
            <span class="page-info">{{ page }} / {{ totalPages }} ({{ total }} total)</span>
            <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="nextPage">Next</button>
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
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.vmAlarmRules.level') }}</label>
                                <select v-model="createForm.level" class="form-input">
                                    <option value="critical">{{ t('dashboard.vmAlarmRules.levels.critical') }}</option>
                                    <option value="warning">{{ t('dashboard.vmAlarmRules.levels.warning') }}</option>
                                    <option value="info">{{ t('dashboard.vmAlarmRules.levels.info') }}</option>
                                </select>
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
                                    <input v-model.number="rule.limit" type="number" class="form-input rule-input-sm" :placeholder="$t('dashboard.forms.placeholder.limitPercentExample')" min="1" max="100" />
                                    <input v-model.number="rule.duration" type="number" class="form-input rule-input-sm" :placeholder="t('dashboard.vmAlarmRules.durationMin')" min="1" />
                                    <button v-if="createForm.rules.length > 1" class="icon-btn-table text-error" @click="removeRuleRow(idx)">
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

.monospace { font-family: var(--font-family-mono, monospace); font-size: 13px; }

.actions-cell { display: flex; gap: 4px; align-items: center; }

.icon-btn-table {
    width: 32px; height: 32px; border-radius: 8px; border: none;
    background: transparent; color: var(--text-tertiary);
    display: flex; align-items: center; justify-content: center;
    cursor: pointer; transition: all 0.2s;
}

.icon-btn-table:hover { background-color: var(--bg-tertiary); color: var(--primary-500); }
.icon-btn-table.text-error:hover { background-color: #fef2f2; color: #ef4444; }
.text-error { color: var(--text-tertiary); }

.pagination { display: flex; align-items: center; justify-content: center; gap: 12px; padding: 16px 0; }
.page-info { font-size: 13px; color: #6b7280; }

.badge-critical { background: #dc2626; color: white; }
.badge-warning { background: #f59e0b; color: white; }
.badge-info { background: #3b82f6; color: white; }
.badge-success { background: #22c55e; color: white; }
.badge-muted { background: #6b7280; color: white; }
.badge-secondary { background: #8b5cf6; color: white; }
.text-danger { color: #ef4444; }
.text-center { text-align: center; }
.text-muted { color: #9ca3af; }

.error-banner {
    background: #fef2f2; color: #dc2626; border: 1px solid #fecaca;
    border-radius: 6px; padding: 10px 14px; margin-bottom: 12px;
    font-size: 13px; cursor: pointer;
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
    background: #ef4444; color: white; border: none;
    padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}
.btn-danger:hover { background: #dc2626; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
