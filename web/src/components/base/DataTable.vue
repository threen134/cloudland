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
    /**
     * 排序取值。字段名和显示值不一致时用它，例如规格的 CPU 在不同接口里
     * 可能叫 vcpus 也可能叫 cpu，直接按 key 取会排错
     */
    sortValue?: (row: Record<string, any>) => string | number | null | undefined
}

const props = withDefaults(
    defineProps<{
        columns: Column[]
        rows: Record<string, any>[]
        /** 行的唯一标识：字段名，或从行数据算出 key 的函数 */
        rowKey?: string | ((row: Record<string, any>) => string)
        loading?: boolean
        /** 加载失败时的提示文案，传了就显示错误态和重试按钮 */
        error?: string
        /** 空态文案，默认用通用的"没有数据"；需要图标等更丰富的空态用 #empty 插槽 */
        emptyText?: string
        /**
         * 单元格里有下拉菜单、悬浮卡片这类要溢出表格显示的内容时传 true：
         * 容器改为 overflow: visible，并给单元格加定位上下文。
         * 代价是宽表格不再能横向滚动，所以只在确实需要时开
         */
        allowOverflow?: boolean
    }>(),
    {
        rowKey: 'id',
    }
)

const keyOf = (row: Record<string, any>) =>
    typeof props.rowKey === 'function' ? props.rowKey(row) : String(row[props.rowKey])

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
    const column = props.columns.find((c) => c.key === sortKey.value)
    const valueOf = column?.sortValue ?? ((row: Record<string, any>) => row[sortKey.value as string])
    const dir = sortDir.value === 'asc' ? 1 : -1
    return [...props.rows].sort((a, b) => {
        const av = valueOf(a)
        const bv = valueOf(b)
        if (av === bv) return 0
        if (av === null || av === undefined) return 1
        if (bv === null || bv === undefined) return -1
        if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * dir
        return String(av).localeCompare(String(bv)) * dir
    })
})
</script>

<template>
    <div class="table-container" :class="{ 'allow-overflow': allowOverflow }">
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
                        <slot name="empty">
                            <p class="table-state-text">{{ emptyText ?? $t('messages.noData') }}</p>
                        </slot>
                    </td>
                </tr>
                <!-- v-for 与 v-else 不能写在同一个元素上，这里用 template 包一层 -->
                <template v-else>
                    <tr v-for="row in sortedRows" :key="keyOf(row)">
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

/* 下拉菜单、悬浮卡片要能溢出表格显示；单元格同时需要成为定位上下文，
   否则插槽里那些 position: absolute 的浮层会以更外层的元素定位 */
.table-container.allow-overflow {
    overflow: visible;
}

.allow-overflow td {
    position: relative;
}

/* 全局 index.css 里的 `.data-table th { text-align: left }` 优先级高于 .text-right
   这类工具类，不加这几条的话列的 align 对表头完全不生效，会出现
   "表头在左、单元格内容在右" 的错位 */
.data-table th.text-left {
    text-align: left;
}

.data-table th.text-center {
    text-align: center;
}

.data-table th.text-right {
    text-align: right;
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
