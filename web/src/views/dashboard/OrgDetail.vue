
<=script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { orgsApi, ORG_ROLES, type Organization, type OrgMember } from '../../api/orgs'
import { ArrowLeft, Building2, Users, UserPlus, Trash2, Edit2, Shield, Crown, X } from 'lucide-vue-next'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const orgId = route.params.id as string

const org = ref<Organization | null>(null)
const members = ref<OrgMember[]>([])
const loading = ref(true)
const membersLoading = ref(false)
const error = ref('')

// Add member modal
const addMemberVisible = ref(false)
const addMemberForm = ref({ user_id: '', org_role: 1 })
const addMemberError = ref('')
const addingMember = ref(false)

// Change role modal
const changeRoleVisible = ref(false)
const changeRoleForm = ref({ user_id: 0, username: '', org_role: 1 })
const changeRoleError = ref('')
const changingRole = ref(false)

// Remove member
const removeMemberVisible = ref(false)
const memberToRemove = ref<OrgMember | null>(null)
const removingMember = ref(false)
const removeMemberError = ref('')

// Transfer ownership
const transferVisible = ref(false)
const transferTargetId = ref<number | null>(null)
const transferring = ref(false)
const transferError = ref('')

const fetchOrg = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await orgsApi.getOrg(orgId)
        org.value = response.data?.org || response.data
    } catch (err: any) {
        error.value = err.response?.data?.detail || 'Failed to load organization.'
    } finally {
        loading.value = false
    }
}

const fetchMembers = async () => {
    membersLoading.value = true
    try {
        const response = await orgsApi.fetchMembers(orgId)
        members.value = response.data?.members || response.data || []
    } catch (err: any) {
        console.error('Failed to fetch members:', err)
    } finally {
        membersLoading.value = false
    }
}

// --- Add Member ---
const openAddMember = () => {
    addMemberForm.value = { user_id: '', org_role: 1 }
    addMemberError.value = ''
    addMemberVisible.value = true
}

const handleAddMember = async () => {
    addMemberError.value = ''
    const uid = Number(addMemberForm.value.user_id)
    if (!uid) {
        addMemberError.value = t('dashboard.org.enterUserId')
        return
    }
    addingMember.value = true
    try {
        await orgsApi.addMember(orgId, { user_id: uid, org_role: addMemberForm.value.org_role })
        await fetchMembers()
        addMemberVisible.value = false
    } catch (err: any) {
        addMemberError.value = err.response?.data?.detail || err.message
    } finally {
        addingMember.value = false
    }
}

// --- Change Role ---
const openChangeRole = (member: OrgMember) => {
    changeRoleForm.value = { user_id: member.user_id, username: member.username, org_role: member.org_role }
    changeRoleError.value = ''
    changeRoleVisible.value = true
}

const handleChangeRole = async () => {
    changeRoleError.value = ''
    changingRole.value = true
    try {
        await orgsApi.updateMemberRole(orgId, changeRoleForm.value.user_id, { org_role: changeRoleForm.value.org_role })
        await fetchMembers()
        changeRoleVisible.value = false
    } catch (err: any) {
        changeRoleError.value = err.response?.data?.detail || err.message
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
        await orgsApi.removeMember(orgId, memberToRemove.value.user_id)
        await fetchMembers()
        removeMemberVisible.value = false
    } catch (err: any) {
        removeMemberError.value = err.response?.data?.detail || err.message
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
        await fetchOrg()
        await fetchMembers()
        transferVisible.value = false
    } catch (err: any) {
        transferError.value = err.response?.data?.detail || err.message
    } finally {
        transferring.value = false
    }
}

const getRoleName = (role: number) => ORG_ROLES[role] || 'Unknown'

onMounted(() => {
    fetchOrg()
    fetchMembers()
})
</script>

