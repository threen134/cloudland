<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle, CheckCircle, XCircle, RefreshCw, Check, Copy } from 'lucide-vue-next'
import { alarmEventsApi, type AlarmEvent, type AlarmDeliveryLog } from '../../api/alarmEvents'
import { useCopyId } from '../../composables/useCopyId'
import { formatDateTime } from '../../utils/format'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'

const { t } = useI18n()
const events = ref<AlarmEvent[]>([])
const loading = ref(false)
const errorMsg = ref('')
const total = ref(0)

const { copiedId, copyId } = useCopyId()
const page = ref(1)
const pageSize = ref(20)
const statusFilter = ref('')
const searchQuery = ref('')
const deliveryLogs = ref<Record<string, AlarmDeliveryLog[]>>({})
const loadingLogs = ref<string | null>(null)

const fetchEvents = async () => {
    loading.value = true
    try {
        const params: { status?: string; query?: string; page?: number; page_size?: number } = {
            page: page.value,
            page_size: pageSize.value,
        }
        if (statusFilter.value) params.status = statusFilter.value
        if (searchQuery.value.trim()) params.query = searchQuery.value.trim()
        const res = await alarmEventsApi.list(params)
        events.value = res.events || []
        total.value = res.total || 0
    } catch (err) {
        console.error('Failed to fetch alarm events:', err)
        errorMsg.value = t('messages.error')
    } finally {
        loading.value = false
    }
}

// 展开某一条时才拉它的发送流水（展开态由 DataTable 维护）
const loadDeliveryLogs = async (eventUuid: string) => {
    if (deliveryLogs.value[eventUuid]) return
    loadingLogs.value = eventUuid
    try {
        const res = await alarmEventsApi.getDeliveryLogs(eventUuid)
        deliveryLogs.value[eventUuid] = res.delivery_logs || []
    } catch (err) {
        console.error('Failed to fetch delivery logs:', err)
        deliveryLogs.value[eventUuid] = []
    } finally {
        loadingLogs.value = null
    }
}

const columns = computed<Column[]>(() => [
    { key: 'alert_name', label: t('dashboard.alarmAlertName') },
    { key: 'vm', label: 'VM' },
    { key: 'severity', label: t('dashboard.alarmSeverity') },
    { key: 'status', label: t('dashboard.table.status') },
    { key: 'fired_at', label: t('dashboard.alarmFiredAt') },
    { key: 'last_fired_at', label: t('dashboard.alarmLastFired') },
    { key: 'resolved_at', label: t('dashboard.alarmResolvedAt') },
])

const severityClass = (severity: string) => {
    switch (severity) {
        case 'critical': return 'badge-critical'
        case 'warning': return 'badge-warning'
        default: return 'badge-info'
    }
}

watch([page, statusFilter], () => fetchEvents())
watch(pageSize, () => { if (page.value === 1) fetchEvents(); else page.value = 1 })

// 搜索走服务端（此前是在当前页的 20 条里前端过滤，翻页后就搜不到别的页了）。
// 输入防抖 400ms，并回到第一页——换了搜索条件后停留在第 3 页没有意义
let searchTimer: ReturnType<typeof setTimeout> | null = null
watch(searchQuery, () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
        if (page.value === 1) fetchEvents()
        else page.value = 1
    }, 400)
})
onUnmounted(() => {
    if (searchTimer) clearTimeout(searchTimer)
})

onMounted(fetchEvents)
</script>

