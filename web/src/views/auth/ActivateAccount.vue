<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { CheckCircle, XCircle, Loader2, ArrowRight, Cloud } from 'lucide-vue-next'
import { authApi } from '../../api/auth'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const status = ref<'loading' | 'success' | 'error'>('loading')
const countdown = ref(5)

const activate = async () => {
    const token = route.query.token as string
    
    if (!token) {
        status.value = 'error'
        return
    }

    try {
        await authApi.activateAccount(token)
        status.value = 'success'
        
        // Auto redirect on success
        const timer = setInterval(() => {
            countdown.value--
            if (countdown.value <= 0) {
                clearInterval(timer)
                router.push('/login')
            }
        }, 1000)
    } catch (error) {
        console.error('Activation failed:', error)
        status.value = 'error'
    }
}

onMounted(() => {
    activate()
})
</script>

<template>
  <div class="auth-page">
    <!-- Decorative background elements -->
    <div class="decor-circle decor-1"></div>
    <div class="decor-circle decor-2"></div>
    <div class="decor-circle decor-3"></div>

    <div class="auth-container">
      <div class="card activation-card">
        <div class="auth-logo">
          <div class="logo-inner">
            <Cloud :size="32" />
          </div>
        </div>

        <!-- Loading State -->
        <div v-if="status === 'loading'" class="state-content">
          <div class="loading-wrapper">
            <Loader2 class="icon-spin" :size="64" />
          </div>
          <h2>{{ t('auth.activating') }}</h2>
          <p class="status-desc">Please wait while we verify your activation link.</p>
        </div>

        <!-- Success State -->
        <div v-if="status === 'success'" class="state-content success">
          <div class="status-icon-wrapper">
            <CheckCircle class="status-icon" :size="64" />
          </div>
          <h2>{{ t('auth.activationSuccess') }}</h2>
          
          <div class="countdown-section">
            <div class="countdown-bar">
              <div class="progress" :style="{ width: (countdown / 5) * 100 + '%' }"></div>
            </div>
            <p class="redirect-text">{{ t('auth.redirecting', { n: countdown }) }}</p>
          </div>

          <button class="btn btn-primary btn-block btn-lg" @click="router.push('/login')">
            <span>{{ t('auth.goToLogin') }}</span>
            <ArrowRight :size="18" />
          </button>
        </div>

        <!-- Error State -->
        <div v-if="status === 'error'" class="state-content error">
          <div class="status-icon-wrapper">
            <XCircle class="status-icon" :size="64" />
          </div>
          <h2>{{ t('auth.activationError') }}</h2>
          <p class="error-desc text-secondary">
            The link may have expired or is already used. Please try registering again or contact support.
          </p>

          <div class="error-actions">
            <button class="btn btn-primary btn-block" @click="router.push('/register')">
              {{ t('nav.signUp') }}
            </button>
            <button class="btn btn-secondary btn-block" @click="router.push('/login')">
              {{ t('auth.signIn') }}
            </button>
          </div>
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
  background: #bae6fd;
  bottom: -50px;
  left: -50px;
}

.decor-3 {
  width: 250px;
  height: 250px;
  background: var(--primary-50);
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

.activation-card {
  background: rgba(255, 255, 255, 0.9);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(255, 255, 255, 0.5);
  padding: var(--spacing-10);
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.08);
  text-align: center;
}

.auth-logo {
  display: flex;
  justify-content: center;
  margin-bottom: var(--spacing-8);
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
  gap: var(--spacing-6);
}

.loading-wrapper {
  color: var(--primary-500);
  display: flex;
  justify-content: center;
}

.icon-spin {
  animation: spin 2s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.status-icon-wrapper {
  display: flex;
  justify-content: center;
}

.success .status-icon {
  color: var(--success-color);
}

.error .status-icon {
  color: var(--error-color);
}

h2 {
  font-size: var(--font-size-2xl);
  font-weight: 800;
  color: var(--text-primary);
  margin: 0;
  letter-spacing: -0.02em;
}

.status-desc, .error-desc {
  font-size: var(--font-size-base);
  color: var(--text-tertiary);
  line-height: 1.6;
}

.countdown-section {
  background: var(--bg-secondary);
  padding: var(--spacing-5);
  border-radius: var(--radius-lg);
}

.countdown-bar {
  width: 100%;
  height: 6px;
  background-color: var(--border-light);
  border-radius: var(--radius-full);
  margin-bottom: var(--spacing-3);
  overflow: hidden;
}

.progress {
  height: 100%;
  background: var(--primary-gradient);
  transition: width 1s linear;
}

.redirect-text {
  font-size: var(--font-size-sm);
  color: var(--text-light);
  font-weight: 500;
  margin: 0;
}

.error-actions {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-3);
}

.btn-block {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--spacing-2);
}
</style>

