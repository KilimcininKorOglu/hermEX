import { useMemo } from "react"
import { Globe, Moon, Palette, Sun } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
import { useI18n } from "@/hooks/useI18n"
import type { DisplaySettings } from "@/hooks/useDisplaySettings"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
import { detectTimeZone, listTimeZones } from "@/utils/timezone"
import { CalendarControls } from "./calendar-controls"
import { CheckboxField, FieldRow, SelectField, SettingRow, SettingSection } from "./setting-layout"

type Theme = "light" | "dark" | "system"

const THEME_BUTTONS: { value: Theme; icon: React.ElementType; titleKey: string }[] = [
  { value: "light", icon: Sun, titleKey: "settings.appearance.lightMode" },
  { value: "dark", icon: Moon, titleKey: "settings.appearance.darkMode" },
  { value: "system", icon: Globe, titleKey: "settings.appearance.systemDefault" },
]

// ThemeControls are the controls of the theme provider, which stores the theme
// itself.
function ThemeControls() {
  const { t } = useI18n()
  const { theme, setTheme, resolvedTheme } = useTheme()
  return (
    <>
      <div className="flex items-center justify-between">
        <div>
          <p className="font-medium">{t("settings.appearance.theme")}</p>
          <p className="text-sm text-muted-foreground">{t("settings.appearance.themeDescription")}</p>
        </div>
        <div className="flex gap-2">
          {THEME_BUTTONS.map(({ value, icon: Icon, titleKey }) => (
            <Button
              key={value}
              variant={theme === value ? "default" : "outline"}
              size="icon"
              onClick={() => setTheme(value)}
              title={t(titleKey)}
            >
              <Icon className="h-4 w-4" />
            </Button>
          ))}
        </div>
      </div>
      <Separator />
      <SettingRow
        title={t("settings.appearance.darkMode")}
        description={t("settings.appearance.darkModeDescription")}
        checked={theme === "dark"}
        onChange={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
      />
    </>
  )
}

function TimezoneField({ timezone, onChange }: { timezone: string; onChange: (tz: string) => void }) {
  const { t } = useI18n()
  const timeZones = useMemo(listTimeZones, [])
  const options = useMemo(
    () => [
      { value: "", label: t("settings.appearance.timezoneAuto", { zone: detectTimeZone() }) },
      ...timeZones.map((z) => ({ value: z, label: z })),
    ],
    [t, timeZones],
  )
  return (
    <SelectField
      title={t("settings.appearance.timezone")}
      description={t("settings.appearance.timezoneDescription")}
      value={timezone}
      options={options}
      onChange={onChange}
    />
  )
}

// DisplayFormatFields are the language and the date, time and name formats.
function DisplayFormatFields({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { theme, setTheme } = useTheme()
  const { appearance, saveAppearance } = display
  return (
    <>
      <SelectField
        title={t("settings.appearance.theme")}
        description={t("settings.appearance.themeDescription")}
        value={theme}
        options={[
          { value: "system", label: t("settings.appearance.themeSystem") },
          { value: "light", label: t("settings.appearance.themeLight") },
          { value: "dark", label: t("settings.appearance.themeDark") },
        ]}
        onChange={(v) => setTheme(v as Theme)}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.language")}
        description={t("settings.appearance.languageDescription")}
        value={appearance.language}
        options={[
          { value: "system", label: t("settings.appearance.languageSystem") },
          { value: "en", label: "English" },
          { value: "tr", label: "Türkçe" },
        ]}
        onChange={(language) => saveAppearance({ language })}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.dateFormat")}
        description={t("settings.appearance.dateFormatDescription")}
        value={appearance.dateFormat}
        options={[
          { value: "iso", label: "YYYY-MM-DD" },
          { value: "dmy", label: "DD/MM/YYYY" },
          { value: "mdy", label: "MM/DD/YYYY" },
        ]}
        onChange={(dateFormat) => saveAppearance({ dateFormat })}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.timeFormat")}
        description={t("settings.appearance.timeFormatDescription")}
        value={appearance.timeFormat}
        options={[
          { value: "24", label: "24h" },
          { value: "12", label: "12h" },
        ]}
        onChange={(timeFormat) => saveAppearance({ timeFormat })}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.nameDisplay")}
        description={t("settings.appearance.nameDisplayDescription")}
        value={appearance.nameDisplay}
        options={[
          { value: "firstlast", label: t("settings.appearance.nameFirstLast") },
          { value: "lastfirst", label: t("settings.appearance.nameLastFirst") },
        ]}
        onChange={(nameDisplay) => saveAppearance({ nameDisplay })}
      />
    </>
  )
}

// LayoutFields are the icon set, the list and panel toggles, the startup folder
// and the automatic Cc.
function LayoutFields({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { appearance, saveAppearance } = display
  return (
    <>
      <SelectField
        title={t("settings.appearance.iconSet")}
        description={t("settings.appearance.iconSetDescription")}
        value={appearance.iconSet}
        options={[
          { value: "breeze", label: t("settings.appearance.iconSetBreeze") },
          { value: "classic", label: t("settings.appearance.iconSetClassic") },
        ]}
        onChange={(iconSet) => saveAppearance({ iconSet })}
      />
      <Separator />
      <CheckboxField
        title={t("settings.appearance.showUnreadCounter")}
        description={t("settings.appearance.showUnreadCounterDescription")}
        checked={appearance.showUnreadCounter}
        onChange={(showUnreadCounter) => saveAppearance({ showUnreadCounter })}
      />
      <Separator />
      <CheckboxField
        title={t("settings.appearance.unreadBorder")}
        description={t("settings.appearance.unreadBorderDescription")}
        checked={appearance.unreadBorder}
        onChange={(unreadBorder) => saveAppearance({ unreadBorder })}
      />
      <Separator />
      <CheckboxField
        title={t("settings.appearance.hideWidgetPanel")}
        description={t("settings.appearance.hideWidgetPanelDescription")}
        checked={appearance.hideWidgetPanel}
        onChange={(hideWidgetPanel) => saveAppearance({ hideWidgetPanel })}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.startupFolder")}
        description={t("settings.appearance.startupFolderDescription")}
        value={appearance.startupFolder}
        options={[
          { value: "", label: t("settings.appearance.startupInbox") },
          { value: "today", label: t("nav.today") },
          { value: "calendar", label: t("nav.calendar") },
          { value: "contacts", label: t("nav.contacts") },
          { value: "tasks", label: t("nav.tasks") },
        ]}
        onChange={(startupFolder) => saveAppearance({ startupFolder })}
      />
      <Separator />
      <FieldRow title={t("settings.appearance.autoCc")} description={t("settings.appearance.autoCcDescription")}>
        <Input
          value={appearance.autoCc}
          onChange={(e) => saveAppearance({ autoCc: e.target.value })}
          placeholder="cc@example.test, boss@example.test"
          className="max-w-[16rem]"
        />
      </FieldRow>
    </>
  )
}

export function AppearanceSection({
  display,
  timezone,
  onTimezoneChange,
}: {
  display: DisplaySettings
  timezone: string
  onTimezoneChange: (tz: string) => void
}) {
  const { t } = useI18n()
  return (
    <SettingSection icon={Palette} title={t("settings.appearance.title")} description={t("settings.appearance.description")}>
      <div className="space-y-4">
        <ThemeControls />
        <Separator />
        <TimezoneField timezone={timezone} onChange={onTimezoneChange} />
        <Separator />
        <DisplayFormatFields display={display} />
        <Separator />
        <LayoutFields display={display} />
        <Separator />
        <CalendarControls display={display} />
      </div>
    </SettingSection>
  )
}
