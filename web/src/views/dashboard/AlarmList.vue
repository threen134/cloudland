<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { alarmsApi, RULE_TYPES, type NodeAlarmRule, type CreateNodeAlarmRulePayload } from '../../api/alarms'
import { Search as SearchIcon, AlertTriangle, Plus, Trash2, RefreshCw, X, Loader2, RefreshCcw } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useRegionStore } from '../../stores/region'

const { t } = useI18n()
const toast = useToast()
const region = useRegionStore()
const alarmList = ref<NodeAlarmRule[]>([])
const loading = ref(false)
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
    enabled: true
})
const configJsonStr = ref('{}')
const configError = ref('')

// Delete
const showDeleteConfirm = ref(false)
const deletingRule = ref<NodeAlarmRule | null>(null)
const deleting = ref(false)

const fetchAlarms = async () => {
    loading.value = true
    try {
        const params: any = {}
        if (filterRuleType.value) params.rule_type = filterRuleType.value
        const response = await alarmsApi.fetchAlarmRules(params)
        const data = response.data as any
        alarmList.value = Array.isArray(data) ? data : (data.data || [])
    } catch (error) {
        console.error('API fetch failed:', error)
        alarmList.value = []
    } finally {
        loading.value = false
    }
}

const filteredAlarms = computed(() => {
    if (!searchQuery.value) return alarmList.value
    const query = searchQuery.value.toLowerCase()
    return alarmList.value.filter(a =>
        (a.name && a.name.toLowerCase().includes(query)) ||
        (a.rule_type && a.rule_type.toLowerCase().includes(query)) ||
        (a.uuid && a.uuid.toLowerCase().includes(query))
    )
})

const getRuleTypeLabel = (type: string) => {
    const found = RULE_TYPES.find(r => r.value === type)
    return found ? found.label : type
}

const getRuleTypeClass = (type: string) => {
    const map: Record<string, string> = {
        node_available: 'rt-critical',
        control_node: 'rt-warning',
        compute_node: 'rt-warning',
        hypervisor_vcpu: 'rt-info',
        packet_drop: 'rt-critical',
        ip_block: 'rt-info',
        ipgroup_available_ip: 'rt-info'
    }
    return map[type] || 'rt-default'
}

// Config templates per rule type
const CONFIG_TEMPLATES: Record<string, object> = {
    node_available: { duration: "5m", severity: "critical" },
    control_node: { cpu_threshold: 80, memory_threshold: 80, duration: "5m", severity: "warning" },
    compute_node: { cpu_threshold: 80, memory_threshold: 80, disk_threshold: 85, duration: "5m", severity: "warning" },
    hypervisor_vcpu: { vcpu_ratio_threshold: 3.0, duration: "10m", severity: "warning" },
    packet_drop: { drop_rate_threshold: 0.01, duration: "5m", severity: "critical" },
    ip_block: { threshold: 80, duration: "10m", severity: "info" },
    ipgroup_available_ip: { min_available: 5, duration: "10m", severity: "info" },
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
    } catch (err: any) {
        const errData = err.response?.data
        toast.error(errData?.error || errData?.message || t('messages.error'))
    } finally {
        creating.value = false
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
    } catch (err: any) {
        toast.error(err.response?.data?.error || t('messages.error'))
    } finally {
        deleting.value = false
    }
}

// Sync mappings
const syncing = ref(false)
const handleSync = async () => {
    syncing.value = true
    try {
        await alarmsApi.syncMappings()
        toast.success(t('dashboard.alarmActions.syncSuccess'))
    } catch (err: any) {
        toast.error(err.response?.data?.error || t('messages.error'))
    } finally {
        syncing.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchAlarms()
    }
})

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchAlarms()
    }
})
</script>

