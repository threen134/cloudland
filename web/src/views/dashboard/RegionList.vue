<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
    Globe2, Plus, Search, RefreshCw, Settings2, Trash2, X,
    CheckCircle2, AlertCircle, KeyRound, Copy, Check, Loader2, ExternalLink, Wrench
} from 'lucide-vue-next'
import { regionsApi, type RegionPublic, type RegionAdmin, type RegionCreated, type CreateRegionPayload, type UpdateRegionPayload } from '../../api/regions'
import { useToast } from '../../composables/useToast'

const { t } = useI18n()
const toast = useToast()
const router = useRouter()

const regions = ref<RegionPublic[]>([])
const isLoading = ref(false)
const searchQuery = ref('')

// Create modal
const showCreateModal = ref(false)
const creating = ref(false)
const createForm = ref<CreateRegionPayload>({
    name: '',
    display_name: '',
    internal_endpoint: '',
    internal_secret: 'auto-generate',
    description: ''
})
const endpointHost = ref('')
const endpointPort = ref('8255')
const createdSecret = ref<string | null>(null)

// Edit modal
const showEditModal = ref(false)
const editing = ref(false)
const editingRegion = ref<RegionAdmin | null>(null)
const editForm = ref<UpdateRegionPayload>({})

// Delete modal
const showDeleteModal = ref(false)
const deleting = ref(false)
const deletingRegion = ref<RegionPublic | null>(null)

// Rotate secret modal
const showRotateModal = ref(false)
const rotating = ref(false)
const rotatingRegion = ref<RegionPublic | null>(null)
const rotatedSecret = ref<string | null>(null)

// Copy
const copiedField = ref<string | null>(null)

const filteredRegions = computed(() => {
    if (!searchQuery.value) return regions.value
    const q = searchQuery.value.toLowerCase()
    return regions.value.filter(r =>
        r.name.toLowerCase().includes(q) ||
        (r.display_name && r.display_name.toLowerCase().includes(q)) ||
        r.uuid.toLowerCase().includes(q)
    )
})

const fetchRegions = async () => {
    isLoading.value = true
    try {
        const response = await regionsApi.fetchRegions()
        const data = response.data
        regions.value = Array.isArray(data) ? data : []
    } catch (err) {
        console.error('Failed to fetch regions:', err)
        regions.value = []
    } finally {
        isLoading.value = false
    }
}

const splitEndpoint = (endpoint: string) => {
    if (!endpoint) return { host: '', port: '8255' }
    try {
        if (endpoint.startsWith('http://') || endpoint.startsWith('https://')) {
            const url = new URL(endpoint)
            return {
                host: `${url.protocol}//${url.hostname}`,
                port: url.port || '8255'
            }
        }
    } catch (e) {}
    return { host: endpoint, port: '8255' }
}

const ensurePort = (endpoint: string) => {
    // keeping this as safety fallback but will use the combined host+port primarily
    if (!endpoint) return endpoint
    try {
        if (endpoint.startsWith('http://') || endpoint.startsWith('https://')) {
            const url = new URL(endpoint)
            if (!url.port) {
                url.port = '8255'
                const result = url.toString()
                return endpoint.endsWith('/') ? result : result.replace(/\/$/, '')
            }
        }
    } catch (e) {}
    return endpoint
}

// Create
const openCreateModal = () => {
    createForm.value = { name: '', display_name: '', internal_endpoint: '', internal_secret: 'auto-generate', description: '' }
    endpointHost.value = ''
    endpointPort.value = '8255'
    createdSecret.value = null
    showCreateModal.value = true
}

const handleCreate = async () => {
    if (!createForm.value.name || !endpointHost.value) return
    
    let host = endpointHost.value.trim()
    if (!host.startsWith('http://') && !host.startsWith('https://')) {
        host = 'http://' + host
    }

    const port = endpointPort.value.trim() || '8255'
    createForm.value.internal_endpoint = `${host}:${port}`.replace(/:+$/, '')

    creating.value = true
    try {
        const response = await regionsApi.createRegion(createForm.value)
        const created = response.data as RegionCreated
        createdSecret.value = created.internal_secret
        toast.success(t('dashboard.regionActions.createdSuccess'))
        await fetchRegions()
        if (!createdSecret.value) {
            router.go(0)
        }
    } catch (err: any) {
        let msg = err.response?.data?.detail
        if (Array.isArray(msg)) msg = msg[0]?.msg
        msg = msg || err.response?.data?.message || 'Create failed'
        toast.error(msg)
    } finally {
        creating.value = false
    }
}

