<script setup lang="ts">
// WireGuard client configuration text with copy and download buttons.
// Used by the client create modal (one-time display of the generated private key) and by the
// "configuration" action of an existing client (template without the private key).
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, Check, Download, AlertTriangle } from 'lucide-vue-next'
import { useCopyId } from '../../composables/useCopyId'

const props = defineProps<{
    config: string
    /** Base name of the downloaded file; ".conf" is appended */
    fileName: string
    /** The config carries a generated private key that is shown exactly once */
    oneTime?: boolean
    /** The config has a placeholder where the user's own private key goes */
    privateKeyMissing?: boolean
}>()

const { t } = useI18n()
const { copiedId, copyId } = useCopyId()

// WireGuard interface names are limited to 15 characters; keep the file name safe on every OS
const downloadName = computed(() => {
    const base = props.fileName.replace(/[^A-Za-z0-9_-]/g, '_').slice(0, 15) || 'wg0'
    return `${base}.conf`
})

const download = () => {
    const blob = new Blob([props.config], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = downloadName.value
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
}
</script>

<template>
    <div class="config-box">
        <div v-if="oneTime" class="config-warning">
            <AlertTriangle :size="16" />
            <span>{{ t('dashboard.vpnGateway.configOneTimeWarning') }}</span>
        </div>
        <div v-else-if="privateKeyMissing" class="config-note">
            {{ t('dashboard.vpnGateway.configPrivateKeyHint') }}
        </div>
        <textarea class="config-text" :value="config" readonly spellcheck="false" rows="12"></textarea>
        <div class="config-actions">
            <button type="button" class="btn btn-secondary btn-sm" @click="copyId(config, 'config')">
                <Check v-if="copiedId === 'config'" :size="14" class="copied-icon" />
                <Copy v-else :size="14" />
                {{ copiedId === 'config' ? t('messages.copied') : t('actions.copy') }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" @click="download">
                <Download :size="14" />
                {{ t('dashboard.vpnGateway.downloadConfig') }}
            </button>
        </div>
    </div>
</template>

<style scoped>
.config-box {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.config-warning {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-2);
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: var(--font-size-sm);
    line-height: 1.5;
}

.config-warning svg {
    flex-shrink: 0;
    margin-top: 2px;
}

.config-note {
    padding: var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--info-light);
    color: var(--info-dark);
    font-size: var(--font-size-sm);
    line-height: 1.5;
}

.config-text {
    width: 100%;
    padding: var(--spacing-3);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    background: var(--bg-secondary);
    color: var(--text-primary);
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    line-height: 1.6;
    resize: vertical;
    white-space: pre;
    overflow: auto;
}

.config-actions {
    display: flex;
    gap: var(--spacing-2);
}

.copied-icon {
    color: var(--success-color);
}
</style>
