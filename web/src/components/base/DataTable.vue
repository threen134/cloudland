<script setup lang="ts">
// 列表页的表格：表头、加载态、空态、错误态、列排序都在这里实现一次。
// 原先 20 多个列表页各写一遍 `<tr v-if="loading">` + 空态文案，且只有一个页面提供了失败重试。
//
// 用法：
//   <DataTable :columns="cols" :rows="items" row-key="id" :loading="loading" :error="error" @retry="fetch">
//     <template #cell-name="{ row }">…</template>
//   </DataTable>
// 每列的内容用 #cell-<key> 插槽渲染；不提供插槽时直接取 row[key]。
import { computed, ref } from 'vue'
import { RefreshCw } from 'lucide-vue-next'

export interface Column {
    key: string
    /** 表头文案（已翻译） */
    label: string
    sortable?: boolean
    align?: 'left' | 'center' | 'right'
    width?: string
}

const props = withDefaults(
    defineProps<{
        columns: Column[]
        rows: Record<string, any>[]
        rowKey?: string
        loading?: boolean
        /** 加载失败时的提示文案，传了就显示错误态和重试按钮 */
        error?: string
        /** 空态文案，默认用通用的"没有数据" */
        emptyText?: string
    }>(),
    {
        rowKey: 'id',
    }
)

defineEmits<{ retry: [] }>()

const sortKey = ref<string | null>(null)
const sortDir = ref<'asc' | 'desc'>('asc')

const toggleSort = (column: Column) => {
    if (!column.sortable) return
    if (sortKey.value === column.key) {
        sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
    } else {
        sortKey.value = column.key
        sortDir.value = 'asc'
    }
}

// 排序在前端做：这些列表页的数据本来就是整页加载的
const sortedRows = computed(() => {
    if (!sortKey.value) return props.rows
    const key = sortKey.value
    const dir = sortDir.value === 'asc' ? 1 : -1
    return [...props.rows].sort((a, b) => {
        const av = a[key]
        const bv = b[key]
        if (av === bv) return 0
        if (av === null || av === undefined) return 1
        if (bv === null || bv === undefined) return -1
        if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * dir
        return String(av).localeCompare(String(bv)) * dir
    })
})
</script>

<template>
    <div class="table-container">
        <table class="data-table">
            <thead>
                <tr>
                    <th
                        v-for="column in columns"
                        :key="column.key"
                        :style="column.width ? { width: column.width } : undefined"
                        :class="[column.align ? `text-${column.align}` : '', { sortable: column.sortable }]"
                        :aria-sort="
                            sortKey === column.key ? (sortDir === 'asc' ? 'ascending' : 'descending') : undefined
                        "
                        @click="toggleSort(column)"
                    >
                        {{ column.label }}
                        <span v-if="column.sortable" class="sort-arrow">
                            {{ sortKey === column.key ? (sortDir === 'asc' ? '▲' : '▼') : '' }}
                        </span>
                    </th>
                </tr>
            </thead>
            <tbody>
                <tr v-if="loading && rows.length === 0">
                    <td :colspan="columns.length" class="table-state">
                        <span class="loading-spinner"></span>
                    </td>
                </tr>
                <tr v-else-if="error">
                    <td :colspan="columns.length" class="table-state">
                        <p class="table-state-text">{{ error }}</p>
                        <button class="btn btn-secondary btn-sm" @click="$emit('retry')">
                            <RefreshCw :size="14" />
                            {{ $t('actions.retry') }}
                        </button>
                    </td>
                </tr>
                <tr v-else-if="rows.length === 0">
                    <td :colspan="columns.length" class="table-state">
                        <p class="table-state-text">{{ emptyText ?? $t('messages.noData') }}</p>
                    </td>
                </tr>
                <!-- v-for 与 v-else 不能写在同一个元素上，这里用 template 包一层 -->
                <template v-else>
                    <tr v-for="row in sortedRows" :key="String(row[rowKey])">
                        <td
                            v-for="column in columns"
                            :key="column.key"
                            :class="column.align ? `text-${column.align}` : ''"
                        >
                            <slot :name="`cell-${column.key}`" :row="row">{{ row[column.key] ?? '-' }}</slot>
                        </td>
                    </tr>
                </template>
            </tbody>
        </table>
        <slot name="footer" />
    </div>
</template>

<style scoped>
.table-container {
    background: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    overflow-x: auto;
}

th.sortable {
    cursor: pointer;
    user-select: none;
}

th.sortable:hover {
    color: var(--text-secondary);
}

.sort-arrow {
    font-size: 10px;
    margin-left: var(--spacing-1);
}

.table-state {
    text-align: center;
    padding: var(--spacing-10) var(--spacing-5);
}

.table-state-text {
    margin: 0 0 var(--spacing-3);
    color: var(--text-tertiary);
    font-size: var(--font-size-sm);
}
</style>
