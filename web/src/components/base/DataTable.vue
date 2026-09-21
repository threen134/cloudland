<script setup lang="ts" generic="T extends Record<string, any>">
// 列表页的表格：表头、加载态、空态、错误态、列排序都在这里实现一次。
// 原先 20 多个列表页各写一遍 `<tr v-if="loading">` + 空态文案，且只有一个页面提供了失败重试。
//
// 用法：
//   <DataTable :columns="cols" :rows="items" row-key="id" :loading="loading" :error="error" @retry="fetch">
//     <template #cell-name="{ row }">…</template>
//   </DataTable>
// 每列的内容用 #cell-<key> 插槽渲染；不提供插槽时直接取 row[key]。
import { computed, ref } from 'vue'
import { RefreshCw, ChevronDown, ChevronRight, Inbox } from 'lucide-vue-next'

// 泛型组件：行类型由调用方传进来的 rows 推断，#cell-* 插槽里的 row 就是 Flavor / Instance
// 这类具体接口类型，而不是 Record<string, any>。此前插槽里拿到的是 Record<string, any>，
// 传给形参是具体类型的函数会编译报错——但项目的 build 脚本用的是 tsc（不解析 .vue），
// 这些错误一直没暴露出来。
//
// Column 不跟着泛型化：它被 20 多个列表页按 `type Column` 直接引用，泛型化会连带改掉所有
// 引用点，而列定义里唯一用到行类型的只有 sortValue 一个回调。

export interface Column {
    key: string
    /** 表头文案（已翻译） */
    label: string
    sortable?: boolean
    align?: 'left' | 'center' | 'right'
    width?: string
    /**
     * 排序取值。字段名和显示值不一致时用它，例如规格的 CPU 在不同接口里
     * 可能叫 vcpus 也可能叫 cpu，直接按 key 取会排错。
     * 仅用于本地排序；服务端排序用 sortField
     */
    sortValue?: (row: Record<string, any>) => string | number | null | undefined
    /** 服务端排序时传给后端的列名，默认取 key（例如列 key 是 name、数据库列是 hostname 时要指定） */
    sortField?: string
}

const props = withDefaults(
    defineProps<{
        columns: Column[]
        rows: T[]
        /** 行的唯一标识：字段名，或从行数据算出 key 的函数 */
        rowKey?: string | ((row: T) => string)
        loading?: boolean
        /** 加载失败时的提示文案，传了就显示错误态和重试按钮 */
        error?: string
        /** 空态文案，默认用通用的"没有数据"；需要图标等更丰富的空态用 #empty 插槽 */
        emptyText?: string
        /**
         * 当前排序串（形如 name / -created_at）。传了就是服务端排序：
         * 点表头只发出事件、不在本地重排——分页后本地排序只能排当前页
         */
        order?: string
        /**
         * 行可展开：最左侧加一列折叠箭头，点行展开 #expanded 插槽（整行宽度的一格）。
         * 一次只展开一行——两页用到的都是「看这一条的明细」，同时展开多行反而难读
         */
        expandable?: boolean
        /**
         * 单元格里有下拉菜单、悬浮卡片这类要溢出表格显示的内容时传 true：
         * 容器改为 overflow: visible，并给单元格加定位上下文。
         * 代价是宽表格不再能横向滚动，所以只在确实需要时开
         */
        allowOverflow?: boolean
        /**
         * 按行内容附加的 class（如把已恢复的告警整行调淡）。
         * 返回值直接交给 :class，所以字符串、数组、对象都行
         */
        rowClass?: (row: T) => string | string[] | Record<string, boolean> | undefined
    }>(),
    {
        rowKey: 'id',
    }
)

const keyOf = (row: T) =>
    typeof props.rowKey === 'function' ? props.rowKey(row) : String(row[props.rowKey])

const emit = defineEmits<{ retry: []; 'update:order': [order: string]; expand: [key: string] }>()

// 展开态按行 key 记录，翻页/重新加载后行还在就保持展开
const expandedKey = ref<string | null>(null)
const isExpanded = (row: T) => expandedKey.value === keyOf(row)
const toggleExpand = (row: T) => {
    const key = keyOf(row)
    expandedKey.value = expandedKey.value === key ? null : key
    if (expandedKey.value) emit('expand', key)
}
// 展开行那一格要横跨所有列（含最左侧的箭头列）
const colspan = computed(() => props.columns.length + (props.expandable ? 1 : 0))

// 传了 order 就是服务端排序，本地不再重排
const serverSorted = computed(() => props.order !== undefined)
const fieldOf = (column: Column) => column.sortField ?? column.key

// 当前按哪一列排、升序还是降序。服务端模式下从 order 串解析（-field 表示降序）
const localSortKey = ref<string | null>(null)
const localSortDir = ref<'asc' | 'desc'>('asc')

