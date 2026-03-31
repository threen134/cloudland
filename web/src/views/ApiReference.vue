<script setup lang="ts">
import { ApiReference } from '@scalar/api-reference'
import { useRouter } from 'vue-router'
import { Cloud } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const router = useRouter()
const { t } = useI18n()

const config = {
  url: '/swagger.json',
  theme: 'default' as const,
  showSidebar: true,
  layout: 'modern' as const,
  hideDownloadButton: false,
  metaData: {
    title: 'CloudLand API Reference',
  },
  customCss: [
    ':root { --scalar-font: "Inter", -apple-system, BlinkMacSystemFont, sans-serif; --scalar-color-1: #1a2332; --scalar-color-2: #475569; --scalar-color-3: #64748b; --scalar-color-accent: #0ea5e9; --scalar-background-1: #ffffff; --scalar-background-2: #f8fafc; --scalar-background-3: #f0f9ff; --scalar-border-color: #e2e8f0; --scalar-radius: 12px; }',
    '.sidebar { background: linear-gradient(180deg, #e8f4fd 0%, #ffffff 45%) !important; border-right: 1px solid #e2e8f0 !important; }',
    'a { color: #0ea5e9; }',
  ].join(' '),
}
</script>

<template>
  <div class="pl-page">
    <!-- Nav — identical to Login page -->
    <header class="pl-nav">
      <router-link to="/" class="pl-nav-brand">
        <div class="pl-logo-wrapper">
          <Cloud :size="20" fill="currentColor" />
        </div>
        <span>CloudLand</span>
      </router-link>
      <div class="pl-nav-actions">
        <span class="pl-api-badge">API Reference</span>
        <button class="pl-back-btn" @click="router.push('/dashboard')">
          ← {{ t('nav.dashboard') }}
        </button>
      </div>
    </header>

    <div class="pl-scalar-wrap">
      <ApiReference :configuration="config" />
    </div>
  </div>
</template>

<style scoped>
.pl-page {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  color: #1a2332;
}

/* ── Navbar — identical to Login page ── */
.pl-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 48px;
  height: 64px;
  background: rgba(255, 255, 255, 0.8);
  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  border-bottom: 1px solid rgba(226, 232, 240, 0.6);
  position: sticky;
  top: 0;
  z-index: 9999;
  flex-shrink: 0;
}

.pl-nav-brand {
  display: flex;
  align-items: center;
  gap: 12px;
  text-decoration: none;
  font-weight: 700;
  font-size: 1.1875rem;
  color: #1a2332;
  letter-spacing: -0.01em;
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
  flex-shrink: 0;
}

.pl-nav-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.pl-api-badge {
  display: inline-flex;
  align-items: center;
  padding: 5px 12px;
  background: #f0f9ff;
  border: 1px solid #bae6fd;
  border-radius: 100px;
  font-size: 0.8125rem;
  font-weight: 600;
  color: #0284c7;
}

.pl-back-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 16px;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.7);
  color: #475569;
  font-size: 0.8125rem;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.2s;
}

.pl-back-btn:hover {
  background: #fff;
  border-color: #cbd5e1;
  color: #0ea5e9;
}

.pl-scalar-wrap {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.pl-scalar-wrap :deep(.scalar-app),
.pl-scalar-wrap :deep(.references-layout) {
  flex: 1;
}
</style>
