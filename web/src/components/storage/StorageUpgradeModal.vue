<script setup lang="ts">
// Upgrading a managed storage cluster (shared-storage-design.md §7.7, §8.7). GPFS: a package of a newer release from
// the package repository, rolled out one host after the other; or, once every host runs it, the irreversible step that
// raises the cluster and its file systems to it. Ceph: the release the hosts install from their distribution, then
// cephadm upgrades the daemons; an image only for a cluster on an image of its own
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { TriangleAlert, Upload } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import {
    storageClustersApi,
    type StorageCluster,
    type StorageCapabilities,
    type StorageTask,
} from '../../api/storageClusters'
import { type StoragePackage } from '../../api/storagePackages'
import { useStoragePackageFlow } from '../../composables/useStoragePackageFlow'
import { packageBusy, packageStatusText, packageEditionText } from '../../utils/storageCluster'
import StoragePackageUploadModal from './StoragePackageUploadModal.vue'
import StorageLicenseModal from './StorageLicenseModal.vue'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; cluster: StorageCluster | null; caps?: StorageCapabilities }>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t, te } = useI18n()

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

// A package the cluster can be upgraded with: a newer release of the edition the cluster runs (the backend refuses
// another edition; the cluster detail says which it runs, nothing when it came from no package)
const candidate = (p: StoragePackage) =>
    p.status === 'ready' &&
    newer(p.version, props.cluster?.version) &&
    (!props.cluster?.edition || p.edition === props.cluster.edition)
// Those with their license accepted can be chosen; one missing is uploaded and its license accepted from here, over
// this dialog
const {
    packages: allPackages,
    ofKind,
    uploaded,
    failed: failedPackage,
    error: packagesError,
    showUpload,
    licenseId,
    uploadDone,
    uploadClosed,
    accepted,
} = useStoragePackageFlow({
    kind: () => props.cluster?.kind,
    active: () => props.show && isGPFS.value,
    wanted: candidate,
    onAccepted: (pkg) => {
        if (candidate(pkg)) chosen.value = pkg.id
    },
})
const packages = computed(() => ofKind.value.filter((p) => candidate(p) && !!p.accepted_by))
// Not usable yet: still downloaded or verified (its release is known only afterwards), or a candidate whose license
// is not accepted
const pendingPackages = computed(() => ofKind.value.filter((p) => packageBusy(p) || (candidate(p) && !p.accepted_by)))
watch(packages, (list) => {
    if (!list.some((p) => p.id === chosen.value)) chosen.value = list[0]?.id || ''
})
// The package just uploaded here turned out not to fit: say why rather than leave it out without a word
const uploadedUnfit = computed(() => {
    const p = uploaded.value
    if (!p || p.status !== 'ready' || candidate(p)) return ''
    if (!newer(p.version, props.cluster?.version))
        return t('storage.upgrade.uploadedOlder', {
            name: p.file_name,
            version: p.version || '-',
            current: props.cluster?.version || '-',
        })
    return t('storage.upgrade.uploadedEdition', {
        name: p.file_name,
        edition: packageEditionText(t, te, p.edition),
        current: packageEditionText(t, te, props.cluster?.edition),
    })
})

watch(
    () => props.show,
    (show) => {
        if (!show) return
        chosen.value = ''
        finalize.value = false
        image.value = ''
        submitError.value = ''
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
                <div v-if="!finalize" class="form-group package-group">
                    <label class="form-label">{{ t('storage.upgrade.package') }}</label>
                    <select v-if="packages.length" v-model="chosen" class="form-input">
                        <option v-for="p in packages" :key="p.id" :value="p.id">
                            {{ p.version }} · {{ packageEditionText(t, te, p.edition) }} · {{ p.file_name }}
                        </option>
                    </select>
                    <div v-else class="empty">{{ t('storage.upgrade.noPackage') }}</div>
                    <div v-for="p in pendingPackages" :key="p.id" class="pending-row">
                        <span class="pending-name">{{ p.version ? `${p.version} · ` : '' }}{{ p.file_name }}</span>
                        <span v-if="packageBusy(p)" class="badge badge-primary">{{ packageStatusText(t, te, p) }}</span>
                        <button v-else type="button" class="btn btn-secondary btn-sm" @click="licenseId = p.id">
                            {{ t('storage.packages.accept') }}
                        </button>
                    </div>
                    <div v-if="packagesError" class="text-error">{{ packagesError }}</div>
                    <div v-if="failedPackage" class="text-error">
                        {{
                            t('storage.packages.uploadedFailed', {
                                name: failedPackage.file_name,
                                reason: failedPackage.reason || '-',
                            })
                        }}
                    </div>
                    <div v-if="uploadedUnfit" class="text-warning">{{ uploadedUnfit }}</div>
                    <div>
                        <button type="button" class="btn btn-secondary btn-sm" @click="showUpload = true">
                            <Upload :size="14" />
                            <span>{{ t('storage.packages.uploadPackage') }}</span>
                        </button>
                    </div>
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
    <StoragePackageUploadModal
        :show="showUpload"
        :packages="allPackages"
        :initial-kind="cluster?.kind"
        @close="uploadClosed"
        @done="uploadDone"
    />
    <StorageLicenseModal :show="!!licenseId" :package-id="licenseId" @close="licenseId = ''" @accepted="accepted" />
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

.package-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-2);
}

.package-group .form-label {
    margin-bottom: 0;
}

.pending-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
}

.pending-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-secondary);
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
