<script setup lang="ts">
// Selftest of the storage task path (shared-storage-design.md §16 S1): steps that only wait, report progress and fail
// where asked, to see commands reach the hosts, the polling, retries and aborts work, without changing a host.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import { hypervisorsApi, type Hypervisor } from '../../api/hypervisors'
import { storageClustersApi, type StorageTask } from '../../api/storageClusters'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t } = useI18n()

const hosts = ref<Hypervisor[]>([])
const hostsError = ref('')
const picked = ref<string[]>([])
const failing = ref<string[]>([])
const form = ref({ steps: 2, sleep: 10, failAttempts: 1, failStep: 0, timeout: 0, lock: 'selftest' })
const submitting = ref(false)
const submitError = ref('')

watch(
    () => props.show,
    async (show) => {
        if (!show) return
        picked.value = []
        failing.value = []
        form.value = { steps: 2, sleep: 10, failAttempts: 1, failStep: 0, timeout: 0, lock: 'selftest' }
        submitError.value = ''
        hostsError.value = ''
        try {
            const response = await hypervisorsApi.fetchHypervisors({ limit: 500 })
            hosts.value = (response.hypers || []).filter((h) => h.hostid >= 0)
        } catch (err) {
            hostsError.value = errorMessage(err, t('messages.error'))
        }
    },
    { immediate: true }
)

// Hosts that fail must take part
watch(picked, (list) => {
    failing.value = failing.value.filter((u) => list.includes(u))
})

const lockValid = computed(() => /^[a-z0-9-]{1,32}$/.test(form.value.lock))
const canSubmit = computed(() => picked.value.length > 0 && lockValid.value && !submitting.value)

const submit = async () => {
    if (!canSubmit.value) return
    submitting.value = true
    submitError.value = ''
    try {
        const f = form.value
        const task = await storageClustersApi.selftest({
            hypervisors: picked.value,
            steps: Number(f.steps),
            sleep_sec: Number(f.sleep),
            fail_hypervisors: failing.value,
            fail_attempts: failing.value.length ? Number(f.failAttempts) : 0,
            fail_step: Number(f.failStep),
            lock: f.lock,
            timeout_sec: Number(f.timeout),
        })
        emit('created', task)
    } catch (err) {
        submitError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        submitting.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="t('storage.cluster.selftestTitle')"
        size="lg"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="selftest-form">
            <p class="intro">{{ t('storage.cluster.selftestIntro') }}</p>

            <div class="form-group">
                <label class="form-label">{{ t('storage.cluster.host') }} *</label>
                <div v-if="hostsError" class="text-error">{{ hostsError }}</div>
                <div v-else class="host-grid">
                    <label v-for="h in hosts" :key="h.uuid" class="checkbox-inline">
                        <input v-model="picked" type="checkbox" :value="h.uuid" />
                        {{ h.hostname }}
                    </label>
                </div>
            </div>

            <div class="grid-2">
                <div class="form-group">
                    <label class="form-label">{{ t('storage.cluster.stepCount') }}</label>
                    <input v-model="form.steps" type="number" min="1" max="10" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.cluster.sleepSec') }}</label>
                    <input v-model="form.sleep" type="number" min="0" max="3600" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.cluster.timeoutSec') }}</label>
                    <input v-model="form.timeout" type="number" min="0" max="86400" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.cluster.lockName') }}</label>
                    <input v-model="form.lock" type="text" maxlength="32" class="form-input" />
                    <span class="form-hint" :class="{ 'text-error': !lockValid }">{{
                        t('storage.cluster.lockHint')
                    }}</span>
                </div>
            </div>

            <div v-if="picked.length" class="form-group">
                <label class="form-label">{{ t('storage.cluster.failHosts') }}</label>
                <div class="host-grid">
                    <template v-for="h in hosts" :key="h.uuid">
                        <label v-if="picked.includes(h.uuid)" class="checkbox-inline">
                            <input v-model="failing" type="checkbox" :value="h.uuid" />
                            {{ h.hostname }}
                        </label>
                    </template>
                </div>
            </div>
            <div v-if="failing.length" class="grid-2">
                <div class="form-group">
                    <label class="form-label">{{ t('storage.cluster.failAttempts') }}</label>
                    <input v-model="form.failAttempts" type="number" min="0" max="10" class="form-input" />
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.cluster.failStep') }}</label>
                    <input v-model="form.failStep" type="number" min="0" max="10" class="form-input" />
                </div>
            </div>
        </div>

        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
                {{ submitting ? t('storage.cluster.starting') : t('storage.cluster.start') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.selftest-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
}

.intro {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.host-grid {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-2) var(--spacing-5);
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.grid-2 {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--spacing-4);
}

.form-hint {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: 4px;
}

.footer-error {
    margin-right: auto;
    font-size: var(--font-size-sm);
}
</style>
