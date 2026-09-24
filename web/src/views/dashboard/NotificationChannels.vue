<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, MessageSquare, Pencil, Power, RefreshCw } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { notificationsApi, type NotificationChannel, type CreateChannelPayload } from '../../api/notifications'
import { useAuthStore } from '../../stores/auth'
import { useTenantStore } from '../../stores/tenant'
import { formatDateTime } from '../../utils/format'
import { errorMessage } from '../../utils/error'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const { t } = useI18n()
const toast = useToast()
const authStore = useAuthStore()
const tenantStore = useTenantStore()
// Writes require org ADMIN or SystemAdmin (enforced by the gateway)
const canManage = computed(() => authStore.user?.is_superuser === true || (tenantStore.currentOrg?.org_role ?? 0) >= 3)
const channels = ref<NotificationChannel[]>([])
const loading = ref(false)
const searchQuery = ref('')
const loadError = ref('')
const showCreateModal = ref(false)
const showDeleteModal = ref(false)
const deleteTarget = ref<NotificationChannel | null>(null)
const editTarget = ref<NotificationChannel | null>(null)

// Create form
const form = ref<CreateChannelPayload>({
    name: '',
    type: 'feishu',
    config: { webhook_url: '', secret: '' },
    enabled: true,
})

// 搜索走服务端（cpgateway 的 GET /notification-channels 收 query）；渠道数量很少，不做分页

const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.name'), sortable: true },
    { key: 'type', label: t('dashboard.notificationChannelType'), sortable: true, sortValue: (ch) => ch.type },
    { key: 'url', label: 'Webhook URL' },
    { key: 'status', label: t('dashboard.table.status'), sortable: true, sortValue: (ch) => (ch.enabled ? 0 : 1) },
    { key: 'created_at', label: t('dashboard.table.createdAt'), sortable: true, sortValue: (ch) => ch.created_at },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

const fetchChannels = async () => {
    loading.value = true
    loadError.value = ''
    try {
        const res = await notificationsApi.list({ limit: 500, query: searchQuery.value.trim() || undefined })
        channels.value = res.channels || []
    } catch (err) {
        console.error('Failed to fetch channels:', err)
        loadError.value = t('messages.error')
    } finally {
        loading.value = false
    }
}

const openCreate = () => {
    editTarget.value = null
    form.value = { name: '', type: 'feishu', config: { webhook_url: '', secret: '' }, enabled: true }
    showCreateModal.value = true
}

const openEdit = (ch: NotificationChannel) => {
    editTarget.value = ch
    form.value = {
        name: ch.name,
        type: ch.type,
        config: JSON.parse(JSON.stringify(ch.config)),
        enabled: ch.enabled,
    }
    showCreateModal.value = true
}

const onTypeChange = () => {
    if (form.value.type === 'feishu') {
        form.value.config = { webhook_url: form.value.config.webhook_url || '', secret: '' }
    } else {
        form.value.config = { url: form.value.config.url || form.value.config.webhook_url || '', headers: {} }
    }
}

const submitForm = async () => {
    try {
        if (editTarget.value) {
            await notificationsApi.update(editTarget.value.uuid, {
                name: form.value.name,
                config: form.value.config,
                enabled: form.value.enabled,
            })
        } else {
            await notificationsApi.create(form.value)
        }
        showCreateModal.value = false
        await fetchChannels()
        toast.success(editTarget.value ? t('messages.updateSuccess') : t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to save channel:', err)
        toast.error(errorMessage(err, t('messages.error')))
    }
}

const confirmDelete = (ch: NotificationChannel) => {
    deleteTarget.value = ch
    showDeleteModal.value = true
}

const executeDelete = async () => {
    if (!deleteTarget.value) return
    try {
        await notificationsApi.delete(deleteTarget.value.uuid)
        showDeleteModal.value = false
        deleteTarget.value = null
        await fetchChannels()
        toast.success(t('messages.deleteSuccess'))
    } catch (err) {
        console.error('Failed to delete channel:', err)
        toast.error(errorMessage(err, t('messages.error')))
    }
}

const toggleEnabled = async (ch: NotificationChannel) => {
    try {
        await notificationsApi.update(ch.uuid, { enabled: !ch.enabled })
        await fetchChannels()
        toast.success(t('messages.updateSuccess'))
    } catch (err) {
        console.error('Failed to toggle channel:', err)
        toast.error(errorMessage(err, t('messages.error')))
    }
}

// 搜索防抖 400ms 后重新向服务端取
let searchTimer: ReturnType<typeof setTimeout> | null = null
watch(searchQuery, () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(fetchChannels, 400)
})
onUnmounted(() => {
    if (searchTimer) clearTimeout(searchTimer)
})

