<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { migrationsApi, MIGRATION_ACTIVE_STATUSES, type Migration } from '../../api/migrations'
import { ArrowLeft, ArrowRightLeft, Copy, Check } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { formatBytes, formatDateTime } from '../../utils/format'
import StatusBadge from '../../components/base/StatusBadge.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import { useCopyId } from '../../composables/useCopyId'
import { useGoBack } from '../../composables/useGoBack'
import { errorMessage } from '../../utils/error'

const { t, te } = useI18n()
const route = useRoute()
const migration = ref<Migration | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const { copiedId: copiedField, copyId: copyToClipboard } = useCopyId()

// 迁移进行中时每 3 秒自动刷新，展示阶段与进度
const inProgress = computed(() => MIGRATION_ACTIVE_STATUSES.includes((migration.value?.status || '').toLowerCase()))
let refreshTimer: ReturnType<typeof setTimeout> | null = null

const fetchMigrationDetail = async (silent = false) => {
    if (!silent) loading.value = true
    error.value = null
    try {
        const id = route.params.id as string
        // GET /migrations/:id 直接返回 MigrationResponse，没有 { migration: ... } 外层包装
        migration.value = await migrationsApi.getMigration(id)
    } catch (err) {
        console.error('Failed to fetch migration detail:', err)
        if (!silent) error.value = errorMessage(err, t('dashboard.migrationDetail.loadError'))
    } finally {
        loading.value = false
        if (refreshTimer) clearTimeout(refreshTimer)
        if (inProgress.value) refreshTimer = setTimeout(() => fetchMigrationDetail(true), 3000)
    }
}

// 缺键时 t() 返回键路径本身，必须用 te() 判断后再回退到原始值。
// progress 可选：只有迁移记录自身的状态按进度细分，下方各阶段任务的状态不传
const getStatusText = (status: string, progress?: number) => {
    const s = (status || '').toLowerCase()
    if (!s) return '-'
    // 复制磁盘和内存期间状态一直是 target_prepared，而它描述的是"已经准备好"这件过去的事，
    // 与还在推进的进度条对不上，看起来像卡住了；这里按进度显示当前真正在做的事
    if (s === 'target_prepared' && (progress ?? 0) > 0) return t('dashboard.migrationStatus.copying')
    if (s === 'source_prepared') return t('dashboard.migrationStatus.finalizing')
    const key = `dashboard.migrationStatus.${s}`
    return te(key) ? t(key) : status
}

const getPhaseName = (name: string) => {
    if (!name) return '-'
    const key = `dashboard.migrationPhase.${name}`
    return te(key) ? t(key) : name
}

// 节点显示成名字而不是编号；名字取不到时回退到编号。
// target_hyper 为 -1 表示尚未由调度器选出目标节点
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

const goBack = useGoBack('migrations')

onMounted(() => fetchMigrationDetail())
onUnmounted(() => {
    if (refreshTimer) clearTimeout(refreshTimer)
})
</script>

<template>
    <div class="vpc-detail">
        <!-- Header -->
        <div class="detail-header">
            <button class="btn btn-ghost back-btn" @click="goBack">
                <ArrowLeft :size="18" />
                <span>{{ $t('dashboard.table.migration') }}</span>
            </button>
        </div>

        <!-- Loading State -->
        <div v-if="loading" class="loading-container">
            <div class="loading-spinner"></div>
            <p class="loading-text">{{ $t('messages.loading') }}</p>
        </div>

        <!-- Error State -->
        <div v-else-if="error" class="error-container card">
            <ArrowRightLeft :size="48" style="opacity: 0.3; margin-bottom: 16px" />
            <p class="text-secondary">{{ error }}</p>
            <button class="btn btn-primary btn-sm" @click="fetchMigrationDetail" style="margin-top: 12px">
                {{ $t('actions.refresh') }}
            </button>
        </div>

        <!-- Detail Content -->
        <div v-else-if="migration" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="title-info">
                    <div class="title-icon">
                        <ArrowRightLeft :size="28" />
                    </div>
                    <div>
                        <h2 class="resource-title">{{ $t('dashboard.migrationDetail.title') }}</h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ migration.id }}</span>
                            <button
                                class="copy-btn"
                                @click="copyToClipboard(migration.id.toString(), 'id')"
                                :title="$t('messages.copied')"
                            >
                                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <StatusBadge
                        :status="migration.status"
                        :label="getStatusText(migration.status, migration.progress)"
                    />
                </div>
            </div>

            <!-- Info Sections -->
            <div class="info-grid">
                <!-- Basic Info Card -->
                <div class="info-card card">
                    <h3 class="card-section-title">{{ $t('dashboard.migrationDetail.overview') }}</h3>
                    <div class="info-rows">
                        <InfoRow :label="$t('dashboard.migrationDetail.instanceId')" mono>{{
                            migration.instance?.hostname || migration.instance?.id || '-'
                        }}</InfoRow>
                        <InfoRow :label="$t('dashboard.migrationDetail.type')">{{
                            getTypeText(migration.type) || $t('messages.unnamed')
                        }}</InfoRow>
                        <InfoRow :label="$t('dashboard.table.creator')">{{ migration.creater_name || '-' }}</InfoRow>
                        <InfoRow :label="$t('dashboard.migrationDetail.createdAt')" mono>{{
                            formatDateTime(migration.created_at)
                        }}</InfoRow>
                        <InfoRow :label="$t('dashboard.migrationDetail.updatedAt')" mono>{{
                            formatDateTime(migration.updated_at)
                        }}</InfoRow>
                    </div>
                </div>

                <!-- Node Placement Card -->
                <div class="info-card card">
                    <h3 class="card-section-title">{{ $t('dashboard.migrationDetail.placementRoute') }}</h3>
                    <div class="info-rows">
                        <InfoRow :label="$t('dashboard.migrationDetail.sourceNode')">{{
                            hyperLabel(migration.source_hyper, migration.source_hyper_name)
                        }}</InfoRow>
                        <InfoRow :label="$t('dashboard.migrationDetail.destinationNode')">{{
                            hyperLabel(migration.target_hyper, migration.target_hyper_name)
                        }}</InfoRow>
                    </div>
                </div>

                <!-- Progress & Phases -->
                <div class="info-card card phases-card">
                    <h3 class="card-section-title">
                        {{ $t('dashboard.migrationDetail.progress') }}
                        <span v-if="inProgress" class="auto-refresh">{{
                            $t('dashboard.migrationDetail.autoRefresh')
                        }}</span>
                    </h3>
                    <div v-if="migration.total" class="progress-block">
                        <div class="progress-track">
                            <div class="progress-fill" :style="{ width: (migration.progress || 0) + '%' }"></div>
                        </div>
                        <div class="progress-text">
                            <span>{{ migration.progress || 0 }}%</span>
                            <span class="mono"
                                >{{ formatBytes(migration.transferred) }} / {{ formatBytes(migration.total) }}</span
                            >
                        </div>
                    </div>
                    <div class="phase-list">
                        <div v-for="(p, idx) in migration.phases || []" :key="idx" class="phase-row">
                            <StatusBadge :status="p.status" :label="getStatusText(p.status)" />
                            <div class="phase-info">
                                <div class="phase-name">{{ getPhaseName(p.name) }}</div>
                                <div class="phase-summary">{{ p.summary }}</div>
                                <div v-if="p.message" class="phase-message">{{ p.message }}</div>
                            </div>
                        </div>
                        <p v-if="!(migration.phases || []).length" class="text-secondary" style="margin: 0">-</p>
                    </div>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.phases-card {
    grid-column: 1 / -1;
}

