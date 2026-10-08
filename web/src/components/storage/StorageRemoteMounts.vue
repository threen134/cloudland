<script setup lang="ts">
// Remote mounts of a storage cluster (shared-storage-design.md §7.11): its file systems other clusters of the kind
// mount, and the file systems of others it mounts, under the same name and mount point; the pools on them are usable
// on the hosts that mount. Made and removed by a task of the cluster that mounts, so the task page opens.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Plus, Trash2, Share2 } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import DeleteModal from '../modals/DeleteModal.vue'
import DataTable, { type Column } from '../base/DataTable.vue'
import StatusBadge from '../base/StatusBadge.vue'
import { storageClustersApi, type StorageCluster, type StorageRemoteMount } from '../../api/storageClusters'
import { useToast } from '../../composables/useToast'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ cluster: StorageCluster; canChange: boolean }>()
const { t, te } = useI18n()
const router = useRouter()
const toast = useToast()

const mounts = ref<StorageRemoteMount[]>([])
const loading = ref(false)
const loadError = ref('')
const load = async () => {
    loading.value = true
    loadError.value = ''
    try {
        mounts.value = await storageClustersApi.remoteMounts(props.cluster.id)
    } catch (err) {
        loadError.value = errorMessage(err, t('storage.remoteMounts.loadFailed'))
    } finally {
        loading.value = false
    }
}
watch(() => props.cluster.id, load, { immediate: true })

const columns = computed<Column[]>(() => [
    { key: 'filesystem', label: t('storage.remoteMounts.filesystem') },
    { key: 'direction', label: t('storage.remoteMounts.direction') },
    { key: 'mount', label: t('storage.remoteMounts.mountPoint') },
    { key: 'status', label: t('storage.remoteMounts.status') },
    { key: 'actions', label: '', align: 'right' },
])
const statusText = (s: string) =>
    te(`storage.remoteMounts.statuses.${s}`) ? t(`storage.remoteMounts.statuses.${s}`) : s
const owned = (m: StorageRemoteMount) => m.owner.id === props.cluster.id

// ---- a new mount: a ready file system of this cluster, another ready managed cluster of the kind ----
const showCreate = ref(false)
const fs = ref('')
const target = ref('')
const targets = ref<StorageCluster[]>([])
const createBusy = ref(false)
const createError = ref('')
const readyFs = computed(() => (props.cluster.filesystems || []).filter((f) => f.status === 'ready'))
const openCreate = async () => {
    fs.value = readyFs.value[0]?.name || ''
    target.value = ''
    createError.value = ''
    showCreate.value = true
    try {
        const list = await storageClustersApi.list({ limit: 500 })
        targets.value = (list.storage_clusters || []).filter(
            (c) =>
                c.id !== props.cluster.id &&
                c.kind === props.cluster.kind &&
                c.mode === 'managed' &&
                c.status === 'ready' &&
                c.capabilities?.remote_mount
        )
        target.value = targets.value[0]?.id || ''
    } catch (err) {
        createError.value = errorMessage(err, t('storage.remoteMounts.loadFailed'))
    }
}
const create = async () => {
    if (!fs.value || !target.value) return
    createBusy.value = true
    createError.value = ''
    try {
        const task = await storageClustersApi.createRemoteMount(props.cluster.id, {
            filesystem: fs.value,
            cluster: target.value,
        })
        showCreate.value = false
        toast.success(t('storage.clusterDetail.taskStarted'))
        router.push({ name: 'storage-task-detail', params: { id: task.id } })
    } catch (err) {
        createError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        createBusy.value = false
    }
}

// ---- removing a mount ----
const deleting = ref<StorageRemoteMount | null>(null)
const deleteBusy = ref(false)
const remove = async () => {
    if (!deleting.value) return
    deleteBusy.value = true
    try {
        const task = await storageClustersApi.deleteRemoteMount(
            deleting.value.owner.id || props.cluster.id,
            deleting.value.id
        )
        deleting.value = null
        toast.success(t('storage.clusterDetail.taskStarted'))
        router.push({ name: 'storage-task-detail', params: { id: task.id } })
    } catch (err) {
        toast.error(errorMessage(err, t('storage.cluster.actionFailed')))
    } finally {
        deleteBusy.value = false
    }
}
</script>

