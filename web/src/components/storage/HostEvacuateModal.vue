<script setup lang="ts">
// Evacuating a host that is down (shared-storage-design.md §11.3): its instances whose disks are all in shared pools
// are defined again on other hosts with the disks as they are, once the host is fenced on the storage. The host must
// have been offline for the grace clapi gives (5 minutes), unless an admin confirms it is powered off (which also
// stands in for a fence the storage can not run). The clapi decides which instances can go; the result says what
// happens to each
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { TriangleAlert } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import {
    hypervisorsApi,
    type Hypervisor,
    type HyperEvacuateResult,
    type HyperListResponse,
} from '../../api/hypervisors'
import type { Instance } from '../../api/instances'
import { OPTION_LIST_LIMIT } from '../../api/listParams'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; hypervisor: Hypervisor | null; instances: Instance[] }>()
const emit = defineEmits<{ close: []; done: [results: HyperEvacuateResult[]] }>()
const { t, te } = useI18n()

const chosen = ref<string[]>([])
const target = ref(-1)
const confirmFenced = ref(false)
const targets = ref<Hypervisor[]>([])
const submitting = ref(false)
const submitError = ref('')
// Offline seconds and grace as clapi counted them when asked (fetchedAt, by this browser's clock): only the time
// passed since is added here, so neither the time zone nor the clock of the browser matter
const offlineSeconds = ref<number | null>(null)
const graceSeconds = ref(300)
const fetchedAt = ref(0)
const now = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | undefined

const takeOffline = (h: Hypervisor | null | undefined) => {
    offlineSeconds.value = typeof h?.offline_seconds === 'number' ? h.offline_seconds : null
    graceSeconds.value = h?.evacuate_grace_seconds || 300
    fetchedAt.value = Date.now()
    now.value = fetchedAt.value
}

watch(
    () => props.show,
    async (show) => {
        clearInterval(ticker)
        if (!show) return
        chosen.value = props.instances.map((i) => i.id)
        target.value = -1
        confirmFenced.value = false
        submitError.value = ''
        takeOffline(props.hypervisor)
        ticker = setInterval(() => (now.value = Date.now()), 15000)
        if (props.hypervisor) {
            // The detail page may have been open for a while: ask again how long it has been offline
            hypervisorsApi
                .getHypervisor(props.hypervisor.uuid)
                .then((h) => props.show && takeOffline(h))
                .catch(() => undefined)
        }
        try {
            const resp = await hypervisorsApi.fetchHypervisors({ limit: OPTION_LIST_LIMIT })
            const data = resp as HyperListResponse | Hypervisor[]
            const list = Array.isArray(data) ? data : data.hypers || []
            targets.value = list.filter((h) => h.status === 1 && h.hostid !== props.hypervisor?.hostid)
        } catch {
            targets.value = []
        }
    }
)

onBeforeUnmount(() => clearInterval(ticker))

const offlineFor = computed(() =>
    offlineSeconds.value === null ? null : offlineSeconds.value + Math.max(0, (now.value - fetchedAt.value) / 1000)
)
// Whole minutes offline, from the time cland took the host offline
const offlineMinutes = computed(() => (offlineFor.value === null ? null : Math.floor(offlineFor.value / 60)))
const graceMinutes = computed(() => Math.ceil(graceSeconds.value / 60))
const tooSoon = computed(
    () => offlineFor.value !== null && offlineFor.value < graceSeconds.value && !confirmFenced.value
)
const allChosen = computed(() => props.instances.length > 0 && chosen.value.length === props.instances.length)
const canSubmit = computed(() => chosen.value.length > 0 && !tooSoon.value && !submitting.value)

const statusText = (s: string) => (te(`dashboard.instanceStatus.${s}`) ? t(`dashboard.instanceStatus.${s}`) : s)

const toggleAll = () => {
    chosen.value = allChosen.value ? [] : props.instances.map((i) => i.id)
}

