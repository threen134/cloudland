<script setup lang="ts">
// Upload of a storage package (shared-storage-design.md §6.1): the file goes up in parts of part_size bytes, in order,
// so an upload cut off can go on from the next part: choosing the same file again resumes the unfinished package of
// that name and size. Or clapi downloads the installer from an address itself.
import { ref, computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import { storagePackagesApi, type StoragePackage } from '../../api/storagePackages'
import { storageClustersApi, type StorageBackend } from '../../api/storageClusters'
import { storageKindText } from '../../utils/storageCluster'
import { formatBytes } from '../../utils/format'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; packages: StoragePackage[] }>()
const emit = defineEmits<{ close: []; done: [pkg: StoragePackage] }>()
const { t, te } = useI18n()

type Source = 'file' | 'url'
const source = ref<Source>('file')
const kinds = ref<StorageBackend[]>([])
const kind = ref('')
const file = ref<File | null>(null)
const url = ref('')
const uploading = ref(false)
const sent = ref(0)
const total = ref(0)
const error = ref('')
// The package this modal is uploading: the page's list does not hold it until it is reloaded, and a second try after
// an upload was cut off has to go on with it rather than start another
const current = ref<StoragePackage | null>(null)
let abort: AbortController | null = null

const percent = computed(() => (total.value ? Math.floor((sent.value * 100) / total.value) : 0))

// An unfinished upload of the same file: same kind, name and size, still taking parts
const resumable = computed(() =>
    file.value
        ? [...(current.value ? [current.value] : []), ...props.packages].find(
              (p) =>
                  p.status === 'uploading' &&
                  !p.source_url &&
                  p.kind === kind.value &&
                  p.file_name === file.value?.name &&
                  p.size_bytes === file.value?.size
          )
        : undefined
)

watch(
    () => props.show,
    async (show) => {
        if (!show) return
        source.value = 'file'
        file.value = null
        url.value = ''
        error.value = ''
        sent.value = 0
        total.value = 0
        try {
            kinds.value = (await storageClustersApi.backends()).filter((b) => b.package)
            if (!kinds.value.some((b) => b.kind === kind.value)) kind.value = kinds.value[0]?.kind || ''
        } catch (err) {
            error.value = errorMessage(err, t('messages.error'))
        }
    },
    { immediate: true }
)

const pickFile = (event: Event) => {
    file.value = (event.target as HTMLInputElement).files?.[0] || null
    error.value = ''
}

const canSubmit = computed(
    () =>
        !uploading.value &&
        !!kind.value &&
        (source.value === 'file' ? !!file.value : /^https?:\/\/\S+$/.test(url.value.trim()))
)

const uploadFile = async (f: File) => {
    // Resuming: where the server stands, not what the list said when it was loaded
    let pkg = resumable.value
        ? await storagePackagesApi.get(resumable.value.id)
        : await storagePackagesApi.startUpload(kind.value, f.name, f.size)
    current.value = pkg
    total.value = pkg.total_parts
    sent.value = pkg.parts_done
    abort = new AbortController()
    for (let n = pkg.parts_done + 1; n <= pkg.total_parts; n++) {
        const start = (n - 1) * pkg.part_size
        const blob = f.slice(start, Math.min(start + pkg.part_size, f.size))
        let tries = 0
        for (;;) {
            try {
                pkg = await storagePackagesApi.uploadPart(pkg.id, n, blob, abort.signal)
                current.value = pkg
                break
            } catch (err) {
                if (abort.signal.aborted || ++tries >= 3) throw err
            }
        }
        sent.value = n
    }
    return storagePackagesApi.complete(pkg.id)
}

const submit = async () => {
    if (!canSubmit.value) return
    uploading.value = true
    error.value = ''
    try {
        const pkg =
            source.value === 'file' && file.value
                ? await uploadFile(file.value)
                : await storagePackagesApi.startDownload(kind.value, url.value.trim())
        emit('done', pkg)
    } catch (err) {
        if (abort?.signal.aborted) {
            error.value = t('storage.packages.uploadStopped')
        } else if (current.value && networkCut(err)) {
            // No answer at all: the network, not the server. The parts that went up are kept
            error.value = t('storage.packages.uploadCut')
        } else {
            error.value = errorMessage(err, t('storage.packages.uploadFailed'))
        }
    } finally {
        uploading.value = false
        abort = null
    }
}

