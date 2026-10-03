<script setup lang="ts">
// Storage clusters (shared-storage-design.md §13.3) and the tasks that build and change them: the creation wizard,
// the precheck of hosts and the selftest of the task path start here; a cluster is changed on its detail page.
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { RefreshCw, ServerCog, ClipboardCheck, Activity, ListChecks, Database, Plus } from 'lucide-vue-next'
import {
    storageClustersApi,
    TASK_LIVE_STATUSES,
    type StorageCluster,
    type StorageTask,
} from '../../api/storageClusters'
import { useListQuery } from '../../composables/useListQuery'
import { useRegionStore } from '../../stores/region'
import { formatDateTime } from '../../utils/format'
import CapacityBar from '../../components/storage/CapacityBar.vue'
import {
    storageKindText,
    clusterStatusText,
    clusterHealthText,
    taskKindText,
    taskStatusText,
    taskVariant,
} from '../../utils/storageCluster'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DetailTabs, { type Tab } from '../../components/base/DetailTabs.vue'
import StoragePrecheckModal from '../../components/storage/StoragePrecheckModal.vue'
import StorageSelftestModal from '../../components/storage/StorageSelftestModal.vue'

const { t, te } = useI18n()
const router = useRouter()
const region = useRegionStore()

type TabId = 'clusters' | 'tasks'
const activeTab = ref<TabId>('clusters')

