<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { migrationsApi, type Migration } from '../../api/migrations'
import { instancesApi, type Instance } from '../../api/instances'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { Search as SearchIcon, ArrowRightLeft, Plus, X, RefreshCw } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
const toast = useToast()
const migrationList = ref<Migration[]>([])
const loading = ref(false)
const searchQuery = ref('')
const router = useRouter()

// Create Migration State
const createModalVisible = ref(false)
const creatingMigration = ref(false)
const resourcesLoading = ref(false)
const availableInstances = ref<Instance[]>([])
const availableHypervisors = ref<Hypervisor[]>([])

const newMigrationForm = ref({
    instance_id: '',
    dest_node: '',
    migration_type: 'live'
})

const fetchMigrations = async () => {
    loading.value = true
    try {
        const response = await migrationsApi.fetchMigrations()
        const data = response.data as any
        migrationList.value = Array.isArray(data) ? data : (data.migrations || [])
    } catch (error) {
        console.error('API fetch failed:', error)
        migrationList.value = []
    } finally {
        loading.value = false
    }
}

const filteredMigrations = computed(() => {
    if (!searchQuery.value) return migrationList.value
    const query = searchQuery.value.toLowerCase()
    return migrationList.value.filter(m => 
        (m.instance_id && m.instance_id.toLowerCase().includes(query)) || 
        (m.id && m.id.toString().includes(query)) ||
        (m.source_node && m.source_node.toLowerCase().includes(query)) ||
        (m.dest_node && m.dest_node.toLowerCase().includes(query))
    )
})

const getStatusClass = (status: string) => {
    const s = (status || '').toLowerCase()
    if (s === 'completed' || s === 'done') return 'status-active'
    if (s === 'error' || s === 'failed') return 'status-error'
    if (s === 'running' || s === 'migrating') return 'status-pending'
    return ''
}

// Create Modal Logic
const openCreateModal = () => {
    newMigrationForm.value = {
        instance_id: '',
        dest_node: '',
        migration_type: 'live'
    }
    createModalVisible.value = true
    fetchResources()
}

const closeCreateModal = () => {
    createModalVisible.value = false
}

const fetchResources = async () => {
    resourcesLoading.value = true
    try {
        const [instRes, hypRes] = await Promise.all([
            instancesApi.fetchInstances(),
            hypervisorsApi.fetchHypervisors()
        ])
        
        const instData = instRes.data as any
        availableInstances.value = Array.isArray(instData) ? instData : (instData.instances || [])
        
        const hypData = hypRes.data as any
        availableHypervisors.value = Array.isArray(hypData) ? hypData : (hypData.hypervisors || [])
    } catch (err) {
        console.error('Error fetching resources for migration:', err)
    } finally {
        resourcesLoading.value = false
    }
}

const handleCreateMigration = async () => {
    if (!newMigrationForm.value.instance_id) {
        toast.error('Please select an instance to migrate.')
        return
    }

    creatingMigration.value = true
    try {
        const payload: any = {
            instance_id: newMigrationForm.value.instance_id
        }
        if (newMigrationForm.value.dest_node) {
            payload.dest_node = newMigrationForm.value.dest_node
        }
        if (newMigrationForm.value.migration_type) {
            payload.migration_type = newMigrationForm.value.migration_type
        }

        await migrationsApi.createMigration(payload)
        await fetchMigrations()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to start migration:', err)
        toast.error(err.response?.data?.error || 'Failed to start migration task.')
    } finally {
        creatingMigration.value = false
    }
}

onMounted(fetchMigrations)
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
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchMigrations" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.startMigration') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.nameId') }}</th>
            <th>{{ $t('dashboard.table.instanceId') }}</th>
            <th>{{ $t('dashboard.table.type') }}</th>
            <th>{{ $t('dashboard.table.sourceNode') }}</th>
            <th>{{ $t('dashboard.table.destNode') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.createdAt') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredMigrations.length === 0">
            <td colspan="7" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else class="empty-state">
                  <ArrowRightLeft :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noData') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="m in filteredMigrations" :key="m.id">
            <td>
              <router-link :to="{ name: 'migration-detail', params: { id: m.id.toString() } }" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon">
                    <ArrowRightLeft :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ $t('dashboard.table.migration') }}</div>
                    <div class="resource-id">{{ m.id }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td><code class="mono-value">{{ m.instance_id }}</code></td>
            <td>{{ m.migration_type || 'Unknown' }}</td>
            <td>{{ m.source_node || '-' }}</td>
            <td>{{ m.dest_node || '-' }}</td>
            <td>
              <span class="status-pill" :class="getStatusClass(m.status)">
                <span class="status-dot"></span>
                {{ m.status }}
              </span>
            </td>
            <td><span class="mono-value">{{ new Date(m.created_at).toLocaleString() }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Migration Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card" style="max-width: 500px;">
        <div class="modal-header">
           <h3>{{ $t('dashboard.migrationForm.title') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body" v-if="resourcesLoading">
            <div class="loading-spinner" style="margin: 40px auto;"></div>
        </div>

        <div class="modal-body" v-else>
          <div class="form-group row-gap">
               <label class="form-label">{{ $t('dashboard.migrationForm.instanceToMigrate') }} <span class="text-error">*</span></label>
              <select v-model="newMigrationForm.instance_id" class="form-select full-width">
                   <option value="" disabled>{{ $t('dashboard.forms.placeholder.none') }}</option>
                  <option v-for="inst in availableInstances" :key="inst.id" :value="inst.id">
                      {{ inst.hostname || inst.name }} ({{ inst.id }})
                  </option>
              </select>
          </div>

          <div class="form-group row-gap">
               <label class="form-label">{{ $t('dashboard.migrationForm.destinationNode') }}</label>
              <select v-model="newMigrationForm.dest_node" class="form-select full-width">
                   <option value="">{{ $t('dashboard.migrationForm.autoSelect') }}</option>
                  <option v-for="hyp in availableHypervisors" :key="hyp.hostname" :value="hyp.hostname">
                      {{ hyp.hostname }} ({{ hyp.hypervisor_type }})
                  </option>
              </select>
              <small class="text-secondary" style="display: block; margin-top: 4px;">Leave blank to let the scheduler choose.</small>
          </div>

          <div class="form-group row-gap">
               <label class="form-label">{{ $t('dashboard.migrationForm.migrationType') }}</label>
              <select v-model="newMigrationForm.migration_type" class="form-select full-width">
                   <option value="live">{{ $t('dashboard.migrationForm.liveMigration') }}</option>
                   <option value="cold">{{ $t('dashboard.migrationForm.coldMigration') }}</option>
              </select>
          </div>
        </div>

        <div class="modal-footer" v-if="!resourcesLoading">
           <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creatingMigration">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateMigration" :disabled="creatingMigration">
            <span v-if="creatingMigration" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
             {{ creatingMigration ? $t('dashboard.migrationForm.starting') : $t('dashboard.buttons.startMigration') }}
          </button>
        </div>
      </div>
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

/* .resource-info etc. are global from index.css */

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
  background: var(--gray-100);
  color: var(--gray-700);
}

.status-active {
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}

.status-error {
  background: rgba(239, 68, 68, 0.1);
  color: #ef4444;
}

.status-pending {
  background: rgba(245, 158, 11, 0.1);
  color: #f59e0b;
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

/* Modal Styles */
.row-gap {
    margin-bottom: 16px;
}
.full-width {
    width: 100%;
    padding: 8px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: transparent;
    color: var(--text-primary);
}

.header-actions { display: flex; gap: 8px; align-items: center; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
