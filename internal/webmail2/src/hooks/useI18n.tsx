import { createContext, useContext, useState, useEffect, useCallback, useRef, type ReactNode } from 'react'
import { toast } from 'sonner'
import { deleteCookie, getCookie, setCookie } from '../utils/cookies'
import api from '../utils/api'
import { SUPPORTED_LOCALES, resolveLocale } from '../utils/locale'

type TranslationMessages = Record<string, unknown>

// Load translations, one catalogue per SUPPORTED_LOCALES entry.
const translations: Record<string, () => Promise<{ default: TranslationMessages }>> = {
  en: () => import('../locales/en.json') as Promise<{ default: TranslationMessages }>,
  tr: () => import('../locales/tr.json') as Promise<{ default: TranslationMessages }>,
}

// LANGUAGE_COOKIE caches the user's language choice for the first render; the
// users record (shared with the admin panel) is where the choice is stored.
const LANGUAGE_COOKIE = 'hermex-language'

interface I18nContextValue {
  locale: string
  // preference is the stored choice: a supported language, or "" to follow the browser.
  preference: string
  // changeLocale applies and stores a choice; "" or "system" follows the browser.
  changeLocale: (newLocale: string) => void
  // applyStoredLocale applies the choice read from the users record without storing it again.
  applyStoredLocale: (stored: string) => void
  t: (key: string, params?: Record<string, string>) => string
  loading: boolean
  supportedLocales: string[]
}

const I18nContext = createContext<I18nContextValue | null>(null)

// usePreference holds the language choice and mirrors it to the cookie and <html lang>.
function usePreference() {
  const [preference, setPreference] = useState(() => {
    const c = getCookie(LANGUAGE_COOKIE)
    return c && SUPPORTED_LOCALES.includes(c) ? c : ''
  })
  const locale = resolveLocale(preference, navigator.language)
  const apply = useCallback((next: string) => {
    const choice = SUPPORTED_LOCALES.includes(next) ? next : ''
    setPreference(choice)
    if (choice) setCookie(LANGUAGE_COOKIE, choice)
    else deleteCookie(LANGUAGE_COOKIE)
    document.documentElement.lang = resolveLocale(choice, navigator.language)
  }, [])
  return { preference, locale, apply }
}

// useMessages loads the message catalogue of locale.
function useMessages(locale: string) {
  const [messages, setMessages] = useState<TranslationMessages | null>(null)
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    const loadTranslations = async () => {
      setLoading(true)
      try {
        const msgs = await (translations[locale] || translations.en)()
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
  return { messages, loading }
}

// translate looks key up in messages and fills its {{param}} placeholders.
function translate(messages: TranslationMessages | null, key: string, params: Record<string, string>): string {
  if (!messages) return key
  let value: unknown = messages
  for (const k of key.split('.')) {
    // Only the local cursor is reassigned: the loop reads nested keys of the
    // loaded locale and never writes to an object, so it cannot touch a prototype.
    // nosemgrep: javascript.lang.security.audit.prototype-pollution.prototype-pollution-loop.prototype-pollution-loop
    value = (value as Record<string, unknown>)?.[k]
    if (value === undefined) return key
  }
  if (typeof value !== 'string') return key
  return value.replace(/\{\{(\w+)\}\}/g, (match, param) => (params[param] !== undefined ? params[param] : match))
}

// useI18nState holds the actual locale/messages state. It lives once in the
// provider so every consumer shares the same locale and a language change
// re-renders the whole app (a bare hook gave each component its own state, so
// switching languages never propagated).
function useI18nState(): I18nContextValue {
  const { preference, locale, apply } = usePreference()
  const { messages, loading } = useMessages(locale)
  const t = useCallback((key: string, params: Record<string, string> = {}) => translate(messages, key, params), [messages])
  // The latest t and choice, for a failed save that answers after a re-render.
  const latest = useRef({ t, preference })
  useEffect(() => {
    latest.current = { t, preference }
  }, [t, preference])

  // changeLocale shows the choice at once and stores it; a failed save puts the
  // previous choice back and says so, so the page never shows an unsaved choice.
  const changeLocale = useCallback((newLocale: string) => {
    const prev = latest.current.preference
    const next = newLocale === 'system' ? '' : newLocale
    apply(next)
    api.setUserPrefs({ locale: SUPPORTED_LOCALES.includes(next) ? next : '' }).catch(() => {
      apply(prev)
      toast.error(latest.current.t('settings.settingSaveFailed'))
    })
  }, [apply])

  return {
    locale, preference, changeLocale, applyStoredLocale: apply, t, loading, supportedLocales: SUPPORTED_LOCALES,
  }
}

// I18nProvider supplies the shared i18n state to the whole app.
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
