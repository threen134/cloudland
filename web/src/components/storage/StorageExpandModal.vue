<script setup lang="ts">
// Adding hosts (with their disks) or disks of members to a storage cluster (shared-storage-design.md §7.5): the
// hosts are prechecked, get the software and join; the disks become part of a file system (the one chosen when the
// cluster has several). Rebalancing is a task of its own. In filesystem mode the disks make a new file system (§7.3).
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import StorageHostPicker, { type HostPick } from './StorageHostPicker.vue'
import {
    storageClustersApi,
    type StorageBackend,
    type StorageCluster,
    type StorageTask,
} from '../../api/storageClusters'
import { errorMessage } from '../../utils/error'

const props = defineProps<{
    show: boolean
    mode: 'nodes' | 'disks' | 'filesystem'
    cluster: StorageCluster | null
    backend?: StorageBackend
}>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t } = useI18n()

const pick = ref<HostPick>({ roles: {}, disks: {} })
const picker = ref<InstanceType<typeof StorageHostPicker> | null>(null)
const allowUnsupported = ref(false)
const submitting = ref(false)
const submitError = ref('')
const members = computed(() => (props.cluster?.nodes || []).map((n) => n.hypervisor.id || '').filter(Boolean))
// The file systems disks can go into; a choice only when there are several
const readyFs = computed(() => (props.cluster?.filesystems || []).filter((f) => f.status === 'ready'))
// The LUNs a SAN cluster serves already: ticked under another host it becomes one more server, never wiped
const usedLUNs = computed(() =>
    props.cluster?.layout === 'san' ? [...new Set((props.cluster.disks || []).map((d) => d.disk_id))] : []
)
const targetFs = ref('')
const fsForm = ref({ name: '', block_size: '4M', data_replicas: 2 })
const fsNameOk = computed(() => /^[A-Za-z][A-Za-z0-9_]{0,31}$/.test(fsForm.value.name.trim()))
const BLOCK_SIZES = ['1M', '2M', '4M', '8M', '16M']

watch(
    () => props.show,
    (show) => {
        if (!show) return
        pick.value = { roles: {}, disks: {} }
        allowUnsupported.value = false
        submitError.value = ''
        targetFs.value = readyFs.value[0]?.name || ''
        fsForm.value = { name: '', block_size: '4M', data_replicas: 2 }
    }
)

const canSubmit = computed(() => {
    if (submitting.value || (picker.value?.missingWipe ?? false)) return false
    if (props.mode === 'nodes') return Object.keys(pick.value.roles).length > 0
    if (props.mode === 'filesystem' && !fsNameOk.value) return false
    return Object.keys(pick.value.disks).length > 0
})

const submit = async () => {
    if (!canSubmit.value || !picker.value || !props.cluster) return
    submitting.value = true
    submitError.value = ''
    try {
        const plan = picker.value.payload()
        let task: StorageTask
        if (props.mode === 'nodes') {
            task = await storageClustersApi.addNodes(props.cluster.id, {
                ...plan,
                allow_unsupported: allowUnsupported.value,
            })
        } else if (props.mode === 'filesystem') {
            task = await storageClustersApi.createFilesystem(props.cluster.id, {
                name: fsForm.value.name.trim(),
                block_size: fsForm.value.block_size,
                data_replicas: Number(fsForm.value.data_replicas) || undefined,
                disks: plan.disks,
            })
        } else {
            task = await storageClustersApi.addDisks(props.cluster.id, {
                disks: plan.disks,
                filesystem: readyFs.value.length > 1 ? targetFs.value : undefined,
            })
        }
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
        :title="
            mode === 'nodes'
                ? t('storage.clusterDetail.addNodesTitle')
                : mode === 'filesystem'
                  ? t('storage.clusterDetail.createFsTitle')
                  : t('storage.clusterDetail.addDisksTitle')
        "
        size="xl"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">
                {{
                    mode === 'nodes'
                        ? t('storage.clusterDetail.addNodesIntro')
                        : mode === 'filesystem'
                          ? t('storage.clusterDetail.createFsIntro')
                          : t('storage.clusterDetail.addDisksIntro')
                }}
            </p>
            <div v-if="mode === 'filesystem'" class="fs-form">
                <div class="form-group">
                    <label class="form-label">{{ t('storage.wizard.fsName') }}</label>
                    <input v-model="fsForm.name" type="text" class="form-input mono" maxlength="32" />
                    <span v-if="fsForm.name && !fsNameOk" class="form-hint text-error">{{
                        t('storage.clusterDetail.fsNameHint')
                    }}</span>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.wizard.blockSize') }}</label>
                    <select v-model="fsForm.block_size" class="form-input">
                        <option v-for="b in BLOCK_SIZES" :key="b" :value="b">{{ b }}</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">{{ t('storage.wizard.dataReplicas') }}</label>
                    <select v-model.number="fsForm.data_replicas" class="form-input">
                        <option v-for="r in [1, 2, 3]" :key="r" :value="r">{{ r }}</option>
                    </select>
                </div>
            </div>
            <div v-if="mode === 'disks' && readyFs.length > 1" class="form-group">
                <label class="form-label">{{ t('storage.clusterDetail.targetFs') }}</label>
                <select v-model="targetFs" class="form-input">
                    <option v-for="f in readyFs" :key="f.id" :value="f.name">{{ f.name }} ({{ f.mount_point }})</option>
                </select>
            </div>
            <StorageHostPicker
                v-if="show"
                ref="picker"
                v-model="pick"
                :backend="backend"
                :mode="mode === 'nodes' ? 'roles' : 'disks'"
                :exclude="mode === 'nodes' ? members : []"
                :only="mode !== 'nodes' ? members : undefined"
                :shared-disks="cluster?.layout === 'san'"
                :used-disks="usedLUNs"
            />
            <label v-if="mode === 'nodes'" class="checkbox-inline" :title="t('storage.cluster.allowUnsupportedHint')">
                <input v-model="allowUnsupported" type="checkbox" />
                {{ t('storage.cluster.allowUnsupported') }}
            </label>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <span v-else-if="picker?.missingWipe" class="footer-error text-secondary">{{
                t('storage.cluster.wipeNeeded')
            }}</span>
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

.fs-form {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: var(--spacing-3);
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
    font-size: var(--font-size-sm);
}
</style>
