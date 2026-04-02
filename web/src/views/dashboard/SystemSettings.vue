<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Save, Send, RefreshCw, ChevronDown, ChevronRight } from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { systemSettingsApi, type SystemSetting } from '../../api/systemSettings'

const { t } = useI18n()
const toast = useToast()

const loading = ref(false)
const saving = ref(false)
const settings = ref<SystemSetting[]>([])
// 编辑中的值（key -> 当前输入值）
const editValues = ref<Record<string, any>>({})
const testingChannel = ref<string | null>(null)
const expandedCategories = ref<string[]>(['general', 'quota', 'notification'])

// 分类标签从 i18n 动态解析，未知分类回退到 key 本身（支持未来扩展）
const getCategoryLabel = (category: string): string => {
    const key = `settings.category.${category}`
    const translated = t(key)
    // vue-i18n 在 key 不存在时会原样返回 key 字符串
    return translated === key ? category : translated
}

const groupedSettings = computed(() => {
    const groups: Record<string, SystemSetting[]> = {}
    for (const s of settings.value) {
        if (!groups[s.category]) groups[s.category] = []
        groups[s.category].push(s)
    }
    return groups
})

const fetchSettings = async () => {
    loading.value = true
    try {
        const res = await systemSettingsApi.list()
        settings.value = res.data.settings || []
        // 初始化编辑值（secret 字段保持脱敏占位，不填入真实值）
        for (const s of settings.value) {
            if (s.value_type === 'json') {
                editValues.value[s.key] = typeof s.value === 'object'
                    ? JSON.stringify(s.value, null, 2)
                    : (s.value ?? '')
            } else {
                editValues.value[s.key] = s.value ?? ''
            }
        }
    } catch (err) {
        console.error('Failed to fetch settings:', err)
        toast.error(t('messages.error'))
    } finally {
        loading.value = false
    }
}

const saveSettings = async () => {
    saving.value = true
    try {
        // 构造 payload，JSON 字段需解析回对象
        const payload: Record<string, any> = {}
        for (const s of settings.value) {
            const raw = editValues.value[s.key]
            if (s.value_type === 'json') {
                try {
                    payload[s.key] = typeof raw === 'string' ? JSON.parse(raw) : raw
                } catch {
                    toast.error(t('settings.jsonParseError', { key: s.key }))
                    saving.value = false
                    return
                }
            } else if (s.value_type === 'number') {
                payload[s.key] = Number(raw)
            } else if (s.value_type === 'boolean') {
                payload[s.key] = Boolean(raw)
            } else {
                payload[s.key] = raw
            }
        }
        const res = await systemSettingsApi.update(payload)
        settings.value = res.data.settings || []
        // 刷新编辑值（secret 字段 API 返回脱敏值，保持回写）
        for (const s of settings.value) {
            if (s.value_type === 'json') {
                editValues.value[s.key] = typeof s.value === 'object'
                    ? JSON.stringify(s.value, null, 2)
                    : (s.value ?? '')
            } else {
                editValues.value[s.key] = s.value ?? ''
            }
        }
        toast.success(t('settings.saveSuccess'))
    } catch (err) {
        console.error('Failed to save settings:', err)
        toast.error(t('messages.error'))
    } finally {
        saving.value = false
    }
}

const testChannel = async (channel: string) => {
    testingChannel.value = channel
    try {
        const res = await systemSettingsApi.testNotification(channel)
        if (res.data.success) {
            toast.success(t('settings.testSuccess', { channel }))
        } else {
            toast.error(t('settings.testFailed', { message: res.data.message }))
        }
    } catch (err) {
        toast.error(t('messages.error'))
    } finally {
        testingChannel.value = null
    }
}

const toggleCategory = (cat: string) => {
    const idx = expandedCategories.value.indexOf(cat)
    if (idx === -1) expandedCategories.value.push(cat)
    else expandedCategories.value.splice(idx, 1)
}

const isExpanded = (cat: string) => expandedCategories.value.includes(cat)

