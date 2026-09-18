import { ref } from 'vue'

/**
 * 复制到剪贴板，并在一段时间内标记"刚复制的是哪一项"（用于把图标切成对勾）。
 *
 * 列表页通常一行只复制 UUID，直接用复制的值当标记即可；详情页一页里有多个
 * 可复制字段（UUID、IP、MAC…），传 key 区分是哪一项。详情页原先各自写了一份
 * 同样的函数，统一到这里。
 */
export function useCopyId(duration = 2000) {
    const copiedId = ref<string | null>(null)
    let timer: ReturnType<typeof setTimeout> | null = null

    async function copyId(text: string, key?: string) {
        try {
            // 非 HTTPS 或浏览器不支持时 clipboard 是 undefined，不能让它抛到全局
            await navigator.clipboard?.writeText(text)
        } catch (err) {
            console.error('Failed to copy to clipboard:', err)
            return false
        }
        if (timer) clearTimeout(timer)
        copiedId.value = key ?? text
        timer = setTimeout(() => { copiedId.value = null }, duration)
        return true
    }

    function truncateId(id: string | undefined | null, len = 8): string {
        if (!id) return '-'
        return id.length > len ? id.slice(0, len) + '...' : id
    }

    return { copiedId, copyId, truncateId }
}
