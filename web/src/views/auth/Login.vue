<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '../../stores/auth'
import { Cloud, User, Lock, ArrowRight, XCircle, AlertTriangle, Eye, EyeOff, Languages } from 'lucide-vue-next'
import { setLanguage, getCurrentLanguage } from '../../locales'
import SecurityVerify from '../../components/auth/SecurityVerify.vue'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()

const email = ref('')
const password = ref('')
const rememberMe = ref(false)
const errorMessage = ref('')
const isVerified = ref(false)
const showPassword = ref(false)
const currentLang = ref(getCurrentLanguage())

const handleVerify = () => {
  isVerified.value = true
}

const toggleLang = () => {
  const newLang = currentLang.value === 'zh' ? 'en' : 'zh'
  setLanguage(newLang)
  currentLang.value = newLang
}

const handleSubmit = async () => {
  try {
    errorMessage.value = ''
    await auth.login(email.value, password.value, rememberMe.value)
    router.push('/dashboard')
  } catch (error: any) {
    console.error('Login failed:', error)
    isVerified.value = false
    if (error.response?.status === 401) {
      errorMessage.value = t('auth.invalidCredentials')
    } else {
      errorMessage.value = t('messages.error')
    }
  }
}
</script>

<template>
  <div class="pl-page">
    <!-- Top Navbar -->
    <header class="pl-nav">
      <router-link to="/" class="pl-nav-brand">
        <div class="pl-logo-wrapper">
          <Cloud :size="20" fill="currentColor" />
        </div>
        <span>CloudLand</span>
      </router-link>
      <div class="pl-nav-actions">
        <button class="pl-lang-btn" @click="toggleLang" :title="currentLang === 'zh' ? 'Switch to English' : '切换到中文'">
          <Languages :size="16" />
          <span>{{ currentLang === 'zh' ? 'EN' : '中文' }}</span>
        </button>
      </div>
    </header>

    <!-- Main Content -->
    <main class="pl-main">
      <div class="pl-card">
        <!-- Left: Branded panel -->
        <div class="pl-card-left">
          <div class="pl-card-left-content">
            <h1 class="pl-hero-title">{{ t('privateCloud.hero.title') }}</h1>
            <p class="pl-hero-subtitle">
              {{ t('privateCloud.hero.subtitle') }}
            </p>
          </div>
          <!-- Subtle decoration -->
          <div class="pl-decor-wave"></div>
        </div>

        <!-- Right: Login panel -->
        <div class="pl-card-right">
          <div class="pl-card-right-content">
            <h2 class="pl-form-title">{{ t('privateCloud.cta.title') }}</h2>
            <p class="pl-form-subtitle">{{ t('privateCloud.cta.subtitle') }}</p>

            <!-- Error Message -->
            <div v-if="errorMessage" class="pl-error-box">
              <XCircle :size="16" />
              <span>{{ errorMessage }}</span>
            </div>

            <!-- Too Many Attempts Warning -->
            <div v-if="auth.failedAttempts >= 3" class="pl-warning-box">
              <AlertTriangle :size="16" />
              <span>{{ t('auth.tooManyAttempts') }}</span>
            </div>

            <form @submit.prevent="handleSubmit" class="pl-form">
              <div class="pl-form-group">
                <label class="pl-label">{{ t('auth.username') }}</label>
                <div class="pl-input-wrapper">
                  <User class="pl-input-icon" :size="18" />
                  <input 
                    type="text" 
                    class="pl-input" 
                    v-model="email"
                    required 
                    :placeholder="t('auth.username')"
                  />
                </div>
              </div>
              
              <div class="pl-form-group">
                <label class="pl-label">{{ t('auth.password') }}</label>
                <div class="pl-input-wrapper">
                  <Lock class="pl-input-icon" :size="18" />
                  <input 
                    :type="showPassword ? 'text' : 'password'" 
                    class="pl-input" 
                    v-model="password"
                    required 
                    placeholder="••••••••"
                  />
                  <button 
                    type="button" 
                    class="pl-password-toggle"
                    @click="showPassword = !showPassword"
                    tabindex="-1"
                  >
                    <EyeOff v-if="showPassword" :size="18" />
                    <Eye v-else :size="18" />
                  </button>
                </div>
              </div>

              <div class="pl-form-actions">
                <label class="pl-checkbox">
                  <input type="checkbox" v-model="rememberMe" />
                  <span class="pl-checkbox-box"></span>
                  <span class="pl-checkbox-label">{{ t('auth.rememberMe') }}</span>
                </label>
                <router-link to="/forgot-password" class="pl-forgot-link">{{ t('auth.forgotPassword') }}</router-link>
              </div>

              <!-- Security Verification (Captcha) -->
              <SecurityVerify 
                v-if="auth.failedAttempts >= 3" 
                @verify="handleVerify"
                class="pl-security-check"
              />

              <button 
                type="submit" 
                class="pl-btn-login"
                :disabled="auth.isLoading || (auth.failedAttempts >= 3 && !isVerified)"
              >
                <span>{{ auth.isLoading ? t('auth.signingIn') : t('auth.signIn') }}</span>
                <ArrowRight v-if="!auth.isLoading" :size="18" />
                <div v-else class="pl-spinner"></div>
              </button>
            </form>

            <div class="pl-divider-container">
              <div class="pl-divider"></div>
              <span class="pl-divider-text">{{ t('privateCloud.action.newUser') }}</span>
            </div>

            <button class="pl-btn-request" @click="router.push('/register')">
              {{ t('nav.signUp') }}
            </button>
          </div>
        </div>
      </div>
    </main>

    <!-- Footer -->
    <footer class="pl-footer">
      <p class="pl-copyright">{{ t('footer.copyright') }}</p>
      <div class="pl-footer-links">
        <a href="#">{{ t('privateCloud.footer.security') }}</a>
        <a href="#">{{ t('privateCloud.footer.terms') }}</a>
        <a href="#">{{ t('privateCloud.footer.privacy') }}</a>
        <a href="/swagger/api/v1/index.html" target="_blank" rel="noopener">{{ t('nav.documentation') }}</a>
      </div>
    </footer>
  </div>
