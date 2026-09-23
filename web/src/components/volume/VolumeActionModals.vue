<script setup lang="ts">
// Attach / detach / resize dialogs shared by the volume list and detail pages.
// The parent calls openAttach / openDetach / openResize through a template ref and refreshes on `changed`;
// the backend only queues the node command, so the volume passes through attaching / detaching / resizing.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle, Info } from 'lucide-vue-next'
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

// Why an instance can not take the volume: its host lacks the pool of the volume, or the volume file is already on
// another host (§5.2, §5.3 of the storage plan)
const attachBlockedFor = (inst: Instance) => {
    const v = target.value
    if (!v) return ''
    if (v.hypervisor?.name && inst.hypervisor && inst.hypervisor !== v.hypervisor.name) {
        return t('storage.volumeOnOtherHost', { host: v.hypervisor.name })
    }
    if (
        v.storage_pool?.id &&
        inst.available_storage_pools &&
        !inst.available_storage_pools.includes(v.storage_pool.id)
    ) {
        return t('storage.poolNotOnHost', { pool: v.storage_pool.name || '' })
    }
    return ''
}

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

// What the guest still has to do once the disk has grown (a running VM is resized online).
// The guest names disks in the order it sees them, so a data disk's device inside the VM is not
// necessarily the platform's target (vdb, vdc ...): the steps start from lsblk and use placeholders,
// with the target only as a reference. The boot disk is the first virtio disk (vda), but which
// partition holds the root and on what file system depends on the image.
const targetRef = computed(() =>
    target.value?.instance && target.value.target
        ? t('dashboard.volumeActions.guideTargetRef', { target: target.value.target })
        : ''
)
const placeholder = computed(() => ({
    dev: t('dashboard.volumeActions.phDevice'),
    part: t('dashboard.volumeActions.phPart'),
    mount: t('dashboard.volumeActions.mountPoint'),
    lv: t('dashboard.volumeActions.phLv'),
}))
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
                <option v-for="inst in instances" :key="inst.id" :value="inst.id" :disabled="!!attachBlockedFor(inst)">
                    {{ instanceLabel(inst) }}{{ attachBlockedFor(inst) ? ` — ${attachBlockedFor(inst)}` : '' }}
                </option>
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
        <p class="modal-note icon-row">
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
        <div class="guest-steps">
            <p class="guest-steps-title icon-row">
                <Info :size="16" />
                <span>{{
                    target?.instance
                        ? t('dashboard.volumeActions.resizeOnline', { instance: instanceName })
                        : t('dashboard.volumeActions.resizeDetached')
                }}</span>
            </p>
            <dl>
                <dt>Linux</dt>
                <template v-if="target?.booting">
                    <dd>{{ t('dashboard.volumeActions.guideLinuxBootAuto') }}</dd>
                    <dd>
                        <i18n-t keypath="dashboard.volumeActions.guideLinuxBootManual" tag="span" scope="global">
                            <template #lsblk><code>lsblk</code></template>
                            <template #growpart>
                                <code
                                    >growpart /dev/vda <var>{{ placeholder.part }}</var></code
                                >
                            </template>
                            <template #ext4>
                                <code
                                    >resize2fs /dev/vda<var>{{ placeholder.part }}</var></code
                                >
                            </template>
                            <template #xfs><code>xfs_growfs /</code></template>
                        </i18n-t>
                    </dd>
                    <dd>
                        <i18n-t keypath="dashboard.volumeActions.guideLinuxLvm" tag="span" scope="global">
                            <template #cmd>
                                <code
                                    >pvresize /dev/vda<var>{{ placeholder.part }}</var></code
                                >
                            </template>
                            <template #lvextend>
                                <code
                                    >lvextend -r -l +100%FREE <var>{{ placeholder.lv }}</var></code
                                >
                            </template>
                        </i18n-t>
                    </dd>
                </template>
                <template v-else>
                    <dd>
                        <i18n-t keypath="dashboard.volumeActions.guideLinuxFind" tag="span" scope="global">
                            <template #cmd><code>lsblk</code></template>
                            <template #target>{{ targetRef }}</template>
                        </i18n-t>
                    </dd>
                    <dd>
                        <i18n-t keypath="dashboard.volumeActions.guideLinuxData" tag="span" scope="global">
                            <template #ext4>
                                <code
                                    >resize2fs /dev/<var>{{ placeholder.dev }}</var></code
                                >
                            </template>
                            <template #xfs>
                                <code
                                    >xfs_growfs <var>{{ placeholder.mount }}</var></code
                                >
                            </template>
                        </i18n-t>
                    </dd>
                    <dd>
                        <i18n-t keypath="dashboard.volumeActions.guideLinuxPartition" tag="span" scope="global">
                            <template #cmd>
                                <!-- the space goes through an expression: Vue drops whitespace that contains a line break between two elements -->
                                <code
                                    >growpart /dev/<var>{{ placeholder.dev }}</var
                                    >{{ ' ' }}<var>{{ placeholder.part }}</var></code
                                >
                            </template>
                        </i18n-t>
                    </dd>
                </template>
                <dt>Windows</dt>
                <dd>
                    {{
                        target?.booting
                            ? t('dashboard.volumeActions.guideWindowsBoot')
                            : t('dashboard.volumeActions.guideWindows')
                    }}
                </dd>
            </dl>
        </div>
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

/* Icon + text on one row: the warning note and the guidance title */
.icon-row {
    display: flex;
    gap: var(--spacing-2);
    align-items: flex-start;
}

.icon-row > svg {
    flex-shrink: 0;
    margin-top: 3px;
}

.modal-note {
    margin: 0 0 var(--spacing-3);
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: var(--font-size-sm);
    line-height: 1.5;
}

.guest-steps {
    margin: 0 0 var(--spacing-3);
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    font-size: var(--font-size-sm);
    line-height: 1.6;
    color: var(--text-secondary);
}

.guest-steps-title {
    margin: 0 0 var(--spacing-2);
    color: var(--text-primary);
}

.guest-steps-title svg {
    color: var(--primary-color);
}

.guest-steps dl {
    display: grid;
    grid-template-columns: 64px minmax(0, 1fr);
    gap: var(--spacing-1) var(--spacing-2);
    margin: 0 0 0 24px;
}

.guest-steps dt {
    font-weight: 500;
    color: var(--text-primary);
}

/* A Linux entry can take two lines; they all belong to the one dt */
.guest-steps dd {
    grid-column: 2;
    margin: 0;
}

.guest-steps code {
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
    /* break long commands only where they must, not in the middle of every word */
    overflow-wrap: anywhere;
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
