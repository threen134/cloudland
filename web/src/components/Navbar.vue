<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Cloud, Menu, X, ChevronDown, Phone, Mail, Globe } from 'lucide-vue-next'
import { useAuthStore } from '../stores/auth'
import { setLanguage, getCurrentLanguage } from '../locales'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()

const isMenuOpen = ref(false)
const activeDropdown = ref<string | null>(null)
const currentLang = ref(getCurrentLanguage())

const closeMenu = () => {
    isMenuOpen.value = false
    activeDropdown.value = null
}

const toggleDropdown = (name: string) => {
    activeDropdown.value = activeDropdown.value === name ? null : name
}

const navigateTo = (path: string) => {
    router.push(path)
    closeMenu()
}

const switchLanguage = (lang: 'en' | 'zh') => {
    setLanguage(lang)
    currentLang.value = lang
    activeDropdown.value = null
}

const productLinks = [
    { nameKey: 'products.cloudInstances', descKey: 'products.cloudInstancesDesc', path: '/marketplace' },
    { nameKey: 'products.blockStorage', descKey: 'products.blockStorageDesc', path: '/marketplace' },
    { nameKey: 'products.loadBalancers', descKey: 'products.loadBalancersDesc', path: '/marketplace' },
    { nameKey: 'products.floatingIPs', descKey: 'products.floatingIPsDesc', path: '/marketplace' },
]

const solutionLinks = [
    { nameKey: 'solutions.webHosting', path: '/marketplace' },
    { nameKey: 'solutions.ecommerce', path: '/marketplace' },
    { nameKey: 'solutions.devTesting', path: '/marketplace' },
    { nameKey: 'solutions.enterprise', path: '/marketplace' },
]
</script>

<template>
  <header class="header">
    <!-- Top bar -->
    <div class="top-bar">
      <div class="container">
        <div class="top-bar-content">
          <div class="contact-info">
            <a href="mailto:support@cloudland.com"><Mail :size="14" /> support@cloudland.com</a>
            <a href="tel:+1-800-000-0000"><Phone :size="14" /> +1-800-CLOUD</a>
          </div>
          <div class="top-links">
            <RouterLink to="/docs">{{ t('nav.documentation') }}</RouterLink>
            <RouterLink to="/support">{{ t('nav.support') }}</RouterLink>
          </div>
        </div>
      </div>
    </div>

    <!-- Main navbar -->
    <nav class="navbar">
      <div class="container">
        <div class="navbar-inner">
          <!-- Logo -->
          <RouterLink to="/" class="logo">
            <Cloud :size="28" class="logo-icon" />
            <span class="logo-text">CloudLand</span>
          </RouterLink>

          <!-- Mobile toggle -->
          <button class="menu-toggle" @click="isMenuOpen = !isMenuOpen">
            <component :is="isMenuOpen ? X : Menu" :size="24" />
          </button>

          <!-- Desktop Navigation -->
          <nav class="nav-menu" :class="{ open: isMenuOpen }">
            <!-- Products Dropdown -->
            <div class="nav-item dropdown" @mouseenter="activeDropdown = 'products'" @mouseleave="activeDropdown = null">
              <button class="nav-link" :class="{ active: activeDropdown === 'products' }">
                {{ t('nav.products') }} <ChevronDown :size="14" />
              </button>
              <div class="dropdown-menu" v-show="activeDropdown === 'products'">
                <RouterLink 
                  v-for="link in productLinks" 
                  :key="link.nameKey" 
                  :to="link.path" 
                  class="dropdown-item"
                  @click="closeMenu"
                >
                  <span class="dropdown-item-title">{{ t(link.nameKey) }}</span>
                  <span class="dropdown-item-desc">{{ t(link.descKey) }}</span>
                </RouterLink>
              </div>
            </div>

            <!-- Solutions Dropdown -->
            <div class="nav-item dropdown" @mouseenter="activeDropdown = 'solutions'" @mouseleave="activeDropdown = null">
              <button class="nav-link" :class="{ active: activeDropdown === 'solutions' }">
                {{ t('nav.solutions') }} <ChevronDown :size="14" />
              </button>
              <div class="dropdown-menu dropdown-compact" v-show="activeDropdown === 'solutions'">
                <RouterLink 
                  v-for="link in solutionLinks" 
                  :key="link.nameKey" 
                  :to="link.path" 
                  class="dropdown-item"
                  @click="closeMenu"
                >
                  {{ t(link.nameKey) }}
                </RouterLink>
              </div>
            </div>

            <!-- Regular Links -->
            <RouterLink to="/marketplace" class="nav-link" @click="closeMenu">{{ t('nav.pricing') }}</RouterLink>
            <RouterLink to="/about" class="nav-link" @click="closeMenu">{{ t('nav.about') }}</RouterLink>
          </nav>

          <!-- Auth & Language -->
          <div class="nav-actions">
            <!-- Language Switcher -->
            <div class="nav-item dropdown" @mouseenter="activeDropdown = 'lang'" @mouseleave="activeDropdown = null">
              <button class="nav-link lang-btn">
                <Globe :size="16" />
                {{ currentLang === 'zh' ? '中文' : 'EN' }}
                <ChevronDown :size="14" />
              </button>
              <div class="dropdown-menu dropdown-compact dropdown-right" v-show="activeDropdown === 'lang'">
                <button class="dropdown-item" :class="{ active: currentLang === 'en' }" @click="switchLanguage('en')">
                  English
                </button>
                <button class="dropdown-item" :class="{ active: currentLang === 'zh' }" @click="switchLanguage('zh')">
                  简体中文
                </button>
              </div>
            </div>

            <template v-if="!auth.user">
              <RouterLink to="/login" class="nav-link">{{ t('nav.login') }}</RouterLink>
              <button class="btn btn-primary" @click="navigateTo('/login')">{{ t('nav.dashboard') }}</button>
            </template>
            <template v-else>
              <button class="btn btn-primary" @click="navigateTo('/dashboard')">{{ t('nav.dashboard') }}</button>
            </template>
          </div>
        </div>
      </div>
    </nav>
  </header>