</template>

<style scoped>
/* ── Reset & Container ── */
.pl-page {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  background: linear-gradient(180deg, #e0f2fe 0%, #f0f9ff 40%, #f8fafc 100%);
  font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  color: #1a2332;
}

/* ── Navbar ── */
.pl-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 48px;
  height: 80px;
  background: transparent;
  flex-shrink: 0;
}

.pl-nav-brand {
  display: flex;
  align-items: center;
  gap: 12px;
  text-decoration: none;
  font-weight: 700;
  font-size: 1.25rem;
  color: #1a2332;
}

.pl-logo-wrapper {
  background: #0ea5e9;
  color: white;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.pl-nav-actions {
  display: flex;
  gap: 12px;
}

.pl-lang-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.7);
  color: #475569;
  cursor: pointer;
  font-size: 0.8125rem;
  font-weight: 600;
  transition: all 0.2s;
}

.pl-lang-btn:hover {
  background: #fff;
  border-color: #cbd5e1;
  color: #0ea5e9;
}

/* ── Main Layout ── */
.pl-main {
  flex: 1;
  display: flex;
  overflow: hidden;
}

.pl-card {
  display: flex;
  width: 100%;
  background: #fff;
}

/* ── Left Panel ── */
.pl-card-left {
  flex: 1.4;
  background: linear-gradient(135deg, #bae6fd 0%, #0ea5e9 100%);
  color: #fff;
  padding: 60px 8%;
  display: flex;
  flex-direction: column;
  justify-content: center;
  position: relative;
  overflow: hidden;
}

.pl-card-left-content {
  position: relative;
  z-index: 1;
}

.pl-hero-title {
  font-size: 2.75rem;
  font-weight: 800;
  line-height: 1.2;
  margin-bottom: 20px;
  letter-spacing: -0.01em;
  color: #fff;
  text-shadow: 0 2px 40px rgba(14, 165, 233, 0.3);
}

.pl-hero-subtitle {
  font-size: 1.0625rem;
  line-height: 1.7;
  color: rgba(255, 255, 255, 0.9);
  max-width: 460px;
}

.pl-decor-wave {
  position: absolute;
  bottom: 0;
  left: 0;
  width: 100%;
  height: 200px;
  background: linear-gradient(to top, rgba(255, 255, 255, 0.12), transparent);
  clip-path: ellipse(85% 100% at 50% 100%);
}

/* ── Right Panel (Form) ── */
.pl-card-right {
  flex: 0.6;
  padding: 60px 72px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #fff;
}

.pl-card-right-content {
  width: 100%;
  max-width: 400px;
  margin: 0 auto;
}

.pl-form-title {
  font-size: 1.875rem;
  font-weight: 800;
  color: #1a2332;
  margin-bottom: 10px;
  letter-spacing: -0.01em;
}

.pl-form-subtitle {
  font-size: 0.9375rem;
  color: #64748b;
  margin-bottom: 36px;
}

.pl-error-box {
  background: #fff1f2;
  border: 1px solid #fecdd3;
  color: #e11d48;
  padding: 12px;
  border-radius: 12px;
  margin-bottom: 24px;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.875rem;
}

.pl-warning-box {
  background-color: #fffbeb;
  border: 1px solid #fef3c7;
  color: #92400e;
  padding: 12px;
  border-radius: 12px;
  margin-bottom: 24px;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.875rem;
  font-weight: 500;
}

.pl-security-check {
  margin-bottom: 24px;
}

.pl-form-group {
  margin-bottom: 22px;
}

.pl-label {
  display: block;
  font-size: 0.875rem;
  font-weight: 600;
  color: #475569;
  margin-bottom: 8px;
}

.pl-input-wrapper {
  position: relative;
}

.pl-input-icon {
  position: absolute;
  left: 16px;
  top: 50%;
  transform: translateY(-50%);
  color: #94a3b8;
}

.pl-input {
  width: 100%;
  padding: 14px 48px 14px 48px;
  background: #f1f5f9;
  border: 2px solid transparent;
  border-radius: 14px;
  font-size: 0.9375rem;
  transition: all 0.2s;
  color: #1a2332;
}

.pl-input:focus {
  outline: none;
  background: #fff;
  border-color: #0ea5e9;
  box-shadow: 0 0 0 4px rgba(14, 165, 233, 0.1);
}

/* Password Toggle */
.pl-password-toggle {
  position: absolute;
  right: 14px;
  top: 50%;
  transform: translateY(-50%);
  background: none;
  border: none;
  color: #94a3b8;
  cursor: pointer;
  padding: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  transition: all 0.2s;
}

.pl-password-toggle:hover {
  color: #64748b;
  background: rgba(0, 0, 0, 0.04);
}

.pl-form-actions {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 28px;
}

.pl-checkbox {
  display: flex;
  align-items: center;
  gap: 10px;
  cursor: pointer;
  user-select: none;
}

.pl-checkbox input {
  display: none;
}

.pl-checkbox-box {
  width: 20px;
  height: 20px;
  background: #e2e8f0;
  border-radius: 6px;
  position: relative;
  transition: all 0.2s;
}

.pl-checkbox input:checked + .pl-checkbox-box {
  background: #0ea5e9;
}

.pl-checkbox-box::after {
  content: '';
  position: absolute;
  left: 7px;
  top: 3px;
  width: 5px;
  height: 10px;
  border: solid white;
  border-width: 0 2px 2px 0;
  transform: rotate(45deg);
  opacity: 0;
  transition: opacity 0.2s;
}

.pl-checkbox input:checked + .pl-checkbox-box::after {
  opacity: 1;
}

.pl-checkbox-label {
  font-size: 0.875rem;
  color: #64748b;
  font-weight: 500;
}

.pl-forgot-link {
  font-size: 0.875rem;
  font-weight: 700;
  color: #0ea5e9;
  text-decoration: none;
}

.pl-btn-login {
  width: 100%;
  height: 54px;
  padding: 0 24px;
  background: linear-gradient(135deg, #0ea5e9 0%, #06b6d4 100%);
  color: #fff;
  border: none;
  border-radius: 14px;
  font-size: 1rem;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  cursor: pointer;
  transition: all 0.2s;
  box-shadow: 0 8px 24px rgba(14, 165, 233, 0.2);
}

.pl-btn-login:hover {
  background: linear-gradient(135deg, #0284c7 0%, #0891b2 100%);
  transform: translateY(-1px);
  box-shadow: 0 12px 28px rgba(14, 165, 233, 0.3);
}

.pl-btn-login:disabled {
  opacity: 0.7;
  cursor: not-allowed;
  transform: none;
}

.pl-divider-container {
  display: flex;
  align-items: center;
  justify-content: center;
  margin: 28px 0 20px;
  position: relative;
}

.pl-divider {
  width: 100%;
  height: 1px;
  background: #f1f5f9;
}

.pl-divider-text {
  position: absolute;
  background: #fff;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: #94a3b8;
  font-weight: 500;
}

.pl-btn-request {
  width: 100%;
  height: 50px;
  background: #fff;
  color: #1a2332;
  border: 1px solid #e2e8f0;
  border-radius: 14px;
  font-size: 0.9375rem;
  font-weight: 700;
  cursor: pointer;
  transition: all 0.2s;
}

.pl-btn-request:hover {
  background: #f8fafc;
  border-color: #cbd5e1;
}

.pl-spinner {
  width: 20px;
  height: 20px;
  border: 2.5px solid rgba(255, 255, 255, 0.3);
  border-top-color: #fff;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* ── Footer ── */
.pl-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 32px 48px;
  flex-shrink: 0;
}

.pl-copyright {
  font-size: 0.8125rem;
  color: #94a3b8;
  margin: 0;
}

.pl-footer-links {
  display: flex;
  gap: 32px;
}

.pl-footer-links a {
  font-size: 0.8125rem;
  color: #64748b;
  text-decoration: none;
  font-weight: 500;
  transition: color 0.2s;
}

.pl-footer-links a:hover {
  color: #1a2332;
}

/* ── Responsive ── */
@media (max-width: 1024px) {
  .pl-features {
    grid-template-columns: 1fr;
  }
  .pl-hero-title {
    font-size: 2.25rem;
  }
}

@media (max-width: 900px) {
  .pl-card {
    flex-direction: column;
    max-width: 480px;
    margin: 20px auto;
    border-radius: 24px;
    overflow: hidden;
    box-shadow: 0 20px 40px rgba(0, 0, 0, 0.08);
  }
  .pl-card-left {
    padding: 48px 32px;
    min-height: auto;
  }
  .pl-features {
    display: none;
  }
  .pl-hero-title {
    font-size: 1.75rem;
    margin-bottom: 12px;
  }
  .pl-hero-subtitle {
    font-size: 0.9375rem;
    margin-bottom: 0;
  }
  .pl-card-right {
    padding: 40px 32px;
  }
}

@media (max-width: 640px) {
  .pl-page {
    background: #fff;
  }
  .pl-nav {
    padding: 0 24px;
  }
  .pl-main {
    padding: 0;
  }
  .pl-card {
    box-shadow: none;
    border-radius: 0;
    margin: 0;
  }
  .pl-card-left {
    padding: 40px 24px;
  }
  .pl-card-right {
    padding: 32px 24px;
  }
  .pl-footer {
    flex-direction: column;
    gap: 24px;
    padding: 40px 24px;
    text-align: center;
  }
  .pl-footer-links {
    flex-wrap: wrap;
    justify-content: center;
    gap: 16px;
  }
}
</style>
