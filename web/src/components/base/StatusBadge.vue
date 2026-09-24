<script setup lang="ts">
// 统一的状态标签。原先各页面自己写 .status-badge，圆角有 4/9/12px 三种、
// 字重 500/600 两种、是否大写也不一致，这里固定成一套。
//
// 颜色由 status 决定（见 utils/status.ts），也可以用 variant 直接指定。
import { computed } from 'vue'
import { statusVariant, type StatusVariant } from '../../utils/status'

const props = defineProps<{
    /** 后端返回的状态串 */
    status?: string | null
    /** 覆盖按状态推导出的语义 */
    variant?: StatusVariant
    /**
     * Translated text. Required on purpose: falling back to the raw status showed backend
     * strings such as "available" untranslated, and vue-tsc now catches a missing label
     */
    label: string
}>()

const variantClass = computed(() => `status-${props.variant ?? statusVariant(props.status)}`)
const text = computed(() => props.label || '-')
</script>

<template>
    <span class="status-badge" :class="variantClass">
        <span class="status-dot" aria-hidden="true"></span>
        {{ text }}
    </span>
</template>

<style scoped>
.status-badge {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    padding: 2px 10px;
    border-radius: var(--radius-full);
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-medium);
    line-height: 1.6;
    white-space: nowrap;
}

.status-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: currentColor;
    flex-shrink: 0;
}

.status-success {
    background: var(--success-light);
    color: var(--success-dark);
}

.status-neutral {
    background: var(--gray-100);
    color: var(--gray-600);
}

.status-warning {
    background: var(--warning-light);
    color: var(--warning-dark);
}

.status-error {
    background: var(--error-light);
    color: var(--error-dark);
}

.status-pending {
    background: var(--info-light);
    color: var(--info-dark);
}
</style>
