<script setup lang="ts">
// A storage task (shared-storage-design.md §6.2): its steps, the run of every step on every host with progress, the
// message and log tail each host sent, and for a precheck the checks of every host. A failed task waits for a retry
// or an abort; the page polls quietly while the task is going.
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
    ArrowLeft,
    ListChecks,
    RefreshCw,
    Copy,
    Check,
    RotateCcw,
    Square,
    AlertTriangle,
    Info,
    Download,
    Loader2,
} from 'lucide-vue-next'
import {
    storageClustersApi,
    TASK_LIVE_STATUSES,
    type StorageTask,
    type StorageStep,
    type StorageRun,
} from '../../api/storageClusters'
import { useToast } from '../../composables/useToast'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { formatDateTime } from '../../utils/format'
import {
    taskKindText,
    taskStatusText,
    stepStatusText,
    runStatusText,
    stepNameText,
    taskVariant,
    formatTimeout,
} from '../../utils/storageCluster'
import InfoRow from '../../components/base/InfoRow.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import StoragePrecheckResults from '../../components/storage/StoragePrecheckResults.vue'

const { t, te } = useI18n()
const route = useRoute()
const toast = useToast()
const goBack = useGoBack('storage-clusters')
const { copiedId, copyId } = useCopyId()

const id = computed(() => String(route.params.id))
const task = ref<StorageTask | null>(null)
const loading = ref(true)
const error = ref('')

const fetchTask = async (silent = false) => {
    if (!silent) loading.value = true
    try {
        task.value = await storageClustersApi.getTask(id.value)
        error.value = ''
    } catch (err) {
        if (!silent || !task.value) error.value = errorMessage(err, t('messages.error'))
    } finally {
        loading.value = false
    }
}

const live = computed(() => !!task.value && TASK_LIVE_STATUSES.includes(task.value.status))
let timer: ReturnType<typeof setInterval> | null = null
watch(live, (isLive) => {
    if (isLive && !timer) {
        timer = setInterval(() => fetchTask(true), 3000)
    } else if (!isLive && timer) {
        clearInterval(timer)
        timer = null
    }
})
watch(id, () => fetchTask())
onMounted(() => fetchTask())
onUnmounted(() => {
    if (timer) clearInterval(timer)
    unmounted = true
})

// ---- whole log of a run (§6.2.6): the host is asked for it, then the page asks clapi again until it is there ----
let unmounted = false
const logState = ref<Record<number, { busy: boolean; error: string }>>({})
const saveText = (name: string, text: string) => {
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }))
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
}
const downloadLog = async (run: StorageRun) => {
    if (!task.value || logState.value[run.id]?.busy) return
    const taskId = task.value.id
    logState.value = { ...logState.value, [run.id]: { busy: true, error: '' } }
    let error = ''
    try {
        // Up to about 90 s: the host uploads in the background, on its next free moment
        for (let i = 0; i < 45 && !unmounted; i++) {
            const log = await storageClustersApi.runLog(taskId, run.id)
            if (log.status === 'ready') {
                saveText(`storage-run-${run.id}.log`, log.content)
                if (log.truncated) toast.info(t('storage.cluster.fullLogTruncated'))
                if (log.message)
                    toast.info(t('storage.cluster.fullLogOfflineCopy', { time: formatDateTime(log.updated_at) }))
                break
            }
            if (log.status === 'error') {
                error = log.message || t('storage.cluster.fullLogFailed')
                break
            }
            await new Promise((resolve) => setTimeout(resolve, 2000))
            if (i === 44) error = t('storage.cluster.fullLogTimeout')
        }
    } catch (err) {
        error = errorMessage(err, t('storage.cluster.fullLogFailed'))
    }
    logState.value = { ...logState.value, [run.id]: { busy: false, error } }
}

const steps = computed<StorageStep[]>(() => task.value?.steps || [])

