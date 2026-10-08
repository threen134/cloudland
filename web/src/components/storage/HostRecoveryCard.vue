<script setup lang="ts">
// Recovery of a host that went down (shared-storage-design.md §11): since when it is offline, when it last confirmed
// which instances are its own (reconcile, §11.4), and the storage clusters that keep it out while its instances run
// elsewhere (fences, §11.2), which are lifted by themselves once it is back and cleaned up, or now / by hand here; and
// the storage clusters deleted while it was offline, whose leave it runs once it is back (§7.6, §8.6)
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldOff, Eraser } from 'lucide-vue-next'
import { hypervisorsApi, type Hypervisor, type HostFence } from '../../api/hypervisors'
import StatusBadge from '../base/StatusBadge.vue'
import InfoRow from '../base/InfoRow.vue'
import DeleteModal from '../modals/DeleteModal.vue'
import type { StatusVariant } from '../../utils/status'
import { formatDateTime } from '../../utils/format'
import { storageKindShort } from '../../utils/storageCluster'
import { errorMessage } from '../../utils/error'
import { useToast } from '../../composables/useToast'

const props = defineProps<{ hypervisor: Hypervisor }>()
const emit = defineEmits<{ changed: [] }>()
const { t, te } = useI18n()
const toast = useToast()

const fences = computed(() => props.hypervisor.fences || [])
const cleanups = computed(() => props.hypervisor.storage_cleanups || [])
const kindName = (k: string) => storageKindShort(t, te, k)
const liftable = computed(() => fences.value.some((f) => f.status !== 'fencing' && f.status !== 'unfencing'))

const fenceVariant = (s: string): StatusVariant => {
    if (s === 'fenced' || s === 'confirmed') return 'warning'
    if (s === 'failed' || s === 'unfence_failed') return 'error'
    return 'pending'
}
const fenceStatus = (f: HostFence) =>
    te(`storage.recovery.fenceStatus.${f.status}`) ? t(`storage.recovery.fenceStatus.${f.status}`) : f.status
const fenceMethod = (f: HostFence) =>
    te(`storage.recovery.fenceMethod.${f.method}`) ? t(`storage.recovery.fenceMethod.${f.method}`) : f.method || '-'

// unfence: lift them now; forget: they were lifted by hand, only the records go
const confirming = ref<'' | 'unfence' | 'forget'>('')
const busy = ref(false)
const confirmError = ref('')
const ask = (what: 'unfence' | 'forget') => {
    confirmError.value = ''
    confirming.value = what
}
const confirm = async () => {
    busy.value = true
    confirmError.value = ''
    try {
        await hypervisorsApi.unfenceHypervisor(props.hypervisor.uuid, confirming.value === 'forget')
        toast.success(
            confirming.value === 'forget' ? t('storage.recovery.forgotten') : t('storage.recovery.unfenceStarted')
        )
        confirming.value = ''
        emit('changed')
    } catch (err) {
        confirmError.value = errorMessage(err, t('messages.error'))
    } finally {
        busy.value = false
    }
}
</script>