<template>
  <div>
    <!-- Back button -->
    <div class="page-header">
      <button class="btn btn-ghost btn-sm" @click="router.push({ name: 'orgs' })">
        <ArrowLeft :size="16" /> {{ $t('actions.back') }}
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="text-center" style="padding: 48px;">
      <div class="loading-spinner" style="margin: 0 auto;"></div>
    </div>

    <!-- Error -->
    <div v-else-if="error" class="card" style="padding: 48px; text-align: center;">
      <p class="text-error">{{ error }}</p>
      <button class="btn btn-secondary btn-sm" @click="fetchOrg">{{ $t('actions.retry') }}</button>
    </div>

    <template v-else-if="org">
      <!-- Org Info Card -->
      <div class="card" style="margin-bottom: var(--spacing-6);">
        <div class="detail-header">
          <div class="detail-title">
            <div class="icon-box-lg">
              <Building2 :size="24" />
            </div>
            <div>
              <h3>{{ org.name }}</h3>
              <div class="detail-meta">
                <span class="meta-item">ID: {{ org.id }}</span>
                <span class="meta-item" v-if="org.slug">Slug: {{ org.slug }}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="detail-grid">
          <div class="detail-item">
            <span class="detail-label">{{ $t('dashboard.table.description') }}</span>
            <span class="detail-value">{{ org.description || '-' }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">Owner</span>
            <span class="detail-value">{{ org.owner_email || org.owner_id || '-' }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">{{ $t('dashboard.org.memberCount') }}</span>
            <span class="detail-value">{{ org.member_count ?? members.length }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">{{ $t('dashboard.table.created') }}</span>
            <span class="detail-value">{{ org.created_at || '-' }}</span>
          </div>
        </div>
      </div>

      <!-- Members Card -->
      <div class="card">
        <div class="card-header-row">
          <h4><Users :size="18" /> {{ $t('dashboard.org.members') }}</h4>
          <div class="card-header-actions">
            <button class="btn btn-secondary btn-sm" @click="openTransfer">
              <Crown :size="14" /> {{ $t('dashboard.org.transferOwnership') }}
            </button>
            <button class="btn btn-primary btn-sm" @click="openAddMember">
              <UserPlus :size="14" /> {{ $t('dashboard.org.addMember') }}
            </button>
          </div>
        </div>

        <table class="data-table">
          <thead>
            <tr>
              <th>{{ $t('dashboard.table.userName') }}</th>
              <th>Email</th>
              <th>{{ $t('dashboard.org.role') }}</th>
              <th>Owner</th>
              <th>{{ $t('dashboard.table.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="membersLoading">
              <td colspan="5" class="text-center">
                <div class="loading-spinner" style="margin: 20px auto;"></div>
              </td>
            </tr>
            <tr v-else-if="members.length === 0">
              <td colspan="5" class="text-center text-secondary" style="padding: 48px;">
                {{ $t('dashboard.org.noMembers') }}
              </td>
            </tr>
            <tr v-else v-for="member in members" :key="member.user_id">
              <td>
                <div style="font-weight: 500;">{{ member.username }}</div>
                <div style="font-size: var(--font-size-xs); color: var(--text-tertiary);">ID: {{ member.user_id }}</div>
              </td>
              <td>{{ member.email || '-' }}</td>
              <td>
                <span class="role-badge" :class="'role-' + member.org_role">
                  {{ getRoleName(member.org_role) }}
                </span>
              </td>
              <td>
                <Crown v-if="member.is_owner" :size="16" style="color: #f59e0b;" />
                <span v-else>-</span>
              </td>
              <td>
                <div class="actions">
                  <button class="btn btn-ghost btn-sm" :title="$t('dashboard.org.changeRole')" @click="openChangeRole(member)">
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

    <!-- Add Member Modal -->
    <div v-if="addMemberVisible" class="modal-overlay" @click.self="addMemberVisible = false">
      <div class="modal-content card" style="max-width: 460px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.org.addMember') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="addMemberVisible = false"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.org.userId') }}</label>
            <input v-model="addMemberForm.user_id" type="number" class="form-input" placeholder="User ID" />
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.org.role') }}</label>
            <select v-model="addMemberForm.org_role" class="form-input">
              <option :value="1">Reader</option>
              <option :value="2">Writer</option>
              <option :value="3">Admin</option>
            </select>
          </div>
        </div>
        <div v-if="addMemberError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm); background:var(--error-light); padding:var(--spacing-2); border-radius:var(--radius-sm)">
          {{ addMemberError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="addMemberVisible = false" :disabled="addingMember">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleAddMember" :disabled="addingMember">
            <span v-if="addingMember" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ addingMember ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Change Role Modal -->
    <div v-if="changeRoleVisible" class="modal-overlay" @click.self="changeRoleVisible = false">
      <div class="modal-content card" style="max-width: 420px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.org.changeRole') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="changeRoleVisible = false"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
          <p style="margin-bottom: var(--spacing-4); color: var(--text-secondary);">
            {{ $t('dashboard.org.changeRoleFor') }} <strong>{{ changeRoleForm.username }}</strong>
          </p>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.org.role') }}</label>
            <select v-model="changeRoleForm.org_role" class="form-input">
              <option :value="1">Reader</option>
              <option :value="2">Writer</option>
              <option :value="3">Admin</option>
            </select>
          </div>
        </div>
        <div v-if="changeRoleError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm); background:var(--error-light); padding:var(--spacing-2); border-radius:var(--radius-sm)">
          {{ changeRoleError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="changeRoleVisible = false" :disabled="changingRole">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleChangeRole" :disabled="changingRole">
            {{ changingRole ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Remove Member Modal -->
    <div v-if="removeMemberVisible" class="modal-overlay" @click.self="removeMemberVisible = false">
      <div class="modal-content card" style="max-width: 420px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.org.removeMember') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="removeMemberVisible = false"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6); text-align: center;">
          <div style="width:64px;height:64px;border-radius:50%;background:var(--error-light);display:flex;align-items:center;justify-content:center;margin:0 auto var(--spacing-4);color:var(--error-color)">
            <Trash2 :size="32" />
          </div>
          <p style="color: var(--text-secondary);">{{ $t('dashboard.org.removeMemberConfirm') }}</p>
          <div style="background:var(--bg-secondary);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:var(--spacing-3) var(--spacing-4);margin-top:var(--spacing-4);text-align:left">
            <span style="font-weight:var(--font-weight-semibold);display:block">{{ memberToRemove?.username }}</span>
            <span style="font-size:var(--font-size-xs);color:var(--text-light);display:block;margin-top:2px">{{ memberToRemove?.email }}</span>
          </div>
          <div v-if="removeMemberError" class="text-error" style="margin-top:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ removeMemberError }}
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="removeMemberVisible = false" :disabled="removingMember">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-danger" @click="handleRemoveMember" :disabled="removingMember">
            <Trash2 :size="14" />
            {{ removingMember ? $t('messages.loading') : $t('dashboard.org.removeMember') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Transfer Ownership Modal -->
    <div v-if="transferVisible" class="modal-overlay" @click.self="transferVisible = false">
      <div class="modal-content card" style="max-width: 420px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.org.transferOwnership') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="transferVisible = false"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
          <p style="margin-bottom: var(--spacing-4); color: var(--text-secondary);">
            {{ $t('dashboard.org.transferConfirm') }}
          </p>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.org.selectNewOwner') }}</label>
            <select v-model="transferTargetId" class="form-input">
              <option :value="null" disabled>-- {{ $t('dashboard.org.selectMember') }} --</option>
              <option v-for="m in members.filter(m => !m.is_owner)" :key="m.user_id" :value="m.user_id">
                {{ m.username }} ({{ m.email }})
              </option>
            </select>
          </div>
          <div v-if="transferError" class="text-error" style="margin-top:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ transferError }}
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="transferVisible = false" :disabled="transferring">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleTransfer" :disabled="transferring || !transferTargetId">
            {{ transferring ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>
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

.detail-item {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-1);
}

.detail-label {
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  font-weight: var(--font-weight-semibold);
}

.detail-value {
  font-size: var(--font-size-sm);
  color: var(--text-primary);
  font-weight: var(--font-weight-medium);
}

.card-header-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-4);
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

.role-0 { background: var(--gray-100); color: var(--gray-600); }
.role-1 { background: #dbeafe; color: #1d4ed8; }
.role-2 { background: #d1fae5; color: #065f46; }
.role-3 { background: #fef3c7; color: #92400e; }

.actions {
  display: flex;
  gap: var(--spacing-02);
}

.text-error {
  color: var(--error-color);
}

/* Modal styles */
.modal-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  backdrop-filter: blur(4px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  animation: fadeIn 0.2s ease-out;
}

.modal-content {
  width: 100%;
  background: var(--bg-primary);
  border: 1px solid var(--border-light);
  box-shadow: var(--shadow-xl);
  animation: slideUp 0.3s cubic-bezier(0.16, 1, 0.3, 1);
  display: flex;
  flex-direction: column;
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-4);
  padding-bottom: var(--spacing-4);
  border-bottom: 1px solid var(--border-light);
}

.modal-header h3 { margin: 0; font-size: var(--font-size-lg); }

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--spacing-3);
  padding-top: var(--spacing-4);
  border-top: 1px solid var(--border-light);
}

.icon-btn { padding: 4px; }

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
.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

@keyframes fadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}

@keyframes slideUp {
  from { transform: translateY(20px); opacity: 0; }
  to { transform: translateY(0); opacity: 1; }
}
</style>
