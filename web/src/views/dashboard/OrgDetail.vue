<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { orgsApi, ORG_ROLES, type OrganizationDetail, type OrgMember, type OrgInvitation } from '../../api/orgs'
import { QUOTA_ROWS, type OrgResourceQuotaUpdate } from '../../api/quota'
import {
    ArrowLeft,
    Building2,
    Users,
    Trash2,
    Shield,
    Crown,
    Mail,
    Clock,
    XCircle,
    Gauge,
    ChevronDown,
    CheckCircle,
    AlertCircle,
    ShieldAlert,
    PauseCircle,
} from 'lucide-vue-next'
import BaseModal from '../../components/modals/BaseModal.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import DetailTabs from '../../components/base/DetailTabs.vue'
import { useGoBack } from '../../composables/useGoBack'
import { useAuthStore } from '../../stores/auth'
import { useTenantStore } from '../../stores/tenant'
import { useQuota } from '../../composables/useQuota'
import { useToast } from '../../composables/useToast'
import { formatDate } from '../../utils/format'
import { errorMessage } from '../../utils/error'

const { t } = useI18n()
const toast = useToast()
const authStore = useAuthStore()
const route = useRoute()
const goBack = useGoBack('orgs')
const orgId = route.params.id as string

const org = ref<OrganizationDetail | null>(null)
const members = ref<OrgMember[]>([])
const loading = ref(true)
const membersLoading = ref(false)
const error = ref('')

// Tabs
const activeTab = ref<string>('members')
const tabs = computed(() => [
    { id: 'members', label: t('dashboard.org.members'), icon: Users },
    { id: 'quota', label: t('quota.limit'), icon: Gauge },
])

// Quota modal
const quotaModalVisible = ref(false)

// Invitations
const invitations = ref<OrgInvitation[]>([])
const invitationsLoading = ref(false)

// Quota
const {
    quotaSummary,
    quotaLoading,
    quotaError,
    editingQuota,
    savingQuota,
    fetchQuota,
    handleSaveQuota: handleSaveQuotaBase,
    getUsagePercent,
    getUsageColor,
} = useQuota()
const isSuperuser = computed(() => authStore.user?.is_superuser === true)
// Listing invitations requires org ADMIN or SystemAdmin (enforced by the gateway)
const tenantStore = useTenantStore()
const canManageInvitations = computed(
    () => isSuperuser.value || (tenantStore.organizations.find((o) => o.id === orgId)?.org_role ?? 0) >= 3
)

const handleSaveQuota = async (regionUuid: string) => {
    try {
        await handleSaveQuotaBase(orgId, regionUuid)
        toast.success(t('messages.updateSuccess'))
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    }
}

const openQuotaModal = () => {
    quotaModalVisible.value = true
    if (!quotaSummary.value) {
        fetchQuota(orgId)
    }
}

// Invite member modal
const addMemberVisible = ref(false)
const addMemberForm = ref({ email: '', org_role: 1 })
const addMemberError = ref('')
const addingMember = ref(false)

// Change role modal
const changeRoleVisible = ref(false)
const changeRoleForm = ref({ user_uuid: '', username: '', org_role: 1 })
const changeRoleError = ref('')
const changingRole = ref(false)

// Remove member
const removeMemberVisible = ref(false)
const memberToRemove = ref<OrgMember | null>(null)
const removingMember = ref(false)
const removeMemberError = ref('')

// Transfer ownership
const transferVisible = ref(false)
const transferTargetId = ref<string | null>(null)
const transferring = ref(false)
const transferError = ref('')

const fetchOrg = async () => {
    loading.value = true
    error.value = ''
    try {
        // GET /orgs/:uuid 直接返回 orgDetailOut，没有 { org: ... } 外层包装
        org.value = await orgsApi.getOrg(orgId)
    } catch (err) {
        error.value = errorMessage(err, 'Failed to load organization.')
    } finally {
        loading.value = false
    }
}

