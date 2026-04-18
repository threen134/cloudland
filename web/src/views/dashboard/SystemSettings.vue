<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
    Save, Send, RefreshCw, ChevronRight,
    Settings2, Lightbulb, Bell, Layers, Shield,
    Globe, Mail, MessageSquare, Webhook,
    Server, Database, HardDrive, CheckCircle2, XCircle, Info
} from 'lucide-vue-next'
import { useToast } from '../../composables/useToast'
import { systemSettingsApi, type SystemSetting } from '../../api/systemSettings'
import { infrastructureApi, type InfrastructureConfig, type TestS3Response } from '../../api/infrastructure'

const { t, te } = useI18n()
const toast = useToast()

const loading = ref(false)
const saving = ref(false)
const settings = ref<SystemSetting[]>([])
const editValues = ref<Record<string, any>>({})
const testingChannel = ref<string | null>(null)

// Helper to get translated field labels and descriptions
const getFieldLabel = (key: string): string => {
    const translationKey = `settings.fields.${key}`
    return te(translationKey) ? t(translationKey) : key
}

const getFieldDesc = (setting: SystemSetting): string => {
    const descKey = `settings.fields.${setting.key}_desc`
    return te(descKey) ? t(descKey) : (setting.description || '')
}

// Categories configuration
const categories = ['general', 'quota', 'notification', 'infrastructure']
const activeMainTab = ref('general')
const activeChannelTab = ref('email')

const categoryIconsMap: Record<string, any> = {
    general: Settings2,
    quota: Layers,
    notification: Bell,
    infrastructure: Server,
}

const getCategoryIcon = (category: string) => {
    return categoryIconsMap[category.toLowerCase()] || Lightbulb
}

const getCategoryLabel = (category: string): string => {
    const cat = category.toLowerCase()
    const key = `settings.category.${cat}`
    const translated = t(key)
    return translated === key ? (category.charAt(0).toUpperCase() + category.slice(1)) : translated
}

const groupedSettings = computed(() => {
    const groups: Record<string, SystemSetting[]> = {}
    for (const s of settings.value) {
        const cat = s.category.toLowerCase()
        if (!groups[cat]) groups[cat] = []
        groups[cat].push(s)
    }
    return groups
})

const currentSettings = computed(() => {
    return groupedSettings.value[activeMainTab.value] || []
})

const getSettingsByChannel = (channel: string) => {
    const items = groupedSettings.value['notification'] || []
    if (channel === 'email') return items.filter(s => s.key.startsWith('SMTP_'))
    if (channel === 'feishu') return items.filter(s => s.key.startsWith('FEISHU_'))
    if (channel === 'slack') return items.filter(s => s.key.startsWith('SLACK_'))
    if (channel === 'webhook') return items.filter(s => s.key.startsWith('CUSTOM_WEBHOOK_'))
    return []
}

const fetchSettings = async () => {
    loading.value = true
    try {
        const res = await systemSettingsApi.list()
        settings.value = res.data.settings || []
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
        toast.error(t('messages.error'))
    } finally {
        loading.value = false
    }
}

