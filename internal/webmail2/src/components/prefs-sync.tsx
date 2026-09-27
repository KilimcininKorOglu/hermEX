import { useEffect, useRef } from "react"
import { useAuth } from "@/contexts/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import { isTheme, useTheme } from "@/components/theme-provider"
import api, { type AppearanceSettings, type UserPrefs } from "@/utils/api"
import { getCookie, setCookie } from "@/utils/cookies"
import { describeError } from "@/utils/errorlog"
import { SUPPORTED_LOCALES } from "@/utils/locale"
import { setShortcutMode, type ShortcutMode } from "@/utils/shortcutMode"
import { applyIconSet } from "@/utils/iconSet"
import { setMailColumns } from "@/utils/mailListColumns"
import { applyUnreadBorder } from "@/utils/displayPrefs"

// WELCOME_COOKIE caches a dismissed welcome banner; the users record stores it.
export const WELCOME_COOKIE = "hermex-welcome-dismissed"

// mirrorAppearance applies the appearance record's app-wide parts: the shortcut
// mode, the icon set, the message-list columns and the unread border.
function mirrorAppearance(s: AppearanceSettings) {
  if (s.shortcutMode) setShortcutMode(s.shortcutMode as ShortcutMode)
  if (s.iconSet) applyIconSet(s.iconSet)
  if (s.mailListColumns) setMailColumns(s.mailListColumns)
  applyUnreadBorder(s.unreadBorder)
}

// StoredPrefs are the preferences /auth/me read from the users record.
export interface StoredPrefs {
  theme?: string
  locale?: string
  showWelcomeBanner?: boolean
}

// BrowserChoices are the choices this browser's cookies cached before the users
// record stored them.
export interface BrowserChoices {
  theme: string | null
  locale: string | null
  bannerDismissed: boolean
}

// readBrowserChoices reads the cookies the preferences were cached in.
function readBrowserChoices(): BrowserChoices {
  return {
    theme: getCookie("webmail-theme"),
    locale: getCookie("hermex-language"),
    bannerDismissed: getCookie(WELCOME_COOKIE) === "1",
  }
}

// adoptions lists the choices this browser made before the users record stored
// them: the theme from the old appearance record or the cookie, the language and
// a dismissed banner from their cookies. The record wins wherever it holds one.
export function adoptions(stored: StoredPrefs, browser: BrowserChoices, appearanceTheme: string | undefined): Partial<UserPrefs> {
  const out: Partial<UserPrefs> = {}
  const oldTheme = isTheme(appearanceTheme) ? appearanceTheme : browser.theme
  if (!isTheme(stored.theme) && isTheme(oldTheme)) out.theme = oldTheme
  if (!stored.locale && browser.locale && SUPPORTED_LOCALES.includes(browser.locale)) out.locale = browser.locale
  if (stored.showWelcomeBanner !== false && browser.bannerDismissed) out.show_welcome_banner = false
  return out
}

// loadAppearance reads the appearance record and applies its app-wide parts,
// reporting a failed read and answering null for it.
function loadAppearance(): Promise<AppearanceSettings | null> {
  return api.getAppearanceSettings().then(
    (s) => {
      mirrorAppearance(s)
      return s
    },
    (err) => {
      console.error("appearance settings:", describeError(err))
      return null
    },
  )
}

// PrefsSync applies the signed-in user's stored preferences once per sign-in:
// the theme and language from the users record the admin panel shares, and the
// app-wide parts of the appearance record. A choice this browser made before the
// record stored it is written to the record once.
export function PrefsSync() {
  const { user, isAuthenticated, updatePrefs } = useAuth()
  const { applyStoredTheme } = useTheme()
  const { applyStoredLocale } = useI18n()
  const synced = useRef<string | null>(null)

  useEffect(() => {
    if (!isAuthenticated) synced.current = null
    if (!isAuthenticated || !user?.email || user.secondFactorRequired || synced.current === user.email) return
    synced.current = user.email
    const stored: StoredPrefs = { theme: user.theme, locale: user.locale, showWelcomeBanner: user.showWelcomeBanner }
    // Read the cookies before the stored values overwrite them.
    const browser = readBrowserChoices()
    if (isTheme(stored.theme)) applyStoredTheme(stored.theme)
    if (stored.locale || !adoptions(stored, browser, undefined).locale) applyStoredLocale(stored.locale ?? "")
    if (stored.showWelcomeBanner === false) setCookie(WELCOME_COOKIE, "1")
    loadAppearance()
      .then((s) => {
        const adopt = adoptions(stored, browser, s?.theme)
        if (Object.keys(adopt).length === 0) return
        return api.setUserPrefs(adopt).then((p) => {
          if (isTheme(p.theme)) applyStoredTheme(p.theme)
          applyStoredLocale(p.locale)
          updatePrefs({ theme: p.theme, locale: p.locale, showWelcomeBanner: p.show_welcome_banner })
        })
      })
      .catch((err) => console.error("adopting the stored preferences:", describeError(err)))
  }, [isAuthenticated, user, applyStoredTheme, applyStoredLocale, updatePrefs])

  return null
}
