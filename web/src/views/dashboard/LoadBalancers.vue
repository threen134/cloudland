<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useRegionStore } from '../../stores/region'
import { loadBalancersApi, vpcsApi, type LoadBalancer, type VPC, type LoadBalancerPayload } from '../../api/networks'
import { isValidName } from '../../utils/validation'

import { GitFork, Plus, Trash2, Search, Pencil, RefreshCw, Check, Copy } from 'lucide-vue-next'
import { quotaErrorMessage } from '../../utils/quotaError'
import { errorMessage } from '../../utils/error'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const loadBalancers = ref<LoadBalancer[]>([])
const loading = ref(false)
const loadError = ref('')
const searchQuery = ref('')
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newLBForm = ref({
    name: '',
    description: '',
    vpc_id: '',
    zone: '',
})

const { t, te } = useI18n()
const toast = useToast()

// Unknown states fall back to the raw value rather than showing an i18n key
const statusText = (status?: string) =>
    status && te(`dashboard.loadBalancerStatus.${status}`) ? t(`dashboard.loadBalancerStatus.${status}`) : status || '-'

const { copiedId, copyId } = useCopyId()
const region = useRegionStore()
const isNameValid = computed(() => isValidName(newLBForm.value.name))

// --- Edit Modal ---
const editModalVisible = ref(false)
const editing = ref(false)
const editError = ref('')
const lbToEdit = ref<LoadBalancer | null>(null)
const editForm = ref({ name: '', description: '' })
const isEditValid = computed(() => isValidName(editForm.value.name))

const handleEditClick = (item: LoadBalancer) => {
    lbToEdit.value = item
    editForm.value = { name: item.name, description: item.description || '' }
    editError.value = ''
    editModalVisible.value = true
}
const closeEditModal = () => {
    editModalVisible.value = false
    lbToEdit.value = null
    editError.value = ''
}
const confirmEdit = async () => {
    if (!lbToEdit.value) return
    if (!isEditValid.value) {
        editError.value = t('messages.invalidHostname')
        return
    }
    editing.value = true
    editError.value = ''
    try {
        await loadBalancersApi.patch(lbToEdit.value.id, {
            name: editForm.value.name,
            description: editForm.value.description,
        })
        await fetchLoadBalancers()
        closeEditModal()
        toast.success(t('messages.success'))
    } catch (error) {
        editError.value = errorMessage(error, t('messages.error'))
    } finally {
        editing.value = false
    }
}

const vpcs = ref<VPC[]>([])

// Pagination and name search are done by the server
const currentPage = ref(1)
const pageSize = ref(20)
const totalCount = ref(0)
const totalPages = computed(() => Math.max(1, Math.ceil(totalCount.value / pageSize.value)))
// Drops responses of superseded requests (fast typing, page switches)
let fetchGeneration = 0

// 分页和搜索都在服务端做，前端排序只能排当前页，所以这些列不开放排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId') },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'ip', label: t('dashboard.table.ipAddress') },
    { key: 'vpc', label: t('dashboard.table.vpc') },
    { key: 'listeners', label: t('dashboard.loadBalancerDetail.listeners') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const fetchLoadBalancers = async () => {
    const generation = ++fetchGeneration
    loading.value = true
    loadError.value = ''
    try {
        const [lbResponse, vpcsResponse] = await Promise.all([
            loadBalancersApi.list({
                offset: (currentPage.value - 1) * pageSize.value,
                limit: pageSize.value,
                query: searchQuery.value.trim() || undefined,
            }),
            vpcsApi.list(),
        ])
        if (generation !== fetchGeneration) return
        loadBalancers.value = lbResponse.load_balancers || []
        totalCount.value = lbResponse.total || 0
        vpcs.value = vpcsResponse.vpcs || []
        // The current page became empty (e.g. its last item was deleted): step back
        if (loadBalancers.value.length === 0 && currentPage.value > 1) {
            currentPage.value = totalPages.value
            await fetchLoadBalancers()
        }
    } catch (err) {
        if (generation !== fetchGeneration) return
        console.error('API fetch failed:', err)
        loadBalancers.value = []
        totalCount.value = 0
        vpcs.value = []
        loadError.value = t('messages.error')
    } finally {
        if (generation === fetchGeneration) loading.value = false
    }
}

const goToPage = (page: number) => {
    if (page < 1 || page > totalPages.value || page === currentPage.value) return
    currentPage.value = page
    fetchLoadBalancers()
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
const onSearchInput = () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
        currentPage.value = 1
        fetchLoadBalancers()
    }, 400)
}

