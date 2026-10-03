<script setup lang="ts">
// Traffic history of a VPN gateway: site connections or WireGuard clients, one full-width chart at a time
// (the switch only when the gateway offers both). Rates come from GET /vpn_gateways/:id/traffic. The site
// view can split every connection into its tunnels (by=tunnel), optionally for one connection only.
import { computed, ref, shallowRef, watch } from 'vue'
import type { ChartData, ChartOptions } from 'chart.js'
import { Line } from 'vue-chartjs'
import { useI18n } from 'vue-i18n'
import { ChartLine } from 'lucide-vue-next'
import MonitoringPanel from '../monitoring/MonitoringPanel.vue'
import { useMonitoring, CHART_OPTIONS } from '../../composables/useMonitoring'
import { vpnGatewaysApi, type VpnConnection, type VpnTrafficResponse, type VpnTrafficSeries } from '../../api/vpn'
import { cssVar } from '../../utils/cssVar'
import { errorMessage } from '../../utils/error'

const props = defineProps<{
    gatewayId: string
    ipsecEnabled: boolean
    clientEnabled: boolean
    /** Connections of the gateway, for the names of the per-tunnel series */
    connections?: VpnConnection[]
}>()
const { t } = useI18n()

const traffic = shallowRef<VpnTrafficResponse | null>(null)
// Site view: one series per tunnel instead of one per connection
const byTunnel = ref(false)
// Whether the data on screen was queried per tunnel: it lags byTunnel until the new answer arrives
const trafficByTunnel = ref(false)
// Per tunnel: the connection whose tunnels are drawn, '' for all of them
const tunnelConnection = ref('')

