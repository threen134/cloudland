<script setup lang="ts">
import { ref, watch } from 'vue'
import { instancesApi } from '../../api/instances'
import { Line } from 'vue-chartjs'
import { useI18n } from 'vue-i18n'
import { Activity, RefreshCw } from 'lucide-vue-next'
import { useMonitoring, TIME_RANGES, CHART_OPTIONS } from '../../composables/useMonitoring'

const props = defineProps<{
    instanceId: string
    interfaces: any[]
    volumes: any[]
}>()

const { t } = useI18n()

const cpuData = ref<any>(null)
const memData = ref<any>(null)
const netData = ref<any>(null)

const fetchData = async () => {
    loading.value = true
    error.value = ''

    const range = getTimeRange()
    if (!range) {
        error.value = t('dashboard.monitoring.selectDatesError')
        loading.value = false
        return
    }

    const commonPayload = {
        id: [props.instanceId],
        start: range.startTs.toString(),
        end: range.endTs.toString(),
        step: step.value
    }

    try {
        const [cpuRes, memRes] = await Promise.all([
            instancesApi.getCPUMetrics(commonPayload),
            instancesApi.getMemoryMetrics(commonPayload)
        ])

        // Parse CPU
        if (cpuRes.data?.data?.result?.[0]) {
            const result = cpuRes.data.data.result[0]
            cpuData.value = {
                labels: result.values.map((v: any) => formatTimestamp(v.time)),
                datasets: [{
                    label: t('dashboard.monitoring.cpuUsage'),
                    data: result.values.map((v: any) => parseFloat(v.value)),
                    borderColor: '#3b82f6',
                    backgroundColor: 'rgba(59, 130, 246, 0.1)',
                    fill: true,
                }]
            }
        }

        // Parse Memory
        if (memRes.data?.data?.result?.[0]) {
            const result = memRes.data.data.result[0]
            if (Array.isArray(result.values) && result.values.length >= 2
                && Array.isArray(result.values[0]) && result.values[0].length > 0) {
                const labels = result.values[0].map((v: any) => formatTimestamp(v.time))
                memData.value = {
                    labels,
                    datasets: [
                        {
                            label: t('dashboard.monitoring.total'),
                            data: result.values[0].map((v: any) => (parseFloat(v.value) / 1024 / 1024).toFixed(2)),
                            borderColor: '#94a3b8',
                            borderDash: [5, 5],
                            fill: false,
                        },
                        {
                            label: t('dashboard.monitoring.used'),
                            data: result.values[1].map((v: any) => (parseFloat(v.value) / 1024 / 1024).toFixed(2)),
                            borderColor: '#10b981',
                            backgroundColor: 'rgba(16, 185, 129, 0.1)',
                            fill: true,
                        }
                    ]
                }
            }
        }

        // Network
        if (props.interfaces?.length > 0) {
            const interfaceIDs: string[] = props.interfaces.map((i: any) => i.id).filter(Boolean)
            if (interfaceIDs.length > 0) {
                const netRes = await instancesApi.getNetworkMetrics({
                    interface_ids: interfaceIDs,
                    start: commonPayload.start,
                    end: commonPayload.end,
                    step: commonPayload.step,
                })
                // build interface_id → name lookup
                const ifaceNameById: Record<string, string> = {}
                props.interfaces.forEach((i: any, idx: number) => {
                    if (i.id) ifaceNameById[i.id] = i.name || `eth${idx}`
                })

                const colors = ['#6366f1', '#f59e0b', '#10b981', '#ef4444', '#8b5cf6', '#06b6d4']
                const datasets: any[] = []
                let labels: string[] = []
                let colorIdx = 0

                const perIfaceResults: any[] = netRes.data || []
                perIfaceResults.forEach((ifaceResult: any) => {
                    const res = ifaceResult?.data?.result?.[0]
                    if (!res?.values || res.values.length < 2) return
                    // match by interface_id from metric, not by array index
                    const ifaceUUID: string = res.metric?.interface_id || ''
                    const ifaceName = ifaceNameById[ifaceUUID] || ifaceUUID.slice(0, 8)
                    if (labels.length === 0) {
                        labels = res.values[0].map((v: any) => formatTimestamp(v.time))
                    }
                    datasets.push(
                        { label: `${ifaceName} ${t('dashboard.monitoring.receive')}`, data: res.values[0].map((v: any) => parseFloat(v.value)), borderColor: colors[colorIdx % colors.length], fill: false },
                        { label: `${ifaceName} ${t('dashboard.monitoring.transmit')}`, data: res.values[1].map((v: any) => parseFloat(v.value)), borderColor: colors[(colorIdx + 1) % colors.length], fill: false }
                    )
                    colorIdx += 2
                })
                if (datasets.length > 0) {
                    netData.value = { labels, datasets }
                }
            }
        }

    } catch (err: any) {
        console.error('Failed to fetch metrics:', err)
        error.value = t('dashboard.monitoring.loadError')
    } finally {
        loading.value = false
    }
}

const {
    timeRange, step, customStart, customEnd, loading, error,
    formatTimestamp, getTimeRange, setRange, toggleCustom,
} = useMonitoring(fetchData)

watch(() => props.instanceId, fetchData)
</script>

