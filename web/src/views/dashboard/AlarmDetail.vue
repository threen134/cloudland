<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { alarmsApi, RULE_TYPES, type NodeAlarmRule, type NodeAlarmRuleListResponse } from '../../api/alarms'
import { errorMessage } from '../../utils/error'
import { ArrowLeft, AlertTriangle, Copy, Check, Trash2, Pencil, Power } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import NodeAlarmRuleEditModal from '../../components/alarm/NodeAlarmRuleEditModal.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import { useCopyId } from '../../composables/useCopyId'
import { useGoBack } from '../../composables/useGoBack'

const { t } = useI18n()
const toast = useToast()
const route = useRoute()
const router = useRouter()
const alarm = ref<NodeAlarmRule | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const { copiedId: copiedField, copyId: copyToClipboard } = useCopyId()

// Delete
const showDeleteConfirm = ref(false)
const deleting = ref(false)

const fetchAlarmDetail = async () => {
    loading.value = true
    error.value = null
    try {
        const uuid = route.params.id as string
        const response = await alarmsApi.fetchAlarmRules({ uuid })
        // 接口返回 { status, data, count }，这里保留"直接是数组"的兼容分支
        const data = response as NodeAlarmRuleListResponse | NodeAlarmRule[]
        const rules = Array.isArray(data) ? data : data.data || []
        alarm.value = rules.length > 0 ? rules[0] : null
        if (!alarm.value) {
            error.value = t('messages.notFound')
        }
    } catch (err) {
        console.error('Failed to fetch alarm detail:', err)
        error.value = errorMessage(err, t('messages.error'))
    } finally {
        loading.value = false
    }
}

// 编辑与启停：和列表页共用同一个弹窗，停用会让后端撤下这条规则的 Prometheus 规则文件
const showEditModal = ref(false)
const toggling = ref(false)

const toggleEnabled = async () => {
    if (!alarm.value) return
    toggling.value = true
    try {
        await alarmsApi.updateAlarmRule(alarm.value.uuid, { enabled: !alarm.value.enabled })
        await fetchAlarmDetail()
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        toggling.value = false
    }
}

const getRuleTypeLabel = (type: string) =>
    (RULE_TYPES as readonly string[]).includes(type) ? t('dashboard.alarmRuleTypes.' + type) : type

// Config shown as "key  value" lines instead of raw JSON. Keys stay the template variable names
// (they are what the edit dialog shows). A nested object, e.g. compute_node's network_types
// { public: {...}, private: {...} }, gets one line per sub-key
const inline = (v: unknown) => (v !== null && typeof v === 'object' ? JSON.stringify(v) : String(v))
const configEntries = computed(() =>
    Object.entries(alarm.value?.config || {}).map(([key, value]) => ({
        key,
        lines:
            value !== null && typeof value === 'object'
                ? Object.entries(value as Record<string, unknown>).map(([sub, v]) =>
                      v !== null && typeof v === 'object'
                          ? `${sub}: ${Object.entries(v as Record<string, unknown>)
                                .map(([k, x]) => `${k}=${inline(x)}`)
                                .join('  ')}`
                          : `${sub}: ${inline(v)}`
                  )
                : [inline(value)],
    }))
)

const goBack = useGoBack('alarms')

const handleDelete = async () => {
    if (!alarm.value) return
    deleting.value = true
    try {
        await alarmsApi.deleteAlarmRule(alarm.value.uuid)
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'alarms' })
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        deleting.value = false
    }
}

onMounted(fetchAlarmDetail)
</script>

