<script setup lang="ts">
// Changing the roles of a member of a storage cluster (shared-storage-design.md §13.1): GPFS quorum and admin hosts,
// Ceph mon, mgr and admin placement. The disk role follows the disks of the host and is not offered here; the role
// rules of the kind (an odd number of quorum hosts or mons, one or two admin hosts...) are checked by the backend.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import {
    storageClustersApi,
    type StorageBackend,
    type StorageCluster,
    type StorageClusterNode,
    type StorageRole,
    type StorageTask,
} from '../../api/storageClusters'
import { roleText } from '../../utils/storageCluster'
import { errorMessage } from '../../utils/error'

const props = defineProps<{
    show: boolean
    cluster: StorageCluster | null
    node: StorageClusterNode | null
    backend?: StorageBackend
}>()
const emit = defineEmits<{ close: []; created: [task: StorageTask] }>()
const { t, te } = useI18n()

const roles = ref<StorageRole[]>([])
const submitting = ref(false)
const submitError = ref('')

const offered = computed(() => (props.backend?.roles || []).filter((r) => r !== props.backend?.disk_role))
const diskRole = computed(() => props.backend?.disk_role || '')
const hasDisks = computed(
    () => !!diskRole.value && (props.cluster?.disks || []).some((d) => d.hypervisor.id === props.node?.hypervisor.id)
)

watch(
    () => props.show,
    (show) => {
        if (!show) return
        roles.value = (props.node?.roles || []).filter((r) => r !== diskRole.value)
        submitError.value = ''
    }
)

const toggle = (role: StorageRole) => {
    let next = roles.value.includes(role) ? roles.value.filter((r) => r !== role) : [...roles.value, role]
    // A client only uses the storage: it goes with the first daemon role, and comes back when none is left
    if (role === 'client' && next.includes('client')) next = ['client']
    else if (role !== 'client') next = next.filter((r) => r !== 'client')
    roles.value = offered.value.filter((r) => next.includes(r))
}

const wanted = computed<StorageRole[]>(() => {
    const list = [...roles.value]
    if (hasDisks.value) list.push(diskRole.value as StorageRole)
    return list.length ? list : ['client']
})
const unchanged = computed(() => {
    const before = [...(props.node?.roles || [])].sort().join(',')
    return before === [...wanted.value].sort().join(',')
})

const submit = async () => {
    if (!props.cluster || !props.node?.hypervisor.id || unchanged.value) return
    submitting.value = true
    submitError.value = ''
    try {
        const task = await storageClustersApi.changeRoles(props.cluster.id, props.node.hypervisor.id, wanted.value)
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
        :title="t('storage.clusterDetail.changeRolesTitle', { host: node?.hypervisor.name || '-' })"
        size="md"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">{{ t('storage.clusterDetail.changeRolesIntro') }}</p>
            <div class="roles">
                <label v-for="r in offered" :key="r" class="role-chip" :class="{ active: roles.includes(r) }">
                    <input type="checkbox" :checked="roles.includes(r)" @change="toggle(r)" />
                    {{ roleText(t, te, r) }}
                </label>
            </div>
            <p v-if="diskRole" class="hint">
                {{
                    hasDisks
                        ? t('storage.clusterDetail.diskRoleKept', { role: roleText(t, te, diskRole) })
                        : t('storage.clusterDetail.diskRoleNone', { role: roleText(t, te, diskRole) })
                }}
            </p>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="submitting || unchanged">
                {{ submitting ? t('storage.cluster.starting') : t('storage.cluster.start') }}
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
.hint {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.hint {
    font-size: var(--font-size-xs);
}

.roles {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
}

.role-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.role-chip.active {
    border-color: var(--primary-color);
    background: var(--primary-light);
}

.footer-error {
    margin-right: auto;
}
</style>
