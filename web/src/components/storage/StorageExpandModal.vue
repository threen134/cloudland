<script setup lang="ts">
// Adding hosts (with their disks) or disks of members to a storage cluster (shared-storage-design.md §7.5): the
// hosts are prechecked, get the software and join; the disks become part of the file system. Rebalancing is a task
// of its own.
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
    mode: 'nodes' | 'disks'
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

watch(
    () => props.show,
    (show) => {
        if (!show) return
        pick.value = { roles: {}, disks: {} }
        allowUnsupported.value = false
        submitError.value = ''
    }
)

const canSubmit = computed(() => {
    if (submitting.value || (picker.value?.missingWipe ?? false)) return false
    return props.mode === 'nodes' ? Object.keys(pick.value.roles).length > 0 : Object.keys(pick.value.disks).length > 0
})

const submit = async () => {
    if (!canSubmit.value || !picker.value || !props.cluster) return
    submitting.value = true
    submitError.value = ''
    try {
        const plan = picker.value.payload()
        const task =
            props.mode === 'nodes'
                ? await storageClustersApi.addNodes(props.cluster.id, {
                      ...plan,
                      allow_unsupported: allowUnsupported.value,
                  })
                : await storageClustersApi.addDisks(props.cluster.id, { disks: plan.disks })
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
        :title="mode === 'nodes' ? t('storage.clusterDetail.addNodesTitle') : t('storage.clusterDetail.addDisksTitle')"
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
                        : t('storage.clusterDetail.addDisksIntro')
                }}
            </p>
            <StorageHostPicker
                v-if="show"
                ref="picker"
                v-model="pick"
                :backend="backend"
                :mode="mode === 'nodes' ? 'roles' : 'disks'"
                :exclude="mode === 'nodes' ? members : []"
                :only="mode === 'disks' ? members : undefined"
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
