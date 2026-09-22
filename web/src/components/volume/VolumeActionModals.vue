<script setup lang="ts">
// Attach / detach / resize dialogs shared by the volume list and detail pages.
// The parent calls openAttach / openDetach / openResize through a template ref and refreshes on `changed`;
// the backend only queues the node command, so the volume passes through attaching / detaching / resizing.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import { volumesApi, type Volume } from '../../api/volumes'
import { instancesApi, type Instance } from '../../api/instances'
import { useToast } from '../../composables/useToast'
import { errorMessage } from '../../utils/error'
import { quotaErrorMessage } from '../../utils/quotaError'
import { formatDisk } from '../../utils/format'
import { primaryIp } from '../../utils/instance'

const emit = defineEmits<{ changed: [volumeId: string] }>()

const { t, te } = useI18n()
const toast = useToast()

const target = ref<Volume | null>(null)
const mode = ref<'attach' | 'detach' | 'resize' | null>(null)
const submitting = ref(false)
const error = ref('')

const open = (m: 'attach' | 'detach' | 'resize', volume: Volume) => {
    openSeq++
    target.value = volume
    mode.value = m
    error.value = ''
}
const close = () => {
    if (submitting.value) return
    openSeq++
    mode.value = null
}

const volumeName = computed(() => target.value?.name || target.value?.id || '')
const instanceName = computed(() => target.value?.instance?.name || target.value?.instance?.id || '')

// --- Attach ---
// The backend refuses paused / rescuing instances; transitional states would fail on the node
const ATTACHABLE_STATUSES = ['running', 'shut_off']
const instances = ref<Instance[]>([])
const instancesLoading = ref(false)
const selectedInstanceId = ref('')

const instanceLabel = (inst: Instance) => [inst.hostname, primaryIp(inst), inst.hypervisor].filter(Boolean).join(' · ')

// Bumped on every open: a slow fetch from an earlier open must not write into the dialog shown now
let openSeq = 0

const openAttach = async (volume: Volume) => {
    open('attach', volume)
    const seq = openSeq
    selectedInstanceId.value = ''
    instances.value = []
    instancesLoading.value = true
    try {
        // The list endpoint defaults to 50 rows
        const res = await instancesApi.fetchInstances({ limit: 500 })
        if (seq !== openSeq) return
        instances.value = (res.instances || []).filter((inst) =>
            ATTACHABLE_STATUSES.includes(inst.status?.toLowerCase())
        )
    } catch (err) {
        if (seq === openSeq) error.value = errorMessage(err, t('messages.error'))
    } finally {
        if (seq === openSeq) instancesLoading.value = false
    }
}

const submitAttach = async () => {
    if (!target.value || !selectedInstanceId.value) return
    await submit(
        () => volumesApi.attach(target.value!.id, selectedInstanceId.value),
        'dashboard.volumeActions.attachSubmitted'
    )
}

// --- Detach ---
const openDetach = (volume: Volume) => open('detach', volume)
const submitDetach = async () => {
    if (!target.value) return
    await submit(() => volumesApi.detach(target.value!.id), 'dashboard.volumeActions.detachSubmitted')
}

// --- Resize ---
const newSize = ref(0)
const openResize = (volume: Volume) => {
    open('resize', volume)
    newSize.value = volume.size + 10
}
const resizeTooSmall = computed(() => !!target.value && !(newSize.value > target.value.size))
const submitResize = async () => {
    if (!target.value || resizeTooSmall.value) return
    await submit(() => volumesApi.resize(target.value!.id, newSize.value), 'dashboard.volumeActions.resizeSubmitted')
}

const submit = async (request: () => Promise<unknown>, successKey: string) => {
    submitting.value = true
    error.value = ''
    try {
        await request()
        const id = target.value!.id
        submitting.value = false
        mode.value = null
        toast.success(t(successKey))
        emit('changed', id)
    } catch (err) {
        // Resize is metered against the disk quota by the gateway (429 quota_exceeded)
        error.value = quotaErrorMessage(err, t, te) || errorMessage(err, t('messages.error'))
    } finally {
        submitting.value = false
    }
}

defineExpose({ openAttach, openDetach, openResize })
</script>

