import { useMemo, useState } from "react"
import { useNavigate } from "react-router-dom"
import { Mail, Check } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useI18n } from "@/hooks/useI18n"
import { useTheme } from "@/components/theme-provider"
import { useAuth } from "@/contexts/AuthContext"
import { detectTimeZone, listTimeZones } from "@/utils/timezone"
import api from "@/utils/api"

// Human-readable names for the supported locales (mirrors the header selector).
const localeNames: Record<string, string> = {
  en: "English",
  tr: "Türkçe",
}

// OnboardingPage is the low-friction first-run step: the timezone is pre-detected
// from the device and the user confirms (or changes) it together with language
// and theme. Everything is also editable later from Settings. Skipping still
// persists the detected/current values and marks the account onboarded so the
// gate does not fire again.
export function OnboardingPage() {
  const navigate = useNavigate()
  const { t, locale, changeLocale, supportedLocales } = useI18n()
  const { setTheme, theme } = useTheme()
  const { user, updatePrefs } = useAuth()

  const zones = useMemo(listTimeZones, [])
  const [timezone, setTimezone] = useState<string>(() => user?.timezone || detectTimeZone())
  const [saving, setSaving] = useState(false)

  const finish = async (chosenTimezone: string) => {
    setSaving(true)
    try {
      await api.updateProfile({
        timezone: chosenTimezone,
        locale,
        theme,
        onboarded: true,
      })
      updatePrefs({ timezone: chosenTimezone, locale, theme, onboarded: true })
      navigate("/inbox", { replace: true })
    } catch {
      toast.error(t("onboarding.saveFailed"))
      setSaving(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-muted/30 p-4">
      <div className="w-full max-w-md rounded-2xl border bg-card p-6 shadow-lg">
        <div className="flex flex-col items-center text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-gradient-to-br from-primary to-primary/80 shadow-lg shadow-primary/25">
            <Mail className="h-6 w-6 text-primary-foreground" />
          </div>
          <h1 className="mt-3 text-xl font-bold">{t("onboarding.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("onboarding.subtitle")}</p>
        </div>

        <div className="mt-6 space-y-5">
          {/* Language */}
          <div className="space-y-2">
            <label className="text-sm font-medium">{t("common.language")}</label>
            <div className="grid grid-cols-2 gap-2">
              {supportedLocales.map((code) => (
                <button
                  key={code}
                  type="button"
                  onClick={() => changeLocale(code)}
                  className={`flex items-center justify-between rounded-lg border px-3 py-2 text-sm transition-colors ${
                    locale === code ? "border-primary bg-primary/10 text-primary" : "hover:bg-accent"
                  }`}
                >
                  <span>{localeNames[code] ?? code.toUpperCase()}</span>
                  {locale === code && <Check className="h-4 w-4" />}
                </button>
              ))}
            </div>
          </div>

          {/* Timezone */}
          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="onboarding-tz">
              {t("onboarding.timezone")}
            </label>
            <select
              id="onboarding-tz"
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
              className="w-full rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
            >
              {zones.map((z) => (
                <option key={z} value={z}>
                  {z}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">{t("onboarding.timezoneHint")}</p>
          </div>

          {/* Theme */}
          <div className="space-y-2">
            <label className="text-sm font-medium">{t("onboarding.theme")}</label>
            <div className="grid grid-cols-3 gap-2">
              {([
                ["light", t("onboarding.themeLight")],
                ["dark", t("onboarding.themeDark")],
                ["system", t("onboarding.themeSystem")],
              ] as const).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  onClick={() => setTheme(value)}
                  className={`rounded-lg border px-3 py-2 text-sm transition-colors ${
                    theme === value ? "border-primary bg-primary/10 text-primary" : "hover:bg-accent"
                  }`}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>
        </div>

        <div className="mt-6 flex items-center justify-between gap-3">
          <Button variant="ghost" onClick={() => void finish(timezone)} disabled={saving}>
            {t("onboarding.skip")}
          </Button>
          <Button onClick={() => void finish(timezone)} disabled={saving}>
            {saving ? t("common.saving") : t("onboarding.continue")}
          </Button>
        </div>
      </div>
    </div>
  )
}
