<script setup lang="ts">
// Activity page: all operations of the current organization in the current region, filtered by time range,
// resource type and result. "Load more" pages by cursor: new entries keep arriving, so page numbers / offsets would
// duplicate or skip entries.
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { RefreshCw, History, ArrowLeft } from 'lucide-vue-next'
import { activitiesApi, type Activity, type ActivityQuery } from '../../api/activities'
import { useRegionStore } from '../../stores/region'
import ActivityText from '../../components/activity/ActivityText.vue'
import { useActivityTime } from '../../components/activity/activityTime'

const { t } = useI18n()
const router = useRouter()
const { relativeTime, absoluteTime } = useActivityTime()

// The page is entered from the Recent Activity card, so "back" goes to the overview
const goBack = () => router.push({ name: 'dashboard' })
const regionStore = useRegionStore()

const PAGE_SIZE = 20
// Must match the backend limit on a single query's time span.
const MAX_SPAN_DAYS = 90
const DAY_MS = 24 * 60 * 60 * 1000

type RangePreset = 'today' | '7d' | '30d' | '90d' | 'custom'
const rangePreset = ref<RangePreset>('7d')
const customStart = ref('')
const customEnd = ref('')
const resourceType = ref('')
const result = ref<'' | 'success' | 'failed'>('')

const RESOURCE_TYPES = [
    'instance', 'volume', 'backup', 'consistency_group', 'image', 'key', 'flavor',
    'vpc', 'subnet', 'security_group', 'floating_ip', 'load_balancer',
    'zone', 'hyper', 'migration',
]

const activities = ref<Activity[]>([])
const nextCursor = ref('')
const loading = ref(false)
const loadingMore = ref(false)
const errorMessage = ref('')
const rangeError = ref('')
// Bumped whenever the filters change; responses of older generations are discarded so they cannot overwrite newer results.
let generation = 0
// Query (including the time range) fixed at the last reload. "Load more" reuses it instead of recomputing,
// otherwise the range would drift over time.
let activeQuery: ActivityQuery | null = null

const detailOf = (err: unknown): string => {
    const detail = (err as { response?: { data?: { detail?: unknown } } })?.response?.data?.detail
    return typeof detail === 'string' ? detail : t('dashboard.activityPage.loadFailed')
}

// Compute the time range. Returns null and sets rangeError when the custom range is invalid.
const buildRange = (): { start?: string; end?: string } | null => {
    rangeError.value = ''
    const now = new Date()
    switch (rangePreset.value) {
        case 'today': {
            const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate())
            return { start: midnight.toISOString(), end: now.toISOString() }
        }
        case '7d':
        case '30d':
        case '90d': {
            const days = parseInt(rangePreset.value)
            // Send end explicitly: with start only, the server uses its own current time as end, and "last 90 days"
            // would slightly exceed the span limit and be rejected.
            return { start: new Date(now.getTime() - days * DAY_MS).toISOString(), end: now.toISOString() }
        }
        case 'custom': {
            if (!customStart.value || !customEnd.value) {
                rangeError.value = t('dashboard.activityPage.rangeRequired')
                return null
            }
            const start = new Date(customStart.value)
            const end = new Date(customEnd.value)
            if (start >= end) {
                rangeError.value = t('dashboard.activityPage.rangeOrder')
                return null
            }
            if (end.getTime() - start.getTime() > MAX_SPAN_DAYS * DAY_MS) {
                rangeError.value = t('dashboard.activityPage.rangeTooLong', { days: MAX_SPAN_DAYS })
                return null
            }
            return { start: start.toISOString(), end: end.toISOString() }
        }
    }
}

const buildQuery = (): ActivityQuery | null => {
    const range = buildRange()
    if (!range) return null
    const query: ActivityQuery = { limit: PAGE_SIZE, ...range }
    if (resourceType.value) query.resource_type = resourceType.value
    if (result.value) query.success = result.value === 'success'
    return query
}

