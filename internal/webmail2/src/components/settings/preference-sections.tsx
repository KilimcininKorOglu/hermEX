import { Bell, Mail, Shield } from "lucide-react"
import { Fragment } from "react"
import { Separator } from "@/components/ui/separator"
import { useI18n } from "@/hooks/useI18n"
import type { PreferenceKey, Preferences } from "@/hooks/usePreferences"
import { SettingRow, SettingSection } from "./setting-layout"
import { SmimeControls } from "./smime-controls"

// PreferenceRows renders one switch per preference, each named by
// `<prefix>.<name>` and described by `<prefix>.<name>Description`.
function PreferenceRows({ prefs, rows, prefix }: { prefs: Preferences; rows: [PreferenceKey, string][]; prefix: string }) {
  const { t } = useI18n()
  return (
    <>
      {rows.map(([key, name], i) => (
        <Fragment key={key}>
          {i > 0 && <Separator />}
          <SettingRow
            title={t(`${prefix}.${name}`)}
            description={t(`${prefix}.${name}Description`)}
            checked={prefs.prefs[key]}
            onChange={() => void prefs.toggle(key)}
          />
        </Fragment>
      ))}
    </>
  )
}

const NOTIFICATION_ROWS: [PreferenceKey, string][] = [
  ["emailNotifications", "email"],
  ["browserNotifications", "browser"],
  ["soundNotifications", "sound"],
  ["desktopNotifications", "desktop"],
]

const COMPOSITION_ROWS: [PreferenceKey, string][] = [
  ["autoSaveDraft", "autoSaveDrafts"],
  ["richTextMode", "richText"],
  ["autoCorrect", "autoCorrect"],
  ["omitOriginalOnReply", "omitOriginal"],
  ["spellCheck", "spellCheck"],
]

const PRIVACY_ROWS: [PreferenceKey, string][] = [
  ["readReceipts", "readReceipts"],
  ["deliveryReceipts", "deliveryReceipts"],
  ["showOnlineStatus", "showOnlineStatus"],
  ["allowReadReceipts", "allowReadReceipts"],
]

export function NotificationsSection({ prefs }: { prefs: Preferences }) {
  const { t } = useI18n()
  return (
    <SettingSection icon={Bell} title={t("settings.notifications.title")} description={t("settings.notifications.description")}>
      <div className="space-y-1">
        <PreferenceRows prefs={prefs} rows={NOTIFICATION_ROWS} prefix="settings.notifications" />
      </div>
    </SettingSection>
  )
}

export function CompositionSection({ prefs }: { prefs: Preferences }) {
  const { t } = useI18n()
  return (
    <SettingSection icon={Mail} title={t("settings.composition.title")} description={t("settings.composition.description")}>
      <div className="space-y-1">
        <PreferenceRows prefs={prefs} rows={COMPOSITION_ROWS} prefix="settings.composition" />
      </div>
    </SettingSection>
  )
}

export function PrivacySection({ prefs }: { prefs: Preferences }) {
  const { t } = useI18n()
  return (
    <SettingSection icon={Shield} title={t("settings.privacy.title")} description={t("settings.privacy.description")}>
      <div className="space-y-1">
        <PreferenceRows prefs={prefs} rows={PRIVACY_ROWS} prefix="settings.privacy" />
        <Separator />
        <SmimeControls />
      </div>
    </SettingSection>
  )
}
