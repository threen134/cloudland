<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle, Search, ChevronDown, ChevronRight, CheckCircle, XCircle, RefreshCw } from 'lucide-vue-next'
import { alarmEventsApi, type AlarmEvent, type AlarmDeliveryLog } from '../../api/alarmEvents'

const { t } = useI18n()
const events = ref<AlarmEvent[]>([])
const loading = ref(false)
const errorMsg = ref('')
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const statusFilter = ref('')
const searchQuery = ref('')
const expandedEvent = ref<string | null>(null)
const deliveryLogs = ref<Record<string, AlarmDeliveryLog[]>>({})
const loadingLogs = ref<string | null>(null)

const filteredEvents = computed(() => {
    if (!searchQuery.value) return events.value
    const q = searchQuery.value.toLowerCase()
    return events.value.filter(e =>
        e.alert_name.toLowerCase().includes(q) ||
        e.vm_name.toLowerCase().includes(q) ||
        e.vm_uuid.toLowerCase().includes(q)
    )
})

const fetchEvents = async () => {
    loading.value = true
    try {
        const params: Record<string, any> = { page: page.value, page_size: pageSize.value }
        if (statusFilter.value) params.status = statusFilter.value
        const res = await alarmEventsApi.list(params)
        events.value = res.data.events || []
        total.value = res.data.total || 0
    } catch (err) {
        console.error('Failed to fetch alarm events:', err)
        errorMsg.value = t('messages.error')
    } finally {
        loading.value = false
    }
}

const toggleExpand = async (eventUuid: string) => {
    if (expandedEvent.value === eventUuid) {
        expandedEvent.value = null
        return
    }
    expandedEvent.value = eventUuid
    if (!deliveryLogs.value[eventUuid]) {
        loadingLogs.value = eventUuid
        try {
            const res = await alarmEventsApi.getDeliveryLogs(eventUuid)
            deliveryLogs.value[eventUuid] = res.data.delivery_logs || []
        } catch (err) {
            console.error('Failed to fetch delivery logs:', err)
            deliveryLogs.value[eventUuid] = []
        } finally {
            loadingLogs.value = null
        }
    }
}

const totalPages = computed(() => Math.ceil(total.value / pageSize.value))

const prevPage = () => { if (page.value > 1) page.value-- }
const nextPage = () => { if (page.value < totalPages.value) page.value++ }

const formatTime = (ts: string | null) => {
    if (!ts) return '-'
    return new Date(ts).toLocaleString()
}

const severityClass = (severity: string) => {
    switch (severity) {
        case 'critical': return 'badge-critical'
        case 'warning': return 'badge-warning'
        default: return 'badge-info'
    }
}

watch([page, statusFilter], fetchEvents)
onMounted(fetchEvents)
</script>

