<script setup lang="ts">
// 详情页的标签页导航。四个详情页各写了一套（class 分别叫 detail-tabs /
// tabs-nav-container / tab-bar），样式和行为都略有出入，这里统一。
//
// 用 v-model 绑定当前标签；图标传 lucide 组件。
import type { Component } from 'vue'

export interface Tab {
    id: string
    /** 已翻译的标签文案 */
    label: string
    icon?: Component
    /** 文案后的数量徽标（规则数、成员数这类） */
    count?: number
}

defineProps<{
    tabs: Tab[]
    modelValue: string
}>()

defineEmits<{ 'update:modelValue': [id: string] }>()
</script>

<template>
    <div class="detail-tabs" role="tablist">
        <button
            v-for="tab in tabs"
            :key="tab.id"
            type="button"
            role="tab"
            class="tab-btn"
            :class="{ active: modelValue === tab.id }"
            :aria-selected="modelValue === tab.id"
            @click="$emit('update:modelValue', tab.id)"
        >
            <component :is="tab.icon" v-if="tab.icon" :size="16" />
            {{ tab.label }}
            <span v-if="tab.count !== undefined" class="tab-count">{{ tab.count }}</span>
        </button>
    </div>
</template>

<style scoped>
/* The baseline is an inset shadow, not a border the tabs overlap with margin-bottom: -1px.
   With overflow-x: auto the other axis becomes auto too, so that 1px overlap made the bar
   vertically scrollable and Windows drew a scrollbar with up/down arrows at its right end.
   The active tab's own bottom border paints over the shadow. */
.detail-tabs {
    display: flex;
    gap: var(--spacing-1);
    box-shadow: inset 0 -1px 0 var(--border-light);
    margin-bottom: var(--spacing-5);
    overflow-x: auto;
    overflow-y: hidden;
}

.tab-btn {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    padding: var(--spacing-3) var(--spacing-4);
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    white-space: nowrap;
    cursor: pointer;
    transition: var(--transition-base);
}

.tab-btn:hover {
    color: var(--text-primary);
}

.tab-btn.active {
    color: var(--primary-color);
    border-bottom-color: var(--primary-color);
}

.tab-count {
    padding: 1px 6px;
    border-radius: var(--radius-full);
    background: var(--bg-tertiary);
    color: var(--text-tertiary);
    font-size: var(--font-size-tiny);
    font-weight: var(--font-weight-semibold);
}

.tab-btn.active .tab-count {
    background: var(--primary-light);
    color: var(--primary-700);
}
</style>