// A request that got no answer (axios gives it no response): the connection was lost
const networkCut = (err: unknown) =>
    typeof err === 'object' && err !== null && 'isAxiosError' in err && !(err as { response?: unknown }).response

const stop = () => abort?.abort()
const close = () => {
    stop()
    emit('close')
}
onUnmounted(stop)
</script>

<template>
    <BaseModal :show="show" :title="t('storage.packages.uploadTitle')" size="lg" form @close="close" @submit="submit">
        <div class="upload-form">
            <p class="intro">{{ t('storage.packages.uploadIntro') }}</p>
            <div class="form-group">
                <label class="form-label">{{ t('storage.cluster.kindLabel') }}</label>
                <select v-model="kind" class="form-input" :disabled="uploading">
                    <option v-for="b in kinds" :key="b.kind" :value="b.kind">
                        {{ storageKindText(t, te, b.kind) }}
                    </option>
                </select>
            </div>
            <div class="source-switch" role="radiogroup">
                <button
                    v-for="s in ['file', 'url'] as Source[]"
                    :key="s"
                    type="button"
                    class="source-option"
                    :class="{ active: source === s }"
                    role="radio"
                    :aria-checked="source === s"
                    :disabled="uploading"
                    @click="source = s"
                >
                    {{ t(`storage.packages.sources.${s}`) }}
                </button>
            </div>
            <div v-if="source === 'file'" class="form-group">
                <input type="file" class="form-input" :disabled="uploading" @change="pickFile" />
                <span v-if="file" class="form-hint">{{ file.name }} · {{ formatBytes(file.size) }}</span>
                <span v-if="resumable" class="form-hint resume-hint">{{
                    t('storage.packages.resumeHint', { done: resumable.parts_done, total: resumable.total_parts })
                }}</span>
            </div>
            <div v-else class="form-group">
                <input
                    v-model="url"
                    class="form-input"
                    type="url"
                    placeholder="https://"
                    :disabled="uploading"
                    autocomplete="off"
                />
                <span class="form-hint">{{ t('storage.packages.urlHint') }}</span>
            </div>
            <div v-if="uploading && source === 'file'" class="progress">
                <div class="progress-bar"><div class="progress-fill" :style="{ width: percent + '%' }"></div></div>
                <span class="progress-text">{{
                    t('storage.packages.uploadProgress', { done: sent, total: total, percent: percent })
                }}</span>
            </div>
        </div>
        <template #footer>
            <span v-if="error" class="footer-error text-error">{{ error }}</span>
            <button v-if="uploading" type="button" class="btn btn-secondary" @click="stop">
                {{ t('storage.packages.stop') }}
            </button>
            <button v-else type="button" class="btn btn-secondary" @click="close">{{ t('actions.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
                {{
                    uploading
                        ? t('storage.packages.uploading')
                        : resumable
                          ? t('storage.packages.resume')
                          : t('storage.packages.upload')
                }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.upload-form {
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

.resume-hint {
    color: var(--primary-color);
}

.source-switch {
    display: inline-flex;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    overflow: hidden;
    align-self: flex-start;
}

.source-option {
    padding: 6px 16px;
    border: none;
    background: var(--bg-primary);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    cursor: pointer;
}

.source-option + .source-option {
    border-left: 1px solid var(--border-default);
}

.source-option.active {
    background: var(--primary-color);
    color: var(--text-inverse);
}

.progress {
    display: flex;
    flex-direction: column;
    gap: 6px;
}

.progress-bar {
    height: 8px;
    background: var(--bg-tertiary);
    border-radius: 4px;
    overflow: hidden;
}

.progress-fill {
    height: 100%;
    background: var(--primary-color);
    transition: width 0.3s;
}

.progress-text {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.footer-error {
    margin-right: auto;
    font-size: var(--font-size-sm);
}
</style>