<template>
    <div class="info-card card recovery-card">
        <h3 class="card-section-title">{{ t('storage.recovery.title') }}</h3>
        <div class="info-rows">
            <InfoRow v-if="hypervisor.offline_at" :label="t('storage.recovery.offlineSince')">{{
                formatDateTime(hypervisor.offline_at)
            }}</InfoRow>
            <InfoRow :label="t('storage.recovery.reconciledAt')">
                <span :title="t('storage.recovery.reconciledHint')">{{
                    hypervisor.reconciled_at
                        ? formatDateTime(hypervisor.reconciled_at)
                        : t('storage.recovery.notReconciled')
                }}</span>
            </InfoRow>
        </div>
        <template v-if="fences.length">
            <div class="fence-head">
                <span class="form-label">{{ t('storage.recovery.fences') }}</span>
                <div v-if="liftable" class="fence-actions">
                    <button
                        type="button"
                        class="btn btn-secondary btn-sm"
                        :title="t('storage.recovery.unfenceHint')"
                        @click="ask('unfence')"
                    >
                        <ShieldOff :size="14" /> {{ t('storage.recovery.unfence') }}
                    </button>
                    <button type="button" class="btn btn-ghost btn-sm" @click="ask('forget')">
                        <Eraser :size="14" /> {{ t('storage.recovery.forget') }}
                    </button>
                </div>
            </div>
            <div class="fence-table">
                <div class="fence-row fence-row-head">
                    <span>{{ t('storage.recovery.fenceCluster') }}</span>
                    <span>{{ t('dashboard.table.status') }}</span>
                    <span>{{ t('dashboard.table.type') }}</span>
                    <span>{{ t('storage.recovery.fenceTarget') }}</span>
                </div>
                <div v-for="f in fences" :key="f.id" class="fence-row">
                    <router-link
                        :to="{ name: 'storage-cluster-detail', params: { id: f.cluster_uuid } }"
                        class="resource-link"
                        >{{ f.cluster }}</router-link
                    >
                    <span><StatusBadge :variant="fenceVariant(f.status)" :label="fenceStatus(f)" /></span>
                    <span
                        >{{ fenceMethod(f) }}<template v-if="f.confirmed_by"> · {{ f.confirmed_by }}</template></span
                    >
                    <span class="mono">{{ f.target || '-' }}</span>
                    <span v-if="f.message" class="fence-message text-secondary">{{ f.message }}</span>
                </div>
            </div>
        </template>

        <template v-if="cleanups.length">
            <div class="fence-head">
                <span class="form-label" :title="t('storage.recovery.cleanupsHint')">{{
                    t('storage.recovery.cleanups')
                }}</span>
            </div>
            <p class="cleanup-hint text-secondary">{{ t('storage.recovery.cleanupsHint') }}</p>
            <div class="fence-table">
                <div class="fence-row fence-row-head">
                    <span>{{ t('storage.recovery.fenceCluster') }}</span>
                    <span>{{ t('dashboard.table.type') }}</span>
                    <span>{{ t('dashboard.table.status') }}</span>
                    <span>{{ t('storage.cluster.task') }}</span>
                </div>
                <div v-for="c in cleanups" :key="c.id" class="fence-row cleanup-row">
                    <span>{{ c.cluster }}</span>
                    <span>{{ kindName(c.kind) }}</span>
                    <span>
                        <StatusBadge
                            :variant="c.message ? 'error' : 'pending'"
                            :label="
                                c.attempts
                                    ? t('storage.recovery.cleanupAttempts', { n: c.attempts })
                                    : t('storage.recovery.cleanupWaiting')
                            "
                        />
                    </span>
                    <span>
                        <router-link
                            v-if="c.task_id"
                            :to="{ name: 'storage-task-detail', params: { id: c.task_id } }"
                            class="resource-link"
                            >{{ c.tried_at ? formatDateTime(c.tried_at) : t('storage.cluster.task') }}</router-link
                        >
                        <template v-else>-</template>
                    </span>
                    <span v-if="c.message" class="fence-message text-secondary">{{ c.message }}</span>
                </div>
            </div>
        </template>

        <DeleteModal
            :show="!!confirming"
            :title="confirming === 'forget' ? t('storage.recovery.forget') : t('storage.recovery.unfence')"
            :message="
                confirming === 'forget'
                    ? t('storage.recovery.forgetConfirm', { host: hypervisor.hostname })
                    : t('storage.recovery.unfenceConfirm', { host: hypervisor.hostname })
            "
            :confirm-label="confirming === 'forget' ? t('storage.recovery.forget') : t('storage.recovery.unfence')"
            :loading="busy"
            :error="confirmError"
            @close="confirming = ''"
            @confirm="confirm"
        />
    </div>
</template>

<style scoped>
.recovery-card {
    margin-bottom: var(--spacing-5);
}

/* As the other cards of the hypervisor page (its own scoped rule does not reach this component) */
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

.fence-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    margin-top: var(--spacing-4);
    margin-bottom: var(--spacing-2);
}

.fence-actions {
    display: flex;
    gap: var(--spacing-2);
}

.fence-table {
    display: flex;
    flex-direction: column;
    font-size: var(--font-size-sm);
}

.fence-row {
    display: grid;
    grid-template-columns: 1.4fr 1fr 1.2fr 1fr;
    gap: var(--spacing-3);
    align-items: center;
    padding: 8px 0;
    border-bottom: 1px solid var(--border-light);
}

.fence-row-head {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
}

.fence-message {
    grid-column: 1 / -1;
    font-size: var(--font-size-xs);
}

.cleanup-hint {
    margin: 0 0 var(--spacing-2);
    font-size: var(--font-size-xs);
    line-height: 1.6;
}

.mono {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
}
</style>
