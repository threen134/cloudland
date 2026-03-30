import { ref, onMounted, onUnmounted } from 'vue'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler } from 'chart.js'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler)

export const TIME_RANGES = [
    { label: '1h', value: '1h', step: '60s' },
    { label: '6h', value: '6h', step: '5m' },
    { label: '24h', value: '24h', step: '10m' },
    { label: '7d', value: '7d', step: '1h' },
    { label: '30d', value: '30d', step: '2h' },
]

export const CHART_OPTIONS = {
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

export function useMonitoring(fetchCallback: () => Promise<void>) {
    const timeRange = ref('1h')
    const step = ref('60s')
    const customStart = ref('')
    const customEnd = ref('')
    const loading = ref(false)
    const error = ref('')

    const formatTimestamp = (ts: string) => {
        const d = new Date(parseInt(ts) * 1000)
        if (timeRange.value === '1h' || timeRange.value === '6h') {
            return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
        }
        return d.toLocaleDateString([], { month: '2-digit', day: '2-digit' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    }

    const getDefaultCustomDates = () => {
        const end = new Date()
        const start = new Date(end.getTime() - 3600 * 1000)
        const pad = (n: number) => n.toString().padStart(2, '0')
        const format = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
        return { start: format(start), end: format(end) }
    }

    /** Calculate start/end unix timestamps from the current time range state */
    const getTimeRange = (): { startTs: number, endTs: number } | null => {
        let startTs: number
        let endTs: number = Math.floor(Date.now() / 1000)

        if (timeRange.value === 'custom') {
            if (!customStart.value || !customEnd.value) return null
            startTs = Math.floor(new Date(customStart.value).getTime() / 1000)
            endTs = Math.floor(new Date(customEnd.value).getTime() / 1000)

            const duration = endTs - startTs
            if (duration <= 3600) step.value = '60s'
            else if (duration <= 86400) step.value = '10m'
            else if (duration <= 7 * 86400) step.value = '1h'
            else step.value = '6h'
        } else {
            const offsets: Record<string, number> = {
                '1h': 3600, '6h': 6 * 3600, '24h': 24 * 3600,
                '7d': 7 * 86400, '30d': 30 * 86400,
            }
            startTs = endTs - (offsets[timeRange.value] || 3600)
        }

        return { startTs, endTs }
    }

    const setRange = (range: { value: string, step: string }) => {
        timeRange.value = range.value
        step.value = range.step
        fetchCallback()
    }

    const toggleCustom = () => {
        if (timeRange.value !== 'custom') {
            const { start, end } = getDefaultCustomDates()
            customStart.value = start
            customEnd.value = end
            timeRange.value = 'custom'
            fetchCallback()
        }
    }

    let refreshInterval: ReturnType<typeof setInterval> | null = null
    onMounted(() => {
        fetchCallback()
        refreshInterval = setInterval(() => {
            if (timeRange.value === '1h') fetchCallback()
        }, 60000)
    })
    onUnmounted(() => {
        if (refreshInterval) clearInterval(refreshInterval)
    })

    return {
        timeRange, step, customStart, customEnd, loading, error,
        formatTimestamp, getTimeRange, setRange, toggleCustom,
    }
}
