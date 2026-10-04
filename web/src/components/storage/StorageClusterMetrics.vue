<script setup lang="ts">
// Curves of a storage cluster (shared-storage-design.md §14.3): the use of its pools, its capacity, throughput and
// IOPS, and its hosts (GPFS) or OSDs (Ceph), from GET /storage_clusters/:id/metrics. A chart the cluster has no data
// for is not drawn. Only for looking at: the alarms come from the health watchdog.
import { computed, shallowRef } from 'vue'
import type { ChartData, ChartOptions } from 'chart.js'
import { Line } from 'vue-chartjs'
import { useI18n } from 'vue-i18n'
import { ChartLine } from 'lucide-vue-next'
import MonitoringPanel from '../monitoring/MonitoringPanel.vue'
import { useMonitoring, CHART_OPTIONS } from '../../composables/useMonitoring'
import { storageClustersApi, type StorageMetricChart, type StorageMetricsResponse } from '../../api/storageClusters'
import { cssVar } from '../../utils/cssVar'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ clusterId: string }>()
const { t, te } = useI18n()

const metrics = shallowRef<StorageMetricsResponse | null>(null)

// Only the latest request may write: a slow 30d query answering after a quick 1h one would replace it
let generation = 0
const fetchData = async () => {
    const range = getTimeRange()
    if (!range) {
        error.value = t('dashboard.monitoring.selectDatesError')
        return
    }
    const mine = ++generation
    loading.value = true
    error.value = ''
    try {
        const data = await storageClustersApi.metrics(props.clusterId, {
            start: range.startTs,
            end: range.endTs,
            step: step.value,
        })
        if (mine === generation) metrics.value = data
    } catch (err) {
        if (mine !== generation) return
        error.value = errorMessage(err, t('dashboard.monitoring.loadError'))
    } finally {
        if (mine === generation) loading.value = false
    }
}

const {
    timeRange,
    step,
    customStart,
    customEnd,
    loading,
    error,
    formatTimestamp,
    getTimeRange,
    setRange,
    toggleCustom,
} = useMonitoring(fetchData)

const labels = computed(() => (metrics.value?.timestamps || []).map((ts) => formatTimestamp(String(ts))))

// Capacity in 1024 steps like everywhere else in the console; operations and counts in 1000 steps
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
const OPS_UNITS = ['', 'K', 'M']
const scaleFor = (unit: string, values: Array<number | null>) => {
    const max = Math.max(0, ...values.map((v) => v ?? 0))
    if (unit === 'bytes' || unit === 'bytes_per_second') {
        let i = 0
        while (max >= 1024 ** (i + 1) && i < BYTE_UNITS.length - 1) i++
        return { div: 1024 ** i, name: BYTE_UNITS[i] + (unit === 'bytes_per_second' ? '/s' : '') }
    }
    if (unit === 'ops_per_second') {
        let i = 0
        while (max >= 1000 ** (i + 1) && i < OPS_UNITS.length - 1) i++
        return { div: 1000 ** i, name: `${OPS_UNITS[i]}${t('storage.metrics.opsPerSecond')}` }
    }
    if (unit === 'percent') return { div: 1, name: '%' }
    return { div: 1, name: '' }
}

const PALETTE = [
    '--primary-color',
    '--success-color',
    '--warning-color',
    '--accent-purple',
    '--accent-teal',
    '--error-color',
    '--gray-500',
    '--warning-dark',
]

const seriesName = (key: string, label?: string) => {
    if (key === 'pool' && label) return label
    const name = te(`storage.metrics.series.${key}`) ? t(`storage.metrics.series.${key}`) : key
    return label ? `${name} · ${label}` : name
}
const chartTitle = (key: string) => (te(`storage.metrics.charts.${key}`) ? t(`storage.metrics.charts.${key}`) : key)

const options = (chart: StorageMetricChart, unitName: string): ChartOptions<'line'> => {
    const integer = chart.unit === 'count'
    return {
        ...(CHART_OPTIONS as ChartOptions<'line'>),
        spanGaps: false,
        plugins: {
            ...(CHART_OPTIONS.plugins as ChartOptions<'line'>['plugins']),
            tooltip: {
                mode: 'index',
                intersect: false,
                callbacks: {
                    label: (ctx) =>
                        `${ctx.dataset.label}: ${
                            ctx.parsed.y === null ? '-' : ctx.parsed.y.toFixed(integer ? 0 : 2)
                        } ${unitName}`.trimEnd(),
                },
            },
        },
        scales: {
            ...(CHART_OPTIONS.scales as ChartOptions<'line'>['scales']),
            y: {
                beginAtZero: true,
                max: chart.unit === 'percent' ? 100 : undefined,
                grid: { color: cssVar('--border-light') },
                ticks: { font: { size: 10 }, precision: integer ? 0 : undefined },
                title: { display: !!unitName, text: unitName, font: { size: 10 } },
            },
        },
    }
}

const charts = computed(() =>
    (metrics.value?.charts || []).map((chart) => {
        const scale = scaleFor(
            chart.unit,
            chart.series.flatMap((s) => s.values)
        )
        // The total of a capacity chart is drawn dashed, as a ceiling for the used curve
        const datasets: ChartData<'line', Array<number | null>>['datasets'] = chart.series.map((s, i) => {
            const color = cssVar(PALETTE[i % PALETTE.length])
            return {
                label: seriesName(s.key, s.label),
                data: s.values.map((v) => (v === null ? null : v / scale.div)),
                borderColor: color,
                backgroundColor: color,
                borderDash: s.key === 'total' ? [5, 4] : undefined,
                fill: false,
            }
        })
        return {
            key: chart.key,
            title: chartTitle(chart.key),
            data: { labels: labels.value, datasets } as ChartData<'line', Array<number | null>>,
            options: options(chart, scale.name),
        }
    })
)
</script>

<template>
    <MonitoringPanel
        v-model:custom-start="customStart"
        v-model:custom-end="customEnd"
        :title="t('storage.metrics.title')"
        :icon="ChartLine"
        :time-range="timeRange"
        :loading="loading"
        :error="error"
        @set-range="setRange"
        @toggle-custom="toggleCustom"
        @refresh="fetchData"
    >
        <div v-if="charts.length" class="chart-grid">
            <div v-for="c in charts" :key="c.key" class="chart-card">
                <h4 class="chart-title">{{ c.title }}</h4>
                <div class="chart-box">
                    <Line :data="c.data" :options="c.options" />
                </div>
            </div>
        </div>
        <div v-else class="metrics-empty">{{ loading ? t('messages.loading') : t('storage.metrics.empty') }}</div>
        <p class="metrics-hint">{{ t('storage.metrics.hint') }}</p>
    </MonitoringPanel>
</template>

<style scoped>
.chart-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(420px, 1fr));
    gap: var(--spacing-4);
}

.chart-card {
    min-width: 0;
}

.chart-title {
    margin: 0 0 var(--spacing-2);
    font-size: var(--font-size-sm);
    font-weight: 600;
}

.chart-box {
    position: relative;
    height: 240px;
}

/* no data: one line instead of chart-sized boxes */
.metrics-empty {
    padding: var(--spacing-4);
    border: 1px dashed var(--border-light);
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    color: var(--text-tertiary);
    text-align: center;
}

.metrics-hint {
    margin: var(--spacing-3) 0 0;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

@media (max-width: 768px) {
    .chart-grid {
        grid-template-columns: 1fr;
    }
}
</style>
