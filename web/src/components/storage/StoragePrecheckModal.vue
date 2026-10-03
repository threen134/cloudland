<script setup lang="ts">
// Precheck of the hosts of a planned storage cluster (shared-storage-design.md §6.5): pick the kind, the hosts and
// their roles and the disks of the disk hosts; the hosts check themselves and nothing is installed. The kinds, their
// roles and suggested roles come from the backends (GET /storage_backends, §4.5), which also apply the role rules
// of §6.3 and refuse disks that are not free in a fresh scan, so the form only guides.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import StorageHostPicker, { type HostPick } from './StorageHostPicker.vue'
import { storageClustersApi, type StorageBackend, type StorageKind, type StorageTask } from '../../api/storageClusters'
import { storageKindText } from '../../utils/storageCluster'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t, te } = useI18n()

const backends = ref<StorageBackend[]>([])
const backendsLoading = ref(false)
const backendsError = ref('')
const kind = ref<StorageKind>('')
const backend = computed(() => backends.value.find((b) => b.kind === kind.value))
const testLayout = ref(false)
const allowUnsupported = ref(false)
const pick = ref<HostPick>({ roles: {}, disks: {} })
const picker = ref<InstanceType<typeof StorageHostPicker> | null>(null)
const submitting = ref(false)
const submitError = ref('')

// Hints are written per kind; a kind without one shows none rather than a key
const hintOf = (key: string) => (te(key) ? t(key) : '')

const reset = () => {
    testLayout.value = false
    allowUnsupported.value = false
    pick.value = { roles: {}, disks: {} }
    submitError.value = ''
}

const loadBackends = async () => {
    backendsLoading.value = true
    backendsError.value = ''
    try {
        backends.value = await storageClustersApi.backends()
        if (!backend.value) kind.value = backends.value[0]?.kind || ''
    } catch (err) {
        backendsError.value = errorMessage(err, t('messages.error'))
    } finally {
        backendsLoading.value = false
    }
}

watch(
    () => props.show,
    (show) => {
        if (!show) return
        reset()
        loadBackends()
    },
    { immediate: true }
)

const missingWipe = computed(() => picker.value?.missingWipe ?? false)
const canSubmit = computed(
    () => !!backend.value && Object.keys(pick.value.roles).length > 0 && !submitting.value && !missingWipe.value
)

const submit = async () => {
    if (!canSubmit.value || !picker.value) return
    submitting.value = true
    submitError.value = ''
    try {
        const task = await storageClustersApi.precheck({
            kind: kind.value,
            ...picker.value.payload(),
            params: { test: testLayout.value },
            allow_unsupported: allowUnsupported.value,
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
        :title="t('storage.cluster.precheckTitle')"
        size="xl"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="precheck-form">
            <p class="intro">{{ t('storage.cluster.precheckIntro') }}</p>

            <div class="form-group">
                <label class="form-label">{{ t('storage.cluster.kindLabel') }}</label>
                <div v-if="backendsLoading" class="muted-line">{{ t('storage.cluster.loadingKinds') }}</div>
                <div v-else-if="backendsError" class="text-error">{{ backendsError }}</div>
                <div v-else-if="backends.length === 0" class="muted-line">{{ t('storage.cluster.noKinds') }}</div>
                <div v-else class="kind-switch" role="radiogroup">
                    <button
                        v-for="b in backends"
                        :key="b.kind"
                        type="button"
                        class="kind-option"
                        :class="{ active: kind === b.kind }"
                        role="radio"
                        :aria-checked="kind === b.kind"
                        @click="kind = b.kind"
                    >
                        {{ storageKindText(t, te, b.kind) }}
                    </button>
                </div>
                <span v-if="hintOf(`storage.cluster.kindHints.${kind}`)" class="form-hint">{{
                    hintOf(`storage.cluster.kindHints.${kind}`)
                }}</span>
            </div>

            <div class="option-row">
                <label class="checkbox-inline" :title="t('storage.cluster.testLayoutHint')">
                    <input v-model="testLayout" type="checkbox" />
                    {{ t('storage.cluster.testLayout') }}
                </label>
                <label class="checkbox-inline" :title="t('storage.cluster.allowUnsupportedHint')">
                    <input v-model="allowUnsupported" type="checkbox" />
                    {{ t('storage.cluster.allowUnsupported') }}
                </label>
            </div>
            <span v-if="testLayout || allowUnsupported" class="form-hint warn-hint">{{
                testLayout ? t('storage.cluster.testLayoutHint') : t('storage.cluster.allowUnsupportedHint')
            }}</span>

            <StorageHostPicker v-if="show" ref="picker" v-model="pick" :backend="backend" />
        </div>

        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <span v-else-if="missingWipe" class="footer-error text-secondary">{{
                t('storage.cluster.wipeNeeded')
            }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
                {{ submitting ? t('storage.cluster.starting') : t('storage.cluster.startPrecheck') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.precheck-form {
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

.form-hint {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: 4px;
}

.warn-hint {
    margin-top: calc(-1 * var(--spacing-2));
    color: var(--warning-dark);
}

.kind-switch {
    display: inline-flex;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    overflow: hidden;
}

.kind-option {
    padding: 6px 16px;
    font-size: var(--font-size-sm);
    background: var(--bg-primary);
    color: var(--text-secondary);
    border: none;
    cursor: pointer;
}

.kind-option + .kind-option {
    border-left: 1px solid var(--border-default);
}

.kind-option.active {
    background: var(--primary-color);
    color: var(--text-inverse);
}

.option-row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-5);
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.muted-line {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    padding: var(--spacing-2) 0;
}

.footer-error {
    margin-right: auto;
    font-size: var(--font-size-sm);
}
</style>
