import js from '@eslint/js'
import globals from 'globals'
import pluginVue from 'eslint-plugin-vue'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

// Vue 3 + TypeScript 项目的 ESLint 配置
export default defineConfig([
  globalIgnores(['dist']),
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/essential'],
  {
    files: ['**/*.vue'],
    languageOptions: {
      parserOptions: { parser: tseslint.parser },
    },
  },
  {
    files: ['**/*.{ts,vue}'],
    languageOptions: {
      ecmaVersion: 2020,
      sourceType: 'module',
      globals: globals.browser,
    },
    rules: {
      // any 目前还有两百多处，多数需要补真实的接口类型才能去掉，机械替换成 unknown 只会
      // 把问题变成编译错误。降级为警告：lint 保持零错误可以进 CI，剩余数量仍然可见，
      // 按模块补类型时再逐步清掉。
      '@typescript-eslint/no-explicit-any': 'warn',
    },
  },
  {
    // scripts/ 下是构建期跑的 Node 脚本，不是浏览器代码
    files: ['scripts/**/*.mjs'],
    languageOptions: {
      globals: globals.node,
    },
  },
  {
    // 路由页面和布局组件的名字由文件名决定（Login、Overview、Layout…），
    // 改成多词名没有意义，这条规则只对可复用组件有价值。
    files: ['src/views/**/*.vue', 'src/App.vue', 'src/components/Navbar.vue'],
    rules: {
      'vue/multi-word-component-names': 'off',
    },
  },
])
