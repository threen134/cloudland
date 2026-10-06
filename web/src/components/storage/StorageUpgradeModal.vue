<script setup lang="ts">
// Upgrading a managed storage cluster (shared-storage-design.md §7.7, §8.7). GPFS: a package of a newer release from
// the package repository, rolled out one host after the other; or, once every host runs it, the irreversible step that
// raises the cluster and its file systems to it. Ceph: the release the hosts install from their distribution, then
// cephadm upgrades the daemons; an image only for a cluster on an image of its own
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { TriangleAlert } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import {
    storageClustersApi,
    type StorageCluster,
    type StorageCapabilities,
    type StorageTask,
} from '../../api/storageClusters'
import { storagePackagesApi, type StoragePackage } from '../../api/storagePackages'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; cluster: StorageCluster | null; caps?: StorageCapabilities }>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t } = useI18n()

const packages = ref<StoragePackage[]>([])
const chosen = ref('')
const finalize = ref(false)
const image = ref('')
const submitting = ref(false)
const submitError = ref('')

const isGPFS = computed(() => props.cluster?.kind === 'gpfs')
// An image reference, not text to translate
const imageExample = 'quay.io/ceph/ceph:v19.2.4'

// Dotted releases compared number by number (6.0.0.2 < 6.0.1.1)
const newer = (a?: string, b?: string) => {
    const x = (a || '').split('.').map(Number)
    const y = (b || '').split('.').map(Number)
    for (let i = 0; i < Math.max(x.length, y.length); i++) {
        const d = (x[i] || 0) - (y[i] || 0)
        if (d) return d > 0
    }
    return false
}

watch(
    () => props.show,
    async (show) => {
        if (!show) return
        chosen.value = ''
        finalize.value = false
        image.value = ''
        submitError.value = ''
        packages.value = []
        if (!isGPFS.value) return
        try {
            const resp = await storagePackagesApi.list({ limit: 100 })
            packages.value = (resp.storage_packages || []).filter(
                (p) =>
                    p.kind === props.cluster?.kind &&
                    p.status === 'ready' &&
                    !!p.accepted_by &&
                    newer(p.version, props.cluster?.version)
            )
            if (packages.value.length) chosen.value = packages.value[0].id
        } catch (err) {
            submitError.value = errorMessage(err, t('messages.error'))
        }
    }
)

const canSubmit = computed(() => {
    if (submitting.value) return false
    if (!isGPFS.value) return true
    return finalize.value || !!chosen.value
})

const submit = async () => {
    if (!canSubmit.value || !props.cluster) return
    submitting.value = true
    submitError.value = ''
    try {
        const payload = isGPFS.value
            ? finalize.value
                ? { finalize: true }
                : { package: chosen.value }
            : { image: image.value.trim() || undefined }
        emit('created', await storageClustersApi.upgrade(props.cluster.id, payload))
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
        :title="t('storage.upgrade.title', { name: cluster?.name || '' })"
        size="lg"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">{{ isGPFS ? t('storage.upgrade.gpfsIntro') : t('storage.upgrade.cephIntro') }}</p>
            <p class="current">
                {{ t('storage.upgrade.current') }}: <strong>{{ cluster?.version || '-' }}</strong>
            </p>

            <template v-if="isGPFS">
                <div v-if="!finalize" class="form-group">
                    <label class="form-label">{{ t('storage.upgrade.package') }}</label>
                    <select v-if="packages.length" v-model="chosen" class="form-input">
                        <option v-for="p in packages" :key="p.id" :value="p.id">
                            {{ p.version }} · {{ p.edition || '-' }} · {{ p.file_name }}
                        </option>
                    </select>
                    <div v-else class="empty">{{ t('storage.upgrade.noPackage') }}</div>
                </div>
                <label v-if="caps?.finalize" class="choice warn">
                    <input v-model="finalize" type="checkbox" />
                    <span>
                        <strong>{{ t('storage.upgrade.finalize') }}</strong>
                        <span class="hint"><TriangleAlert :size="12" /> {{ t('storage.upgrade.finalizeHint') }}</span>
                    </span>
                </label>
            </template>
            <div v-else class="form-group">
                <label class="form-label">{{ t('storage.upgrade.image') }}</label>
                <input v-model="image" type="text" class="form-input mono" :placeholder="imageExample" />
                <small class="text-secondary">{{ t('storage.upgrade.imageHint') }}</small>
            </div>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" :class="['btn', finalize ? 'btn-danger' : 'btn-primary']" :disabled="!canSubmit">
                {{
                    submitting
                        ? t('storage.cluster.starting')
                        : finalize
                          ? t('storage.upgrade.finalize')
                          : t('storage.upgrade.action')
                }}
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

.intro,
.current {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.empty {
    padding: var(--spacing-3);
    border: 1px dashed var(--border-light);
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    color: var(--text-tertiary);
}

.choice {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.choice.warn {
    border-color: var(--warning-color);
    background: var(--warning-light);
}

.choice input {
    margin-top: 3px;
}

.hint {
    display: block;
    margin-top: 4px;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    line-height: 1.5;
}

.mono {
    font-family: var(--font-family-mono);
}

.footer-error {
    margin-right: auto;
}
</style>
