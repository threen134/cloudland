<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
    Globe2,
    Plus,
    Search,
    RefreshCw,
    Settings2,
    Trash2,
    CheckCircle2,
    AlertCircle,
    KeyRound,
    Copy,
    Check,
    Loader2,
    Wrench,
} from 'lucide-vue-next'
import {
    regionsApi,
    type RegionPublic,
    type RegionAdmin,
    type RegionCreated,
    type CreateRegionPayload,
    type UpdateRegionPayload,
} from '../../api/regions'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import BaseModal from '../../components/modals/BaseModal.vue'

const { t } = useI18n()
const toast = useToast()
const router = useRouter()

const regions = ref<RegionPublic[]>([])
const isLoading = ref(false)
const loadError = ref('')
const searchQuery = ref('')

// 状态列显示的是翻译后的文案，排序按原始状态串
// 参数写成可选字段的结构类型，才能直接交给 DataTable 的 sortValue（它拿到的是通用行对象）
const regionState = (r: { maintenance_mode?: boolean; is_available?: boolean }) =>
    r.maintenance_mode ? 'maintenance' : r.is_available ? 'available' : 'offline'

const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true, sortValue: (r) => r.display_name || r.name },
    { key: 'status', label: t('dashboard.table.status'), sortable: true, sortValue: regionState },
    { key: 'description', label: t('dashboard.table.description'), sortable: true },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// Create modal
const showCreateModal = ref(false)
const creating = ref(false)
const createForm = ref<CreateRegionPayload>({
    name: '',
    display_name: '',
    internal_endpoint: '',
    internal_secret: 'auto-generate',
    description: '',
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
const { copiedId, copyId } = useCopyId()

// 搜索走服务端（cpgateway 的 GET /regions 收 query）；区域是个位数量级，不做分页

const fetchRegions = async () => {
    isLoading.value = true
    loadError.value = ''
    try {
        // 区域数量很少，这里一次取满上限即可（列表页不再做前端过滤，搜索交给服务端）
        const data = await regionsApi.fetchRegions({ limit: 500, query: searchQuery.value.trim() || undefined })
        regions.value = data.regions || []
    } catch (err) {
        console.error('Failed to fetch regions:', err)
        regions.value = []
        loadError.value = t('messages.error')
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
                port: url.port || '8255',
            }
        }
    } catch {
        // endpoint 不是合法 URL：按原样当作主机名，端口取默认值
    }
    return { host: endpoint, port: '8255' }
}

// Create
const openCreateModal = () => {
    createForm.value = {
        name: '',
        display_name: '',
        internal_endpoint: '',
        internal_secret: 'auto-generate',
        description: '',
    }
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
        const created = (await regionsApi.createRegion(createForm.value)) as RegionCreated
        createdSecret.value = created.internal_secret
        toast.success(t('dashboard.regionActions.createdSuccess'))
        await fetchRegions()
        if (!createdSecret.value) {
            router.go(0)
        }
    } catch (err) {
        toast.error(errorMessage(err, 'Create failed'))
    } finally {
        creating.value = false
    }
}

// Edit
const openEditModal = async (region: RegionPublic) => {
    try {
        const detail = (await regionsApi.getRegion(region.uuid)) as RegionAdmin
        editingRegion.value = detail

        const { host, port } = splitEndpoint(detail.internal_endpoint)
        endpointHost.value = host
        endpointPort.value = port

        editForm.value = {
            display_name: detail.display_name || '',
            internal_endpoint: detail.internal_endpoint,
            maintenance_mode: detail.maintenance_mode,
            description: detail.description || '',
        }
        showEditModal.value = true
    } catch (err) {
        toast.error(errorMessage(err, 'Failed to load region details'))
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
    } catch (err) {
        toast.error(errorMessage(err, 'Update failed'))
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
    } catch (err) {
        toast.error(errorMessage(err, 'Delete failed'))
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
        rotatedSecret.value = response.new_secret
        toast.success(t('messages.success'))
    } catch (err) {
        toast.error(errorMessage(err, 'Rotate failed'))
    } finally {
        rotating.value = false
    }
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        toast.success(t('dashboard.regionActions.secretCopied'))
        setTimeout(() => {
            copiedField.value = null
        }, 2000)
    })
}