// Edit
const openEditModal = async (region: RegionPublic) => {
    try {
        const response = await regionsApi.getRegion(region.uuid)
        const detail = response.data as RegionAdmin
        editingRegion.value = detail
        
        const { host, port } = splitEndpoint(detail.internal_endpoint)
        endpointHost.value = host
        endpointPort.value = port

        editForm.value = {
            display_name: detail.display_name || '',
            internal_endpoint: detail.internal_endpoint,
            maintenance_mode: detail.maintenance_mode,
            description: detail.description || ''
        }
        showEditModal.value = true
    } catch (err: any) {
        toast.error(err.response?.data?.detail || 'Failed to load region details')
    }
}

const handleEdit = async () => {
    if (!editingRegion.value) return

    let host = endpointHost.value.trim()
    if (host) {
        if (!host.startsWith('http://') && !host.startsWith('https://')) {
            host = 'http://' + host
        }
        const port = endpointPort.value.trim() || '8255'
        editForm.value.internal_endpoint = `${host}:${port}`.replace(/:+$/, '')
    }

    editing.value = true
    try {
        await regionsApi.updateRegion(editingRegion.value.uuid, editForm.value)
        showEditModal.value = false
        toast.success(t('messages.success'))
        await fetchRegions()
        router.go(0)
    } catch (err: any) {
        toast.error(err.response?.data?.detail || 'Update failed')
    } finally {
        editing.value = false
    }
}

// Delete
const confirmDelete = (region: RegionPublic) => {
    deletingRegion.value = region
    showDeleteModal.value = true
}

const handleDelete = async () => {
    if (!deletingRegion.value) return
    deleting.value = true
    try {
        await regionsApi.deleteRegion(deletingRegion.value.uuid)
        showDeleteModal.value = false
        deletingRegion.value = null
        toast.success(t('messages.success'))
        await fetchRegions()
    } catch (err: any) {
        toast.error(err.response?.data?.detail || 'Delete failed')
    } finally {
        deleting.value = false
    }
}

const closeAndReload = () => {
    showCreateModal.value = false
    router.go(0)
}

// Rotate secret
const confirmRotate = (region: RegionPublic) => {
    rotatingRegion.value = region
    rotatedSecret.value = null
    showRotateModal.value = true
}

