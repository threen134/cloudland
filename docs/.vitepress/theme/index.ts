import { h } from 'vue'
import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme'
import ScalarApiReference from './ScalarApiReference.vue'
import './custom.css'

export default {
  extends: DefaultTheme,
  Layout: () => {
    return h(DefaultTheme.Layout, null, {})
  },
  enhanceApp({ app }) {
    // 全局注册 Scalar API 渲染组件
    app.component('ScalarApiReference', ScalarApiReference)
  }
} satisfies Theme


