<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import {
    migrationsApi,
    MIGRATION_ACTIVE_STATUSES,
    type Migration,
    type CreateMigrationPayload,
} from '../../api/migrations'
import { instancesApi, type Instance, type InstanceListResponse } from '../../api/instances'
import { hypervisorsApi, type Hypervisor, type HyperListResponse } from '../../api/hypervisors'
import { Search as SearchIcon, ArrowRightLeft, Plus, RefreshCw, Check, Copy } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { useI18n } from 'vue-i18n'
import { useRegionStore } from '../../stores/region'
import { formatDateTime } from '../../utils/format'
import { errorMessage } from '../../utils/error'
import BaseModal from '../../components/modals/BaseModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const { t, te } = useI18n()
const region = useRegionStore()
const toast = useToast()

const { copiedId, copyId } = useCopyId()

// 分页与搜索都在服务端做
const {
    items: migrationList,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    order,
    toggleSort,
    load: fetchMigrations,
    reload: reloadMigrations,
} = useListQuery<Migration>(
    async ({ offset, limit, query, order }) => {
        const response = await migrationsApi.fetchMigrations({ offset, limit, order, query: query || undefined })
        return { items: response.migrations || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

// Create Migration State
const createModalVisible = ref(false)
const creatingMigration = ref(false)
const resourcesLoading = ref(false)
const availableInstances = ref<Instance[]>([])
const availableHypervisors = ref<Hypervisor[]>([])

const newMigrationForm = ref({
    instance_id: '',
    target_hyper: '' as number | '',
})

// 选中实例当前所在节点的主机名（接口返回的 hypervisor 就是 hostname）。
// 目标节点下拉框据此标注并禁选该节点：迁到同一节点后端会直接跳过，
// 用户却以为任务已提交
const selectedInstanceHyper = computed(() => {
    const inst = availableInstances.value.find((i) => i.id === newMigrationForm.value.instance_id)
    return inst?.hypervisor || ''
})

// 换实例后，原先选中的目标节点可能正是新实例所在节点，这里清掉避免提交到无效目标
watch(
    () => newMigrationForm.value.instance_id,
    () => {
        const current = availableHypervisors.value.find((h) => h.hostname === selectedInstanceHyper.value)
        if (current && newMigrationForm.value.target_hyper === current.hostid) {
            newMigrationForm.value.target_hyper = ''
        }
    }
)

// 按所在节点筛选待迁移的云服务器（节点多、虚拟机多时便于定位）
const instanceHyperFilter = ref('')
const filteredInstances = computed(() =>
    instanceHyperFilter.value
        ? availableInstances.value.filter((i) => i.hypervisor === instanceHyperFilter.value)
        : availableInstances.value
)

// 筛选后已选实例可能不在列表里，清掉以免提交一个界面上看不见的实例
watch(instanceHyperFilter, () => {
    if (
        newMigrationForm.value.instance_id &&
        !filteredInstances.value.some((i) => i.id === newMigrationForm.value.instance_id)
    ) {
        newMigrationForm.value.instance_id = ''
    }
})

// 有迁移进行中时每 5 秒自动刷新，展示进度。
// 取数交给 useListQuery 之后没有 finally 可以挂，改成每次列表更新（每次加载都会
// 重新赋值）后重新排期，切换区域、翻页、搜索之后同样能接上
let refreshTimer: ReturnType<typeof setTimeout> | null = null
const hasActiveMigration = computed(() =>
    migrationList.value.some((m) => MIGRATION_ACTIVE_STATUSES.includes((m.status || '').toLowerCase()))
)

watch(migrationList, () => {
    if (refreshTimer) clearTimeout(refreshTimer)
    // 静默刷新：不显示加载态，失败时保留列表（后台每 5 秒一次，一次网络抖动
    // 不该把用户正在看的迁移列表清空）
    if (hasActiveMigration.value) refreshTimer = setTimeout(() => fetchMigrations(true), 5000)
})

// 缺键时 t() 返回键路径本身，必须用 te() 判断后再回退到原始状态串。
// progress 可选：只有迁移记录自身的状态需要按进度细分，阶段任务状态（in_progress/completed）不传
const getStatusText = (status: string, progress?: number) => {
    const s = (status || '').toLowerCase()
    if (!s) return '-'
    // 复制磁盘和内存的整个过程状态都停在 target_prepared，而它描述的是"已经准备好"这件过去的事，
    // 和还在推进的进度条对不上，看起来像卡住了；这里按进度显示当前真正在做的事
    if (s === 'target_prepared' && (progress ?? 0) > 0) return t('dashboard.migrationStatus.copying')
    if (s === 'source_prepared') return t('dashboard.migrationStatus.finalizing')
    const key = `dashboard.migrationStatus.${s}`
    return te(key) ? t(key) : status
}

// 节点显示成名字而不是编号；名字取不到时回退到编号。
// target_hyper 为 -1 表示尚未由调度器选出目标节点，此时没有节点可显示
const hyperLabel = (id?: number, name?: string) => {
    if (name) return name
    if (id === undefined || id === null) return '-'
    if (id < 0) return t('dashboard.migrationForm.autoSelect')
    return String(id)
}

const getTypeText = (type: string) => {
    const s = (type || '').toLowerCase()
    if (!s) return ''
    const key = `dashboard.migrationTypeValue.${s}`
    return te(key) ? t(key) : type
}

// 排序在服务端做（sortField 是数据库列名，和列 key 不一定同名）。
// 实例名、源/目标节点名都不在 migrations 表里（迁移记录只存实例 ID 和节点编号，
// 名字是另外查出来的），按这些列排会文不对题，所以不提供排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId'), sortable: true },
    { key: 'instance', label: t('dashboard.table.instanceId') },
    { key: 'type', label: t('dashboard.table.type'), sortable: true },
    { key: 'sourceNode', label: t('dashboard.table.sourceNode') },
    { key: 'destNode', label: t('dashboard.table.destNode') },
    { key: 'status', label: t('dashboard.table.status'), sortable: true },
    { key: 'progress', label: t('dashboard.migrationDetail.progressShort'), sortable: true },
    { key: 'creator', label: t('dashboard.table.creator'), sortable: true, sortField: 'creater_name' },
    { key: 'createdAt', label: t('dashboard.table.createdAt'), sortable: true, sortField: 'created_at' },
])

// Create Modal Logic
const openCreateModal = () => {
    newMigrationForm.value = {
        instance_id: '',
        target_hyper: '',
    }
    instanceHyperFilter.value = ''
    createModalVisible.value = true
    fetchResources()
}

const closeCreateModal = () => {
    createModalVisible.value = false
}

const fetchResources = async () => {
    resourcesLoading.value = true
    try {
        const [instRes, hypRes] = await Promise.all([instancesApi.fetchInstances(), hypervisorsApi.fetchHypervisors()])

        const instData = instRes as InstanceListResponse | Instance[]
        availableInstances.value = Array.isArray(instData) ? instData : instData.instances || []

        // 接口返回的字段是 hypers，不是 hypervisors。这里原先写成 as any，字段名拼错也能编译通过，
        // 结果目标节点下拉框永远取到 undefined 而回退成空数组；改用真实类型让同类错误在编译期暴露
        const hypData = hypRes as HyperListResponse | Hypervisor[]
        availableHypervisors.value = Array.isArray(hypData) ? hypData : hypData.hypers || []
    } catch (err) {
        console.error('Error fetching resources for migration:', err)
    } finally {
        resourcesLoading.value = false
    }
}

const handleCreateMigration = async () => {
    if (!newMigrationForm.value.instance_id) {
        toast.error(t('messages.selectInstanceToMigrate'))
        return
    }

    creatingMigration.value = true
    try {
        // 接口要求 name 与 instances 数组；目标节点用 hostid。
        // 不传 force：它是「源节点已离线时强行迁移」，不是冷迁移开关，本地存储下还会被直接拒绝。
        // 热迁移 / 冷迁移由后端按虚拟机当前状态自行决定，不由用户选择
        const inst = availableInstances.value.find((i) => i.id === newMigrationForm.value.instance_id)
        const payload: CreateMigrationPayload = {
            name: `ui-${(inst?.hostname || 'migration').slice(0, 20)}-${Date.now().toString().slice(-6)}`,
            instances: [{ id: newMigrationForm.value.instance_id }],
        }
        if (newMigrationForm.value.target_hyper !== '') {
            payload.target_hyper = Number(newMigrationForm.value.target_hyper)
        }

        await migrationsApi.createMigration(payload)
        // 列表按创建时间倒序，新建的在第一页
        await reloadMigrations()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err) {
        console.error('Failed to start migration:', err)
        toast.error(errorMessage(err, t('messages.startMigrationFailed')))
    } finally {
        creatingMigration.value = false
    }
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchMigrations()
    }
})