// 搜索框改用 PageToolbar 的 v-model:search，这里接回原来的防抖逻辑
watch(searchQuery, onSearchInput)

const openCreateModal = () => {
    newLBForm.value = { name: '', description: '', vpc_id: vpcs.value[0]?.id || '', zone: '' }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateLB = async () => {
    createError.value = ''
    if (!newLBForm.value.name || !newLBForm.value.vpc_id) return
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    creating.value = true
    try {
        const payload: LoadBalancerPayload = {
            name: newLBForm.value.name,
            vpc: { id: newLBForm.value.vpc_id },
        }
        if (newLBForm.value.description) payload.description = newLBForm.value.description
        if (newLBForm.value.zone) payload.zone = newLBForm.value.zone
        await loadBalancersApi.create(payload)

        // The list is sorted newest first: show the first page
        currentPage.value = 1
        await fetchLoadBalancers()

        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create load balancer:', err)
        createError.value = quotaErrorMessage(err, t, te) || errorMessage(err, t('messages.error'))
    } finally {
        creating.value = false
    }
}

const formatListeners = (lb: LoadBalancer) => {
    if (!lb.listeners || lb.listeners.length === 0) return '-'
    return lb.listeners.map((l) => `${l.port}/${l.mode.toUpperCase()}`).join(', ')
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<LoadBalancer | null>(null)

const handleDeleteClick = (item: LoadBalancer) => {
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
        await loadBalancersApi.delete(resourceToDelete.value.id)
        await fetchLoadBalancers()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error) {
        console.error('Failed to delete load balancer:', error)
        deleteError.value = errorMessage(error, t('messages.error'))
    } finally {
        deletingResource.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchLoadBalancers()
    }
})

// Re-fetch when region changes
watch(
    () => region.currentRegionId,
    (newId) => {
        if (newId) {
            fetchLoadBalancers()
        }
    }
)