const saveSettings = async () => {
    saving.value = true
    try {
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
        await systemSettingsApi.update(payload)
        await fetchSettings() // Fetch latest data FIRST to ensure UI is perfectly synced
        toast.success(t('settings.saveSuccess'))
    } catch (err) {
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

// --- Infrastructure tab state ---
const infraConfig = ref<InfrastructureConfig | null>(null)
const infraLoading = ref(false)
const infraError = ref<string>('')
const testingS3 = ref(false)
const s3TestResult = ref<TestS3Response | null>(null)

const fetchInfrastructure = async () => {
    infraLoading.value = true
    infraError.value = ''
    s3TestResult.value = null
    try {
        const res = await infrastructureApi.get()
        infraConfig.value = res.data
    } catch (err: any) {
        infraError.value = err?.response?.data?.detail || err?.message || 'Failed to load'
        infraConfig.value = null
    } finally {
        infraLoading.value = false
    }
}

const testS3Connection = async () => {
    testingS3.value = true
    s3TestResult.value = null
    try {
        const res = await infrastructureApi.testS3()
        s3TestResult.value = res.data
        if (res.data.success) {
            toast.success(t('settings.infra.testS3Success'))
        } else {
            toast.error(t('settings.infra.testS3Failed', { message: res.data.message }))
        }
    } catch (err: any) {
        const msg = err?.response?.data?.detail || err?.message || 'Unknown error'
        s3TestResult.value = { success: false, message: msg }
        toast.error(t('settings.infra.testS3Failed', { message: msg }))
    } finally {
        testingS3.value = false
    }
}

// infrastructure 字段分组（只读展示）
const infraGroups = computed(() => {
    if (!infraConfig.value) return []
    const c = infraConfig.value
    return [
        {
            icon: Database,
            title: t('settings.infra.groupS3'),
            fields: [
                { label: 'S3_ENDPOINT', value: c.s3_endpoint || '—' },
                { label: 'S3_ACCESS_KEY', value: c.s3_access_key || '—' },
                { label: 'S3_SECRET_KEY', value: c.s3_secret_key_set ? c.s3_secret_key : t('settings.infra.unset'), secret: true },
                { label: 'S3_BUCKET', value: c.s3_bucket || '—' },
                { label: 'S3_REGION', value: c.s3_region || '—' },
                { label: 'S3_USE_SSL', value: String(c.s3_use_ssl) },
                { label: 'S3_UPLOAD_TIMEOUT_MINUTES', value: String(c.s3_upload_timeout_minutes || '—') },
            ],
        },
        {
            icon: HardDrive,
            title: t('settings.infra.groupMinio'),
            fields: [
                { label: 'MINIO_HOSTNAME', value: c.minio_hostname || '—' },
            ],
        },
        {
            icon: Server,
            title: t('settings.infra.groupCapture'),
            fields: [
                { label: 'CLAPI_HOSTNAME', value: c.clapi_hostname || '—' },
                { label: 'CLAPI_INTERNAL_URL', value: c.clapi_internal_url || t('settings.infra.autoDerived') },
                { label: 'SCI_SHARED_SECRET', value: c.sci_shared_secret_set ? c.sci_shared_secret : t('settings.infra.unset'), secret: true },
            ],
        },
    ]
})

watch(activeMainTab, (tab) => {
    if (tab === 'infrastructure' && !infraConfig.value && !infraLoading.value) {
        fetchInfrastructure()
    }
})

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

const isScrolled = ref(false)
const scrollContainerRef = ref<HTMLElement | null>(null)
const handleScroll = (e: Event) => {
    const target = e.target as HTMLElement
    isScrolled.value = target.scrollTop > 20
}

onMounted(() => {
    fetchSettings()
    const container = document.querySelector('.page-content') as HTMLElement | null
    if (container) {
        scrollContainerRef.value = container
        container.addEventListener('scroll', handleScroll)
    }
})

onUnmounted(() => {
    scrollContainerRef.value?.removeEventListener('scroll', handleScroll)
})
</script>

<template>
  <div class="settings-premium-view">
    <!-- Combined Header & Tabs Card -->
    <div class="page-header-card card card-bordered" :class="{ 'is-scrolled': isScrolled }">
        <div class="header-top">
            <div class="header-info">
                <div class="breadcrumb-nav">
                    <span>{{ $t('nav.dashboard') }}</span>
                    <ChevronRight :size="12" />
                    <span class="active">{{ $t('settings.title') }}</span>
                </div>
                <h2>{{ $t('settings.title') }}</h2>
            </div>
            <div class="header-apps">
                <button type="button" class="btn btn-secondary btn-icon-only" @click.stop.prevent="activeMainTab === 'infrastructure' ? fetchInfrastructure() : fetchSettings()" :disabled="loading || infraLoading" :title="$t('common.refresh')" :aria-label="$t('common.refresh')">
                    <RefreshCw :size="18" :class="{ spin: loading || infraLoading }" />
                </button>
                <button v-if="activeMainTab !== 'infrastructure'" type="button" class="btn btn-primary btn-save-main" @click.stop.prevent="saveSettings" :disabled="saving || loading">
                    <Save :size="18" />
                    <span>{{ saving ? $t('common.saving') : $t('common.save') }}</span>
                </button>
            </div>
        </div>

        <div class="header-tabs">
            <button 
                v-for="cat in categories" 
                :key="cat" 
                @click="activeMainTab = cat"
                class="main-tab-item"
                :class="{ active: activeMainTab === cat }"
            >
                <div class="icon-box">
                    <component :is="getCategoryIcon(cat)" :size="18" />
                </div>
                <span class="label">{{ getCategoryLabel(cat) }}</span>
                <div class="tab-indicator"></div>
            </button>
        </div>
    </div>

    <!-- Workspace Area -->
    <div class="workspace-area">
        <transition name="view-fade" mode="out-in">
            <div :key="activeMainTab" class="view-panel" :class="{ 'card card-bordered': activeMainTab !== 'notification' }">
                <!-- Notification Layout (Unified Split-Card) -->
                <div v-if="activeMainTab === 'notification'" class="unified-split-card card card-bordered">
                    <aside class="sidebar-nav">
                        <div class="nav-section-title">{{ $t('settings.notificationChannels') }}</div>
                        <div class="nav-list">
                            <button 
                                v-for="ch in ['email', 'feishu', 'slack', 'webhook']" 
                                :key="ch"
                                class="side-item"
                                :class="{ active: activeChannelTab === ch, enabled: enabledChannels.includes(ch) }"
                                @click="activeChannelTab = ch"
                            >
                                <div class="side-icon-wrap">
                                    <Mail v-if="ch === 'email'" :size="18" />
                                    <MessageSquare v-else-if="ch === 'feishu'" :size="18" />
                                    <Globe v-else-if="ch === 'slack'" :size="18" />
                                    <Webhook v-else-if="ch === 'webhook'" :size="18" />
                                </div>
                                <div class="side-text">
                                    <span class="name">{{ $t(`settings.channel.${ch}`) }}</span>
                                    <span class="status">{{ enabledChannels.includes(ch) ? $t('common.on') : $t('common.off') }}</span>
                                </div>
                                <div class="pill-active"></div>
                            </button>
                        </div>
                    </aside>

                    <div class="pane-content">
                        <div class="activation-hero">
                            <div class="hero-desc">
                                <h3>{{ $t('settings.channelActivation') }}</h3>
                                <p>{{ $t('settings.enableHint') }}</p>
                            </div>
                            <div class="hero-action">
                                <label class="custom-toggle">
                                    <input
                                        type="checkbox"
                                        :checked="enabledChannels.includes(activeChannelTab)"
                                        @change="toggleChannel(activeChannelTab)"
                                    />
                                    <span class="toggle-slider"></span>
                                </label>
                            </div>
                        </div>

                        <div class="settings-form-body" :class="{ disabled: !enabledChannels.includes(activeChannelTab) }">
                            <div v-for="setting in getSettingsByChannel(activeChannelTab)" :key="setting.key" class="form-row">
                                <div class="form-info">
                                    <label class="item-label" :for="setting.key">{{ getFieldLabel(setting.key) }}</label>
                                    <p class="item-desc">{{ getFieldDesc(setting) }}</p>
                                </div>
                                <div class="form-control">
                                    <div v-if="setting.value_type === 'boolean'" class="bool-wrap">
                                        <label class="custom-toggle sm">
                                            <input :id="setting.key" :name="setting.key" type="checkbox" :disabled="!enabledChannels.includes(activeChannelTab)" :checked="!!editValues[setting.key]" @change="editValues[setting.key] = ($event.target as HTMLInputElement).checked" />
                                            <span class="toggle-slider"></span>
                                        </label>
                                        <span class="bool-status-text">{{ !!editValues[setting.key] ? $t('common.on') : $t('common.off') }}</span>
                                    </div>
                                    <div v-else-if="setting.value_type === 'number'" class="input-wrap">
                                        <input :id="setting.key" :name="setting.key" type="number" class="form-input" :disabled="!enabledChannels.includes(activeChannelTab)" v-model.number="editValues[setting.key]" />
                                    </div>
                                    <div v-else-if="setting.value_type === 'secret'" class="input-wrap">
                                        <input :id="setting.key" :name="setting.key" type="password" class="form-input" :disabled="!enabledChannels.includes(activeChannelTab)" v-model="editValues[setting.key]" :placeholder="$t('settings.secretPlaceholder')" autocomplete="new-password" />
                                        <Shield class="icon-inner" :size="16" />
                                    </div>
                                    <div v-else-if="setting.value_type === 'json'" class="input-wrap">
                                        <textarea :id="setting.key" :name="setting.key" class="form-input code-area" :disabled="!enabledChannels.includes(activeChannelTab)" v-model="editValues[setting.key]" rows="4" spellcheck="false" />
                                        <div class="type-tag">JSON</div>
                                    </div>
                                    <div v-else class="input-wrap">
                                        <input :id="setting.key" :name="setting.key" type="text" class="form-input" :disabled="!enabledChannels.includes(activeChannelTab)" v-model="editValues[setting.key]" />
                                    </div>
                                </div>
                            </div>
                        </div>

                        <!-- Panel Footer (Only Connectivity Test) -->
                        <div class="pane-footer center-align">
                            <button
                                class="btn btn-secondary btn-sm connectivity-btn"
                                :disabled="testingChannel !== null || !enabledChannels.includes(activeChannelTab)"
                                @click="testChannel(activeChannelTab)"
                            >
                                <RefreshCw v-if="testingChannel === activeChannelTab" :size="14" class="spin" />
                                <Send v-else :size="14" />
                                {{ $t('settings.testConnectivity') }}
                            </button>
                            <div class="footer-hint-box simple">
                                <Lightbulb :size="14" />
                                <span>{{ $t('settings.testHint') }}</span>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Infrastructure Context (read-only runtime config) -->
                <div v-else-if="activeMainTab === 'infrastructure'" class="focused-panel pc-24 infrastructure">
                    <!-- Modify hint banner -->
                    <div class="infra-hint-banner">
                        <Info :size="16" class="hint-icon" />
                        <div class="hint-text">
                            <div class="hint-title">{{ $t('settings.infra.readOnlyTitle') }}</div>
                            <div class="hint-desc">{{ $t('settings.infra.readOnlyDesc') }}</div>
                        </div>
                    </div>

                    <!-- Loading / Error states -->
                    <div v-if="infraLoading" class="infra-state-box">
                        <div class="loading-spinner sm"></div>
                        <span>{{ $t('common.loading') }}</span>
                    </div>
                    <div v-else-if="infraError" class="infra-state-box error">
                        <XCircle :size="18" />
                        <span>{{ infraError }}</span>
                        <button class="btn btn-sm btn-secondary" @click="fetchInfrastructure">
                            <RefreshCw :size="14" />
                            {{ $t('actions.retry') }}
                        </button>
                    </div>

                    <template v-else-if="infraConfig">
                        <!-- Mode Card -->
                        <div class="infra-mode-card" :class="`mode-${infraConfig.mode}`">
                            <div class="mode-header">
                                <div class="mode-badge">
                                    <span class="dot"></span>
                                    {{ $t(`settings.infra.mode.${infraConfig.mode}`) }}
                                </div>
                                <div class="mode-title">{{ $t('settings.infra.currentMode') }}</div>
                            </div>
                            <p class="mode-desc">{{ $t(`settings.infra.modeDesc.${infraConfig.mode}`) }}</p>
                            <div class="mode-meta">
                                <span class="meta-item">
                                    <span class="meta-label">Region:</span>
                                    <span class="meta-value">{{ infraConfig.region_name }}</span>
                                </span>
                                <span class="meta-item">
                                    <span class="meta-label">S3 Client:</span>
                                    <span class="meta-value" :class="{ 'ok': infraConfig.s3_enabled, 'nok': !infraConfig.s3_enabled }">
                                        <CheckCircle2 v-if="infraConfig.s3_enabled" :size="14" />
                                        <XCircle v-else :size="14" />
                                        {{ infraConfig.s3_enabled ? $t('settings.infra.initialized') : $t('settings.infra.notInitialized') }}
                                    </span>
                                </span>
                            </div>
                        </div>

                        <!-- Test S3 action -->
                        <div class="infra-test-section">
                            <button
                                class="btn btn-primary btn-sm"
                                :disabled="testingS3 || infraConfig.mode === 'legacy'"
                                @click="testS3Connection"
                            >
                                <RefreshCw v-if="testingS3" :size="14" class="spin" />
                                <Send v-else :size="14" />
                                {{ $t('settings.infra.testS3') }}
                            </button>
                            <div v-if="s3TestResult" class="test-result" :class="{ success: s3TestResult.success, failure: !s3TestResult.success }">
                                <CheckCircle2 v-if="s3TestResult.success" :size="16" />
                                <XCircle v-else :size="16" />
                                <span>{{ s3TestResult.message }}</span>
                                <span v-if="s3TestResult.latency_ms !== undefined" class="latency">
                                    {{ s3TestResult.latency_ms }}ms
                                </span>
                            </div>
                        </div>

                        <!-- Config groups (read-only) -->
                        <div v-for="group in infraGroups" :key="group.title" class="infra-group">
                            <div class="group-header">
                                <component :is="group.icon" :size="16" />
                                <span>{{ group.title }}</span>
                            </div>
                            <div class="group-fields">
                                <div v-for="f in group.fields" :key="f.label" class="infra-field">
                                    <div class="field-key">{{ f.label }}</div>
                                    <div class="field-value" :class="{ 'is-secret': f.secret, 'is-empty': f.value === '—' || !f.value }">
                                        <Shield v-if="f.secret" :size="12" class="secret-icon" />
                                        {{ f.value }}
                                    </div>
                                </div>
                            </div>
                        </div>
                    </template>
                </div>

                <!-- Generic Settings Context (General, Quota) -->
                <div v-else class="focused-panel pc-24" :class="activeMainTab">
                    <div class="settings-form-body">
                        <div v-for="setting in currentSettings" :key="setting.key" class="form-row">
                            <div class="form-info">
                                <label class="item-label" :for="setting.key">{{ getFieldLabel(setting.key) }}</label>
                                <p class="item-desc">{{ getFieldDesc(setting) }}</p>
                            </div>
                            <div class="form-control">
                                <div v-if="setting.value_type === 'boolean'" class="bool-wrap">
                                    <label class="custom-toggle sm">
                                        <input :id="setting.key" :name="setting.key" type="checkbox" :checked="!!editValues[setting.key]" @change="editValues[setting.key] = ($event.target as HTMLInputElement).checked" />
                                        <span class="toggle-slider"></span>
                                    </label>
                                    <span class="bool-status-text">{{ !!editValues[setting.key] ? $t('common.on') : $t('common.off') }}</span>
                                </div>
                                <div v-else-if="setting.value_type === 'number'" class="input-wrap">
                                    <input :id="setting.key" :name="setting.key" type="number" class="form-input" v-model.number="editValues[setting.key]" />
                                </div>
                                <div v-else-if="setting.value_type === 'secret'" class="input-wrap">
                                    <input :id="setting.key" :name="setting.key" type="password" class="form-input" v-model="editValues[setting.key]" :placeholder="$t('settings.secretPlaceholder')" autocomplete="new-password" />
                                    <Shield class="icon-inner" :size="16" />
                                </div>
                                <div v-else-if="setting.value_type === 'json'" class="input-wrap">
                                    <textarea :id="setting.key" :name="setting.key" class="form-input code-area" v-model="editValues[setting.key]" rows="6" spellcheck="false" />
                                    <div class="type-tag">JSON</div>
                                </div>
                                <div v-else class="input-wrap">
                                    <input :id="setting.key" :name="setting.key" type="text" class="form-input" v-model="editValues[setting.key]" />
                                </div>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        </transition>

        <!-- Spinner -->
        <div v-if="loading" class="workspace-spinner">
            <div class="loading-spinner"></div>
            <span>{{ $t('common.loading') }}</span>
        </div>
    </div>
  </div>
</template>

<style scoped>
.settings-premium-view {
  max-width: 1200px;
  margin: 0 auto;
  padding: 0 0 60px;
}

/* Page Header Card (Combined) */
.page-header-card {
  position: sticky;
  top: 0;
  z-index: 20; /* Lowered from 100 to avoid covering global Layout header dropdowns */
  display: flex;
  flex-direction: column;
  padding: 0;
  border-radius: var(--radius-xl);
  margin-bottom: var(--spacing-6);
  background: var(--bg-secondary); /* Changed to slightly darker secondary bg */
  border: 1px solid var(--border-light);
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  overflow: hidden;
}

.page-header-card.is-scrolled {
  margin: var(--spacing-4) 0 var(--spacing-8);
  top: var(--spacing-2); 
  border-radius: var(--radius-xl);
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.08);
  background: var(--bg-primary); /* Uses theme variable instead of hardcoded white */
  opacity: 0.98;
  backdrop-filter: blur(12px);
  border: 1px solid var(--border-default);
}

.header-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--spacing-6) var(--spacing-6) var(--spacing-4);
}

