import { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react'
import { getCookie, setCookie } from '../utils/cookies'

type TranslationMessages = Record<string, unknown>

// Load translations
const translations: Record<string, () => Promise<{ default: TranslationMessages }>> = {
  en: () => import('../locales/en.json') as Promise<{ default: TranslationMessages }>,
  tr: () => import('../locales/tr.json') as Promise<{ default: TranslationMessages }>,
}

const STORAGE_KEY = 'hermex-language'

interface I18nContextValue {
  locale: string
  changeLocale: (newLocale: string) => void
  t: (key: string, params?: Record<string, string>) => string
  loading: boolean
  supportedLocales: string[]
}

const I18nContext = createContext<I18nContextValue | null>(null)

// useI18nState holds the actual locale/messages state. It lives once in the
// provider so every consumer shares the same locale and a language change
// re-renders the whole app (a bare hook gave each component its own state, so
// switching languages never propagated).
function useI18nState(): I18nContextValue {
  const [locale, setLocale] = useState(() => {
    return getCookie(STORAGE_KEY) || navigator.language.split('-')[0] || 'en'
  })
  const [messages, setMessages] = useState<TranslationMessages | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const loadTranslations = async () => {
      setLoading(true)
      try {
        const loader = translations[locale] || translations.en
        const msgs = await loader()
        setMessages((msgs.default || msgs) as TranslationMessages)
      } catch (err) {
        console.error('Failed to load translations:', err)
        const fallback = await translations.en()
        setMessages((fallback.default || fallback) as TranslationMessages)
      }
      setLoading(false)
    }

    loadTranslations()
  }, [locale])

  const changeLocale = useCallback((newLocale: string) => {
    setLocale(newLocale)
    setCookie(STORAGE_KEY, newLocale)
    document.documentElement.lang = newLocale
  }, [])

  const t = useCallback(
    (key: string, params: Record<string, string> = {}): string => {
      if (!messages) return key

      const keys = key.split('.')
      let value: unknown = messages

      for (const k of keys) {
        value = (value as Record<string, unknown>)?.[k]
        if (value === undefined) return key
      }

      if (typeof value === 'string') {
        return value.replace(/\{\{(\w+)\}\}/g, (match, param) => {
          return params[param] !== undefined ? params[param] : match
        })
      }

      return key
    },
    [messages]
  )

  return { locale, changeLocale, t, loading, supportedLocales: Object.keys(translations) }
}

// I18nProvider supplies the shared i18n state to the whole admin app.
export function I18nProvider({ children }: { children: ReactNode }) {
  const value = useI18nState()
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

// useI18n reads the shared i18n context. Must be used within I18nProvider.
export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext)
  if (!ctx) {
    throw new Error('useI18n must be used within an I18nProvider')
  }
  return ctx
}

export default useI18n