<template>
    <div class="monitoring-container card">
        <div class="monitor-header">
            <div class="monitor-title">
                <Activity :size="20" class="text-primary" />
                <h3>{{ t('dashboard.instanceDetail.resourceMonitoring') }}</h3>
            </div>
            <div class="monitor-actions">
                <div class="range-selector">
                    <button
                        v-for="r in TIME_RANGES"
                        :key="r.value"
                        :class="['range-btn', { active: timeRange === r.value }]"
                        @click="setRange(r)"
                    >
                        {{ r.label }}
                    </button>
                    <button
                        :class="['range-btn', { active: timeRange === 'custom' }]"
                        @click="toggleCustom"
                    >
                        {{ t('dashboard.monitoring.custom') }}
                    </button>
                </div>
                <button class="btn btn-ghost btn-sm icon-only" @click="fetchData" :disabled="loading">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
            </div>
        </div>

        <div v-if="timeRange === 'custom'" class="custom-range-bar">
            <div class="input-group">
                <label>{{ t('dashboard.monitoring.start') }}</label>
                <input type="datetime-local" v-model="customStart" class="range-date-input" />
            </div>
            <div class="input-group">
                <label>{{ t('dashboard.monitoring.end') }}</label>
                <input type="datetime-local" v-model="customEnd" class="range-date-input" />
            </div>
            <button class="btn btn-primary btn-sm" @click="fetchData" :disabled="loading" style="padding: 4px 16px;">
                {{ t('actions.apply') }}
            </button>
        </div>

        <div v-if="error" class="monitor-error">
            <p>{{ error }}</p>
            <button class="btn btn-primary btn-xs" @click="fetchData">{{ t('actions.retry') }}</button>
        </div>

        <div class="charts-grid">
            <div class="chart-box">
                <div class="chart-title">{{ t('dashboard.monitoring.cpuUtilization') }}</div>
                <div class="chart-wrapper">
                    <div v-if="loading && !cpuData" class="chart-loader"><RefreshCw class="spinning" /></div>
                    <Line v-else-if="cpuData" :data="cpuData" :options="CHART_OPTIONS" />
                    <div v-else class="no-data">{{ t('dashboard.monitoring.noCpuData') }}</div>
                </div>
            </div>

            <div class="chart-box">
                <div class="chart-title">{{ t('dashboard.monitoring.memoryUsage') }}</div>
                <div class="chart-wrapper">
                    <div v-if="loading && !memData" class="chart-loader"><RefreshCw class="spinning" /></div>
                    <Line v-else-if="memData" :data="memData" :options="CHART_OPTIONS" />
                    <div v-else class="no-data">{{ t('dashboard.monitoring.noMemoryData') }}</div>
                </div>
            </div>

            <div class="chart-box">
                <div class="chart-title">{{ t('dashboard.monitoring.networkThroughput') }}</div>
                <div class="chart-wrapper">
                    <div v-if="loading && !netData" class="chart-loader"><RefreshCw class="spinning" /></div>
                    <Line v-else-if="netData" :data="netData" :options="CHART_OPTIONS" />
                    <div v-else class="no-data">{{ t('dashboard.monitoring.noNetworkData') }}</div>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.monitoring-container {
    padding: var(--spacing-5);
    margin-top: var(--spacing-5);
}

.monitor-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-5);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-4);
}

.monitor-title {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.monitor-title h3 {
    margin: 0;
    font-size: var(--font-size-md);
    font-weight: 600;
}

.monitor-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

.range-selector {
    display: flex;
    background: var(--bg-secondary);
    padding: 2px;
    border-radius: var(--radius-md);
    border: 1px solid var(--border-light);
}

.range-btn {
    padding: 4px 12px;
    border: none;
    background: none;
    font-size: var(--font-size-xs);
    font-weight: 500;
    color: var(--text-secondary);
    cursor: pointer;
    border-radius: var(--radius-sm);
    transition: all 0.2s;
}

.range-btn:hover {
    color: var(--primary-color);
}

.range-btn.active {
    background: var(--bg-primary);
    color: var(--primary-color);
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
}

.custom-range-bar {
    display: flex;
    align-items: flex-end;
    gap: var(--spacing-4);
    background: var(--bg-secondary);
    padding: var(--spacing-3) var(--spacing-4);
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-5);
    border: 1px solid var(--border-light);
}

.input-group {
    display: flex;
    flex-direction: column;
    gap: 4px;
}

.input-group label {
    font-size: 11px;
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
}

.range-date-input {
    padding: 6px 10px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    outline: none;
    transition: border-color 0.2s;
}

.range-date-input:focus {
    border-color: var(--primary-color);
}

.charts-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: var(--spacing-5);
}

.chart-box {
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    padding: var(--spacing-4);
}

.chart-title {
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-secondary);
    margin-bottom: var(--spacing-3);
    text-transform: uppercase;
    letter-spacing: 0.05em;
}

.chart-wrapper {
    height: 220px;
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
}

.chart-loader {
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--primary-color);
}

.no-data {
    font-size: var(--font-size-sm);
    color: var(--text-light);
}

.monitor-error {
    background: #fef2f2;
    color: #ef4444;
    padding: 8px 12px;
    border-radius: var(--radius-md);
    margin-bottom: 16px;
    font-size: 13px;
    display: flex;
    justify-content: space-between;
    align-items: center;
}

.spinning {
    animation: spin 1s linear infinite;
}

@keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
}

@media (max-width: 768px) {
    .charts-grid {
        grid-template-columns: 1fr;
    }
}
</style>
