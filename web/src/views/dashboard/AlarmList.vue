<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import {
    alarmsApi,
    RULE_TYPES,
    type NodeAlarmRule,
    type NodeAlarmRuleListResponse,
    type CreateNodeAlarmRulePayload,
} from '../../api/alarms'
import {
    Search as SearchIcon,
    AlertTriangle,
    Plus,
    Trash2,
    Pencil,
    Power,
    RefreshCw,
    Loader2,
    Check,
    Copy,
} from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { useRegionStore } from '../../stores/region'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import NodeAlarmRuleEditModal from '../../components/alarm/NodeAlarmRuleEditModal.vue'

const { t } = useI18n()
const toast = useToast()
const region = useRegionStore()
const alarmList = ref<NodeAlarmRule[]>([])
const loading = ref(false)
const loadError = ref('')
const { copiedId, copyId } = useCopyId()
const searchQuery = ref('')
const filterRuleType = ref('')

// Create modal
const showCreateModal = ref(false)
const creating = ref(false)
const createForm = ref<CreateNodeAlarmRulePayload>({
    rule_type: '',
    name: '',
    config: {},
    description: '',
    enabled: true,
})
const configJsonStr = ref('{}')
const configError = ref('')

// Delete
const showDeleteConfirm = ref(false)
const deletingRule = ref<NodeAlarmRule | null>(null)
const deleting = ref(false)

const fetchAlarms = async () => {
    loading.value = true
    loadError.value = ''
    try {
        const params: { uuid?: string; rule_type?: string } = {}
        if (filterRuleType.value) params.rule_type = filterRuleType.value
        const response = await alarmsApi.fetchAlarmRules(params)
        // 接口返回 { status, data, count }，这里保留"直接是数组"的兼容分支
        const data = response as NodeAlarmRuleListResponse | NodeAlarmRule[]
        alarmList.value = Array.isArray(data) ? data : data.data || []
    } catch (error) {
        console.error('API fetch failed:', error)
        alarmList.value = []
        loadError.value = t('messages.error')
    } finally {
        loading.value = false
    }
}

// 规则类型列显示的是标签、状态列显示的是翻译文案，排序都按原始值
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    {
        key: 'ruleType',
        label: t('dashboard.alarmActions.ruleType'),
        sortable: true,
        sortValue: (a) => a.rule_type || '',
    },
    {
        key: 'status',
        label: t('dashboard.table.status'),
        sortable: true,
        sortValue: (a) => (a.enabled ? 'enabled' : 'disabled'),
    },
    { key: 'description', label: t('dashboard.table.description'), sortable: true },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const filteredAlarms = computed(() => {
    if (!searchQuery.value) return alarmList.value
    const query = searchQuery.value.toLowerCase()
    return alarmList.value.filter(
        (a) =>
            (a.name && a.name.toLowerCase().includes(query)) ||
            (a.rule_type && a.rule_type.toLowerCase().includes(query)) ||
            (a.uuid && a.uuid.toLowerCase().includes(query))
    )
})

const getRuleTypeLabel = (type: string) =>
    (RULE_TYPES as readonly string[]).includes(type) ? t('dashboard.alarmRuleTypes.' + type) : type

// Config templates per rule type.
// The keys must match the variables in the Prometheus rule templates under
// deploy/roles/monitor/templates/*.j2 — anything else is silently ignored (the template
// falls back to its own default), and for a variable without a default the placeholder
// ends up verbatim in the rule file, which makes Prometheus reject every rule it has.
// The values below are each template's own defaults.
const CONFIG_TEMPLATES: Record<string, object> = {
    // node-availability.yml.j2
    node_available: { node_down_duration: '5m' },
    // management-resources.yml.j2 (thresholds in %, disk is *free* space)
    control_node: {
        cpu_usage_threshold: 80,
        cpu_alert_duration: '10m',
        memory_usage_threshold: 80,
        memory_alert_duration: '10m',
        disk_space_threshold: 20,
        disk_alert_duration: '10m',
        network_traffic_threshold_gb: 5,
        network_alert_duration: '10m',
    },
    // compute-core-resources.yml.j2 + compute-network-resources.yml.j2
    // network_types is required: the network template loops over it (pattern is the
    // Prometheus device matcher, threshold is in Gbps)
    compute_node: {
        cpu_usage_threshold: 80,
        cpu_alert_duration: '10m',
        memory_usage_threshold: 80,
        memory_alert_duration: '10m',
        disk_space_threshold: 10,
        disk_alert_duration: '20m',
        network_traffic_threshold_gb: 25,
        network_alert_duration: '5m',
        network_types: {
            public: { pattern: '=~"bond1"', threshold: 20, duration: '5m' },
            private: { pattern: '=~"bond0"', threshold: 20, duration: '5m' },
        },
    },
    // compute-vcpu-resources.yml.j2 — neither variable has a default, both are required
    hypervisor_vcpu: { vcpu_usage_threshold: 85, for_duration: '10m' },
    // packet-drop-monitor.yml.j2
    packet_drop: { packet_drop_threshold: 0, for_duration: '1m', severity: 'warning' },
    // ip-block-monitor.yml.j2
    ip_block: { for_duration: '30s', severity: 'warning' },
    // ipgroup-available-ip-monitor.yml.j2
    ipgroup_available_ip: { threshold: 100, for_duration: '5m', severity: 'warning' },
}

