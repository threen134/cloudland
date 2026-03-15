<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { alarmsApi, type AlarmPolicy } from '../../api/alarms'
import { ArrowLeft, AlertTriangle, Copy, Check } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const alarm = ref<AlarmPolicy | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)

const fetchAlarmDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const id = route.params.id as string
        const response = await alarmsApi.getAlarm(id)
        const data = response.data as any
        alarm.value = data.alarm_policy || data
    } catch (err: any) {
        console.error('Failed to fetch alarm detail:', err)
        error.value = err.message || 'Failed to load Alarm Policy details'
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

const getSeverityClass = (severity: string) => {
    const s = (severity || '').toLowerCase()
    if (s === 'critical' || s === 'high') return 'severity-high'
    if (s === 'warning' || s === 'medium') return 'severity-medium'
    return 'severity-low'
}

const goBack = () => {
    router.push({ name: 'alarms' })
}

onMounted(fetchAlarmDetail)
</script>

<template>
  <div class="vpc-detail">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>Node Alarm Rules</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ $t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <AlertTriangle :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchAlarmDetail" style="margin-top: 12px;">
        {{ $t('actions.refresh') }}
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
            <h2 class="resource-title">{{ alarm.name || 'Alarm Policy' }}</h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ alarm.id || '-' }}</span>
              <button class="copy-btn" v-if="alarm.id" @click="copyToClipboard(alarm.id.toString(), 'id')" :title="$t('messages.copied')">
                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <span :class="['badge', 'badge-lg', alarm.status === 'enabled' || alarm.status === 'active' ? 'status-active' : '']" :style="alarm.status !== 'enabled' && alarm.status !== 'active' ? 'background: var(--gray-100); color: var(--gray-700);' : ''">
            {{ alarm.status || 'Active' }}
          </span>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <!-- Basic Info Card -->
        <div class="info-card card">
          <h3 class="card-section-title">Overview</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.name') }}</span>
              <span class="info-value">{{ alarm.name }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">Severity</span>
              <span class="info-value">
                <span class="severity-badge" :class="getSeverityClass(alarm.severity)">
                  {{ alarm.severity || 'Normal' }}
                </span>
              </span>
            </div>
            <div class="info-row" style="flex-direction: column; align-items: flex-start;">
              <span class="info-label" style="margin-bottom: 4px;">Description</span>
              <span class="info-value" style="text-align: left; font-size: 0.8125rem;">{{ alarm.description || '-' }}</span>
            </div>
          </div>
        </div>

        <!-- Configuration Rules Details -->
        <div class="info-card card">
          <h3 class="card-section-title">Configuration & Rules</h3>
          <div class="info-rows">
             <!-- Dynamically load config variables using same pattern -->
             <div class="info-row" v-for="(value, key) in alarm" :key="key" v-show="!['id', 'name', 'description', 'severity', 'status'].includes(key)" style="flex-direction: column; align-items: flex-start; gap: 4px;">
                <span class="info-label" style="text-transform: capitalize;">{{ key.replace(/_/g, ' ') }}</span>
                <span class="info-value" style="width: 100%; text-align: left;">
                   <template v-if="typeof value === 'object'">
                       <pre style="margin: 0; background: var(--bg-tertiary); padding: 8px; border-radius: var(--radius-sm); font-size: 0.75rem; white-space: pre-wrap; font-family: var(--font-family-mono); overflow-x: auto;">{{ JSON.stringify(value, null, 2) }}</pre>
                   </template>
                   <template v-else><span class="mono">{{ value !== null && value !== '' ? String(value) : '-' }}</span></template>
                </span>
             </div>
             
             <!-- Fallback if there are no extra fields -->
             <div v-if="Object.keys(alarm).filter(k => !['id', 'name', 'description', 'severity', 'status'].includes(k)).length === 0" class="text-secondary text-sm" style="text-align: center; padding: 12px 0;">
                No additional rule configuration properties found.
             </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.vpc-detail {
  max-width: 1100px;
}

.detail-header {
  margin-bottom: var(--spacing-4);
}

.back-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--spacing-2);
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  padding: var(--spacing-2) var(--spacing-3);
  border-radius: var(--radius-md);
  transition: all 0.2s;
}

.back-btn:hover {
  color: var(--primary-color);
  background: var(--primary-50);
}

/* Loading */
.loading-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 0;
}

.loading-text {
  margin-top: var(--spacing-3);
  color: var(--text-secondary);
  font-size: var(--font-size-sm);
}

/* Error */
.error-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  text-align: center;
}

/* Title Bar */
.title-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-5);
}

.title-info {
  display: flex;
  align-items: center;
  gap: var(--spacing-4);
}

.title-icon {
  width: 52px;
  height: 52px;
  border-radius: var(--radius-lg);
  background: linear-gradient(135deg, var(--primary-50), var(--primary-100));
  color: var(--primary-color);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.resource-title {
  margin: 0 0 4px 0;
  font-size: var(--font-size-xl);
  font-weight: var(--font-weight-semibold);
  color: var(--text-primary);
}

.resource-id-row {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
}

.resource-id-text {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  font-family: var(--font-family-mono);
}

.copy-btn {
  background: none;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-sm);
  padding: 2px 5px;
  cursor: pointer;
  color: var(--text-light);
  display: inline-flex;
  align-items: center;
  transition: all 0.15s;
}

.copy-btn:hover {
  color: var(--primary-color);
  border-color: var(--primary-200);
  background: var(--primary-50);
}

.copied-icon {
  color: var(--success-color);
}

.title-actions {
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
}

.badge-lg {
  font-size: var(--font-size-sm);
  padding: 6px 14px;
}

.badge.status-active, .status-active {
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}

.severity-badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: var(--radius-sm);
  font-size: var(--font-size-xs);
  text-transform: capitalize;
  font-weight: 500;
  border: 1px solid var(--border-subtle);
}

.severity-high { background: rgba(239, 68, 68, 0.1); color: #ef4444; border-color: rgba(239, 68, 68, 0.2); }
.severity-medium { background: rgba(245, 158, 11, 0.1); color: #f59e0b; border-color: rgba(245, 158, 11, 0.2); }
.severity-low { background: rgba(16, 185, 129, 0.1); color: #10b981; border-color: rgba(16, 185, 129, 0.2); }

/* Info Grid */
.info-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--spacing-4);
  margin-bottom: var(--spacing-5);
}

.info-card {
  padding: var(--spacing-5);
}

.card-section-title {
  margin: 0 0 var(--spacing-4) 0;
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-semibold);
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding-bottom: var(--spacing-3);
  border-bottom: 1px solid var(--border-light);
}

.info-rows {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-3);
}

.info-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--spacing-1) 0;
  min-height: 24px;
}

.info-label {
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  flex-shrink: 0;
}

.info-value {
  font-size: var(--font-size-sm);
  color: var(--text-primary);
  font-weight: var(--font-weight-medium);
  text-align: right;
  word-break: break-all;
}

.info-value.mono {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
}

@media (max-width: 768px) {
  .info-grid {
    grid-template-columns: 1fr;
  }

  .title-bar {
    flex-direction: column;
    align-items: flex-start;
    gap: var(--spacing-3);
  }
}
</style>