// Only the latest request may write: range clicks, Apply and the auto refresh can overlap, and a slow 30d
// query answering after a quick 1h one would otherwise replace it
let generation = 0
const fetchData = async () => {
    const range = getTimeRange()
    if (!range) {
        error.value = t('dashboard.monitoring.selectDatesError')
        return
    }
    const mine = ++generation
    const perTunnel = byTunnel.value
    loading.value = true
    error.value = ''
    try {
        const data = await vpnGatewaysApi.traffic(props.gatewayId, {
            start: range.startTs,
            end: range.endTs,
            step: step.value,
            by: perTunnel ? 'tunnel' : undefined,
        })
        if (mine === generation) {
            traffic.value = data
            trafficByTunnel.value = perTunnel
        }
    } catch (err) {
        if (mine !== generation) return
        console.error('Failed to fetch VPN traffic:', err)
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

watch(byTunnel, () => fetchData())

// bits per second, scaled to the largest value of a chart
const UNITS = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps']
const unitFor = (values: Array<number | null>) => {
    const max = Math.max(0, ...values.map((v) => v ?? 0))
    let i = 0
    while (max >= 1000 ** (i + 1) && i < UNITS.length - 1) i++
    return { name: UNITS[i], div: 1000 ** i }
}
const formatRate = (bps: number) => {
    const u = unitFor([bps])
    return `${(bps / u.div).toFixed(bps >= 1000 ? 2 : 0)} ${u.name}`
}
// The current rate is the newest point, or the one before when the newest has no sample yet (its rate
// window may still be filling); anything older is not current. In and out come from the same point.
const latestPoint = (ins: Array<number | null>, outs: Array<number | null>) => {
    for (let i = ins.length - 1; i >= Math.max(0, ins.length - 2); i--) {
        if (ins[i] !== null || outs[i] !== null) return { in: ins[i], out: outs[i] }
    }
    return null
}
const sumSeries = (series: Array<Array<number | null>>, length: number) =>
    Array.from({ length }, (_, i) => {
        let total: number | null = null
        for (const s of series) {
            const v = s[i]
            if (v !== null && v !== undefined) total = (total ?? 0) + v
        }
        return total
    })

// One colour pair per connection, ↓ (in) first and ↑ (out) second: the legend shows dots, so the two
// directions of a connection must differ in colour, not only in the dashing of the line
const PALETTE: Array<[string, string]> = [
    ['--success-color', '--primary-color'],
    ['--accent-teal', '--accent-purple'],
    ['--warning-color', '--error-color'],
    ['--gray-500', '--warning-dark'],
]

const chartOptions = (unit: string): ChartOptions<'line'> => ({
    ...(CHART_OPTIONS as ChartOptions<'line'>),
    spanGaps: false,
    plugins: {
        ...(CHART_OPTIONS.plugins as ChartOptions<'line'>['plugins']),
        tooltip: {
            mode: 'index',
            intersect: false,
            callbacks: {
                label: (ctx) =>
                    `${ctx.dataset.label}: ${ctx.parsed.y === null ? '-' : ctx.parsed.y.toFixed(2)} ${unit}`,
            },
        },
    },
    scales: {
        ...(CHART_OPTIONS.scales as ChartOptions<'line'>['scales']),
        y: {
            beginAtZero: true,
            grid: { color: cssVar('--border-light') },
            ticks: { font: { size: 10 } },
            title: { display: true, text: unit, font: { size: 10 } },
        },
    },
})

const labels = computed(() => (traffic.value?.timestamps || []).map((ts) => formatTimestamp(String(ts))))

// Which views exist: a gateway only shows the kinds of access it offers, and the switch only appears
// when it offers both
type View = 'site' | 'client'
const views = computed<View[]>(() => {
    const list: View[] = []
    if (props.ipsecEnabled) list.push('site')
    if (props.clientEnabled) list.push('client')
    return list.length ? list : ['site']
})
const selected = ref<View>('site')
const activeView = computed<View>(() => (views.value.includes(selected.value) ? selected.value : views.value[0]))

type Series = { name: string; in: Array<number | null>; out: Array<number | null> }

// "<connection> · tunnel N"; the connection name is taken from the gateway, the series name ("<name> / tN")
// is the fallback
const connectionName = (id?: string) => props.connections?.find((c) => c.id === id)?.name
const seriesName = (s: VpnTrafficSeries) =>
    s.connection_id && s.slot
        ? t('dashboard.vpnGateway.tunnelSeries', {
              conn: connectionName(s.connection_id) || s.name.replace(/ \/ t\d+$/, ''),
              n: s.slot,
          })
        : s.name

// Connections present in the per-tunnel answer, for the connection filter
const tunnelConnections = computed(() => {
    if (!trafficByTunnel.value) return []
    const seen = new Map<string, string>()
    for (const s of traffic.value?.connections || []) {
        if (s.connection_id && !seen.has(s.connection_id)) {
            seen.set(s.connection_id, connectionName(s.connection_id) || s.name.replace(/ \/ t\d+$/, ''))
        }
    }
    return [...seen].map(([id, name]) => ({ id, name }))
})
// A connection that is gone from the answer falls back to all of them
const selectedConnection = computed(() =>
    tunnelConnections.value.some((c) => c.id === tunnelConnection.value) ? tunnelConnection.value : ''
)

// ↓ in (solid) and ↑ out (dashed), a colour pair per series
const lineChart = (series: Series[]) => {
    const n = traffic.value?.timestamps.length || 0
    const unit = unitFor(series.flatMap((s) => [...s.in, ...s.out]))
    const scale = (v: Array<number | null>) => v.map((x) => (x === null ? null : x / unit.div))
    const datasets: ChartData<'line', Array<number | null>>['datasets'] = []
    series.forEach((s, i) => {
        const [inVar, outVar] = PALETTE[i % PALETTE.length]
        const inColor = cssVar(inVar)
        const outColor = cssVar(outVar)
        datasets.push({
            label: `${s.name} ↓`,
            data: scale(s.in),
            borderColor: inColor,
            backgroundColor: inColor,
            fill: false,
        })
        datasets.push({
            label: `${s.name} ↑`,
            data: scale(s.out),
            borderColor: outColor,
            backgroundColor: outColor,
            borderDash: [5, 4],
            fill: false,
        })
    })
    return {
        data: { labels: labels.value, datasets } as ChartData<'line', Array<number | null>>,
        options: chartOptions(unit.name),
        latest: latestPoint(
            sumSeries(
                series.map((s) => s.in),
                n
            ),
            sumSeries(
                series.map((s) => s.out),
                n
            )
        ),
    }
}

// One line pair per client as long as every client gets its own colours; beyond that the chart shows
// their sum (site connections are usually few and keep one pair each, the colours repeat past the palette)
const MAX_CLIENT_LINES = PALETTE.length

const chart = computed(() => {
    const data = traffic.value
    if (!data) return null
    if (activeView.value === 'site') {
        const series = selectedConnection.value
            ? data.connections.filter((s) => s.connection_id === selectedConnection.value)
            : data.connections
        return series.length ? lineChart(series.map((s) => ({ ...s, name: seriesName(s) }))) : null
    }
    if (!data.clients.length) return null
    if (data.clients.length <= MAX_CLIENT_LINES) return lineChart(data.clients)
    const n = data.timestamps.length
    return lineChart([
        {
            name: t('dashboard.vpnGateway.trafficTotal'),
            in: sumSeries(
                data.clients.map((c) => c.in),
                n
            ),
            out: sumSeries(
                data.clients.map((c) => c.out),
                n
            ),
        },
    ])
})

const currentText = computed(() => {
    const p = chart.value?.latest
    return p ? `↓ ${formatRate(p.in ?? 0)} · ↑ ${formatRate(p.out ?? 0)}` : ''
})
const viewLabel = (v: View) =>
    v === 'site' ? t('dashboard.vpnGateway.siteConnections') : t('dashboard.vpnGateway.clientsTab')
const chartTitle = computed(() =>
    activeView.value === 'site' ? t('dashboard.vpnGateway.siteTraffic') : t('dashboard.vpnGateway.clientTraffic')
)
const emptyText = computed(() =>
    activeView.value === 'site' ? t('dashboard.vpnGateway.noSiteTraffic') : t('dashboard.vpnGateway.noClientTraffic')
)
</script>

<template>
    <MonitoringPanel
        v-model:custom-start="customStart"
        v-model:custom-end="customEnd"
        :title="t('dashboard.vpnGateway.trafficTitle')"
        :icon="ChartLine"
        :time-range="timeRange"
        :loading="loading"
        :error="error"
        @set-range="setRange"
        @toggle-custom="toggleCustom"
        @refresh="fetchData"
    >
        <!-- What to draw sits right above the chart; the time range in the panel header applies to both -->
        <div class="traffic-toolbar">
            <div v-if="views.length > 1" class="view-switch" role="tablist">
                <button
                    v-for="v in views"
                    :key="v"
                    class="btn btn-ghost btn-xs"
                    :class="{ active: activeView === v }"
                    role="tab"
                    :aria-selected="activeView === v"
                    @click="selected = v"
                >
                    {{ viewLabel(v) }}
                </button>
            </div>
            <h4 v-else class="traffic-title">{{ chartTitle }}</h4>
            <div class="toolbar-right">
                <!-- Site view: split the connections into their tunnels, optionally one connection only -->
                <template v-if="activeView === 'site'">
                    <label class="tunnel-toggle">
                        <input v-model="byTunnel" type="checkbox" />
                        {{ t('dashboard.vpnGateway.trafficByTunnel') }}
                    </label>
                    <select
                        v-if="byTunnel && tunnelConnections.length > 1"
                        v-model="tunnelConnection"
                        class="form-input tunnel-filter"
                        :aria-label="t('dashboard.vpnGateway.connections')"
                    >
                        <option value="">{{ t('dashboard.vpnGateway.trafficAllConnections') }}</option>
                        <option v-for="c in tunnelConnections" :key="c.id" :value="c.id">{{ c.name }}</option>
                    </select>
                </template>
                <span class="current-value">{{ currentText }}</span>
            </div>
        </div>

        <div v-if="chart" class="traffic-chart">
            <Line :data="chart.data" :options="chart.options" />
        </div>
        <div v-else class="traffic-empty">{{ loading ? t('messages.loading') : emptyText }}</div>

        <p class="traffic-hint">{{ t('dashboard.vpnGateway.trafficHint') }}</p>
    </MonitoringPanel>
</template>

<style scoped>
.traffic-toolbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-3);
}

.toolbar-right {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-3);
    margin-left: auto;
}

.tunnel-toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-1);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    cursor: pointer;
    white-space: nowrap;
}

/* Compact select next to the toggle */
.tunnel-filter {
    width: auto;
    max-width: 200px;
    padding: 2px 8px;
    font-size: var(--font-size-xs);
    border-radius: var(--radius-md);
}

.current-value {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    white-space: nowrap;
}

.traffic-title {
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 600;
}

/* same look as the time range selector of the panel header */
.view-switch {
    display: flex;
    padding: 2px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
}

.view-switch .btn {
    height: 24px;
    min-height: 24px;
    padding: 2px 12px;
    font-size: var(--font-size-xs);
}

.view-switch .btn.active {
    background: var(--bg-primary);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
}

.traffic-chart {
    position: relative;
    height: 280px;
}

/* no data: one line instead of an empty chart-sized box */
.traffic-empty {
    padding: var(--spacing-4);
    border: 1px dashed var(--border-light);
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    color: var(--text-tertiary);
    text-align: center;
}

.traffic-hint {
    margin: var(--spacing-3) 0 0;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}
</style>