const onRuleTypeChange = () => {
    const tpl = CONFIG_TEMPLATES[createForm.value.rule_type]
    if (tpl) {
        configJsonStr.value = JSON.stringify(tpl, null, 2)
        configError.value = ''
    }
}

// Create
const openCreateModal = () => {
    createForm.value = { rule_type: '', name: '', config: {}, description: '', enabled: true }
    configJsonStr.value = '{}'
    configError.value = ''
    showCreateModal.value = true
}

const validateConfig = () => {
    try {
        createForm.value.config = JSON.parse(configJsonStr.value)
        configError.value = ''
        return true
    } catch {
        configError.value = t('dashboard.alarmActions.invalidJson')
        return false
    }
}

const handleCreate = async () => {
    if (!createForm.value.rule_type || !createForm.value.name) return
    if (!validateConfig()) return
    creating.value = true
    try {
        await alarmsApi.createAlarmRule(createForm.value)
        showCreateModal.value = false
        toast.success(t('messages.success'))
        await fetchAlarms()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        creating.value = false
    }
}
// 编辑弹窗与详情页共用（components/alarm/NodeAlarmRuleEditModal.vue）
const showEditModal = ref(false)
const editingRule = ref<NodeAlarmRule | null>(null)

const openEditModal = (rule: NodeAlarmRule) => {
    editingRule.value = rule
    showEditModal.value = true
}

// 停用只改数据库行是不够的：后端会同时撤下这条规则的 Prometheus 规则文件
const togglingUuid = ref('')
const toggleEnabled = async (rule: NodeAlarmRule) => {
    togglingUuid.value = rule.uuid
    try {
        await alarmsApi.updateAlarmRule(rule.uuid, { enabled: !rule.enabled })
        await fetchAlarms()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        togglingUuid.value = ''
    }
}

// Delete
const confirmDelete = (rule: NodeAlarmRule) => {
    deletingRule.value = rule
    showDeleteConfirm.value = true
}

const handleDelete = async () => {
    if (!deletingRule.value) return
    deleting.value = true
    try {
        await alarmsApi.deleteAlarmRule(deletingRule.value.uuid)
        showDeleteConfirm.value = false
        deletingRule.value = null
        toast.success(t('messages.success'))
        await fetchAlarms()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        deleting.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchAlarms()
    }
})

// Re-fetch when region changes
watch(
    () => region.currentRegionId,
    (newId) => {
        if (newId) {
            fetchAlarms()
        }
    }
)
</script>