// 搜索防抖 400ms 后重新向服务端取
let searchTimer: ReturnType<typeof setTimeout> | null = null
watch(searchQuery, () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(fetchRegions, 400)
})
onUnmounted(() => {
    if (searchTimer) clearTimeout(searchTimer)
})

onMounted(fetchRegions)
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchRegions" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: isLoading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="regions"
            row-key="uuid"
            :loading="isLoading"
            :error="loadError"
            @retry="fetchRegions"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <Globe2 :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ t('dashboard.regionActions.noRegions') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: region }">
                <div class="resource-info">
                    <div class="resource-icon">
                        <Globe2 :size="16" />
                    </div>
                    <div>
                        <div class="resource-name">{{ region.display_name || region.name }}</div>
                        <div class="resource-id-row">
                            <span class="resource-id" :title="region.name + ' / ' + region.uuid">
                                {{ region.name }} / {{ region.uuid.slice(0, 8) }}...
                            </span>
                            <button
                                class="copy-btn-mini"
                                @click.stop.prevent="copyId(region.uuid)"
                                :title="t('actions.copy')"
                                :aria-label="t('actions.copy')"
                            >
                                <Check v-if="copiedId === region.uuid" :size="10" style="color: var(--success-color)" />
                                <Copy v-else :size="10" />
                            </button>
                        </div>
                    </div>
                </div>
            </template>

            <template #cell-status="{ row: region }">
                <span
                    class="status-pill"
                    :class="
                        region.maintenance_mode
                            ? 'status-maintenance'
                            : region.is_available
                              ? 'status-available'
                              : 'status-offline'
                    "
                >
                    <Wrench v-if="region.maintenance_mode" :size="12" />
                    <CheckCircle2 v-else-if="region.is_available" :size="12" />
                    <AlertCircle v-else :size="12" />
                    {{
                        region.maintenance_mode
                            ? t('dashboard.regionActions.maintenance')
                            : region.is_available
                              ? t('dashboard.regionActions.available')
                              : t('dashboard.regionActions.offline')
                    }}
                </span>
            </template>

            <template #cell-description="{ row: region }">
                <div class="desc-cell">{{ region.description || '-' }}</div>
            </template>

            <template #cell-actions="{ row: region }">
                <div class="table-actions">
                    <button class="icon-btn-table" @click="openEditModal(region)" :title="t('actions.edit')">
                        <Settings2 :size="16" />
                    </button>
                    <button
                        class="icon-btn-table"
                        @click="confirmRotate(region)"
                        :title="t('dashboard.regionActions.rotateSecret')"
                    >
                        <KeyRound :size="16" />
                    </button>
                    <button
                        class="icon-btn-table text-error"
                        @click="confirmDelete(region)"
                        :title="t('actions.delete')"
                    >
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>
        </DataTable>

        <!-- Create Modal -->
        <Teleport to="body">
            <BaseModal
                :show="showCreateModal"
                :title="t('dashboard.regionActions.createTitle')"
                size="lg"
                @close="createdSecret ? closeAndReload() : (showCreateModal = false)"
            >
                <!-- Show secret after creation -->
                <div v-if="createdSecret" class="secret-display">
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

                <!-- Create form -->
                <div v-else class="form-stack">
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.regionActions.name') }} *</label>
                        <input
                            type="text"
                            v-model="createForm.name"
                            class="form-input"
                            :placeholder="t('dashboard.forms.placeholder.regionIdExample')"
                        />
                        <span class="form-hint">{{ t('dashboard.regionActions.nameHint') }}</span>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.regionActions.displayName') }}</label>
                        <input
                            type="text"
                            v-model="createForm.display_name"
                            class="form-input"
                            :placeholder="t('dashboard.forms.placeholder.regionNameExample')"
                        />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.regionActions.internalEndpoint') }} *</label>
                        <div style="display: flex; gap: 8px">
                            <input
                                type="text"
                                v-model="endpointHost"
                                class="form-input"
                                style="flex: 1"
                                :placeholder="t('dashboard.forms.placeholder.endpointExample')"
                            />
                            <div style="width: 100px">
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

                <template #footer>
                    <button v-if="createdSecret" class="btn btn-primary" @click="closeAndReload">
                        {{ t('actions.close') }}
                    </button>
                    <template v-else>
                        <button class="btn btn-secondary" @click="showCreateModal = false">
                            {{ t('actions.cancel') }}
                        </button>
                        <button
                            class="btn btn-primary"
                            @click="handleCreate"
                            :disabled="creating || !createForm.name || !endpointHost"
                        >
                            <Loader2 v-if="creating" :size="14" class="spinning" />
                            {{ creating ? t('messages.creating') : t('actions.create') }}
                        </button>
                    </template>
                </template>
            </BaseModal>
        </Teleport>

        <!-- Edit Modal -->
        <Teleport to="body">
            <BaseModal
                :show="showEditModal"
                :title="t('dashboard.regionActions.editTitle')"
                size="lg"
                :loading="editing"
                form
                @close="showEditModal = false"
                @submit="handleEdit"
            >
                <div class="form-stack">
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.regionActions.displayName') }}</label>
                        <input type="text" v-model="editForm.display_name" class="form-input" />
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.regionActions.internalEndpoint') }} *</label>
                        <div style="display: flex; gap: 8px">
                            <input type="text" v-model="endpointHost" class="form-input" style="flex: 1" />
                            <div style="width: 100px">
                                <input type="text" v-model="endpointPort" class="form-input" placeholder="8255" />
                            </div>
                        </div>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.table.description') }}</label>
                        <input type="text" v-model="editForm.description" class="form-input" />
                    </div>
                    <div class="form-group">
                        <label class="form-label" style="display: flex; align-items: center; gap: 8px; cursor: pointer">
                            <input type="checkbox" v-model="editForm.maintenance_mode" />
                            {{ t('dashboard.regionActions.maintenanceMode') }}
                        </label>
                        <span class="form-hint">{{ t('dashboard.regionActions.maintenanceModeHint') }}</span>
                    </div>
                </div>

                <template #footer>
                    <button type="button" class="btn btn-secondary" @click="showEditModal = false">
                        {{ t('actions.cancel') }}
                    </button>
                    <button type="submit" class="btn btn-primary" :disabled="editing">
                        <Loader2 v-if="editing" :size="14" class="spinning" />
                        {{ editing ? t('messages.saving') : t('actions.save') }}
                    </button>
                </template>
            </BaseModal>
        </Teleport>

        <!-- Delete Confirm Modal -->
        <Teleport to="body">
            <BaseModal
                :show="showDeleteModal"
                :title="t('dashboard.regionActions.deleteTitle')"
                :loading="deleting"
                @close="showDeleteModal = false"
            >
                <p>
                    {{
                        t('dashboard.regionActions.deleteConfirm', {
                            name: deletingRegion?.display_name || deletingRegion?.name,
                        })
                    }}
                </p>
                <div class="delete-warning-box">
                    <i class="fas fa-exclamation-triangle"></i>
                    <p>{{ t('dashboard.regionActions.deleteWarning') }}</p>
                </div>

                <template #footer>
                    <button class="btn btn-secondary" @click="showDeleteModal = false">
                        {{ t('actions.cancel') }}
                    </button>
                    <button
                        class="btn btn-danger"
                        @click="handleDelete"
                        :disabled="deleting || !deletingRegion?.maintenance_mode"
                    >
                        <Loader2 v-if="deleting" :size="14" class="spinning" />
                        {{ deleting ? t('messages.deleting') : t('actions.delete') }}
                    </button>
                </template>
            </BaseModal>
        </Teleport>

        <!-- Rotate Secret Modal -->
        <Teleport to="body">
            <BaseModal
                :show="showRotateModal"
                :title="t('dashboard.regionActions.rotateSecretTitle')"
                :loading="rotating"
                @close="showRotateModal = false"
            >
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
                <p v-else>
                    {{
                        t('dashboard.regionActions.rotateSecretConfirm', {
                            name: rotatingRegion?.display_name || rotatingRegion?.name,
                        })
                    }}
                </p>

                <template #footer>
                    <button v-if="rotatedSecret" class="btn btn-primary" @click="showRotateModal = false">
                        {{ t('actions.close') }}
                    </button>
                    <template v-else>
                        <button class="btn btn-secondary" @click="showRotateModal = false">
                            {{ t('actions.cancel') }}
                        </button>
                        <button class="btn btn-warning" @click="handleRotate" :disabled="rotating">
                            <Loader2 v-if="rotating" :size="14" class="spinning" />
                            {{ rotating ? '...' : t('dashboard.regionActions.rotateSecret') }}
                        </button>
                    </template>
                </template>
            </BaseModal>
        </Teleport>
    </div>