.breadcrumb-nav {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: 4px;
}

.breadcrumb-nav .active {
  color: var(--primary-color);
}

.header-info h2 {
  font-size: var(--font-size-3xl);
  font-weight: var(--font-weight-bold);
  margin: 0;
  letter-spacing: -0.025em;
  line-height: 1.2;
}

.header-apps {
  display: flex;
  gap: var(--spacing-3);
}

.btn-icon-only {
  width: 38px;
  height: 38px;
  padding: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-md);
}

.btn-save-main {
  height: 38px;
  padding: 0 var(--spacing-4);
  border-radius: var(--radius-md);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
  gap: var(--spacing-2);
}

/* Tabs inside Header Card */
.header-tabs {
  display: flex;
  padding: 0 var(--spacing-6);
  border-top: 1px solid var(--border-light);
  gap: var(--spacing-10);
}

.main-tab-item {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  padding: var(--spacing-4) 0;
  background: transparent;
  border: none;
  cursor: pointer;
  position: relative;
  color: var(--text-tertiary);
  font-weight: var(--font-weight-bold);
  transition: all 0.2s;
}

.main-tab-item:hover {
  color: var(--text-primary);
}

.main-tab-item.active {
  color: var(--primary-color);
}

.icon-box {
  width: 30px;
  height: 30px;
  border-radius: var(--radius-md);
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-tertiary); /* Darker icon box for contrast on secondary bg */
  transition: all 0.2s;
}

