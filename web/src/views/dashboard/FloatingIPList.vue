<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { floatingIpsApi, subnetsApi, type FloatingIP, type Subnet } from '../../api/networks'
import { Globe2, Plus, Link, Unlink, Trash2, Search, X, RefreshCw } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const { t } = useI18n()
const router = useRouter()
const floatingIps = ref<FloatingIP[]>([])
const loading = ref(false)
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newFipForm = ref({
    name: '',
    selectedSubnetId: ''
})
const siteSubnets = ref<Subnet[]>([])

const fetchFloatingIPs = async () => {
    loading.value = true
    try {
        const [ipResponse, subnetsResponse] = await Promise.all([
            floatingIpsApi.list(),
            subnetsApi.list()
        ])
        floatingIps.value = ipResponse.floating_ips || []
        
        // Filter for site subnets (assuming type 'site' is what we want for FIP pools)
        // Adjust logic if 'public' is the correct type, but 'site' was mentioned in types
        siteSubnets.value = (subnetsResponse.subnets || []).filter(s => s.type === 'site')
    } catch (err) {
        console.error('API fetch failed:', err)
        floatingIps.value = []
        siteSubnets.value = []
    } finally {
        loading.value = false
    }
}

const openCreateModal = () => {
    newFipForm.value = {
        name: '',
        selectedSubnetId: siteSubnets.value[0]?.id || ''
    }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateIP = async () => {
    createError.value = ''
    if (!newFipForm.value.selectedSubnetId) {
        createError.value = 'Please select a subnet.'
        return
    }

    creating.value = true
    try {
        await floatingIpsApi.create({
            name: newFipForm.value.name || undefined,
            site_subnets: [{ id: newFipForm.value.selectedSubnetId }]
        })
        
        // Refresh list
        const response = await floatingIpsApi.list()
        floatingIps.value = response.floating_ips || []
        
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to create floating IP:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

const filteredFloatingIPs = computed(() => {
    if (!searchQuery.value) return floatingIps.value
    const query = searchQuery.value.toLowerCase()
    return floatingIps.value.filter(fip => 
        (fip.name?.toLowerCase() || '').includes(query) ||
        (fip.public_ip || fip.ip_address || '').toLowerCase().includes(query) || 
        (fip.id?.toLowerCase() || '').includes(query)
    )
})

// Status removed as requested
// const getStatusClass = (status: string) => {
//     return status === 'in-use' ? 'status-running' : 'status-active'
// }

const navigateToDetail = (fip: FloatingIP) => {
    router.push({ name: 'floating-ip-detail', params: { id: fip.id } })
}

const navigateToInstance = (instanceId: string) => {
    router.push({ name: 'instance-detail', params: { id: instanceId } })
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<FloatingIP | null>(null)

const handleDeleteClick = (item: FloatingIP) => {
    resourceToDelete.value = item
    deleteModalVisible.value = true
}
const closeDeleteModal = () => {
    deleteModalVisible.value = false
    resourceToDelete.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!resourceToDelete.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await floatingIpsApi.delete(resourceToDelete.value.id)
        await fetchFloatingIPs()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete floating IP:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

onMounted(fetchFloatingIPs)
</script>

<template>
  <div>
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <Search :size="16" class="search-icon" />
          <input 
            type="text" 
            v-model="searchQuery"
            :placeholder="$t('actions.search') + '...'" 
            class="search-input"
          />
        </div>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchFloatingIPs" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createIp') }}
        </button>
      </div>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.userName') }}</th>
            <th>{{ $t('dashboard.table.ipAddress') }}</th>
            <th>{{ $t('dashboard.table.type') }}</th>
            <th>{{ $t('dashboard.table.attachedTo') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="5" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredFloatingIPs.length === 0">
            <td colspan="5" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <Globe2 :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noFloatingIPs') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="fip in filteredFloatingIPs" :key="fip.id">
            <td>
              <router-link :to="{ name: 'floating-ip-detail', params: { id: fip.id } }" class="resource-link">
                <div class="resource-info">
                  <div class="resource-icon">
                    <Globe2 :size="16" />
                  </div>
                  <div>
                    <div class="resource-name">{{ fip.name || 'Unnamed' }}</div>
                    <div class="resource-id">{{ fip.id }}</div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <div class="ip-address">{{ fip.public_ip || fip.ip_address }}</div>
            </td>
            <td>
              <span class="type-badge">{{ fip.type || '-' }}</span>
            </td>
            <td>
              <span 
                v-if="fip.target_interface?.from_instance" 
                class="resource-link"
                @click="navigateToInstance(fip.target_interface.from_instance.id)"
              >
                {{ fip.target_interface.from_instance.hostname || 'Unnamed Instance' }}
              </span>
              <span v-else class="text-light">Not attached</span>
            </td>
            <td>
              <div class="actions">
                <button v-if="!fip.target_interface" class="btn btn-ghost btn-sm" title="Attach"
                  :disabled="fip.type !== 'floating' && fip.type !== 'site'">
                  <Link :size="14" /> Attach
                </button>
                <button v-else class="btn btn-ghost btn-sm" title="Detach"
                  :disabled="fip.type !== 'floating' && fip.type !== 'site'">
                  <Unlink :size="14" /> Detach
                </button>
                <button class="btn btn-ghost btn-sm text-error" title="Release"
                  :disabled="fip.type !== 'floating' && fip.type !== 'loadbalancer'"
                  @click="(fip.type === 'floating' || fip.type === 'loadbalancer') && handleDeleteClick(fip)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Floating IP Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createIp') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>
        
        <div class="modal-body">
          <p class="text-secondary mb-4">
            Allocate a new public IP address from a site subnet pool.
          </p>

          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
            <input 
              v-model="newFipForm.name" 
              type="text" 
              class="form-input" 
              placeholder="Optional: Enter a name for this FIP" 
            />
          </div>

          <div class="form-group">
            <label class="form-label">Subnet Pool</label>
            <div class="select-wrapper">
                <select v-model="newFipForm.selectedSubnetId" class="form-input">
                    <option v-for="subnet in siteSubnets" :key="subnet.id" :value="subnet.id">
                        {{ subnet.name }} ({{ subnet.network || subnet.network_cidr }})
                    </option>
                </select>
            </div>
          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleCreateIP" :disabled="creating">
            <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createIp') }}
          </button>
        </div>
      </div>
    </div>

    <DeleteModal
      :show="deleteModalVisible"
      :resource-name="resourceToDelete?.public_ip || resourceToDelete?.ip_address"
      :resource-id="resourceToDelete?.id"
      :loading="deletingResource"
      :error="deleteError"
      @close="closeDeleteModal"
      @confirm="confirmDelete"
    />
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0px;
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
  overflow: visible;
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

.actions {
  display: flex;
  gap: var(--spacing-02);
}

.text-error {
  color: var(--error-color);
}

.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.header-actions { display: flex; gap: 8px; align-items: center; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
