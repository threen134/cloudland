<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { usersApi, type User, type ResourceQuota, type ResourceQuotaUpdate } from '../../api/users'
import { orgsApi } from '../../api/orgs'
import { useTenantStore } from '../../stores/tenant'
import { User as UserIcon, Plus, Trash2, Edit, Search, X, Gauge } from 'lucide-vue-next'

const { t } = useI18n()
const tenantStore = useTenantStore()

const users = ref<User[]>([])
const loading = ref(false)
const searchQuery = ref('')
const router = useRouter()
const inviteForm = ref({
    email: '',
    org_role: 1
})
const createModalVisible = ref(false)
const createError = ref('')
const editUserForm = ref({
    uuid: '',
    username: '',
    email: '',
    role: 'user'
})
const editingResource = ref(false)
const creatingResource = ref(false)
const editModalVisible = ref(false)
const editError = ref('')

const fetchUsers = async () => {
    loading.value = true
    try {
        const orgId = tenantStore.currentOrgId
        if (orgId) {
            // Fetch org members
            const response = await orgsApi.fetchMembers(orgId)
            const members = Array.isArray(response.data) ? response.data : []
            users.value = members.map((m: any) => ({
                user: {
                    uuid: m.user_uuid,
                    name: m.user_email || m.user_uuid,
                },
                uuid: m.user_uuid,
                member_uuid: m.uuid,
                username: m.user_email?.split('@')[0] || m.user_uuid,
                email: m.user_email || '',
                role: m.org_role === 3 ? 'admin' : m.org_role === 2 ? 'writer' : m.org_role === 1 ? 'reader' : 'member',
                status: m.invitation_status === 0 ? 'invited' : 'active',
                created_at: m.created_at,
            }))
        } else {
            // Fallback: global user list (system admin context)
            const response = await usersApi.fetchUsers()
            users.value = (response.data as any).users || []
        }
    } catch (error) {
        console.error('Failed to fetch users:', error)
        users.value = []
    } finally {
        loading.value = false
    }
}

const filteredUsers = computed(() => {
    if (!searchQuery.value) return users.value
    const query = searchQuery.value.toLowerCase()
    return users.value.filter(user => 
        (user.username?.toLowerCase().includes(query) || '') || 
        (user.email?.toLowerCase().includes(query) || '') ||
        (user.uuid?.toLowerCase().includes(query) || '')
    )
})

const userStatusMap: Record<number | string, string> = {
    0: 'invited',
    1: 'active',
    2: 'inactive',
    3: 'inactive',
}
const getUserStatus = (status: number | string | undefined): string => {
    if (status === undefined || status === null) return 'active'
    return userStatusMap[status] || String(status)
}

const navigateToDetail = (user: User) => {
    router.push({ name: 'user-detail', params: { id: user.uuid } })
}

