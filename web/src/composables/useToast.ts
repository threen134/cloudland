import { ref } from 'vue'

export interface Toast {
    id: number
    message: string
    type: 'success' | 'error' | 'warning' | 'info'
    duration: number
}

const toasts = ref<Toast[]>([])
let nextId = 0

function addToast(message: string, type: Toast['type'] = 'info', duration = 4000) {
    const id = nextId++
    toasts.value.push({ id, message, type, duration })
    setTimeout(() => removeToast(id), duration)
}

function removeToast(id: number) {
    toasts.value = toasts.value.filter(t => t.id !== id)
}

export function useToast() {
    return {
        toasts,
        removeToast,
        success: (msg: string) => addToast(msg, 'success'),
        error: (msg: string) => addToast(msg, 'error', 6000),
        warning: (msg: string) => addToast(msg, 'warning'),
        info: (msg: string) => addToast(msg, 'info'),
    }
}
