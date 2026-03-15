<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { Search as SearchIcon, ServerCog } from 'lucide-vue-next'

const hypervisorList = ref<Hypervisor[]>([])
const loading = ref(false)
const searchQuery = ref('')

const fetchHypervisors = async () => {
    loading.value = true
    try {
        const response = await hypervisorsApi.fetchHypervisors()
        const data = response.data as any
        hypervisorList.value = Array.isArray(data) ? data : (data.hypervisors || [])
    } catch (error) {
        console.error('API fetch failed:', error)
        hypervisorList.value = []
    } finally {
        loading.value = false
    }
}

const filteredHypervisors = computed(() => {
    if (!searchQuery.value) return hypervisorList.value
    const query = searchQuery.value.toLowerCase()
    return hypervisorList.value.filter(h => 
        (h.hostname && h.hostname.toLowerCase().includes(query)) || 
        (h.host_ip && h.host_ip.toLowerCase().includes(query))
    )
})

const formatMemory = (mb: number) => {
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(1)} GB`
    }
    return `${mb} MB`
}

onMounted(fetchHypervisors)
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
            <th>{{ $t('dashboard.table.hostIp') }}</th>
            <th>{{ $t('dashboard.table.state') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.vcpus') }} (Used/Total)</th>
            <th>{{ $t('dashboard.table.memory') }} (Used/Total)</th>
            <th>{{ $t('dashboard.table.zone') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredHypervisors.length === 0">
            <td colspan="7" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else class="empty-state">
                  <ServerCog :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="h in filteredHypervisors" :key="h.id">
            <td>
              <router-link :to="{ name: 'hypervisor-detail', params: { id: h.id.toString() } }" class="resource-link">
                <div class="vpc-info">
                  <div class="resource-icon">
                    <ServerCog :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ h.hostname }}</div>
                    <div class="resource-id">{{ h.id }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td><code class="mono-value">{{ h.host_ip }}</code></td>
            <td>
              <span class="status-pill" :class="h.state === 'up' ? 'status-active' : 'status-error'">
                <span class="status-dot"></span>
                {{ h.state }}
              </span>
            </td>
            <td>
              <span class="status-pill" :class="h.status === 'enabled' ? 'status-active' : ''" :style="h.status !== 'enabled' ? 'background: var(--gray-100); color: var(--gray-700);' : ''">
                <span class="status-dot"></span>
                {{ h.status }}
              </span>
            </td>
            <td>{{ h.vcpus_used }} / {{ h.vcpus }}</td>
            <td>{{ formatMemory(h.memory_mb_used) }} / {{ formatMemory(h.memory_mb) }}</td>
            <td>{{ h.zone }}</td>
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

.status-error {
  background: rgba(239, 68, 68, 0.1);
  color: #ef4444;
}

.status-dot {
  width: 6px;
  height: 6px;
  background: currentColor;
  border-radius: 50%;
}

.mono-value {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
  color: var(--text-primary);
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}
</style>