<template>
  <div class="vpc-list-container">
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <SearchIcon :size="16" class="search-icon" />
          <input type="text" v-model="searchQuery" :placeholder="t('actions.search') + '...'" class="search-input" />
        </div>
        <select v-model="filterRuleType" @change="fetchAlarms" class="filter-select">
          <option value="">{{ t('dashboard.alarmActions.allTypes') }}</option>
          <option v-for="rt in RULE_TYPES" :key="rt.value" :value="rt.value">{{ rt.label }}</option>
        </select>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm" @click="handleSync" :disabled="syncing" :title="t('dashboard.alarmActions.syncMappings')">
          <Loader2 v-if="syncing" :size="14" class="spinning" />
          <RefreshCcw v-else :size="14" />
          <span>{{ syncing ? t('dashboard.alarmActions.syncing') : t('dashboard.alarmActions.syncMappings') }}</span>
        </button>
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchAlarms" :title="t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" />
          <span>{{ t('actions.create') }}</span>
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('dashboard.table.nameId') }}</th>
            <th>{{ t('dashboard.alarmActions.ruleType') }}</th>
            <th>{{ t('dashboard.table.status') }}</th>
            <th>{{ t('dashboard.table.description') }}</th>
            <th>{{ t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="5" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredAlarms.length === 0">
            <td colspan="5" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery || filterRuleType">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ t('messages.noResults') }}</p>
               </div>
               <div v-else class="empty-state">
                  <AlertTriangle :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                  <p>{{ t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="a in filteredAlarms" :key="a.uuid">
            <td>
              <router-link :to="{ name: 'alarm-detail', params: { id: a.uuid } }" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon">
                    <AlertTriangle :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ a.name }}</div>
                    <div class="resource-id">{{ a.uuid }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <span class="rule-type-badge" :class="getRuleTypeClass(a.rule_type)">
                {{ getRuleTypeLabel(a.rule_type) }}
              </span>
            </td>
            <td>
              <span class="status-pill" :class="a.enabled ? 'status-active' : 'status-disabled'">
                <span class="status-dot"></span>
                {{ a.enabled ? t('dashboard.alarmActions.enabled') : t('dashboard.alarmActions.disabled') }}
              </span>
            </td>
            <td class="desc-cell">{{ a.description || '-' }}</td>
            <td>
              <button class="icon-btn-table text-error" @click.prevent="confirmDelete(a)" :title="t('actions.delete')">
                <Trash2 :size="16" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Modal -->
    <Teleport to="body">
      <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
        <div class="modal-content card" style="max-width: 560px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.alarmActions.createTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showCreateModal = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <div class="form-stack">
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.alarmActions.ruleType') }} *</label>
                <select v-model="createForm.rule_type" @change="onRuleTypeChange" class="form-input">
                  <option value="" disabled>{{ t('dashboard.alarmActions.selectRuleType') }}</option>
                  <option v-for="rt in RULE_TYPES" :key="rt.value" :value="rt.value">{{ rt.label }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.name') }} *</label>
                <input type="text" v-model="createForm.name" class="form-input" :placeholder="t('dashboard.forms.placeholder.alarmNameExample')" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.description') }}</label>
                <input type="text" v-model="createForm.description" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label">Config (JSON) *</label>
                <textarea v-model="configJsonStr" class="form-input config-textarea" rows="6" @blur="validateConfig" placeholder='{"threshold": 80, "duration": "5m"}'></textarea>
                <span v-if="configError" class="form-error">{{ configError }}</span>
                <span v-else-if="createForm.rule_type" class="form-hint">{{ t('dashboard.alarmActions.configHint') }}</span>
              </div>
              <div class="form-group">
                <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer;">
                  <input type="checkbox" v-model="createForm.enabled" />
                  {{ t('dashboard.alarmActions.enabled') }}
                </label>
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showCreateModal = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreate" :disabled="creating || !createForm.rule_type || !createForm.name">
              <Loader2 v-if="creating" :size="14" class="spinning" />
              {{ creating ? t('messages.creating') : t('actions.create') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Delete Confirm Modal -->
    <Teleport to="body">
      <div v-if="showDeleteConfirm" class="modal-overlay" @click.self="showDeleteConfirm = false">
        <div class="modal-content card" style="max-width: 440px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.alarmActions.deleteTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showDeleteConfirm = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p>{{ t('dashboard.alarmActions.deleteConfirm', { name: deletingRule?.name }) }}</p>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" @click="showDeleteConfirm = false">{{ t('actions.cancel') }}</button>
            <button class="btn btn-danger" @click="handleDelete" :disabled="deleting">
              <Loader2 v-if="deleting" :size="14" class="spinning" />
              {{ deleting ? t('messages.deleting') : t('actions.delete') }}
            </button>
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
  min-width: 140px;
}

.table-card { padding: 0; overflow: hidden; }


.resource-link:hover .resource-name { color: var(--primary-600); text-decoration: underline; }

.status-pill {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 4px 10px; border-radius: var(--radius-full);
  font-size: var(--font-size-xs); font-weight: var(--font-weight-medium);
}

.status-active { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-disabled { background: var(--gray-100); color: var(--gray-500); }
.status-dot { width: 6px; height: 6px; background: currentColor; border-radius: 50%; }

.rule-type-badge {
  display: inline-flex; align-items: center;
  padding: 2px 8px; border-radius: var(--radius-sm);
  font-size: var(--font-size-xs); font-weight: 500;
  border: 1px solid var(--border-subtle);
}

.rt-critical { background: rgba(239, 68, 68, 0.1); color: #ef4444; border-color: rgba(239, 68, 68, 0.2); }
.rt-warning { background: rgba(245, 158, 11, 0.1); color: #f59e0b; border-color: rgba(245, 158, 11, 0.2); }
.rt-info { background: rgba(59, 130, 246, 0.1); color: #3b82f6; border-color: rgba(59, 130, 246, 0.2); }
.rt-default { background: var(--gray-100); color: var(--gray-600); }

.desc-cell {
  max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--font-size-sm); color: var(--text-secondary);
}

.empty-state {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
}

.icon-btn-table {
  width: 32px; height: 32px; border-radius: 8px; border: none;
  background: transparent; color: var(--text-tertiary);
  display: flex; align-items: center; justify-content: center;
  cursor: pointer; transition: all 0.2s;
}

.icon-btn-table:hover { background-color: var(--bg-tertiary); color: var(--primary-500); }
.icon-btn-table.text-error:hover { background-color: #fef2f2; color: #ef4444; }

/* Modal */
.form-stack { display: flex; flex-direction: column; gap: 16px; }
.form-group { display: flex; flex-direction: column; }
.form-label { font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 4px; font-weight: 500; }

.form-input {
  width: 100%; padding: 8px 12px;
  border: 1px solid var(--border-light); border-radius: var(--radius-md);
  font-size: 0.875rem; background: var(--bg-primary); color: var(--text-primary);
}

.form-input:focus { outline: none; border-color: var(--primary-300); box-shadow: 0 0 0 2px var(--primary-100); }

.config-textarea {
  font-family: var(--font-family-mono); font-size: 0.8125rem; resize: vertical;
}

.form-error { color: #ef4444; font-size: 0.75rem; margin-top: 4px; }
.form-hint { color: var(--text-light); font-size: 0.75rem; margin-top: 4px; }

.btn-danger {
  background: #ef4444; color: white; border: none;
  padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}

.btn-danger:hover { background: #dc2626; }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
