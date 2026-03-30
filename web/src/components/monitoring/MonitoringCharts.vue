<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { instancesApi } from '../../api/instances'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler } from 'chart.js'
import { Line } from 'vue-chartjs'
import { useI18n } from 'vue-i18n'
import { Activity, Clock, RefreshCw } from 'lucide-vue-next'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler)

const props = defineProps<{
    instanceId: string
    interfaces: any[]
    volumes: any[]
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
    const start = new Date(end.getTime() - 3600 * 1000) // Default last 1h
    const pad = (n: number) => n.toString().padStart(2, '0')
    const format = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
    return { start: format(start), end: format(end) }
}

// --- Charts Data ---
const cpuData = ref<any>(null)
const memData = ref<any>(null)
const diskData = ref<any>(null)
const netData = ref<any>(null)

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
    loading.value = true
    error.value = ''
    
    // Calculate start/end
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
        
        // Auto-calculate step based on duration
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
        id: [props.instanceId],
        start: startTs.toString(),
        end: endTs.toString(),
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

        // Add Disk/Network later if devices found
        if (props.interfaces?.length > 0) {
            // CloudLand uses tap + last 6 chars of MAC for libvirt target_device
            // Use primary interface if found, otherwise use the first one
            const iface = props.interfaces.find(i => i.is_primary) || props.interfaces[0]
            const tapName = iface.mac_address 
                ? 'tap' + iface.mac_address.replace(/:/g, '').slice(-6).toLowerCase()
                : (iface.name || 'eth0')

            const netRes = await instancesApi.getNetworkMetrics({
                ...commonPayload,
                network: [tapName]
            })
            const res = netRes.data?.[0]?.data?.result?.[0] || netRes.data?.data?.result?.[0]
            if (res) {
                const labels = res.values[0].map((v: any) => formatTimestamp(v.time))
                netData.value = {
                    labels,
                    datasets: [
                        { label: t('dashboard.monitoring.receive'), data: res.values[0].map((v: any) => parseFloat(v.value)), borderColor: '#6366f1', fill: false },
                        { label: t('dashboard.monitoring.transmit'), data: res.values[1].map((v: any) => parseFloat(v.value)), borderColor: '#f59e0b', fill: false }
                    ]
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

const setRange = (range: any) => {
    timeRange.value = range.value
    step.value = range.step
    fetchData()
}

const toggleCustom = () => {
    if (timeRange.value !== 'custom') {
        const { start, end } = getDefaultCustomDates()
        customStart.value = start
        customEnd.value = end
        timeRange.value = 'custom'
        fetchData()
    }
}

watch(() => props.instanceId, fetchData)

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
                <h3>{{ $t('dashboard.instanceDetail.resourceMonitoring') || 'Monitoring' }}</h3>
            </div>
            <div class="monitor-actions">
                <div class="range-selector">
                    <button 
                        v-for="r in ranges" 
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
                        {{ t('dashboard.monitoring.custom') || 'Custom' }}
                    </button>
                </div>
                <button class="btn btn-ghost btn-sm icon-only" @click="fetchData" :disabled="loading">
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
            </div>
        </div>

        <div v-if="timeRange === 'custom'" class="custom-range-bar">
            <div class="input-group">
                <label>{{ t('dashboard.monitoring.start') || 'Start' }}</label>
                <input type="datetime-local" v-model="customStart" class="range-date-input" />
            </div>
            <div class="input-group">
                <label>{{ t('dashboard.monitoring.end') || 'End' }}</label>
                <input type="datetime-local" v-model="customEnd" class="range-date-input" />
            </div>
            <button class="btn btn-primary btn-sm" @click="fetchData" :disabled="loading" style="padding: 4px 16px;">
                {{ t('actions.apply') || 'Apply' }}
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
                    <Line v-else-if="cpuData" :data="cpuData" :options="chartOptions" />
                    <div v-else class="no-data">{{ t('dashboard.monitoring.noCpuData') }}</div>
                </div>
            </div>

            <div class="chart-box">
                <div class="chart-title">{{ t('dashboard.monitoring.memoryUsage') }}</div>
                <div class="chart-wrapper">
                    <div v-if="loading && !memData" class="chart-loader"><RefreshCw class="spinning" /></div>
                    <Line v-else-if="memData" :data="memData" :options="chartOptions" />
                    <div v-else class="no-data">{{ t('dashboard.monitoring.noMemoryData') }}</div>
                </div>
            </div>

            <div class="chart-box">
                <div class="chart-title">{{ t('dashboard.monitoring.networkThroughput') }}</div>
                <div class="chart-wrapper">
                    <div v-if="loading && !netData" class="chart-loader"><RefreshCw class="spinning" /></div>
                    <Line v-else-if="netData" :data="netData" :options="chartOptions" />
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
