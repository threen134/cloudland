<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, Search, Bell, Pencil, ToggleLeft, ToggleRight } from 'lucide-vue-next'
import { notificationsApi, type NotificationChannel, type CreateChannelPayload } from '../../api/notifications'

const { t } = useI18n()
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
    } catch (err: any) {
        console.error('Failed to save channel:', err)
        errorMsg.value = err.response?.data?.error || t('messages.error')
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
    } catch (err: any) {
        console.error('Failed to delete channel:', err)
        errorMsg.value = err.response?.data?.error || t('messages.error')
    }
}

const toggleEnabled = async (ch: NotificationChannel) => {
    try {
        await notificationsApi.update(ch.uuid, { enabled: !ch.enabled })
        await fetchChannels()
    } catch (err: any) {
        console.error('Failed to toggle channel:', err)
        errorMsg.value = err.response?.data?.error || t('messages.error')
    }
}

onMounted(fetchChannels)
</script>

<template>
    <div class="page-container">
        <div class="page-header">
            <h2><Bell :size="22" /> {{ t('dashboard.notificationChannels') }}</h2>
            <div class="header-actions">
                <div class="search-box">
                    <Search :size="16" />
                    <input v-model="searchQuery" :placeholder="t('actions.search')" class="form-input" />
                </div>
                <button class="btn btn-primary btn-sm" @click="openCreate">
                    <Plus :size="16" /> {{ t('actions.create') }}
                </button>
            </div>
        </div>

        <div v-if="errorMsg" class="error-banner" @click="errorMsg = ''">{{ errorMsg }}</div>

        <div v-if="loading" class="loading-spinner">Loading...</div>

        <table v-else class="data-table">
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
                <tr v-for="ch in filteredChannels" :key="ch.uuid">
                    <td>{{ ch.name }}</td>
                    <td>
                        <span class="badge" :class="ch.type === 'feishu' ? 'badge-info' : 'badge-secondary'">
                            {{ ch.type === 'feishu' ? t('dashboard.notificationFeishu') : 'Webhook' }}
                        </span>
                    </td>
                    <td class="url-cell">{{ ch.config.webhook_url || ch.config.url || '-' }}</td>
                    <td>
                        <span class="badge" :class="ch.enabled ? 'badge-success' : 'badge-muted'">
                            {{ ch.enabled ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled') }}
                        </span>
                    </td>
                    <td>{{ new Date(ch.created_at).toLocaleString() }}</td>
                    <td class="actions-cell">
                        <button class="btn btn-ghost btn-sm" @click="toggleEnabled(ch)" :title="ch.enabled ? 'Disable' : 'Enable'">
                            <component :is="ch.enabled ? ToggleRight : ToggleLeft" :size="16" />
                        </button>
                        <button class="btn btn-ghost btn-sm" @click="openEdit(ch)">
                            <Pencil :size="16" />
                        </button>
                        <button class="btn btn-ghost btn-sm text-danger" @click="confirmDelete(ch)">
                            <Trash2 :size="16" />
                        </button>
                    </td>
                </tr>
                <tr v-if="filteredChannels.length === 0">
                    <td colspan="6" class="text-center text-muted">{{ t('messages.noResults') }}</td>
                </tr>
            </tbody>
        </table>

        <!-- Create/Edit Modal -->
        <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3>{{ editTarget ? t('actions.edit') : t('actions.create') }} {{ t('dashboard.notificationChannel') }}</h3>
                </div>
                <div class="modal-body">
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
                        <input v-model="form.config[form.type === 'feishu' ? 'webhook_url' : 'url']" class="form-input" placeholder="https://..." required />
                    </div>
                    <div class="form-group" v-if="form.type === 'feishu'">
                        <label class="form-label">{{ t('dashboard.notificationSecret') }}</label>
                        <input v-model="form.config.secret" class="form-input" :placeholder="t('dashboard.notificationSecretPlaceholder')" />
                    </div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary btn-sm" @click="showCreateModal = false">{{ t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm" @click="submitForm">{{ t('actions.save') }}</button>
                </div>
            </div>
        </div>

        <!-- Delete Confirm Modal -->
        <div v-if="showDeleteModal" class="modal-overlay" @click.self="showDeleteModal = false">
            <div class="modal-content">
                <div class="modal-header">
                    <h3>{{ t('actions.confirmDelete') }}</h3>
                </div>
                <div class="modal-body">
                    <p>{{ t('messages.confirmDeleteChannel', { name: deleteTarget?.name }) }}</p>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary btn-sm" @click="showDeleteModal = false">{{ t('actions.cancel') }}</button>
                    <button class="btn btn-primary btn-sm text-danger" @click="executeDelete">{{ t('actions.delete') }}</button>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.error-banner { background: #fef2f2; color: #dc2626; border: 1px solid #fecaca; border-radius: 6px; padding: 10px 14px; margin-bottom: 12px; font-size: 13px; cursor: pointer; }
.url-cell {
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.badge-info { background: #3b82f6; color: white; }
.badge-success { background: #22c55e; color: white; }
.badge-muted { background: #6b7280; color: white; }
.badge-secondary { background: #8b5cf6; color: white; }
.text-danger { color: #ef4444; }
.text-center { text-align: center; }
.text-muted { color: #9ca3af; }
</style>
