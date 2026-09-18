<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteLeave } from 'vue-router'
import {
    Save, Send, RefreshCw,
    Settings2, Bell, Layers, Server,
    Mail, MessageSquare, CheckCircle2, XCircle, Shield
} from 'lucide-vue-next'
import BaseModal from '../../components/modals/BaseModal.vue'
import { useToast } from '../../composables/useToast'
import { systemSettingsApi, type SystemSetting } from '../../api/systemSettings'
import { infrastructureApi, type InfrastructureConfig, type TestS3Response } from '../../api/infrastructure'

const { t, te } = useI18n()
const toast = useToast()

type Category = 'general' | 'quota' | 'notification' | 'infrastructure'
type Channel = 'email' | 'feishu'

const categories: Array<{ key: Category; icon: Component }> = [
    { key: 'general', icon: Settings2 },
    { key: 'quota', icon: Layers },
    { key: 'notification', icon: Bell },
    { key: 'infrastructure', icon: Server },
]
const channels: Array<{ key: Channel; icon: Component; prefix: string }> = [
    { key: 'email', icon: Mail, prefix: 'SMTP_' },
    { key: 'feishu', icon: MessageSquare, prefix: 'FEISHU_' },
]

// Editable settings are shown in titled sections; keys missing here fall into a trailing "other" section
const sectionLayout: Record<'general' | 'quota', Array<{ key: string; fields: string[] }>> = {
    general: [
        { key: 'access', fields: ['FRONTEND_URL'] },
        { key: 'retention', fields: ['ALARM_EVENT_RETENTION_DAYS', 'AUDIT_LOG_RETENTION_DAYS'] },
        { key: 'network', fields: ['DNS_UPSTREAM'] },
        { key: 'hostConsole', fields: ['HOST_CONSOLE_ENABLED', 'HOST_CONSOLE_REQUIRE_PASSWORD', 'HOST_CONSOLE_IDLE_MINUTES'] },
    ],
    quota: [
        { key: 'quotaCompute', fields: ['DEFAULT_CPU_CORES', 'DEFAULT_RAM_GB', 'DEFAULT_DISK_GB'] },
        { key: 'quotaOther', fields: ['DEFAULT_PUBLIC_IPS', 'DEFAULT_VPCS', 'DEFAULT_LOAD_BALANCERS', 'DEFAULT_IMAGES'] },
    ],
}

const activeTab = ref<Category>('general')
const activeChannel = ref<Channel>('email')

const loading = ref(false)
const saving = ref(false)
const settings = ref<SystemSetting[]>([])
// Values bound to the inputs, and the normalized values last loaded from the server
const editValues = ref<Record<string, any>>({})
const savedValues = ref<Record<string, string>>({})

// Numeric settings with an allowed range; keep in sync with settingRanges in cpgateway (the backend validates too)
const numberRanges: Record<string, { min: number; max: number; integer: boolean }> = {
    AUDIT_LOG_RETENTION_DAYS: { min: 90, max: 3650, integer: true },
    HOST_CONSOLE_IDLE_MINUTES: { min: 5, max: 240, integer: true },
    DEFAULT_CPU_CORES: { min: 0, max: 1e6, integer: false },
    DEFAULT_RAM_GB: { min: 0, max: 1e7, integer: false },
    DEFAULT_DISK_GB: { min: 0, max: 1e9, integer: false },
    DEFAULT_PUBLIC_IPS: { min: 0, max: 1e5, integer: true },
    DEFAULT_VPCS: { min: 0, max: 1e5, integer: true },
    DEFAULT_LOAD_BALANCERS: { min: 0, max: 1e5, integer: true },
    DEFAULT_IMAGES: { min: 0, max: 1e5, integer: true },
}

const fieldUnits: Record<string, 'cores' | 'gb' | 'days' | 'minutes'> = {
    DEFAULT_CPU_CORES: 'cores',
    DEFAULT_RAM_GB: 'gb',
    DEFAULT_DISK_GB: 'gb',
    ALARM_EVENT_RETENTION_DAYS: 'days',
    AUDIT_LOG_RETENTION_DAYS: 'days',
    HOST_CONSOLE_IDLE_MINUTES: 'minutes',
}

// NOTIFICATION_CHANNELS is edited through the channel switches, not as a field
const CHANNELS_KEY = 'NOTIFICATION_CHANNELS'

