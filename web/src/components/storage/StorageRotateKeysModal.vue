<script setup lang="ts">
// Rotating the keys of a managed storage cluster (shared-storage-design.md §6.6, §8.4): the SSH key the admin hosts
// log in with and, for Ceph, the key of the client user. Neither is ever missing for a moment; running instances keep
// their sessions with the old client key
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import {
    storageClustersApi,
    type StorageCluster,
    type StorageCapabilities,
    type StorageTask,
} from '../../api/storageClusters'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; cluster: StorageCluster | null; caps?: StorageCapabilities }>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t } = useI18n()

const ssh = ref(true)
const client = ref(false)
const submitting = ref(false)
const submitError = ref('')

watch(
    () => props.show,
    (show) => {
        if (!show) return
        ssh.value = true
        client.value = !!props.caps?.client_key
        submitError.value = ''
    }
)

const canSubmit = computed(() => (ssh.value || client.value) && !submitting.value)

const submit = async () => {
    if (!canSubmit.value || !props.cluster) return
    submitting.value = true
    submitError.value = ''
    try {
        emit('created', await storageClustersApi.rotateKeys(props.cluster.id, { ssh: ssh.value, client: client.value }))
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
        :title="t('storage.rotate.title', { name: cluster?.name || '' })"
        size="md"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">{{ t('storage.rotate.intro') }}</p>
            <label class="choice">
                <input v-model="ssh" type="checkbox" />
                <span>
                    <strong>{{ t('storage.rotate.ssh') }}</strong>
                    <span class="hint">{{ t('storage.rotate.sshHint') }}</span>
                </span>
            </label>
            <label v-if="caps?.client_key" class="choice">
                <input v-model="client" type="checkbox" />
                <span>
                    <strong>{{ t('storage.rotate.client') }}</strong>
                    <span class="hint">{{ t('storage.rotate.clientHint') }}</span>
                </span>
            </label>
            <p v-if="!ssh && !client" class="text-error nothing">{{ t('storage.rotate.nothing') }}</p>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
                {{ submitting ? t('storage.cluster.starting') : t('storage.rotate.action') }}
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

.nothing {
    margin: 0;
    font-size: var(--font-size-sm);
}

.footer-error {
    margin-right: auto;
}
</style>
