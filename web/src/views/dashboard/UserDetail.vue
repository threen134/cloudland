<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { usersApi, type User } from '../../api/users'
import { ArrowLeft, User as UserIcon, Trash2, Mail, Shield, Check, Copy } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import InfoRow from '../../components/base/InfoRow.vue'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import { formatDateTime } from '../../utils/format'

const { t } = useI18n()
const toast = useToast()
const { copiedId, copyId } = useCopyId()
const route = useRoute()
const router = useRouter()
const userId = route.params.id as string

const user = ref<User | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)

const fetchUser = async () => {
    loading.value = true
    error.value = ''
    try {
        user.value = await usersApi.getUser(userId)
    } catch (err) {
        console.error('Failed to fetch user:', err)
        error.value = t('dashboard.userDetail.loadError')
    } finally {
        loading.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deleteError = ref('')

const handleDeleteClick = () => {
    deleteModalVisible.value = true
}

const closeDeleteModal = () => {
    deleteModalVisible.value = false
    deleteError.value = ''
}

const confirmDelete = async () => {
    deleting.value = true
    deleteError.value = ''
    try {
        await usersApi.deleteUser(userId)
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'users' })
    } catch (err) {
        console.error('Failed to delete user:', err)
        deleteError.value = t('dashboard.userDetail.deleteFailed')
    } finally {
        deleting.value = false
    }
}

const goBack = useGoBack('users')

onMounted(fetchUser)
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <div v-if="loading" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error" class="error-container card">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary" @click="fetchUser">{{ $t('dashboard.userDetail.retry') }}</button>
        </div>

        <div v-else-if="user" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon">
                        <UserIcon :size="20" />
                    </div>
                    <div>
                        <h2 class="resource-title">
                            {{ user.username || $t('dashboard.userDetail.unknownUser') }}
                            <StatusBadge
                                :status="user.status || 'active'"
                                :label="$t('userStatus.' + (user.status || 'active'))"
                            />
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ user.uuid }}</span>
                            <button
                                class="copy-btn"
                                :title="$t('actions.copy')"
                                :aria-label="$t('actions.copy')"
                                @click="copyId(user.uuid)"
                            >
                                <Check v-if="copiedId === user.uuid" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-danger-outline btn-sm" @click="handleDeleteClick" :disabled="deleting">
                        <Trash2 :size="14" />
                        {{ deleting ? $t('dashboard.userDetail.deleting') : $t('dashboard.userDetail.deleteUser') }}
                    </button>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.userDetail.generalInfo') }}</h3>
                    <div class="key-value-list">
                        <InfoRow :label="$t('dashboard.userDetail.username')">{{ user.username }}</InfoRow>
                        <InfoRow :label="$t('dashboard.userDetail.email')">
                            <template #label><Mail :size="14" /> {{ $t('dashboard.userDetail.email') }}</template>
                            {{ user.email || '-' }}
                        </InfoRow>
                        <!-- 接口（cpgateway 的 userOut）只返回系统角色，不返回任何组织内角色；
                             原先这里写 user.role，该字段从不存在，永远显示回退值 Member -->
                        <InfoRow :label="$t('dashboard.userDetail.systemRole')">
                            <template #label
                                ><Shield :size="14" /> {{ $t('dashboard.userDetail.systemRole') }}</template
                            >
                            {{ user.is_superuser ? $t('roles.superuser') : $t('roles.member') }}
                        </InfoRow>
                        <InfoRow :label="$t('dashboard.userDetail.language')">{{ user.language || '-' }}</InfoRow>
                        <InfoRow :label="$t('dashboard.userDetail.createdAt')">{{
                            formatDateTime(user.created_at)
                        }}</InfoRow>
                    </div>
                </div>
            </div>
        </div>

        <DeleteModal
            :show="deleteModalVisible"
            :message="$t('dashboard.userDetail.deleteConfirm')"
            :resource-name="user?.username"
            :resource-id="user?.uuid"
            :loading="deleting"
            :error="deleteError"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
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
</style>
