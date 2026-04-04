<script setup lang="ts">
import { ref, reactive, computed } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Cloud, User, Mail, Lock, ArrowRight, ShieldCheck, Check, Building2, XCircle, Eye, EyeOff } from 'lucide-vue-next'
import { authApi } from '../../api/auth'

const { t, locale } = useI18n()
const router = useRouter()
const isLoading = ref(false)

const form = reactive({
  lastName: '',
  firstName: '',
  username: '',
  email: '',
  password: '',
  confirmPassword: '',
  orgName: '',
  orgSlug: ''
})

const showPassword = ref(false)
const showConfirmPassword = ref(false)

const autoGenerateSlug = () => {
  if (form.orgName && !form.orgSlug) {
    form.orgSlug = form.orgName
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-|-$/g, '')
  }
}

const isPasswordMismatch = computed(() => {
  return !!(form.password && form.confirmPassword && form.password !== form.confirmPassword)
})

const usernameError = computed(() => {
  if (!form.username) return ''
  if (/^[0-9]/.test(form.username)) return t('auth.usernameStartLetterError')
  if (!/^[a-zA-Z0-9]+$/.test(form.username)) return t('auth.usernameCharsetError')
  return ''
})

const handleSubmit = async () => {
    if (form.password !== form.confirmPassword) {
      alert(t('auth.passwordMismatch')) 
      return
    }

    if (usernameError.value) {
      alert(usernameError.value)
      return
    }

    try {
        isLoading.value = true
        // Call the registration API
        await authApi.register({
            email: form.email,
            username: form.username,
            password: form.password,
            language: locale.value === 'zh' ? 'zh' : 'en',
            org_name: form.orgName,
            org_slug: form.orgSlug
        })
        
        // Redirect to success page
        router.push('/register/success')
    } catch (error: any) {
        console.error('Registration failed:', error)
        const detail = error.response?.data?.detail
        const msg = Array.isArray(detail)
            ? detail.map((d: any) => d.msg).join('; ')
            : (detail || error.message || t('messages.error'))
        alert(msg)
    } finally {
        isLoading.value = false
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
          <h2>{{ t('auth.registerTitle') }}</h2>
          <p>{{ t('auth.registerSubtitle') }}</p>
        </div>

        <form @submit.prevent="handleSubmit" class="auth-form">
          <div class="form-row">
            <div class="form-group">
              <label class="form-label">{{ t('auth.lastName') }}</label>
              <input 
                v-model="form.lastName" 
                type="text" 
                class="form-control minimal" 
                required 
                :placeholder="t('auth.lastName')" 
              />
            </div>
            <div class="form-group">
              <label class="form-label">{{ t('auth.firstName') }}</label>
              <input 
                v-model="form.firstName" 
                type="text" 
                class="form-control minimal" 
                required 
                :placeholder="t('auth.firstName')" 
              />
            </div>
          </div>

          <div class="form-group">
            <label class="form-label">{{ t('auth.username') }}</label>
            <div class="input-wrapper">
              <User class="input-icon" :size="18" />
              <input 
                v-model="form.username" 
                type="text" 
                class="form-control" 
                :class="{ 'border-error': !!usernameError }"
                required 
                :placeholder="t('auth.username')" 
              />
            </div>
            <span v-if="usernameError" class="error-text">
              <XCircle :size="12" /> {{ usernameError }}
            </span>
          </div>

          <div class="form-group">
            <label class="form-label">{{ t('auth.email') }}</label>
            <div class="input-wrapper">
              <Mail class="input-icon" :size="18" />
              <input 
                v-model="form.email" 
                type="email" 
                class="form-control" 
                required 
                placeholder="john@example.com" 
              />
            </div>
          </div>

          <div class="form-group">
            <label class="form-label">{{ t('auth.orgName') }}</label>
            <div class="input-wrapper">
              <Building2 class="input-icon" :size="18" />
              <input
                v-model="form.orgName"
                type="text"
                class="form-control"
                required
                :placeholder="t('auth.orgNamePlaceholder')"
                @blur="autoGenerateSlug"
              />
            </div>
          </div>

          <div class="form-group">
            <label class="form-label">{{ t('auth.orgSlug') }}</label>
            <div class="input-wrapper">
              <Building2 class="input-icon" :size="18" />
              <input
                v-model="form.orgSlug"
                type="text"
                class="form-control"
                required
                :placeholder="t('auth.orgSlugPlaceholder')"
                pattern="^[a-z0-9][a-z0-9-]*[a-z0-9]$"
              />
            </div>
            <span class="hint-text">{{ t('auth.orgSlugHint') }}</span>
          </div>

          <div class="form-group">
            <label class="form-label">{{ t('auth.password') }}</label>
            <div class="input-wrapper">
              <Lock class="input-icon" :size="18" />
              <input 
                v-model="form.password" 
                :type="showPassword ? 'text' : 'password'" 
                class="form-control" 
                :class="{ 'border-error': isPasswordMismatch, 'with-suffix': true }"
                required 
                placeholder="••••••••" 
              />
              <button 
                type="button" 
                class="password-toggle"
                @click="showPassword = !showPassword"
                tabindex="-1"
              >
                <EyeOff v-if="showPassword" :size="18" />
                <Eye v-else :size="18" />
              </button>
            </div>
          </div>

          <div class="form-group">
            <label class="form-label">{{ t('auth.confirmPassword') }}</label>
            <div class="input-wrapper">
              <Lock class="input-icon" :size="18" />
              <input 
                v-model="form.confirmPassword" 
                :type="showConfirmPassword ? 'text' : 'password'" 
                class="form-control" 
                :class="{ 'border-error': isPasswordMismatch, 'with-suffix': true }"
                required 
                placeholder="••••••••" 
              />
              <button 
                type="button" 
                class="password-toggle"
                @click="showConfirmPassword = !showConfirmPassword"
                tabindex="-1"
              >
                <EyeOff v-if="showConfirmPassword" :size="18" />
                <Eye v-else :size="18" />
              </button>
            </div>
            <span v-if="isPasswordMismatch" class="error-text">
              <XCircle :size="12" /> {{ t('auth.passwordMismatch') }}
            </span>
          </div>

          <button 
            type="submit" 
            class="btn btn-primary btn-block btn-lg"
            :disabled="isLoading || isPasswordMismatch || !!usernameError"
          >
            <span>{{ isLoading ? t('auth.signingIn') : t('nav.signUp') }}</span>
            <ArrowRight v-if="!isLoading" :size="18" />
            <div v-else class="loading-spinner-sm"></div>
          </button>
        </form>
        
        <div class="auth-footer">
          <p>
            {{ t('auth.hasAccount') }} 
            <RouterLink to="/login" class="link-highlight">{{ t('auth.signIn') }}</RouterLink>
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
  background: radial-gradient(circle at 100% 0%, var(--primary-50) 0%, transparent 40%),
              radial-gradient(circle at 0% 100%, var(--primary-100) 0%, transparent 40%),
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
  width: 500px;
  height: 500px;
  background: var(--primary-100);
  top: -150px;
  left: -100px;
}

.decor-2 {
  width: 400px;
  height: 400px;
  background: #bae6fd;
  bottom: -100px;
  right: -50px;
}

.decor-3 {
  width: 300px;
  height: 300px;
  background: var(--primary-200);
  top: 10%;
  right: 15%;
  opacity: 0.3;
}

.auth-container {
  width: 100%;
  max-width: 520px;
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

.auth-form {
  margin-bottom: var(--spacing-8);
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--spacing-4);
}

.form-group {
  margin-bottom: var(--spacing-4);
  position: relative;
}

.form-label {
  display: block;
  font-size: var(--font-size-sm);
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: var(--spacing-2);
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

.form-control.minimal {
  padding-left: 14px;
}

.form-control:focus {
  outline: none;
  border-color: var(--primary-400);
  box-shadow: 0 0 0 4px var(--primary-50);
}

.form-control:focus + .input-icon {
  color: var(--primary-500);
}

.form-control.with-suffix {
  padding-right: 42px;
}

.password-toggle {
  position: absolute;
  right: 12px;
  top: 50%;
  transform: translateY(-50%);
  background: none;
  border: none;
  color: var(--text-light);
  cursor: pointer;
  padding: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-md);
  transition: all 0.2s;
  z-index: 2;
}

.password-toggle:hover {
  color: var(--primary-500);
  background: var(--primary-50);
}

.border-error {
  border-color: var(--error-color) !important;
}

.error-text {
  color: var(--error-color);
  font-size: var(--font-size-xs);
  margin-top: var(--spacing-1);
  display: flex;
  align-items: center;
  gap: 4px;
  font-weight: 500;
}

.hint-text {
  color: var(--text-light);
  font-size: var(--font-size-xs);
  margin-top: var(--spacing-1);
  display: block;
}

.btn-block {
  width: 100%;
  display: flex;
  justify-content: center;
  align-items: center;
  gap: var(--spacing-2);
  margin-top: var(--spacing-4);
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

@media (max-width: 480px) {
  .form-row {
    grid-template-columns: 1fr;
    gap: 0;
  }
}
</style>