// The runs that count for a step: the last attempt on every host
const latestRuns = (step: StorageStep) => {
    const byHost = new Map<number, StorageRun>()
    for (const run of step.runs) {
        const prev = byHost.get(run.hostid)
        if (!prev || run.attempt > prev.attempt) byHost.set(run.hostid, run)
    }
    return [...byHost.values()].sort((a, b) => a.hostid - b.hostid)
}

// ---- precheck results: of a precheck task, and of the first step of a deployment or an expansion ----
const hasPrecheck = computed(() => steps.value.some((step) => step.name === 'precheck' && step.runs.length > 0))

// ---- runs table ----
const runColumns = computed<Column[]>(() => [
    { key: 'host', label: t('storage.cluster.host') },
    { key: 'attempt', label: t('storage.cluster.attemptCol'), width: '90px' },
    { key: 'status', label: t('storage.status'), width: '110px' },
    { key: 'progress', label: t('storage.cluster.progress'), width: '160px' },
    { key: 'message', label: t('storage.cluster.message') },
    { key: 'updated_at', label: t('storage.cluster.updatedAt'), hideBelow: 1280 },
])

const resultText = (run: StorageRun) => (run.result === undefined ? '' : JSON.stringify(run.result, null, 2))

// ---- retry / abort ----
const busy = ref(false)
const confirmAbort = ref(false)
const canRetry = computed(() => task.value?.status === 'failed')
const canAbort = computed(() => task.value?.status === 'running' || task.value?.status === 'failed')

const retry = async () => {
    if (!task.value) return
    busy.value = true
    try {
        await storageClustersApi.retryTask(task.value.id)
        toast.success(t('storage.cluster.retried'))
        await fetchTask(true)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.cluster.actionFailed')))
    } finally {
        busy.value = false
    }
}

const abort = async () => {
    if (!task.value) return
    busy.value = true
    try {
        await storageClustersApi.abortTask(task.value.id)
        confirmAbort.value = false
        toast.success(t('storage.cluster.abortSent'))
        await fetchTask(true)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.cluster.actionFailed')))
    } finally {
        busy.value = false
    }
}