</template>

<style scoped>
/* .resource-info etc. are global from index.css */

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

.status-available {
    background: rgba(16, 185, 129, 0.1);
    color: var(--success-color);
}
.status-offline {
    background: rgba(239, 68, 68, 0.1);
    color: #ef4444;
}
.status-maintenance {
    background: rgba(245, 158, 11, 0.1);
    color: #d97706;
}

.table-actions {
    display: flex;
    justify-content: center;
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

.icon-btn-table:hover {
    background-color: var(--bg-tertiary);
    color: var(--primary-500);
}
.icon-btn-table.text-error:hover {
    background-color: #fef2f2;
    color: var(--error-dark);
}

/* Modals */
.form-stack {
    display: flex;
    flex-direction: column;
    gap: 16px;
}
.form-group {
    display: flex;
    flex-direction: column;
}
.form-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: 4px;
    font-weight: 500;
}

.form-input {
    width: 100%;
    padding: 8px 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    background: var(--bg-primary);
    color: var(--text-primary);
}

.form-input:focus {
    outline: none;
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}
.form-hint {
    color: var(--text-light);
    font-size: 0.75rem;
    margin-top: 4px;
}

.btn-danger {
    background: #ef4444;
    color: white;
    border: none;
    padding: 8px 16px;
    border-radius: var(--radius-md);
    cursor: pointer;
    font-weight: 500;
}
.btn-danger:hover {
    background: #dc2626;
}
.btn-danger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.btn-warning {
    background: #f59e0b;
    color: white;
    border: none;
    padding: 8px 16px;
    border-radius: var(--radius-md);
    cursor: pointer;
    font-weight: 500;
}
.btn-warning:hover {
    background: #d97706;
}
.btn-warning:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

/* Secret display */
.secret-display {
    display: flex;
    flex-direction: column;
    gap: 12px;
}

.secret-warning {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px 16px;
    background: var(--warning-light);
    border: 1px solid #fde68a;
    border-radius: var(--radius-md);
    color: #92400e;
    font-size: 0.875rem;
    font-weight: 500;
}

.secret-box {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px;
    background: var(--bg-tertiary);
    border-radius: var(--radius-md);
    border: 1px solid var(--border-light);
}

.secret-box code {
    flex: 1;
    font-size: 0.8125rem;
    word-break: break-all;
    font-family: var(--font-family-mono);
    color: var(--text-primary);
}

.copy-btn {
    background: none;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    padding: 4px 8px;
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

@keyframes spin {
    to {
        transform: rotate(360deg);
    }
}
.spinning {
    animation: spin 1s linear infinite;
}

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
