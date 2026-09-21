<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useSecurityGroup } from '../../composables/useSecurityGroup'
import { securityGroupsApi, type SecurityGroup, type SecurityRule } from '../../api/networks'
import { isValidName } from '../../utils/validation'
import {
    formatRulePort,
    ruleServiceName,
    filterAndSortRules,
    type RuleSortKey,
    type RuleFilter,
} from '../../utils/securityRule'
import SecurityRuleModal from '../../components/securityGroup/SecurityRuleModal.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import DetailTabs from '../../components/base/DetailTabs.vue'
import { useGoBack } from '../../composables/useGoBack'
import { errorMessage } from '../../utils/error'
import {
    ArrowLeft,
    Shield,
    Trash2,
    Plus,
    X,
    Edit,
    ArrowUpDown,
    ArrowUp,
    ArrowDown,
    Network,
    Server,
    ChevronDown,
    Search,
} from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const toast = useToast()
const { translateDescription } = useSecurityGroup()
const goBack = useGoBack('security-groups')
const groupId = route.params.id as string

const group = ref<SecurityGroup | null>(null)
const loading = ref(true)
const error = ref('')

const showActionMenu = ref(false)
const activeTab = ref('rules')

// 标签页文案里带数量（原先是文案后面的小徽标，DetailTabs 只接受文本）
const tabs = computed(() => [
    { id: 'rules', label: t('dashboard.table.securityRules'), count: group.value?.security_rules?.length || 0 },
    {
        id: 'interfaces',
        label: t('dashboard.securityGroupDetail.associatedInterfaces'),
        count: group.value?.target_interfaces?.length || 0,
    },
])

// --- Rules filter and sort ---
const showRuleFilter = ref(false)
const ruleFilter = ref<RuleFilter>({ direction: '', protocol: '', keyword: '' })
const sortKey = ref<RuleSortKey>('direction')
const sortOrder = ref<'asc' | 'desc'>('asc')

const toggleRuleFilter = () => {
    showRuleFilter.value = !showRuleFilter.value
    if (!showRuleFilter.value) {
        ruleFilter.value = { direction: '', protocol: '', keyword: '' }
    }
}

const toggleSort = (key: RuleSortKey) => {
    if (sortKey.value === key) {
        sortOrder.value = sortOrder.value === 'asc' ? 'desc' : 'asc'
    } else {
        sortKey.value = key
        sortOrder.value = 'asc'
    }
}

const sortedRules = computed(() =>
    filterAndSortRules(group.value?.security_rules || [], ruleFilter.value, sortKey.value, sortOrder.value)
)

const ruleColumns: Array<{ key: RuleSortKey; label: string }> = [
    { key: 'name', label: 'dashboard.table.name' },
    { key: 'direction', label: 'dashboard.table.direction' },
    { key: 'protocol', label: 'dashboard.table.protocol' },
    { key: 'port', label: 'dashboard.table.portRange' },
    { key: 'remote_cidr', label: 'dashboard.table.remoteCidr' },
]

const fetchGroup = async () => {
    loading.value = true
    error.value = ''
    try {
        group.value = await securityGroupsApi.get(groupId)
    } catch (err) {
        console.error('Failed to fetch security group:', err)
        error.value = t('dashboard.securityGroupDetail.loadError')
    } finally {
        loading.value = false
    }
}

const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}
const closeActionMenu = () => {
    showActionMenu.value = false
}

// --- Add / Edit Rule ---
const ruleModalVisible = ref(false)
const ruleToEdit = ref<SecurityRule | null>(null)

const openAddRuleModal = () => {
    ruleToEdit.value = null
    ruleModalVisible.value = true
}

const openEditRuleModal = (rule: SecurityRule) => {
    ruleToEdit.value = rule
    ruleModalVisible.value = true
}

const onRuleSaved = async (edited: boolean) => {
    ruleModalVisible.value = false
    await fetchGroup()
    toast.success(edited ? t('messages.updateSuccess') : t('messages.createSuccess'))
}

