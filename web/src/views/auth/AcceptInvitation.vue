<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { CheckCircle, XCircle, Loader2, ArrowRight, Cloud, Mail } from 'lucide-vue-next'
import { authApi } from '../../api/auth'
import { ORG_ROLES } from '../../api/orgs'

const route = useRoute()
const router = useRouter()

const status = ref<'loading' | 'info' | 'accepting' | 'success' | 'error'>('loading')
const errorMsg = ref('')

// Invitation info
const invitationInfo = ref<{
    email: string
    org_name: string
    org_role: number
    inviter_email: string
    is_existing_user: boolean
    expires_at: string
} | null>(null)

// New user form
const username = ref('')
const password = ref('')
const confirmPassword = ref('')

const token = (route.query.token as string) || ''

const fetchInfo = async () => {
    if (!token) {
        status.value = 'error'
        errorMsg.value = 'Missing invitation token.'
        return
    }
    try {
        const response = await authApi.getInvitationInfo(token)
        invitationInfo.value = response.data
        status.value = 'info'
    } catch (err: any) {
        status.value = 'error'
        errorMsg.value = err.response?.data?.detail || 'Invalid or expired invitation.'
    }
}

const handleAccept = async () => {
    if (!invitationInfo.value) return

    // Validate new user fields
    if (!invitationInfo.value.is_existing_user) {
        if (!username.value.trim()) {
            errorMsg.value = 'Username is required.'
            return
        }
        if (!password.value || password.value.length < 8) {
            errorMsg.value = 'Password must be at least 8 characters.'
            return
        }
        if (password.value !== confirmPassword.value) {
            errorMsg.value = 'Passwords do not match.'
            return
        }
    }

    errorMsg.value = ''
    status.value = 'accepting'

    try {
        await authApi.acceptInvitation({
            token,
            username: invitationInfo.value.is_existing_user ? undefined : username.value.trim(),
            password: invitationInfo.value.is_existing_user ? undefined : password.value,
        })
        status.value = 'success'
    } catch (err: any) {
        status.value = 'info'
        errorMsg.value = err.response?.data?.detail || 'Failed to accept invitation.'
    }
}

onMounted(() => {
    fetchInfo()
})
</script>

<template>
  <div class="auth-page">
    <div class="decor-circle decor-1"></div>
    <div class="decor-circle decor-2"></div>

    <div class="auth-container">
      <div class="card invitation-card">
        <div class="auth-logo">
          <div class="logo-inner">
            <Cloud :size="32" />
          </div>
        </div>

        <!-- Loading -->
        <div v-if="status === 'loading'" class="state-content">
          <div class="loading-wrapper">
            <Loader2 class="icon-spin" :size="48" />
          </div>
          <h2>Loading Invitation...</h2>
        </div>

        <!-- Invitation Info -->
        <div v-else-if="status === 'info' || status === 'accepting'" class="state-content">
          <div class="status-icon-wrapper">
            <Mail :size="48" style="color: var(--primary-500);" />
          </div>
          <h2>You're Invited!</h2>

          <div class="invite-details">
            <div class="invite-detail-row">
              <span class="label">Organization</span>
              <span class="value">{{ invitationInfo?.org_name }}</span>
            </div>
            <div class="invite-detail-row">
              <span class="label">Role</span>
              <span class="value">
                <span class="role-badge" :class="'role-' + invitationInfo?.org_role">
                  {{ ORG_ROLES[invitationInfo?.org_role || 1] }}
                </span>
              </span>
            </div>
            <div class="invite-detail-row">
              <span class="label">Invited by</span>
              <span class="value">{{ invitationInfo?.inviter_email }}</span>
            </div>
          </div>

          <!-- New user: need to set username & password -->
          <div v-if="!invitationInfo?.is_existing_user" class="new-user-form">
            <p class="form-hint">Create your account to join:</p>
            <div class="form-group">
              <label class="form-label">Email</label>
              <input type="email" class="form-input" :value="invitationInfo?.email" disabled />
            </div>
            <div class="form-group">
              <label class="form-label">Username</label>
              <input v-model="username" type="text" class="form-input" placeholder="Choose a username" />
            </div>
            <div class="form-group">
              <label class="form-label">Password</label>
              <input v-model="password" type="password" class="form-input" placeholder="At least 8 characters" />
            </div>
            <div class="form-group">
              <label class="form-label">Confirm Password</label>
              <input v-model="confirmPassword" type="password" class="form-input" placeholder="Confirm password" />
            </div>
          </div>

          <!-- Existing user info -->
          <div v-else class="existing-user-info">
            <p>You already have an account (<strong>{{ invitationInfo?.email }}</strong>). Click below to join this organization.</p>
          </div>

          <div v-if="errorMsg" class="error-banner">{{ errorMsg }}</div>

          <button class="btn btn-primary btn-block btn-lg" @click="handleAccept" :disabled="status === 'accepting'">
            <Loader2 v-if="status === 'accepting'" class="icon-spin" :size="18" />
            <span v-else>Accept Invitation</span>
          </button>
        </div>

        <!-- Success -->
        <div v-else-if="status === 'success'" class="state-content success">
          <div class="status-icon-wrapper">
            <CheckCircle class="status-icon" :size="64" />
          </div>
          <h2>Welcome!</h2>
          <p class="status-desc">
            You've successfully joined <strong>{{ invitationInfo?.org_name }}</strong>.
          </p>
          <button class="btn btn-primary btn-block btn-lg" @click="router.push('/login')">
            <span>Go to Login</span>
            <ArrowRight :size="18" />
          </button>
        </div>

        <!-- Error -->
        <div v-else-if="status === 'error'" class="state-content error">
          <div class="status-icon-wrapper">
            <XCircle class="status-icon" :size="64" />
          </div>
          <h2>Invalid Invitation</h2>
          <p class="error-desc">{{ errorMsg }}</p>
          <button class="btn btn-secondary btn-block" @click="router.push('/login')">
            Go to Login
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.auth-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-secondary);
  background: radial-gradient(circle at 20% 20%, var(--primary-50) 0%, transparent 40%),
              radial-gradient(circle at 80% 80%, var(--primary-100) 0%, transparent 40%),
              var(--bg-secondary);
  position: relative;
  overflow: hidden;
  padding: var(--spacing-6);
}