.auto-refresh {
    margin-left: 8px;
    font-size: 12px;
    font-weight: 400;
    color: var(--gray-500);
}

.progress-block {
    margin-bottom: var(--spacing-4);
}

.progress-track {
    height: 8px;
    border-radius: 4px;
    background: var(--gray-100);
    overflow: hidden;
}

.progress-fill {
    height: 100%;
    background: var(--primary-500, #3b82f6);
    transition: width 0.4s ease;
}

.progress-text {
    display: flex;
    justify-content: space-between;
    margin-top: 6px;
    font-size: 13px;
    color: var(--gray-600);
}

.phase-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
}

.phase-row {
    display: flex;
    align-items: flex-start;
    gap: 10px;
}

.phase-name {
    font-weight: 500;
}

.phase-summary,
.phase-message {
    font-size: 12px;
    color: var(--gray-500);
    word-break: break-word;
}

.phase-message {
    color: var(--danger-600, #dc2626);
}

.vpc-detail {
    max-width: 1100px;
}

.detail-header {
    margin-bottom: var(--spacing-4);
}

.back-btn {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-md);
    transition: all 0.2s;
}

.back-btn:hover {
    color: var(--primary-color);
    background: var(--primary-50);
}

/* Loading */
.loading-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 80px 0;
}

.loading-text {
    margin-top: var(--spacing-3);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
}

/* Error */
.error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px 20px;
    text-align: center;
}

/* Title Bar */
.title-bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-5);
}

.title-info {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
}

.title-icon {
    width: 52px;
    height: 52px;
    border-radius: var(--radius-lg);
    background: linear-gradient(135deg, var(--primary-50), var(--primary-100));
    color: var(--primary-color);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
}

.resource-title {
    margin: 0 0 4px 0;
    font-size: var(--font-size-xl);
    font-weight: var(--font-weight-semibold);
    color: var(--text-primary);
}

.resource-id-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.resource-id-text {
    font-size: var(--font-size-xs);
    color: var(--text-light);
    font-family: var(--font-family-mono);
}

.copy-btn {
    background: none;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    padding: 2px 5px;
    cursor: pointer;
    color: var(--text-light);
    display: inline-flex;
    align-items: center;
    transition: all 0.15s;
}

.copy-btn:hover {
    color: var(--primary-color);
    border-color: var(--primary-200);
    background: var(--primary-50);
}

.copied-icon {
    color: var(--success-color);
}

.title-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

/* Info Grid */
.info-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-5);
}

.info-card {
    padding: var(--spacing-5);
}

.card-section-title {
    margin: 0 0 var(--spacing-4) 0;
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-semibold);
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding-bottom: var(--spacing-3);
    border-bottom: 1px solid var(--border-light);
}

.info-rows {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

@media (max-width: 768px) {
    .info-grid {
        grid-template-columns: 1fr;
    }

    .title-bar {
        flex-direction: column;
        align-items: flex-start;
        gap: var(--spacing-3);
    }
}
</style>