// --- Delete Rule ---
const ruleToDelete = ref<SecurityRule | null>(null)
const deletingRule = ref(false)
const deleteRuleError = ref('')

const handleDeleteRule = (rule: SecurityRule) => {
    ruleToDelete.value = rule
    deleteRuleError.value = ''
}

// 删除确认框里展示的规则描述（方向 + 协议 + 端口 + 来源）
const ruleToDeleteName = computed(() => {
    const rule = ruleToDelete.value
    if (!rule) return ''
    const direction = rule.direction === 'ingress' ? t('dashboard.table.ingress') : t('dashboard.table.egress')
    return `${direction} ${rule.protocol.toUpperCase()} ${formatRulePort(rule, t)} ${rule.remote_cidr || ''}`.trim()
})

const closeDeleteRuleModal = () => {
    if (deletingRule.value) return
    ruleToDelete.value = null
    deleteRuleError.value = ''
}

const confirmDeleteRule = async () => {
    if (!ruleToDelete.value) return
    deletingRule.value = true
    deleteRuleError.value = ''
    try {
        await securityGroupsApi.deleteRule(groupId, ruleToDelete.value.id)
        await fetchGroup()
        ruleToDelete.value = null
        toast.success(t('messages.deleteSuccess'))
    } catch (err) {
        console.error('Failed to delete rule:', err)
        deleteRuleError.value = errorMessage(err, t('messages.error'))
    } finally {
        deletingRule.value = false
    }
}

// --- Delete Group ---
const deleteGroupModalVisible = ref(false)
const deletingGroup = ref(false)
const deleteGroupError = ref('')

const handleDelete = () => {
    closeActionMenu()
    deleteGroupError.value = ''
    deleteGroupModalVisible.value = true
}

const closeDeleteGroupModal = () => {
    if (deletingGroup.value) return
    deleteGroupModalVisible.value = false
    deleteGroupError.value = ''
}

const confirmDeleteGroup = async () => {
    deletingGroup.value = true
    deleteGroupError.value = ''
    try {
        await securityGroupsApi.delete(groupId)
        toast.success(t('messages.deleteSuccess'))
        deleteGroupModalVisible.value = false
        router.push({ name: 'security-groups' })
    } catch (err) {
        console.error('Failed to delete security group:', err)
        deleteGroupError.value = errorMessage(err, t('messages.error'))
    } finally {
        deletingGroup.value = false
    }
}

// --- Edit Name / Description ---
const showEditModal = ref(false)
const editForm = ref({ name: '', description: '' })
const savingInfo = ref(false)
const editError = ref('')
const isEditValid = computed(() => !!editForm.value.name.trim() && isValidName(editForm.value.name))

const openEditModal = () => {
    closeActionMenu()
    editForm.value = { name: group.value?.name || '', description: group.value?.description || '' }
    editError.value = ''
    showEditModal.value = true
}

const saveInfo = async () => {
    editError.value = ''
    if (!isEditValid.value) {
        editError.value = t('messages.invalidHostname')
        return
    }
    const payload: { name?: string; description?: string } = {}
    if (editForm.value.name !== group.value?.name) {
        payload.name = editForm.value.name
    }
    if (editForm.value.description !== (group.value?.description || '')) {
        payload.description = editForm.value.description
    }
    if (Object.keys(payload).length === 0) {
        showEditModal.value = false
        return
    }
    savingInfo.value = true
    try {
        await securityGroupsApi.patch(groupId, payload)
        if (group.value) {
            if (payload.name) group.value.name = payload.name
            if (payload.description !== undefined) group.value.description = payload.description
        }
        showEditModal.value = false
        toast.success(t('messages.updateSuccess'))
    } catch (err) {
        console.error('Failed to update info:', err)
        editError.value = errorMessage(err, t('messages.error'))
    } finally {
        savingInfo.value = false
    }
}