.decor-circle {
  position: absolute;
  border-radius: 50%;
  filter: blur(80px);
  z-index: 0;
  opacity: 0.5;
}
.decor-1 { width: 400px; height: 400px; background: var(--primary-200); top: -100px; right: -50px; }
.decor-2 { width: 350px; height: 350px; background: #bae6fd; bottom: -50px; left: -50px; }

.auth-container {
  width: 100%;
  max-width: 480px;
  position: relative;
  z-index: 1;
  animation: fadeIn 0.6s ease-out;
}

@keyframes fadeIn {
  from { opacity: 0; transform: translateY(20px); }
  to { opacity: 1; transform: translateY(0); }
}

.invitation-card {
  background: rgba(255, 255, 255, 0.9);
  backdrop-filter: blur(20px);
  border: 1px solid rgba(255, 255, 255, 0.5);
  padding: var(--spacing-10);
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.08);
  text-align: center;
}

.auth-logo {
  display: flex;
  justify-content: center;
  margin-bottom: var(--spacing-6);
}

.logo-inner {
  width: 56px;
  height: 56px;
  background: var(--primary-gradient);
  color: white;
  border-radius: var(--radius-lg);
  display: flex;
  align-items: center;
  justify-content: center;
}

.state-content {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-5);
}

.loading-wrapper {
  color: var(--primary-500);
  display: flex;
  justify-content: center;
}

.icon-spin { animation: spin 2s linear infinite; }

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.status-icon-wrapper { display: flex; justify-content: center; }
.success .status-icon { color: var(--success-color); }
.error .status-icon { color: var(--error-color); }

h2 {
  font-size: var(--font-size-2xl);
  font-weight: 800;
  color: var(--text-primary);
  margin: 0;
}

.status-desc, .error-desc {
  font-size: var(--font-size-base);
  color: var(--text-tertiary);
  line-height: 1.6;
}

.invite-details {
  background: var(--bg-secondary);
  border: 1px solid var(--border-light);
  border-radius: var(--radius-lg);
  padding: var(--spacing-4);
  text-align: left;
}

.invite-detail-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--spacing-2) 0;
}

.invite-detail-row + .invite-detail-row {
  border-top: 1px solid var(--border-light);
}

.invite-detail-row .label {
  font-size: var(--font-size-sm);
  color: var(--text-tertiary);
}

.invite-detail-row .value {
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-medium);
  color: var(--text-primary);
}

.role-badge {
  display: inline-block;
  padding: 2px 10px;
  border-radius: var(--radius-full);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-semibold);
}
.role-1 { background: #dbeafe; color: #1d4ed8; }
.role-2 { background: #d1fae5; color: #065f46; }
.role-3 { background: #fef3c7; color: #92400e; }

.new-user-form {
  text-align: left;
}

.form-hint {
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  margin-bottom: var(--spacing-3);
}

.form-group {
  margin-bottom: var(--spacing-3);
}

.form-label {
  display: block;
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-medium);
  color: var(--text-secondary);
  margin-bottom: var(--spacing-1);
}

.form-input {
  width: 100%;
  padding: var(--spacing-2) var(--spacing-3);
  border: 1px solid var(--border-light);
  border-radius: var(--radius-md);
  font-size: var(--font-size-sm);
  outline: none;
  transition: border-color var(--transition-base);
}

.form-input:focus {
  border-color: var(--primary-500);
  box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.1);
}

.form-input:disabled {
  background: var(--bg-secondary);
  color: var(--text-tertiary);
}

.existing-user-info {
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  line-height: 1.6;
}

.error-banner {
  background: var(--error-light);
  color: var(--error-color);
  padding: var(--spacing-2) var(--spacing-3);
  border-radius: var(--radius-md);
  font-size: var(--font-size-sm);
}

.btn-block {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--spacing-2);
}

.btn-lg {
  padding: var(--spacing-3) var(--spacing-6);
  font-size: var(--font-size-base);
}
</style>
