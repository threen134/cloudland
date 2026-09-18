<script setup lang="ts">
// 分页条。原先 6 个列表页各写一份（页码省略号的写法都一样），这里统一。
import { computed } from 'vue'

const props = defineProps<{
    page: number
    pageSize: number
    total: number
}>()

const emit = defineEmits<{ 'update:page': [page: number] }>()

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
const from = computed(() => (props.page - 1) * props.pageSize + 1)
const to = computed(() => Math.min(props.page * props.pageSize, props.total))

// 首页、末页、当前页前后各一页；其余位置用省略号
const isVisible = (p: number) => p === 1 || p === totalPages.value || (p >= props.page - 1 && p <= props.page + 1)
const isEllipsis = (p: number) => p === props.page - 2 || p === props.page + 2

const goTo = (p: number) => {
    if (p >= 1 && p <= totalPages.value && p !== props.page) emit('update:page', p)
}
</script>

<template>
    <div v-if="totalPages > 1" class="pagination-bar">
        <span class="pagination-info">
            {{ $t('dashboard.pagination.showing', { from, to, total }) }}
        </span>
        <div class="pagination-controls">
            <button class="page-btn" :disabled="page <= 1" :aria-label="$t('actions.prev')" @click="goTo(page - 1)">
                &lsaquo;
            </button>
            <template v-for="p in totalPages" :key="p">
                <button
                    v-if="isVisible(p)"
                    class="page-btn"
                    :class="{ active: p === page }"
                    :aria-current="p === page ? 'page' : undefined"
                    @click="goTo(p)"
                >
                    {{ p }}
                </button>
                <span v-else-if="isEllipsis(p)" class="page-ellipsis">...</span>
            </template>
            <button
                class="page-btn"
                :disabled="page >= totalPages"
                :aria-label="$t('actions.next')"
                @click="goTo(page + 1)"
            >
                &rsaquo;
            </button>
        </div>
    </div>
</template>

<style scoped>
.pagination-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-4);
    padding: var(--spacing-3) var(--spacing-5);
    border-top: 1px solid var(--border-light);
    flex-wrap: wrap;
}

.pagination-info {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

.pagination-controls {
    display: flex;
    align-items: center;
    gap: var(--spacing-1);
}

.page-btn {
    min-width: 32px;
    height: 32px;
    padding: 0 var(--spacing-2);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    cursor: pointer;
    transition: var(--transition-fast);
}

.page-btn:hover:not(:disabled) {
    border-color: var(--primary-300);
    color: var(--primary-color);
}

.page-btn.active {
    background: var(--primary-color);
    border-color: var(--primary-color);
    color: var(--text-inverse);
}

.page-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.page-ellipsis {
    padding: 0 var(--spacing-1);
    color: var(--text-tertiary);
}
</style>
