import { onUnmounted, watch, type WatchSource } from 'vue'

/**
 * Calls fn every ms milliseconds while cond is true; stops when it turns false or the component unmounts.
 * For a list page that refreshes quietly while some of its rows change state on their own (being created or
 * deleted, tasks running), e.g. usePollWhile(anyChanging, () => load(true)).
 */
export function usePollWhile(cond: WatchSource<boolean>, fn: () => unknown, ms = 5000) {
    let timer: ReturnType<typeof setInterval> | null = null
    const stop = () => {
        if (timer) clearInterval(timer)
        timer = null
    }
    watch(
        cond,
        (on) => {
            if (on && !timer) {
                timer = setInterval(fn, ms)
            } else if (!on) {
                stop()
            }
        },
        { immediate: true }
    )
    onUnmounted(stop)
}
