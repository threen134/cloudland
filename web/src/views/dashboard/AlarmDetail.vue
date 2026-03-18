<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { alarmsApi, RULE_TYPES, type NodeAlarmRule } from '../../api/alarms'
import { ArrowLeft, AlertTriangle, Copy, Check, Trash2, X, Loader2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'

const { t } = useI18n()
const toast = useToast()
const route = useRoute()
const router = useRouter()
const alarm = ref<NodeAlarmRule | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)

// Delete
const showDeleteConfirm = ref(false)
const deleting = ref(false)

const fetchAlarmDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const uuid = route.params.id as string
        const response = await alarmsApi.fetchAlarmRules({ uuid })
        const data = response.data as any
        const rules = Array.isArray(data) ? data : (data.data || [])
        alarm.value = rules.length > 0 ? rules[0] : null
        if (!alarm.value) {
            error.value = 'Alarm rule not found'
        }
    } catch (err: any) {
        console.error('Failed to fetch alarm detail:', err)
        error.value = err.message || 'Failed to load Alarm Rule details'
    } finally {
        loading.value = false
    }
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

const getRuleTypeLabel = (type: string) => {
    const found = RULE_TYPES.find(r => r.value === type)
    return found ? found.label : type
}

const goBack = () => { router.push({ name: 'alarms' }) }

const handleDelete = async () => {
    if (!alarm.value) return
    deleting.value = true
    try {
        await alarmsApi.deleteAlarmRule(alarm.value.uuid)
        router.push({ name: 'alarms' })
    } catch (err: any) {
        toast.error(err.response?.data?.error || 'Delete failed')
    } finally {
        deleting.value = false
    }
}

onMounted(fetchAlarmDetail)
</script>

<template>
  <div class="vpc-detail">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>{{ t('dashboard.alarms') }}</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <AlertTriangle :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchAlarmDetail" style="margin-top: 12px;">
        {{ t('actions.refresh') }}
      </button>
    </div>

    <!-- Detail Content -->
    <div v-else-if="alarm" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <AlertTriangle :size="28" />
          </div>
          <div>
            <h2 class="resource-title">{{ alarm.name }}</h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ alarm.uuid }}</span>
              <button class="copy-btn" @click="copyToClipboard(alarm.uuid, 'uuid')" :title="t('messages.copied')">
                <Check v-if="copiedField === 'uuid'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <button class="btn btn-danger-outline btn-sm" @click="showDeleteConfirm = true">
            <Trash2 :size="14" />
            {{ t('actions.delete') }}
          </button>
          <span :class="['badge', 'badge-lg', alarm.enabled ? 'status-active' : 'status-disabled']">
            {{ alarm.enabled ? 'Enabled' : 'Disabled' }}
          </span>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <!-- Basic Info Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ t('dashboard.table.overview') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ t('dashboard.table.name') }}</span>
              <span class="info-value">{{ alarm.name }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ t('dashboard.alarmActions.ruleType') }}</span>
              <span class="info-value">
                <span class="rule-type-badge">{{ getRuleTypeLabel(alarm.rule_type) }}</span>
              </span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ t('dashboard.alarmActions.owner') }}</span>
              <span class="info-value">{{ alarm.owner || '-' }}</span>
            </div>
            <div class="info-row" v-if="alarm.description">
              <span class="info-label">{{ t('dashboard.table.description') }}</span>
              <span class="info-value">{{ alarm.description }}</span>
            </div>
          </div>
        </div>

        <!-- Configuration Card -->
        <div class="info-card card">
          <h3 class="card-section-title">Config</h3>
          <div class="config-display">
            <pre>{{ JSON.stringify(alarm.config, null, 2) }}</pre>
          </div>
        </div>
      </div>
    </div>

    <!-- Delete Confirm Modal -->
    <Teleport to="body">
      <div v-if="showDeleteConfirm" class="modal-overlay" @click.self="showDeleteConfirm = false">
        <div class="modal-content" style="max-width: 440px;">
          <div class="modal-header">
            <h3>{{ t('dashboard.alarmActions.deleteTitle') }}</h3>
            <button class="btn btn-ghost btn-icon" @click="showDeleteConfirm = false"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p>{{ t('dashboard.alarmActions.deleteConfirm', { name: alarm?.name }) }}</p>
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
.vpc-detail { max-width: 1100px; }
.detail-header { margin-bottom: var(--spacing-4); }

.back-btn {
  display: inline-flex; align-items: center; gap: var(--spacing-2);
  font-size: var(--font-size-sm); color: var(--text-secondary);
  padding: var(--spacing-2) var(--spacing-3); border-radius: var(--radius-md); transition: all 0.2s;
}

