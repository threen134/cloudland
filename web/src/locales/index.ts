import { STORAGE_KEYS } from '../utils/storage'
import { createI18n } from 'vue-i18n'
import en from './en'
import zh from './zh'
import zhTW from './zh-TW'

export type Language = 'en' | 'zh' | 'zh-TW'

export const SUPPORTED_LANGUAGES: Language[] = ['en', 'zh', 'zh-TW']

// 语言切换菜单的显示名（languages.* 文案键）
export const LANGUAGE_LABEL_KEYS: Record<Language, string> = {
    en: 'languages.en',
    zh: 'languages.zh_hans',
    'zh-TW': 'languages.zh_hant',
}

const STORAGE_KEY = STORAGE_KEYS.language

// 浏览器语言标签 → 支持的语言；繁体：zh-TW / zh-HK / zh-MO / zh-Hant*，其余 zh-* 为简体
const matchBrowserLanguage = (tag: string): Language | null => {
    const lang = tag.toLowerCase()
    if (lang === 'zh' || lang.startsWith('zh-')) {
        if (lang.includes('hant') || /^zh-(tw|hk|mo)\b/.test(lang)) {
            return 'zh-TW'
        }
        return 'zh'
    }
    if (lang === 'en' || lang.startsWith('en-')) {
        return 'en'
    }
    return null
}

const detectBrowserLanguage = (): Language => {
    const tags = navigator.languages?.length ? navigator.languages : [navigator.language]
    for (const tag of tags) {
        const matched = tag && matchBrowserLanguage(tag)
        if (matched) {
            return matched
        }
    }
    return 'en'
}

// 用户手动选择过的语言优先，否则跟随浏览器
const getSavedLanguage = (): Language => {
    try {
        const saved = localStorage.getItem(STORAGE_KEY)
        if (saved && (SUPPORTED_LANGUAGES as string[]).includes(saved)) {
            return saved as Language
        }
    } catch {
        // localStorage 不可用时跟随浏览器
    }
    return detectBrowserLanguage()
}

const i18n = createI18n({
    legacy: false, // Use Composition API
    locale: getSavedLanguage(),
    fallbackLocale: 'en',
    messages: {
        en,
        zh,
        'zh-TW': zhTW,
    },
})

const applyDocumentLang = (lang: Language) => {
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : lang
}

applyDocumentLang(i18n.global.locale.value as Language)

// Helper to save language preference
export const setLanguage = (lang: Language) => {
    try {
        localStorage.setItem(STORAGE_KEY, lang)
    } catch {
        // ignore
    }
    i18n.global.locale.value = lang
    applyDocumentLang(lang)
}

export const getCurrentLanguage = (): Language => {
    return i18n.global.locale.value as Language
}

export const isChinese = (lang: string) => lang === 'zh' || lang === 'zh-TW'

export default i18n