const handleRotate = async () => {
    if (!rotatingRegion.value) return
    rotating.value = true
    try {
        const response = await regionsApi.rotateSecret(rotatingRegion.value.uuid)
        rotatedSecret.value = (response.data as any).new_secret
        toast.success(t('messages.success'))
    } catch (err: any) {
        toast.error(err.response?.data?.detail || 'Rotate failed')
    } finally {
        rotating.value = false
    }
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        toast.success(t('dashboard.regionActions.secretCopied'))
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

onMounted(fetchRegions)
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
                        :placeholder="t('actions.search') + '...'" 
                        class="search-input"
                    />
                </div>
            </div>
            <div class="header-actions">
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchRegions" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ 'spinning': isLoading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </div>
        </div>

        <div class="card table-card">
            <table class="data-table">
                <thead>
                    <tr>
                        <th>{{ t('dashboard.table.nameId') }}</th>
                        <th>{{ t('dashboard.table.status') }}</th>
                        <th>{{ t('dashboard.table.description') }}</th>
                        <th>{{ t('dashboard.table.actions') }}</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-if="isLoading && regions.length === 0">
                        <td colspan="4" class="text-center" style="padding: 48px;">
                            <div class="loading-spinner" style="margin: 0 auto;"></div>
                        </td>
                    </tr>
                    <tr v-else-if="filteredRegions.length === 0">
                        <td colspan="4" class="text-center text-secondary" style="padding: 48px;">
                            <div v-if="searchQuery">
                                <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                                <p>{{ t('messages.noResults') }}</p>
                            </div>
                            <div v-else>
                                <Globe2 :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                                <p>{{ t('dashboard.regionActions.noRegions') }}</p>
                            </div>
                        </td>
                    </tr>
                    <tr v-else v-for="region in filteredRegions" :key="region.uuid">
                        <td>
                            <div class="resource-info">
                                <div class="resource-icon">
                                    <Globe2 :size="16" />
                                </div>
                                <div>
                                    <div class="resource-name">{{ region.display_name || region.name }}</div>
                                    <div class="resource-id">{{ region.name }} / {{ region.uuid }}</div>
                                </div>
                            </div>
                        </td>
                        <td>
                            <span class="status-pill"
                                :class="region.maintenance_mode ? 'status-maintenance' : region.is_available ? 'status-available' : 'status-offline'">
                                <Wrench v-if="region.maintenance_mode" :size="12" />
                                <CheckCircle2 v-else-if="region.is_available" :size="12" />
                                <AlertCircle v-else :size="12" />
                                {{ region.maintenance_mode ? t('dashboard.regionActions.maintenance') : region.is_available ? t('dashboard.regionActions.available') : t('dashboard.regionActions.offline') }}
                            </span>
                        </td>
                        <td class="desc-cell">{{ region.description || '-' }}</td>
                        <td>
                            <div class="table-actions">
                                <button class="icon-btn-table" @click="openEditModal(region)" :title="t('actions.edit')">
                                    <Settings2 :size="16" />
                                </button>
                                <button class="icon-btn-table" @click="confirmRotate(region)" :title="t('dashboard.regionActions.rotateSecret')">
                                    <KeyRound :size="16" />
                                </button>
                                <button class="icon-btn-table text-error" @click="confirmDelete(region)" :title="t('actions.delete')">
                                    <Trash2 :size="16" />
                                </button>
                            </div>
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>

        <!-- Create Modal -->
        <Teleport to="body">
            <div v-if="showCreateModal" class="modal-overlay" @click.self="!createdSecret && (showCreateModal = false)">
                <div class="modal-content card" style="max-width: 560px;">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.regionActions.createTitle') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="createdSecret ? closeAndReload() : (showCreateModal = false)"><X :size="18" /></button>
                    </div>

                    <!-- Show secret after creation -->
                    <div v-if="createdSecret" class="modal-body">
                        <div class="secret-display">
                            <div class="secret-warning">
                                <AlertCircle :size="18" />
                                <span>{{ t('dashboard.regionActions.secretWarning') }}</span>
                            </div>
                            <div class="secret-box">
                                <code>{{ createdSecret }}</code>
                                <button class="copy-btn" @click="copyToClipboard(createdSecret!, 'created-secret')">
                                    <Check v-if="copiedField === 'created-secret'" :size="14" class="copied-icon" />
                                    <Copy v-else :size="14" />
                                </button>
                            </div>
                        </div>
                    </div>

                    <!-- Create form -->
                    <div v-else class="modal-body">
                        <div class="form-stack">
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.regionActions.name') }} *</label>
                                <input type="text" v-model="createForm.name" class="form-input" :placeholder="t('dashboard.forms.placeholder.regionIdExample')" />
                                <span class="form-hint">{{ t('dashboard.regionActions.nameHint') }}</span>
                            </div>
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.regionActions.displayName') }}</label>
                                <input type="text" v-model="createForm.display_name" class="form-input" :placeholder="t('dashboard.forms.placeholder.regionNameExample')" />
                            </div>
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.regionActions.internalEndpoint') }} *</label>
                                <div style="display: flex; gap: 8px;">
                                    <input type="text" v-model="endpointHost" class="form-input" style="flex: 1;" :placeholder="t('dashboard.forms.placeholder.endpointExample')" />
                                    <div style="width: 100px;">
                                        <input type="text" v-model="endpointPort" class="form-input" placeholder="8255" />
                                    </div>
                                </div>
                                <span class="form-hint">{{ t('dashboard.regionActions.internalEndpointHint') }}</span>
                            </div>
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.regionActions.internalSecret') }}</label>
                                <input type="text" v-model="createForm.internal_secret" class="form-input" />
                                <span class="form-hint">{{ t('dashboard.regionActions.internalSecretHint') }}</span>
                            </div>
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.table.description') }}</label>
                                <input type="text" v-model="createForm.description" class="form-input" />
                            </div>
                        </div>
                    </div>

                    <div class="modal-footer">
                        <button v-if="createdSecret" class="btn btn-primary" @click="closeAndReload">{{ t('actions.close') }}</button>
                        <template v-else>
                            <button class="btn btn-secondary" @click="showCreateModal = false">{{ t('actions.cancel') }}</button>
                            <button class="btn btn-primary" @click="handleCreate" :disabled="creating || !createForm.name || !endpointHost">
                                <Loader2 v-if="creating" :size="14" class="spinning" />
                                {{ creating ? t('messages.creating') : t('actions.create') }}
                            </button>
                        </template>
                    </div>
                </div>
            </div>
        </Teleport>

        <!-- Edit Modal -->
        <Teleport to="body">
            <div v-if="showEditModal" class="modal-overlay" @click.self="showEditModal = false">
                <div class="modal-content card" style="max-width: 560px;">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.regionActions.editTitle') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showEditModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
                        <div class="form-stack">
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.regionActions.displayName') }}</label>
                                <input type="text" v-model="editForm.display_name" class="form-input" />
                            </div>
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.regionActions.internalEndpoint') }} *</label>
                                <div style="display: flex; gap: 8px;">
                                    <input type="text" v-model="endpointHost" class="form-input" style="flex: 1;" />
                                    <div style="width: 100px;">
                                        <input type="text" v-model="endpointPort" class="form-input" placeholder="8255" />
                                    </div>
                                </div>
                            </div>
                            <div class="form-group">
                                <label class="form-label">{{ t('dashboard.table.description') }}</label>
                                <input type="text" v-model="editForm.description" class="form-input" />
                            </div>
                            <div class="form-group">
                                <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer;">
                                    <input type="checkbox" v-model="editForm.maintenance_mode" />
                                    {{ t('dashboard.regionActions.maintenanceMode') }}
                                </label>
                                <span class="form-hint">{{ t('dashboard.regionActions.maintenanceModeHint') }}</span>
                            </div>
                        </div>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showEditModal = false">{{ t('actions.cancel') }}</button>
                        <button class="btn btn-primary" @click="handleEdit" :disabled="editing">
                            <Loader2 v-if="editing" :size="14" class="spinning" />
                            {{ editing ? t('messages.saving') : t('actions.save') }}
                        </button>
                    </div>
                </div>
            </div>
        </Teleport>

        <!-- Delete Confirm Modal -->
        <Teleport to="body">
            <div v-if="showDeleteModal" class="modal-overlay" @click.self="showDeleteModal = false">
                <div class="modal-content card" style="max-width: 440px;">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.regionActions.deleteTitle') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showDeleteModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
                        <p>{{ t('dashboard.regionActions.deleteConfirm', { name: deletingRegion?.display_name || deletingRegion?.name }) }}</p>
                        <div class="delete-warning-box">
                            <i class="fas fa-exclamation-triangle"></i>
                            <p>{{ t('dashboard.regionActions.deleteWarning') }}</p>
                        </div>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showDeleteModal = false">{{ t('actions.cancel') }}</button>
                        <button 
                            class="btn btn-danger" 
                            @click="handleDelete" 
                            :disabled="deleting || !deletingRegion?.maintenance_mode"
                        >
                            <Loader2 v-if="deleting" :size="14" class="spinning" />
                            {{ deleting ? t('messages.deleting') : t('actions.delete') }}
                        </button>
                    </div>
                </div>
            </div>
        </Teleport>

        <!-- Rotate Secret Modal -->
        <Teleport to="body">
            <div v-if="showRotateModal" class="modal-overlay" @click.self="!rotatedSecret && (showRotateModal = false)">
                <div class="modal-content card" style="max-width: 500px;">
                    <div class="modal-header">
                        <h3>{{ t('dashboard.regionActions.rotateSecretTitle') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showRotateModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
                        <div v-if="rotatedSecret" class="secret-display">
                            <div class="secret-warning">
                                <AlertCircle :size="18" />
                                <span>{{ t('dashboard.regionActions.secretWarning') }}</span>
                            </div>
                            <label class="form-label">{{ t('dashboard.regionActions.newSecret') }}</label>
                            <div class="secret-box">
                                <code>{{ rotatedSecret }}</code>
                                <button class="copy-btn" @click="copyToClipboard(rotatedSecret!, 'rotated-secret')">
                                    <Check v-if="copiedField === 'rotated-secret'" :size="14" class="copied-icon" />
                                    <Copy v-else :size="14" />
                                </button>
                            </div>
                        </div>
                        <p v-else>{{ t('dashboard.regionActions.rotateSecretConfirm', { name: rotatingRegion?.display_name || rotatingRegion?.name }) }}</p>
                    </div>
                    <div class="modal-footer">
                        <button v-if="rotatedSecret" class="btn btn-primary" @click="showRotateModal = false">{{ t('actions.close') }}</button>
                        <template v-else>
                            <button class="btn btn-secondary" @click="showRotateModal = false">{{ t('actions.cancel') }}</button>
                            <button class="btn btn-warning" @click="handleRotate" :disabled="rotating">
                                <Loader2 v-if="rotating" :size="14" class="spinning" />
                                {{ rotating ? '...' : t('dashboard.regionActions.rotateSecret') }}
                            </button>
                        </template>
                    </div>
                </div>
            </div>
        </Teleport>
    </div>
</template>

<style scoped>
/* .resource-info etc. are global from index.css */

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

.header-actions {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
}

.table-card {
    padding: 0;
    overflow: hidden;
}

.desc-cell {
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.875rem;
    color: var(--text-secondary);
}

.status-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border-radius: 20px;
    font-size: 0.75rem;
    font-weight: 600;
}

.status-available { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-offline { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
.status-maintenance { background: rgba(245, 158, 11, 0.1); color: #d97706; }

.table-actions {
    display: flex;
    gap: 8px;
}

.icon-btn-table {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    border: none;
    background: transparent;
    color: var(--text-tertiary);
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: all 0.2s;
}

.icon-btn-table:hover { background-color: var(--bg-tertiary); color: var(--primary-500); }
.icon-btn-table.text-error:hover { background-color: #fef2f2; color: var(--error-600); }

/* Modals */
.form-stack { display: flex; flex-direction: column; gap: 16px; }
.form-group { display: flex; flex-direction: column; }
.form-label { font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 4px; font-weight: 500; }

.form-input {
    width: 100%; padding: 8px 12px;
    border: 1px solid var(--border-light); border-radius: var(--radius-md);
    font-size: 0.875rem; background: var(--bg-primary); color: var(--text-primary);
}

.form-input:focus { outline: none; border-color: var(--primary-300); box-shadow: 0 0 0 2px var(--primary-100); }
.form-hint { color: var(--text-light); font-size: 0.75rem; margin-top: 4px; }

.btn-danger {
    background: #ef4444; color: white; border: none;
    padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}
.btn-danger:hover { background: #dc2626; }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.btn-warning {
    background: #f59e0b; color: white; border: none;
    padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}
.btn-warning:hover { background: #d97706; }
.btn-warning:disabled { opacity: 0.5; cursor: not-allowed; }

/* Secret display */
.secret-display { display: flex; flex-direction: column; gap: 12px; }

.secret-warning {
    display: flex; align-items: center; gap: 8px;
    padding: 12px 16px; background: #fef3c7; border: 1px solid #fde68a;
    border-radius: var(--radius-md); color: #92400e; font-size: 0.875rem; font-weight: 500;
}

.secret-box {
    display: flex; align-items: center; gap: 8px;
    padding: 12px; background: var(--bg-tertiary); border-radius: var(--radius-md);
    border: 1px solid var(--border-light);
}

.secret-box code {
    flex: 1; font-size: 0.8125rem; word-break: break-all;
    font-family: var(--font-mono); color: var(--text-primary);
}

.copy-btn {
    background: none; border: 1px solid var(--border-light);
    border-radius: var(--radius-sm); padding: 4px 8px; cursor: pointer;
    color: var(--text-light); display: inline-flex; align-items: center;
    transition: all 0.15s;
}
.copy-btn:hover { color: var(--primary-color); border-color: var(--primary-200); background: var(--primary-50); }
.copied-icon { color: var(--success-color); }

@keyframes spin { to { transform: rotate(360deg); } }
.spinning { animation: spin 1s linear infinite; }

.delete-warning-box {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-top: 1rem;
    padding: 12px 16px;
    background: rgba(239, 68, 68, 0.05);
    border: 1px solid rgba(239, 68, 68, 0.2);
    border-radius: var(--radius-md);
    color: #ef4444;
}

.delete-warning-box i {
    font-size: 1.125rem;
}

.delete-warning-box p {
    margin: 0;
    font-size: 0.875rem;
    line-height: 1.4;
    font-weight: 500;
}
</style>
