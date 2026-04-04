<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { floatingIpsApi, subnetsApi, type FloatingIP, type FloatingIPPayload, type Subnet } from '../../api/networks'
import { instancesApi, type Instance } from '../../api/instances'
import { Globe2, Plus, Link, Unlink, Trash2, Search, X, RefreshCw, Check, Copy, ChevronDown, ChevronUp } from 'lucide-vue-next'
import { useFloatingIP } from '../../composables/useFloatingIP'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import { useRegionStore } from '../../stores/region'

const region = useRegionStore()

const { t } = useI18n()
const toast = useToast()
const { getTypeBadgeClass, getTypeLabel } = useFloatingIP()
const router = useRouter()

const { copiedId, copyId } = useCopyId()
const floatingIps = ref<FloatingIP[]>([])
const loading = ref(false)
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const showAdvanced = ref(false)

const newFipForm = ref({
    name: '',
    selectedSiteSubnetId: '',
    selectedPublicSubnetId: '',
    inbound: null as number | null,
    outbound: null as number | null,
    instanceId: '',
    publicIp: '',
    activationCount: null as number | null,
})

const siteSubnets = ref<Subnet[]>([])
const publicSubnets = ref<Subnet[]>([])
const instances = ref<Instance[]>([])
const subnetAddresses = ref<Record<string, any[]>>({})
const addressesLoading = ref<Record<string, boolean>>({})

const fetchSubnetAddresses = async (subnetId: string) => {
    if (!subnetId) return
    addressesLoading.value[subnetId] = true
    try {
        const response = await subnetsApi.listAddresses(subnetId)
        subnetAddresses.value[subnetId] = (response.addresses || []).filter((a: any) => !a.allocated && !a.reserved)
    } catch (err) {
        console.error('Failed to fetch subnet addresses:', err)
    } finally {
        addressesLoading.value[subnetId] = false
    }
}

const fetchFloatingIPs = async () => {
    loading.value = true
    try {
        const [ipResponse, subnetsResponse] = await Promise.all([
            floatingIpsApi.list(),
            subnetsApi.list()
        ])
        floatingIps.value = ipResponse.floating_ips || []

        const allSubnets = subnetsResponse.subnets || []
        siteSubnets.value = allSubnets.filter(s => s.type === 'site')
        publicSubnets.value = allSubnets.filter(s => s.type === 'public')
    } catch (err) {
        console.error('API fetch failed:', err)
        floatingIps.value = []
        siteSubnets.value = []
        publicSubnets.value = []
    } finally {
        loading.value = false
    }
}

const fetchInstances = async () => {
    try {
        const response = await instancesApi.fetchInstances()
        instances.value = (response.data?.instances || []).filter((inst: Instance) =>
            inst.vpc && inst.status !== 'provisioning' && inst.interfaces?.some(iface => iface.is_primary)
        )
    } catch (err) {
        console.error('Failed to fetch instances:', err)
        instances.value = []
    }
}

