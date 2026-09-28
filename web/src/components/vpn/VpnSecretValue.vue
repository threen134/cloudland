<script setup lang="ts">
// A stored key (PSK, BGP password) shown masked, with show / hide and copy. The value only reaches the
// page for members with write permission on the gateway; callers fall back to "set / not set" otherwise.
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, EyeOff, Copy, Check } from 'lucide-vue-next'
import { useCopyId } from '../../composables/useCopyId'

defineProps<{ value: string }>()

const { t } = useI18n()
const visible = ref(false)
const { copiedId, copyId } = useCopyId()
</script>

<template>
    <span class="secret-value">
        <span class="mono secret-text">{{ visible ? value : '••••••••' }}</span>
        <button
            type="button"
            class="secret-btn"
            :title="visible ? t('dashboard.vpnGateway.hideSecret') : t('dashboard.vpnGateway.showSecret')"
            @click="visible = !visible"
        >
            <EyeOff v-if="visible" :size="14" />
            <Eye v-else :size="14" />
        </button>
        <button type="button" class="secret-btn" :title="t('actions.copy')" @click="copyId(value, 'secret')">
            <Check v-if="copiedId === 'secret'" :size="14" class="copied-icon" />
            <Copy v-else :size="14" />
        </button>
    </span>
</template>

<style scoped>
.secret-value {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
    max-width: 100%;
}
.secret-text {
    word-break: break-all;
}
.secret-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 24px;
    height: 24px;
    padding: 0;
    border: none;
    border-radius: 4px;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
}
.secret-btn:hover {
    background: var(--bg-secondary);
    color: var(--text-primary);
}
.copied-icon {
    color: var(--success-color);
}
</style>
