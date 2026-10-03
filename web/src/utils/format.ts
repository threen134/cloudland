// 展示用的格式化函数。
//
// 这些函数原先在十几个页面里各自实现，结果同一个值在不同页面显示不一样
// （2048MB 在节点详情是 "2.0 GB"、在云服务器列表是 "2 GB"），单位进制也不统一。
// 统一约定：
//   - 容量一律按 1024 进制（虚拟机内存、磁盘、镜像大小在后端都是二进制单位）
//   - 整数不显示小数位，有小数时保留一位（2 GB、1.5 GB）
//   - 空值统一显示 '-'，由调用方决定是否需要别的占位符
// 单位文案在三个语言包里完全相同（B/KB/MB/GB/TB），所以这里直接用字面量，不走 i18n。

import { getCurrentLanguage } from '../locales'

const PLACEHOLDER = '-'

/** 整数去掉小数位，非整数保留一位：2 → "2"，1.53 → "1.5" */
const trim = (value: number): string => {
    const rounded = Math.round(value * 10) / 10
    return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1)
}

/** 字节数 → 带单位的字符串，如 3.5 GB */
export const formatBytes = (bytes?: number | null): string => {
    if (bytes === undefined || bytes === null || bytes <= 0) return PLACEHOLDER
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    let value = bytes
    let i = 0
    while (value >= 1024 && i < units.length - 1) {
        value /= 1024
        i++
    }
    // 字节数本身不显示小数
    return `${i === 0 ? Math.round(value) : trim(value)} ${units[i]}`
}

/** 内存（MB）→ 带单位的字符串，如 2 GB、512 MB */
export const formatMemory = (mb?: number | null): string => {
    if (mb === undefined || mb === null || mb <= 0) return PLACEHOLDER
    if (mb >= 1024) return `${trim(mb / 1024)} GB`
    return `${Math.round(mb)} MB`
}

/** 磁盘容量（GB）→ 带单位的字符串，如 2 TB、50 GB */
export const formatDisk = (gb?: number | null): string => {
    if (gb === undefined || gb === null || gb <= 0) return PLACEHOLDER
    if (gb >= 1024) return `${trim(gb / 1024)} TB`
    return `${trim(gb)} GB`
}

/** 时间戳字符串 → 按当前界面语言显示的本地时间 */
export const formatDateTime = (value?: string | number | Date | null): string => {
    if (!value) return PLACEHOLDER
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return PLACEHOLDER
    return date.toLocaleString(getCurrentLanguage())
}

/**
 * clapi timestamp ("YYYY-MM-DD HH:mm:ss.ffffff") cut to the minute, for list columns.
 * Pair it with class="cell-time" and put the full value in the title
 */
export const formatToMinute = (value?: string | null): string => (value ? value.slice(0, 16) : PLACEHOLDER)

/** 时间戳字符串 → 只取日期部分 */
export const formatDate = (value?: string | number | Date | null): string => {
    if (!value) return PLACEHOLDER
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return PLACEHOLDER
    return date.toLocaleDateString(getCurrentLanguage())
}
