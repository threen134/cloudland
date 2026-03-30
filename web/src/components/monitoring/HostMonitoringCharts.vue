<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { hypervisorsApi } from '../../api/hypervisors'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler } from 'chart.js'
import { Line } from 'vue-chartjs'
import { useI18n } from 'vue-i18n'
import { Activity, Clock, RefreshCw } from 'lucide-vue-next'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler)

const props = defineProps<{
    hostname: string
}>()

const { t } = useI18n()

// --- Filter State ---
const timeRange = ref('1h')
const step = ref('60s')
const customStart = ref('')
const customEnd = ref('')
const loading = ref(false)
const error = ref('')

const ranges = [
    { label: '1h', value: '1h', step: '60s' },
    { label: '6h', value: '6h', step: '5m' },
    { label: '24h', value: '24h', step: '10m' },
    { label: '7d', value: '7d', step: '1h' },
    { label: '30d', value: '30d', step: '2h' },
]

// --- Helper to get default datetime strings for custom range ---
const getDefaultCustomDates = () => {
    const end = new Date()
    const start = new Date(end.getTime() - 3600 * 1000)
    const pad = (n: number) => n.toString().padStart(2, '0')
    const format = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
    return { start: format(start), end: format(end) }
}

// --- Charts Data ---
const cpuData = ref<any>(null)
const memData = ref<any>(null)

const chartOptions = {
    responsive: true,
    maintainAspectRatio: false,
    plugins: {
        legend: {
            display: true,
            position: 'bottom' as const,
            labels: { boxWidth: 12, usePointStyle: true, font: { size: 11 } }
        },
        tooltip: {
            mode: 'index' as const,
            intersect: false,
        }
    },
    scales: {
        x: {
            display: true,
            grid: { display: false },
            ticks: { maxRotation: 0, autoSkip: true, maxTicksLimit: 8, font: { size: 10 } }
        },
        y: {
            beginAtZero: true,
            grid: { color: 'rgba(0, 0, 0, 0.05)' },
            ticks: { font: { size: 10 } }
        }
    },
    elements: {
        line: { tension: 0.3, borderWidth: 2 },
        point: { radius: 0, hoverRadius: 4 }
    }
}

