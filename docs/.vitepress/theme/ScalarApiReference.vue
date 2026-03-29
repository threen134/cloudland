<template>
  <ClientOnly>
    <div class="scalar-container" :class="{ 'dark-mode': isDark }">
      <!-- 动态 Server 配置工具栏：允许客户手动配置调谐的目标 API 地址 -->
      <div class="server-config-panel">
        <div class="config-inner">
          <label class="config-label">
            <span class="icon">⚡</span> Target API Server:
          </label>
          <div class="input-wrapper">
            <input 
              v-model="baseServerURL" 
              type="text" 
              class="server-input" 
              placeholder="e.g. http://your-cloud-ip:5173/api/v1"
            />
          </div>
          <div class="status-badge" @click="baseServerURL = defaultServer">
            Reset to Default
          </div>
        </div>
      </div>
      
      <ApiReference :configuration="config" :key="isDark + baseServerURL" />
    </div>
  </ClientOnly>
</template>


<script setup>
import { computed, ref } from 'vue'
import { ApiReference } from '@scalar/api-reference'
import '@scalar/api-reference/style.css'
import { useData } from 'vitepress'

const { isDark } = useData()

// 定义默认地址，方便重置
const defaultServer = 'http://localhost:5173/api/v1'
// 定义客户可手动修改的响应式变量
const baseServerURL = ref(defaultServer)

// 使用 computed 确保配置对象在 isDark 或 baseServerURL 变化时动态反映
const config = computed(() => ({
  url: '/docs/api-v1.json',
  baseServerURL: baseServerURL.value,
  theme: 'bluePlanet',
  showSidebar: true,
  layout: 'modern',
  darkMode: isDark.value,
  forceDarkModeState: isDark.value ? 'dark' : 'light',
  hideDarkModeToggle: true,
}))
</script>

<style>
/* --- Server 配置面板样式 (Glacial Console) --- */
.server-config-panel {
  padding: 12px 24px;
  background: #e0f2fe !important; /* 实色冰川蓝 */
  border-bottom: 2px solid #bae6fd;
  position: sticky;
  top: var(--vp-nav-height); /* 紧贴导航栏下方 */
  z-index: 50;
  display: flex;
  justify-content: center;
  backdrop-filter: none !important;
}

.dark-mode .server-config-panel {
  background: #1e293b !important;
  border-bottom-color: #0ea5e9;
}

.config-inner {
  display: flex;
  align-items: center;
  gap: 16px;
  max-width: 1200px;
  width: 100%;
}