.main-tab-item.active .icon-box {
  background: var(--bg-primary); /* Pop out when active */
  color: var(--primary-color);
  box-shadow: var(--shadow-sm);
}

.tab-indicator {
  position: absolute;
  bottom: -1px;
  left: -4px;
  right: -4px;
  height: 3px;
  background: var(--primary-color);
  border-radius: 3px 3px 0 0;
  transform: scaleX(0);
  transition: transform 0.3s;
}

.main-tab-item.active .tab-indicator {
  transform: scaleX(1);
}

.workspace-area {
  position: relative;
}

/* Workspace panel logic */
.view-panel {
    background: transparent;
}

.view-panel.card {
  overflow: hidden;
  background: var(--bg-primary);
}

/* Unified Split Card (Notification View) */
.unified-split-card {
  display: flex;
  background: var(--bg-primary);
  border-radius: var(--radius-xl);
  overflow: hidden;
  min-height: 600px;
}

.sidebar-nav {
  width: 260px;
  background: var(--bg-secondary);
  padding: var(--spacing-8) var(--spacing-4);
  flex-shrink: 0;
  margin: var(--spacing-4);
  border-radius: var(--radius-xl);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.06);
}

.nav-section-title {
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
  color: var(--text-tertiary);
  text-transform: uppercase;
  padding: 0 var(--spacing-4) var(--spacing-6);
  letter-spacing: 0.1em;
}