const clusterQuery = useListQuery<StorageCluster>(
    async ({ offset, limit }) => {
        const response = await storageClustersApi.list({ offset, limit })
        return { items: response.storage_clusters || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)
const taskQuery = useListQuery<StorageTask>(
    async ({ offset, limit }) => {
        const response = await storageClustersApi.listTasks({ offset, limit })
        return { items: response.tasks || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

const tabs = computed<Tab[]>(() => [
    { id: 'clusters', label: t('storage.cluster.clustersTab'), icon: Database, count: clusterQuery.total.value },
    { id: 'tasks', label: t('storage.cluster.tasksTab'), icon: ListChecks, count: taskQuery.total.value },
])

const clusterColumns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.name') },
    { key: 'kind', label: t('storage.cluster.kind') },
    { key: 'mode', label: t('storage.cluster.mode') },
    { key: 'status', label: t('storage.status') },
    { key: 'health', label: t('storage.cluster.health'), hideBelow: 1280 },
    { key: 'counts', label: t('storage.clusterDetail.counts'), hideBelow: 1024 },
    { key: 'capacity', label: t('storage.capacity') },
    { key: 'created_at', label: t('dashboard.table.createdAt'), hideBelow: 1440 },
])

const taskColumns = computed<Column[]>(() => [
    { key: 'kind', label: t('storage.cluster.task') },
    { key: 'cluster', label: t('storage.cluster.cluster') },
    { key: 'status', label: t('storage.status') },
    { key: 'message', label: t('storage.cluster.message'), hideBelow: 1280 },
    { key: 'creator', label: t('storage.cluster.creator'), hideBelow: 1024 },
    { key: 'created_at', label: t('storage.cluster.createdAt') },
    { key: 'finished_at', label: t('storage.cluster.finishedAt'), hideBelow: 1280 },
])

// Tasks still going: refresh quietly so their status moves on its own
let timer: ReturnType<typeof setInterval> | null = null
const anyLive = computed(() => taskQuery.items.value.some((task) => TASK_LIVE_STATUSES.includes(task.status)))
watch(
    anyLive,
    (live) => {
        if (live && !timer) {
            timer = setInterval(() => taskQuery.load(true), 5000)
        } else if (!live && timer) {
            clearInterval(timer)
            timer = null
        }
    },
    { immediate: true }
)
onUnmounted(() => {
    if (timer) clearInterval(timer)
})

const refresh = () => {
    clusterQuery.load()
    taskQuery.load()
}

const showPrecheck = ref(false)
const showSelftest = ref(false)
const openTask = (task: StorageTask) => {
    showPrecheck.value = false
    showSelftest.value = false
    router.push({ name: 'storage-task-detail', params: { id: task.id } })
}

const clusterPage = clusterQuery.page
const clusterPageSize = clusterQuery.pageSize
const taskPage = taskQuery.page
const taskPageSize = taskQuery.pageSize

onMounted(() => {
    if (region.currentRegionId) refresh()
})
</script>

<template>
    <div class="vpc-list-container">
        <div class="page-actions">
            <DetailTabs v-model="activeTab" :tabs="tabs" />
            <div class="actions">
                <button class="btn btn-secondary btn-sm btn-icon" :title="t('actions.refresh')" @click="refresh">
                    <RefreshCw
                        :size="14"
                        :class="{ spinning: clusterQuery.loading.value || taskQuery.loading.value }"
                    />
                </button>
                <button
                    class="btn btn-secondary btn-sm"
                    :title="t('storage.cluster.selftestHint')"
                    @click="showSelftest = true"
                >
                    <Activity :size="14" />
                    <span>{{ t('storage.cluster.selftest') }}</span>
                </button>
                <button class="btn btn-secondary btn-sm" @click="showPrecheck = true">
                    <ClipboardCheck :size="14" />
                    <span>{{ t('storage.cluster.precheck') }}</span>
                </button>
                <button class="btn btn-primary btn-sm" @click="router.push({ name: 'storage-cluster-create' })">
                    <Plus :size="14" />
                    <span>{{ t('storage.wizard.create') }}</span>
                </button>
            </div>
        </div>

        <DataTable
            v-if="activeTab === 'clusters'"
            :columns="clusterColumns"
            :rows="clusterQuery.items.value"
            row-key="id"
            :loading="clusterQuery.loading.value"
            :error="clusterQuery.error.value"
            @retry="() => clusterQuery.load()"
        >
            <template #empty>
                <div class="empty-state">
                    <ServerCog :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p class="empty-title">{{ t('storage.cluster.emptyTitle') }}</p>
                    <p class="empty-hint">{{ t('storage.cluster.emptyHint') }}</p>
                    <button class="btn btn-primary btn-sm" @click="router.push({ name: 'storage-cluster-create' })">
                        <Plus :size="14" />
                        <span>{{ t('storage.wizard.create') }}</span>
                    </button>
                </div>
            </template>
            <template #cell-name="{ row: c }">
                <router-link :to="{ name: 'storage-cluster-detail', params: { id: c.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon"><ServerCog :size="16" /></div>
                        <div>
                            <div class="resource-name">
                                {{ c.name }}
                                <span v-if="c.unsupported" class="badge badge-warning">{{
                                    t('storage.cluster.unsupported')
                                }}</span>
                                <span v-if="c.active_task || c.active_pool_task" class="badge badge-primary">{{
                                    t('storage.clusterDetail.busy')
                                }}</span>
                            </div>
                            <div class="resource-id-row">
                                <span class="resource-id">{{ c.description || c.id.slice(0, 8) }}</span>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>
            <template #cell-counts="{ row: c }">{{
                t('storage.clusterDetail.countsValue', {
                    nodes: c.node_count,
                    disks: c.disk_count,
                    pools: c.pool_count,
                })
            }}</template>
            <template #cell-capacity="{ row: c }">
                <CapacityBar
                    v-if="c.capacity_bytes"
                    :capacity="c.capacity_bytes"
                    :used="c.capacity_bytes - c.free_bytes"
                    :allocated="c.allocated_bytes"
                />
                <span v-else class="text-secondary">-</span>
            </template>
            <template #cell-kind="{ row: c }">{{ storageKindText(t, te, c.kind) }}</template>
            <template #cell-mode="{ row: c }">{{
                te(`storage.cluster.modes.${c.mode}`) ? t(`storage.cluster.modes.${c.mode}`) : c.mode
            }}</template>
            <template #cell-status="{ row: c }">
                <StatusBadge :status="c.status" :label="clusterStatusText(t, te, c.status)" />
            </template>
            <template #cell-health="{ row: c }">
                <StatusBadge :status="c.health" :label="clusterHealthText(t, te, c.health)" />
            </template>
            <template #cell-created_at="{ row: c }">{{ formatDateTime(c.created_at) }}</template>
            <template #footer>
                <PaginationBar
                    :page="clusterPage"
                    :page-size="clusterPageSize"
                    :total="clusterQuery.total.value"
                    @update:page="clusterPage = $event"
                    @update:page-size="clusterPageSize = $event"
                />
            </template>
        </DataTable>

        <DataTable
            v-else
            :columns="taskColumns"
            :rows="taskQuery.items.value"
            row-key="id"
            :loading="taskQuery.loading.value"
            :error="taskQuery.error.value"
            @retry="() => taskQuery.load()"
        >
            <template #empty>
                <div class="empty-state">
                    <ListChecks :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p>{{ t('storage.cluster.noTasks') }}</p>
                </div>
            </template>
            <template #cell-kind="{ row: task }">
                <router-link :to="{ name: 'storage-task-detail', params: { id: task.id } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon"><ListChecks :size="16" /></div>
                        <div>
                            <div class="resource-name">{{ taskKindText(t, te, task.kind) }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id">{{ task.id.slice(0, 8) }}</span>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>
            <template #cell-cluster="{ row: task }">
                <span v-if="task.cluster?.id">{{ task.cluster.name }}</span>
                <span v-else class="text-secondary">{{ t('storage.cluster.noCluster') }}</span>
            </template>
            <template #cell-status="{ row: task }">
                <StatusBadge :variant="taskVariant(task.status)" :label="taskStatusText(t, te, task.status)" />
            </template>
            <template #cell-message="{ row: task }">
                <span class="message-cell" :title="task.message">{{ task.message || '-' }}</span>
            </template>
            <template #cell-creator="{ row: task }">{{ task.creator || '-' }}</template>
            <template #cell-created_at="{ row: task }">{{ formatDateTime(task.created_at) }}</template>
            <template #cell-finished_at="{ row: task }">{{
                task.finished_at ? formatDateTime(task.finished_at) : '-'
            }}</template>
            <template #footer>
                <PaginationBar
                    :page="taskPage"
                    :page-size="taskPageSize"
                    :total="taskQuery.total.value"
                    @update:page="taskPage = $event"
                    @update:page-size="taskPageSize = $event"
                />
            </template>
        </DataTable>

        <StoragePrecheckModal :show="showPrecheck" @close="showPrecheck = false" @created="openTask" />
        <StorageSelftestModal :show="showSelftest" @close="showSelftest = false" @created="openTask" />
    </div>
</template>

<style scoped>
.page-actions {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: var(--spacing-3);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-4);
}

.page-actions :deep(.detail-tabs) {
    margin-bottom: 0;
    flex: 1;
}

.actions {
    display: flex;
    gap: var(--spacing-2);
    padding-bottom: var(--spacing-2);
}

.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: var(--spacing-6) var(--spacing-4);
}

.empty-title {
    font-weight: 600;
    margin: 0 0 var(--spacing-2);
}

.empty-hint {
    max-width: 560px;
    margin: 0 0 var(--spacing-4);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
    text-align: center;
}

.message-cell {
    display: inline-block;
    max-width: 360px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
    color: var(--text-secondary);
}

.resource-name .badge {
    margin-left: 6px;
    font-size: 0.6875rem;
}
</style>