onMounted(fetchGroup)
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <div v-if="loading && !group" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error && !group" class="error-container card">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary" @click="fetchGroup">{{ $t('actions.retry') }}</button>
        </div>

        <div v-else-if="group" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="resource-icon">
                    <Shield :size="24" />
                </div>
                <div class="title-info">
                    <h1>{{ group.name }}</h1>
                    <div class="subtitle">
                        <span class="id-text">{{ group.id }}</span>
                        <span v-if="group.is_default" class="badge">{{ $t('dashboard.table.default') }}</span>
                    </div>
                </div>
                <div class="title-actions">
                    <div class="action-dropdown">
                        <button class="btn btn-primary" @click="toggleActionMenu">
                            {{ $t('actions.actions') }} <ChevronDown :size="14" />
                        </button>
                        <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu" />
                        <Transition name="dropdown">
                            <div v-if="showActionMenu" class="dropdown-menu">
                                <button class="dropdown-item" @click="openEditModal">
                                    <Edit :size="14" /> {{ $t('actions.edit') }}
                                </button>
                                <div class="dropdown-divider" />
                                <button
                                    class="dropdown-item dropdown-item-danger"
                                    :title="
                                        group.is_default ? $t('dashboard.securityGroupDetail.defaultNotDeletable') : ''
                                    "
                                    :disabled="group.is_default"
                                    @click="handleDelete"
                                >
                                    <Trash2 :size="14" /> {{ $t('actions.delete') }}
                                </button>
                            </div>
                        </Transition>
                    </div>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                    <div class="key-value-list">
                        <InfoRow :label="$t('dashboard.table.name')">{{ group.name }}</InfoRow>
                        <InfoRow :label="$t('dashboard.table.description')">
                            {{ translateDescription(group.description || '') || '-' }}
                        </InfoRow>
                        <InfoRow :label="$t('dashboard.table.vpc')">
                            <router-link
                                v-if="group.vpc"
                                :to="{ name: 'vpc-detail', params: { id: group.vpc.id } }"
                                class="text-link"
                            >
                                {{ group.vpc.name }}
                            </router-link>
                            <span v-else>-</span>
                        </InfoRow>
                        <InfoRow :label="$t('dashboard.table.createdAt')">
                            {{ group.created_at ? group.created_at.replace(/\.\d+$/, '') : '-' }}
                        </InfoRow>
                    </div>
                </div>
            </div>

            <!-- Tabbed Section -->
            <div class="card tab-card">
                <div class="tab-header">
                    <DetailTabs v-model="activeTab" :tabs="tabs" />
                    <div v-if="activeTab === 'rules'" class="tab-header-actions">
                        <button
                            :class="['btn btn-ghost btn-sm', { 'btn-filter-active': showRuleFilter }]"
                            :title="$t('actions.filter')"
                            @click="toggleRuleFilter"
                        >
                            <Search :size="14" />
                        </button>
                        <button class="btn btn-primary btn-sm" @click="openAddRuleModal">
                            <Plus :size="14" /> {{ $t('dashboard.buttons.addRule') }}
                        </button>
                    </div>
                </div>

                <!-- Rules Filter Bar -->
                <div v-if="activeTab === 'rules' && showRuleFilter" class="filter-bar">
                    <div class="select-wrapper filter-select">
                        <select v-model="ruleFilter.direction" class="form-input form-input-sm">
                            <option value="">
                                {{ $t('dashboard.table.direction') }}: {{ $t('dashboard.forms.placeholder.all') }}
                            </option>
                            <option value="ingress">{{ $t('dashboard.table.ingress') }}</option>
                            <option value="egress">{{ $t('dashboard.table.egress') }}</option>
                        </select>
                    </div>
                    <div class="select-wrapper filter-select">
                        <select v-model="ruleFilter.protocol" class="form-input form-input-sm">
                            <option value="">
                                {{ $t('dashboard.table.protocol') }}: {{ $t('dashboard.forms.placeholder.all') }}
                            </option>
                            <option value="tcp">TCP</option>
                            <option value="udp">UDP</option>
                            <option value="icmp">ICMP</option>
                        </select>
                    </div>
                    <div class="filter-search">
                        <Search :size="14" class="filter-search-icon" />
                        <input
                            v-model="ruleFilter.keyword"
                            type="text"
                            class="filter-search-input"
                            :placeholder="$t('actions.search') + '...'"
                        />
                        <button v-if="ruleFilter.keyword" class="filter-clear" @click="ruleFilter.keyword = ''">
                            <X :size="12" />
                        </button>
                    </div>
                </div>

                <!-- Rules Tab -->
                <div v-if="activeTab === 'rules'" class="table-responsive">
                    <table class="data-table">
                        <thead>
                            <tr>
                                <th
                                    v-for="col in ruleColumns"
                                    :key="col.key"
                                    class="sortable-th"
                                    @click="toggleSort(col.key)"
                                >
                                    {{ $t(col.label) }}
                                    <ArrowUp v-if="sortKey === col.key && sortOrder === 'asc'" :size="12" />
                                    <ArrowDown v-else-if="sortKey === col.key && sortOrder === 'desc'" :size="12" />
                                    <ArrowUpDown v-else :size="12" class="sort-idle" />
                                </th>
                                <th>{{ $t('dashboard.table.actions') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-if="!sortedRules.length">
                                <td colspan="6" class="text-center text-secondary">{{ $t('messages.noData') }}</td>
                            </tr>
                            <tr v-else v-for="rule in sortedRules" :key="rule.id">
                                <td>{{ rule.name || '-' }}</td>
                                <td>
                                    <span :class="['direction-badge', rule.direction]">
                                        {{
                                            rule.direction === 'ingress'
                                                ? $t('dashboard.table.ingress')
                                                : $t('dashboard.table.egress')
                                        }}
                                    </span>
                                </td>
                                <td class="mono">{{ rule.protocol.toUpperCase() }}</td>
                                <td class="mono">
                                    {{ formatRulePort(rule, t) }}
                                    <span v-if="ruleServiceName(rule)" class="service-tag">{{
                                        ruleServiceName(rule)
                                    }}</span>
                                </td>
                                <td class="mono">{{ rule.remote_cidr || '-' }}</td>
                                <td class="actions-cell">
                                    <button
                                        class="btn btn-ghost btn-sm"
                                        :title="$t('actions.edit')"
                                        @click="openEditRuleModal(rule)"
                                    >
                                        <Edit :size="14" />
                                    </button>
                                    <button
                                        class="btn btn-ghost btn-sm text-error"
                                        :title="$t('actions.delete')"
                                        @click="handleDeleteRule(rule)"
                                    >
                                        <Trash2 :size="14" />
                                    </button>
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>

                <!-- Interfaces Tab -->
                <div v-if="activeTab === 'interfaces'" class="table-responsive">
                    <table class="data-table">
                        <thead>
                            <tr>
                                <th>{{ $t('dashboard.table.name') }}</th>
                                <th>{{ $t('dashboard.securityGroupDetail.ipAddress') }}</th>
                                <th>{{ $t('dashboard.securityGroupDetail.instance') }}</th>
                                <th>{{ $t('dashboard.floatingIPDetail.interfaceId') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-if="!group.target_interfaces?.length">
                                <td colspan="4" class="text-center text-secondary">
                                    {{ $t('dashboard.securityGroupDetail.noInterfaces') }}
                                </td>
                            </tr>
                            <tr v-else v-for="iface in group.target_interfaces" :key="iface.id">
                                <td>{{ iface.name || '-' }}</td>
                                <td>
                                    <div class="iface-cell">
                                        <Network :size="14" class="text-secondary" />
                                        <span class="mono">{{ iface.ip_address || '-' }}</span>
                                    </div>
                                </td>
                                <td>
                                    <div v-if="iface.from_instance" class="iface-cell">
                                        <Server :size="14" class="text-secondary" />
                                        <router-link
                                            :to="{ name: 'instance-detail', params: { id: iface.from_instance.id } }"
                                            class="text-link"
                                        >
                                            {{ iface.from_instance.hostname || iface.from_instance.id }}
                                        </router-link>
                                    </div>
                                    <span v-else class="text-secondary">-</span>
                                </td>
                                <td class="mono text-secondary">{{ iface.id }}</td>
                            </tr>
                        </tbody>
                    </table>
                </div>
            </div>
        </div>

        <!-- Add / Edit Rule Modal -->
        <SecurityRuleModal
            :show="ruleModalVisible"
            :group-id="groupId"
            :rule="ruleToEdit"
            @close="ruleModalVisible = false"
            @saved="onRuleSaved"
        />

        <!-- Edit Security Group Modal -->
        <BaseModal
            :show="showEditModal"
            :title="$t('actions.edit')"
            :loading="savingInfo"
            form
            @close="showEditModal = false"
            @submit="saveInfo"
        >
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.table.name') }}</label>
                <input v-model="editForm.name" type="text" :class="['form-input', { 'input-error': !isEditValid }]" />
                <div v-if="!isEditValid" class="text-error text-xs mt-1">{{ $t('messages.invalidHostname') }}</div>
            </div>
            <div class="form-group">
                <label class="form-label">{{ $t('dashboard.table.description') }}</label>
                <input
                    v-model="editForm.description"
                    type="text"
                    class="form-input"
                    :placeholder="$t('messages.placeholderDescription')"
                />
            </div>
            <div v-if="editError" class="error-box">{{ editError }}</div>

            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showEditModal = false" :disabled="savingInfo">
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="savingInfo || !isEditValid">
                    <span
                        v-if="savingInfo"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ savingInfo ? $t('messages.saving') : $t('actions.save') }}
                </button>
            </template>
        </BaseModal>

        <!-- Delete Rule Modal -->
        <DeleteModal
            :show="!!ruleToDelete"
            :message="$t('dashboard.securityGroupDetail.deleteRuleConfirm')"
            :resource-name="ruleToDeleteName"
            :resource-id="ruleToDelete?.id"
            :loading="deletingRule"
            :error="deleteRuleError"
            @close="closeDeleteRuleModal"
            @confirm="confirmDeleteRule"
        />

        <!-- Delete Security Group Modal -->
        <DeleteModal
            :show="deleteGroupModalVisible && !!group"
            :message="$t('dashboard.securityGroupDetail.deleteConfirm')"
            :resource-name="group?.name"
            :resource-id="group?.id"
            :loading="deletingGroup"
            :error="deleteGroupError"
            @close="closeDeleteGroupModal"
            @confirm="confirmDeleteGroup"
        />
    </div>
</template>

<style scoped>
.detail-page {
    max-width: 1200px;
    margin: 0 auto;
}

.detail-header {
    margin-bottom: var(--spacing-4);
}

.loading-container,
.error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px;
}

/* Title Bar */
.title-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
    padding: var(--spacing-6);
    margin-bottom: var(--spacing-6);
}

