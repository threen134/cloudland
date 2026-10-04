<script setup lang="ts">
// Replacing a failed disk of a storage cluster (shared-storage-design.md §7.5, §8.5): a new disk of the same host takes
// its place. GPFS drops the failed NSD and restores the replication on the new one; Ceph gives the new disk the id of
// the failed OSD. The failed disk is not wiped. Only offered for a disk the storage reports down.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import {
    storageClustersApi,
    type StorageCluster,
    type StorageClusterDisk,
    type StorageTask,
} from '../../api/storageClusters'
import { hostStorageApi, type HostDisk } from '../../api/storagePools'
import { formatBytes } from '../../utils/format'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; cluster: StorageCluster | null; disk: StorageClusterDisk | null }>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t, te } = useI18n()

const disks = ref<HostDisk[]>([])
const loading = ref(false)
const loadError = ref('')
const chosen = ref('')
const wipe = ref(false)
const submitting = ref(false)
const submitError = ref('')

const load = async () => {
    const host = props.disk?.hypervisor.id
    if (!host) return
    loading.value = true
    loadError.value = ''
    try {
        disks.value = await hostStorageApi.disks(host)
    } catch (err) {
        loadError.value = errorMessage(err, t('messages.error'))
    } finally {
        loading.value = false
    }
}

watch(
    () => props.show,
    (show) => {
        if (!show) return
        chosen.value = ''
        wipe.value = false
        submitError.value = ''
        disks.value = []
        load()
    }
)

const candidates = computed(() => disks.value.filter((d) => d.state === 'free' || d.state === 'dirty'))
const picked = computed(() => candidates.value.find((d) => d.disk_id === chosen.value))
const needsWipe = computed(() => picked.value?.state === 'dirty' && !wipe.value)
const canSubmit = computed(() => !!picked.value && !needsWipe.value && !submitting.value)
const stateText = (s: string) => (te(`storage.diskStates.${s}`) ? t(`storage.diskStates.${s}`) : s)
const label = computed(() => props.disk?.name || props.disk?.disk_id || '')

const submit = async () => {
    if (!canSubmit.value || !props.cluster || !props.disk || !picked.value) return
    submitting.value = true
    submitError.value = ''
    try {
        const task = await storageClustersApi.replaceDisk(props.cluster.id, props.disk.id, {
            disk_id: picked.value.disk_id,
            wipe: picked.value.state === 'dirty' ? wipe.value : undefined,
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
        :title="t('storage.clusterDetail.replaceDiskTitle', { disk: label })"
        size="lg"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">
                {{
                    t('storage.clusterDetail.replaceDiskIntro', {
                        host: disk?.hypervisor.name || '-',
                        state: disk?.state || '-',
                    })
                }}
            </p>
            <div class="list-head">
                <span class="form-label">{{ t('storage.clusterDetail.newDisk') }}</span>
                <button type="button" class="btn btn-ghost btn-xs" :disabled="loading" @click="load">
                    <RefreshCw :size="12" :class="{ spinning: loading }" />
                </button>
            </div>
            <div v-if="loadError" class="text-error">{{ loadError }}</div>
            <div v-else-if="!loading && candidates.length === 0" class="empty">
                {{ t('storage.clusterDetail.noFreeDisk') }}
            </div>
            <label v-for="d in candidates" :key="d.disk_id" class="disk-row" :class="{ active: chosen === d.disk_id }">
                <input v-model="chosen" type="radio" name="new-disk" :value="d.disk_id" />
                <span class="disk-name">{{ d.name }}</span>
                <span class="mono">{{ d.disk_id }}</span>
                <span class="sub">{{ formatBytes(d.size_bytes) }} · {{ (d.media || '-').toUpperCase() }}</span>
                <span class="sub" :class="{ dirty: d.state === 'dirty' }">{{ stateText(d.state) }}</span>
            </label>
            <label v-if="picked?.state === 'dirty'" class="checkbox-inline">
                <input v-model="wipe" type="checkbox" />
                {{ t('storage.clusterDetail.wipeNewDisk') }}
            </label>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
                {{ submitting ? t('storage.cluster.starting') : t('storage.clusterDetail.replaceDisk') }}
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

.disk-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    padding: 8px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.disk-row.active {
    border-color: var(--primary-color);
    background: var(--primary-light);
}

.disk-name {
    font-weight: 600;
}

.mono {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    word-break: break-all;
}

.sub {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.sub.dirty {
    color: var(--warning-dark);
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.footer-error {
    margin-right: auto;
}
</style>
