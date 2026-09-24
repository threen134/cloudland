import { ref } from 'vue'
import { consumeRecentTraceId } from '../api/client'

export interface Toast {
    id: number
    message: string
    type: 'success' | 'error' | 'warning' | 'info'
    duration: number
    // 刚发生的 API 错误对应的 trace id，便于用户反馈问题
    traceId?: string
}

const toasts = ref<Toast[]>([])
let nextId = 0

function addToast(message: string, type: Toast['type'] = 'info', duration = 4000, traceId?: string) {
    const id = nextId++
    toasts.value.push({ id, message, type, duration, traceId })
    setTimeout(() => removeToast(id), duration)
}

function removeToast(id: number) {
    toasts.value = toasts.value.filter((t) => t.id !== id)
}

export function useToast() {
    return {
        toasts,
        removeToast,
        success: (msg: string) => addToast(msg, 'success'),
        error: (msg: string) => addToast(msg, 'error', 6000, consumeRecentTraceId()),
        warning: (msg: string) => addToast(msg, 'warning'),
        info: (msg: string) => addToast(msg, 'info'),
    }
}