.resource-icon {
    width: 48px;
    height: 48px;
    background: var(--bg-tertiary);
    color: var(--primary-color);
    border-radius: var(--radius-md);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
}

.title-info {
    flex: 1;
    min-width: 0;
}

.title-info h1 {
    font-size: var(--font-size-xl);
    font-weight: 600;
    margin: 0 0 4px 0;
    color: var(--text-primary);
}

.subtitle {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    font-size: var(--font-size-sm);
    flex-wrap: wrap;
}

.id-text {
    font-family: var(--font-family-mono);
    color: var(--text-secondary);
    word-break: break-all;
}

.badge {
    background: var(--primary-50);
    color: var(--primary-700);
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 11px;
    font-weight: 600;
}

/* Action Dropdown */
.action-dropdown {
    position: relative;
}

.dropdown-backdrop {
    position: fixed;
    inset: 0;
    z-index: var(--z-dropdown-backdrop);
}

.dropdown-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 160px;
    background: var(--bg-primary, var(--bg-primary));
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
    padding: 4px 0;
    z-index: var(--z-dropdown);
}

.dropdown-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 14px;
    border: none;
    background: none;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
    transition: background 0.15s;
    text-align: left;
}

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-hover, var(--gray-100));
}

.dropdown-item:disabled {
    opacity: 0.4;
    cursor: not-allowed;
}