const submit = async () => {
    if (!canSubmit.value || !props.hypervisor) return
    submitting.value = true
    submitError.value = ''
    try {
        const results = await hypervisorsApi.evacuateHypervisor(props.hypervisor.uuid, {
            target_hyper: target.value,
            confirm_fenced: confirmFenced.value || undefined,
            // Every instance of the host when they all are chosen: those that came since go too
            instances: allChosen.value ? undefined : chosen.value,
        })
        emit('done', results)
    } catch (err) {
        submitError.value = errorMessage(err, t('messages.error'))
    } finally {
        submitting.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="t('storage.recovery.evacuateTitle', { host: hypervisor?.hostname || '' })"
        size="lg"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">{{ t('storage.recovery.evacuateIntro') }}</p>
            <p v-if="offlineMinutes !== null" class="offline" :class="{ warn: tooSoon }">
                {{ t('storage.recovery.evacuateOfflineFor', { min: offlineMinutes }) }}
                <template v-if="tooSoon">
                    · {{ t('storage.recovery.evacuateTooSoon', { min: graceMinutes }) }}</template
                >
            </p>

            <div class="list-head">
                <span class="form-label">{{ t('storage.recovery.evacuateInstances') }}</span>
                <label v-if="instances.length" class="checkbox-inline">
                    <input type="checkbox" :checked="allChosen" @change="toggleAll" />
                    {{ t('storage.recovery.evacuateAll') }}
                </label>
            </div>
            <div v-if="!instances.length" class="empty">{{ t('storage.recovery.evacuateNoInstances') }}</div>
            <div v-else class="instance-list">
                <label v-for="inst in instances" :key="inst.id" class="instance-row">
                    <input v-model="chosen" type="checkbox" :value="inst.id" />
                    <span class="instance-name">{{ inst.hostname }}</span>
                    <span class="sub">{{ statusText(inst.status) }}</span>
                </label>
            </div>

            <div class="form-group">
                <label class="form-label">{{ t('storage.recovery.evacuateTarget') }}</label>
                <select v-model.number="target" class="form-input">
                    <option :value="-1">{{ t('storage.recovery.evacuateTargetAuto') }}</option>
                    <option v-for="h in targets" :key="h.hostid" :value="h.hostid">{{ h.hostname }}</option>
                </select>
            </div>

            <label class="confirm-fenced">
                <input v-model="confirmFenced" type="checkbox" />
                <span>
                    <strong>{{ t('storage.recovery.evacuateConfirmFenced') }}</strong>
                    <span class="hint"
                        ><TriangleAlert :size="12" /> {{ t('storage.recovery.evacuateConfirmFencedHint') }}</span
                    >
                </span>
            </label>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
                {{ t('storage.recovery.evacuateSubmit') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.form-stack {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.intro {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.offline {
    margin: 0;
    font-size: var(--font-size-sm);
}

.offline.warn {
    color: var(--warning-dark);
}

.list-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
}

.empty {
    padding: var(--spacing-3);
    border: 1px dashed var(--border-light);
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    color: var(--text-tertiary);
}

.instance-list {
    display: flex;
    flex-direction: column;
    max-height: 220px;
    overflow-y: auto;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
}

.instance-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 12px;
    font-size: var(--font-size-sm);
    cursor: pointer;
    border-bottom: 1px solid var(--border-light);
}

.instance-row:last-child {
    border-bottom: none;
}

.instance-name {
    font-weight: 600;
}

.sub {
    margin-left: auto;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.confirm-fenced {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 12px;
    border: 1px solid var(--warning-color);
    border-radius: var(--radius-md);
    background: var(--warning-light);
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.confirm-fenced input {
    margin-top: 3px;
}

.confirm-fenced .hint {
    display: block;
    margin-top: 4px;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    line-height: 1.5;
}

.footer-error {
    margin-right: auto;
}
</style>
