<script setup lang="ts">
// 分页条。原先 6 个列表页各写一份（页码省略号的写法都一样），这里统一。
import { computed } from 'vue'

const props = withDefaults(
    defineProps<{
        page: number
        pageSize: number
        total: number
        /** 可选的每页条数；不传则不显示选择器 */
        pageSizeOptions?: number[]
    }>(),
    { pageSizeOptions: () => [10, 20, 50, 100] }
)

const emit = defineEmits<{ 'update:page': [page: number]; 'update:pageSize': [size: number] }>()

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
    <!-- 只有一页时仍显示：用户可能想调大每页条数 -->
    <div v-if="total > 0" class="pagination-bar">
        <div class="pagination-left">
            <span class="pagination-info">
                {{ $t('dashboard.pagination.showing', { from, to, total }) }}
            </span>
            <label v-if="pageSizeOptions.length > 1" class="page-size">
                <span class="page-size-label">{{ $t('dashboard.pagination.perPage') }}</span>
                <select
                    class="page-size-select"
                    :value="pageSize"
                    @change="emit('update:pageSize', Number(($event.target as HTMLSelectElement).value))"
                >
                    <option v-for="size in pageSizeOptions" :key="size" :value="size">{{ size }}</option>
                </select>
            </label>
        </div>
        <div v-if="totalPages > 1" class="pagination-controls">
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

.pagination-left {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
    flex-wrap: wrap;
}

.pagination-info {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

.page-size {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
}

.page-size-label {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
}

.page-size-select {
    height: 28px;
    padding: 0 var(--spacing-2);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    cursor: pointer;
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