.nav-list {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-1);
}

.side-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: var(--spacing-4);
  padding: var(--spacing-4) var(--spacing-4);
  background: transparent;
  border: none;
  cursor: pointer;
  border-radius: var(--radius-lg);
  position: relative;
  text-align: left;
  transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
}

.side-item:hover {
  background: var(--bg-hover);
}

.side-item.active {
  background: var(--primary-50);
}

.side-icon-wrap {
  color: var(--text-tertiary);
}

.side-item.active .side-icon-wrap {
  color: var(--primary-color);
}

.side-text {
  display: flex;
  flex-direction: column;
}

.side-text .name {
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
  color: var(--text-primary);
}

.side-text .status {
  font-size: 10px;
  font-weight: var(--font-weight-bold);
  color: var(--text-tertiary);
  text-transform: uppercase;
}

.side-item.enabled .status {
  color: var(--success-dark);
}

.pill-active {
  position: absolute;
  left: 0;
  top: var(--spacing-3);
  bottom: var(--spacing-3);
  width: 3px;
  background: var(--primary-color);
  border-radius: 0 4px 4px 0;
  transform: scaleX(0);
  transition: transform 0.2s;
}

.side-item.active .pill-active {
  transform: scaleX(1);
}

.pane-content {
  flex: 1;
  padding: var(--spacing-10);
  background: var(--bg-primary);
}