const fieldLabel = (setting: SystemSetting) => {
    const key = `settings.fields.${setting.key}`
    return te(key) ? t(key) : (setting.description || setting.key)
}

const fieldDesc = (setting: SystemSetting) => {
    const key = `settings.fields.${setting.key}_desc`
    return te(key) ? t(key) : ''
}

const unitLabel = (key: string) => {
    const unit = fieldUnits[key]
    return unit ? t(`settings.units.${unit}`) : ''
}

// Comparable form of a value: inputs hand back strings or numbers, and a list of channel names is a set
const normalize = (setting: SystemSetting, value: unknown): string => {
    switch (setting.value_type) {
        case 'json':
            return JSON.stringify(Array.isArray(value) && value.every(v => typeof v === 'string')
                ? [...value].sort()
                : (value ?? null))
        case 'number':
            return value === '' || value == null ? '' : String(Number(value))
        case 'boolean':
            return String(Boolean(value))
        default:
            return String(value ?? '')
    }
}

const loadValues = (list: SystemSetting[]) => {
    settings.value = list
    const edits: Record<string, any> = {}
    const saved: Record<string, string> = {}
    for (const s of list) {
        let value = s.value
        if (s.value_type === 'json' && typeof value === 'string') {
            try { value = JSON.parse(value) } catch { /* keep the raw string */ }
        }
        edits[s.key] = value ?? ''
        saved[s.key] = normalize(s, edits[s.key])
    }
    editValues.value = edits
    savedValues.value = saved
}

const dirtySettings = computed(() =>
    settings.value.filter(s => normalize(s, editValues.value[s.key]) !== savedValues.value[s.key]))

const dirtyKeys = computed(() => new Set(dirtySettings.value.map(s => s.key)))

const categoryDirty = (category: Category) =>
    dirtySettings.value.some(s => s.category === category)

const channelDirty = (channel: Channel) => {
    const prefix = channels.find(c => c.key === channel)!.prefix
    return dirtySettings.value.some(s => s.key.startsWith(prefix))
}

const fetchSettings = async () => {
    loading.value = true
    try {
        const res = await systemSettingsApi.list()
        loadValues(res.data.settings || [])
    } catch (err) {
        console.error('Failed to load system settings:', err)
        toast.error(t('messages.error'))
    } finally {
        loading.value = false
    }
}

// Turning the host console password prompt on or off asks for the password itself, so that setting is saved
// through a password dialog
const PASSWORD_GUARDED_KEY = 'HOST_CONSOLE_REQUIRE_PASSWORD'
const showPasswordPrompt = ref(false)
const confirmPassword = ref('')
const passwordError = ref('')

// Set while the dialog belongs to the toggle itself: confirming saves only that setting, cancelling puts the
// switch back. Empty when the dialog was opened by the save button.
const promptFromToggle = ref(false)

const askPassword = async (fromToggle = false) => {
    confirmPassword.value = ''
    passwordError.value = ''
    promptFromToggle.value = fromToggle
    showPasswordPrompt.value = true
}

const cancelPasswordPrompt = () => {
    // The switch was flipped before the dialog opened: put it back
    if (promptFromToggle.value) {
        editValues.value[PASSWORD_GUARDED_KEY] = savedValues.value[PASSWORD_GUARDED_KEY] === 'true'
    }
    showPasswordPrompt.value = false
    promptFromToggle.value = false
    confirmPassword.value = ''
    passwordError.value = ''
}

// Flipping the host console password switch asks for the password right away, instead of waiting for the save
const onToggle = (key: string, checked: boolean) => {
    editValues.value[key] = checked
    if (key === PASSWORD_GUARDED_KEY) askPassword(true)
}