onMounted(fetchChannels)
</script>

<template>
    <div class="vpc-list-container">
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchChannels" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button v-if="canManage" class="btn btn-primary btn-sm" @click="openCreate">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="channels"
            row-key="uuid"
            :loading="loading"
            :error="loadError"
            @retry="fetchChannels"
        >
            <template #empty>
                <div class="empty-state">
                    <MessageSquare :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p>{{ t('messages.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: ch }">{{ ch.name }}</template>

            <template #cell-type="{ row: ch }">
                <span class="badge badge-secondary">
                    {{ ch.type === 'feishu' ? t('dashboard.notificationFeishu') : 'Webhook' }}
                </span>
            </template>

            <template #cell-url="{ row: ch }">
                <div class="url-cell">{{ ch.config.webhook_url || ch.config.url || '-' }}</div>
            </template>

            <template #cell-status="{ row: ch }">
                <span class="status-pill" :class="ch.enabled ? 'status-active' : 'status-disabled'">
                    <span class="status-dot"></span>
                    {{ ch.enabled ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                </span>
            </template>

            <template #cell-created_at="{ row: ch }">{{ formatDateTime(ch.created_at) }}</template>

            <template #cell-actions="{ row: ch }">
                <div v-if="canManage" class="row-actions">
                    <button
                        class="icon-btn-table"
                        :class="{ 'is-active': ch.enabled }"
                        @click="toggleEnabled(ch)"
                        :title="ch.enabled ? t('actions.disable') : t('actions.enable')"
                    >
                        <Power :size="16" />
                    </button>
                    <button class="icon-btn-table" @click="openEdit(ch)" :title="t('actions.edit')">
                        <Pencil :size="16" />
                    </button>
                    <button class="icon-btn-table icon-danger" @click="confirmDelete(ch)" :title="t('actions.delete')">
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>
        </DataTable>

        <!-- Create/Edit Modal -->
        <Teleport to="body">
            <BaseModal
                :show="showCreateModal"
                :title="`${editTarget ? t('actions.edit') : t('actions.create')} ${t('dashboard.notificationChannel')}`"
                size="lg"
                form
                @close="showCreateModal = false"
                @submit="submitForm"
            >
                <div class="form-stack">
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.table.name') }}</label>
                        <input v-model="form.name" class="form-input" required />
                    </div>
                    <div class="form-group" v-if="!editTarget">
                        <label class="form-label">{{ t('dashboard.notificationChannelType') }}</label>
                        <select v-model="form.type" class="form-input" @change="onTypeChange">
                            <option value="feishu">{{ t('dashboard.notificationFeishu') }} Webhook</option>
                            <option value="webhook">{{ t('dashboard.notificationCustomWebhook') }}</option>
                        </select>
                    </div>
                    <div class="form-group">
                        <label class="form-label">Webhook URL</label>
                        <input
                            v-model="form.config[form.type === 'feishu' ? 'webhook_url' : 'url']"
                            class="form-input"
                            :placeholder="$t('dashboard.forms.placeholder.webhookExample')"
                            required
                        />
                    </div>
                    <div class="form-group" v-if="form.type === 'feishu'">
                        <label class="form-label">{{ t('dashboard.notificationSecret') }}</label>
                        <input
                            v-model="form.config.secret"
                            class="form-input"
                            :placeholder="t('dashboard.notificationSecretPlaceholder')"
                        />
                    </div>
                </div>

                <template #footer>
                    <button type="button" class="btn btn-secondary" @click="showCreateModal = false">
                        {{ t('actions.cancel') }}
                    </button>
                    <button type="submit" class="btn btn-primary">{{ t('actions.save') }}</button>
                </template>
            </BaseModal>
        </Teleport>

        <!-- Delete Confirm Modal -->
        <Teleport to="body">
            <DeleteModal
                :show="showDeleteModal"
                :title="t('actions.confirmDelete')"
                :message="t('messages.confirmDeleteChannel', { name: deleteTarget?.name })"
                @close="showDeleteModal = false"
                @confirm="executeDelete"
            />
        </Teleport>
    </div>
</template>

<style scoped>
.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
}

.url-cell {
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.8125rem;
    color: var(--text-secondary);
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
    background: var(--success-light);
    color: var(--success-dark);
}
.status-disabled {
    background: var(--bg-tertiary);
    color: var(--text-tertiary);
}
.status-dot {
    width: 6px;
    height: 6px;
    background: currentColor;
    border-radius: 50%;
}

/* Modal */
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
</style>
