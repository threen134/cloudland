<script setup lang="ts">
// 详情页信息卡里的一行「标签 — 值」。
//
// 这类结构原先每个详情页各写一套，标签的 class 有 label / info-label /
// detail-label / form-label 四种，字号、颜色、左右布局也各不相同。统一成一个组件。
//
// 标签左、值右对齐；值为空时显示 '-'，不必每处都写 `|| '-'`。
defineProps<{
    label: string
    /** 值用等宽字体显示（UUID、IP、MAC 这类） */
    mono?: boolean
}>()
</script>

<template>
    <div class="info-row">
        <span class="info-label">
            <slot name="label">{{ label }}</slot>
        </span>
        <span class="info-value" :class="{ mono }">
            <slot>-</slot>
        </span>
    </div>
</template>

<style scoped>
.info-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-4);
    padding: var(--spacing-2) 0;
    min-height: 32px;
    border-bottom: 1px solid var(--border-light);
}

.info-row:last-child {
    border-bottom: none;
}

.info-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    flex-shrink: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

.info-value {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    min-width: 0;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    font-weight: var(--font-weight-medium);
    text-align: right;
    word-break: break-all;
}

.info-value.mono {
    font-family: var(--font-family-mono);
}
</style>
