<script setup lang="ts">
// A CloudLand pool on a storage cluster (shared-storage-design.md §7.4, §7.8, §8.3): on a managed GPFS cluster an
// independent fileset of a file system, on a managed Ceph cluster an RBD pool, each with the media its data goes to and
// an optional quota; on an imported cluster a directory or an RBD pool its admins made. The pool is made by a task of
// the cluster and can be chosen once the task succeeded.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import { storageClustersApi, type StorageCluster, type StorageBackend } from '../../api/storageClusters'
import { storagePoolsApi, type PoolMedia, type StoragePoolTask } from '../../api/storagePools'
import { storageKindText } from '../../utils/storageCluster'
import { formatBytes } from '../../utils/format'
import { errorMessage } from '../../utils/error'

const props = defineProps<{
    show: boolean
    /** The cluster, when the dialog is opened from it; otherwise one of the ready clusters is chosen */
    cluster?: StorageCluster | null
}>()
const emit = defineEmits<{ close: []; created: [result: StoragePoolTask] }>()
const { t, te } = useI18n()

const clusters = ref<StorageCluster[]>([])
const backends = ref<StorageBackend[]>([])
const loading = ref(false)
const loadError = ref('')
const clusterId = ref('')
const detail = ref<StorageCluster | null>(null)
const submitting = ref(false)
const submitError = ref('')
const form = ref({
    name: '',
    filesystem: '',
    media: '' as PoolMedia,
    quota_gb: 0,
    over_ratio: 1,
    inode_limit: 100000,
    path: '',
    fileset: '',
    ceph_pool: '',
    is_default: false,
    description: '',
})

const backendOf = (kind?: string) => backends.value.find((b) => b.kind === kind)
// Clusters a pool can be made on: ready, of a kind with pools
const candidates = computed(() =>
    clusters.value.filter((c) => c.status === 'ready' && backendOf(c.kind)?.capabilities?.pools)
)
const external = computed(() => detail.value?.mode === 'external')
const isCeph = computed(() => detail.value?.kind === 'ceph')
// Only a ready file system takes a pool: one being made or deleted is left out
const filesystems = computed(() => (detail.value?.filesystems || []).filter((f) => !f.status || f.status === 'ready'))
// Media a pool can ask for: those of the disks of the cluster (the backend checks the file system has them)
const mediaChoices = computed(() =>
    [...new Set((detail.value?.disks || []).map((d) => d.media).filter(Boolean))].sort()
)

const reset = () => {
    form.value = {
        name: '',
        filesystem: '',
        media: '',
        quota_gb: 0,
        over_ratio: 1,
        inode_limit: 100000,
        path: '',
        fileset: '',
        ceph_pool: '',
        is_default: false,
        description: '',
    }
    submitError.value = ''
    detail.value = null
}

const loadDetail = async (id: string) => {
    detail.value = null
    if (!id) return
    try {
        detail.value = await storageClustersApi.get(id)
        form.value.filesystem = filesystems.value[0]?.name || ''
        if (external.value && !form.value.path && detail.value.filesystems?.[0]) {
            form.value.path = `${detail.value.filesystems[0].mount_point}/`
        }
    } catch (err) {
        loadError.value = errorMessage(err, t('messages.error'))
    }
}

watch(
    () => props.show,
    async (show) => {
        if (!show) return
        reset()
        loading.value = true
        loadError.value = ''
        try {
            backends.value = await storageClustersApi.backends()
            if (props.cluster) {
                clusters.value = [props.cluster]
                clusterId.value = props.cluster.id
            } else {
                clusters.value = (await storageClustersApi.list({ limit: 500 })).storage_clusters || []
                clusterId.value = candidates.value[0]?.id || ''
            }
            await loadDetail(clusterId.value)
        } catch (err) {
            loadError.value = errorMessage(err, t('messages.error'))
        } finally {
            loading.value = false
        }
    },
    { immediate: true }
)
watch(clusterId, (id, old) => {
    if (old !== undefined && id !== old) loadDetail(id)
})

const canSubmit = computed(
    () =>
        !!form.value.name &&
        !!detail.value &&
        !submitting.value &&
        (!external.value ||
            (isCeph.value
                ? /^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$/.test(form.value.ceph_pool)
                : /^\/[A-Za-z0-9_./-]+$/.test(form.value.path)))
)