const saveSettings = async (password?: string) => {
    if (!password && dirtyKeys.value.has(PASSWORD_GUARDED_KEY)) {
        await askPassword()
        return
    }
    // The dialog opened by the switch saves that one setting, leaving other pending edits alone
    const pending = promptFromToggle.value
        ? dirtySettings.value.filter(s => s.key === PASSWORD_GUARDED_KEY)
        : dirtySettings.value
    const payload: Record<string, unknown> = {}
    if (password) payload.password = password
    for (const s of pending) {
        const raw = editValues.value[s.key]
        if (s.value_type === 'number') {
            const range = numberRanges[s.key]
            const num = Number(raw)
            if (raw === '' || Number.isNaN(num) || (range && ((range.integer && !Number.isInteger(num)) || num < range.min || num > range.max))) {
                activeTab.value = s.category as Category
                toast.error(range
                    ? t(range.integer ? 'settings.rangeError' : 'settings.rangeErrorNumber', { label: fieldLabel(s), min: range.min, max: range.max })
                    : t('messages.error'))
                return
            }
            payload[s.key] = num
        } else if (s.value_type === 'boolean') {
            payload[s.key] = Boolean(raw)
        } else {
            payload[s.key] = raw
        }
    }
    if (Object.keys(payload).length === 0) return

    // Saving just the switch must not drop edits of other fields, which the reloaded values would overwrite
    const otherEdits = promptFromToggle.value
        ? Object.fromEntries(dirtySettings.value
            .filter(s => s.key !== PASSWORD_GUARDED_KEY)
            .map(s => [s.key, editValues.value[s.key]]))
        : null

    saving.value = true
    try {
        const res = await systemSettingsApi.update(payload)
        loadValues(res.data.settings || [])
        if (otherEdits) Object.assign(editValues.value, otherEdits)
        promptFromToggle.value = false
        cancelPasswordPrompt()
        toast.success(t('settings.saveSuccess'))
    } catch (err) {
        // Show the backend reason when validation fails (e.g. a value out of range)
        const response = (err as { response?: { status?: number; data?: { detail?: unknown } } })?.response
        const detail = response?.data?.detail
        const message = typeof detail === 'string' ? detail : t('messages.error')
        // A wrong password keeps the dialog open with the reason next to the input
        if (password && (response?.status === 403 || response?.status === 429)) {
            passwordError.value = message
            confirmPassword.value = ''
            return
        }
        toast.error(message)
    } finally {
        saving.value = false
    }
}

// The loaded list still holds the server values: reloading from it drops every edit
const discardChanges = () => loadValues(settings.value)

const sections = computed(() => {
    const tab = activeTab.value
    if (tab !== 'general' && tab !== 'quota') return []
    const byKey = new Map(settings.value.filter(s => s.category === tab).map(s => [s.key, s]))
    const placed = new Set<string>()
    const result = sectionLayout[tab].map(section => {
        const fields = section.fields.map(k => byKey.get(k)).filter((s): s is SystemSetting => !!s)
        fields.forEach(s => placed.add(s.key))
        return { key: section.key, fields }
    }).filter(section => section.fields.length)
    const rest = [...byKey.values()].filter(s => !placed.has(s.key) && s.key !== CHANNELS_KEY)
    if (rest.length) result.push({ key: 'other', fields: rest })
    return result
})

// --- Notification channels ---
const enabledChannels = computed<string[]>(() => {
    const value = editValues.value[CHANNELS_KEY]
    return Array.isArray(value) ? value : []
})

const toggleChannel = (channel: Channel) => {
    const current = enabledChannels.value.filter(c => c !== channel)
    if (!enabledChannels.value.includes(channel)) current.push(channel)
    editValues.value[CHANNELS_KEY] = current
}

const channelFields = computed(() => {
    const prefix = channels.find(c => c.key === activeChannel.value)!.prefix
    return settings.value.filter(s => s.category === 'notification' && s.key.startsWith(prefix))
})

const testingChannel = ref<Channel | null>(null)

const testChannel = async (channel: Channel) => {
    testingChannel.value = channel
    try {
        const res = await systemSettingsApi.testNotification(channel)
        if (res.data.success) {
            toast.success(t('settings.testSuccess', { channel: t(`settings.channel.${channel}`) }))
        } else {
            toast.error(t('settings.testFailed', { message: res.data.message }))
        }
    } catch (err) {
        console.error('Notification test failed:', err)
        toast.error(t('messages.error'))
    } finally {
        testingChannel.value = null
    }
}

// --- Infrastructure tab (read-only runtime config of the current region) ---
const infraConfig = ref<InfrastructureConfig | null>(null)
const infraLoading = ref(false)
const infraError = ref('')
const testingS3 = ref(false)
const s3TestResult = ref<TestS3Response | null>(null)

const errorMessage = (err: unknown, fallback: string) => {
    const e = err as { response?: { data?: { detail?: string } }; message?: string }
    return e?.response?.data?.detail || e?.message || fallback
}