<template>
    <div class="vpc-list-container">
        <PageToolbar v-model:search="searchQuery">
            <template #filters>
                <select v-model="filterRuleType" @change="fetchAlarms" class="filter-select">
                    <option value="">{{ t('dashboard.alarmActions.allTypes') }}</option>
                    <option v-for="rt in RULE_TYPES" :key="rt" :value="rt">
                        {{ t('dashboard.alarmRuleTypes.' + rt) }}
                    </option>
                </select>
            </template>
            <template #actions>
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchAlarms" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="filteredAlarms"
            row-key="uuid"
            :loading="loading"
            :error="loadError"
            @retry="fetchAlarms"
        >
            <template #empty>
                <div v-if="searchQuery || filterRuleType">
                    <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ t('messages.noResults') }}</p>
                </div>
                <div v-else class="empty-state">
                    <AlertTriangle :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p>{{ t('messages.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: a }">
                <router-link :to="{ name: 'alarm-detail', params: { id: a.uuid } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <AlertTriangle :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ a.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="a.uuid">{{ a.uuid.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(a.uuid)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check v-if="copiedId === a.uuid" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-ruleType="{ row: a }">
                <span class="badge badge-secondary">
                    {{ getRuleTypeLabel(a.rule_type) }}
                </span>
            </template>

            <template #cell-status="{ row: a }">
                <StatusBadge
                    :variant="a.enabled ? 'success' : 'neutral'"
                    :label="a.enabled ? t('dashboard.alarmActions.enabled') : t('dashboard.alarmActions.disabled')"
                />
            </template>

            <template #cell-description="{ row: a }">
                <div class="desc-cell">{{ a.description || '-' }}</div>
            </template>

            <template #cell-actions="{ row: a }">
                <div class="row-actions">
                    <button class="icon-btn-table" @click.prevent="openEditModal(a)" :title="t('actions.edit')">
                        <Pencil :size="16" />
                    </button>
                    <button
                        class="icon-btn-table"
                        :class="{ 'is-active': a.enabled }"
                        :disabled="togglingUuid === a.uuid"
                        @click.prevent="toggleEnabled(a)"
                        :title="a.enabled ? t('actions.disable') : t('actions.enable')"
                    >
                        <Power :size="16" />
                    </button>
                    <button
                        class="icon-btn-table icon-danger"
                        @click.prevent="confirmDelete(a)"
                        :title="t('actions.delete')"
                    >
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>
        </DataTable>

        <!-- Create Modal -->
        <BaseModal
            :show="showCreateModal"
            :title="t('dashboard.alarmActions.createTitle')"
            size="lg"
            form
            :loading="creating"
            @close="showCreateModal = false"
            @submit="handleCreate"
        >
            <div class="form-stack">
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.alarmActions.ruleType') }} *</label>
                    <select v-model="createForm.rule_type" @change="onRuleTypeChange" class="form-input">
                        <option value="" disabled>{{ t('dashboard.alarmActions.selectRuleType') }}</option>
                        <option v-for="rt in RULE_TYPES" :key="rt" :value="rt">
                            {{ t('dashboard.alarmRuleTypes.' + rt) }}
                        </option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.name') }} *</label>
                    <input
                        type="text"
                        v-model="createForm.name"
                        class="form-input"
                        :placeholder="t('dashboard.forms.placeholder.alarmNameExample')"
                    />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.description') }}</label>
                    <input type="text" v-model="createForm.description" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.alarmActions.configLabel') }} *</label>
                    <textarea
                        v-model="configJsonStr"
                        class="form-input config-textarea"
                        rows="6"
                        @blur="validateConfig"
                        placeholder='{"threshold": 80, "duration": "5m"}'
                    ></textarea>
                    <span v-if="configError" class="form-error">{{ configError }}</span>
                    <span v-else-if="createForm.rule_type" class="form-hint">{{
                        t('dashboard.alarmActions.configHint')
                    }}</span>
                </div>
                <div class="form-group">
                    <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer">
                        <input type="checkbox" v-model="createForm.enabled" />
                        {{ t('dashboard.alarmActions.enabled') }}
                    </label>
                </div>
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showCreateModal = false">
                    {{ t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    class="btn btn-primary"
                    :disabled="creating || !createForm.rule_type || !createForm.name"
                >
                    <Loader2 v-if="creating" :size="14" class="spinning" />
                    {{ creating ? t('messages.creating') : t('actions.create') }}
                </button>
            </template>
        </BaseModal>

        <NodeAlarmRuleEditModal
            :show="showEditModal"
            :rule="editingRule"
            @close="showEditModal = false"
            @saved="fetchAlarms()"
        />

        <!-- Delete Confirm Modal -->
        <DeleteModal
            :show="showDeleteConfirm"
            :title="t('dashboard.alarmActions.deleteTitle')"
            :message="t('dashboard.alarmActions.deleteConfirm', { name: deletingRule?.name })"
            :loading="deleting"
            @close="showDeleteConfirm = false"
            @confirm="handleDelete"
        />
    </div>
</template>

<style scoped>
.filter-select {
    height: 40px;
    padding: 0 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    background: var(--bg-secondary);
    color: var(--text-primary);
    min-width: 140px;
}

.resource-link:hover .resource-name {
    color: var(--primary-600);
    text-decoration: underline;
}

.desc-cell {
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
}

/* Modal */
.form-stack {
    display: flex;
    flex-direction: column;
    gap: 16px;
}
.form-group {
    display: flex;
    flex-direction: column;
}
.form-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: 4px;
    font-weight: 500;
}

.form-input {
    width: 100%;
    padding: 8px 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    background: var(--bg-primary);
    color: var(--text-primary);
}

.form-input:focus {
    outline: none;
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.config-textarea {
    font-family: var(--font-family-mono);
    font-size: 0.8125rem;
    resize: vertical;
}

.form-error {
    color: var(--error-color);
    font-size: 0.75rem;
    margin-top: 4px;
}
.form-hint {
    color: var(--text-light);
    font-size: 0.75rem;
    margin-top: 4px;
}
</style>