const reload = async () => {
    const query = buildQuery()
    activeQuery = query
    // The filters changed: invalidate in-flight requests whether or not the new filters are valid.
    const gen = ++generation
    if (!query) {
        // Invalid custom range: clear the old results and cursor so data for the old filters is neither shown nor paged.
        activities.value = []
        nextCursor.value = ''
        errorMessage.value = ''
        loading.value = false
        return
    }
    loading.value = true
    errorMessage.value = ''
    try {
        const res = await activitiesApi.list(query)
        if (gen !== generation) return
        activities.value = res.activities || []
        nextCursor.value = res.next_cursor || ''
    } catch (err) {
        if (gen !== generation) return
        activities.value = []
        nextCursor.value = ''
        errorMessage.value = detailOf(err)
    } finally {
        if (gen === generation) loading.value = false
    }
}

const loadMore = async () => {
    if (!nextCursor.value || loadingMore.value) return
    const query = activeQuery ? { ...activeQuery, cursor: nextCursor.value } : null
    if (!query) return
    const gen = generation
    loadingMore.value = true
    try {
        const res = await activitiesApi.list(query)
        if (gen !== generation) return
        activities.value.push(...(res.activities || []))
        nextCursor.value = res.next_cursor || ''
    } catch (err) {
        if (gen !== generation) return
        errorMessage.value = detailOf(err)
    } finally {
        loadingMore.value = false
    }
}

// Reload when a preset range is selected. For a custom range wait for Apply, so half-typed input doesn't trigger requests.
watch(rangePreset, (preset) => {
    if (preset !== 'custom') reload()
})
// Resource type and result filters reload immediately under any range (a custom range uses the current input
// and reports an error if it is invalid).
watch([resourceType, result], () => reload())

