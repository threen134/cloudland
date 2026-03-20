<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, Search, ShieldAlert, Link, Unlink } from 'lucide-vue-next'
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
const selectedType = ref<VMRuleType>('cpu')
const searchQuery = ref('')
const page = ref(1)
const pageSize = ref(20)
const totalPages = ref(1)
const total = ref(0)

// Create modal
const showCreateModal = ref(false)
const createForm = ref({
    name: '',
    rule_id: '',
    level: 'warning',
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
        const res = await vmAlarmRulesApi.listRules(selectedType.value, {
            page: page.value,
            page_size: pageSize.value,
        })
        rules.value = res.data.data || []
        total.value = res.data.meta?.total || 0
        totalPages.value = res.data.meta?.total_pages || 1
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
        rules: selectedType.value === 'bw'
            ? [{ direction: 'in', name: '', limit: 80, duration: 5 }]
            : [{ name: '', limit: 80, duration: 5, rule: 'gt' }],
    }
    showCreateModal.value = true
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

        if (selectedType.value === 'cpu') {
            await vmAlarmRulesApi.createCPURule({ ...base, rules: createForm.value.rules as any })
        } else if (selectedType.value === 'memory') {
            await vmAlarmRulesApi.createMemoryRule({ ...base, rules: createForm.value.rules as any })
        } else {
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
    if (!deleteTarget.value) return
    try {
        await vmAlarmRulesApi.deleteRule(selectedType.value, deleteTarget.value.rule_id)
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
    if (selectedType.value === 'bw') {
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

watch([selectedType], () => { page.value = 1; fetchRules() })
watch([page], fetchRules)
onMounted(fetchRules)
</script>

<template>
    <div class="page-container">
        <div class="page-header">
            <h2><ShieldAlert :size="22" /> {{ t('dashboard.vmAlarmRules.title') }}</h2>
            <div class="header-actions">
                <select v-model="selectedType" class="form-input filter-select">
                    <option v-for="rt in VM_RULE_TYPES" :key="rt.value" :value="rt.value">{{ rt.label }}</option>
                </select>
                <div class="search-box">
                    <Search :size="16" />
                    <input v-model="searchQuery" :placeholder="t('actions.search')" class="form-input" />
                </div>
                <button class="btn btn-primary btn-sm" @click="openCreate">
                    <Plus :size="16" /> {{ t('actions.create') }}
                </button>
            </div>
        </div>

        <div v-if="errorMsg" class="error-banner" @click="errorMsg = ''">{{ errorMsg }}</div>

        <div v-if="loading" class="loading-spinner">Loading...</div>

        <table v-else class="data-table">
            <thead>
                <tr>
                    <th>{{ t('dashboard.table.name') }}</th>
                    <th>Rule ID</th>
                    <th>{{ t('dashboard.vmAlarmRules.level') }}</th>
                    <th>{{ t('dashboard.vmAlarmRules.linkedVMs') }}</th>
                    <th>{{ t('dashboard.table.status') }}</th>
                    <th>{{ t('dashboard.table.actions') }}</th>
                </tr>
            </thead>
            <tbody>
                <tr v-for="rule in filteredRules" :key="rule.rule_id">
                    <td>{{ rule.name }}</td>
                    <td class="monospace">{{ rule.rule_id }}</td>
                    <td>
                        <span class="badge" :class="'badge-' + rule.level">{{ rule.level }}</span>
                    </td>
                    <td>{{ rule.linkedvms?.length || 0 }} VMs</td>
                    <td>
                        <span class="badge" :class="rule.enable ? 'badge-success' : 'badge-muted'">
                            {{ rule.enable ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                        </span>
                    </td>
                    <td class="actions-cell">
                        <button class="btn btn-ghost btn-sm" @click="openBindChannels(rule)" :title="t('dashboard.vmAlarmRules.bindChannels')">
                            <Link :size="16" />
                        </button>
                        <button class="btn btn-ghost btn-sm text-danger" @click="confirmDelete(rule)">
                            <Trash2 :size="16" />
                        </button>
                    </td>
                </tr>
                <tr v-if="filteredRules.length === 0 && !loading">
                    <td colspan="6" class="text-center text-muted">{{ t('messages.noResults') }}</td>
                </tr>
            </tbody>
        </table>

        <!-- Pagination -->
        <div v-if="totalPages > 1" class="pagination">
            <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="prevPage">Prev</button>
            <span class="page-info">{{ page }} / {{ totalPages }} ({{ total }} total)</span>
            <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="nextPage">Next</button>
        </div>

        <!-- Create Modal -->
        <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
            <div class="modal-content modal-lg">
                <div class="modal-header">
                    <h3>{{ t('dashboard.vmAlarmRules.createTitle', { type: selectedType.toUpperCase() }) }}</h3>
                </div>
                <div class="modal-body">
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.table.name') }}</label>
                        <input v-model="createForm.name" class="form-input" required />
                    </div>
                    <div class="form-group">
                        <label class="form-label">Rule ID</label>
                        <input v-model="createForm.rule_id" class="form-input" :placeholder="t('dashboard.vmAlarmRules.ruleIdPlaceholder')" />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.vmAlarmRules.level') }}</label>
                        <select v-model="createForm.level" class="form-input">
                            <option value="critical">Critical</option>
                            <option value="warning">Warning</option>
                            <option value="info">Info</option>
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
                            <select v-if="selectedType === 'bw'" v-model="rule.direction" class="form-input rule-input-sm">
                                <option value="in">Inbound</option>
                                <option value="out">Outbound</option>
                            </select>
                            <input v-model.number="rule.limit" type="number" class="form-input rule-input-sm" placeholder="Limit %" min="1" max="100" />
                            <input v-model.number="rule.duration" type="number" class="form-input rule-input-sm" :placeholder="t('dashboard.vmAlarmRules.durationMin')" min="1" />
                            <button v-if="createForm.rules.length > 1" class="btn btn-ghost btn-sm text-danger" @click="removeRuleRow(idx)">
                                <Trash2 :size="14" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary btn-sm" @click="showCreateModal = false">{{ t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm" @click="submitCreate" :disabled="!createForm.name">{{ t('actions.save') }}</button>
                </div>
            </div>
        </div>

        <!-- Delete Modal -->
        <div v-if="showDeleteModal" class="modal-overlay" @click.self="showDeleteModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3>{{ t('actions.confirmDelete') }}</h3>
                </div>
                <div class="modal-body">
                    <p>{{ t('dashboard.vmAlarmRules.deleteConfirm', { name: deleteTarget?.name }) }}</p>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary btn-sm" @click="showDeleteModal = false">{{ t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm text-danger" @click="executeDelete">{{ t('actions.delete') }}</button>
                </div>
            </div>
        </div>

        <!-- Bind Channels Modal -->
        <div v-if="showBindModal" class="modal-overlay" @click.self="showBindModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3>{{ t('dashboard.vmAlarmRules.bindChannels') }} - {{ bindTarget?.name }}</h3>
                </div>
                <div class="modal-body">
                    <div v-if="bindLoading" class="loading-spinner">Loading...</div>
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
                    <button class="btn btn-secondary btn-sm" @click="showBindModal = false">{{ t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm" @click="saveBindings" :disabled="bindLoading">{{ t('actions.save') }}</button>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.error-banner { background: #fef2f2; color: #dc2626; border: 1px solid #fecaca; border-radius: 6px; padding: 10px 14px; margin-bottom: 12px; font-size: 13px; cursor: pointer; }
.filter-select { width: 130px; }
.monospace { font-family: monospace; font-size: 13px; }
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

.modal-lg { max-width: 600px; }
.rules-section { margin-top: 12px; }
.rules-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.rule-row { display: flex; gap: 8px; margin-bottom: 6px; align-items: center; }
.rule-input-sm { width: 120px; }

.channel-list { max-height: 300px; overflow-y: auto; }
.channel-item { display: flex; align-items: center; gap: 8px; padding: 8px; cursor: pointer; border-radius: 4px; }
.channel-item:hover { background: var(--bg-hover, #f3f4f6); }
.channel-item input[type="checkbox"] { cursor: pointer; }
.channel-name { flex: 1; }
</style>