// 搜索防抖定时器：组件卸载后不应再触发请求
onUnmounted(() => {
    if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="fetchLoadBalancers"
                    :title="$t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.createLoadBalancer') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="loadBalancers"
            row-key="id"
            :loading="loading"
            :error="loadError"
            @retry="fetchLoadBalancers"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <GitFork :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p class="text-secondary">{{ $t('messages.noLoadBalancers') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: lb }">
                <router-link :to="{ name: 'load-balancer-detail', params: { id: lb.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <GitFork :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ lb.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="lb.id">{{ lb.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(lb.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check v-if="copiedId === lb.id" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                            <div v-if="lb.description" class="resource-desc">{{ lb.description }}</div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-status="{ row: lb }">
                <StatusBadge :status="lb.status" :label="statusText(lb.status)" />
            </template>

            <template #cell-ip="{ row: lb }">
                <code class="ip-address">{{ lb.floating_ips?.[0]?.fip_address || '-' }}</code>
            </template>

            <template #cell-vpc="{ row: lb }">{{ lb.vpc?.name || '-' }}</template>

            <template #cell-listeners="{ row: lb }">
                <span class="text-secondary text-sm">{{ formatListeners(lb as LoadBalancer) }}</span>
            </template>

            <template #cell-actions="{ row: lb }">
                <div class="row-actions">
                    <button
                        class="icon-btn-table"
                        :title="$t('actions.edit')"
                        @click="handleEditClick(lb as LoadBalancer)"
                    >
                        <Pencil :size="16" />
                    </button>
                    <button
                        class="icon-btn-table icon-danger"
                        :title="$t('actions.delete')"
                        @click="handleDeleteClick(lb as LoadBalancer)"
                    >
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>

            <template #footer>
                <PaginationBar
                    :page="currentPage"
                    :page-size="pageSize"
                    :total="totalCount"
                    @update:page="goToPage"
                    @update:page-size="
                        (size) => {
                            pageSize = size
                            currentPage = 1
                            fetchLoadBalancers()
                        }
                    "
                />
            </template>
        </DataTable>
        <!-- Create LB Modal -->
        <BaseModal
            :show="createModalVisible"
            :title="$t('dashboard.buttons.createLoadBalancer')"
            :loading="creating"
            form
            @close="closeCreateModal"
            @submit="handleCreateLB"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
                <input
                    v-model="newLBForm.name"
                    type="text"
                    :class="['form-input', { 'input-error': !isNameValid }]"
                    :placeholder="$t('dashboard.forms.placeholder.lbNameExample')"
                />
                <div v-if="!isNameValid" class="text-error text-xs mt-1">
                    {{ $t('messages.invalidHostname') }}
                </div>
            </div>

            <div class="form-group">
                <label class="form-label"
                    >{{ $t('dashboard.forms.description') }}（{{ $t('dashboard.forms.optional') }}）</label
                >
                <input
                    v-model="newLBForm.description"
                    type="text"
                    class="form-input"
                    :placeholder="$t('messages.placeholderDescription')"
                />
            </div>

            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.vpc') }}</label>
                <div class="select-wrapper">
                    <select v-model="newLBForm.vpc_id" class="form-input">
                        <option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">
                            {{ vpc.name }} ({{ vpc.id.slice(0, 8) }}...)
                        </option>
                    </select>
                </div>
            </div>
            <div class="form-group">
                <label class="form-label"
                    >{{ $t('dashboard.forms.zone') }}（{{ $t('dashboard.forms.optional') }}）</label
                >
                <input
                    v-model="newLBForm.zone"
                    type="text"
                    class="form-input"
                    :placeholder="$t('dashboard.forms.placeholder.zoneExample')"
                />
            </div>

            <div v-if="createError" class="modal-error text-error">
                {{ createError }}
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="creating">
                    <span
                        v-if="creating"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createLoadBalancer') }}
                </button>
            </template>
        </BaseModal>

        <!-- Edit Modal -->
        <BaseModal
            :show="editModalVisible"
            :title="$t('actions.edit')"
            :loading="editing"
            form
            @close="closeEditModal"
            @submit="confirmEdit"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.name') }}</label>
                <input
                    v-model="editForm.name"
                    type="text"
                    :class="['form-input', { 'input-error': !isEditValid }]"
                    :placeholder="$t('dashboard.forms.placeholder.lbNameExample')"
                />
                <div v-if="!isEditValid" class="text-error text-xs mt-1">{{ $t('messages.invalidHostname') }}</div>
            </div>
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
                <input
                    v-model="editForm.description"
                    type="text"
                    class="form-input"
                    :placeholder="$t('messages.placeholderDescription')"
                />
            </div>

            <div v-if="editError" class="modal-error text-error">
                {{ editError }}
            </div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="closeEditModal" :disabled="editing">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="editing || !isEditValid">
                    <span
                        v-if="editing"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ editing ? $t('messages.saving') : $t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <!-- Delete Confirmation Modal -->
        <DeleteModal
            :show="deleteModalVisible"
            :resource-name="resourceToDelete?.name"
            :resource-id="resourceToDelete?.id"
            :loading="deletingResource"
            :error="deleteError"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
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

/* Modal Styles */

.modal-error {
    margin-top: var(--spacing-4);
    font-size: var(--font-size-sm);
    background: var(--error-light);
    padding: var(--spacing-2);
    border-radius: var(--radius-sm);
}

.resource-link {
    color: var(--primary-600);
    cursor: pointer;
}

.resource-link:hover {
    text-decoration: underline;
}

.resource-desc {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 2px;
}
</style>