</template>

<style scoped>
.header {
    position: sticky;
    top: 0;
    z-index: var(--z-sticky);
    background: rgba(255, 255, 255, 0.9);
    backdrop-filter: blur(12px);
    -webkit-backdrop-filter: blur(12px);
    box-shadow: var(--shadow-sm);
}

/* Top bar */
.top-bar {
    background: var(--bg-dark);
    color: var(--text-inverse);
    font-size: var(--font-size-sm);
    padding: var(--spacing-2) 0;
}

.top-bar-content {
    display: flex;
    justify-content: space-between;
    align-items: center;
}

.contact-info {
    display: flex;
    gap: var(--spacing-6);
}

.contact-info a,
.top-links a {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    color: var(--gray-300);
    font-size: var(--font-size-xs);
}

.contact-info a:hover,
.top-links a:hover {
    color: var(--text-inverse);
}

.top-links {
    display: flex;
    gap: var(--spacing-5);
}

/* Navbar */
.navbar {
    border-bottom: 1px solid var(--border-light);
}

.navbar-inner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 80px; /* Increased height for better presence */
}

/* Logo */
.logo {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    color: var(--text-primary);
    font-weight: var(--font-weight-bold);
    font-size: var(--font-size-xl);
}

.logo:hover {
    color: var(--text-primary);
}

.logo-icon {
    color: var(--primary-color);
}

/* Nav Menu */
.nav-menu {
    display: flex;
    align-items: center;
    gap: var(--spacing-1);
}

.nav-item {
    position: relative;
}

.nav-link {
    display: flex;
    align-items: center;
    gap: var(--spacing-1);
    padding: var(--spacing-2) var(--spacing-4);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    background: none;
    border: none;
    cursor: pointer;
    border-radius: var(--radius-md);
    transition: all var(--transition-fast);
}

.nav-link:hover,
.nav-link.active {
    color: var(--text-primary);
    background: var(--bg-secondary);
}

.lang-btn {
    gap: var(--spacing-2);
}

/* Dropdown */
.dropdown-menu {
    position: absolute;
    top: 100%;
    left: 0;
    min-width: 280px;
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-xl);
    padding: var(--spacing-2);
    z-index: var(--z-dropdown);
}

.dropdown-compact {
    min-width: 140px;
}

.dropdown-right {
    left: auto;
    right: 0;
}

.dropdown-item {
    display: block;
    width: 100%;
    padding: var(--spacing-3) var(--spacing-4);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    background: none;
    border: none;
    text-align: left;
    cursor: pointer;
    font-size: var(--font-size-sm);
    transition: all var(--transition-fast);
}

.dropdown-item:hover {
    background: var(--bg-secondary);
    color: var(--primary-color);
}

.dropdown-item.active {
    background: var(--primary-light);
    color: var(--primary-color);
}

.dropdown-item-title {
    display: block;
    font-weight: var(--font-weight-medium);
}

.dropdown-item-desc {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 2px;
}

/* Nav Actions */
.nav-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

/* Mobile Toggle */
.menu-toggle {
    display: none;
    background: none;
    border: none;
    cursor: pointer;
    color: var(--text-primary);
}

/* Mobile Responsive */
@media (max-width: 1024px) {
    .top-bar {
        display: none;
    }

    .menu-toggle {
        display: flex;
    }

    .nav-menu {
        position: absolute;
        top: var(--header-height);
        left: 0;
        right: 0;
        flex-direction: column;
        background: var(--bg-primary);
        border-bottom: 1px solid var(--border-light);
        padding: var(--spacing-4);
        display: none;
    }

    .nav-menu.open {
        display: flex;
    }

    .dropdown-menu {
        position: static;
        box-shadow: none;
        border: none;
        padding-left: var(--spacing-4);
    }

    .nav-actions {
        display: none;
    }
}
</style>
