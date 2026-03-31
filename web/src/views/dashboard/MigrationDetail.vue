<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { migrationsApi, type Migration } from '../../api/migrations'
import { ArrowLeft, ArrowRightLeft, Copy, Check } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const migration = ref<Migration | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)

const fetchMigrationDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const id = route.params.id as string
        const response = await migrationsApi.getMigration(id)
        const data = response.data as any
        migration.value = data.migration || data
    } catch (err: any) {
        console.error('Failed to fetch migration detail:', err)
        error.value = err.message || t('dashboard.migrationDetail.loadError')
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

const getStatusClass = (status: string) => {
    const s = (status || '').toLowerCase()
    if (s === 'completed' || s === 'done') return 'status-active'
    if (s === 'error' || s === 'failed') return 'status-error'
    if (s === 'running' || s === 'migrating') return 'status-pending'
    return ''
}

const goBack = () => {
    router.push({ name: 'migrations' })
}

onMounted(fetchMigrationDetail)
</script>

<template>
  <div class="vpc-detail">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>{{ $t('dashboard.table.migration') }}</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ $t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <ArrowRightLeft :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchMigrationDetail" style="margin-top: 12px;">
        {{ $t('actions.refresh') }}
      </button>
    </div>

    <!-- Detail Content -->
    <div v-else-if="migration" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <ArrowRightLeft :size="28" />
          </div>
          <div>
            <h2 class="resource-title">{{ $t('dashboard.migrationDetail.title') }}</h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ migration.id }}</span>
              <button class="copy-btn" @click="copyToClipboard(migration.id.toString(), 'id')" :title="$t('messages.copied')">
                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <span :class="['badge', 'badge-lg', getStatusClass(migration.status)]" :style="!getStatusClass(migration.status) ? 'background: var(--gray-100); color: var(--gray-700);' : ''">
            {{ migration.status }}
          </span>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <!-- Basic Info Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ $t('dashboard.migrationDetail.overview') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.migrationDetail.instanceId') }}</span>
              <span class="info-value mono">{{ migration.instance_id }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.migrationDetail.type') }}</span>
              <span class="info-value">{{ migration.migration_type || $t('messages.unnamed') }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.migrationDetail.createdAt') }}</span>
              <span class="info-value mono">{{ new Date(migration.created_at).toLocaleString() }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.migrationDetail.updatedAt') }}</span>
              <span class="info-value mono">{{ new Date(migration.updated_at).toLocaleString() }}</span>
            </div>
          </div>
        </div>

        <!-- Node Placement Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ $t('dashboard.migrationDetail.placementRoute') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.migrationDetail.sourceNode') }}</span>
              <span class="info-value">{{ migration.source_node || '-' }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.migrationDetail.destinationNode') }}</span>
              <span class="info-value">{{ migration.dest_node || '-' }}</span>
            </div>
            <div class="info-row" v-if="migration.dest_compute && migration.dest_compute !== migration.dest_node">
              <span class="info-label">{{ $t('dashboard.migrationDetail.finalDestCompute') }}</span>
              <span class="info-value">{{ migration.dest_compute }}</span>
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

.badge.status-active {
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}

.badge.status-error {
  background: rgba(239, 68, 68, 0.1);
  color: #ef4444;
}

.badge.status-pending {
  background: rgba(245, 158, 11, 0.1);
  color: #f59e0b;
}

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
