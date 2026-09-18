<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { usersApi, type User } from '../../api/users'
import { orgsApi } from '../../api/orgs'
import { useTenantStore } from '../../stores/tenant'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { User as UserIcon, Plus, Trash2, Edit, Search, RefreshCw, Check, Copy } from 'lucide-vue-next'
import { formatDate } from '../../utils/format'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'

const { t } = useI18n()
const tenantStore = useTenantStore()
const toast = useToast()

const { copiedId, copyId } = useCopyId()

const users = ref<User[]>([])
const loading = ref(false)
const loadError = ref('')
const searchQuery = ref('')
const inviteForm = ref({
    email: '',
    org_role: 1,
    is_superuser: false
})

const isSystemOrg = computed(() => {
    const org = tenantStore.currentOrg
    return org?.org_type === 2
})
const createModalVisible = ref(false)
const createError = ref('')
const editUserForm = ref({
    uuid: '',
    username: '',
    email: '',
    role: 'user'
})
const creatingResource = ref(false)
const editModalVisible = ref(false)
const editError = ref('')

const fetchUsers = async () => {
    loading.value = true
    loadError.value = ''
    try {
        // Wait for tenant store to finish loading if needed
        if (tenantStore.isLoading) {
            await new Promise<void>(resolve => {
                const unwatch = watch(() => tenantStore.isLoading, (val) => {
                    if (!val) { unwatch(); resolve() }
                })
            })
        }
        const orgId = tenantStore.currentOrgId
        if (orgId) {
            // Fetch org members
            const response = await orgsApi.fetchMembers(orgId)
            const members = Array.isArray(response) ? response : []
            users.value = members.map((m: any) => ({
                user: {
                    uuid: m.user_uuid,
                    name: m.user_email || m.user_uuid,
                },
                uuid: m.user_uuid,
                member_uuid: m.uuid,
                username: m.username || m.user_email?.split('@')[0] || m.user_uuid,
                email: m.user_email || '',
                role: m.is_owner ? 'owner' : m.org_role === 3 ? 'admin' : m.org_role === 2 ? 'writer' : m.org_role === 1 ? 'reader' : 'member',
                is_superuser: !!m.is_superuser,
                status: m.invitation_status === 0 ? 'invited' : 'active',
                created_at: m.created_at,
            }))
        } else {
            // Fallback: global user list (system admin context)
            const response = await usersApi.fetchUsers()
            users.value = (response as any).users || []
        }
    } catch (error) {
        console.error('Failed to fetch users:', error)
        users.value = []
        loadError.value = t('messages.error')
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

const getUserStatus = (status: string | undefined): string => {
    return status || 'active'
}

// 创建时间列显示的是格式化后的文案，排序要按原始值；状态列空值按 active 处理
const columns = computed<Column[]>(() => [
    { key: 'username', label: t('dashboard.table.userName'), sortable: true },
    { key: 'email', label: t('dashboard.table.email'), sortable: true },
    { key: 'role', label: t('dashboard.table.role'), sortable: true, sortValue: (u) => u.role || 'member' },
    { key: 'status', label: t('dashboard.table.status'), sortable: true, sortValue: (u) => getUserStatus(u.status) },
    { key: 'created', label: t('dashboard.table.created'), sortable: true, sortValue: (u) => u.created_at || '' },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])


const openCreateModal = () => {
    inviteForm.value = { email: '', org_role: 1, is_superuser: false }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleInviteUser = async () => {
    createError.value = ''
    if (!inviteForm.value.email) {
        createError.value = t('messages.emailRequired')
        return
    }

    creatingResource.value = true
    try {
        const orgId = tenantStore.currentOrgId
        if (!orgId) throw new Error('No organization selected')
        await orgsApi.inviteMember(orgId, inviteForm.value)
        await fetchUsers()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
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
        editError.value = t('messages.usernameRequired')
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
        toast.success(t('messages.updateSuccess'))
    } catch (err: any) {
        console.error('Failed to update user:', err)
        editError.value = err.response?.data?.detail || err.message || t('messages.error')
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
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete user:', error)
        deleteError.value = error.response?.data?.detail || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

onMounted(fetchUsers)
</script>

<template>
  <div>
    <PageToolbar v-model:search="searchQuery">
      <template #actions>
        <button class="btn btn-secondary btn-sm btn-icon" @click="fetchUsers" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createUser') }}
        </button>
      </template>
    </PageToolbar>

    <DataTable
      :columns="columns"
      :rows="filteredUsers"
      row-key="uuid"
      :loading="loading"
      :error="loadError"
      @retry="fetchUsers"
    >
      <template #empty>
        <div v-if="searchQuery">
          <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noResults') }}</p>
        </div>
        <div v-else>
          <p>{{ $t('messages.noUsers') }}</p>
        </div>
      </template>

      <template #cell-username="{ row: user }">
        <router-link :to="{ name: 'user-detail', params: { id: user.uuid } }" class="resource-link">
          <div class="resource-info">
            <div class="resource-icon">
              <UserIcon :size="16" />
            </div>
            <div>
              <div class="resource-name">{{ user.username }}</div>
              <div class="resource-id-row">
                <span class="resource-id" :title="user.uuid">{{ user.uuid.slice(0, 8) }}...</span>
                <button class="copy-btn-mini" @click.stop.prevent="copyId(user.uuid)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                  <Check v-if="copiedId === user.uuid" :size="10" style="color: #10b981;" />
                  <Copy v-else :size="10" />
                </button>
              </div>
            </div>
          </div>
        </router-link>
      </template>

      <template #cell-email="{ row: user }">{{ user.email }}</template>

      <template #cell-role="{ row: user }">
        <span class="role-badge">{{ $t('roles.' + (user.role?.toLowerCase() || 'member')) }}</span>
        <span v-if="isSystemOrg && user.is_superuser" class="superuser-tag">{{ $t('roles.superuser') }}</span>
      </template>

      <template #cell-status="{ row: user }">
        <span :class="'status-' + getUserStatus(user.status)">{{ $t('userStatus.' + getUserStatus(user.status)) }}</span>
      </template>

      <template #cell-created="{ row: user }">{{ formatDate(user.created_at || new Date()) }}</template>

      <template #cell-actions="{ row: user }">
        <div class="actions">
          <button class="btn btn-ghost btn-sm" :title="$t('actions.edit')" @click="openEditModal(user as any)">
            <Edit :size="14" />
          </button>
          <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(user as any)">
            <Trash2 :size="14" />
          </button>
        </div>
      </template>
    </DataTable>

    <!-- Create User Modal -->
    <BaseModal
      :show="createModalVisible"
      :title="$t('dashboard.buttons.createUser')"
      :loading="creatingResource"
      form
      @close="closeCreateModal"
      @submit="handleInviteUser"
    >
          <div class="form-group">
            <label class="form-label" for="user_email">{{ $t('dashboard.table.email') }}</label>
            <input
              id="user_email"
              name="email"
              v-model="inviteForm.email"
              type="email"
              class="form-input"
              :placeholder="$t('dashboard.table.email')"
              autocomplete="email"
            />
          </div>
          <div class="form-group">
            <label class="form-label" for="user_role">{{ $t('dashboard.table.role') }}</label>
            <select id="user_role" name="role" v-model="inviteForm.org_role" class="form-input" :disabled="inviteForm.is_superuser">
              <option :value="1">{{ $t('roles.reader') }}</option>
              <option :value="2">{{ $t('roles.writer') }}</option>
              <option :value="3">{{ $t('roles.admin') }}</option>
            </select>
          </div>
          <div v-if="isSystemOrg" class="form-group">
            <label class="form-check-label">
              <input type="checkbox" v-model="inviteForm.is_superuser" class="form-check-input"
                @change="inviteForm.org_role = inviteForm.is_superuser ? 3 : inviteForm.org_role" />
              {{ $t('roles.superuser') }}
            </label>
          </div>

          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creatingResource">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="creatingResource">
          <span v-if="creatingResource" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ creatingResource ? $t('messages.loading') : $t('actions.confirm') }}
        </button>
      </template>
    </BaseModal>

    <!-- Edit User Modal -->
    <BaseModal
      :show="editModalVisible"
      :title="$t('actions.edit')"
      :loading="creatingResource"
      form
      @close="closeEditModal"
      @submit="handleEditUser"
    >
          <div class="form-group">
            <label class="form-label" for="edit_user_name">{{ $t('dashboard.table.userName') }}</label>
            <input 
              id="edit_user_name"
              name="username"
              v-model="editUserForm.username" 
              type="text" 
              class="form-input" 
              :placeholder="$t('dashboard.table.userName')" 
              autocomplete="username"
            />
          </div>
          <div class="form-group">
            <label class="form-label" for="edit_user_email">{{ $t('dashboard.table.email') }}</label>
            <input 
              id="edit_user_email"
              name="email"
              v-model="editUserForm.email" 
              type="email" 
              class="form-input" 
              :placeholder="$t('dashboard.table.email')" 
              autocomplete="email"
            />
          </div>
          <div class="form-group">
            <label class="form-label" for="edit_user_role">{{ $t('dashboard.table.role') }}</label>
            <select id="edit_user_role" name="role" v-model="editUserForm.role" class="form-input">
              <option value="user">{{ $t('roles.member') }}</option>
              <option value="admin">{{ $t('roles.admin') }}</option>
            </select>
          </div>

          <div v-if="editError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ editError }}
          </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="closeEditModal" :disabled="creatingResource">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="creatingResource">
          <span v-if="creatingResource" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ creatingResource ? $t('messages.loading') : $t('actions.confirm') }}
        </button>
      </template>
    </BaseModal>

    <!-- Delete Confirmation Modal -->
    <DeleteModal
      :show="deleteModalVisible"
      :title="isInvitedUser ? $t('actions.cancelInvitation') : $t('actions.delete')"
      :message="isInvitedUser ? $t('messages.confirmCancelInvitation') : $t('dashboard.deleteConfirm.message')"
      :confirm-label="isInvitedUser ? $t('actions.cancelInvitation') : undefined"
      :resource-name="resourceToDelete?.username"
      :resource-id="resourceToDelete?.uuid"
      :loading="deletingResource"
      :error="deleteError"
      @close="closeDeleteModal"
      @confirm="confirmDelete"
    />

    <!-- Quota Management Modal -->
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

.role-badge {
  display: inline-block;
  padding: 2px 8px;
  background: var(--gray-50);
  border-radius: var(--radius-full);
  font-size: var(--font-size-xs);
  color: var(--text-secondary);
}

.superuser-tag {
  display: inline-block;
  padding: 2px 6px;
  margin-left: 4px;
  background: var(--primary-light, #e0e7ff);
  color: var(--primary-600, #4f46e5);
  border-radius: var(--radius-full);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
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
  justify-content: center;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

.resource-link {
  color: var(--primary-600);
  cursor: pointer;
}

.resource-link:hover {
  text-decoration: underline;
}

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
