<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '../../stores/auth'
import { Cloud, User, Lock, ArrowRight, ShieldCheck, XCircle, AlertTriangle } from 'lucide-vue-next'
import SecurityVerify from '../../components/auth/SecurityVerify.vue'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()

const email = ref('')
const password = ref('')
const errorMessage = ref('')
const isVerified = ref(false)

const handleVerify = () => {
  isVerified.value = true
}

const handleSubmit = async () => {
  try {
    errorMessage.value = ''
    await auth.login(email.value, password.value)
    router.push('/dashboard')
  } catch (error: any) {
    console.error('Login failed:', error)
    isVerified.value = false // Reset verification on failure
    if (error.response?.status === 401) {
      errorMessage.value = t('auth.invalidCredentials')
    } else {
      errorMessage.value = t('messages.error')
    }
  }
}
</script>

<template>
  <div class="auth-page">
    <!-- Decorative background elements -->
    <div class="decor-circle decor-1"></div>
    <div class="decor-circle decor-2"></div>
    <div class="decor-circle decor-3"></div>

    <div class="auth-container">
      <div class="card auth-card">
        <div class="auth-header">
          <div class="auth-logo">
            <div class="logo-inner">
              <Cloud :size="32" />
            </div>
          </div>
          <h2>{{ t('auth.loginTitle') }}</h2>
          <p>{{ t('auth.loginSubtitle') }}</p>
        </div>

        <!-- Error Message -->
        <div v-if="errorMessage" class="error-alert">
          <XCircle :size="18" />
          <span>{{ errorMessage }}</span>
        </div>

        <div v-if="auth.failedAttempts >= 3" class="warning-alert">
          <AlertTriangle :size="18" />
          <span>{{ t('auth.tooManyAttempts') }}</span>
        </div>

        <form @submit.prevent="handleSubmit" class="auth-form">
          <div class="form-group">
            <label class="form-label">{{ t('auth.username') }}</label>
            <div class="input-wrapper">
              <User class="input-icon" :size="18" />
              <input 
                type="text" 
                class="form-control" 
                v-model="email"
                required 
                :placeholder="t('auth.username')"
              />
            </div>
          </div>
          
          <div class="form-group">
            <div class="label-row">
              <label class="form-label">{{ t('auth.password') }}</label>
              <RouterLink to="/forgot-password" class="forgot-link">{{ t('auth.forgotPassword') }}</RouterLink>
            </div>
            <div class="input-wrapper">
              <Lock class="input-icon" :size="18" />
              <input 
                type="password" 
                class="form-control" 
                v-model="password"
                required 
                placeholder="••••••••"
              />
            </div>
          </div>

          <div class="form-options">
            <label class="checkbox-label">
              <input type="checkbox" />
              <span>{{ t('auth.rememberMe') }}</span>
            </label>
          </div>

          <SecurityVerify 
            v-if="auth.failedAttempts >= 3" 
            @verify="handleVerify"
            class="security-check-container"
          />

          <button 
            type="submit" 
            class="btn btn-primary btn-block btn-lg"
            :disabled="auth.isLoading || (auth.failedAttempts >= 3 && !isVerified)"
          >
            <span>{{ auth.isLoading ? t('auth.signingIn') : t('auth.signIn') }}</span>
            <ArrowRight v-if="!auth.isLoading" :size="18" />
            <div v-else class="loading-spinner-sm"></div>
          </button>
        </form>
        
        <div class="auth-footer">
          <p>
            {{ t('auth.noAccount') }} 
            <RouterLink to="/register" class="link-highlight">{{ t('nav.signUp') }}</RouterLink>
          </p>
        </div>

        <div class="trust-badge">
          <ShieldCheck :size="14" />
          <span>{{ t('hero.badge').split('·')[0] }}</span>
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
  background: radial-gradient(circle at 0% 0%, var(--primary-50) 0%, transparent 40%),
              radial-gradient(circle at 100% 100%, var(--primary-100) 0%, transparent 40%),
              var(--bg-secondary);
  position: relative;
  overflow: hidden;
  padding: var(--spacing-6);
}

/* Decorative circles */
.decor-circle {
  position: absolute;
  border-radius: 50%;
  filter: blur(80px);
  z-index: 0;
  opacity: 0.5;
}

.decor-1 {
  width: 400px;
  height: 400px;
  background: var(--primary-200);
  top: -100px;
  right: -50px;
}

.decor-2 {
  width: 350px;
  height: 350px;
  background: #93c5fd;
  bottom: -50px;
  left: -50px;
}

