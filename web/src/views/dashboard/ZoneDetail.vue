<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { zonesApi, type Zone } from '../../api/zones'
import { ArrowLeft, MapPin, Copy, Check } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const zone = ref<Zone | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copiedField = ref<string | null>(null)

const fetchZoneDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const zoneName = route.params.name as string
        const response = await zonesApi.getZone(zoneName)
        const data = response.data as any
        zone.value = data.zone || data
    } catch (err: any) {
        console.error('Failed to fetch zone detail:', err)
        error.value = err.message || 'Failed to load Zone details'
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

const goBack = () => {
    router.push({ name: 'zones' })
}

onMounted(fetchZoneDetail)
</script>

<template>
  <div class="vpc-detail">
    <!-- Header -->
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>{{ $t('dashboard.zones') }}</span>
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p class="loading-text">{{ $t('messages.loading') }}</p>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="error-container card">
      <MapPin :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
      <p class="text-secondary">{{ error }}</p>
      <button class="btn btn-primary btn-sm" @click="fetchZoneDetail" style="margin-top: 12px;">
        {{ $t('actions.refresh') }}
      </button>
    </div>

    <!-- Zone Detail Content -->
    <div v-else-if="zone" class="detail-content">
      <!-- Title Bar -->
      <div class="title-bar card">
        <div class="title-info">
          <div class="title-icon">
            <MapPin :size="28" />
          </div>
          <div>
            <h2 class="resource-title">{{ zone.name }}</h2>
            <div class="resource-id-row">
              <span class="resource-id-text">{{ zone.id || '-' }}</span>
              <button class="copy-btn" v-if="zone.id" @click="copyToClipboard(zone.id, 'id')" :title="$t('messages.copied')">
                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                <Copy v-else :size="12" />
              </button>
            </div>
          </div>
        </div>
        <div class="title-actions">
          <span class="badge badge-lg status-active">
            Active
          </span>
        </div>
      </div>

      <!-- Info Sections -->
      <div class="info-grid">
        <!-- Basic Info Card -->
        <div class="info-card card">
          <h3 class="card-section-title">{{ $t('dashboard.table.overview') }}</h3>
          <div class="info-rows">
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.name') }}</span>
              <span class="info-value">{{ zone.name }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.id') }}</span>
              <span class="info-value mono">{{ zone.id || '-' }}</span>
            </div>
            <div class="info-row">
              <span class="info-label">{{ $t('dashboard.table.type') }}</span>
              <span class="info-value">
                <span class="badge status-pending">Default</span>
              </span>
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
