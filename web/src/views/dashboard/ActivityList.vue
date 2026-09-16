<script setup lang="ts">
// Activity page: all operations of the current organization in the current region, filtered by time range,
// resource type and result. "Load more" pages by cursor: new entries keep arriving, so page numbers / offsets would
// duplicate or skip entries.
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw, History } from 'lucide-vue-next'
import { activitiesApi, type Activity, type ActivityQuery } from '../../api/activities'
import { useRegionStore } from '../../stores/region'
import ActivityEntry from '../../components/activity/ActivityEntry.vue'

const { t } = useI18n()
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
</script>

<template>
  <div class="activity-page">
    <div class="filter-bar">
      <div class="filter-group">
        <label for="activity-range">{{ $t('dashboard.activityPage.timeRange') }}</label>
        <select id="activity-range" v-model="rangePreset" class="filter-control">
          <option value="today">{{ $t('dashboard.activityPage.ranges.today') }}</option>
          <option value="7d">{{ $t('dashboard.activityPage.ranges.last7d') }}</option>
          <option value="30d">{{ $t('dashboard.activityPage.ranges.last30d') }}</option>
          <option value="90d">{{ $t('dashboard.activityPage.ranges.last90d') }}</option>
          <option value="custom">{{ $t('dashboard.activityPage.ranges.custom') }}</option>
        </select>
      </div>
      <template v-if="rangePreset === 'custom'">
        <div class="filter-group">
          <label for="activity-start">{{ $t('dashboard.activityPage.start') }}</label>
          <input id="activity-start" v-model="customStart" type="datetime-local" class="filter-control" />
        </div>
        <div class="filter-group">
          <label for="activity-end">{{ $t('dashboard.activityPage.end') }}</label>
          <input id="activity-end" v-model="customEnd" type="datetime-local" class="filter-control" />
        </div>
        <button class="btn btn-primary btn-sm apply-btn" @click="reload">{{ $t('dashboard.activityPage.apply') }}</button>
      </template>
      <div class="filter-group">
        <label for="activity-type">{{ $t('dashboard.activityPage.resourceType') }}</label>
        <select id="activity-type" v-model="resourceType" class="filter-control">
          <option value="">{{ $t('dashboard.activityPage.all') }}</option>
          <option v-for="rt in RESOURCE_TYPES" :key="rt" :value="rt">{{ $t(`dashboard.activityPage.resourceTypes.${rt}`) }}</option>
        </select>
      </div>
      <div class="filter-group">
        <label for="activity-result">{{ $t('dashboard.activityPage.result') }}</label>
        <select id="activity-result" v-model="result" class="filter-control">
          <option value="">{{ $t('dashboard.activityPage.all') }}</option>
          <option value="success">{{ $t('dashboard.activityPage.succeeded') }}</option>
          <option value="failed">{{ $t('dashboard.activityPage.failed') }}</option>
        </select>
      </div>
      <button class="btn btn-secondary btn-sm btn-icon refresh-btn" :title="$t('actions.refresh')" :aria-label="$t('actions.refresh')" @click="reload">
        <RefreshCw :size="14" :class="{ spinning: loading }" />
      </button>
    </div>

    <div class="card list-card">
      <div v-if="loading" class="state-box">
        <div class="loading-spinner"></div>
      </div>
      <div v-else-if="errorMessage && activities.length === 0" class="state-box">
        <p>{{ errorMessage }}</p>
        <button class="btn btn-secondary btn-sm" @click="reload">{{ $t('dashboard.overview.activityRetry') }}</button>
      </div>
      <div v-else-if="rangeError" class="state-box">
        <p>{{ rangeError }}</p>
      </div>
      <div v-else-if="activities.length === 0" class="state-box">
        <History :size="40" class="state-icon" />
        <p>{{ hasFilter ? $t('dashboard.activityPage.emptyFiltered') : $t('dashboard.activityPage.empty') }}</p>
      </div>
      <template v-else>
        <ul class="activity-list">
          <ActivityEntry v-for="a in activities" :key="a.id" :activity="a" show-absolute-time />
        </ul>
        <div class="list-footer">
          <p v-if="errorMessage" class="range-error">{{ errorMessage }}</p>
          <button v-if="nextCursor" class="btn btn-secondary btn-sm" :disabled="loadingMore" @click="loadMore">
            {{ loadingMore ? $t('dashboard.activityPage.loadingMore') : $t('dashboard.activityPage.loadMore') }}
          </button>
          <span v-else class="end-hint">{{ $t('dashboard.activityPage.noMore', { n: activities.length }) }}</span>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.activity-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.filter-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 12px;
}

.filter-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.filter-group label {
  font-size: 0.75rem;
  color: var(--text-secondary);
}

.filter-control {
  height: 32px;
  padding: 0 8px;
  font-size: 0.85rem;
  color: var(--text-primary);
  background: var(--bg-primary);
  border: 1px solid var(--border-light);
  border-radius: var(--radius-sm);
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
}

.filter-control:hover {
  border-color: var(--border-default);
}

.filter-control:focus {
  outline: none;
  border-color: var(--primary-color);
  box-shadow: 0 0 0 3px var(--primary-100);
}

.apply-btn,
.refresh-btn {
  height: 32px;
}

.refresh-btn {
  margin-left: auto;
}

.range-error {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--error-color);
}

.list-card {
  padding: 24px;
}

.activity-list {
  list-style: none;
  padding: 0;
  margin: 0;
}

.state-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 48px 0;
  color: var(--gray-400);
  font-size: 0.875rem;
}

.state-box p {
  margin: 0;
}

.state-icon {
  opacity: 0.4;
}

.list-footer {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--border-light);
}

.end-hint {
  font-size: 0.8125rem;
  color: var(--gray-400);
}

.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}
</style>