.dropdown-item-danger {
    color: var(--error-color, var(--error-color));
}

.dropdown-item-danger:hover:not(:disabled) {
    background: var(--error-light);
}

.dropdown-divider {
    height: 1px;
    background: var(--border-light);
    margin: 4px 0;
}

.dropdown-enter-active {
    transition:
        opacity 0.15s,
        transform 0.15s;
}
.dropdown-leave-active {
    transition:
        opacity 0.1s,
        transform 0.1s;
}
.dropdown-enter-from,
.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-4px);
}

/* Info Grid */
.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-6);
}

.info-card {
    padding: var(--spacing-5);
}

.info-card h3 {
    font-size: var(--font-size-base);
    font-weight: 600;
    margin: 0 0 var(--spacing-4) 0;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-3);
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}
.text-link:hover {
    text-decoration: underline;
}

/* Tabs */
.tab-card {
    padding: 0;
    overflow: hidden;
}

.tab-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    padding: 0 var(--spacing-5);
    border-bottom: 1px solid var(--border-light);
}

/* 分隔线由 .tab-header 统一提供，去掉 DetailTabs 自带的下边线与下边距 */
.tab-header .detail-tabs {
    border-bottom: none;
    margin-bottom: 0;
}

.tab-header-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.btn-filter-active {
    background: var(--primary-50);
    color: var(--primary-color);
}

