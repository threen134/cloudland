import { ref } from 'vue'

export function useCopyId(duration = 2000) {
    const copiedId = ref<string | null>(null)
    let timer: ReturnType<typeof setTimeout> | null = null

    function copyId(id: string) {
        navigator.clipboard.writeText(id)
        if (timer) clearTimeout(timer)
        copiedId.value = id
        timer = setTimeout(() => { copiedId.value = null }, duration)
    }

    function truncateId(id: string | undefined | null, len = 8): string {
        if (!id) return '-'
        return id.length > len ? id.slice(0, len) + '...' : id
    }

    return { copiedId, copyId, truncateId }
}
