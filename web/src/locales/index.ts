import { createI18n } from 'vue-i18n'
import en from './en'
import zh from './zh'

// Get saved language from localStorage or browser preference
const getSavedLanguage = (): string => {
    const saved = localStorage.getItem('ibm_cloud_china_language')
    if (saved && ['en', 'zh'].includes(saved)) {
        return saved
    }

    // Check browser language preference
    const browserLang = navigator.language.toLowerCase()
    if (browserLang.startsWith('zh')) {
        return 'zh'
    }

    return 'en'
}

const i18n = createI18n({
    legacy: false, // Use Composition API
    locale: getSavedLanguage(),
    fallbackLocale: 'en',
    messages: {
        en,
        zh,
    },
})

// Helper to save language preference
export const setLanguage = (lang: string) => {
    localStorage.setItem('ibm_cloud_china_language', lang)
    i18n.global.locale.value = lang as 'en' | 'zh'
}

export const getCurrentLanguage = () => {
    return i18n.global.locale.value
}

export default i18n
