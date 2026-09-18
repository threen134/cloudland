import { ref, computed, watch, onUnmounted, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'

/**
 * 列表页的服务端分页 + 搜索。
 *
 * 此前多数列表页是一次性把全部数据拉回来、在前端过滤，数据量大就会慢；少数做了
 * 分页的页面又各自抄了一份页码、防抖、竞态处理。这里统一一次。
 *
 * 关键点：**分页和搜索必须都在服务端做**。只分页、搜索仍在前端过滤的话，
 * 用户搜到的只是当前页里的内容，比不能搜还糟糕。
 */
export interface ListQueryParams {
    offset: number
    limit: number
    /** 搜索关键词，空串表示不过滤 */
    query: string
}

export interface ListQueryResult<T> {
    items: T[]
    total: number
}

export function useListQuery<T>(
    fetcher: (params: ListQueryParams) => Promise<ListQueryResult<T>>,
    options: {
        pageSize?: number
        /** 搜索输入防抖，毫秒 */
        debounce?: number
        /** 依赖变化时回到第一页重新加载（如切换区域、切换筛选条件） */
        watchSources?: Ref<unknown>[]
    } = {}
) {
    const { t } = useI18n()
    const pageSize = ref(options.pageSize ?? 20)
    const debounceMs = options.debounce ?? 400

    const items = ref<T[]>([]) as Ref<T[]>
    const total = ref(0)
    const page = ref(1)
    const loading = ref(false)
    const error = ref('')
    const search = ref('')

    const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))

    // 快速翻页或快速输入时，旧请求可能后返回并覆盖新结果，用递增的代号丢弃过期响应
    let generation = 0
    let searchTimer: ReturnType<typeof setTimeout> | null = null

    /**
     * 加载当前页。
     *
     * silent：轮询刷新用。不显示加载态，失败时也保留已有数据（只打日志）——
     * 后台每几秒刷一次的场景下，一次网络抖动不该把用户正在看的列表清空。
     */
    const load = async (silent = false) => {
        const current = ++generation
        if (!silent) {
            loading.value = true
            error.value = ''
        }
        try {
            const result = await fetcher({
                offset: (page.value - 1) * pageSize.value,
                limit: pageSize.value,
                query: search.value.trim(),
            })
            if (current !== generation) return
            items.value = result.items
            total.value = result.total
            // 当前页被删空了（例如删掉了本页最后一条），退回到最后一页
            if (items.value.length === 0 && page.value > 1) {
                page.value = totalPages.value
                await load()
            }
        } catch (err) {
            if (current !== generation) return
            console.error('Failed to load list:', err)
            if (!silent) {
                items.value = []
                total.value = 0
                error.value = t('messages.error')
            }
        } finally {
            if (current === generation && !silent) loading.value = false
        }
    }

    /** 回到第一页重新加载：筛选条件变化、新建资源之后用它 */
    const reload = () => {
        page.value = 1
        return load()
    }

    // 不能直接写 watch(page, load)：watch 会把新旧值当参数传进去，
    // 新页码会被当成 load 的 silent 参数
    watch(page, () => load())

    // 改每页条数后当前页码通常已经越界，回到第一页
    watch(pageSize, () => reload())

    watch(search, () => {
        if (searchTimer) clearTimeout(searchTimer)
        searchTimer = setTimeout(() => {
            // 已经在第一页时 watch(page) 不会触发，需要自己调
            if (page.value === 1) load()
            else page.value = 1
        }, debounceMs)
    })

    for (const source of options.watchSources ?? []) {
        watch(source, () => reload())
    }

    onUnmounted(() => {
        if (searchTimer) clearTimeout(searchTimer)
        // 让已经在飞的请求作废，避免组件卸载后还写状态
        generation++
    })

    return { items, total, page, pageSize, totalPages, loading, error, search, load, reload }
}