.decor-3 {
  width: 250px;
  height: 250px;
  background: #bae6fd;
  top: 40%;
  left: 10%;
  opacity: 0.3;
}

.auth-container {
  width: 100%;
  max-width: 440px;
  position: relative;
  z-index: 1;
  animation: fadeIn 0.6s ease-out;
}

@keyframes fadeIn {
  from { opacity: 0; transform: translateY(20px); }
  to { opacity: 1; transform: translateY(0); }
}

.auth-card {
  background: rgba(255, 255, 255, 0.9);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(255, 255, 255, 0.5);
  padding: var(--spacing-10);
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.08);
}

.auth-header {
  text-align: center;
  margin-bottom: var(--spacing-8);
}

.auth-logo {
  display: flex;
  justify-content: center;
  margin-bottom: var(--spacing-6);
}

.logo-inner {
  width: 64px;
  height: 64px;
  background: var(--primary-gradient);
  color: white;
  border-radius: var(--radius-xl);
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 8px 16px rgba(59, 130, 246, 0.2);
}

h2 {
  font-size: var(--font-size-2xl);
  font-weight: 800;
  color: var(--text-primary);
  margin-bottom: var(--spacing-2);
  letter-spacing: -0.02em;
}

p {
  color: var(--text-tertiary);
  font-size: var(--font-size-base);
}

.error-alert {
  background-color: var(--error-50, #fef2f2);
  border: 1px solid var(--error-200, #fecaca);
  color: var(--error-700, #b91c1c);
  padding: var(--spacing-4);
  border-radius: var(--radius-lg);
  margin-bottom: var(--spacing-6);
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
  font-size: var(--font-size-sm);
  font-weight: 500;
  animation: shake 0.5s cubic-bezier(.36,.07,.19,.97) both;
}

@keyframes shake {
  10%, 90% { transform: translate3d(-1px, 0, 0); }
  20%, 80% { transform: translate3d(2px, 0, 0); }
  30%, 50%, 70% { transform: translate3d(-4px, 0, 0); }
  40%, 60% { transform: translate3d(4px, 0, 0); }
}

.warning-alert {
  background-color: #fffbeb;
  border: 1px solid #fef3c7;
  color: #92400e;
  padding: var(--spacing-4);
  border-radius: var(--radius-lg);
  margin-bottom: var(--spacing-6);
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
  font-size: var(--font-size-sm);
  font-weight: 500;
  animation: fadeIn 0.4s ease-out;
}

.security-check-container {
  margin-bottom: var(--spacing-6);
}

.auth-form {
  margin-bottom: var(--spacing-8);
}

.label-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-2);
}

.form-label {
  display: block;
  font-size: var(--font-size-sm);
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: var(--spacing-2);
}

.forgot-link {
  font-size: var(--font-size-xs);
  color: var(--primary-600);
  font-weight: 600;
}

.forgot-link:hover {
  text-decoration: underline;
}

.input-wrapper {
  position: relative;
}

.input-icon {
  position: absolute;
  left: 14px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-light);
  transition: color 0.2s;
}

.form-control {
  width: 100%;
  padding: 12px 14px 12px 42px;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-lg);
  font-size: var(--font-size-base);
  background: white;
  transition: all 0.2s;
}

.form-control:focus {
  outline: none;
  border-color: var(--primary-400);
  box-shadow: 0 0 0 4px var(--primary-50);
}

.form-control:focus + .input-icon {
  color: var(--primary-500);
}

.form-options {
  margin: var(--spacing-4) 0 var(--spacing-6);
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  font-size: var(--font-size-sm);
  color: var(--text-tertiary);
  cursor: pointer;
}

.checkbox-label input {
  width: 16px;
  height: 16px;
  accent-color: var(--primary-color);
}

.btn-block {
  width: 100%;
  display: flex;
  justify-content: center;
  align-items: center;
  gap: var(--spacing-2);
}

.loading-spinner-sm {
  width: 18px;
  height: 18px;
  border: 2px solid rgba(255, 255, 255, 0.3);
  border-top-color: white;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.auth-footer {
  text-align: center;
  border-top: 1px solid var(--border-light);
  padding-top: var(--spacing-6);
}

.link-highlight {
  color: var(--primary-600);
  font-weight: 700;
  margin-left: var(--spacing-1);
}

.link-highlight:hover {
  text-decoration: underline;
}

.trust-badge {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--spacing-1);
  margin-top: var(--spacing-8);
  font-size: var(--font-size-xs);
  color: var(--text-light);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  font-weight: 600;
}
</style>

