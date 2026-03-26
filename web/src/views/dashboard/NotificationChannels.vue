<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, Search, Bell, Pencil, ToggleLeft, ToggleRight, RefreshCw, X } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { notificationsApi, type NotificationChannel, type CreateChannelPayload } from '../../api/notifications'

const { t } = useI18n()
const toast = useToast()
const channels = ref<NotificationChannel[]>([])
const loading = ref(false)
const searchQuery = ref('')
const errorMsg = ref('')
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

const filteredChannels = computed(() => {
    if (!searchQuery.value) return channels.value
    const q = searchQuery.value.toLowerCase()
    return channels.value.filter(ch =>
        ch.name.toLowerCase().includes(q) || ch.type.toLowerCase().includes(q)
    )
})

const fetchChannels = async () => {
    loading.value = true
    try {
        const res = await notificationsApi.list()
        channels.value = res.data.channels || []
    } catch (err) {
        console.error('Failed to fetch channels:', err)
        errorMsg.value = t('messages.error')
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
    } catch (err: any) {
        console.error('Failed to save channel:', err)
        toast.error(err.response?.data?.error || t('messages.error'))
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
    } catch (err: any) {
        console.error('Failed to delete channel:', err)
        toast.error(err.response?.data?.error || t('messages.error'))
    }
}

const toggleEnabled = async (ch: NotificationChannel) => {
    try {
        await notificationsApi.update(ch.uuid, { enabled: !ch.enabled })
        await fetchChannels()
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to toggle channel:', err)
        toast.error(err.response?.data?.error || t('messages.error'))
    }
}

onMounted(fetchChannels)
</script>

