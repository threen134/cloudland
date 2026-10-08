<script setup lang="ts">
// The orphan report of a shared pool (shared-storage-design.md §16 S5): a host that reaches the pool lists its files
// or RBD images, and CloudLand shows those it has no record of. Nothing is removed here; an admin looks at each.
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ScanSearch, Loader2 } from 'lucide-vue-next'
import StatusBadge from '../base/StatusBadge.vue'
import { storagePoolsApi, type StoragePoolReconcile } from '../../api/storagePools'
import { formatBytes, formatDateTime } from '../../utils/format'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ poolId: string }>()
const { t, te } = useI18n()

const report = ref<StoragePoolReconcile | null>(null)
const starting = ref(false)
const error = ref('')
let timer: ReturnType<typeof setTimeout> | null = null
let unmounted = false

const load = async () => {
    try {
        report.value = await storagePoolsApi.reconcileReport(props.poolId)
        error.value = ''
    } catch (err) {
        error.value = errorMessage(err, t('messages.error'))
    }
    if (timer) clearTimeout(timer)
    // The host answers in the background; look again while it works
    if (!unmounted && report.value?.status === 'running') timer = setTimeout(load, 3000)
}

const start = async () => {
    starting.value = true
    error.value = ''
    try {
        report.value = await storagePoolsApi.reconcile(props.poolId)
        load()
    } catch (err) {
        error.value = errorMessage(err, t('storage.submitFailed'))
    } finally {
        starting.value = false
    }
}

onMounted(load)
onUnmounted(() => {
    unmounted = true
    if (timer) clearTimeout(timer)
})

const running = computed(() => report.value?.status === 'running')
const kindText = (k: string) => (te(`storage.reconcile.kinds.${k}`) ? t(`storage.reconcile.kinds.${k}`) : k)
const statusText = (s: string) => (te(`storage.reconcile.status.${s}`) ? t(`storage.reconcile.status.${s}`) : s)
const statusVariant = (s: string) => (s === 'done' ? 'success' : s === 'error' ? 'error' : 'pending')
const mtime = (s: number) => (s ? formatDateTime(s * 1000) : '-')
</script>

<template>
    <div class="info-card card">
        <div class="card-head">
            <h3 class="card-section-title">{{ t('storage.reconcile.title') }}</h3>
            <button class="btn btn-secondary btn-sm" :disabled="starting || running" @click="start">
                <Loader2 v-if="starting || running" :size="14" class="spinning" />
                <ScanSearch v-else :size="14" />
                {{ t('storage.reconcile.start') }}
            </button>
        </div>
        <p class="hint">{{ t('storage.reconcile.hint') }}</p>
        <div v-if="error" class="text-error">{{ error }}</div>
        <template v-if="report?.status">
            <div class="summary">
                <StatusBadge :variant="statusVariant(report.status)" :label="statusText(report.status)" />
                <span v-if="report.hypervisor?.name" class="sub">{{
                    t('storage.reconcile.listedBy', { host: report.hypervisor.name })
                }}</span>
                <span v-if="report.checked_at" class="sub">{{ formatDateTime(report.checked_at) }}</span>
                <span v-if="report.status === 'done'" class="sub">{{
                    t('storage.reconcile.counts', { objects: report.objects, orphans: report.orphans.length })
                }}</span>
            </div>
            <div v-if="report.error" class="text-error">{{ report.error }}</div>
            <div v-if="report.truncated" class="text-secondary sub">{{ t('storage.reconcile.truncated') }}</div>
            <table v-if="report.status === 'done' && report.orphans.length" class="data-table">
                <thead>
                    <tr>
                        <th>{{ t('storage.reconcile.kind') }}</th>
                        <th>{{ t('storage.reconcile.name') }}</th>
                        <th>{{ t('storage.size') }}</th>
                        <th>{{ t('storage.reconcile.modified') }}</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-for="o in report.orphans" :key="o.name">
                        <td>{{ kindText(o.kind) }}</td>
                        <td class="mono">{{ o.name }}</td>
                        <td>{{ formatBytes(o.size) }}</td>
                        <td>{{ mtime(o.mtime || 0) }}</td>
                    </tr>
                </tbody>
            </table>
            <div v-else-if="report.status === 'done'" class="text-secondary">{{ t('storage.reconcile.none') }}</div>
        </template>
    </div>
</template>

<style scoped>
.card-section-title {
    margin: 0 0 var(--spacing-4) 0;
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-semibold);
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding-bottom: var(--spacing-3);
    border-bottom: 1px solid var(--border-light);
}

.card-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-2);
}

.card-head .card-section-title {
    flex: 1;
}

.hint {
    margin: 0 0 var(--spacing-3);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    line-height: 1.6;
}

.summary {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: var(--spacing-2);
}

.sub {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.mono {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    word-break: break-all;
}
</style>
