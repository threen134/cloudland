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
import i18n from '../locales'

interface ApiErrorShape {
    response?: {
        data?: {
            error_code?: unknown
            error_message?: unknown
            error?: unknown
            detail?: unknown
            message?: unknown
        }
    }
    message?: unknown
}

/**
 * clapi error codes (api/src/common/error_codes.go) shown in the user's language instead of the English
 * error_message. Only codes whose message says no more than the code itself are listed: a message with
 * specifics (a pool name, sizes, a limit, the members that are stuck or migrating, which connection still uses an
 * address) is more useful than a generic translation and stays as it is, and so does any code not listed here.
 * A code that also has messages with specifics is translated only when the message matches `only`.
 */
const ERROR_CODE_MESSAGES: Record<number, string | { key: string; only: RegExp }> = {
    111702: 'errorCodes.placementGroupExists',
    111905: 'errorCodes.flavorInUse',
    121012: 'errorCodes.volumeAttached',
    // "Only N addresses can be allocated" keeps its number
    131004: { key: 'errorCodes.insufficientAddress', only: /Not enough idle addresses|No idle addresses/ },
    131307: 'messages.vpcHasFloatingIPs',
    131308: 'errorCodes.vpcHasSubnets',
    132005: 'errorCodes.vpnGatewayExists',
    132025: 'errorCodes.vpnClientPoolExhausted',
    132033: 'errorCodes.vpcHasVpnGateway',
    133002: 'errorCodes.transitGatewayExists',
    // The VPC is the one just picked: its name adds nothing
    133012: 'errorCodes.vpcAttachedToTransitGateway',
    133015: 'errorCodes.tgwAttachmentBusy',
    // "Route table X is associated with N attachment(s)" keeps its specifics
    133023: { key: 'errorCodes.tgwDefaultRouteTable', only: /default route table can not be deleted/ },
    133042: 'errorCodes.tgwPropagationExists',
    133051: 'errorCodes.vpcHasTransitGateway',
    141007: 'errorCodes.defaultSecurityGroup',
    141008: 'errorCodes.securityGroupHasInterfaces',
    151001: 'errorCodes.imageInUse',
    161006: 'messages.sshKeyInUse',
    171011: 'errorCodes.zoneHasHypervisors',
}

/** The localized message for a known clapi error code, or null */
export function errorCodeMessage(err: unknown): string | null {
    const data = (err as ApiErrorShape)?.response?.data
    const entry = typeof data?.error_code === 'number' ? ERROR_CODE_MESSAGES[data.error_code] : undefined
    if (!entry) return null
    if (typeof entry !== 'string' && !entry.only.test(String(data?.error_message ?? ''))) return null
    const key = typeof entry === 'string' ? entry : entry.key
    if (!i18n.global.te(key)) return null
    return i18n.global.t(key)
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

/**
 * 取接口返回的错误文案，取不到时返回 fallback（通常是 t('messages.error')）。
 * A known clapi error code gives the localized message (see ERROR_CODE_MESSAGES)
 */
export function errorMessage(err: unknown, fallback: string): string {
    const data = (err as ApiErrorShape)?.response?.data
    return (
        errorCodeMessage(err) ??
        firstString(data?.error_message, data?.error, data?.detail, data?.message, (err as ApiErrorShape)?.message) ??
        fallback
    )
}