const formatTimestamp = (ts: string) => {
    const d = new Date(parseInt(ts) * 1000)
    if (timeRange.value === '1h' || timeRange.value === '6h') {
        return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    }
    return d.toLocaleDateString([], { month: '2-digit', day: '2-digit' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

const fetchData = async () => {
    if (!props.hostname) return
    loading.value = true
    error.value = ''
    
    let startTs: number
    let endTs: number = Math.floor(Date.now() / 1000)

    if (timeRange.value === 'custom') {
        if (!customStart.value || !customEnd.value) {
            error.value = 'Please select both start and end times'
            loading.value = false
            return
        }
        startTs = Math.floor(new Date(customStart.value).getTime() / 1000)
        endTs = Math.floor(new Date(customEnd.value).getTime() / 1000)
        
        const duration = endTs - startTs
        if (duration <= 3600) step.value = '60s'
        else if (duration <= 86400) step.value = '10m'
        else if (duration <= 7 * 86400) step.value = '1h'
        else step.value = '6h'
    } else {
        startTs = endTs - 3600
        if (timeRange.value === '6h') startTs = endTs - 6 * 3600
        if (timeRange.value === '24h') startTs = endTs - 24 * 3600
        if (timeRange.value === '7d') startTs = endTs - 7 * 24 * 3600
        if (timeRange.value === '30d') startTs = endTs - 30 * 24 * 3600
    }

    const commonPayload = {
        hostname: [props.hostname],
        start: startTs.toString(),
        end: endTs.toString(),
        step: step.value
    }

    try {
        const [cpuRes, memRes] = await Promise.all([
            hypervisorsApi.getCPUMetrics(commonPayload),
            hypervisorsApi.getMemoryMetrics(commonPayload)
        ])

        // Parse CPU
        const cpuResult = cpuRes.data?.data?.result?.[0] || cpuRes.data?.[0]?.data?.result?.[0]
        if (cpuResult) {
            cpuData.value = {
                labels: cpuResult.values.map((v: any) => formatTimestamp(v.time)),
                datasets: [{
                    label: t('dashboard.monitoring.cpuUsage'),
                    data: cpuResult.values.map((v: any) => parseFloat(v.value)),
                    borderColor: '#3b82f6',
                    backgroundColor: 'rgba(59, 130, 246, 0.1)',
                    fill: true,
                }]
            }
        }

        // Parse Memory
        const memResult = memRes.data?.data?.result?.[0] || memRes.data?.[0]?.data?.result?.[0]
        if (memResult && memResult.values && memResult.values.length >= 2) {
            const totalSamples = memResult.values[0] || []
            const usedSamples = memResult.values[1] || []
            
            if (totalSamples.length > 0) {
                const labels = totalSamples.map((v: any) => formatTimestamp(v.time))
                memData.value = {
                    labels,
                    datasets: [
                        {
                            label: t('dashboard.monitoring.total'),
                            data: totalSamples.map((v: any) => (parseFloat(v.value) / 1024 / 1024).toFixed(2)),
                            borderColor: '#94a3b8',
                            borderDash: [5, 5],
                            fill: false,
                        },
                        {
                            label: t('dashboard.monitoring.used'),
                            data: usedSamples.map((v: any) => (parseFloat(v.value) / 1024 / 1024).toFixed(2)),
                            borderColor: '#10b981',
                            backgroundColor: 'rgba(16, 185, 129, 0.1)',
                            fill: true,
                        }
                    ]
                }
            }
        }
        
    } catch (err: any) {
        console.error('Failed to fetch host metrics:', err)
        error.value = t('dashboard.monitoring.loadError')
    } finally {
        loading.value = false
    }
}

const setRange = (range: any) => {
    timeRange.value = range.value
    step.value = range.step
    fetchData()
}

watch(() => props.hostname, fetchData)

onMounted(fetchData)

let refreshInterval: any = null
onMounted(() => {
    refreshInterval = setInterval(() => {
        if (timeRange.value === '1h') fetchData()
    }, 60000)
})

onUnmounted(() => {
    if (refreshInterval) clearInterval(refreshInterval)
})

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
                        v-for="r in ranges" 
                        :key="r.value"
                        class="btn btn-ghost btn-xs"
                        :class="{ active: timeRange === r.value }"
                        @click="setRange(r)"
                    >
                        {{ r.label }}
                    </button>
                    <button 
                        class="btn btn-ghost btn-xs"
                        :class="{ active: timeRange === 'custom' }"
                        @click="timeRange = 'custom'"
                    >
                        {{ t('dashboard.monitoring.custom') }}
                    </button>
                </div>
                <button class="btn btn-ghost btn-xs" @click="fetchData" :disabled="loading">
                    <RefreshCw :size="14" :class="{ 'animate-spin': loading }" />
                </button>
            </div>
        </div>

        <div v-if="timeRange === 'custom'" class="custom-range-picker">
            <div class="filter-group">
                <label><Clock :size="14" /> {{ t('dashboard.monitoring.start') }}</label>
                <input type="datetime-local" v-model="customStart" class="form-select" />
            </div>
            <div class="filter-group">
                <label><Clock :size="14" /> {{ t('dashboard.monitoring.end') }}</label>
                <input type="datetime-local" v-model="customEnd" class="form-select" />
            </div>
            <button class="btn btn-primary btn-sm" @click="fetchData" :disabled="loading">
                {{ t('actions.apply') }}
            </button>
        </div>

        <div v-if="error" class="monitor-error">
            {{ error }}
        </div>

        <div class="charts-grid">
            <div class="chart-box">
                <div class="chart-header">
                    <h4>{{ t('dashboard.monitoring.cpuUsage') }}</h4>
                    <span v-if="cpuData" class="current-value">
                        {{ cpuData.datasets[0].data[cpuData.datasets[0].data.length - 1] }}%
                    </span>
                </div>
                <div class="chart-body">
                    <Line v-if="cpuData" :data="cpuData" :options="chartOptions" />
                    <div v-else-if="loading" class="chart-loading">Loading...</div>
                    <div v-else class="chart-empty">{{ t('dashboard.monitoring.noCpuData') }}</div>
                </div>
            </div>

            <div class="chart-box">
                <div class="chart-header">
                    <h4>{{ t('dashboard.monitoring.memoryUsage') }}</h4>
                    <span v-if="memData" class="current-value">
                        {{ memData.datasets[1].data[memData.datasets[1].data.length - 1] }} GB / {{ memData.datasets[0].data[memData.datasets[0].data.length - 1] }} GB
                    </span>
                </div>
                <div class="chart-body">
                    <Line v-if="memData" :data="memData" :options="chartOptions" />
                    <div v-else-if="loading" class="chart-loading">Loading...</div>
                    <div v-else class="chart-empty">{{ t('dashboard.monitoring.noMemoryData') }}</div>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.monitoring-container {
    padding: var(--spacing-5);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-6);
}

.monitor-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
}

.monitor-title {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.monitor-title h3 {
    font-size: 1.1rem;
    font-weight: 600;
    margin: 0;
}

.monitor-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
}

.range-selector {
    display: flex;
    background: var(--bg-secondary);
    padding: 2px;
    border-radius: var(--radius-md);
    border: 1px solid var(--border-color);
}

.range-selector .btn {
    padding: 2px 10px;
    font-size: 0.75rem;
    height: 24px;
    min-height: 24px;
}

.range-selector .btn.active {
    background: var(--bg-primary);
    box-shadow: var(--shadow-sm);
    color: var(--text-primary);
}

.custom-range-picker {
    display: flex;
    align-items: flex-end;
    gap: var(--spacing-4);
    background: var(--bg-secondary);
    padding: var(--spacing-4);
    border-radius: var(--radius-md);
    margin-top: -8px;
}

.filter-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-1);
}

.filter-group label {
    font-size: 0.75rem;
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    gap: 4px;
}

.filter-group .form-select {
    height: 32px;
    padding: 0 8px;
    font-size: 0.85rem;
    background: var(--bg-primary);
}

.charts-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
    gap: var(--spacing-6);
}

.chart-box {
    background: var(--bg-primary);
    border: 1px solid var(--border-color);
    border-radius: var(--radius-lg);
    padding: var(--spacing-4);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
    height: 320px;
}

.chart-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
}

.chart-header h4 {
    font-size: 0.9rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
}

.current-value {
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--text-secondary);
    background: var(--bg-secondary);
    padding: 2px 8px;
    border-radius: var(--radius-full);
}

.chart-body {
    flex: 1;
    position: relative;
    min-height: 0;
}

.chart-loading, .chart-empty {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--text-secondary);
    font-size: 0.85rem;
}

.monitor-error {
    padding: var(--spacing-3);
    background: #fef2f2;
    color: #ef4444;
    border-radius: var(--radius-md);
    font-size: 0.85rem;
    border: 1px solid #fee2e2;
}

@media (max-width: 768px) {
    .charts-grid {
        grid-template-columns: 1fr;
    }
    
    .custom-range-picker {
        flex-direction: column;
        align-items: flex-start;
    }
}
</style>