.filter-bar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-2);
    padding: var(--spacing-3) var(--spacing-5);
    border-bottom: 1px solid var(--border-light);
    background: var(--bg-secondary, var(--gray-50));
}

.filter-select {
    width: 160px;
    flex-shrink: 0;
}

.filter-search {
    position: relative;
    display: flex;
    align-items: center;
    flex: 1;
    min-width: 160px;
    max-width: 260px;
}

.filter-search-icon {
    position: absolute;
    left: 9px;
    color: var(--text-tertiary);
    pointer-events: none;
}

.filter-search-input {
    width: 100%;
    padding: 6px 28px 6px 32px;
    border: 1px solid var(--border-light);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.5;
    outline: none;
    transition:
        border-color var(--transition-fast),
        box-shadow var(--transition-fast);
}

.filter-search-input:focus {
    border-color: var(--primary-color);
    box-shadow: 0 0 0 3px var(--primary-100);
}

.filter-search-input::placeholder {
    color: var(--text-light);
}

.filter-clear {
    position: absolute;
    right: 8px;
    background: none;
    border: none;
    padding: 0;
    color: var(--text-tertiary);
    cursor: pointer;
    display: flex;
    align-items: center;
}

.filter-clear:hover {
    color: var(--text-primary);
}

.table-responsive {
    overflow-x: auto;
}

.actions-cell {
    white-space: nowrap;
}

.direction-badge {
    display: inline-block;
    padding: 2px 6px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 600;
}

.direction-badge.ingress {
    background: var(--success-light);
    color: var(--success-dark);
}
.direction-badge.egress {
    background: var(--info-light);
    color: var(--info-dark);
}

.mono {
    font-family: var(--font-family-mono);
}

.sortable-th {
    cursor: pointer;
    user-select: none;
    white-space: nowrap;
}

.sortable-th:hover {
    color: var(--primary-color);
}
.sortable-th svg {
    vertical-align: middle;
    margin-left: 4px;
}
.sort-idle {
    opacity: 0.3;
}

.service-tag {
    display: inline-block;
    padding: 1px 6px;
    border-radius: 4px;
    font-size: 10px;
    font-weight: 600;
    font-family: var(--font-family);
    background: var(--primary-50);
    color: var(--primary-700);
    margin-left: 6px;
    vertical-align: middle;
}

.iface-cell {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.text-error {
    color: var(--error-color);
}

.input-error {
    border-color: var(--error-color) !important;
    box-shadow: 0 0 0 3px var(--error-light) !important;
}

.error-box {
    color: var(--error-color);
    font-size: var(--font-size-sm);
    background: var(--error-light);
    padding: var(--spacing-2);
    border-radius: var(--radius-sm);
    text-align: left;
}
</style>