<template>
    <div class="vpc-list-container">
        <div class="page-header">
            <div class="search-wrapper">
                <div class="search-box">
                    <Search :size="16" class="search-icon" />
                    <input v-model="searchQuery" :placeholder="t('actions.search') + '...'" class="search-input" />
                </div>
                <select v-model="statusFilter" @change="fetchEvents" class="filter-select">
                    <option value="">{{ t('dashboard.alarmFilterAll') }}</option>
                    <option value="firing">{{ t('dashboard.alarmStatusFiring') }}</option>
                    <option value="resolved">{{ t('dashboard.alarmStatusResolved') }}</option>
                </select>
            </div>
            <div class="header-actions">
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchEvents" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
            </div>
        </div>

        <div v-if="errorMsg" class="error-banner" @click="errorMsg = ''">{{ errorMsg }}</div>

        <div class="card table-card">
            <table class="data-table">
                <thead>
                    <tr>
                        <th style="width: 30px"></th>
                        <th>{{ t('dashboard.alarmAlertName') }}</th>
                        <th>VM</th>
                        <th>{{ t('dashboard.alarmSeverity') }}</th>
                        <th>{{ t('dashboard.table.status') }}</th>
                        <th>{{ t('dashboard.alarmFiredAt') }}</th>
                        <th>{{ t('dashboard.alarmLastFired') }}</th>
                        <th>{{ t('dashboard.alarmResolvedAt') }}</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-if="loading">
                        <td colspan="8" class="text-center">
                            <div class="loading-spinner" style="margin: 20px auto;"></div>
                        </td>
                    </tr>
                    <template v-else-if="filteredEvents.length > 0" v-for="event in filteredEvents" :key="event.uuid">
                        <tr class="event-row" @click="toggleExpand(event.uuid)">
                            <td>
                                <component :is="expandedEvent === event.uuid ? ChevronDown : ChevronRight" :size="14" />
                            </td>
                            <td>{{ event.alert_name }}</td>
                            <td>{{ event.vm_name || event.vm_uuid }}</td>
                            <td>
                                <span class="badge" :class="severityClass(event.severity)">
                                    {{ t('dashboard.vmAlarmRules.levels.' + event.severity) }}
                                </span>
                            </td>
                            <td>
                                <span class="badge" :class="event.status === 'firing' ? 'badge-firing' : 'badge-resolved'">
                                    {{ event.status === 'firing' ? t('dashboard.alarmStatusFiring') : t('dashboard.alarmStatusResolved') }}
                                </span>
                            </td>
                            <td>{{ formatTime(event.fired_at) }}</td>
                            <td>{{ formatTime(event.last_fired_at) }}</td>
                            <td>{{ formatTime(event.resolved_at) }}</td>
                        </tr>
                        <!-- Expanded: Delivery Logs -->
                        <tr v-if="expandedEvent === event.uuid" class="expanded-row">
                            <td colspan="8">
                                <div class="delivery-logs">
                                    <h4>{{ t('dashboard.alarmDeliveryLogs') }}</h4>
                                    <div v-if="loadingLogs === event.uuid" class="loading-spinner" style="margin: 12px auto;"></div>
                                    <table v-else-if="deliveryLogs[event.uuid]?.length" class="data-table nested-table">
                                        <thead>
                                            <tr>
                                                <th>{{ t('dashboard.alarmChannelName') }}</th>
                                                <th>{{ t('dashboard.notificationChannelType') }}</th>
                                                <th>{{ t('dashboard.alarmNotifyType') }}</th>
                                                <th>{{ t('dashboard.table.status') }}</th>
                                                <th>{{ t('dashboard.alarmSentAt') }}</th>
                                                <th>{{ t('dashboard.alarmError') }}</th>
                                            </tr>
                                        </thead>
                                        <tbody>
                                            <tr v-for="log in deliveryLogs[event.uuid]" :key="log.uuid">
                                                <td>{{ log.channel_name }}</td>
                                                <td>{{ log.channel_type }}</td>
                                                <td>
                                                    <span class="badge badge-secondary">{{ log.notify_type }}</span>
                                                </td>
                                                <td>
                                                    <CheckCircle v-if="log.status === 'sent'" :size="16" class="text-success" />
                                                    <XCircle v-else :size="16" class="text-danger" />
                                                    <span style="vertical-align: middle; margin-left: 4px;">{{ log.status === 'sent' ? t('messages.success') : t('messages.error') }}</span>
                                                </td>
                                                <td>{{ formatTime(log.sent_at) }}</td>
                                                <td class="error-cell">{{ log.error_message || '-' }}</td>
                                            </tr>
                                        </tbody>
                                    </table>
                                    <p v-else class="text-muted">{{ t('dashboard.alarmNoDeliveryLogs') }}</p>
                                </div>
                            </td>
                        </tr>
                    </template>
                    <tr v-else>
                        <td colspan="8" class="text-center text-secondary" style="padding: 48px;">
                            <div class="empty-state">
                                <AlertTriangle :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                                <p>{{ t('messages.noData') }}</p>
                            </div>
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>

        <!-- Pagination -->
        <div v-if="totalPages > 1" class="pagination">
            <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="prevPage">{{ t('dashboard.pagination.prev') }}</button>
            <span class="page-info">{{ page }} / {{ totalPages }} ({{ total }} {{ t('dashboard.overview.total') }})</span>
            <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="nextPage">{{ t('dashboard.pagination.next') }}</button>
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

.filter-select {
    height: 40px;
    padding: 0 12px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    background: var(--bg-secondary);
    color: var(--text-primary);
    min-width: 120px;
}

.table-card { padding: 0; overflow: hidden; }

.event-row { cursor: pointer; }
.event-row:hover { background: var(--bg-hover, #f3f4f6); }
.expanded-row td { padding: 0; }
.delivery-logs { padding: 12px 16px; background: var(--bg-secondary, #f9fafb); }
.delivery-logs h4 { margin: 0 0 8px 0; font-size: 13px; }
.nested-table { margin: 0; font-size: 13px; }

.pagination { display: flex; align-items: center; justify-content: center; gap: 12px; padding: 16px 0; }
.page-info { font-size: 13px; color: #6b7280; }

.badge-firing { background: #ef4444; color: white; }
.badge-resolved { background: #22c55e; color: white; }
.badge-critical { background: #dc2626; color: white; }
.badge-warning { background: #f59e0b; color: white; }
.badge-info { background: #3b82f6; color: white; }
.badge-secondary { background: #6b7280; color: white; }
.text-success { color: #22c55e; }
.text-danger { color: #ef4444; }
.text-center { text-align: center; }
.text-muted { color: #9ca3af; }
.error-cell { max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.error-banner {
    background: #fef2f2; color: #dc2626; border: 1px solid #fecaca;
    border-radius: 6px; padding: 10px 14px; margin-bottom: 12px;
    font-size: 13px; cursor: pointer;
}

.empty-state { display: flex; flex-direction: column; align-items: center; justify-content: center; }

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
