<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { alarmsApi, type AlarmPolicy } from '../../api/alarms'
import { Search as SearchIcon, AlertTriangle } from 'lucide-vue-next'

const alarmList = ref<AlarmPolicy[]>([])
const loading = ref(false)
const searchQuery = ref('')

const fetchAlarms = async () => {
    loading.value = true
    try {
        const response = await alarmsApi.fetchAlarms()
        const data = response.data as any
        alarmList.value = Array.isArray(data) ? data : (data.alarm_policies || [])
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
        (a.id && a.id.toString().includes(query)) ||
        (a.severity && a.severity.toLowerCase().includes(query))
    )
})

const getSeverityClass = (severity: string) => {
    const s = (severity || '').toLowerCase()
    if (s === 'critical' || s === 'high') return 'severity-high'
    if (s === 'warning' || s === 'medium') return 'severity-medium'
    return 'severity-low'
}

onMounted(fetchAlarms)
</script>

<template>
  <div class="vpc-list-container">
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <SearchIcon :size="16" class="search-icon" />
          <input 
            type="text" 
            v-model="searchQuery"
            :placeholder="$t('actions.search') + '...'" 
            class="search-input"
          />
        </div>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.severity') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.description') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="4" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredAlarms.length === 0">
            <td colspan="4" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else class="empty-state">
                  <AlertTriangle :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="a in filteredAlarms" :key="a.id">
            <td>
              <router-link :to="{ name: 'alarm-detail', params: { id: a.id.toString() } }" class="resource-link">
                <div class="vpc-info">
                  <div class="resource-icon">
                    <AlertTriangle :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ a.name || $t('dashboard.alarms') }}</div>
                    <div class="resource-id">{{ a.id || '-' }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <span class="severity-badge" :class="getSeverityClass(a.severity)">
                {{ a.severity || 'Normal' }}
              </span>
            </td>
            <td>
              <span class="status-pill" :class="a.status === 'enabled' || a.status === 'active' ? 'status-active' : ''" :style="a.status !== 'enabled' && a.status !== 'active' ? 'background: var(--gray-100); color: var(--gray-700);' : ''">
                <span class="status-dot"></span>
                {{ a.status || 'Active' }}
              </span>
            </td>
            <td>{{ a.description || '-' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
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
  flex: 1;
  max-width: 400px;
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
}

.search-box:focus-within {
  border-color: var(--primary-300);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.search-icon {
  color: var(--gray-400);
}

.search-input {
  border: none;
  background: transparent;
  width: 100%;
  height: 100%;
  font-size: 0.875rem;
  color: var(--text-primary);
}

.search-input:focus {
  outline: none;
}

.table-card {
  padding: 0;
  overflow: hidden;
}

.vpc-info {
  display: flex;
  align-items: center;
  gap: var(--spacing-03);
}

.resource-icon {
  width: 32px;
  height: 32px;
  background: var(--primary-light);
  color: var(--primary-color);
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
}

.resource-name {
  font-weight: var(--font-weight-semibold);
  color: var(--text-main);
  font-size: var(--font-size-sm);
}

.resource-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
  font-family: var(--font-family-mono);
}

.resource-link {
  text-decoration: none;
  display: block;
  padding: 4px 0;
  border-radius: var(--radius-sm);
  transition: all 0.15s;
}

.resource-link:hover .resource-name {
  color: var(--primary-600);
  text-decoration: underline;
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: var(--radius-full);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
}

.status-active {
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}

.status-dot {
  width: 6px;
  height: 6px;
  background: currentColor;
  border-radius: 50%;
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

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}
</style>