.pc-24 {
    padding: var(--spacing-6) var(--spacing-10);
}

/* Hero Section */
.activation-hero {
  background: var(--bg-secondary);
  padding: var(--spacing-6) var(--spacing-8);
  border-radius: var(--radius-xl);
  display: flex;
  align-items: center;
  justify-content: space-between;
  border: 1px dashed var(--border-default);
  margin-bottom: var(--spacing-10);
}

.hero-desc h3 {
  margin: 0;
  font-size: var(--font-size-base);
  font-weight: var(--font-weight-bold);
}

.hero-desc p {
  margin: 4px 0 0;
  font-size: var(--font-size-sm);
}

/* Settings Form */
.settings-form-body {
  display: flex;
  flex-direction: column;
}

.settings-form-body.disabled {
  opacity: 0.5;
  pointer-events: none;
  filter: grayscale(0.2);
}

.form-row {
  display: flex;
  align-items: flex-start;
  padding: var(--spacing-8) 0;
  border-bottom: 1px solid var(--border-light);
  gap: var(--spacing-12);
}

.form-row:last-child {
  border-bottom: none;
}

.form-info {
  flex: 0 0 320px;
}

.item-label {
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
  color: var(--text-primary);
  display: block;
  margin-bottom: 4px;
}

.item-desc {
  font-size: var(--font-size-xs);
  line-height: 1.5;
  color: var(--text-tertiary);
  margin: 0;
}