const fetchMembers = async () => {
    membersLoading.value = true
    try {
        // GET /orgs/:uuid/members 直接返回数组，没有 { members: ... } 外层包装
        // 详情页展示的是这个组织的全部成员，一次取满上限（列表页那边才分页）
        members.value = (await orgsApi.fetchMembers(orgId, { limit: 500 })).members || []
    } catch (err) {
        console.error('Failed to fetch members:', err)
    } finally {
        membersLoading.value = false
    }
}

// --- Fetch Invitations ---
const fetchInvitations = async () => {
    invitationsLoading.value = true
    try {
        const response = await orgsApi.fetchInvitations(orgId)
        invitations.value = response || []
    } catch (err) {
        console.error('Failed to fetch invitations:', err)
    } finally {
        invitationsLoading.value = false
    }
}
// The org list may load after this page mounts
watch(
    canManageInvitations,
    (allowed) => {
        if (allowed) fetchInvitations()
    },
    { immediate: true }
)

// --- Invite Member ---
const openAddMember = () => {
    addMemberForm.value = { email: '', org_role: 1 }
    addMemberError.value = ''
    addMemberVisible.value = true
}

const handleAddMember = async () => {
    addMemberError.value = ''
    const email = addMemberForm.value.email.trim()
    if (!email) {
        addMemberError.value = 'Please enter an email address'
        return
    }
    addingMember.value = true
    try {
        await orgsApi.inviteMember(orgId, { email, org_role: addMemberForm.value.org_role })
        toast.success(t('messages.createSuccess'))
        await fetchInvitations()
        addMemberVisible.value = false
    } catch (err) {
        addMemberError.value = errorMessage(err, t('messages.error'))
    } finally {
        addingMember.value = false
    }
}

// --- Cancel Invitation ---
const cancellingInvitation = ref<string | null>(null)
const handleCancelInvitation = async (inv: OrgInvitation) => {
    cancellingInvitation.value = inv.uuid
    try {
        await orgsApi.cancelInvitation(orgId, inv.uuid)
        toast.success(t('messages.deleteSuccess'))
        await fetchInvitations()
    } catch (err) {
        console.error('Failed to cancel invitation:', err)
    } finally {
        cancellingInvitation.value = null
    }
}

// --- Change Role ---
const openChangeRole = (member: OrgMember) => {
    changeRoleForm.value = { user_uuid: member.user_uuid, username: member.username, org_role: member.org_role }
    changeRoleError.value = ''
    changeRoleVisible.value = true
}

const handleChangeRole = async () => {
    changeRoleError.value = ''
    changingRole.value = true
    try {
        await orgsApi.updateMemberRole(orgId, changeRoleForm.value.user_uuid, {
            org_role: changeRoleForm.value.org_role,
        })
        toast.success(t('messages.updateSuccess'))
        await fetchMembers()
        changeRoleVisible.value = false
    } catch (err) {
        changeRoleError.value = errorMessage(err, t('messages.error'))
    } finally {
        changingRole.value = false
    }
}

// --- Remove Member ---
const openRemoveMember = (member: OrgMember) => {
    memberToRemove.value = member
    removeMemberError.value = ''
    removeMemberVisible.value = true
}

const handleRemoveMember = async () => {
    if (!memberToRemove.value) return
    removingMember.value = true
    removeMemberError.value = ''
    try {
        await orgsApi.removeMember(orgId, memberToRemove.value.user_uuid)
        toast.success(t('messages.deleteSuccess'))
        await fetchMembers()
        removeMemberVisible.value = false
    } catch (err) {
        removeMemberError.value = errorMessage(err, t('messages.error'))
    } finally {
        removingMember.value = false
    }
}

// --- Transfer Ownership ---
const openTransfer = () => {
    transferTargetId.value = null
    transferError.value = ''
    transferVisible.value = true
}

const handleTransfer = async () => {
    if (!transferTargetId.value) return
    transferring.value = true
    transferError.value = ''
    try {
        await orgsApi.transferOwnership(orgId, transferTargetId.value)
        toast.success(t('messages.updateSuccess'))
        await fetchOrg()
        await fetchMembers()
        transferVisible.value = false
    } catch (err) {
        transferError.value = errorMessage(err, t('messages.error'))
    } finally {
        transferring.value = false
    }
}

