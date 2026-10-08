<script setup lang="ts">
// 列表页顶部的工具条：左侧搜索框 + 可选筛选器，右侧操作按钮。
// 原先 20 多个列表页各写一遍同样的结构和 CSS（.page-header/.search-box/.header-actions），
// 这里统一一次。搜索框用 v-model:search 双向绑定。
//
// 页面通过插槽塞进来的元素带的是页面自己的 scoped 标记，本组件的样式对它们不生效，
// 所以筛选器和按钮区的排版规则写成 :deep()。
import { Search } from 'lucide-vue-next'

withDefaults(
    defineProps<{
        /** 搜索关键词，v-model:search */
        search?: string
        searchPlaceholder?: string
        /** 不需要搜索框时传 false */
        searchable?: boolean
    }>(),
    {
        search: '',
        searchable: true,
    }
)

defineEmits<{ 'update:search': [value: string] }>()
</script>

<template>
    <div class="page-header">
        <div class="search-wrapper">
            <div v-if="searchable" class="search-box">
                <Search :size="16" class="search-icon" />
                <input
                    type="text"
                    class="search-input"
                    :value="search"
                    :placeholder="searchPlaceholder ?? $t('actions.search') + '...'"
                    @input="$emit('update:search', ($event.target as HTMLInputElement).value)"
                />
            </div>
            <slot name="filters" />
        </div>
        <div class="header-actions">
            <slot name="actions" />
        </div>
    </div>
</template>

<style scoped>
.page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-4);
    flex-wrap: wrap;
}

.search-wrapper {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    flex: 1;
    min-width: 0;
}

.search-box {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    background: var(--bg-secondary);
    padding: 0 var(--spacing-3);
    height: 40px;
    border-radius: var(--radius-md);
    border: 1px solid var(--border-light);
    transition: var(--transition-base);
    flex: 1;
    max-width: 400px;
}

.search-box:focus-within {
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.search-icon {
    color: var(--text-tertiary);
    flex-shrink: 0;
}

.search-input {
    border: none;
    background: transparent;
    outline: none;
    width: 100%;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
}

.header-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    flex-shrink: 0;
}

/* 插槽内容带的是页面的 scoped 标记，只能用 :deep() 统一排版 */
.search-wrapper :deep(.filter-select),
.search-wrapper :deep(select) {
    height: 40px;
    padding: 0 var(--spacing-3);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: var(--bg-primary);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
}
</style>
