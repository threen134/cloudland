<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { keysApi, type SSHKey } from '../../api/keys'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import { formatToMinute } from '../../utils/format'

import { Key, Plus, Trash2, Copy, Check, Search, RefreshCw } from 'lucide-vue-next'
import PageToolbar from '../../components/base/PageToolbar.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const copiedFingerprintId = ref<string | null>(null)
const { copiedId, copyId } = useCopyId()
const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newKeyForm = ref({
    name: '',
    public_key: '',
})

const { t } = useI18n()
const toast = useToast()
const isNameValid = computed(() => isValidName(newKeyForm.value.name))

// const router = useRouter()

// 排序在服务端做（sortField 是数据库列名，和列 key 不一定同名）。
// 类型是前端从 public_key 里截出来的，数据库没有这一列，不提供排序
// Fingerprints never wrap (~380px), so below 1280px the created time is hidden to avoid overflow
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'type', label: t('dashboard.table.type') },
    { key: 'fingerprint', label: t('dashboard.table.fingerprint'), sortable: true, sortField: 'finger_print' },
    { key: 'created_at', label: t('dashboard.table.createdAt'), sortable: true, hideBelow: 1280 },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// 分页与搜索都在服务端做
const {
    items: keys,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchKeys,
    reload: reloadKeys,
} = useListQuery<SSHKey>(async ({ offset, limit, query, order }) => {
    const response = await keysApi.fetchKeys({ offset, limit, order, query: query || undefined })
    return { items: response.keys || [], total: response.total ?? 0 }
})

const copyFingerprint = async (key: SSHKey) => {
    if (key.finger_print) {
        await navigator.clipboard.writeText(key.finger_print)
        copiedFingerprintId.value = key.id
        setTimeout(() => {
            copiedFingerprintId.value = null
        }, 2000)
    }
}

const openCreateModal = () => {
    newKeyForm.value = { name: '', public_key: '' }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateKey = async () => {
    createError.value = ''
    if (!newKeyForm.value.name || !newKeyForm.value.public_key) {
        createError.value = t('messages.fillNameAndKey')
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    creating.value = true
    try {
        await keysApi.createKey(newKeyForm.value)
        // 列表按创建时间倒序，新建的在第一页
        await reloadKeys()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to create key:', err)
        createError.value = errorMessage(err, t('messages.error'))
    } finally {
        creating.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<SSHKey | null>(null)

const handleDeleteClick = (item: SSHKey) => {
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
        await keysApi.deleteKey(resourceToDelete.value.id)
        await fetchKeys()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error) {
        console.error('Failed to delete SSH key:', error)
        const data = (error as { response?: { data?: { error_code?: number; error_code_str?: string } } })?.response
            ?.data
        if (data?.error_code === 161006 || data?.error_code_str === 'SSHKeyInUse') {
            deleteError.value = t('messages.sshKeyInUse')
        } else {
            deleteError.value = errorMessage(error, t('messages.error'))
        }
    } finally {
        deletingResource.value = false
    }
}

onMounted(fetchKeys)
</script>

<template>
    <div>
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="() => fetchKeys()"
                    :title="$t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.createKey') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="keys"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="() => fetchKeys()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <Search :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else>
                    <Key :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noSSHKeys') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: key }">
                <div class="resource-link-static">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <Key :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ key.name }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="key.id">{{ key.id.slice(0, 8) }}...</span>
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(key.id)"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check v-if="copiedId === key.id" :size="10" style="color: var(--success-color)" />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </div>
            </template>

            <template #cell-type="{ row: key }">
                <span class="key-type">
                    {{ key.public_key?.split(' ')[0] || 'ssh-rsa' }}
                </span>
            </template>

            <template #cell-fingerprint="{ row: key }">
                <div class="fingerprint-cell">
                    <code class="fingerprint">{{ key.finger_print || '-' }}</code>
                    <button
                        class="btn btn-ghost btn-sm copy-btn"
                        @click="copyFingerprint(key as SSHKey)"
                        :title="copiedFingerprintId === key.id ? $t('messages.copied') : $t('actions.copy')"
                    >
                        <component :is="copiedFingerprintId === key.id ? Check : Copy" :size="14" />
                    </button>
                </div>
            </template>

            <template #cell-created_at="{ row: key }">
                <span class="cell-time" :title="key.created_at">{{ formatToMinute(key.created_at) }}</span>
            </template>

            <template #cell-actions="{ row: key }">
                <button
                    class="icon-btn-table icon-danger"
                    :title="$t('actions.delete')"
                    @click="handleDeleteClick(key as SSHKey)"
                >
                    <Trash2 :size="16" />
                </button>
            </template>

            <template #footer>
                <PaginationBar
                    :page="page"
                    :page-size="pageSize"
                    :total="total"
                    @update:page="page = $event"
                    @update:page-size="pageSize = $event"
                />
            </template>
        </DataTable>

        <!-- Create Key Modal -->
        <BaseModal
            :show="createModalVisible"
            :title="$t('dashboard.buttons.createKey')"
            :loading="creating"
            form
            @close="closeCreateModal"
            @submit="handleCreateKey"
        >
            <div class="form-group">
                <label class="form-label" for="name">{{ $t('dashboard.forms.name') }}</label>
                <input
                    id="name"
                    name="name"
                    v-model="newKeyForm.name"
                    type="text"
                    :class="['form-input', { 'input-error': !isNameValid }]"
                    :placeholder="$t('dashboard.forms.placeholder.sshKeyNameExample')"
                />
                <div v-if="!isNameValid" class="text-error text-xs mt-1">
                    {{ $t('messages.invalidHostname') }}
                </div>
            </div>

            <div class="form-group">
                <label class="form-label" for="public_key">{{ $t('dashboard.forms.publicKey') }}</label>
                <textarea
                    id="public_key"
                    name="public_key"
                    v-model="newKeyForm.public_key"
                    class="form-input"
                    rows="5"
                    :placeholder="$t('dashboard.forms.placeholder.sshPubKeyExample')"
                    style="font-family: monospace; font-size: 0.8em"
                ></textarea>
                <p class="helper-text">{{ $t('dashboard.forms.publicKeyHelp') }}</p>
            </div>

            <div v-if="createError" class="text-error modal-error">
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
                    {{ creating ? $t('messages.creating') : $t('dashboard.buttons.createKey') }}
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

.resource-link-static {
    display: block;
    padding: 4px 0;
}

.key-type {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    padding: var(--spacing-1) var(--spacing-2);
    background: var(--gray-50);
    border-radius: var(--radius-sm);
}

.fingerprint-cell {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.fingerprint {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.copy-btn {
    opacity: 0.5;
}

.copy-btn:hover {
    opacity: 1;
}

/* Modal Styles */
.helper-text {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: var(--spacing-2);
}

.modal-error {
    font-size: var(--font-size-sm);
    background: var(--error-light);
    padding: var(--spacing-2);
    border-radius: var(--radius-sm);
    margin-top: var(--spacing-2);
}

/* .resource-link removed */
</style>