const openCreateModal = () => {
    inviteForm.value = { email: '', org_role: 1 }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleInviteUser = async () => {
    createError.value = ''
    if (!inviteForm.value.email) {
        createError.value = t('dashboard.table.email') + ' is required.'
        return
    }

    creatingResource.value = true
    try {
        const orgId = tenantStore.currentOrgId
        if (!orgId) throw new Error('No organization selected')
        await orgsApi.inviteMember(orgId, inviteForm.value)
        await fetchUsers()
        closeCreateModal()
    } catch (err: any) {
        console.error('Failed to invite user:', err)
        createError.value = err.response?.data?.error_message || err.response?.data?.detail || err.message || t('messages.error')
    } finally {
        creatingResource.value = false
    }
}

const openEditModal = (user: User) => {
    editUserForm.value = {
        uuid: user.uuid,
        username: user.username,
        email: user.email || '',
        role: user.role || 'user'
    }
    editModalVisible.value = true
}

const closeEditModal = () => {
    editModalVisible.value = false
    editError.value = ''
}

const handleEditUser = async () => {
    editError.value = ''
    if (!editUserForm.value.username) {
        editError.value = 'Username is required.'
        return
    }

    creatingResource.value = true
    try {
        await usersApi.updateUser(editUserForm.value.uuid, {
            username: editUserForm.value.username,
            email: editUserForm.value.email,
            role: editUserForm.value.role
        })
        await fetchUsers()
        closeEditModal()
    } catch (err: any) {
        console.error('Failed to update user:', err)
        editError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creatingResource.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<User | null>(null)

const isInvitedUser = computed(() => (resourceToDelete.value as any)?.status === 'invited')

const handleDeleteClick = (item: User) => {
    resourceToDelete.value = item
    deleteModalVisible.value = true
}
const closeDeleteModal = () => {
    deleteModalVisible.value = false
    resourceToDelete.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!resourceToDelete.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        const item = resourceToDelete.value as any
        if (item.status === 'invited' && item.member_uuid) {
            // Cancel invitation
            const orgId = tenantStore.currentOrgId
            if (orgId) {
                await orgsApi.cancelInvitation(orgId, item.member_uuid)
            }
        } else {
            await usersApi.deleteUser(resourceToDelete.value.uuid)
        }
        await fetchUsers()
        closeDeleteModal()
    } catch (error: any) {
        console.error('Failed to delete user:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

// --- Quota Management Modal ---
const quotaModalVisible = ref(false)
const quotaLoading = ref(false)
const quotaSaving = ref(false)
const quotaError = ref('')
const quotaTargetUser = ref<User | null>(null)
const quotaForm = ref<ResourceQuotaUpdate>({
    max_cpu_cores: 0,
    max_ram_gb: 0,
    max_traffic_gb: 0,
    max_public_ips: 0,
    max_disk_gb: 0
})

const openQuotaModal = async (user: User) => {
    quotaTargetUser.value = user
    quotaModalVisible.value = true
    quotaLoading.value = true
    quotaError.value = ''
    try {
        const response = await usersApi.getUserQuota(user.uuid)
        const quota = response.data as any
        quotaForm.value = {
            max_cpu_cores: quota.max_cpu_cores ?? 0,
            max_ram_gb: quota.max_ram_gb ?? 0,
            max_traffic_gb: quota.max_traffic_gb ?? 0,
            max_public_ips: quota.max_public_ips ?? 0,
            max_disk_gb: quota.max_disk_gb ?? 0
        }
    } catch (err: any) {
        quotaForm.value = { max_cpu_cores: 0, max_ram_gb: 0, max_traffic_gb: 0, max_public_ips: 0, max_disk_gb: 0 }
    } finally {
        quotaLoading.value = false
    }
}

const closeQuotaModal = () => {
    quotaModalVisible.value = false
    quotaTargetUser.value = null
    quotaError.value = ''
}

const handleSaveQuota = async () => {
    if (!quotaTargetUser.value) return
    quotaSaving.value = true
    quotaError.value = ''
    try {
        await usersApi.updateUserQuota(quotaTargetUser.value.uuid, quotaForm.value)
        closeQuotaModal()
    } catch (err: any) {
        console.error('Failed to update quota:', err)
        quotaError.value = err.response?.data?.detail || err.message || t('messages.error')
    } finally {
        quotaSaving.value = false
    }
}

onMounted(fetchUsers)
</script>

<template>
  <div>
    <div class="page-header">
      <div class="search-wrapper">
        <div class="search-box">
          <Search :size="16" class="search-icon" />
          <input 
            type="text" 
            v-model="searchQuery"
            :placeholder="$t('actions.search') + '...'" 
            class="search-input"
          />
        </div>
      </div>
      <button class="btn btn-primary btn-sm" @click="openCreateModal">
        <Plus :size="14" /> {{ $t('dashboard.buttons.createUser') }}
      </button>
    </div>

    <div class="card table-card">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ $t('dashboard.table.userName') }}</th>
            <th>{{ $t('dashboard.table.email') }}</th>
            <th>{{ $t('dashboard.table.role') }}</th>
            <th>{{ $t('dashboard.table.status') }}</th>
            <th>{{ $t('dashboard.table.created') }}</th>
            <th>{{ $t('dashboard.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="6" class="text-center">
              <div class="loading-spinner" style="margin: 20px auto;"></div>
            </td>
          </tr>
          <tr v-else-if="filteredUsers.length === 0">
            <td colspan="6" class="text-center text-secondary" style="padding: 48px;">
               <div v-if="searchQuery">
                  <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
                  <p>{{ $t('messages.noResults') }}</p>
               </div>
               <div v-else>
                  <p>{{ $t('messages.noUsers') }}</p>
               </div>
            </td>
          </tr>
          <tr v-else v-for="user in filteredUsers" :key="user.uuid">
            <td>
              <div class="user-cell">
                <div class="avatar">
                  <UserIcon :size="16" />
                </div>
                <div>
                   <div class="user-name resource-link" @click="navigateToDetail(user)">{{ user.username }}</div>
                   <div class="user-id">{{ user.uuid }}</div>
                </div>
              </div>
            </td>
            <td>{{ user.email }}</td>
            <td>
               <span class="role-badge">{{ $t('roles.' + (user.role?.toLowerCase() || 'member')) }}</span>
            </td>
            <td>
               <span :class="'status-' + getUserStatus(user.status)">{{ $t('userStatus.' + getUserStatus(user.status)) }}</span>
            </td>
            <td>{{ user.created_at ? new Date(user.created_at).toLocaleDateString() : new Date().toLocaleDateString() }}</td>
            <td>
              <div class="actions">
                <button class="btn btn-ghost btn-sm" :title="$t('quota.manage')" @click="openQuotaModal(user)">
                  <Gauge :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="openEditModal(user)">
                  <Edit :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(user)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create User Modal -->
    <div v-if="createModalVisible" class="modal-overlay" @click.self="closeCreateModal">
      <div class="modal-content card" style="max-width: 500px;">
        <div class="modal-header">
          <h3>{{ $t('dashboard.buttons.createUser') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeCreateModal"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.email') }}</label>
            <input
              v-model="inviteForm.email"
              type="email"
              class="form-input"
              :placeholder="$t('dashboard.table.email')"
            />
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.role') }}</label>
            <select v-model="inviteForm.org_role" class="form-input">
              <option :value="1">Reader</option>
              <option :value="2">Writer</option>
              <option :value="3">Admin</option>
            </select>
          </div>
        </div>
        <div v-if="createError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ createError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeCreateModal" :disabled="creatingResource">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleInviteUser" :disabled="creatingResource">
            <span v-if="creatingResource" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creatingResource ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Edit User Modal -->
    <div v-if="editModalVisible" class="modal-overlay" @click.self="closeEditModal">
      <div class="modal-content card" style="max-width: 500px;">
        <div class="modal-header">
          <h3>{{ $t('actions.edit') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeEditModal"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.userName') }}</label>
            <input 
              v-model="editUserForm.username" 
              type="text" 
              class="form-input" 
              :placeholder="$t('dashboard.table.userName')" 
            />
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.email') }}</label>
            <input 
              v-model="editUserForm.email" 
              type="email" 
              class="form-input" 
              :placeholder="$t('dashboard.table.email')" 
            />
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.role') }}</label>
            <select v-model="editUserForm.role" class="form-input">
              <option value="user">User</option>
              <option value="admin">Admin</option>
            </select>
          </div>
        </div>
        <div v-if="editError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ editError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeEditModal" :disabled="creatingResource">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleEditUser" :disabled="creatingResource">
            <span v-if="creatingResource" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ creatingResource ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Delete Confirmation Modal -->
    <div v-if="deleteModalVisible" class="modal-overlay" @click.self="closeDeleteModal">
      <div class="modal-content card" style="max-width: 460px;">
        <div class="modal-header">
          <h3>{{ isInvitedUser ? '取消邀请' : $t('actions.delete') }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeDeleteModal"><X :size="20" /></button>
        </div>
        <div class="modal-body">
          <div style="text-align:center;padding:var(--spacing-4) 0">
            <div style="width:64px;height:64px;border-radius:50%;background:var(--error-light);display:flex;align-items:center;justify-content:center;margin:0 auto var(--spacing-4);color:var(--error-color)"><Trash2 :size="32" /></div>
            <p style="color:var(--text-secondary);margin:0 0 var(--spacing-4)">{{ isInvitedUser ? '确定要取消该用户的邀请吗？' : $t('dashboard.deleteConfirm.message') }}</p>
            <div style="background:var(--bg-secondary);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:var(--spacing-3) var(--spacing-4);text-align:left">
              <span style="font-size:var(--font-size-xs);color:var(--text-tertiary);text-transform:uppercase;letter-spacing:0.05em;display:block;margin-bottom:var(--spacing-1)">{{ $t('dashboard.deleteConfirm.resource') }}</span>
              <span style="font-weight:var(--font-weight-semibold);display:block">{{ resourceToDelete?.username }}</span>
              <span style="font-size:var(--font-size-xs);color:var(--text-light);font-family:var(--font-family-mono);display:block;margin-top:2px">{{ resourceToDelete?.uuid }}</span>
            </div>
            <div v-if="deleteError" class="text-error" style="margin-top:var(--spacing-4);font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
              {{ deleteError }}
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeDeleteModal" :disabled="deletingResource">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-danger" @click="confirmDelete" :disabled="deletingResource">
            <span v-if="deletingResource" class="loading-spinner" style="width:16px;height:16px;border-width:2px"></span>
            <Trash2 v-else :size="14" />
            {{ deletingResource ? $t('dashboard.deleteConfirm.deleting') : (isInvitedUser ? '取消邀请' : $t('actions.delete')) }}
          </button>
        </div>
      </div>
    </div>

    <!-- Quota Management Modal -->
    <div v-if="quotaModalVisible" class="modal-overlay" @click.self="closeQuotaModal">
      <div class="modal-content card" style="max-width: 520px;">
        <div class="modal-header">
          <h3>{{ $t('quota.manage') }} - {{ quotaTargetUser?.username }}</h3>
          <button class="btn btn-ghost btn-sm icon-btn" @click="closeQuotaModal"><X :size="20" /></button>
        </div>
        <div class="modal-body" style="padding: var(--spacing-6);">
          <div v-if="quotaLoading" style="text-align: center; padding: var(--spacing-8);">
            <div class="loading-spinner" style="margin: 0 auto;"></div>
          </div>
          <template v-else>
            <div class="form-group">
              <label class="form-label">{{ $t('quota.cpuCores') }}</label>
              <input v-model.number="quotaForm.max_cpu_cores" type="number" min="0" step="1" class="form-input" />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('quota.ramGb') }}</label>
              <input v-model.number="quotaForm.max_ram_gb" type="number" min="0" step="1" class="form-input" />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('quota.diskGb') }}</label>
              <input v-model.number="quotaForm.max_disk_gb" type="number" min="0" step="1" class="form-input" />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('quota.publicIps') }}</label>
              <input v-model.number="quotaForm.max_public_ips" type="number" min="0" step="1" class="form-input" />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('quota.trafficGb') }}</label>
              <input v-model.number="quotaForm.max_traffic_gb" type="number" min="0" step="1" class="form-input" />
            </div>
          </template>
        </div>
        <div v-if="quotaError" class="text-error" style="margin: 0 var(--spacing-6) var(--spacing-4); font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
          {{ quotaError }}
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeQuotaModal" :disabled="quotaSaving">{{ $t('actions.cancel') }}</button>
          <button class="btn btn-primary" @click="handleSaveQuota" :disabled="quotaSaving || quotaLoading">
            <span v-if="quotaSaving" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
            {{ quotaSaving ? $t('messages.loading') : $t('actions.confirm') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0;
  padding-right: 20px;
}

.search-wrapper {
  flex: 1;
  max-width: 400px;
}

.search-box {
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--bg-secondary);
  padding: 0 12px;
  height: 40px;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-light);
  transition: all 0.2s;
}

.search-box:focus-within {
  border-color: var(--primary-300);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.search-icon {
  color: var(--gray-400);
}

.search-input {
  border: none;
  background: transparent;
  width: 100%;
  height: 100%;
  font-size: 0.875rem;
  color: var(--text-primary);
}

.search-input:focus {
  outline: none;
}

.table-card {
  padding: 0;
  overflow: hidden;
}

.user-cell {
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
}

.avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background-color: var(--primary-light);
  color: var(--primary-color);
  display: flex;
  align-items: center;
  justify-content: center;
}

.user-name {
  font-weight: var(--font-weight-medium);
  color: var(--text-main);
}

.user-id {
  font-size: var(--font-size-xs);
  color: var(--text-light);
}

.role-badge {
  display: inline-block;
  padding: 2px 8px;
  background: var(--gray-10);
  border-radius: var(--radius-full);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
}

.status-active {
  color: var(--success-color);
  font-weight: var(--font-weight-medium);
}

.status-invited {
  color: var(--warning-color, #e6a23c);
  font-weight: var(--font-weight-medium);
}

.status-inactive {
  color: var(--text-light);
  font-weight: var(--font-weight-medium);
}

.actions {
  display: flex;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
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
  max-width: 500px;
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

.modal-header h3 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.modal-body {
  margin-bottom: var(--spacing-6);
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--spacing-3);
  padding-top: var(--spacing-4);
  border-top: 1px solid var(--border-light);
}

.icon-btn {
  padding: 4px;
}

@keyframes fadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}

@keyframes slideUp {
  from { transform: translateY(20px); opacity: 0; }
  to { transform: translateY(0); opacity: 1; }
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
.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

.resource-link {
  color: var(--primary-600);
  cursor: pointer;
}

.resource-link:hover {
  text-decoration: underline;
}
</style>
