<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Mail, CheckCircle, ArrowRight, Cloud } from 'lucide-vue-next'

const { t } = useI18n()
const router = useRouter()
const countdown = ref(5)
let timer: any = null

onMounted(() => {
  timer = setInterval(() => {
    countdown.value--
    if (countdown.value <= 0) {
      clearInterval(timer)
      router.push('/login')
    }
  }, 1000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div class="auth-page">
    <!-- Decorative background elements -->
    <div class="decor-circle decor-1"></div>
    <div class="decor-circle decor-2"></div>
    <div class="decor-circle decor-3"></div>

    <div class="auth-container">
      <div class="card success-card">
        <div class="success-header">
          <div class="auth-logo">
            <div class="logo-inner">
              <Cloud :size="32" />
            </div>
          </div>
          <div class="status-icon-wrapper">
            <CheckCircle :size="64" class="success-icon" />
          </div>
          <h2>{{ t('auth.welcome') }}</h2>
        </div>

        <div class="success-body">
          <div class="mail-illustration">
            <div class="mail-circle">
              <Mail :size="40" class="mail-icon" />
            </div>
            <div class="floating-dots">
              <span></span><span></span><span></span>
            </div>
          </div>
          <p class="message">{{ t('auth.checkEmail') }}</p>
          
          <div class="countdown-section">
            <div class="countdown-bar">
              <div class="progress" :style="{ width: (countdown / 5) * 100 + '%' }"></div>
            </div>
            <p class="redirect-text">
              {{ t('auth.redirecting', { n: countdown }) }}
            </p>
          </div>

          <button class="btn btn-primary btn-block btn-lg" @click="router.push('/login')">
            <span>{{ t('auth.signIn') }}</span>
            <ArrowRight :size="18" />
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
  background: radial-gradient(circle at 10% 10%, var(--primary-50) 0%, transparent 40%),
              radial-gradient(circle at 90% 90%, var(--primary-100) 0%, transparent 40%),
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
  width: 450px;
  height: 450px;
  background: var(--primary-100);
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
  background: var(--primary-200);
  top: 30%;
  right: 10%;
  opacity: 0.3;
}

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

.success-card {
  background: rgba(255, 255, 255, 0.9);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(255, 255, 255, 0.5);
  padding: var(--spacing-10);
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.08);
  text-align: center;
}

.success-header {
  margin-bottom: var(--spacing-8);
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

.status-icon-wrapper {
  margin-bottom: var(--spacing-4);
}

.success-icon {
  color: var(--success-color);
}

h2 {
  font-size: var(--font-size-2xl);
  font-weight: 800;
  color: var(--text-primary);
  margin: 0;
  letter-spacing: -0.02em;
}

.mail-illustration {
  position: relative;
  width: fit-content;
  margin: 0 auto var(--spacing-6);
}

.mail-circle {
  width: 96px;
  height: 96px;
  background-color: var(--primary-50);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 2px solid white;
  box-shadow: 0 8px 24px rgba(59, 130, 246, 0.1);
}

.mail-icon {
  color: var(--primary-500);
}

.floating-dots span {
  position: absolute;
  width: 8px;
  height: 8px;
  background: var(--primary-300);
  border-radius: 50%;
  animation: float 3s infinite ease-in-out;
}

.floating-dots span:nth-child(1) { top: 10%; right: -20px; animation-delay: 0s; }
.floating-dots span:nth-child(2) { top: 50%; left: -30px; animation-delay: 1s; width: 12px; height: 12px; }
.floating-dots span:nth-child(3) { bottom: 10%; right: -15px; animation-delay: 2s; }

@keyframes float {
  0%, 100% { transform: translateY(0); }
  50% { transform: translateY(-15px); }
}

.message {
  font-size: var(--font-size-lg);
  color: var(--text-secondary);
  line-height: 1.6;
  margin-bottom: var(--spacing-8);
}

.countdown-section {
  background: var(--bg-secondary);
  padding: var(--spacing-5);
  border-radius: var(--radius-lg);
  margin-bottom: var(--spacing-8);
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

.btn-block {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--spacing-2);
}
</style>

