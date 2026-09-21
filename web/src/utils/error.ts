/**
 * 从接口错误里取出给用户看的文案。
 *
 * 各页面原先都写成 `catch (err: any)` 再手动展开 `err.response?.data?.xxx`，
 * 全站 125 处，而且取的字段还不统一——clapi 用 `error_message` 和 `error`，
 * cpgateway 用 `detail`，少数用 `message`。这里统一处理，顺带去掉 `any`：
 * 用 unknown + 类型收窄，写错字段编译期就能发现。
 *
 * detail 可能是字符串，也可能是 FastAPI 风格的数组（[{msg}]）或配额超限那种对象，
 * 后者交给 quotaErrorMessage 处理，这里只认字符串和数组。
 */
interface ApiErrorShape {
    response?: {
        data?: {
            error_message?: unknown
            error?: unknown
            detail?: unknown
            message?: unknown
        }
    }
    message?: unknown
}

const firstString = (...values: unknown[]): string | null => {
    for (const value of values) {
        if (typeof value === 'string' && value.trim()) return value
        // FastAPI 的校验错误：detail 是 [{ loc, msg, type }]
        if (Array.isArray(value)) {
            const msg = value.find((item) => typeof item?.msg === 'string')?.msg
            if (typeof msg === 'string' && msg.trim()) return msg
        }
    }
    return null
}

/** 取 HTTP 状态码；不是 axios 错误（网络中断、代码抛的普通 Error）时返回 undefined */
export function errorStatus(err: unknown): number | undefined {
    const status = (err as { response?: { status?: unknown } })?.response?.status
    return typeof status === 'number' ? status : undefined
}

/** 取接口返回的错误文案，取不到时返回 fallback（通常是 t('messages.error')） */
export function errorMessage(err: unknown, fallback: string): string {
    const data = (err as ApiErrorShape)?.response?.data
    return (
        firstString(data?.error_message, data?.error, data?.detail, data?.message, (err as ApiErrorShape)?.message) ??
        fallback
    )
}