const title = computed(() => (task.value ? taskKindText(t, te, task.value.kind) : ''))
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" />
                <span>{{ $t('actions.back') }}</span>
            </button>
        </div>

        <div v-if="loading && !task" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error && !task" class="error-container card">
            <ListChecks :size="48" style="opacity: 0.3" />
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary btn-sm" @click="fetchTask()">{{ $t('actions.retry') }}</button>
        </div>

        <template v-else-if="task">
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon"><ListChecks :size="20" /></div>
                    <div>
                        <h2 class="resource-title">
                            {{ title }}
                            <StatusBadge
                                :variant="taskVariant(task.status)"
                                :label="taskStatusText(t, te, task.status)"
                            />
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ task.id }}</span>
                            <button
                                class="copy-btn"
                                :title="$t('actions.copy')"
                                :aria-label="$t('actions.copy')"
                                @click="copyId(task.id, 'id')"
                            >
                                <Check v-if="copiedId === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <button
                        class="btn btn-secondary btn-sm btn-icon"
                        :title="$t('actions.refresh')"
                        @click="fetchTask(true)"
                    >
                        <RefreshCw :size="14" />
                    </button>
                    <button
                        v-if="canRetry"
                        class="btn btn-secondary btn-sm"
                        :disabled="busy"
                        :title="t('storage.cluster.retryHint')"
                        @click="retry"
                    >
                        <RotateCcw :size="14" /> {{ t('storage.cluster.retry') }}
                    </button>
                    <button
                        v-if="canAbort"
                        class="btn btn-danger-outline btn-sm"
                        :disabled="busy"
                        @click="confirmAbort = true"
                    >
                        <Square :size="14" /> {{ t('storage.cluster.abort') }}
                    </button>
                </div>
            </div>

            <div v-if="task.status === 'aborting'" class="notice-banner warning-banner" role="status">
                <Info :size="16" class="notice-icon" />
                <span class="notice-text">{{ t('storage.cluster.abortingNotice') }}</span>
            </div>
            <div v-else-if="task.status === 'failed'" class="notice-banner error-banner" role="status">
                <AlertTriangle :size="16" class="notice-icon" />
                <span class="notice-text">
                    <strong>{{ task.message }}</strong>
                    <br />
                    {{ t('storage.cluster.failedNotice') }}
                </span>
            </div>

            <div class="info-grid">
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                    <div class="key-value-list">
                        <InfoRow :label="t('storage.cluster.task')">{{ title }}</InfoRow>
                        <InfoRow :label="t('storage.cluster.cluster')">{{
                            task.cluster?.name || t('storage.cluster.noCluster')
                        }}</InfoRow>
                        <InfoRow :label="t('storage.cluster.currentStep')"
                            >{{ task.current_step }} / {{ steps.length }}</InfoRow
                        >
                        <InfoRow :label="t('storage.cluster.message')">{{ task.message || '-' }}</InfoRow>
                    </div>
                </div>
                <div class="card info-card">
                    <h3>{{ t('storage.cluster.timeline') }}</h3>
                    <div class="key-value-list">
                        <InfoRow :label="t('storage.cluster.creator')">{{ task.creator || '-' }}</InfoRow>
                        <InfoRow :label="t('storage.cluster.createdAt')">{{ formatDateTime(task.created_at) }}</InfoRow>
                        <InfoRow :label="t('storage.cluster.finishedAt')">{{
                            task.finished_at ? formatDateTime(task.finished_at) : '-'
                        }}</InfoRow>
                    </div>
                </div>
            </div>

            <!-- Precheck: the checks of every host -->
            <div v-if="hasPrecheck" class="card info-card">
                <h3>{{ t('storage.cluster.precheckResults') }}</h3>
                <StoragePrecheckResults :task="task" />
            </div>

            <!-- Steps and their runs -->
            <div v-for="step in steps" :key="step.seq" class="card info-card step-card">
                <h3 class="step-title">
                    {{ t('storage.cluster.stepTitle', { seq: step.seq, name: stepNameText(t, te, step.name) }) }}
                    <StatusBadge :variant="taskVariant(step.status)" :label="stepStatusText(t, te, step.status)" />
                </h3>
                <div class="step-meta">
                    <span>{{
                        step.scope === 'admin' ? t('storage.cluster.scopeAdmin') : t('storage.cluster.scopeNodes')
                    }}</span>
                    <span>{{
                        step.timeout_sec
                            ? t('storage.cluster.timeoutOf', { d: formatTimeout(t, step.timeout_sec) })
                            : t('storage.cluster.noTimeout')
                    }}</span>
                    <span v-if="step.started_at">{{ formatDateTime(step.started_at) }}</span>
                    <span v-if="step.finished_at">→ {{ formatDateTime(step.finished_at) }}</span>
                </div>
                <div v-if="step.runs.length === 0" class="muted-line">{{ t('storage.cluster.notStarted') }}</div>
                <DataTable
                    v-else
                    :columns="runColumns"
                    :rows="step.runs"
                    row-key="id"
                    expandable
                    :row-class="(run: StorageRun) => ({ superseded: !latestRuns(step).includes(run) })"
                >
                    <template #cell-host="{ row: run }">{{ run.hypervisor?.name || run.hostid }}</template>
                    <template #cell-attempt="{ row: run }"
                        >{{ t('storage.cluster.attempt', { n: run.attempt }) }}
                        <span v-if="run.dispatches > 1" class="sub" :title="t('storage.cluster.dispatchesHint')">
                            · {{ t('storage.cluster.dispatches', { n: run.dispatches }) }}
                        </span>
                    </template>
                    <template #cell-status="{ row: run }">
                        <StatusBadge :variant="taskVariant(run.status)" :label="runStatusText(t, te, run.status)" />
                    </template>
                    <template #cell-progress="{ row: run }">
                        <div class="progress">
                            <div class="progress-bar">
                                <div
                                    class="progress-fill"
                                    :class="`fill-${run.status}`"
                                    :style="{ width: `${run.progress}%` }"
                                ></div>
                            </div>
                            <span class="progress-text">{{ run.progress }}%</span>
                        </div>
                    </template>
                    <template #cell-message="{ row: run }">
                        <span class="message-cell" :title="run.message">{{ run.message || '-' }}</span>
                    </template>
                    <template #cell-updated_at="{ row: run }">{{ formatDateTime(run.updated_at) }}</template>
                    <template #expanded="{ row: run }">
                        <div class="run-detail">
                            <div class="run-detail-title log-title">
                                {{ t('storage.cluster.log') }}
                                <button
                                    class="btn btn-ghost btn-xs"
                                    :disabled="logState[run.id]?.busy"
                                    @click="downloadLog(run)"
                                >
                                    <Loader2 v-if="logState[run.id]?.busy" :size="12" class="spinning" />
                                    <Download v-else :size="12" />
                                    {{
                                        logState[run.id]?.busy
                                            ? t('storage.cluster.fullLogFetching')
                                            : t('storage.cluster.fullLog')
                                    }}
                                </button>
                                <span v-if="logState[run.id]?.error" class="text-error log-error">{{
                                    logState[run.id].error
                                }}</span>
                            </div>
                            <pre v-if="run.log_tail" class="log">{{ run.log_tail }}</pre>
                            <div v-else class="muted-line">{{ t('storage.cluster.noLog') }}</div>
                            <template v-if="resultText(run)">
                                <div class="run-detail-title">{{ t('storage.cluster.result') }}</div>
                                <pre class="log">{{ resultText(run) }}</pre>
                            </template>
                        </div>
                    </template>
                </DataTable>
            </div>
        </template>

        <DeleteModal
            :show="confirmAbort"
            :title="t('storage.cluster.abortTitle')"
            :message="t('storage.cluster.abortMessage', { name: title })"
            :confirm-label="t('storage.cluster.abort')"
            :loading="busy"
            @close="confirmAbort = false"
            @confirm="abort"
        />
    </div>
