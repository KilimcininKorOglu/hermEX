import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { useTheme } from "@/components/theme-provider"
import { useI18n } from "@/hooks/useI18n"
import api, { type CalendarSettings } from "@/utils/api"
import { setShortcutMode, type ShortcutMode } from "@/utils/shortcutMode"
import { applyIconSet } from "@/utils/iconSet"
import { setMailColumns } from "@/utils/mailListColumns"
import { applyUnreadBorder } from "@/utils/displayPrefs"
import { setInboxNavigation, type InboxNavMode } from "@/utils/inboxNavigation"
import {
  appearanceFromServer,
  calendarFromServer,
  DEFAULT_CALENDAR,
  defaultAppearance,
  type AppearanceRecord,
} from "@/utils/settingsRecords"

// CalendarToastKeys are the i18n keys a calendar change reports with; a change
// without a saved key reports only a failure.
export interface CalendarToastKeys {
  saved?: string
  failed: string
}

type Theme = "light" | "dark" | "system"

// mirrorAppearance applies a record outside React at once: the cookie mirrors
// the key hooks and the inbox read, and the classes on <html>.
function mirrorAppearance(rec: AppearanceRecord) {
  // Mirror the shortcut mode to its cookie so the key hooks pick it up live.
  setShortcutMode(rec.shortcutMode as ShortcutMode)
  // Apply the icon set to the document root immediately for a live preview.
  applyIconSet(rec.iconSet)
  // Mirror the message-list columns to their cookie so the inbox re-reads live.
  setMailColumns(rec.mailListColumns)
  // Reflect the unread-border toggle onto <html> for a live preview.
  applyUnreadBorder(rec.unreadBorder)
  // Mirror the inbox navigation mode + page size so MailboxContext re-reads live.
  setInboxNavigation(rec.inboxNavMode as InboxNavMode, rec.inboxPageSize)
}

// useDisplaySettings owns the appearance and calendar records (DB-backed, per
// user, no localStorage). Each save writes a WHOLE record, so a record saves only
// after it was read: a save before the read answered, or after it failed, would
// replace the stored values with this page's defaults.
export function useDisplaySettings() {
  const { theme, setTheme } = useTheme()
  const { t, changeLocale } = useI18n()
  const [appearance, setAppearance] = useState<AppearanceRecord>(defaultAppearance)
  const [calendar, setCalendar] = useState<CalendarSettings>(DEFAULT_CALENDAR)
  const [loaded, setLoaded] = useState({ appearance: false, calendar: false })
  const [resetting, setResetting] = useState(false)
  // appearanceSaves numbers the saves, so a failed save restores the record only
  // while no later change has replaced it.
  const appearanceSaves = useRef(0)

  useEffect(() => {
    api.getCalendarSettings()
      .then((c) => {
        setCalendar(calendarFromServer(c))
        setLoaded((prev) => ({ ...prev, calendar: true }))
      })
      .catch(() => undefined)
    api.getAppearanceSettings()
      .then((a) => {
        setAppearance(appearanceFromServer(a))
        setLoaded((prev) => ({ ...prev, appearance: true }))
      })
      .catch(() => undefined)
  }, [])

  // applyAppearance shows a record at once, before the save answers.
  const applyAppearance = (rec: AppearanceRecord) => {
    setAppearance(rec)
    if (rec.language !== "system") changeLocale(rec.language)
    mirrorAppearance(rec)
    // Let app-wide consumers (e.g. the title unread counter) re-read the settings.
    document.dispatchEvent(new CustomEvent("appearance-changed"))
  }

  // saveAppearance stores a change to the appearance record. The theme belongs to
  // useTheme, which every theme control on the page writes, so the record carries
  // its current value rather than the one read at load.
  const saveAppearance = (change: Partial<AppearanceRecord>) => {
    if (!loaded.appearance) {
      toast.error(t("settings.notLoaded"))
      return
    }
    const prev = appearance
    const next = { ...appearance, ...change, theme }
    const seq = ++appearanceSaves.current
    applyAppearance(next)
    api.setAppearanceSettings(next).catch(() => {
      if (seq === appearanceSaves.current) applyAppearance(prev)
      toast.error(t("settings.settingSaveFailed"))
    })
  }

  // saveCalendar stores a change to the calendar record the controls already
  // show. A failed save puts the controls back, so the page never shows a value
  // that was not stored, and reports the failure.
  const saveCalendar = async (change: Partial<CalendarSettings>, keys: CalendarToastKeys) => {
    const prev = calendar
    const next = { ...calendar, ...change }
    setCalendar(next)
    try {
      if (!loaded.calendar) throw new Error(t("settings.notLoaded"))
      await api.setCalendarSettings(next)
      if (keys.saved) toast.success(t(keys.saved))
    } catch (err) {
      setCalendar(prev)
      toast.error(err instanceof Error ? err.message : t(keys.failed))
    }
  }

  // resetSettings drops every webmail2-owned settings key, then reloads both
  // records so the page snaps back to the defaults without a reload.
  const resetSettings = async () => {
    setResetting(true)
    try {
      await api.resetSettings()
      const [a, c] = await Promise.all([api.getAppearanceSettings(), api.getCalendarSettings()])
      const rec = appearanceFromServer(a)
      setAppearance(rec)
      mirrorAppearance(rec)
      setCalendar(calendarFromServer(c))
      setTheme(rec.theme as Theme)
      setLoaded({ appearance: true, calendar: true })
      toast.success(t("settings.reset.settingsReset"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.reset.resetFailed"))
    } finally {
      setResetting(false)
    }
  }

  return { appearance, saveAppearance, calendar, saveCalendar, resetSettings, resetting }
}

export type DisplaySettings = ReturnType<typeof useDisplaySettings>