const openCreateModal = () => {
    newFipForm.value = {
        name: '',
        selectedSiteSubnetId: '',
        selectedPublicSubnetId: '',
        inbound: null,
        outbound: null,
        instanceId: '',
        publicIp: '',
        activationCount: null,
    }
    subnetAddresses.value = {}
    showAdvanced.value = false
    createError.value = ''
    createModalVisible.value = true
    fetchInstances()
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateIP = async () => {
    createError.value = ''
    const form = newFipForm.value

    if (!form.name || form.name.length < 2 || form.name.length > 32) {
        createError.value = t('dashboard.floatingIPDetail.nameRequired')
        return
    }

    creating.value = true
    try {
        const payload: FloatingIPPayload = {
            name: form.name,
        }

        if (form.selectedSiteSubnetId) {
            payload.site_subnets = [{ id: form.selectedSiteSubnetId }]
        }
        if (form.selectedPublicSubnetId) {
            payload.public_subnets = [{ id: form.selectedPublicSubnetId }]
        }
        if (form.inbound && form.inbound > 0) {
            payload.inbound = form.inbound
        }
        if (form.outbound && form.outbound > 0) {
            payload.outbound = form.outbound
        }
        if (form.instanceId) {
            payload.instance = { id: form.instanceId }
        }
        if (form.publicIp) {
            if (!/^(\d{1,3}\.){3}\d{1,3}$/.test(form.publicIp)) {
                createError.value = t('dashboard.floatingIPDetail.invalidIp')
                creating.value = false
                return
            }
            payload.public_ip = form.publicIp
        }
        if (form.activationCount && form.activationCount > 0) {
            payload.activation_count = form.activationCount
        }

        await floatingIpsApi.create(payload)

        const response = await floatingIpsApi.list()
        floatingIps.value = response.floating_ips || []

        closeCreateModal()
        toast.success(t('messages.createSuccess'))
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
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete floating IP:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

// --- Attach Modal ---
const attachModalVisible = ref(false)
const attaching = ref(false)
const attachError = ref('')
const fipToAttach = ref<FloatingIP | null>(null)
const selectedInstanceId = ref('')

const handleAttachClick = (fip: FloatingIP) => {
    fipToAttach.value = fip
    selectedInstanceId.value = ''
    attachError.value = ''
    attachModalVisible.value = true
    fetchInstances()
}

const closeAttachModal = () => {
    attachModalVisible.value = false
    fipToAttach.value = null
    attachError.value = ''
}

const confirmAttach = async () => {
    if (!fipToAttach.value || !selectedInstanceId.value) {
        attachError.value = t('dashboard.floatingIPDetail.selectInstance')
        return
    }
    attaching.value = true
    attachError.value = ''
    try {
        await floatingIpsApi.attach(fipToAttach.value.id, selectedInstanceId.value)
        await fetchFloatingIPs()
        closeAttachModal()
        toast.success(t('messages.attachSuccess') || t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to attach floating IP:', err)
        attachError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        attaching.value = false
    }
}

// --- Detach Confirmation Modal ---
const detachModalVisible = ref(false)
const detaching = ref(false)
const detachError = ref('')
const fipToDetach = ref<FloatingIP | null>(null)

const handleDetachClick = (fip: FloatingIP) => {
    fipToDetach.value = fip
    detachError.value = ''
    detachModalVisible.value = true
}

const closeDetachModal = () => {
    detachModalVisible.value = false
    fipToDetach.value = null
    detachError.value = ''
}

const confirmDetach = async () => {
    if (!fipToDetach.value) return
    detaching.value = true
    detachError.value = ''
    try {
        await floatingIpsApi.detach(fipToDetach.value.id)
        await fetchFloatingIPs()
        closeDetachModal()
        toast.success(t('messages.detachSuccess') || t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to detach floating IP:', err)
        detachError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        detaching.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchFloatingIPs()
    }
})

// Re-fetch when region changes
watch(() => region.currentRegionId, (newId) => {
    if (newId) {
        fetchFloatingIPs()
    }
})

watch(() => newFipForm.value.selectedSiteSubnetId, (newId) => {
    if (newId) {
        newFipForm.value.selectedPublicSubnetId = ''
        fetchSubnetAddresses(newId)
    }
})

watch(() => newFipForm.value.selectedPublicSubnetId, (newId) => {
    if (newId) {
        newFipForm.value.selectedSiteSubnetId = ''
        fetchSubnetAddresses(newId)
    }
})
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
                    <div class="resource-name">{{ fip.name || $t('messages.unnamed') }}</div>
                    <div class="resource-id-row">
                      <span class="resource-id" :title="fip.id">{{ fip.id.slice(0, 8) }}...</span>
                      <button class="copy-btn-mini" @click.stop.prevent="copyId(fip.id)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                        <Check v-if="copiedId === fip.id" :size="10" style="color: #10b981;" />
                        <Copy v-else :size="10" />
                      </button>
                    </div>
                  </div>
                </div>
              </router-link>
            </td>
            <td>
              <div class="ip-address monospace">{{ fip.public_ip || fip.ip_address }}</div>
            </td>
            <td>
              <span :class="['badge', getTypeBadgeClass(fip.type || '')]">
                {{ getTypeLabel(fip.type || '') }}
              </span>
            </td>
            <td>
              <span 
                v-if="fip.target_interface?.from_instance" 
                class="resource-link"
                @click="navigateToInstance(fip.target_interface.from_instance.id)"
              >
                {{ fip.target_interface.from_instance.hostname || $t('messages.unnamed') }}
              </span>
              <span v-else class="text-light">{{ $t('messages.notAttached') }}</span>
            </td>
            <td>
              <div class="actions">
                <button v-if="!fip.target_interface" class="btn btn-ghost btn-sm" :title="$t('actions.attach')"
                  :disabled="fip.type !== 'floating' && fip.type !== 'site'"
                  @click="(fip.type === 'floating' || fip.type === 'site') && handleAttachClick(fip)">
                  <Link :size="14" /> {{ $t('actions.attach') }}
                </button>
                <button v-else class="btn btn-ghost btn-sm" :title="$t('actions.detach')"
                  :disabled="fip.type !== 'floating' && fip.type !== 'site'"
                  @click="(fip.type === 'floating' || fip.type === 'site') && handleDetachClick(fip)">
                  <Unlink :size="14" /> {{ $t('actions.detach') }}
                </button>
                <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.release')"
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
      <div class="modal-content card modal-wide">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createIp') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal">
            <X :size="20" />
          </button>
        </div>

        <div class="modal-body">
          <p class="text-secondary mb-4">
            {{ $t('dashboard.floatingIPDetail.createDesc') }}
          </p>

          <!-- Name (required) -->
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }} <span class="text-error">*</span></label>
            <input
              v-model="newFipForm.name"
              type="text"
              class="form-input"
              maxlength="32"
              :placeholder="$t('dashboard.floatingIPDetail.namePlaceholder')"
            />
          </div>

          <!-- Bandwidth -->
          <div class="form-row">
            <div class="form-group form-group-half">
              <label class="form-label">{{ $t('dashboard.floatingIPDetail.inbound') }}</label>
              <div class="input-with-suffix">
                <input
                  v-model.number="newFipForm.inbound"
                  type="number"
                  class="form-input"
                  min="1" max="20000"
                  :placeholder="$t('dashboard.floatingIPDetail.inboundPlaceholder')"
                />
                <span class="input-suffix">{{ $t('dashboard.floatingIPDetail.mbps') }}</span>
              </div>
            </div>
            <div class="form-group form-group-half">
              <label class="form-label">{{ $t('dashboard.floatingIPDetail.outbound') }}</label>
              <div class="input-with-suffix">
                <input
                  v-model.number="newFipForm.outbound"
                  type="number"
                  class="form-input"
                  min="1" max="20000"
                  :placeholder="$t('dashboard.floatingIPDetail.outboundPlaceholder')"
                />
                <span class="input-suffix">{{ $t('dashboard.floatingIPDetail.mbps') }}</span>
              </div>
            </div>
          </div>

          <!-- Site Subnet -->
          <div class="form-group" v-if="siteSubnets.length > 0">
            <label class="form-label">{{ $t('dashboard.floatingIPDetail.subnetPool') }}</label>
            <div class="select-wrapper">
              <select v-model="newFipForm.selectedSiteSubnetId" class="form-input">
                <option value="">{{ $t('dashboard.floatingIPDetail.selectSubnet') }}</option>
                <option v-for="subnet in siteSubnets" :key="subnet.id" :value="subnet.id">
                  {{ subnet.name }} ({{ subnet.network || subnet.network_cidr }})
                </option>
              </select>
            </div>
          </div>

          <!-- Public Subnet -->
          <div class="form-group" v-if="publicSubnets.length > 0">
            <label class="form-label">{{ $t('dashboard.floatingIPDetail.publicSubnet') }}</label>
            <div class="select-wrapper">
              <select v-model="newFipForm.selectedPublicSubnetId" class="form-input">
                <option value="">{{ $t('dashboard.floatingIPDetail.selectSubnet') }}</option>
                <option v-for="subnet in publicSubnets" :key="subnet.id" :value="subnet.id">
                  {{ subnet.name }} ({{ subnet.network || subnet.network_cidr }})
                </option>
              </select>
            </div>
          </div>

          <!-- Public IP -->
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.floatingIPDetail.publicIp') }}</label>
            <div class="select-wrapper" v-if="newFipForm.selectedSiteSubnetId || newFipForm.selectedPublicSubnetId">
              <select v-model="newFipForm.publicIp" class="form-input" :disabled="addressesLoading[newFipForm.selectedSiteSubnetId || newFipForm.selectedPublicSubnetId]">
                <option value="">{{ $t('dashboard.floatingIPDetail.autoAllocate') || '自动分配' }}</option>
                <option v-for="addr in (subnetAddresses[newFipForm.selectedSiteSubnetId || newFipForm.selectedPublicSubnetId] || [])" :key="addr.address" :value="addr.address.split('/')[0]">
                  {{ addr.address.split('/')[0] }}
                </option>
              </select>
            </div>
            <input
              v-else
              v-model="newFipForm.publicIp"
              type="text"
              class="form-input"
              :placeholder="$t('dashboard.floatingIPDetail.publicIpPlaceholder')"
            />
          </div>

          <!-- Bind Instance -->
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.floatingIPDetail.bindInstance') }}</label>
            <div class="select-wrapper">
              <select v-model="newFipForm.instanceId" class="form-input">
                <option value="">{{ $t('dashboard.floatingIPDetail.selectInstance') }}</option>
                <option v-for="inst in instances" :key="inst.id" :value="inst.id">
                  {{ inst.hostname || inst.name }} ({{ inst.ip_address || inst.id }})
                </option>
              </select>
            </div>
          </div>

          <!-- Advanced Options -->
          <div class="advanced-toggle" @click="showAdvanced = !showAdvanced">
            <span>{{ $t('dashboard.floatingIPDetail.advancedOptions') }}</span>
            <ChevronDown v-if="!showAdvanced" :size="16" />
            <ChevronUp v-else :size="16" />
          </div>

          <div v-if="showAdvanced" class="advanced-section">
            <!-- Activation Count -->
            <div class="form-group">
              <label class="form-label">{{ $t('dashboard.floatingIPDetail.activationCount') }}</label>
              <input
                v-model.number="newFipForm.activationCount"
                type="number"
                class="form-input"
                min="1" max="64"
                :placeholder="$t('dashboard.floatingIPDetail.activationCountPlaceholder')"
              />
            </div>
          </div>
        </div>

        <div class="modal-footer" style="flex-direction: column; align-items: stretch; gap: var(--spacing-2);">
          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>
          <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2);">
            <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
            <button class="btn btn-primary" @click="handleCreateIP" :disabled="creating">
              <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createIp') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Attach Floating IP Modal -->
    <div v-if="attachModalVisible" class="modal-overlay" @click.self="closeAttachModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('actions.attach') }} - {{ fipToAttach?.name || fipToAttach?.public_ip }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeAttachModal">
            <X :size="20" />
          </button>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.floatingIPDetail.bindInstance') }} <span class="text-error">*</span></label>
            <div class="select-wrapper">
              <select v-model="selectedInstanceId" class="form-input">
                <option value="">{{ $t('dashboard.floatingIPDetail.selectInstance') }}</option>
                <option v-for="inst in instances" :key="inst.id" :value="inst.id">
                  {{ inst.hostname || inst.name }} ({{ inst.ip_address || inst.id }})
                </option>
              </select>
            </div>
          </div>
        </div>
        <div v-if="attachError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ attachError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeAttachModal" :disabled="attaching">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="confirmAttach" :disabled="attaching || !selectedInstanceId">
            <span v-if="attaching" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ attaching ? $t('messages.loading') : $t('actions.attach') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Detach Confirmation Modal -->
    <div v-if="detachModalVisible" class="modal-overlay" @click.self="closeDetachModal">
      <div class="modal-content card">
        <div class="modal-header">
          <h3>{{ $t('actions.detach') }} - {{ fipToDetach?.name || fipToDetach?.public_ip }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeDetachModal">
            <X :size="20" />
          </button>
        </div>
        <div class="modal-body">
          <p class="text-secondary">
            {{ $t('messages.confirmDetach') }}
          </p>
        </div>
        <div v-if="detachError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ detachError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeDetachModal" :disabled="detaching">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="confirmDetach" :disabled="detaching">
            <span v-if="detaching" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ detaching ? $t('messages.loading') : $t('actions.detach') }}
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

.monospace { font-family: var(--font-family-mono); }

.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.header-actions { display: flex; gap: 8px; align-items: center; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.modal-wide {
  width: 560px;
  max-width: 90vw;
}

.form-row {
  display: flex;
  gap: var(--spacing-4);
}

.form-group-half {
  flex: 1;
}

.input-with-suffix {
  position: relative;
  display: flex;
  align-items: center;
}

.input-with-suffix .form-input {
  padding-right: 50px;
}

.input-suffix {
  position: absolute;
  right: 12px;
  color: var(--gray-400);
  font-size: var(--font-size-sm);
  pointer-events: none;
}

.advanced-toggle {
  display: flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
  color: var(--primary-600);
  font-size: var(--font-size-sm);
  padding: var(--spacing-2) 0;
  user-select: none;
}

.advanced-toggle:hover {
  color: var(--primary-700);
}

.advanced-section {
  padding-top: var(--spacing-2);
  border-top: 1px solid var(--border-light);
  margin-top: var(--spacing-2);
}
</style>