<template>
    <div class="vpc-detail">
        <!-- Header -->
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <!-- Loading State -->
        <div v-if="loading" class="loading-container">
            <div class="loading-spinner"></div>
            <p class="loading-text">{{ t('messages.loading') }}</p>
        </div>

        <!-- Error State -->
        <div v-else-if="error" class="error-container card">
            <AlertTriangle :size="48" style="opacity: 0.3; margin-bottom: 16px" />
            <p class="text-secondary">{{ error }}</p>
            <button class="btn btn-primary btn-sm" @click="fetchAlarmDetail" style="margin-top: 12px">
                {{ t('actions.refresh') }}
            </button>
        </div>

        <!-- Detail Content -->
        <div v-else-if="alarm" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon">
                        <AlertTriangle :size="20" />
                    </div>
                    <div>
                        <h2 class="resource-title">
                            {{ alarm.name }}
                            <StatusBadge
                                :variant="alarm.enabled ? 'success' : 'neutral'"
                                :label="alarm.enabled ? t('dashboard.alarm.enabled') : t('dashboard.alarm.disabled')"
                            />
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ alarm.uuid }}</span>
                            <button
                                class="copy-btn"
                                @click="copyToClipboard(alarm.uuid, 'uuid')"
                                :title="t('messages.copied')"
                            >
                                <Check v-if="copiedField === 'uuid'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-secondary btn-sm" @click="showEditModal = true">
                        <Pencil :size="14" />
                        {{ t('actions.edit') }}
                    </button>
                    <button class="btn btn-secondary btn-sm" :disabled="toggling" @click="toggleEnabled">
                        <Power :size="14" />
                        {{ alarm.enabled ? t('actions.disable') : t('actions.enable') }}
                    </button>
                    <button class="btn btn-danger-outline btn-sm" @click="showDeleteConfirm = true">
                        <Trash2 :size="14" />
                        {{ t('actions.delete') }}
                    </button>
                </div>
            </div>

            <!-- Info Sections -->
            <div class="info-grid">
                <!-- Basic Info Card -->
                <div class="info-card card">
                    <h3 class="card-section-title">{{ t('dashboard.table.overview') }}</h3>
                    <div class="info-rows">
                        <InfoRow :label="t('dashboard.table.name')">{{ alarm.name }}</InfoRow>
                        <InfoRow :label="t('dashboard.alarmActions.ruleType')">
                            <span class="badge badge-secondary">{{ getRuleTypeLabel(alarm.rule_type) }}</span>
                        </InfoRow>
                        <!-- 原先这里显示 alarm.owner，那是 clapi 内部的组织自增 ID（界面上就是个 "1"），
                             既不是创建者也不是任何用户能对上的东西，去掉 -->
                        <InfoRow v-if="alarm.description" :label="t('dashboard.table.description')">{{
                            alarm.description
                        }}</InfoRow>
                    </div>
                </div>

                <!-- Configuration Card -->
                <div class="info-card card">
                    <h3 class="card-section-title">{{ t('dashboard.configuration') }}</h3>
                    <div v-if="configEntries.length" class="config-list">
                        <template v-for="entry in configEntries" :key="entry.key">
                            <code class="config-key">{{ entry.key }}</code>
                            <div class="config-value">
                                <div v-for="(line, i) in entry.lines" :key="i">{{ line }}</div>
                            </div>
                        </template>
                    </div>
                    <p v-else class="text-secondary">-</p>
                </div>
            </div>
        </div>

        <NodeAlarmRuleEditModal
            :show="showEditModal"
            :rule="alarm"
            @close="showEditModal = false"
            @saved="fetchAlarmDetail()"
        />

        <!-- Delete Confirm Modal -->
        <DeleteModal
            :show="showDeleteConfirm"
            :title="t('dashboard.alarmActions.deleteTitle')"
            :message="t('dashboard.alarmActions.deleteConfirm', { name: alarm?.name })"
            :loading="deleting"
            @close="showDeleteConfirm = false"
            @confirm="handleDelete"
        />
    </div>
</template>

<style scoped>
.detail-header {
    margin-bottom: var(--spacing-4);
}

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

.error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px 20px;
    text-align: center;
}
.copied-icon {
    color: var(--success-color);
}

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

/* One grid for all entries so the key column lines up; rows separated like InfoRow */
.config-list {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    align-items: baseline;
    column-gap: var(--spacing-6);
    font-size: var(--font-size-sm);
}

.config-key,
.config-value {
    padding: var(--spacing-2) 0;
    line-height: 1.6;
    border-bottom: 1px solid var(--border-light);
}

.config-list > :nth-last-child(-n + 2) {
    border-bottom: none;
}

.config-key {
    background: none;
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.config-value {
    color: var(--text-primary);
    font-weight: var(--font-weight-medium);
    font-variant-numeric: tabular-nums;
    word-break: break-all;
}

@media (max-width: 768px) {
    .info-grid {
        grid-template-columns: 1fr;
    }
}
</style>