.form-control {
  flex: 1;
  max-width: 500px;
}

.input-wrap {
  position: relative;
}

.icon-inner {
  position: absolute;
  right: var(--spacing-3);
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-tertiary);
  pointer-events: none;
}

.form-input {
  width: 100%;
  padding: var(--spacing-3) var(--spacing-4);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-lg);
  background: var(--bg-primary);
  color: var(--text-primary);
  font-size: var(--font-size-sm);
  transition: all 0.2s;
}

.form-input:focus {
  outline: none;
  border-color: var(--primary-color);
  box-shadow: 0 0 0 3px var(--primary-100);
}

.code-area {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-sm);
  line-height: 1.6;
}

.type-tag {
  position: absolute;
  bottom: var(--spacing-2);
  right: var(--spacing-3);
  font-size: 10px;
  font-weight: var(--font-weight-bold);
  color: var(--text-light);
  opacity: 0.4;
}

.bool-wrap {
  display: flex;
  align-items: center;
  gap: var(--spacing-3);
}

.bool-status-text {
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
  color: var(--text-tertiary);
  text-transform: uppercase;
}

/* Custom Toggle Switch */
.custom-toggle {
  position: relative;
  display: inline-block;
  width: 52px;
  height: 28px;
  cursor: pointer;
}

.custom-toggle.sm {
  width: 44px;
  height: 24px;
}

.custom-toggle input {
  opacity: 0;
  width: 0;
  height: 0;
}

.toggle-slider {
  position: absolute;
  inset: 0;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-default);
  border-radius: 30px;
  transition: all 0.35s cubic-bezier(0.4, 0, 0.2, 1);
}

.toggle-slider::before {
  content: '';
  position: absolute;
  height: 22px;
  width: 22px;
  left: 2px;
  top: 2px;
  background: white;
  border-radius: 50%;
  transition: all 0.35s cubic-bezier(0.4, 0, 0.2, 1);
  box-shadow: var(--shadow-sm);
}

.custom-toggle.sm .toggle-slider::before {
  height: 18px;
  width: 18px;
}

.custom-toggle input:checked + .toggle-slider {
  background: var(--primary-color);
  border-color: var(--primary-600);
}

.custom-toggle input:checked + .toggle-slider::before {
  transform: translateX(24px);
}

.custom-toggle.sm input:checked + .toggle-slider::before {
  transform: translateX(20px);
}

/* Pane Footer */
.pane-footer {
  display: flex;
  align-items: center;
  gap: var(--spacing-4);
  margin-top: var(--spacing-10);
  padding-top: var(--spacing-8);
  border-top: 1px solid var(--border-light);
}

.pane-footer.center-align {
  justify-content: flex-start;
}

.connectivity-btn {
  padding: 0 var(--spacing-6);
  height: 38px;
  border-radius: var(--radius-lg);
  font-weight: var(--font-weight-bold);
}

.footer-hint-box.simple {
  background: transparent;
  padding: 0;
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
}

/* Spinner Overlay */
.workspace-spinner {
  position: absolute;
  inset: 0;
  background: var(--bg-overlay, rgba(255, 255, 255, 0.8));
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--spacing-4);
  z-index: 100;
  backdrop-filter: blur(4px);
}