.config-label {
  font-size: 13px;
  font-weight: 700;
  color: var(--pl-primary);
  white-space: nowrap;
  display: flex;
  align-items: center;
  gap: 6px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.input-wrapper {
  flex-grow: 1;
}

.server-input {
  width: 100%;
  padding: 8px 16px;
  background: #ffffff !important; /* 实屏输入框 */
  border: 1px solid rgba(14, 165, 233, 0.3);
  border-radius: 8px;
  font-family: var(--vp-font-family-mono);
  font-size: 13px;
  color: var(--vp-c-text-1);
  transition: all 0.3s ease;
}

.dark-mode .server-input {
  background: #0f172a !important;
  border-color: rgba(255, 255, 255, 0.1);
}

.server-input:focus {
  outline: none;
  border-color: var(--pl-primary);
  background: #fff;
  box-shadow: 0 0 0 4px rgba(14, 165, 233, 0.1);
}

.status-badge {
  font-size: 11px;
  padding: 4px 10px;
  background: #bae6fd; /* 更明显的浅蓝色 */
  color: #0369a1;
  border: 1px solid #7dd3fc;
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
  font-weight: 600;
}

.status-badge:hover {
  background: var(--pl-primary);
  color: white;
}

/* --- 核心注入：让 Scalar 内部主题对齐全站，彻底杜绝纯白和透明穿透 --- */
.scalar-app {
  --scalar-font: var(--vp-font-family) !important;
  --scalar-color-accent: var(--pl-primary) !important;
  
  /* 基础背景变量 - 确认为实色 */
  --scalar-background-1: #f0f9ff !important; /* 冰川蓝背景 - 取代纯白 */
  --scalar-background-2: #e0f2fe !important; /* 侧边栏/输入框背景 */
  --scalar-background-3: #ffffff !important; /* 内容卡片/主内容背景 - 核心阅读区使用实白以保清晰 */
  
  background: var(--scalar-background-1) !important;
}

.dark-mode .scalar-app {
  --scalar-background-1: #0f172a !important; /* 深蓝背景 */
  --scalar-background-2: #1e293b !important;
  --scalar-background-3: #111827 !important;
}

/* 顶部/主体背景：实色背景以确保阅读清晰，且与 custom.css 的 body 渐变底部对齐 */
.scalar-container:not(.dark-mode) {
  background-color: #f0f9ff !important; 
}

.scalar-container.dark-mode {
  background-color: #0f172a !important; 
}

/* 侧边栏样式：改为实色蓝，形成层次感 */
.scalar-sidebar {
  background: #e0f2fe !important; /* 实色冰川蓝 */
  border-right: 1px solid rgba(14, 165, 233, 0.15) !important;
  box-shadow: 2px 0 10px rgba(0, 0, 0, 0.02);
}

.dark-mode .scalar-sidebar {
  background: #1e293b !important; /* 深蓝实色 */
  border-right: 1px solid rgba(255, 255, 255, 0.05) !important;
}


/* 时效性操作区域（Operation）：背景透明，利用侧边栏或主背景颜色 */
.scalar-operation {
  background: #ffffff !important; /* 核心内容区使用实白，杜绝穿透 */
}

.dark-mode .scalar-operation {
  background: #0f172a !important;
}

.scalar-api-reference {
  background: #ffffff !important; 
}

.dark-mode .scalar-api-reference {
  background: #0f172a !important;
}

/* 代码卡片和辅助面板：给予微弱的品牌影子，主背景维持实色 */
.scalar-card {
  background: #ffffff !important; /* 实白卡片 */
  border-radius: 12px !important;
  border: 1px solid #e0f2fe !important;
  box-shadow: 0 4px 15px rgba(0, 0, 0, 0.03) !important;
}

.dark-mode .scalar-card {
  background: #1e293b !important;
  border: 1px solid rgba(255, 255, 255, 0.05) !important;
}

/* 移除可能产生透明感的磨砂滤镜 */
.scalar-sidebar,
.scalar-card,
.server-config-panel {
  backdrop-filter: none !important;
  -webkit-backdrop-filter: none !important;
}

/* 页脚和辅助栏背景色压深，增强层级 */
.scalar-card-footer,
.scalar-client-code-select {
  background: #f8fafc !important;
  border-top: 1px solid #f1f5f9 !important;
}

.dark-mode .scalar-card-footer,
.dark-mode .scalar-client-code-select {
  background: #111827 !important;
  border-top-color: rgba(255, 255, 255, 0.05) !important;
}

/* Server 配置面板样式 (统一 Glacier 风格) */
.server-config-panel {
  background: #e0f2fe !important; /* 明显的顶栏颜色 */
  border-bottom: 2px solid #bae6fd;
}

.dark-mode .server-config-panel {
  background: #1e293b !important;
  border-bottom-color: #0ea5e9;
}

/* 移除背景图片干扰 */
.scalar-app::before,
.scalar-app::after {
  content: none !important;
}

/* 覆盖 VitePress 的文档容器限制 — 仅限 VitePress 自身容器，不影响 Scalar 内部 */
.VPDoc,
.VPDoc > .container,
.VPDoc > .container > .content,
.vp-doc {
  max-width: 100% !important;
  margin: 0 !important;
  padding-left: 0 !important;
  padding-right: 0 !important;
  padding-bottom: 0 !important;
}

/* 通过 Scalar 官方 CSS 变量告知外部头部高度，让内部 sidebar/viewport 计算自动正确 */
.scalar-api-reference {
  --scalar-custom-header-height: calc(var(--vp-nav-height, 64px) + 56px) !important;
}

/* 隐藏右侧的页面大纲 (Outline) */
.aside {
  display: none !important;
}
</style>