const fetchInfrastructure = async () => {
    infraLoading.value = true
    infraError.value = ''
    s3TestResult.value = null
    try {
        const res = await infrastructureApi.get()
        infraConfig.value = res.data
    } catch (err) {
        infraError.value = errorMessage(err, t('messages.error'))
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
    } catch (err) {
        const msg = errorMessage(err, t('messages.error'))
        s3TestResult.value = { success: false, message: msg }
        toast.error(t('settings.infra.testS3Failed', { message: msg }))
    } finally {
        testingS3.value = false
    }
}

const infraGroups = computed(() => {
    const c = infraConfig.value
    if (!c) return []
    const unset = t('settings.infra.unset')
    return [
        {
            title: t('settings.infra.groupS3'),
            fields: [
                { label: 'S3_ENDPOINT', value: c.s3_endpoint },
                { label: 'S3_ACCESS_KEY', value: c.s3_access_key },
                { label: 'S3_SECRET_KEY', value: c.s3_secret_key_set ? c.s3_secret_key : unset, secret: true },
                { label: 'S3_BUCKET', value: c.s3_bucket },
                { label: 'S3_REGION', value: c.s3_region },
                { label: 'S3_USE_SSL', value: String(c.s3_use_ssl) },
                { label: 'S3_UPLOAD_TIMEOUT_MINUTES', value: c.s3_upload_timeout_minutes ? String(c.s3_upload_timeout_minutes) : '' },
            ],
        },
        {
            title: t('settings.infra.groupMinio'),
            fields: [
                { label: 'MINIO_HOSTNAME', value: c.minio_hostname },
            ],
        },
        {
            title: t('settings.infra.groupCapture'),
            fields: [
                { label: 'CLAPI_HOSTNAME', value: c.clapi_hostname },
                { label: 'CLAPI_INTERNAL_URL', value: c.clapi_internal_url || t('settings.infra.autoDerived') },
                { label: 'CAPTURE_UPLOAD_SECRET', value: c.capture_upload_secret_set ? c.capture_upload_secret : unset, secret: true, missing: !c.capture_upload_secret_set },
            ],
        },
    ]
})

watch(activeTab, (tab) => {
    if (tab === 'infrastructure' && !infraConfig.value && !infraLoading.value) {
        fetchInfrastructure()
    }
})

const refresh = () => {
    if (activeTab.value === 'infrastructure') {
        fetchInfrastructure()
    } else if (!dirtySettings.value.length || confirm(t('settings.leaveConfirm'))) {
        fetchSettings()
    }
}

// --- Unsaved changes guard ---
const onBeforeUnload = (event: BeforeUnloadEvent) => {
    if (dirtySettings.value.length) {
        event.preventDefault()
        event.returnValue = ''
    }
}

onBeforeRouteLeave(() => {
    if (dirtySettings.value.length && !confirm(t('settings.leaveConfirm'))) return false
})

onMounted(() => {
    fetchSettings()
    window.addEventListener('beforeunload', onBeforeUnload)
})

onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', onBeforeUnload)
})
</script>