<template>
    <div class="vpc-list-container">
        <PageToolbar v-model:search="searchQuery">
            <template #filters>
                <select v-model="statusFilter" @change="fetchEvents" class="filter-select">
                    <option value="">{{ t('dashboard.alarmFilterAll') }}</option>
                    <option value="firing">{{ t('dashboard.alarmStatusFiring') }}</option>
                    <option value="resolved">{{ t('dashboard.alarmStatusResolved') }}</option>
                </select>
            </template>
            <template #actions>
                <button class="btn btn-secondary btn-sm btn-icon" @click="fetchEvents" :title="t('actions.refresh')">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="events"
            row-key="uuid"
            :loading="loading"
            :error="errorMsg"
            expandable
            @expand="loadDeliveryLogs"
            @retry="() => fetchEvents()"
        >
            <template #empty>
                <AlertTriangle :size="48" style="opacity: 0.2; margin-bottom: 16px;" />
                <p>{{ searchQuery || statusFilter ? t('messages.noResults') : t('messages.noData') }}</p>
            </template>

            <template #cell-alert_name="{ row: event }">
                <span class="alert-name-cell" :data-tooltip="event.alert_name">{{ event.alert_name }}</span>
            </template>

            <template #cell-vm="{ row: event }">
                <template v-if="event.vm_uuid">
                    <div v-if="event.vm_name" class="resource-name">{{ event.vm_name }}</div>
                    <div class="resource-id-row">
                        <span class="resource-id" :title="event.vm_uuid">{{ event.vm_uuid.slice(0, 8) + '...' }}</span>
                        <button class="copy-btn-mini" @click.stop.prevent="copyId(event.vm_uuid)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                            <Check v-if="copiedId === event.vm_uuid" :size="10" style="color: #10b981;" />
                            <Copy v-else :size="10" />
                        </button>
                    </div>
                </template>
                <!-- 节点级告警没有虚拟机 -->
                <span v-else class="text-muted">-</span>
            </template>

            <template #cell-severity="{ row: event }">
                <span class="badge" :class="severityClass(event.severity)">
                    {{ t('dashboard.vmAlarmRules.levels.' + event.severity) }}
                </span>
            </template>

            <template #cell-status="{ row: event }">
                <StatusBadge
                    :status="event.status"
                    :label="event.status === 'firing' ? t('dashboard.alarmStatusFiring') : t('dashboard.alarmStatusResolved')"
                />
            </template>

            <template #cell-fired_at="{ row: event }">{{ formatDateTime(event.fired_at) }}</template>
            <template #cell-last_fired_at="{ row: event }">{{ formatDateTime(event.last_fired_at) }}</template>
            <template #cell-resolved_at="{ row: event }">{{ formatDateTime(event.resolved_at) }}</template>

            <template #expanded="{ row: event }">
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
                                    <span class="badge" :class="{
                                        'badge-notify-trigger': log.notify_type === 'firing_trigger',
                                        'badge-notify-remind': log.notify_type === 'repeat_remind',
                                        'badge-resolved': log.notify_type === 'resolved',
                                        'badge-secondary': !['firing_trigger','repeat_remind','resolved'].includes(log.notify_type)
                                    }">{{ t('dashboard.alarmNotifyTypes.' + log.notify_type) }}</span>
                                </td>
                                <td>
                                    <CheckCircle v-if="log.status === 'sent'" :size="16" class="text-success" />
                                    <XCircle v-else :size="16" class="text-danger" />
                                    <span style="vertical-align: middle; margin-left: 4px;">{{ log.status === 'sent' ? t('messages.success') : t('messages.error') }}</span>
                                </td>
                                <td>{{ formatDateTime(log.sent_at) }}</td>
                                <td class="error-cell">{{ log.error_message || '-' }}</td>
                            </tr>
                        </tbody>
                    </table>
                    <p v-else class="text-muted">{{ t('dashboard.alarmNoDeliveryLogs') }}</p>
                </div>
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
    </div>
</template>

<style scoped>
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

.delivery-logs { padding: 12px 16px; background: var(--bg-secondary, #f9fafb); }
.delivery-logs h4 { margin: 0 0 8px 0; font-size: 13px; }
.nested-table { margin: 0; font-size: 13px; width: 100%; }
.nested-table th, .nested-table td { text-align: center; }


.badge-resolved { background: #22c55e; color: white; }
.badge-critical { background: #dc2626; color: white; }
.badge-warning { background: #f59e0b; color: white; }
.badge-info { background: #3b82f6; color: white; }
.badge-secondary { background: #6b7280; color: white; }
.badge-notify-trigger { background: #1e3a8a; color: white; }
.badge-notify-remind { background: #60a5fa; color: white; }
.text-success { color: #22c55e; }
.text-danger { color: #ef4444; }
.text-center { text-align: center; }
.text-muted { color: #9ca3af; }
.error-cell { max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.alert-name-cell {
    display: inline-block;
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
    cursor: default;
    position: relative;
}
.alert-name-cell::after {
    content: attr(data-tooltip);
    position: absolute;
    left: 0;
    top: 100%;
    margin-top: 4px;
    background: rgba(0, 0, 0, 0.75);
    color: #fff;
    padding: 4px 8px;
    border-radius: 4px;
    font-size: 12px;
    white-space: nowrap;
    pointer-events: none;
    opacity: 0;
    transition: opacity 0.15s;
    z-index: var(--z-tooltip);
}
.alert-name-cell:hover::after {
    opacity: 1;
}

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