// 通知渠道：从 NOTIFICATION_CHANNELS 设置中读取启用列表
const enabledChannels = computed<string[]>(() => {
    const raw = editValues.value['NOTIFICATION_CHANNELS']
    if (!raw) return []
    try {
        const parsed = typeof raw === 'string' ? JSON.parse(raw) : raw
        return Array.isArray(parsed) ? parsed : []
    } catch {
        return []
    }
})

const toggleChannel = (channel: string) => {
    const current = [...enabledChannels.value]
    const idx = current.indexOf(channel)
    if (idx === -1) current.push(channel)
    else current.splice(idx, 1)
    editValues.value['NOTIFICATION_CHANNELS'] = JSON.stringify(current)
}

onMounted(fetchSettings)
</script>

<template>
  <div class="system-settings">
    <div class="page-header">
      <h2>{{ $t('settings.title') }}</h2>
      <div class="header-actions">
        <button class="btn btn-secondary" @click="fetchSettings" :disabled="loading">
          <RefreshCw :size="16" :class="{ spin: loading }" />
          {{ $t('common.refresh') }}
        </button>
        <button class="btn btn-primary" @click="saveSettings" :disabled="saving || loading">
          <Save :size="16" />
          {{ saving ? $t('common.saving') : $t('common.save') }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="loading-state">
      <RefreshCw :size="24" class="spin" />
    </div>

    <div v-else class="settings-body">
      <div
        v-for="(items, category) in groupedSettings"
        :key="category"
        class="settings-section"
      >
        <!-- Section header -->
        <button class="section-header" @click="toggleCategory(String(category))">
          <span class="section-title">{{ getCategoryLabel(String(category)) }}</span>
          <component :is="isExpanded(String(category)) ? ChevronDown : ChevronRight" :size="16" />
        </button>

        <div v-show="isExpanded(String(category))" class="section-body">
          <!-- Notification channels toggle (only in notification section) -->
          <div v-if="category === 'notification'" class="channel-toggles">
            <span class="field-label">{{ $t('settings.enabledChannels') }}</span>
            <div class="toggle-group">
              <label v-for="ch in ['email', 'feishu', 'slack', 'webhook']" :key="ch" class="toggle-label">
                <input
                  type="checkbox"
                  :checked="enabledChannels.includes(ch)"
                  @change="toggleChannel(ch)"
                />
                {{ $t(`settings.channel.${ch}`) }}
              </label>
            </div>
          </div>

          <!-- NOTIFICATION_CHANNELS 由顶部 checkbox 管理，此处跳过原始渲染 -->
          <div
            v-for="setting in items.filter(s => s.key !== 'NOTIFICATION_CHANNELS')"
            :key="setting.key"
            class="field-row"
          >
            <div class="field-meta">
              <label class="field-label">{{ setting.key }}</label>
              <span v-if="setting.description" class="field-desc">{{ setting.description }}</span>
            </div>

            <!-- boolean: toggle -->
            <div v-if="setting.value_type === 'boolean'" class="field-input">
              <label class="switch">
                <input
                  type="checkbox"
                  :checked="!!editValues[setting.key]"
                  @change="editValues[setting.key] = ($event.target as HTMLInputElement).checked"
                />
                <span class="slider"></span>
              </label>
            </div>

            <!-- number: number input -->
            <div v-else-if="setting.value_type === 'number'" class="field-input">
              <input
                type="number"
                class="input"
                v-model.number="editValues[setting.key]"
              />
            </div>

            <!-- secret: password input -->
            <div v-else-if="setting.value_type === 'secret'" class="field-input">
              <input
                type="password"
                class="input"
                v-model="editValues[setting.key]"
                :placeholder="$t('settings.secretPlaceholder')"
                autocomplete="new-password"
              />
            </div>

            <!-- json: textarea -->
            <div v-else-if="setting.value_type === 'json'" class="field-input">
              <textarea
                class="input textarea"
                v-model="editValues[setting.key]"
                rows="3"
                spellcheck="false"
              />
            </div>

            <!-- string: text input -->
            <div v-else class="field-input">
              <input
                type="text"
                class="input"
                v-model="editValues[setting.key]"
              />
            </div>
          </div>

          <!-- Test buttons for notification section -->
          <div v-if="category === 'notification'" class="test-buttons">
            <span class="field-label">{{ $t('settings.testConnectivity') }}</span>
            <div class="btn-group">
              <button
                v-for="ch in ['email', 'feishu', 'slack', 'webhook']"
                :key="ch"
                class="btn btn-sm btn-secondary"
                :disabled="testingChannel !== null"
                @click="testChannel(ch)"
              >
                <Send :size="14" :class="{ spin: testingChannel === ch }" />
                {{ $t(`settings.channel.${ch}`) }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.system-settings {
  max-width: 860px;
}

.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 24px;
}

.page-header h2 {
  font-size: 1.25rem;
  font-weight: 600;
  margin: 0;
}

.header-actions {
  display: flex;
  gap: 10px;
}

.loading-state {
  display: flex;
  justify-content: center;
  padding: 60px 0;
  color: var(--text-tertiary);
}

.settings-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.settings-section {
  border: 1px solid var(--border-color);
  border-radius: 8px;
  overflow: hidden;
}

.section-header {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 18px;
  background: var(--bg-secondary);
  border: none;
  cursor: pointer;
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--text-primary);
}