<template>
  <div class="settings-page">
    <!-- Tabs and actions, laid out like the tab bar of the resource detail pages -->
    <div class="card tab-card">
      <div class="tab-header">
        <div class="tab-nav" role="tablist">
          <button
            v-for="cat in categories"
            :key="cat.key"
            type="button"
            role="tab"
            :class="['tab-btn', { active: activeTab === cat.key }]"
            :aria-selected="activeTab === cat.key"
            @click="activeTab = cat.key"
          >
            <component :is="cat.icon" :size="16" />
            {{ $t(`settings.category.${cat.key}`) }}
            <span v-if="categoryDirty(cat.key)" class="dirty-dot" />
          </button>
        </div>
        <div class="tab-header-actions">
          <template v-if="dirtySettings.length">
            <span class="unsaved-text">{{ $t('settings.unsavedChanges', { n: dirtySettings.length }, dirtySettings.length) }}</span>
            <button type="button" class="btn btn-ghost btn-sm" :disabled="saving" @click="discardChanges">
              {{ $t('settings.discardChanges') }}
            </button>
          </template>
          <button
            type="button"
            class="btn btn-secondary btn-sm btn-icon"
            :disabled="loading || infraLoading"
            :title="$t('actions.refresh')"
            :aria-label="$t('actions.refresh')"
            @click="refresh"
          >
            <RefreshCw :size="14" :class="{ spinning: loading || infraLoading }" />
          </button>
          <button
            v-if="activeTab !== 'infrastructure' || dirtySettings.length"
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="saving || loading || !dirtySettings.length"
            @click="saveSettings()"
          >
            <RefreshCw v-if="saving" :size="14" class="spinning" />
            <Save v-else :size="14" />
            {{ saving ? $t('common.saving') : $t('common.save') }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="loading && !settings.length" class="loading-container">
      <div class="loading-spinner"></div>
    </div>

    <!-- General / quota: one card per section -->
    <template v-else-if="activeTab === 'general' || activeTab === 'quota'">
      <div v-for="section in sections" :key="section.key" class="card info-card">
        <h3>{{ $t(`settings.sections.${section.key}`) }}</h3>
        <p v-if="$te(`settings.sections.${section.key}Desc`)" class="card-desc">{{ $t(`settings.sections.${section.key}Desc`) }}</p>
        <div class="setting-list">
          <div v-for="setting in section.fields" :key="setting.key" class="setting-row" :class="{ changed: dirtyKeys.has(setting.key) }">
            <div class="row-info">
              <label class="row-label" :for="setting.key" :title="setting.key">
                {{ fieldLabel(setting) }}
                <span v-if="dirtyKeys.has(setting.key)" class="dirty-dot" />
              </label>
              <p v-if="fieldDesc(setting)" class="row-desc">{{ fieldDesc(setting) }}</p>
            </div>
            <div class="row-control">
              <div v-if="setting.value_type === 'number'" class="input-affix narrow">
                <input
                  :id="setting.key"
                  v-model.number="editValues[setting.key]"
                  type="number"
                  class="form-input"
                  :min="numberRanges[setting.key]?.min"
                  :max="numberRanges[setting.key]?.max"
                  :step="numberRanges[setting.key]?.integer ? 1 : 'any'"
                />
                <span v-if="unitLabel(setting.key)" class="affix">{{ unitLabel(setting.key) }}</span>
              </div>
              <label v-else-if="setting.value_type === 'boolean'" class="toggle-row">
                <span class="custom-toggle">
                  <input
                    :id="setting.key"
                    type="checkbox"
                    :checked="!!editValues[setting.key]"
                    @change="onToggle(setting.key, ($event.target as HTMLInputElement).checked)"
                  />
                  <span class="toggle-slider"></span>
                </span>
              </label>
              <input
                v-else
                :id="setting.key"
                v-model="editValues[setting.key]"
                :type="setting.value_type === 'secret' ? 'password' : 'text'"
                class="form-input wide"
                :placeholder="setting.value_type === 'secret' ? $t('settings.secretPlaceholder') : ''"
                :autocomplete="setting.value_type === 'secret' ? 'new-password' : 'off'"
              />
            </div>
          </div>
        </div>
      </div>
    </template>

    <!-- Notification: channels as second-level tabs inside one card -->
    <div v-else-if="activeTab === 'notification'" class="card tab-card">
      <div class="tab-header">
        <div class="tab-nav" role="tablist">
          <button
            v-for="ch in channels"
            :key="ch.key"
            type="button"
            role="tab"
            :class="['tab-btn', { active: activeChannel === ch.key }]"
            :aria-selected="activeChannel === ch.key"
            @click="activeChannel = ch.key"
          >
            <component :is="ch.icon" :size="16" />
            {{ $t(`settings.channel.${ch.key}`) }}
            <span :class="['status-badge', { on: enabledChannels.includes(ch.key) }]">
              {{ enabledChannels.includes(ch.key) ? $t('settings.channelEnabled') : $t('settings.channelDisabled') }}
            </span>
            <span v-if="channelDirty(ch.key)" class="dirty-dot" />
          </button>
        </div>
        <div class="tab-header-actions">
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="testingChannel !== null || channelDirty(activeChannel)"
            :title="channelDirty(activeChannel) ? $t('settings.testUsesSaved') : $t('settings.testHint')"
            @click="testChannel(activeChannel)"
          >
            <RefreshCw v-if="testingChannel === activeChannel" :size="14" class="spinning" />
            <Send v-else :size="14" />
            {{ $t('settings.testConnectivity') }}
          </button>
        </div>
      </div>

      <div class="tab-body">
        <p class="card-desc">
          {{ $t('settings.notificationHint') }}
          <router-link :to="{ name: 'notification-channels' }" class="text-link">{{ $t('settings.notificationHintLink') }}</router-link>{{ $t('settings.notificationHintSuffix') }}
        </p>
        <div class="setting-list">
          <div class="setting-row" :class="{ changed: dirtyKeys.has(CHANNELS_KEY) }">
            <div class="row-info">
              <span class="row-label">
                {{ $t('settings.enableChannel') }}
                <span v-if="dirtyKeys.has(CHANNELS_KEY)" class="dirty-dot" />
              </span>
              <p class="row-desc">{{ $t('settings.enableChannelDesc') }}</p>
            </div>
            <div class="row-control">
              <label class="toggle-row">
                <span class="custom-toggle">
                  <input
                    type="checkbox"
                    :checked="enabledChannels.includes(activeChannel)"
                    :aria-label="$t('settings.enableChannel')"
                    @change="toggleChannel(activeChannel)"
                  />
                  <span class="toggle-slider"></span>
                </span>
              </label>
            </div>
          </div>

          <div v-for="setting in channelFields" :key="setting.key" class="setting-row" :class="{ changed: dirtyKeys.has(setting.key) }">
            <div class="row-info">
              <label class="row-label" :for="setting.key" :title="setting.key">
                {{ fieldLabel(setting) }}
                <span v-if="dirtyKeys.has(setting.key)" class="dirty-dot" />
              </label>
              <p v-if="fieldDesc(setting)" class="row-desc">{{ fieldDesc(setting) }}</p>
            </div>
            <div class="row-control">
              <label v-if="setting.value_type === 'boolean'" class="toggle-row">
                <span class="custom-toggle">
                  <input
                    :id="setting.key"
                    type="checkbox"
                    :checked="!!editValues[setting.key]"
                    @change="onToggle(setting.key, ($event.target as HTMLInputElement).checked)"
                  />
                  <span class="toggle-slider"></span>
                </span>
              </label>
              <input
                v-else-if="setting.value_type === 'number'"
                :id="setting.key"
                v-model.number="editValues[setting.key]"
                type="number"
                class="form-input narrow plain-number"
              />
              <input
                v-else
                :id="setting.key"
                v-model="editValues[setting.key]"
                :type="setting.value_type === 'secret' ? 'password' : 'text'"
                class="form-input wide"
                :placeholder="setting.value_type === 'secret' ? $t('settings.secretPlaceholder') : ''"
                :autocomplete="setting.value_type === 'secret' ? 'new-password' : 'off'"
              />
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Infrastructure (read-only) -->
    <template v-else>
      <div v-if="infraLoading" class="loading-container">
        <div class="loading-spinner"></div>
      </div>
      <div v-else-if="infraError" class="card error-container">
        <p class="text-error">{{ infraError }}</p>
        <button type="button" class="btn btn-primary btn-sm" @click="fetchInfrastructure">{{ $t('actions.retry') }}</button>
      </div>

      <template v-else-if="infraConfig">
        <div class="card info-card">
          <h3>{{ $t('settings.infra.currentMode') }}</h3>
          <p class="card-desc">{{ $t('settings.infra.readOnlyDesc') }}</p>
          <div class="mode-row">
            <div class="mode-info">
              <span :class="['mode-badge', `mode-${infraConfig.mode}`]">{{ $t(`settings.infra.mode.${infraConfig.mode}`) }}</span>
              <span class="mode-desc">{{ $t(`settings.infra.modeDesc.${infraConfig.mode}`) }}</span>
            </div>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="testingS3 || infraConfig.mode === 'legacy'"
              @click="testS3Connection"
            >
              <RefreshCw v-if="testingS3" :size="14" class="spinning" />
              <Send v-else :size="14" />
              {{ $t('settings.infra.testS3') }}
            </button>
          </div>
          <div class="key-value-list">
            <div class="kv-item">
              <span class="label">Region</span>
              <span class="value">{{ infraConfig.region_name }}</span>
            </div>
            <div class="kv-item">
              <span class="label">S3</span>
              <span :class="['value', 'status-text', infraConfig.s3_enabled ? 'ok' : 'nok']">
                <CheckCircle2 v-if="infraConfig.s3_enabled" :size="14" />
                <XCircle v-else :size="14" />
                {{ infraConfig.s3_enabled ? $t('settings.infra.initialized') : $t('settings.infra.notInitialized') }}
              </span>
            </div>
            <div v-if="s3TestResult" class="kv-item">
              <span class="label">{{ $t('settings.infra.testS3') }}</span>
              <span :class="['value', 'status-text', s3TestResult.success ? 'ok' : 'nok']">
                {{ s3TestResult.message }}<template v-if="s3TestResult.latency_ms !== undefined"> · {{ s3TestResult.latency_ms }}ms</template>
              </span>
            </div>
          </div>
        </div>

        <div v-for="group in infraGroups" :key="group.title" class="card info-card">
          <h3>{{ group.title }}</h3>
          <div class="key-value-list">
            <div v-for="f in group.fields" :key="f.label" class="kv-item">
              <span class="label mono">{{ f.label }}</span>
              <span :class="['value', 'mono', { 'text-secondary': !f.value, 'text-error': f.missing }]">
                <Shield v-if="f.secret" :size="12" />
                {{ f.value || '-' }}
              </span>
            </div>
          </div>
        </div>
      </template>
    </template>

    <!-- Turning the host console password prompt on or off asks for the password itself -->
    <BaseModal
      :show="showPasswordPrompt"
      :title="$t('settings.passwordPrompt.title')"
      :loading="saving"
      form
      @close="cancelPasswordPrompt"
      @submit="saveSettings(confirmPassword)"
    >
      <p class="card-desc">
        {{ editValues[PASSWORD_GUARDED_KEY]
          ? $t('settings.passwordPrompt.descEnable')
          : $t('settings.passwordPrompt.descDisable') }}
      </p>
      <input type="text" autocomplete="username" class="visually-hidden" tabindex="-1" aria-hidden="true" />
      <input
        v-model="confirmPassword"
        type="password"
        class="form-input"
        style="width: 100%;"
        autocomplete="current-password"
        :placeholder="$t('settings.passwordPrompt.placeholder')"
      />
      <p v-if="passwordError" class="password-error">{{ passwordError }}</p>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="cancelPasswordPrompt">{{ $t('actions.cancel') }}</button>
        <button type="submit" class="btn btn-primary" :disabled="saving || !confirmPassword">
          <RefreshCw v-if="saving" :size="14" class="spinning" />
          {{ saving ? $t('common.saving') : $t('common.save') }}
        </button>
      </template>
    </BaseModal>
  </div>
</template>

<style scoped>
/* Tab card: same look as the tab bar of the resource detail pages */
.tab-card {
  padding: 0;
  overflow: hidden;
  margin-bottom: var(--spacing-6);
}

.tab-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--spacing-2);
  padding: 0 var(--spacing-5);
}