.back-btn:hover { color: var(--primary-color); background: var(--primary-50); }

.loading-container {
  display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 80px 0;
}

.loading-text { margin-top: var(--spacing-3); color: var(--text-secondary); font-size: var(--font-size-sm); }

.error-container {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  padding: 60px 20px; text-align: center;
}

.title-bar {
  display: flex; justify-content: space-between; align-items: center; margin-bottom: var(--spacing-5);
}

.title-info { display: flex; align-items: center; gap: var(--spacing-4); }

.title-icon {
  width: 52px; height: 52px; border-radius: var(--radius-lg);
  background: linear-gradient(135deg, var(--primary-50), var(--primary-100));
  color: var(--primary-color);
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}

.resource-title {
  margin: 0 0 4px 0; font-size: var(--font-size-xl);
  font-weight: var(--font-weight-semibold); color: var(--text-primary);
}

.resource-id-row { display: flex; align-items: center; gap: var(--spacing-2); }

.resource-id-text {
  font-size: var(--font-size-xs); color: var(--text-light); font-family: var(--font-family-mono);
}

.copy-btn {
  background: none; border: 1px solid var(--border-light); border-radius: var(--radius-sm);
  padding: 2px 5px; cursor: pointer; color: var(--text-light);
  display: inline-flex; align-items: center; transition: all 0.15s;
}

.copy-btn:hover { color: var(--primary-color); border-color: var(--primary-200); background: var(--primary-50); }
.copied-icon { color: var(--success-color); }
.title-actions { display: flex; align-items: center; gap: var(--spacing-3); }
.badge-lg { font-size: var(--font-size-sm); padding: 6px 14px; }

.badge.status-active, .status-active { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-disabled { background: var(--gray-100); color: var(--gray-500); }

.rule-type-badge {
  display: inline-flex; align-items: center;
  padding: 2px 8px; border-radius: var(--radius-sm);
  font-size: var(--font-size-xs); font-weight: 500;
  background: rgba(59, 130, 246, 0.1); color: #3b82f6;
  border: 1px solid rgba(59, 130, 246, 0.2);
}

.info-grid {
  display: grid; grid-template-columns: repeat(2, 1fr);
  gap: var(--spacing-4); margin-bottom: var(--spacing-5);
}

.info-card { padding: var(--spacing-5); }

.card-section-title {
  margin: 0 0 var(--spacing-4) 0; font-size: var(--font-size-sm);
  font-weight: var(--font-weight-semibold); color: var(--text-secondary);
  text-transform: uppercase; letter-spacing: 0.05em;
  padding-bottom: var(--spacing-3); border-bottom: 1px solid var(--border-light);
}

.info-rows { display: flex; flex-direction: column; gap: var(--spacing-3); }

.info-row {
  display: flex; justify-content: space-between; align-items: center;
  padding: var(--spacing-1) 0; min-height: 24px;
}

.info-label { font-size: var(--font-size-sm); color: var(--text-secondary); flex-shrink: 0; }

.info-value {
  font-size: var(--font-size-sm); color: var(--text-primary);
  font-weight: var(--font-weight-medium); text-align: right; word-break: break-all;
}

.config-display {
  background: var(--bg-tertiary); border-radius: var(--radius-md);
  padding: 12px; overflow-x: auto;
}

.config-display pre {
  margin: 0; font-size: 0.75rem; font-family: var(--font-family-mono);
  white-space: pre-wrap; color: var(--text-primary);
}

.btn-danger-outline {
  background: transparent; color: #ef4444; border: 1px solid #ef4444;
  padding: 6px 12px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
  display: inline-flex; align-items: center; gap: 4px; font-size: var(--font-size-sm);
}

.btn-danger-outline:hover { background: #fef2f2; }

/* Modal */
.modal-overlay {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5);
  display: flex; align-items: center; justify-content: center; z-index: 1000;
}

.modal-content {
  background: var(--bg-primary); border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl); width: 90%;
}

.modal-header {
  display: flex; justify-content: space-between; align-items: center;
  padding: 20px 24px; border-bottom: 1px solid var(--border-light);
}

.modal-header h3 { margin: 0; font-size: 1.125rem; }
.modal-body { padding: 24px; }

.modal-footer {
  display: flex; justify-content: flex-end; gap: 8px;
  padding: 16px 24px; border-top: 1px solid var(--border-light);
}

.btn-danger {
  background: #ef4444; color: white; border: none;
  padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}

.btn-danger:hover { background: #dc2626; }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 768px) {
  .info-grid { grid-template-columns: 1fr; }
  .title-bar { flex-direction: column; align-items: flex-start; gap: var(--spacing-3); }
}
</style>