const submit = async () => {
    if (!canSubmit.value || !detail.value) return
    submitting.value = true
    submitError.value = ''
    const f = form.value
    let params: Record<string, unknown>
    if (isCeph.value) params = external.value ? { ceph_pool: f.ceph_pool } : {}
    else if (external.value)
        params = { filesystem: f.filesystem || undefined, path: f.path, fileset: f.fileset || undefined }
    else params = { filesystem: f.filesystem || undefined, inode_limit: Number(f.inode_limit) || undefined }
    try {
        const result = await storagePoolsApi.createShared({
            name: f.name,
            cluster: { id: detail.value.id },
            media: external.value ? undefined : f.media || undefined,
            quota_gb: external.value ? undefined : Number(f.quota_gb) || undefined,
            over_ratio: Number(f.over_ratio) || undefined,
            is_default: f.is_default,
            description: f.description || undefined,
            params,
        })
        emit('created', result)
    } catch (err) {
        submitError.value = errorMessage(err, t('storage.submitFailed'))
    } finally {
        submitting.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="t('storage.sharedPool.createTitle')"
        size="lg"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">{{ t('storage.sharedPool.intro') }}</p>
            <div v-if="loading" class="muted-line">{{ t('messages.loading') }}</div>
            <div v-else-if="loadError" class="text-error">{{ loadError }}</div>
            <div v-else-if="!cluster && candidates.length === 0" class="muted-line">
                {{ t('storage.sharedPool.noCluster') }}
            </div>
            <template v-else>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.cluster.cluster') }}</label>
                    <select v-model="clusterId" class="form-input" :disabled="!!cluster">
                        <option v-for="c in cluster ? [cluster] : candidates" :key="c.id" :value="c.id">
                            {{ c.name }} · {{ storageKindText(t, te, c.kind) }}
                            <template v-if="c.mode === 'external'">
                                · {{ t('storage.cluster.modes.external') }}</template
                            >
                        </option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.name') }} *</label>
                    <input v-model="form.name" type="text" class="form-input" maxlength="64" required />
                </div>
                <div v-if="filesystems.length" class="form-group">
                    <label class="form-label">{{ t('storage.sharedPool.filesystem') }}</label>
                    <select v-model="form.filesystem" class="form-input">
                        <option v-for="f in filesystems" :key="f.id" :value="f.name">
                            {{ f.name }} · {{ f.mount_point }}
                            <template v-if="f.capacity_bytes"> · {{ formatBytes(f.capacity_bytes) }}</template>
                        </option>
                    </select>
                </div>
                <template v-if="external && isCeph">
                    <div class="form-group">
                        <label class="form-label">{{ t('storage.sharedPool.cephPool') }} *</label>
                        <input v-model="form.ceph_pool" type="text" class="form-input mono" maxlength="100" required />
                        <span class="form-hint">{{ t('storage.sharedPool.cephPoolHint') }}</span>
                    </div>
                </template>
                <template v-else-if="external">
                    <div class="form-group">
                        <label class="form-label">{{ t('storage.sharedPool.path') }} *</label>
                        <input v-model="form.path" type="text" class="form-input" maxlength="200" required />
                        <span class="form-hint">{{ t('storage.sharedPool.pathHint') }}</span>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('storage.sharedPool.fileset') }}</label>
                        <input v-model="form.fileset" type="text" class="form-input" maxlength="64" />
                        <span class="form-hint">{{ t('storage.sharedPool.filesetHint') }}</span>
                    </div>
                </template>
                <template v-else>
                    <div class="form-group">
                        <label class="form-label">{{ t('storage.media') }}</label>
                        <select v-model="form.media" class="form-input">
                            <option value="">
                                {{ isCeph ? t('storage.sharedPool.mediaAnyCeph') : t('storage.sharedPool.mediaAny') }}
                            </option>
                            <option v-for="m in mediaChoices" :key="m" :value="m">{{ m.toUpperCase() }}</option>
                        </select>
                        <span class="form-hint">{{
                            isCeph ? t('storage.sharedPool.mediaHintCeph') : t('storage.sharedPool.mediaHint')
                        }}</span>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('storage.sharedPool.quota') }}</label>
                        <input v-model="form.quota_gb" type="number" min="0" step="1" class="form-input" />
                        <span class="form-hint">{{
                            isCeph ? t('storage.sharedPool.quotaHintCeph') : t('storage.sharedPool.quotaHint')
                        }}</span>
                    </div>
                    <div v-if="!isCeph" class="form-group">
                        <label class="form-label">{{ t('storage.sharedPool.inodeLimit') }}</label>
                        <input v-model="form.inode_limit" type="number" min="1000" step="1000" class="form-input" />
                        <span class="form-hint">{{ t('storage.sharedPool.inodeHint') }}</span>
                    </div>
                </template>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.overRatio') }}</label>
                    <input v-model="form.over_ratio" type="number" min="0.1" max="20" step="0.1" class="form-input" />
                    <span class="form-hint">{{ t('storage.sharedPool.overRatioHint') }}</span>
                </div>
                <div class="form-group">
                    <label class="checkbox-inline">
                        <input v-model="form.is_default" type="checkbox" />
                        {{ t('storage.setDefault') }}
                    </label>
                    <span class="form-hint">{{ t('storage.sharedPool.defaultHint') }}</span>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('dashboard.table.description') }}</label>
                    <input v-model="form.description" type="text" class="form-input" maxlength="256" />
                </div>
            </template>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
                {{ submitting ? t('storage.cluster.starting') : t('actions.create') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.form-stack {
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
}

.footer-error {
    margin-right: auto;
    font-size: var(--font-size-sm);
}
</style>