.tab-nav {
  display: flex;
  gap: 0;
  overflow-x: auto;
  scrollbar-width: none;
}

.tab-btn {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  padding: var(--spacing-4);
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--text-secondary);
  white-space: nowrap;
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s;
}

.tab-btn:hover {
  color: var(--text-primary);
}

.tab-btn.active {
  color: var(--primary-color);
  border-bottom-color: var(--primary-color);
}

.tab-header-actions {
  display: flex;
  align-items: center;
  gap: var(--spacing-2);
  padding: var(--spacing-2) 0;
  margin-left: auto;
}

.tab-body {
  padding: var(--spacing-5);
  border-top: 1px solid var(--border-light);
}

.unsaved-text {
  font-size: var(--font-size-xs);
  color: var(--warning-dark, #b45309);
  white-space: nowrap;
}

.dirty-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--warning-color, #f59e0b);
  flex-shrink: 0;
}

.status-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 18px;
  padding: 0 6px;
  border-radius: 9px;
  font-size: 11px;
  font-weight: 600;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
}

.status-badge.on {
  background: var(--success-light);
  color: var(--success-dark);
}

.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.loading-container,
.error-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--spacing-3);
  padding: 60px;
}

/* Section cards: same look as the "General Information" card of the detail pages */
.info-card {
  padding: var(--spacing-5);
  margin-bottom: var(--spacing-6);
}