onUnmounted(() => {
    if (refreshTimer) clearTimeout(refreshTimer)
})
</script>

<template>
    <div class="vpc-list-container">
        <PageToolbar v-model:search="searchQuery">
            <template #actions>
                <button
                    class="btn btn-secondary btn-sm btn-icon"
                    @click="() => fetchMigrations()"
                    :title="$t('actions.refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
                <button class="btn btn-primary btn-sm" @click="openCreateModal">
                    <Plus :size="14" /> {{ $t('dashboard.buttons.startMigration') }}
                </button>
            </template>
        </PageToolbar>

        <DataTable
            :columns="columns"
            :rows="migrationList"
            row-key="id"
            :loading="loading"
            :error="loadError"
            :order="order"
            @update:order="toggleSort"
            @retry="fetchMigrations()"
        >
            <template #empty>
                <div v-if="searchQuery">
                    <SearchIcon :size="48" style="opacity: 0.3; margin-bottom: 16px" />
                    <p>{{ $t('messages.noResults') }}</p>
                </div>
                <div v-else class="empty-state">
                    <ArrowRightLeft :size="48" style="opacity: 0.2; margin-bottom: 16px" />
                    <p>{{ $t('messages.noData') }}</p>
                </div>
            </template>

            <template #cell-name="{ row: m }">
                <router-link :to="{ name: 'migration-detail', params: { id: m.id.toString() } }" class="resource-link">
                    <div class="resource-info">
                        <div class="resource-icon">
                            <ArrowRightLeft :size="16" />
                        </div>
                        <div>
                            <div class="resource-name">{{ $t('dashboard.table.migration') }}</div>
                            <div class="resource-id-row">
                                <span class="resource-id" :title="m.id.toString()"
                                    >{{ m.id.toString().slice(0, 8)
                                    }}{{ m.id.toString().length > 8 ? '...' : '' }}</span
                                >
                                <button
                                    class="copy-btn-mini"
                                    @click.stop.prevent="copyId(m.id.toString())"
                                    :title="t('actions.copy')"
                                    :aria-label="t('actions.copy')"
                                >
                                    <Check
                                        v-if="copiedId === m.id.toString()"
                                        :size="10"
                                        style="color: var(--success-color)"
                                    />
                                    <Copy v-else :size="10" />
                                </button>
                            </div>
                        </div>
                    </div>
                </router-link>
            </template>

            <template #cell-instance="{ row: m }">
                <div class="resource-id-row">
                    <span class="resource-id" :title="m.instance?.id">{{
                        m.instance?.hostname || (m.instance?.id || '').slice(0, 8) || '-'
                    }}</span>
                    <button
                        v-if="m.instance?.id"
                        class="copy-btn-mini"
                        @click.stop.prevent="copyId(m.instance!.id)"
                        :title="t('actions.copy')"
                        :aria-label="t('actions.copy')"
                    >
                        <Check v-if="copiedId === m.instance?.id" :size="10" style="color: var(--success-color)" />
                        <Copy v-else :size="10" />
                    </button>
                </div>
            </template>

            <template #cell-type="{ row: m }">{{ getTypeText(m.type) || $t('messages.unnamed') }}</template>
            <template #cell-sourceNode="{ row: m }">{{ hyperLabel(m.source_hyper, m.source_hyper_name) }}</template>
            <template #cell-destNode="{ row: m }">{{ hyperLabel(m.target_hyper, m.target_hyper_name) }}</template>

            <template #cell-status="{ row: m }">
                <StatusBadge :status="m.status" :label="getStatusText(m.status, m.progress)" />
            </template>

            <template #cell-progress="{ row: m }">
                <div v-if="m.total" class="progress-cell">
                    <div class="progress-track">
                        <div class="progress-fill" :style="{ width: (m.progress || 0) + '%' }"></div>
                    </div>
                    <span class="progress-value">{{ m.progress || 0 }}%</span>
                </div>
                <span v-else class="text-secondary">-</span>
            </template>

            <template #cell-creator="{ row: m }">{{ m.creater_name || '-' }}</template>
            <template #cell-createdAt="{ row: m }"
                ><span class="mono-value">{{ formatDateTime(m.created_at) }}</span></template
            >

            <template #footer>
                <PaginationBar
                    :page="page"
                    :page-size="pageSize"
                    :total="total"
                    @update:page="page = $event"
                    @update:page-size="pageSize = $event"
                />
            </template>
        </DataTable>

        <!-- Create Migration Modal -->
        <BaseModal
            :show="createModalVisible"
            :title="$t('dashboard.migrationForm.title')"
            :loading="creatingMigration"
            form
            @close="closeCreateModal"
            @submit="handleCreateMigration"
        >
            <div v-if="resourcesLoading">
                <div class="loading-spinner" style="margin: 40px auto"></div>
            </div>

            <div v-else>
                <div class="form-group row-gap">
                    <label class="form-label">{{ $t('dashboard.migrationForm.filterByNode') }}</label>
                    <select v-model="instanceHyperFilter" class="form-select full-width">
                        <option value="">{{ $t('dashboard.migrationForm.allNodes') }}</option>
                        <option v-for="hyp in availableHypervisors" :key="hyp.uuid" :value="hyp.hostname">
                            {{ hyp.hostname }} ({{ hyp.hostid }})
                        </option>
                    </select>
                </div>

                <div class="form-group row-gap">
                    <label class="form-label"
                        >{{ $t('dashboard.migrationForm.instanceToMigrate') }} <span class="text-error">*</span></label
                    >
                    <select v-model="newMigrationForm.instance_id" class="form-select full-width">
                        <option value="" disabled>{{ $t('dashboard.forms.placeholder.none') }}</option>
                        <option v-if="!filteredInstances.length" value="" disabled>
                            {{ $t('dashboard.migrationForm.noInstanceOnNode') }}
                        </option>
                        <option v-for="inst in filteredInstances" :key="inst.id" :value="inst.id">
                            {{ inst.hostname || inst.name
                            }}<template v-if="inst.hypervisor"> · {{ inst.hypervisor }}</template> ({{ inst.id }})
                        </option>
                    </select>
                </div>

                <div class="form-group row-gap">
                    <label class="form-label">{{ $t('dashboard.migrationForm.destinationNode') }}</label>
                    <select v-model="newMigrationForm.target_hyper" class="form-select full-width">
                        <option value="">{{ $t('dashboard.migrationForm.autoSelect') }}</option>
                        <option
                            v-for="hyp in availableHypervisors"
                            :key="hyp.uuid"
                            :value="hyp.hostid"
                            :disabled="!!selectedInstanceHyper && hyp.hostname === selectedInstanceHyper"
                        >
                            {{ hyp.hostname }} ({{ hyp.hostid }})<template
                                v-if="hyp.hostname === selectedInstanceHyper"
                            >
                                — {{ $t('dashboard.migrationForm.currentNode') }}</template
                            >
                        </option>
                    </select>
                    <small class="text-secondary" style="display: block; margin-top: 4px">{{
                        $t('messages.placementRouteHint')
                    }}</small>
                </div>

                <div class="form-group row-gap">
                    <small class="text-secondary">{{ $t('dashboard.migrationForm.typeAutoHint') }}</small>
                </div>
            </div>

            <template #footer>
                <template v-if="!resourcesLoading">
                    <button
                        type="button"
                        class="btn btn-secondary"
                        @click="closeCreateModal"
                        :disabled="creatingMigration"
                    >
                        {{ $t('actions.cancel') }}
                    </button>
                    <button type="submit" class="btn btn-primary" :disabled="creatingMigration">
                        <span
                            v-if="creatingMigration"
                            class="loading-spinner"
                            style="width: 16px; height: 16px; border-width: 2px"
                        ></span>
                        {{
                            creatingMigration
                                ? $t('dashboard.migrationForm.starting')
                                : $t('dashboard.buttons.startMigration')
                        }}
                    </button>
                </template>
            </template>
        </BaseModal>
    </div>
</template>

<style scoped>
/* .resource-info etc. are global from index.css */

.resource-link {
    text-decoration: none;
    display: block;
    padding: 4px 0;
    border-radius: var(--radius-sm);
    transition: all 0.15s;
}

.resource-link:hover .resource-name {
    color: var(--primary-600);
    text-decoration: underline;
}

.mono-value {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
}

.empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
}

/* Modal Styles */
.row-gap {
    margin-bottom: 16px;
}
.full-width {
    width: 100%;
    padding: 8px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: transparent;
    color: var(--text-primary);
}

</style>