const getRoleName = (role: number) => ORG_ROLES[role] || 'Unknown'

// Actions dropdown
const showActionMenu = ref(false)
const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}
const closeActionMenu = () => {
    showActionMenu.value = false
}

const handleKeydown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') closeActionMenu()
}

onMounted(() => {
    fetchOrg()
    fetchMembers()
    fetchQuota(orgId)
    document.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
    document.removeEventListener('keydown', handleKeydown)
})
</script>

<template>
    <div>
        <!-- Back button -->
        <div class="page-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <!-- Loading -->
        <div v-if="loading" class="text-center" style="padding: 48px">
            <div class="loading-spinner" style="margin: 0 auto"></div>
        </div>

        <!-- Error -->
        <div v-else-if="error" class="card" style="padding: 48px; text-align: center">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-secondary btn-sm" @click="fetchOrg">{{ $t('actions.retry') }}</button>
        </div>

        <template v-else-if="org">
            <!-- Org Info Card -->
            <div class="card" style="margin-bottom: var(--spacing-6)">
                <div class="detail-header">
                    <div class="detail-title">
                        <div class="icon-box-lg">
                            <Building2 :size="24" />
                        </div>
                        <div>
                            <h3>{{ org.name }}</h3>
                            <div class="detail-meta">
                                <span class="meta-item">UUID: {{ org.uuid }}</span>
                                <span class="meta-item" v-if="org.slug"
                                    >{{ $t('dashboard.table.slug') }}: {{ org.slug }}</span
                                >
                            </div>
                        </div>
                    </div>
                    <div class="action-dropdown">
                        <button class="btn btn-primary" @click="toggleActionMenu">
                            {{ $t('actions.actions') }} <ChevronDown :size="14" />
                        </button>
                        <Transition name="dropdown">
                            <div v-if="showActionMenu" class="dropdown-menu" @click="closeActionMenu">
                                <button class="dropdown-item" @click="openAddMember">
                                    <Mail :size="14" /> {{ $t('dashboard.org.inviteMember') }}
                                </button>
                                <button class="dropdown-item" @click="openTransfer">
                                    <Crown :size="14" /> {{ $t('dashboard.org.transferOwnership') }}
                                </button>
                                <div class="dropdown-divider"></div>
                                <button class="dropdown-item" @click="openQuotaModal">
                                    <Gauge :size="14" /> {{ $t('quota.manage') }}
                                </button>
                            </div>
                        </Transition>
                        <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
                    </div>
                </div>

                <div class="detail-grid">
                    <InfoRow :label="$t('dashboard.table.description')">{{ org.description || '-' }}</InfoRow>
                    <InfoRow :label="$t('dashboard.table.owner')">{{
                        org.owner_email || org.owner_uuid || '-'
                    }}</InfoRow>
                    <InfoRow :label="$t('dashboard.org.memberCount')">{{ org.member_count ?? members.length }}</InfoRow>
                    <InfoRow :label="$t('dashboard.table.status')">
                        <div class="status-cell" :class="'status-' + (org.status || 0)">
                            <CheckCircle v-if="org.status === 1" :size="14" />
                            <PauseCircle v-else-if="org.status === 2" :size="14" />
                            <ShieldAlert v-else-if="org.status === 3" :size="14" />
                            <AlertCircle v-else :size="14" />
                            <span>
                                {{
                                    org.status === 0
                                        ? $t('dashboard.org.status.pending')
                                        : org.status === 1
                                          ? $t('dashboard.org.status.active')
                                          : org.status === 2
                                            ? $t('dashboard.org.status.suspended')
                                            : org.status === 3
                                              ? $t('dashboard.org.status.disabled')
                                              : $t('dashboard.org.status.pending')
                                }}
                            </span>
                        </div>
                    </InfoRow>
                    <InfoRow :label="$t('dashboard.table.created')">{{ org.created_at || '-' }}</InfoRow>
                </div>
            </div>

            <!-- Tabs -->
            <DetailTabs v-model="activeTab" :tabs="tabs" />

            <!-- Quota Tab (read-only) -->
            <div v-if="activeTab === 'quota'" class="card">
                <div v-if="quotaLoading" class="text-center" style="padding: 32px">
                    <div class="loading-spinner" style="margin: 0 auto"></div>
                </div>
                <div v-else-if="quotaError" class="text-center" style="padding: 24px">
                    <p class="text-error">{{ quotaError }}</p>
                    <button class="btn btn-secondary btn-sm" @click="fetchQuota(orgId)">
                        {{ $t('actions.retry') }}
                    </button>
                </div>
                <div v-else-if="quotaSummary && quotaSummary.regions.length > 0">
                    <div v-for="region in quotaSummary.regions" :key="region.region_name" class="quota-region-card">
                        <h5 class="quota-region-title">{{ region.region_name }}</h5>
                        <table class="data-table quota-table">
                            <thead>
                                <tr>
                                    <th>{{ $t('dashboard.table.name') }}</th>
                                    <th>{{ $t('quota.limit') }}</th>
                                    <th>{{ $t('quota.used') }}</th>
                                    <th style="width: 200px">{{ $t('dashboard.table.status') }}</th>
                                </tr>
                            </thead>
                            <tbody>
                                <tr v-for="res in QUOTA_ROWS" :key="res.key">
                                    <td>{{ $t(res.label) }}</td>
                                    <td>{{ region.quota[res.qkey] }}</td>
                                    <td>{{ region.consumption[res.key] }}</td>
                                    <td>
                                        <div class="usage-bar-container">
                                            <div
                                                class="usage-bar"
                                                :style="{
                                                    width:
                                                        getUsagePercent(
                                                            region.consumption[res.key],
                                                            region.quota[res.qkey]
                                                        ) + '%',
                                                    background: getUsageColor(
                                                        getUsagePercent(
                                                            region.consumption[res.key],
                                                            region.quota[res.qkey]
                                                        )
                                                    ),
                                                }"
                                            ></div>
                                        </div>
                                        <span class="usage-text"
                                            >{{
                                                getUsagePercent(region.consumption[res.key], region.quota[res.qkey])
                                            }}%</span
                                        >
                                    </td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                </div>
                <div v-else class="text-center text-secondary" style="padding: 32px">
                    {{ $t('quota.noQuota') }}
                </div>
            </div>

            <!-- Members Card -->
            <div v-if="activeTab === 'members'" class="card">
                <div class="card-header-row">
                    <h4><Users :size="18" /> {{ $t('dashboard.org.members') }}</h4>
                </div>

                <!-- Pending Invitations -->
                <div v-if="invitations.length > 0" class="invitations-section">
                    <h5 class="section-subtitle"><Clock :size="14" /> {{ $t('dashboard.org.pendingInvitations') }}</h5>
                    <div class="invitation-list">
                        <div v-for="inv in invitations" :key="inv.uuid" class="invitation-item">
                            <div class="invitation-info">
                                <span class="invitation-email">{{ inv.email }}</span>
                                <span class="role-badge" :class="'role-' + inv.org_role">{{
                                    getRoleName(inv.org_role)
                                }}</span>
                            </div>
                            <div class="invitation-meta">
                                <span>{{ $t('dashboard.org.invitedBy', { user: inv.inviter_email }) }}</span>
                                <span>{{ $t('dashboard.org.expires', { date: formatDate(inv.expires_at) }) }}</span>
                            </div>
                            <button
                                class="btn btn-ghost btn-sm text-error"
                                :title="$t('actions.cancelInvitation')"
                                @click="handleCancelInvitation(inv)"
                                :disabled="cancellingInvitation === inv.uuid"
                            >
                                <XCircle :size="14" />
                            </button>
                        </div>
                    </div>
                </div>

                <table class="data-table">
                    <thead>
                        <tr>
                            <th>{{ $t('dashboard.table.userName') }}</th>
                            <th>{{ $t('dashboard.table.email') || 'Email' }}</th>
                            <th>{{ $t('dashboard.org.role') }}</th>
                            <th>{{ $t('dashboard.table.owner') || 'Owner' }}</th>
                            <th>{{ $t('dashboard.table.actions') }}</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-if="membersLoading">
                            <td colspan="5" class="text-center">
                                <div class="loading-spinner" style="margin: 20px auto"></div>
                            </td>
                        </tr>
                        <tr v-else-if="members.length === 0">
                            <td colspan="5" class="text-center text-secondary" style="padding: 48px">
                                {{ $t('dashboard.org.noMembers') }}
                            </td>
                        </tr>
                        <tr v-else v-for="member in members" :key="member.user_uuid">
                            <td>
                                <div style="font-weight: 500">{{ member.username }}</div>
                                <div style="font-size: var(--font-size-xs); color: var(--text-tertiary)">
                                    {{ member.user_uuid }}
                                </div>
                            </td>
                            <td>{{ member.user_email || '-' }}</td>
                            <td>
                                <span class="role-badge" :class="'role-' + member.org_role">
                                    {{ getRoleName(member.org_role) }}
                                </span>
                            </td>
                            <td>
                                <Crown v-if="member.is_owner" :size="16" style="color: var(--warning-color)" />
                                <span v-else>-</span>
                            </td>
                            <td>
                                <div class="actions">
                                    <button
                                        class="btn btn-ghost btn-sm"
                                        :title="$t('dashboard.org.changeRole')"
                                        @click="openChangeRole(member)"
                                    >
                                        <Shield :size="14" />
                                    </button>
                                    <button
                                        class="btn btn-ghost btn-sm text-error"
                                        :title="$t('dashboard.org.removeMember')"
                                        @click="openRemoveMember(member)"
                                        :disabled="member.is_owner"
                                    >
                                        <Trash2 :size="14" />
                                    </button>
                                </div>
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </template>

        <!-- Quota Management Modal -->
        <BaseModal :show="quotaModalVisible" size="xl" @close="quotaModalVisible = false">
            <template #header>
                <h3><Gauge :size="18" style="margin-right: 8px" />{{ $t('quota.manage') }}</h3>
            </template>

            <div v-if="quotaLoading" class="text-center" style="padding: 48px">
                <div class="loading-spinner" style="margin: 0 auto"></div>
            </div>
            <div v-else-if="quotaError" class="text-center" style="padding: 24px">
                <p class="text-error">{{ quotaError }}</p>
                <button class="btn btn-secondary btn-sm" @click="fetchQuota(orgId)">{{ $t('actions.retry') }}</button>
            </div>
            <div v-else-if="quotaSummary && quotaSummary.regions.length > 0">
                <div v-for="region in quotaSummary.regions" :key="region.region_name" class="quota-region-card">
                    <h5 class="quota-region-title">{{ region.region_name }}</h5>
                    <table class="data-table quota-table">
                        <thead>
                            <tr>
                                <th>{{ $t('dashboard.table.name') }}</th>
                                <th>{{ $t('quota.limit') }}</th>
                                <th>{{ $t('quota.used') }}</th>
                                <th style="width: 200px">{{ $t('dashboard.table.status') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-for="res in QUOTA_ROWS" :key="res.key">
                                <td>{{ $t(res.label) }}</td>
                                <td>
                                    <input
                                        v-if="isSuperuser"
                                        type="number"
                                        class="form-input quota-input"
                                        :value="
                                            editingQuota[region.region_uuid]?.[res.qkey as keyof OrgResourceQuotaUpdate]
                                        "
                                        @input="
                                            (e: Event) => {
                                                if (editingQuota[region.region_uuid])
                                                    editingQuota[region.region_uuid][res.qkey] = Number(
                                                        (e.target as HTMLInputElement).value
                                                    )
                                            }
                                        "
                                        min="0"
                                        step="1"
                                    />
                                    <span v-else>{{ region.quota[res.qkey] }}</span>
                                </td>
                                <td>{{ region.consumption[res.key] }}</td>
                                <td>
                                    <div class="usage-bar-container">
                                        <div
                                            class="usage-bar"
                                            :style="{
                                                width:
                                                    getUsagePercent(
                                                        region.consumption[res.key],
                                                        region.quota[res.qkey]
                                                    ) + '%',
                                                background: getUsageColor(
                                                    getUsagePercent(region.consumption[res.key], region.quota[res.qkey])
                                                ),
                                            }"
                                        ></div>
                                    </div>
                                    <span class="usage-text"
                                        >{{
                                            getUsagePercent(region.consumption[res.key], region.quota[res.qkey])
                                        }}%</span
                                    >
                                </td>
                            </tr>
                        </tbody>
                    </table>
                    <div v-if="isSuperuser" class="quota-save-row">
                        <button
                            class="btn btn-primary btn-sm"
                            @click="handleSaveQuota(region.region_uuid)"
                            :disabled="savingQuota === region.region_uuid"
                        >
                            {{ savingQuota === region.region_uuid ? $t('messages.loading') : $t('actions.save') }}
                        </button>
                    </div>
                </div>
            </div>
            <div v-else class="text-center text-secondary" style="padding: 48px">
                {{ $t('quota.noQuota') || 'No quota assigned' }}
            </div>

            <template #footer>
                <button class="btn btn-secondary" @click="quotaModalVisible = false">
                    {{ $t('actions.close') || $t('actions.cancel') }}
                </button>
            </template>
        </BaseModal>

        <!-- Invite Member Modal -->
        <BaseModal
            :show="addMemberVisible"
            :loading="addingMember"
            form
            @close="addMemberVisible = false"
            @submit="handleAddMember"
        >
            <template #header>
                <h3><Mail :size="18" /> {{ $t('dashboard.org.inviteMember') }}</h3>
            </template>

            <p style="margin-bottom: var(--spacing-4); color: var(--text-secondary); font-size: var(--font-size-sm)">
                {{ $t('dashboard.org.inviteHint') }}
            </p>
            <div class="form-group">
                <label class="form-label" for="invite_email">{{ $t('dashboard.table.email') }}</label>
                <input
                    id="invite_email"
                    v-model="addMemberForm.email"
                    type="email"
                    class="form-input"
                    :placeholder="$t('dashboard.forms.placeholder.emailExample')"
                />
            </div>
            <div class="form-group">
                <label class="form-label" for="invite_role">{{ $t('dashboard.org.role') }}</label>
                <select id="invite_role" v-model="addMemberForm.org_role" class="form-input">
                    <option :value="1">{{ $t('roles.reader') }}</option>
                    <option :value="2">{{ $t('roles.writer') }}</option>
                    <option :value="3">{{ $t('roles.admin') }}</option>
                </select>
            </div>
            <div v-if="addMemberError" class="text-error modal-error">
                {{ addMemberError }}
            </div>

            <template #footer>
                <button
                    type="button"
                    class="btn btn-secondary"
                    @click="addMemberVisible = false"
                    :disabled="addingMember"
                >
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="addingMember">
                    <span
                        v-if="addingMember"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    <Mail v-if="!addingMember" :size="14" />
                    {{ addingMember ? $t('messages.loading') : $t('dashboard.org.sendInvitation') }}
                </button>
            </template>
        </BaseModal>

        <!-- Change Role Modal -->
        <BaseModal
            :show="changeRoleVisible"
            :title="$t('dashboard.org.changeRole')"
            :loading="changingRole"
            form
            @close="changeRoleVisible = false"
            @submit="handleChangeRole"
        >
            <p style="margin-bottom: var(--spacing-4); color: var(--text-secondary)">
                {{ $t('dashboard.org.changeRoleFor') }} <strong>{{ changeRoleForm.username }}</strong>
            </p>
            <div class="form-group">
                <label class="form-label" for="change_role_select">{{ $t('dashboard.org.role') }}</label>
                <select id="change_role_select" v-model="changeRoleForm.org_role" class="form-input">
                    <option :value="1">{{ $t('roles.reader') }}</option>
                    <option :value="2">{{ $t('roles.writer') }}</option>
                    <option :value="3">{{ $t('roles.admin') }}</option>
                </select>
            </div>
            <div v-if="changeRoleError" class="text-error modal-error">
                {{ changeRoleError }}
            </div>

            <template #footer>
                <button
                    type="button"
                    class="btn btn-secondary"
                    @click="changeRoleVisible = false"
                    :disabled="changingRole"
                >
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="changingRole">
                    {{ changingRole ? $t('messages.loading') : $t('actions.confirm') }}
                </button>
            </template>
        </BaseModal>

        <!-- Remove Member Modal -->
        <BaseModal
            :show="removeMemberVisible"
            :title="$t('dashboard.org.removeMember')"
            :loading="removingMember"
            content-class="remove-member-modal"
            @close="removeMemberVisible = false"
        >
            <div class="remove-member-body">
                <div class="remove-member-icon">
                    <Trash2 :size="32" />
                </div>
                <p style="color: var(--text-secondary)">{{ $t('dashboard.org.removeMemberConfirm') }}</p>
                <div class="remove-member-target">
                    <span style="font-weight: var(--font-weight-semibold); display: block">{{
                        memberToRemove?.username
                    }}</span>
                    <span
                        style="
                            font-size: var(--font-size-xs);
                            color: var(--text-light);
                            display: block;
                            margin-top: 2px;
                        "
                        >{{ memberToRemove?.user_email }}</span
                    >
                </div>
                <div v-if="removeMemberError" class="text-error modal-error">
                    {{ removeMemberError }}
                </div>
            </div>

            <template #footer>
                <button class="btn btn-secondary" @click="removeMemberVisible = false" :disabled="removingMember">
                    {{ $t('actions.cancel') }}
                </button>
                <button class="btn btn-danger" @click="handleRemoveMember" :disabled="removingMember">
                    <Trash2 :size="14" />
                    {{ removingMember ? $t('messages.loading') : $t('dashboard.org.removeMember') }}
                </button>
            </template>
        </BaseModal>

        <!-- Transfer Ownership Modal -->
        <BaseModal
            :show="transferVisible"
            :title="$t('dashboard.org.transferOwnership')"
            :loading="transferring"
            form
            @close="transferVisible = false"
            @submit="handleTransfer"
        >
            <p style="margin-bottom: var(--spacing-4); color: var(--text-secondary)">
                {{ $t('dashboard.org.transferConfirm') }}
            </p>
            <div class="form-group">
                <label class="form-label" for="transfer_owner_target">{{ $t('dashboard.org.selectNewOwner') }}</label>
                <select id="transfer_owner_target" v-model="transferTargetId" class="form-input">
                    <option :value="null" disabled>-- {{ $t('dashboard.org.selectMember') }} --</option>
                    <option v-for="m in members.filter((m) => !m.is_owner)" :key="m.user_uuid" :value="m.user_uuid">
                        {{ m.username }} ({{ m.user_email || '-' }})
                    </option>
                </select>
            </div>
            <div v-if="transferError" class="text-error modal-error">
                {{ transferError }}
            </div>

            <template #footer>
                <button
                    type="button"
                    class="btn btn-secondary"
                    @click="transferVisible = false"
                    :disabled="transferring"
                >
                    {{ $t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="transferring || !transferTargetId">
                    {{ transferring ? $t('messages.loading') : $t('actions.confirm') }}
                </button>
            </template>
        </BaseModal>
    </div>
</template>

<style scoped>
.page-header {
    margin-bottom: var(--spacing-4);
}

.detail-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    margin-bottom: var(--spacing-6);
    padding-bottom: var(--spacing-4);
    border-bottom: 1px solid var(--border-light);
}

.detail-title {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
}

.detail-title h3 {
    margin: 0 0 var(--spacing-1);
    font-size: var(--font-size-xl);
}

.detail-meta {
    display: flex;
    gap: var(--spacing-4);
}

.meta-item {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    font-family: var(--font-family-mono);
}

.icon-box-lg {
    width: 48px;
    height: 48px;
    border-radius: var(--radius-md);
    background: linear-gradient(135deg, rgba(59, 130, 246, 0.1), rgba(6, 182, 212, 0.1));
    color: var(--primary-600);
    display: flex;
    align-items: center;
    justify-content: center;
}

.detail-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: var(--spacing-5);
}

.card-header-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-4);
}

