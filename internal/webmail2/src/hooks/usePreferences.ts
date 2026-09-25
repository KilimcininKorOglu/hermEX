import { useEffect, useState } from "react"
import { toast } from "sonner"
import { useI18n } from "@/hooks/useI18n"
import api from "@/utils/api"

// DEFAULT_PREFERENCES are the switches the page shows until the stored ones are
// read.
const DEFAULT_PREFERENCES = {
  // Notifications
  emailNotifications: true,
  browserNotifications: false,
  soundNotifications: true,
  desktopNotifications: true,
  // Email
  autoSaveDraft: true,
  readReceipts: false,
  deliveryReceipts: true,
  // Privacy
  showOnlineStatus: false,
  allowReadReceipts: true,
  // Composition
  richTextMode: true,
  autoCorrect: true,
  omitOriginalOnReply: false,
  spellCheck: true,
}

export type PreferenceKey = keyof typeof DEFAULT_PREFERENCES

// usePreferences owns the preference switches. A save writes the whole record,
// so a switch saves only after the stored record was read.
export function usePreferences() {
  const { t } = useI18n()
  const [prefs, setPrefs] = useState(DEFAULT_PREFERENCES)
  const [loaded, setLoaded] = useState(false)

  // Load persisted preferences on mount and merge over the defaults.
  useEffect(() => {
    let cancelled = false
    api.getPreferences()
      .then((res) => {
        if (cancelled) return
        if (res.preferences) setPrefs((prev) => ({ ...prev, ...res.preferences }))
        setLoaded(true)
      })
      .catch(() => {
        // keep defaults
      })
    return () => {
      cancelled = true
    }
  }, [])

  const toggle = async (key: PreferenceKey) => {
    if (!loaded) {
      toast.error(t("settings.notLoaded"))
      return
    }
    const prev = prefs
    const next = { ...prefs, [key]: !prefs[key] }
    setPrefs(next)
    try {
      await api.setPreferences(next)
      toast.success(t("settings.settingUpdated"))
    } catch (err) {
      console.error("Failed to save setting:", err)
      toast.error(t("settings.settingSaveFailed"))
      setPrefs(prev)
    }
  }

  return { prefs, toggle }
}

export type Preferences = ReturnType<typeof usePreferences>