const activeSortField = computed(() =>
    serverSorted.value ? (props.order || '').replace(/^-/, '') : localSortKey.value
)
const activeSortDir = computed<'asc' | 'desc'>(() =>
    serverSorted.value ? ((props.order || '').startsWith('-') ? 'desc' : 'asc') : localSortDir.value
)

const isSortedBy = (column: Column) => activeSortField.value === (serverSorted.value ? fieldOf(column) : column.key)

const toggleSort = (column: Column) => {
    if (!column.sortable) return
    if (serverSorted.value) {
        emit('update:order', fieldOf(column))
        return
    }
    if (localSortKey.value === column.key) {
        localSortDir.value = localSortDir.value === 'asc' ? 'desc' : 'asc'
    } else {
        localSortKey.value = column.key
        localSortDir.value = 'asc'
    }
}

// 本地排序：只用于没有分页、数据一次性全部加载的表格
const sortedRows = computed(() => {
    if (serverSorted.value || !localSortKey.value) return props.rows
    const column = props.columns.find((c) => c.key === localSortKey.value)
    const valueOf = column?.sortValue ?? ((row: Record<string, any>) => row[localSortKey.value as string])
    const dir = localSortDir.value === 'asc' ? 1 : -1
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
                    <th v-if="expandable" class="expand-col"></th>
                    <th
                        v-for="column in columns"
                        :key="column.key"
                        :style="column.width ? { width: column.width } : undefined"
                        :class="[column.align ? `text-${column.align}` : '', { sortable: column.sortable }]"
                        :aria-sort="
                            isSortedBy(column) ? (activeSortDir === 'asc' ? 'ascending' : 'descending') : undefined
                        "
                        @click="toggleSort(column)"
                    >
                        {{ column.label }}
                        <span v-if="column.sortable" class="sort-arrow">
                            {{ isSortedBy(column) ? (activeSortDir === 'asc' ? '▲' : '▼') : '' }}
                        </span>
                    </th>
                </tr>
            </thead>
            <tbody>
                <tr v-if="loading && rows.length === 0">
                    <td :colspan="colspan" class="table-state">
                        <span class="loading-spinner"></span>
                    </td>
                </tr>
                <tr v-else-if="error">
                    <td :colspan="colspan" class="table-state">
                        <p class="table-state-text">{{ error }}</p>
                        <button class="btn btn-secondary btn-sm" @click="$emit('retry')">
                            <RefreshCw :size="14" />
                            {{ $t('actions.retry') }}
                        </button>
                    </td>
                </tr>
                <tr v-else-if="rows.length === 0">
                    <td :colspan="colspan" class="table-state">
                        <!-- 默认空态：图标 + 文案 + 可选的行动按钮（#empty-action）。
                             页面想完全自定义就用 #empty 覆盖整块 -->
                        <slot name="empty">
                            <div class="table-empty">
                                <Inbox :size="40" class="table-empty-icon" />
                                <p class="table-state-text">{{ emptyText ?? $t('messages.noData') }}</p>
                                <slot name="empty-action"></slot>
                            </div>
                        </slot>
                    </td>
                </tr>
                <!-- v-for 与 v-else 不能写在同一个元素上，这里用 template 包一层 -->
                <template v-else>
                    <template v-for="row in sortedRows" :key="keyOf(row)">
                        <tr
                            :class="[
                                { expandable: expandable, expanded: expandable && isExpanded(row) },
                                rowClass?.(row),
                            ]"
                            @click="expandable && toggleExpand(row)"
                        >
                            <td v-if="expandable" class="expand-col">
                                <component :is="isExpanded(row) ? ChevronDown : ChevronRight" :size="14" />
                            </td>
                            <td
                                v-for="column in columns"
                                :key="column.key"
                                :class="column.align ? `text-${column.align}` : ''"
                            >
                                <slot :name="`cell-${column.key}`" :row="row">{{ row[column.key] ?? '-' }}</slot>
                            </td>
                        </tr>
                        <tr v-if="expandable && isExpanded(row)" class="expanded-row">
                            <td :colspan="colspan">
                                <slot name="expanded" :row="row" />
                            </td>
                        </tr>
                    </template>
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

/* 行展开：箭头列窄一点，展开行的内容自己铺满 */
.expand-col {
    width: 36px;
    text-align: center;
    color: var(--text-tertiary);
}

tr.expandable {
    cursor: pointer;
}

tr.expanded,
tr.expandable:hover {
    background: var(--bg-secondary);
}

.expanded-row > td {
    padding: 0;
    background: var(--bg-secondary);
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

.table-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--spacing-1);
}

.table-empty-icon {
    color: var(--gray-300);
    margin-bottom: var(--spacing-2);
}
</style>