// datetime-local expects local time as YYYY-MM-DDTHH:mm. Prefill the last 7 days when switching to custom.
const toLocalInput = (d: Date) => {
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
watch(rangePreset, (preset) => {
    if (preset === 'custom' && !customStart.value && !customEnd.value) {
        const now = new Date()
        customStart.value = toLocalInput(new Date(now.getTime() - 7 * DAY_MS))
        customEnd.value = toLocalInput(now)
    }
})
watch(() => regionStore.currentRegionId, (id) => { if (id) reload() })
onMounted(reload)

const hasFilter = computed(() => resourceType.value !== '' || result.value !== '')

// The reason for an invalid range or a failed request is shown in the banner above the table
const emptyText = computed(() => {
    if (rangeError.value || errorMessage.value) return t('dashboard.activityPage.loadFailed')
    return hasFilter.value ? t('dashboard.activityPage.emptyFiltered') : t('dashboard.activityPage.empty')
})
</script>

<template>
  <div class="vpc-list-container activity-page">
    <div class="detail-header">
      <button class="btn btn-ghost back-btn" @click="goBack">
        <ArrowLeft :size="18" />
        <span>{{ $t('dashboard.overview.title') }}</span>
      </button>
    </div>

    <div class="page-header">
      <div class="filter-wrapper">
        <select v-model="rangePreset" class="filter-select" :aria-label="$t('dashboard.activityPage.timeRange')">
          <option value="today">{{ $t('dashboard.activityPage.ranges.today') }}</option>
          <option value="7d">{{ $t('dashboard.activityPage.ranges.last7d') }}</option>
          <option value="30d">{{ $t('dashboard.activityPage.ranges.last30d') }}</option>
          <option value="90d">{{ $t('dashboard.activityPage.ranges.last90d') }}</option>
          <option value="custom">{{ $t('dashboard.activityPage.ranges.custom') }}</option>
        </select>
        <template v-if="rangePreset === 'custom'">
          <input v-model="customStart" type="datetime-local" class="filter-select" :aria-label="$t('dashboard.activityPage.start')" :title="$t('dashboard.activityPage.start')" />
          <span class="range-sep">~</span>
          <input v-model="customEnd" type="datetime-local" class="filter-select" :aria-label="$t('dashboard.activityPage.end')" :title="$t('dashboard.activityPage.end')" />
          <button class="btn btn-primary btn-sm" @click="reload">{{ $t('dashboard.activityPage.apply') }}</button>
        </template>
        <select v-model="resourceType" class="filter-select" :aria-label="$t('dashboard.activityPage.resourceType')">
          <option value="">{{ $t('dashboard.activityPage.allResourceTypes') }}</option>
          <option v-for="rt in RESOURCE_TYPES" :key="rt" :value="rt">{{ $t(`dashboard.activityPage.resourceTypes.${rt}`) }}</option>
        </select>
        <select v-model="result" class="filter-select" :aria-label="$t('dashboard.activityPage.result')">
          <option value="">{{ $t('dashboard.activityPage.allResults') }}</option>
          <option value="success">{{ $t('dashboard.activityPage.succeeded') }}</option>
          <option value="failed">{{ $t('dashboard.activityPage.failed') }}</option>
        </select>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" :title="$t('actions.refresh')" :aria-label="$t('actions.refresh')" @click="reload">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
      </div>
    </div>

    <div v-if="rangeError || errorMessage" class="error-banner">{{ rangeError || errorMessage }}</div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th class="col-time">{{ $t('dashboard.activityPage.colTime') }}</th>
            <th class="col-actor">{{ $t('dashboard.activityPage.colActor') }}</th>
            <th>{{ $t('dashboard.activityPage.colAction') }}</th>
            <th class="col-result">{{ $t('dashboard.activityPage.result') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="4" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="activities.length === 0">
            <td colspan="4" class="text-center text-secondary" style="padding: 48px;">
              <div class="empty-state">
                <History :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                <p>{{ emptyText }}</p>
                <button v-if="errorMessage" class="btn btn-secondary btn-sm" @click="reload">{{ $t('dashboard.overview.activityRetry') }}</button>
              </div>
            </td>
          </tr>
          <tr v-else v-for="a in activities" :key="a.id">
            <td class="col-time">
              <div>{{ relativeTime(a.created_at) }}</div>
              <div class="time-absolute">{{ absoluteTime(a.created_at) }}</div>
            </td>
            <td class="col-actor">{{ a.actor || $t('dashboard.overview.activityUnknownActor') }}</td>
            <td class="col-action"><ActivityText :activity="a" /></td>
            <td class="col-result">
              <span :class="['badge', a.success ? 'badge-success' : 'badge-error']">
                {{ a.success ? $t('dashboard.activityPage.succeeded') : $t('dashboard.activityPage.failed') }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && activities.length > 0" class="table-footer">
        <button v-if="nextCursor" class="btn btn-secondary btn-sm" :disabled="loadingMore" @click="loadMore">
          {{ loadingMore ? $t('dashboard.activityPage.loadingMore') : $t('dashboard.activityPage.loadMore') }}
        </button>
        <span v-else class="end-hint">{{ $t('dashboard.activityPage.noMore', { n: activities.length }) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Header, filters and table follow the other list pages (e.g. AlarmEvents.vue); the back button follows the detail pages */
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

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
  padding-right: 20px;
}

.filter-wrapper {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  flex: 1;
}

.header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

.filter-select {
  height: 40px;
  padding: 0 12px;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  font-size: 0.875rem;
  background: var(--bg-secondary);
  color: var(--text-primary);
  min-width: 120px;
  transition: border-color 0.2s, box-shadow 0.2s;
}

.filter-select:focus {
  outline: none;
  border-color: var(--primary-300);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.range-sep {
  color: var(--gray-400);
}

.error-banner {
  background: #fef2f2;
  color: #dc2626;
  border: 1px solid #fecaca;
  border-radius: 6px;
  padding: 10px 14px;
  margin-bottom: 12px;
  font-size: 13px;
}

.table-card {
  padding: 0;
  overflow: hidden;
}

.col-time {
  width: 190px;
  white-space: nowrap;
}

.time-absolute {
  font-size: 0.75rem;
  color: var(--gray-400);
}

.col-actor {
  width: 160px;
  font-weight: 500;
  color: var(--text-primary);
}

.col-action {
  color: var(--gray-700);
}

.col-result {
  width: 100px;
}

.table-footer {
  display: flex;
  justify-content: center;
  padding: 16px;
  border-top: 1px solid var(--border-light);
}

.end-hint {
  font-size: 0.8125rem;
  color: var(--gray-400);
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}

.empty-state p {
  margin: 0 0 12px;
}

.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}
</style>