.section-header:hover {
  background: var(--bg-hover);
}

.section-body {
  padding: 0 18px 18px;
}

.channel-toggles,
.test-buttons {
  padding: 14px 0 10px;
  border-bottom: 1px solid var(--border-color);
  margin-bottom: 12px;
}

.toggle-group {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 8px;
}

.toggle-label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.875rem;
  cursor: pointer;
}

.btn-group {
  display: flex;
  gap: 8px;
  margin-top: 8px;
  flex-wrap: wrap;
}

.field-row {
  display: grid;
  grid-template-columns: 280px 1fr;
  gap: 16px;
  align-items: start;
  padding: 12px 0;
  border-bottom: 1px solid var(--border-color);
}

.field-row:last-child {
  border-bottom: none;
}


.field-meta {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.field-label {
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--text-primary);
  font-family: 'Monaco', 'Consolas', monospace;
}

.field-desc {
  font-size: 0.75rem;
  color: var(--text-tertiary);
}

.field-input {
  display: flex;
  align-items: center;
}

.input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--bg-primary);
  color: var(--text-primary);
  font-size: 0.875rem;
  transition: border-color 0.2s;
}

.input:focus {
  outline: none;
  border-color: var(--primary-color);
}

.textarea {
  resize: vertical;
  font-family: 'Monaco', 'Consolas', monospace;
  font-size: 0.8rem;
  line-height: 1.5;
}

/* Toggle switch */
.switch {
  position: relative;
  display: inline-block;
  width: 40px;
  height: 22px;
  cursor: pointer;
}

.switch input {
  opacity: 0;
  width: 0;
  height: 0;
}

.slider {
  position: absolute;
  inset: 0;
  background: var(--border-color);
  border-radius: 22px;
  transition: background 0.2s;
}

.slider::before {
  content: '';
  position: absolute;
  width: 16px;
  height: 16px;
  left: 3px;
  top: 3px;
  background: white;
  border-radius: 50%;
  transition: transform 0.2s;
}

.switch input:checked + .slider {
  background: var(--primary-color);
}

.switch input:checked + .slider::before {
  transform: translateX(18px);
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  border-radius: 6px;
  font-size: 0.875rem;
  font-weight: 500;
  cursor: pointer;
  border: none;
  transition: all 0.2s;
}

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--primary-color);
  color: white;
}

.btn-primary:hover:not(:disabled) {
  background: var(--primary-hover);
}

.btn-secondary {
  background: var(--bg-secondary);
  color: var(--text-primary);
  border: 1px solid var(--border-color);
}

.btn-secondary:hover:not(:disabled) {
  background: var(--bg-hover);
}

.btn-sm {
  padding: 5px 10px;
  font-size: 0.8rem;
}

.spin {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
