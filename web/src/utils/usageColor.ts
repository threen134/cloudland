/**
 * 用量进度条的颜色：正常用主色，偏高转警告色，接近满转错误色。
 *
 * 概览页和云服务器列表页各写过一套，云服务器列表那套还给内存用了紫色
 * （`--accent-purple`），而紫色在产品语义里不表示任何东西。统一到这里。
 *
 * 返回的是 `var(--token)`，只能用在 CSS（含 `:style` 绑定）里；
 * canvas / chart.js 那类要真实颜色字符串的场合用 `utils/cssVar.ts`。
 */
export const usageColor = (percent: number): string => {
    if (percent > 90) return 'var(--error-color)'
    if (percent > 75) return 'var(--warning-color)'
    return 'var(--primary-color)'
}
