import { useRouter } from 'vue-router'

/**
 * 详情页的返回。
 *
 * 各详情页原先两种写法并存：`router.back()`（11 个页面）和
 * `router.push({ name: '列表页' })`（3 个）。两种都有问题：
 * - 直接用 back()：从外部链接或新标签页打开详情页时，历史里没有上一页，
 *   点返回会退出应用
 * - 直接 push 列表页：正常从列表点进来时，返回会丢掉列表的滚动位置和筛选状态
 *
 * 这里取两者的好处：有本站历史就 back，没有就回退到列表页。
 */
export function useGoBack(fallbackRouteName: string) {
    const router = useRouter()

    return () => {
        // history.state.back 为空说明这是本次会话进入应用的第一页
        if (window.history.state?.back) {
            router.back()
        } else {
            router.push({ name: fallbackRouteName })
        }
    }
}
