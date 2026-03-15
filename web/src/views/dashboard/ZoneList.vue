<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { zonesApi, type Zone } from '../../api/zones'
import { Search as SearchIcon, MapPin } from 'lucide-vue-next'

const zoneList = ref<Zone[]>([])
const loading = ref(false)
const searchQuery = ref('')

const fetchZones = async () => {
    loading.value = true
    try {
        const response = await zonesApi.fetchZones()
        const data = response.data as any
        zoneList.value = Array.isArray(data) ? data : (data.zones || [])
    } catch (error) {
        console.error('API fetch failed:', error)
        zoneList.value = []
    } finally {
        loading.value = false
    }
}

const filteredZones = computed(() => {
    if (!searchQuery.value) return zoneList.value
    const query = searchQuery.value.toLowerCase()
    return zoneList.value.filter(z => 
        (z.name && z.name.toLowerCase().includes(query)) || 
        (z.id && z.id.toLowerCase().includes(query))
    )
})

onMounted(fetchZones)
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
      <!-- Add Zone button might go here if API supported creating zones -->
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.type') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="3" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredZones.length === 0">
            <td colspan="3" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else class="empty-state">
                  <MapPin :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="zone in filteredZones" :key="zone.name">
            <td>
              <router-link :to="{ name: 'zone-detail', params: { name: zone.name } }" class="resource-link">
                <div class="vpc-info">
                  <div class="resource-icon">
                    <MapPin :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ zone.name }}</div>
                    <div class="resource-id">{{ zone.id || '-' }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <span class="status-pill status-active">
                <span class="status-dot"></span>
                Active
              </span>
            </td>
            <td>
              <span class="subnet-badge">Default</span>
            </td>
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
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}

.status-dot {
  width: 6px;
  height: 6px;
  background: currentColor;
  border-radius: 50%;
}

.subnet-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  background: var(--gray-10);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}
</style>
