<script setup lang="ts">
// The checks every host reported in the precheck step of a task (scripts/kvm/storage/stc_precheck.sh): a precheck
// task, or the first step of a deployment or an expansion
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusBadge from '../base/StatusBadge.vue'
import type { PrecheckResult, StorageRun, StorageTask } from '../../api/storageClusters'
import {
    checkNameText,
    checkStatusText,
    checkStatusVariant,
    runStatusText,
    taskVariant,
} from '../../utils/storageCluster'

const props = defineProps<{ task: StorageTask | null }>()
const { t, te } = useI18n()

const isPrecheckResult = (value: unknown): value is PrecheckResult =>
    !!value && typeof value === 'object' && Array.isArray((value as PrecheckResult).items)

const hosts = computed(() => {
    const step = props.task?.steps?.find((s) => s.name === 'precheck')
    if (!step) return []
    // The runs that count: the last attempt on every host
    const byHost = new Map<number, StorageRun>()
    for (const run of step.runs) {
        const prev = byHost.get(run.hostid)
        if (!prev || run.attempt > prev.attempt) byHost.set(run.hostid, run)
    }
    return [...byHost.values()]
        .sort((a, b) => a.hostid - b.hostid)
        .map((run) => {
            const result = isPrecheckResult(run.result) ? run.result : null
            const items = result?.items || []
            const worst = items.some((i) => i.status === 'fail')
                ? 'fail'
                : items.some((i) => i.status === 'warn')
                  ? 'warn'
                  : 'ok'
            return { run, result, items, worst }
        })
})

defineExpose({ hosts })
</script>

<template>
    <div v-if="hosts.length" class="precheck-hosts">
        <div v-for="h in hosts" :key="h.run.id" class="precheck-host">
            <div class="precheck-host-header">
                <span class="precheck-host-name">{{ h.run.hypervisor?.name || h.run.hostid }}</span>
                <StatusBadge
                    v-if="h.result"
                    :variant="checkStatusVariant(h.worst)"
                    :label="checkStatusText(t, te, h.worst)"
                />
                <StatusBadge v-else :variant="taskVariant(h.run.status)" :label="runStatusText(t, te, h.run.status)" />
                <span v-if="h.result?.facts" class="facts">
                    {{ h.result.facts.os }} · {{ h.result.facts.kernel }} · {{ h.result.facts.hostname }}
                </span>
            </div>
            <div v-if="!h.result" class="muted-line">
                {{ h.run.message || t('storage.cluster.waitingResult') }}
            </div>
            <table v-else class="check-table">
                <tbody>
                    <tr v-for="item in h.items" :key="item.name" :class="`check-${item.status}`">
                        <td class="check-status">
                            <StatusBadge
                                :variant="checkStatusVariant(item.status)"
                                :label="checkStatusText(t, te, item.status)"
                            />
                        </td>
                        <td class="check-name">{{ checkNameText(t, te, item.name) }}</td>
                        <td class="check-detail">{{ item.detail }}</td>
                    </tr>
                </tbody>
            </table>
        </div>
    </div>
</template>

<style scoped>
.precheck-hosts {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
}

.precheck-host-header {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-2);
}

.precheck-host-name {
    font-weight: 600;
}

.facts {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.muted-line {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

.check-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--font-size-sm);
}

.check-table td {
    padding: 6px 8px;
    border-top: 1px solid var(--border-light);
    vertical-align: top;
}

.check-status {
    width: 90px;
}

.check-name {
    width: 260px;
    font-weight: 500;
    word-break: break-all;
}

.check-detail {
    color: var(--text-secondary);
    word-break: break-word;
}

.check-fail .check-detail {
    color: var(--error-color);
}
</style>