.action-dropdown {
    position: relative;
}

.dropdown-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 200px;
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
    gap: 10px;
    width: 100%;
    padding: 10px 16px;
    border: none;
    background: none;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
    transition: background 0.15s;
    text-align: left;
}

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-hover, #f3f4f6);
}

.dropdown-divider {
    height: 1px;
    background: var(--border-light);
    margin: 4px 0;
}

.dropdown-backdrop {
    position: fixed;
    inset: 0;
    z-index: var(--z-dropdown-backdrop);
}

.dropdown-enter-active,
.dropdown-leave-active {
    transition:
        opacity 0.15s,
        transform 0.15s;
}

.dropdown-enter-from,
.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-4px);
}

.card-header-row h4 {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    margin: 0;
    font-size: var(--font-size-lg);
}

.card-header-actions {
    display: flex;
    gap: var(--spacing-2);
}

.role-badge {
    display: inline-block;
    padding: 2px 10px;
    border-radius: var(--radius-full);
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-semibold);
}

.role-0 {
    background: var(--gray-100);
    color: var(--gray-600);
}
.role-1 {
    background: #dbeafe;
    color: #1d4ed8;
}
.role-2 {
    background: var(--success-light);
    color: #065f46;
}
.role-3 {
    background: var(--warning-light);
    color: #92400e;
}