.spin {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* Infrastructure Tab */
.infra-hint-banner {
    display: flex;
    gap: var(--spacing-3);
    padding: var(--spacing-4) var(--spacing-5);
    background: var(--warning-50, #fff7ed);
    border: 1px solid var(--warning-200, #fed7aa);
    border-radius: var(--radius-lg);
    margin-bottom: var(--spacing-6);
}

.infra-hint-banner .hint-icon {
    color: var(--warning-dark, #b45309);
    flex-shrink: 0;
    margin-top: 2px;
}

.hint-title {
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-bold);
    color: var(--text-primary);
    margin-bottom: 2px;
}

.hint-desc {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    line-height: 1.5;
    white-space: pre-wrap;
    font-family: var(--font-family-mono);
}

.infra-state-box {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    padding: var(--spacing-6);
    background: var(--bg-secondary);
    border-radius: var(--radius-lg);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
}

.infra-state-box.error {
    color: var(--danger-dark);
    background: var(--danger-50, #fef2f2);
}

.infra-state-box .loading-spinner.sm {
    width: 18px;
    height: 18px;
}

.infra-mode-card {
    padding: var(--spacing-6);
    border-radius: var(--radius-xl);
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    margin-bottom: var(--spacing-6);
    transition: all 0.2s;
}

.infra-mode-card.mode-minio {
    background: linear-gradient(135deg, var(--primary-50), var(--bg-secondary));
    border-color: var(--primary-200, #bfdbfe);
}

.infra-mode-card.mode-external_s3 {
    background: linear-gradient(135deg, var(--success-50, #ecfdf5), var(--bg-secondary));
    border-color: var(--success-200, #bbf7d0);
}

.infra-mode-card.mode-legacy {
    background: linear-gradient(135deg, var(--warning-50, #fff7ed), var(--bg-secondary));
    border-color: var(--warning-200, #fed7aa);
}

.mode-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-2);
}

.mode-badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 12px;
    border-radius: 999px;
    background: var(--bg-primary);
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-bold);
    color: var(--primary-color);
    border: 1px solid var(--border-default);
    text-transform: uppercase;
    letter-spacing: 0.05em;
}

.mode-badge .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--primary-color);
    animation: pulse 1.8s infinite;
}

.mode-minio .mode-badge .dot { background: var(--primary-color); }
.mode-external_s3 .mode-badge .dot { background: var(--success-color); }
.mode-legacy .mode-badge .dot { background: var(--warning-color); }

@keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
}

.mode-title {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    font-weight: var(--font-weight-bold);
    text-transform: uppercase;
    letter-spacing: 0.05em;
}

.mode-desc {
    margin: 0 0 var(--spacing-4);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.mode-meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-4);
    font-size: var(--font-size-xs);
}

.meta-item {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
}

.meta-label {
    color: var(--text-tertiary);
    font-weight: var(--font-weight-bold);
    text-transform: uppercase;
    letter-spacing: 0.04em;
}

.meta-value {
    color: var(--text-primary);
    font-weight: var(--font-weight-bold);
    font-family: var(--font-family-mono);
}

.meta-value.ok {
    color: var(--success-dark);
    display: inline-flex;
    align-items: center;
    gap: 4px;
}

.meta-value.nok {
    color: var(--danger-dark);
    display: inline-flex;
    align-items: center;
    gap: 4px;
}

.infra-test-section {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
    padding: var(--spacing-4) 0;
    margin-bottom: var(--spacing-2);
    flex-wrap: wrap;
}

.test-result {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    padding: 6px 12px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-xs);
    font-family: var(--font-family-mono);
}

.test-result.success {
    background: var(--success-50, #ecfdf5);
    color: var(--success-dark);
}

.test-result.failure {
    background: var(--danger-50, #fef2f2);
    color: var(--danger-dark);
}

.test-result .latency {
    opacity: 0.7;
    margin-left: 4px;
}

.infra-group {
    margin-top: var(--spacing-6);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-lg);
    overflow: hidden;
}

.group-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    padding: var(--spacing-3) var(--spacing-4);
    background: var(--bg-secondary);
    border-bottom: 1px solid var(--border-light);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-bold);
    color: var(--text-primary);
}

.group-fields {
    background: var(--bg-primary);
}

.infra-field {
    display: grid;
    grid-template-columns: 280px 1fr;
    gap: var(--spacing-4);
    padding: var(--spacing-3) var(--spacing-4);
    border-bottom: 1px solid var(--border-light);
    align-items: center;
}

.infra-field:last-child {
    border-bottom: none;
}

.field-key {
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-bold);
    color: var(--text-tertiary);
    font-family: var(--font-family-mono);
    letter-spacing: 0.02em;
}

.field-value {
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    font-family: var(--font-family-mono);
    word-break: break-all;
    display: inline-flex;
    align-items: center;
    gap: 6px;
}

.field-value.is-secret {
    color: var(--text-secondary);
    letter-spacing: 0.1em;
}

.field-value.is-empty {
    color: var(--text-tertiary);
    font-style: italic;
}

.secret-icon {
    color: var(--text-tertiary);
}

/* Transitions */
.view-fade-enter-active, .view-fade-leave-active {
  transition: all 0.3s ease;
}
.view-fade-enter-from { opacity: 0; transform: translateY(10px); }
.view-fade-leave-to { opacity: 0; transform: translateY(-10px); }
</style>