</template>

<style scoped>
.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-4);
}

.info-card {
    padding: var(--spacing-5);
    margin-bottom: var(--spacing-4);
}

.info-grid .info-card {
    margin-bottom: 0;
}

.info-card h3 {
    margin: 0 0 var(--spacing-4);
    font-size: 1rem;
    font-weight: 600;
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.notice-banner {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-4);
    padding: var(--spacing-3) var(--spacing-4);
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    line-height: 1.6;
}

.notice-icon {
    flex-shrink: 0;
    margin-top: 3px;
}

.notice-text {
    flex: 1;
    min-width: 0;
}

.error-banner {
    background: var(--error-light);
    color: var(--error-dark);
}

.warning-banner {
    background: var(--warning-light);
    color: var(--warning-dark);
}

.step-title {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.step-meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-4);
    margin: calc(-1 * var(--spacing-2)) 0 var(--spacing-3);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.muted-line {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    padding: var(--spacing-2) 0;
}

.sub {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.progress {
    display: flex;
    align-items: center;
    gap: 8px;
}

.progress-bar {
    flex: 1;
    height: 6px;
    border-radius: 3px;
    background: var(--border-light);
    overflow: hidden;
}

.progress-fill {
    height: 100%;
    background: var(--primary-color);
    transition: width 0.3s;
}

.progress-fill.fill-succeeded {
    background: var(--success-color);
}

.progress-fill.fill-failed {
    background: var(--error-color);
}

.progress-text {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    width: 36px;
    text-align: right;
}

.message-cell {
    display: inline-block;
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
}

:deep(tr.superseded) {
    opacity: 0.55;
}

.run-detail {
    padding: var(--spacing-2) var(--spacing-3);
}

.run-detail-title {
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-secondary);
    margin: var(--spacing-2) 0 4px;
}

.log-title {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-2);
}

.log-error {
    font-weight: normal;
}

.log {
    margin: 0;
    max-height: 360px;
    overflow: auto;
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    line-height: 1.5;
    white-space: pre-wrap;
    word-break: break-all;
}
</style>