<template>
    <div class="vpc-list-container">
        <div class="page-header">
            <div class="search-wrapper">
                <div class="search-box">
                    <Search :size="16" class="search-icon" />
                    <input v-model="searchQuery" :placeholder="t('actions.search') + '...'" class="search-input" />
                </div>
            </div>
            <div class="header-actions">
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchChannels" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreate">
                    <Plus :size="14" />
                    <span>{{ t('actions.create') }}</span>
                </button>
            </div>
        </div>

        <div v-if="errorMsg" class="error-banner" @click="errorMsg = ''">{{ errorMsg }}</div>

        <div class="card table-card">
            <table class="data-table">
                <thead>
                    <tr>
                        <th>{{ t('dashboard.table.name') }}</th>
                        <th>{{ t('dashboard.notificationChannelType') }}</th>
                        <th>Webhook URL</th>
                        <th>{{ t('dashboard.table.status') }}</th>
                        <th>{{ t('dashboard.table.createdAt') }}</th>
                        <th>{{ t('dashboard.table.actions') }}</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-if="loading">
                        <td colspan="6" class="text-center">
                            <div class="loading-spinner" style="margin: 20px auto;"></div>
                        </td>
                    </tr>
                    <tr v-else-if="filteredChannels.length === 0">
                        <td colspan="6" class="text-center text-secondary" style="padding: 48px;">
                            <div class="empty-state">
                                <Bell :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                                <p>{{ t('messages.noData') }}</p>
                            </div>
                        </td>
                    </tr>
                    <tr v-else v-for="ch in filteredChannels" :key="ch.uuid">
                        <td>{{ ch.name }}</td>
                        <td>
                            <span class="badge" :class="ch.type === 'feishu' ? 'badge-info' : 'badge-secondary'">
                                {{ ch.type === 'feishu' ? t('dashboard.notificationFeishu') : 'Webhook' }}
                            </span>
                        </td>
                        <td class="url-cell">{{ ch.config.webhook_url || ch.config.url || '-' }}</td>
                        <td>
                            <span class="status-pill" :class="ch.enabled ? 'status-active' : 'status-disabled'">
                                <span class="status-dot"></span>
                                {{ ch.enabled ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                            </span>
                        </td>
                        <td>{{ new Date(ch.created_at).toLocaleString() }}</td>
                        <td>
                            <div class="actions-cell">
                                <button class="icon-btn-table" @click="toggleEnabled(ch)" :title="ch.enabled ? 'Disable' : 'Enable'">
                                    <component :is="ch.enabled ? ToggleRight : ToggleLeft" :size="16" />
                                </button>
                                <button class="icon-btn-table" @click="openEdit(ch)">
                                    <Pencil :size="16" />
                                </button>
                                <button class="icon-btn-table text-error" @click="confirmDelete(ch)">
                                    <Trash2 :size="16" />
                                </button>
                            </div>
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>

        <!-- Create/Edit Modal -->
        <Teleport to="body">
            <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
                <div class="modal-content card" style="max-width: 520px;">
                    <div class="modal-header">
                        <h3>{{ editTarget ? t('actions.edit') : t('actions.create') }} {{ t('dashboard.notificationChannel') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showCreateModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
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
                                <input v-model="form.config[form.type === 'feishu' ? 'webhook_url' : 'url']" class="form-input" :placeholder="$t('dashboard.forms.placeholder.webhookExample')" required />
                            </div>
                            <div class="form-group" v-if="form.type === 'feishu'">
                                <label class="form-label">{{ t('dashboard.notificationSecret') }}</label>
                                <input v-model="form.config.secret" class="form-input" :placeholder="t('dashboard.notificationSecretPlaceholder')" />
                            </div>
                        </div>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showCreateModal = false">{{ t('actions.cancel') }}</button>
                        <button class="btn btn-primary" @click="submitForm">{{ t('actions.save') }}</button>
                    </div>
                </div>
            </div>
        </Teleport>

        <!-- Delete Confirm Modal -->
        <Teleport to="body">
            <div v-if="showDeleteModal" class="modal-overlay" @click.self="showDeleteModal = false">
                <div class="modal-content card" style="max-width: 440px;">
                    <div class="modal-header">
                        <h3>{{ t('actions.confirmDelete') }}</h3>
                        <button class="btn btn-ghost btn-icon" @click="showDeleteModal = false"><X :size="18" /></button>
                    </div>
                    <div class="modal-body">
                        <p>{{ t('messages.confirmDeleteChannel', { name: deleteTarget?.name }) }}</p>
                    </div>
                    <div class="modal-footer">
                        <button class="btn btn-secondary" @click="showDeleteModal = false">{{ t('actions.cancel') }}</button>
                        <button class="btn btn-danger" @click="executeDelete">{{ t('actions.delete') }}</button>
                    </div>
                </div>
            </div>
        </Teleport>
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
    display: flex;
    gap: 8px;
    flex: 1;
    max-width: 560px;
}

.header-actions {
    display: flex;
    gap: 8px;
    align-items: center;
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
    flex: 1;
}

.search-box:focus-within {
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.search-icon { color: var(--gray-400); }

.search-input {
    border: none;
    background: transparent;
    width: 100%;
    height: 100%;
    font-size: 0.875rem;
    color: var(--text-primary);
}

.search-input:focus { outline: none; }

.table-card { padding: 0; overflow: hidden; }

.empty-state { display: flex; flex-direction: column; align-items: center; justify-content: center; }

.url-cell {
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.8125rem;
    color: var(--text-secondary);
}

.actions-cell { display: flex; gap: 4px; align-items: center; }

.icon-btn-table {
    width: 32px; height: 32px; border-radius: 8px; border: none;
    background: transparent; color: var(--text-tertiary);
    display: flex; align-items: center; justify-content: center;
    cursor: pointer; transition: all 0.2s;
}

.icon-btn-table:hover { background-color: var(--bg-tertiary); color: var(--primary-500); }
.icon-btn-table.text-error:hover { background-color: #fef2f2; color: #ef4444; }
.text-error { color: var(--text-tertiary); }

.status-pill {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 4px 10px; border-radius: var(--radius-full);
    font-size: var(--font-size-xs); font-weight: var(--font-weight-medium);
}

.status-active { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-disabled { background: var(--gray-100); color: var(--gray-500); }
.status-dot { width: 6px; height: 6px; background: currentColor; border-radius: 50%; }

.badge-info { background: #3b82f6; color: white; }
.badge-success { background: #22c55e; color: white; }
.badge-muted { background: #6b7280; color: white; }
.badge-secondary { background: #8b5cf6; color: white; }
.text-danger { color: #ef4444; }
.text-center { text-align: center; }

.error-banner {
    background: #fef2f2; color: #dc2626; border: 1px solid #fecaca;
    border-radius: 6px; padding: 10px 14px; margin-bottom: 12px;
    font-size: 13px; cursor: pointer;
}

/* Modal */
.form-stack { display: flex; flex-direction: column; gap: 16px; }
.form-group { display: flex; flex-direction: column; }
.form-label { font-size: 0.8125rem; color: var(--text-secondary); margin-bottom: 4px; font-weight: 500; }

.form-input {
    width: 100%; padding: 8px 12px;
    border: 1px solid var(--border-light); border-radius: var(--radius-md);
    font-size: 0.875rem; background: var(--bg-primary); color: var(--text-primary);
}

.form-input:focus { outline: none; border-color: var(--primary-300); box-shadow: 0 0 0 2px var(--primary-100); }

.btn-danger {
    background: #ef4444; color: white; border: none;
    padding: 8px 16px; border-radius: var(--radius-md); cursor: pointer; font-weight: 500;
}
.btn-danger:hover { background: #dc2626; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