.info-card h3 {
  font-size: var(--font-size-base);
  font-weight: 600;
  margin: 0 0 var(--spacing-4) 0;
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-light);
  padding-bottom: var(--spacing-3);
}

.card-desc {
  margin: calc(-1 * var(--spacing-2)) 0 var(--spacing-3);
  font-size: var(--font-size-xs);
  line-height: 1.6;
  color: var(--text-tertiary);
  white-space: pre-line;
}

.tab-body .card-desc {
  margin-top: 0;
}

.setting-list {
  display: flex;
  flex-direction: column;
}

.setting-row {
  display: grid;
  grid-template-columns: minmax(0, 320px) minmax(0, 1fr);
  gap: var(--spacing-6);
  align-items: center;
  padding: var(--spacing-3) 0;
}

.row-info {
  min-width: 0;
}

.row-label {
  display: inline-flex;
  align-items: center;
  gap: var(--spacing-2);
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
}

.row-desc {
  margin: 2px 0 0;
  font-size: var(--font-size-xs);
  line-height: 1.5;
  color: var(--text-tertiary);
}

.row-control {
  display: flex;
  align-items: center;
  min-width: 0;
}

.form-input {
  width: 100%;
  padding: 8px 12px;
  font-size: var(--font-size-sm);
}

.form-input.wide {
  max-width: 420px;
}