<template>
    <div class="remote-mounts card">
        <div class="remote-head">
            <h4><Share2 :size="16" /> {{ t('storage.remoteMounts.title') }}</h4>
            <button class="btn btn-secondary btn-sm" :disabled="!canChange || readyFs.length === 0" @click="openCreate">
                <Plus :size="14" /> {{ t('storage.remoteMounts.create') }}
            </button>
        </div>
        <p class="form-hint">{{ t('storage.remoteMounts.hint') }}</p>
        <DataTable
            :columns="columns"
            :rows="mounts"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :empty-text="t('storage.remoteMounts.empty')"
            @retry="load"
        >
            <template #cell-filesystem="{ row: m }"
                ><span class="resource-name">{{ m.filesystem }}</span></template
            >
            <template #cell-direction="{ row: m }">
                <span v-if="owned(m)">{{ t('storage.remoteMounts.mountedBy', { cluster: m.access.name }) }}</span>
                <span v-else>{{ t('storage.remoteMounts.mountedFrom', { cluster: m.owner.name }) }}</span>
            </template>
            <template #cell-mount="{ row: m }"
                ><span class="mono-cell">{{ m.mount_point }}</span></template
            >
            <template #cell-status="{ row: m }">
                <StatusBadge :status="m.status" :label="statusText(m.status)" :title="m.reason || ''" />
            </template>
            <template #cell-actions="{ row: m }">
                <div class="row-actions">
                    <button
                        class="icon-btn-table icon-danger"
                        :title="t('storage.remoteMounts.delete')"
                        :disabled="!canChange || (m.status !== 'ready' && m.status !== 'error')"
                        @click="deleting = m"
                    >
                        <Trash2 :size="16" />
                    </button>
                </div>
            </template>
        </DataTable>

        <BaseModal
            :show="showCreate"
            :title="t('storage.remoteMounts.create')"
            size="md"
            form
            :loading="createBusy"
            @close="showCreate = false"
            @submit="create"
        >
            <p class="form-hint">{{ t('storage.remoteMounts.createIntro') }}</p>
            <div class="form-group">
                <label class="form-label">{{ t('storage.remoteMounts.filesystem') }}</label>
                <select v-model="fs" class="form-input">
                    <option v-for="f in readyFs" :key="f.id" :value="f.name">{{ f.name }} ({{ f.mount_point }})</option>
                </select>
            </div>
            <div class="form-group">
                <label class="form-label">{{ t('storage.remoteMounts.target') }}</label>
                <select v-model="target" class="form-input">
                    <option v-for="c in targets" :key="c.id" :value="c.id">{{ c.name }}</option>
                </select>
                <span v-if="targets.length === 0" class="form-hint">{{ t('storage.remoteMounts.noTarget') }}</span>
            </div>
            <template #footer>
                <span v-if="createError" class="footer-error text-error">{{ createError }}</span>
                <button type="button" class="btn btn-secondary" @click="showCreate = false">
                    {{ t('common.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="createBusy || !fs || !target">
                    {{ t('storage.remoteMounts.create') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="deleting !== null"
            :title="t('storage.remoteMounts.delete')"
            :message="
                t('storage.remoteMounts.deleteMessage', {
                    fs: deleting?.filesystem,
                    cluster: deleting?.access.name,
                })
            "
            :confirm-label="t('storage.remoteMounts.delete')"
            :loading="deleteBusy"
            @close="deleting = null"
            @confirm="remove"
        />
    </div>
</template>

<style scoped>
.remote-mounts {
    margin-top: var(--spacing-4);
    padding: var(--spacing-4);
}

.remote-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
}

.remote-head h4 {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 600;
}
</style>