.actions {
    display: flex;
    gap: var(--spacing-2);
}

.text-error {
    color: var(--error-color);
}

/* Invitations */
.invitations-section {
    margin-bottom: var(--spacing-5);
    padding-bottom: var(--spacing-4);
    border-bottom: 1px solid var(--border-light);
}

.section-subtitle {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-3);
    font-weight: var(--font-weight-semibold);
}

.invitation-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-2);
}

.invitation-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
    padding: var(--spacing-3) var(--spacing-4);
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
}

.invitation-info {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    flex: 1;
}

.invitation-email {
    font-weight: var(--font-weight-medium);
    font-size: var(--font-size-sm);
}

.invitation-meta {
    display: flex;
    gap: var(--spacing-4);
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

/* Modal styles */
.modal-error {
    margin-top: var(--spacing-4);
    font-size: var(--font-size-sm);
    background: var(--error-light);
    padding: var(--spacing-2);
    border-radius: var(--radius-sm);
}

.remove-member-body {
    text-align: center;
}

.remove-member-icon {
    width: 64px;
    height: 64px;
    border-radius: 50%;
    background: var(--error-light);
    display: flex;
    align-items: center;
    justify-content: center;
    margin: 0 auto var(--spacing-4);
    color: var(--error-color);
}

.remove-member-target {
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    padding: var(--spacing-3) var(--spacing-4);
    margin-top: var(--spacing-4);
    text-align: left;
}

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 20px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    transition: background var(--transition-base);
}
.btn-danger:hover {
    background: var(--error-dark);
}
.btn-danger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

/* Quota */
.quota-region-card {
    padding: var(--spacing-4) 0;
    border-bottom: 1px solid var(--border-light);
}

.quota-region-card:last-child {
    border-bottom: none;
}

.quota-region-title {
    margin: 0 0 var(--spacing-3);
    font-size: var(--font-size-base);
    font-weight: var(--font-weight-semibold);
    color: var(--text-primary);
}

.quota-table {
    margin-bottom: var(--spacing-3);
}

.quota-input {
    width: 100px;
    padding: 4px 8px;
    font-size: var(--font-size-sm);
}

.quota-save-row {
    display: flex;
    justify-content: flex-end;
    padding-top: var(--spacing-2);
}

.usage-bar-container {
    display: inline-block;
    width: 120px;
    height: 8px;
    background: var(--bg-tertiary, #e5e7eb);
    border-radius: 4px;
    overflow: hidden;
    vertical-align: middle;
    margin-right: var(--spacing-2);
}

.usage-bar {
    height: 100%;
    border-radius: 4px;
    transition: width 0.3s ease;
}

.usage-text {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}
</style>