.narrow {
  width: 160px;
  max-width: 100%;
}

.setting-row.changed .form-input {
  border-color: var(--warning-color, #f59e0b);
}

.input-affix {
  position: relative;
}

.input-affix .form-input {
  padding-right: 44px;
}

.input-affix input[type='number'],
.plain-number {
  appearance: textfield;
  -moz-appearance: textfield;
}

.input-affix input[type='number']::-webkit-inner-spin-button,
.input-affix input[type='number']::-webkit-outer-spin-button,
.plain-number::-webkit-inner-spin-button,
.plain-number::-webkit-outer-spin-button {
  -webkit-appearance: none;
  margin: 0;
}

.affix {
  position: absolute;
  right: 12px;
  top: 50%;
  transform: translateY(-50%);
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
  pointer-events: none;
}

/* Toggle */
.toggle-row {
  display: inline-flex;
  align-items: center;
  min-height: 36px;
  cursor: pointer;
}

.custom-toggle {
  position: relative;
  display: inline-block;
  width: 40px;
  height: 22px;
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
  border-radius: 999px;
  transition: background 0.2s, border-color 0.2s;
}

.toggle-slider::before {
  content: '';
  position: absolute;
  width: 16px;
  height: 16px;
  left: 2px;
  top: 2px;
  background: white;
  border-radius: 50%;
  box-shadow: var(--shadow-sm);
  transition: transform 0.2s;
}

.custom-toggle input:checked + .toggle-slider {
  background: var(--primary-color);
  border-color: var(--primary-600);
}

.custom-toggle input:checked + .toggle-slider::before {
  transform: translateX(18px);
}

.custom-toggle input:focus-visible + .toggle-slider {
  box-shadow: 0 0 0 3px var(--primary-100);
}

.text-link {
  color: var(--primary-600);
  text-decoration: none;
}

.text-link:hover {
  text-decoration: underline;
}

/* Infrastructure: key-value rows like the detail pages */
.mode-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--spacing-3);
  padding: var(--spacing-2) 0 var(--spacing-4);
}

.mode-info {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--spacing-3);
  min-width: 0;
}

.mode-badge {
  background: var(--primary-50);
  color: var(--primary-700);
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
}

.mode-badge.mode-external_s3 {
  background: var(--success-light);
  color: var(--success-dark);
}

.mode-badge.mode-legacy {
  background: var(--warning-light, #fff7ed);
  color: var(--warning-dark, #b45309);
}

.mode-desc {
  font-size: var(--font-size-sm);
  color: var(--text-secondary);
}

.key-value-list {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-3);
}

.kv-item {
  display: flex;
  justify-content: space-between;
  gap: var(--spacing-4);
  font-size: var(--font-size-sm);
}

.kv-item .label {
  color: var(--text-secondary);
  flex-shrink: 0;
}

.kv-item .value {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-primary);
  font-weight: 500;
  text-align: right;
  overflow-wrap: anywhere;
}

.mono {
  font-family: var(--font-family-mono);
}

.status-text.ok {
  color: var(--success-dark);
}

.status-text.nok,
.text-error {
  color: var(--error-color);
}

@media (max-width: 720px) {
  .tab-header {
    padding: 0 var(--spacing-3);
  }

  .setting-row {
    grid-template-columns: minmax(0, 1fr);
    gap: var(--spacing-2);
  }

  .kv-item {
    flex-direction: column;
    gap: 2px;
  }

  .kv-item .value {
    text-align: left;
  }
}

.password-error {
  margin: var(--spacing-2) 0 0;
  font-size: var(--font-size-xs);
  color: var(--error, #ef4444);
}

/* Lets password managers fill in the password of the current account */
.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
  pointer-events: none;
}
</style>