<template>
    <!-- Attach -->
    <BaseModal
        :show="mode === 'attach'"
        :title="t('dashboard.volumeActions.attachTitle', { name: volumeName })"
        :loading="submitting"
        form
        @close="close"
        @submit="submitAttach"
    >
        <div class="form-group">
            <label class="form-label" for="volume-attach-instance">{{ t('dashboard.volumeActions.instance') }}</label>
            <select
                id="volume-attach-instance"
                v-model="selectedInstanceId"
                class="form-input"
                :disabled="instancesLoading || !instances.length"
            >
                <option value="" disabled>
                    {{
                        instancesLoading
                            ? t('dashboard.volumeActions.loadingInstances')
                            : instances.length
                              ? t('dashboard.volumeActions.selectInstance')
                              : t('dashboard.volumeActions.noInstances')
                    }}
                </option>
                <option v-for="inst in instances" :key="inst.id" :value="inst.id">{{ instanceLabel(inst) }}</option>
            </select>
            <p class="form-hint">{{ t('dashboard.volumeActions.attachHint') }}</p>
        </div>
        <div v-if="error" class="modal-error">{{ error }}</div>

        <template #footer>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="close">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="submitting || !selectedInstanceId">
                <span v-if="submitting" class="loading-spinner btn-spinner"></span>
                {{ t('actions.attach') }}
            </button>
        </template>
    </BaseModal>

    <!-- Detach -->
    <BaseModal
        :show="mode === 'detach'"
        :title="t('dashboard.volumeActions.detachTitle', { name: volumeName })"
        :loading="submitting"
        form
        @close="close"
        @submit="submitDetach"
    >
        <p class="modal-text">
            {{ t('dashboard.volumeActions.detachConfirm', { name: volumeName, instance: instanceName }) }}
        </p>
        <p class="modal-note">
            <AlertTriangle :size="16" />
            <span>{{ t('dashboard.volumeActions.detachHint') }}</span>
        </p>
        <div v-if="error" class="modal-error">{{ error }}</div>

        <template #footer>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="close">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="submitting">
                <span v-if="submitting" class="loading-spinner btn-spinner"></span>
                {{ t('actions.detach') }}
            </button>
        </template>
    </BaseModal>

    <!-- Resize -->
    <BaseModal
        :show="mode === 'resize'"
        :title="t('dashboard.volumeActions.resizeTitle', { name: volumeName })"
        :loading="submitting"
        form
        @close="close"
        @submit="submitResize"
    >
        <div class="form-group">
            <span class="form-label">{{ t('dashboard.volumeActions.currentSize') }}</span>
            <div class="current-size">{{ formatDisk(target?.size) }}</div>
        </div>
        <div class="form-group">
            <label class="form-label" for="volume-resize-size">{{ t('dashboard.volumeActions.newSize') }}</label>
            <input
                id="volume-resize-size"
                v-model.number="newSize"
                type="number"
                :min="(target?.size ?? 0) + 1"
                step="1"
                :class="['form-input', { 'input-error': resizeTooSmall }]"
            />
            <p v-if="resizeTooSmall" class="form-hint form-hint-error">
                {{ t('dashboard.volumeActions.resizeTooSmall', { size: target?.size }) }}
            </p>
            <p v-else class="form-hint">{{ t('dashboard.volumeActions.resizeHint') }}</p>
        </div>
        <p v-if="target?.instance" class="modal-note">
            <AlertTriangle :size="16" />
            <span>{{ t('dashboard.volumeActions.resizeAttachedWarning', { instance: instanceName }) }}</span>
        </p>
        <div v-if="error" class="modal-error">{{ error }}</div>

        <template #footer>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="close">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="submitting || resizeTooSmall">
                <span v-if="submitting" class="loading-spinner btn-spinner"></span>
                {{ t('actions.resize') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.form-hint {
    margin: var(--spacing-2) 0 0;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    line-height: 1.5;
}

.form-hint-error {
    color: var(--error-color);
}

.current-size {
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    font-weight: 500;
}

.modal-text {
    margin: 0 0 var(--spacing-3);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    line-height: 1.6;
}

.modal-note {
    display: flex;
    gap: var(--spacing-2);
    align-items: flex-start;
    margin: 0 0 var(--spacing-3);
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: var(--font-size-sm);
    line-height: 1.5;
}

.modal-note svg {
    flex-shrink: 0;
    margin-top: 2px;
}

.modal-error {
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--error-light);
    color: var(--error-dark);
    font-size: var(--font-size-sm);
}

.btn-spinner {
    width: 16px;
    height: 16px;
    border-width: 2px;
}
</style>
