<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle, Search, ChevronDown, ChevronRight, CheckCircle, XCircle } from 'lucide-vue-next'
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
    <div class="page-container">
        <div class="page-header">
            <h2><AlertTriangle :size="22" /> {{ t('dashboard.alarmEvents') }}</h2>
            <div class="header-actions">
                <select v-model="statusFilter" class="form-input filter-select">
                    <option value="">{{ t('dashboard.alarmFilterAll') }}</option>
                    <option value="firing">Firing</option>
                    <option value="resolved">Resolved</option>
                </select>
                <div class="search-box">
                    <Search :size="16" />
                    <input v-model="searchQuery" :placeholder="t('actions.search')" class="form-input" />
                </div>
            </div>
        </div>

        <div v-if="errorMsg" class="error-banner" @click="errorMsg = ''">{{ errorMsg }}</div>

        <div v-if="loading" class="loading-spinner">Loading...</div>

        <table v-else class="data-table">
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
                <template v-for="event in filteredEvents" :key="event.UUID">
                    <tr class="event-row" @click="toggleExpand(event.UUID)">
                        <td>
                            <component :is="expandedEvent === event.UUID ? ChevronDown : ChevronRight" :size="14" />
                        </td>
                        <td>{{ event.alert_name }}</td>
                        <td>{{ event.vm_name || event.vm_uuid }}</td>
                        <td>
                            <span class="badge" :class="severityClass(event.severity)">
                                {{ event.severity }}
                            </span>
                        </td>
                        <td>
                            <span class="badge" :class="event.status === 'firing' ? 'badge-firing' : 'badge-resolved'">
                                {{ event.status }}
                            </span>
                        </td>
                        <td>{{ formatTime(event.fired_at) }}</td>
                        <td>{{ formatTime(event.last_fired_at) }}</td>
                        <td>{{ formatTime(event.resolved_at) }}</td>
                    </tr>
                    <!-- Expanded: Delivery Logs -->
                    <tr v-if="expandedEvent === event.UUID" class="expanded-row">
                        <td colspan="8">
                            <div class="delivery-logs">
                                <h4>{{ t('dashboard.alarmDeliveryLogs') }}</h4>
                                <div v-if="loadingLogs === event.UUID" class="loading-spinner">Loading...</div>
                                <table v-else-if="deliveryLogs[event.UUID]?.length" class="data-table nested-table">
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
                                        <tr v-for="log in deliveryLogs[event.UUID]" :key="log.UUID">
                                            <td>{{ log.channel_name }}</td>
                                            <td>{{ log.channel_type }}</td>
                                            <td>
                                                <span class="badge badge-secondary">{{ log.notify_type }}</span>
                                            </td>
                                            <td>
                                                <CheckCircle v-if="log.status === 'sent'" :size="16" class="text-success" />
                                                <XCircle v-else :size="16" class="text-danger" />
                                                {{ log.status }}
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
                <tr v-if="filteredEvents.length === 0 && !loading">
                    <td colspan="8" class="text-center text-muted">{{ t('messages.noResults') }}</td>
                </tr>
            </tbody>
        </table>

        <!-- Pagination -->
        <div v-if="totalPages > 1" class="pagination">
            <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="prevPage">Prev</button>
            <span class="page-info">{{ page }} / {{ totalPages }} ({{ total }} total)</span>
            <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="nextPage">Next</button>
        </div>
    </div>
</template>

<style scoped>
.event-row { cursor: pointer; }
.event-row:hover { background: var(--bg-hover, #f3f4f6); }
.expanded-row td { padding: 0; }
.delivery-logs { padding: 12px 16px; background: var(--bg-secondary, #f9fafb); }
.delivery-logs h4 { margin: 0 0 8px 0; font-size: 13px; }
.nested-table { margin: 0; font-size: 13px; }
.filter-select { width: 120px; }
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
.error-banner { background: #fef2f2; color: #dc2626; border: 1px solid #fecaca; border-radius: 6px; padding: 10px 14px; margin-bottom: 12px; font-size: 13px; cursor: pointer; }
</style>
